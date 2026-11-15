package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A Client is one way of talking to the gateway: the bare HTTP client, or an
// official MCP SDK. Tests that must hold for every client are written
// against this interface and run once per entry in Clients.

// ClientRequest is one request made through a client.
type ClientRequest struct {
	Method string
	// Params is the request's params object. It may hold _meta.
	Params map[string]any
	// RawParams, when set, is the params exactly as they must go on the wire:
	// any JSON value, with duplicate keys and number spellings kept. Only
	// the raw client sends it; an SDK builds its request from Params, and
	// cannot make a request whose RawParams is not an object.
	RawParams json.RawMessage
	// Capabilities is what the client declares. Nil declares nothing.
	Capabilities any
	// ClientInfo is the client's declared identity. Nil or empty keeps the
	// client's own.
	ClientInfo map[string]any
	// Progress asks for progress events.
	Progress bool
	// Elicit holds scripted answers to elicitation requests, in order. Each
	// is {"action": ..., "content": ...}. A question with no answer left
	// stops the call there.
	Elicit []map[string]any
	// Token is sent as a bearer token.
	Token string
}

// ClientError is a JSON-RPC error as the client saw it.
type ClientError struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// ClientReply is what the client saw.
type ClientReply struct {
	// Result is the result object, with numbers kept as written.
	Result any
	// Error is a JSON-RPC error.
	Error *ClientError
	// Failure is a failure that was not a JSON-RPC error: the transport, or
	// the client itself refusing to send or to decode something.
	Failure string
	// Progress is the progress events, in the order the client got them.
	Progress []map[string]any
	// Elicitations is every question the client's handler was asked.
	Elicitations []map[string]any
	// NotConnected is set when an SDK could not open its session, so the
	// request itself was never made.
	NotConnected bool
	// Refused is set when an SDK declined to send the request at all, with
	// its reason.
	Refused string
	// Unanswered is set when the client was asked a question that no
	// scripted answer covered. For an SDK the call stops there.
	Unanswered bool
	// SDK names the client library: name, version, protocolVersion.
	SDK map[string]any
	// Wire is the raw HTTP reply. Only the raw client has one.
	Wire *RawReply
}

// Client is one way of making requests.
type Client interface {
	Name() string
	// Can reports why this client cannot make the request, or "".
	Can(req ClientRequest) string
	// Do connects to the endpoint, makes the request and disconnects.
	Do(ctx context.Context, endpoint string, req ClientRequest) (*ClientReply, error)
}

// Clients is every client the cross-client tests run with. The Python and
// TypeScript SDK drivers of the clients package are added to it by
// drivers_test.go, the one place a clients.Driver is adapted to Client.
var Clients = []Client{RawHTTP{}, GoSDK{}}

// SkippedClients names every client that could not be started, with the
// reason. Tests that run once per client report these by name.
var SkippedClients []string

// SDKShape reports why a request cannot be made through a typed SDK call,
// or "". It is the same for every SDK: the method must have a call, and the
// params must fit that call's fields.
func SDKShape(req ClientRequest) string { return sdkShape(req) }

// ---- the raw HTTP client --------------------------------------------------------------

// RawHTTP is the bare HTTP client. It can send anything.
type RawHTTP struct{}

func (RawHTTP) Name() string             { return "raw" }
func (RawHTTP) Can(ClientRequest) string { return "" }

func (RawHTTP) Do(ctx context.Context, endpoint string, req ClientRequest) (*ClientReply, error) {
	out := &ClientReply{SDK: map[string]any{"name": "raw-http", "protocolVersion": V2026}}
	// The params go out as written. Only _meta is touched, to declare the
	// client's identity and capabilities, and each top-level value keeps its
	// own bytes.
	top := map[string]json.RawMessage{}
	verbatim := false // params that are not an object are sent untouched
	switch {
	case len(req.RawParams) > 0:
		if json.Unmarshal(req.RawParams, &top) != nil || top == nil {
			verbatim = true
		}
	default:
		for k, v := range req.Params {
			top[k], _ = json.Marshal(v)
		}
	}
	meta := map[string]any{}
	if raw, ok := top["_meta"]; ok {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if dec.Decode(&meta) != nil || meta == nil {
			verbatim = true // a _meta that is not an object is sent as it is
		}
	}
	if len(req.ClientInfo) > 0 {
		if _, has := meta[metaClient]; !has {
			meta[metaClient] = req.ClientInfo
		}
	}
	if req.Capabilities != nil {
		if _, has := meta[metaCaps]; !has {
			meta[metaCaps] = req.Capabilities
		}
	}
	if req.Progress {
		meta["progressToken"] = "raw-progress"
	}
	answers := req.Elicit
	for round := 0; round < 10; round++ {
		rr := RawRequest{Method: req.Method, Token: req.Token}
		switch {
		case verbatim:
			rr.RawParams = req.RawParams
		case len(top) == 0 && len(meta) == 0 && len(req.RawParams) == 0 && req.Params == nil:
			// no params at all
		default:
			if len(meta) > 0 {
				top["_meta"], _ = json.Marshal(meta)
			}
			rr.RawParams, _ = json.Marshal(top)
		}
		rep, err := RawDo(ctx, endpoint, rr)
		if err != nil {
			return nil, err
		}
		out.Wire = rep
		for _, m := range rep.Messages() {
			if m["method"] == "notifications/progress" {
				p, _ := m["params"].(map[string]any)
				out.Progress = append(out.Progress, p)
			}
		}
		if !rep.Stream() {
			out.Failure = fmt.Sprintf("HTTP %d: %s", rep.Status, bytes.TrimSpace(rep.Body))
			return out, nil
		}
		final := rep.Final()
		if final == nil {
			out.Failure = "the stream ended without a response"
			return out, nil
		}
		if e, ok := final["error"].(map[string]any); ok {
			ce := &ClientError{Data: e["data"]}
			ce.Message, _ = e["message"].(string)
			if n, ok := e["code"].(json.Number); ok {
				ce.Code, _ = n.Int64()
			}
			out.Error = ce
			return out, nil
		}
		out.Result = final["result"]
		res, _ := final["result"].(map[string]any)
		if res["resultType"] != "input_required" || req.Elicit == nil {
			return out, nil
		}
		// Answer the questions as an SDK would, from the script.
		reqs, _ := res["inputRequests"].(map[string]any)
		keys := make([]string, 0, len(reqs))
		for k := range reqs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		responses := map[string]any{}
		for _, k := range keys {
			q, _ := reqs[k].(map[string]any)
			qp, _ := q["params"].(map[string]any)
			out.Elicitations = append(out.Elicitations, qp)
			if len(answers) == 0 {
				out.Unanswered = true
				return out, nil
			}
			responses[k] = answers[0]
			answers = answers[1:]
		}
		top["inputResponses"], _ = json.Marshal(responses)
		top["requestState"], _ = json.Marshal(res["requestState"])
	}
	out.Failure = "too many input rounds"
	return out, nil
}

// ---- the Go SDK client ----------------------------------------------------------------

// GoSDK is the official Go SDK client.
type GoSDK struct{}

func (GoSDK) Name() string { return "go-sdk" }

// sdkMethods are the requests an SDK has a call for.
var sdkMethods = map[string]bool{
	"tools/list": true, "tools/call": true, "prompts/list": true, "prompts/get": true,
	"resources/list": true, "resources/templates/list": true, "resources/read": true,
}

// sdkShape reports why a request's params cannot be passed through a typed
// SDK call, or "". It is the same for every SDK.
func sdkShape(req ClientRequest) string {
	if !sdkMethods[req.Method] {
		return "no SDK call for " + req.Method
	}
	if len(req.RawParams) > 0 && req.Params == nil {
		return "params that are not an object"
	}
	allowed := map[string][]string{
		"tools/call": {"name", "arguments", "_meta"}, "prompts/get": {"name", "arguments", "_meta"}, "resources/read": {"uri", "_meta"},
		"tools/list": {"_meta"}, "prompts/list": {"_meta"}, "resources/list": {"_meta"}, "resources/templates/list": {"_meta"},
	}[req.Method]
	for k := range req.Params {
		ok := false
		for _, a := range allowed {
			ok = ok || a == k
		}
		if !ok {
			if k == "requestState" || k == "inputResponses" {
				return "a hand-built requestState or inputResponses"
			}
			return "a params field the SDK call does not have: " + k
		}
	}
	if m, has := req.Params["_meta"]; has {
		if _, ok := m.(map[string]any); !ok {
			return "a _meta that is not an object"
		}
	}
	switch req.Method {
	case "tools/call", "prompts/get":
		if _, ok := req.Params["name"].(string); !ok {
			return "a name that is not a string"
		}
		if a, has := req.Params["arguments"]; has {
			m, ok := a.(map[string]any)
			if !ok {
				return "arguments that are not an object"
			}
			if req.Method == "prompts/get" {
				for _, v := range m {
					if _, ok := v.(string); !ok {
						return "a prompt argument that is not a string"
					}
				}
			}
		}
	case "resources/read":
		if _, ok := req.Params["uri"].(string); !ok {
			return "a uri that is not a string"
		}
	}
	return ""
}

func (GoSDK) Can(req ClientRequest) string {
	if why := sdkShape(req); why != "" {
		return why
	}
	if _, why := goCapabilities(req.Capabilities); why != "" {
		return why
	}
	for k, v := range req.ClientInfo {
		if _, isString := v.(string); !isString || (k != "name" && k != "version" && k != "title") {
			return "a client identity the SDK cannot declare"
		}
	}
	return ""
}

// goCapabilities turns declared capabilities into the SDK's type. It fails
// when the SDK's type cannot carry them.
func goCapabilities(declared any) (*mcp.ClientCapabilities, string) {
	caps := &mcp.ClientCapabilities{}
	if declared == nil {
		return caps, ""
	}
	want, ok := declared.(map[string]any)
	if !ok {
		return nil, "capabilities that are not an object"
	}
	b, _ := json.Marshal(want)
	if err := json.Unmarshal(b, caps); err != nil {
		return nil, "capabilities the SDK cannot declare"
	}
	back, _ := json.Marshal(caps)
	var got map[string]any
	_ = json.Unmarshal(back, &got)
	for k, v := range want {
		if asJSONText(got[k]) != asJSONText(v) {
			return nil, "capabilities the SDK cannot declare: " + k
		}
	}
	return caps, ""
}

func asJSONText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

var errUnanswered = errors.New("harness: no scripted answer for this question")

func (GoSDK) Do(ctx context.Context, endpoint string, req ClientRequest) (*ClientReply, error) {
	out := &ClientReply{SDK: map[string]any{"name": "go-sdk", "version": "v1.8.0"}}
	caps, why := goCapabilities(req.Capabilities)
	if why != "" {
		return nil, errors.New(why)
	}
	var mu sync.Mutex
	answers := req.Elicit
	opts := &mcp.ClientOptions{Capabilities: caps}
	if caps.Elicitation != nil {
		opts.ElicitationHandler = func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			mu.Lock()
			defer mu.Unlock()
			asked := map[string]any{}
			b, _ := json.Marshal(r.Params)
			_ = json.Unmarshal(b, &asked)
			out.Elicitations = append(out.Elicitations, asked)
			if len(answers) == 0 {
				out.Unanswered = true
				return nil, errUnanswered
			}
			a := answers[0]
			answers = answers[1:]
			res := &mcp.ElicitResult{}
			res.Action, _ = a["action"].(string)
			res.Content, _ = a["content"].(map[string]any)
			return res, nil
		}
	}
	if req.Progress {
		opts.ProgressNotificationHandler = func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			p := map[string]any{}
			b, _ := json.Marshal(r.Params)
			_ = json.Unmarshal(b, &p)
			mu.Lock()
			out.Progress = append(out.Progress, p)
			mu.Unlock()
		}
	}
	impl := &mcp.Implementation{Name: "harness-go-sdk", Version: "1.0.0"}
	if v, ok := req.ClientInfo["name"].(string); ok {
		impl.Name = v
	}
	if v, ok := req.ClientInfo["version"].(string); ok {
		impl.Version = v
	}
	if v, ok := req.ClientInfo["title"].(string); ok {
		impl.Title = v
	}
	c := mcp.NewClient(impl, opts)
	tr := &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           &http.Client{Transport: bearer{token: req.Token, next: http.DefaultTransport}},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}
	fail := func(err error) {
		var je *jsonrpc.Error
		if errors.As(err, &je) {
			ce := &ClientError{Code: je.Code, Message: je.Message}
			if len(je.Data) > 0 {
				_ = json.Unmarshal(je.Data, &ce.Data)
			}
			out.Error = ce
			return
		}
		out.Failure = err.Error()
	}
	cs, err := c.Connect(ctx, tr, nil)
	if err != nil {
		fail(err)
		if out.Failure == "" {
			out.Failure = "connect: " + err.Error()
		}
		out.NotConnected = true
		return out, nil
	}
	defer cs.Close()
	out.SDK["protocolVersion"] = cs.InitializeResult().ProtocolVersion

	var meta mcp.Meta
	if m, ok := req.Params["_meta"].(map[string]any); ok {
		meta = mcp.Meta(m)
	}
	var res any
	switch req.Method {
	case "tools/list":
		res, err = cs.ListTools(ctx, &mcp.ListToolsParams{Meta: meta})
	case "prompts/list":
		res, err = cs.ListPrompts(ctx, &mcp.ListPromptsParams{Meta: meta})
	case "resources/list":
		res, err = cs.ListResources(ctx, &mcp.ListResourcesParams{Meta: meta})
	case "resources/templates/list":
		res, err = cs.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{Meta: meta})
	case "tools/call":
		p := &mcp.CallToolParams{Meta: meta}
		p.Name, _ = req.Params["name"].(string)
		if a, ok := req.Params["arguments"]; ok {
			p.Arguments = a
		}
		if req.Progress {
			p.SetProgressToken("go-progress")
		}
		res, err = cs.CallTool(ctx, p)
	case "prompts/get":
		p := &mcp.GetPromptParams{Meta: meta}
		p.Name, _ = req.Params["name"].(string)
		if a, ok := req.Params["arguments"].(map[string]any); ok {
			p.Arguments = map[string]string{}
			for k, v := range a {
				p.Arguments[k], _ = v.(string)
			}
		}
		if req.Progress {
			p.SetProgressToken("go-progress")
		}
		res, err = cs.GetPrompt(ctx, p)
	case "resources/read":
		p := &mcp.ReadResourceParams{Meta: meta}
		p.URI, _ = req.Params["uri"].(string)
		if req.Progress {
			p.SetProgressToken("go-progress")
		}
		res, err = cs.ReadResource(ctx, p)
	default:
		return nil, fmt.Errorf("no SDK call for %s", req.Method)
	}
	if req.Progress {
		// Progress reaches the handler on the SDK's own goroutine and can
		// trail the result; give it a moment to settle.
		settle := func() int { mu.Lock(); defer mu.Unlock(); return len(out.Progress) }
		for n, deadline := settle(), time.Now().Add(100*time.Millisecond); time.Now().Before(deadline); {
			time.Sleep(5 * time.Millisecond)
			if m := settle(); m == n {
				break
			} else {
				n = m
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if err != nil {
		if !out.Unanswered {
			fail(err)
		}
		return out, nil
	}
	b, merr := json.Marshal(res)
	if merr != nil {
		out.Failure = "encoding the SDK's result: " + merr.Error()
		return out, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	_ = dec.Decode(&out.Result)
	return out, nil
}

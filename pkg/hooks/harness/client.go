package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ClientOptions shapes the SDK client a test connects with.
type ClientOptions struct {
	// Elicit answers elicitation requests. When nil, the client declares no
	// elicitation capability.
	Elicit func(*mcp.ElicitRequest) (*mcp.ElicitResult, error)
	// Progress receives progress notifications.
	Progress func(*mcp.ProgressNotificationParams)
	// Logs receives log notifications.
	Logs func(*mcp.LoggingMessageParams)
	// Token is sent as a bearer token.
	Token string
	// ManualInput turns off the SDK's automatic handling of input_required,
	// so a test sees those results and retries by hand.
	ManualInput bool
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	if b.token != "" {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.next.RoundTrip(r)
}

// Connect opens a session of the official Go SDK client against a gateway
// endpoint. The session speaks 2026-07-28; the test fails if it negotiates
// anything else. It is closed when the test ends.
func Connect(t testing.TB, endpoint string, o ClientOptions) *mcp.ClientSession {
	t.Helper()
	cs, err := TryConnect(t, endpoint, o)
	if err != nil {
		t.Fatalf("harness: connect to %s: %v", endpoint, err)
	}
	if v := cs.InitializeResult().ProtocolVersion; v != V2026 {
		t.Fatalf("harness: the SDK client negotiated %q, want %s", v, V2026)
	}
	return cs
}

// TryConnect is Connect that returns the error.
func TryConnect(t testing.TB, endpoint string, o ClientOptions) (*mcp.ClientSession, error) {
	opts := &mcp.ClientOptions{}
	if o.Elicit != nil {
		opts.ElicitationHandler = func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) { return o.Elicit(r) }
	}
	if o.Progress != nil {
		opts.ProgressNotificationHandler = func(_ context.Context, r *mcp.ProgressNotificationClientRequest) { o.Progress(r.Params) }
	}
	if o.Logs != nil {
		opts.LoggingMessageHandler = func(_ context.Context, r *mcp.LoggingMessageRequest) { o.Logs(r.Params) }
	}
	if o.ManualInput {
		opts.MultiRoundTrip = &mcp.MultiRoundTripOptions{Disabled: true}
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "harness-client", Version: "1.0.0"}, opts)
	tr := &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           &http.Client{Transport: bearer{token: o.Token, next: http.DefaultTransport}},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cs, err := c.Connect(ctx, tr, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, nil
}

// TextOf joins the text blocks of a tool result.
func TextOf(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// ---- raw client -----------------------------------------------------------------------

// Frame is one thing read from an SSE reply: a comment line or a message.
type Frame struct {
	Comment bool
	Msg     map[string]any
	At      time.Duration // since the request was sent
}

// Method is the frame's JSON-RPC method, empty for a response or a comment.
func (f Frame) Method() string {
	s, _ := f.Msg["method"].(string)
	return s
}

// RawReply is an HTTP reply as the wire showed it.
type RawReply struct {
	Status int
	Header http.Header
	// Frames are the SSE comments and messages in arrival order. Empty when
	// the reply was not a stream.
	Frames []Frame
	// Body is the body of a reply that was not a stream.
	Body []byte
	// ReadErr is how reading the stream ended, nil for a clean end.
	ReadErr error
}

// Stream reports whether the reply was an SSE stream.
func (r *RawReply) Stream() bool {
	return strings.HasPrefix(r.Header.Get("Content-Type"), "text/event-stream")
}

// Messages returns the JSON-RPC messages, without the comments.
func (r *RawReply) Messages() []map[string]any {
	var out []map[string]any
	for _, f := range r.Frames {
		if !f.Comment {
			out = append(out, f.Msg)
		}
	}
	return out
}

// Comments counts the comment lines.
func (r *RawReply) Comments() int {
	n := 0
	for _, f := range r.Frames {
		if f.Comment {
			n++
		}
	}
	return n
}

// Final returns the response message, nil when the stream ended without one.
func (r *RawReply) Final() map[string]any {
	for _, m := range r.Messages() {
		if _, isNote := m["method"]; !isNote {
			return m
		}
	}
	// A reply that is not a stream is one JSON body. An SDK server answers
	// some errors that way, with an HTTP error status.
	if !r.Stream() && len(r.Body) > 0 {
		var m map[string]any
		dec := json.NewDecoder(bytes.NewReader(r.Body))
		dec.UseNumber()
		if dec.Decode(&m) == nil {
			if _, isNote := m["method"]; !isNote && m["jsonrpc"] != nil {
				return m
			}
		}
	}
	return nil
}

// Result is the final response's result object.
func (r *RawReply) Result() map[string]any {
	m, _ := r.Final()["result"].(map[string]any)
	return m
}

// Error is the final response's error object.
func (r *RawReply) Error() map[string]any {
	m, _ := r.Final()["error"].(map[string]any)
	return m
}

// RawRequest describes a request for the raw client.
type RawRequest struct {
	Method string
	Params map[string]any
	// Capabilities are declared in the request's _meta, as a 2026-07-28
	// client does on every request.
	Capabilities map[string]any
	// ProgressToken asks for progress.
	ProgressToken any
	Token         string
	// Meta, when ExactMeta is set, is the request's whole _meta: nothing is
	// added, and an empty Meta sends no _meta at all.
	Meta      map[string]any
	ExactMeta bool
	// RawParams, when set, is sent verbatim as the params value.
	RawParams json.RawMessage
	// Body, when set, is sent verbatim instead of a JSON-RPC envelope.
	Body string
	// OnFrame is called for each frame as it arrives. Returning false stops
	// reading and closes the connection, which is how a client cancels.
	OnFrame func(Frame) bool
}

var rawMu sync.Mutex
var rawSeq int

// Raw sends one request with a bare HTTP client and reads the reply to its
// end. It shows what the SDK hides: the status, the headers and every SSE
// frame in order.
func Raw(t testing.TB, endpoint string, rr RawRequest) *RawReply {
	t.Helper()
	rep, err := RawDo(context.Background(), endpoint, rr)
	if err != nil {
		t.Fatalf("harness: raw request to %s: %v", endpoint, err)
	}
	return rep
}

// RawDo is Raw with a context and an error.
func RawDo(ctx context.Context, endpoint string, rr RawRequest) (*RawReply, error) {
	body := []byte(rr.Body)
	if rr.Body == "" {
		rawMu.Lock()
		rawSeq++
		id := rawSeq
		rawMu.Unlock()
		env := map[string]any{"jsonrpc": "2.0", "id": id, "method": rr.Method}
		switch {
		case len(rr.RawParams) > 0:
			env["params"] = rr.RawParams
		default:
			params := map[string]any{}
			for k, v := range rr.Params {
				params[k] = v
			}
			var meta map[string]any
			if rr.ExactMeta {
				meta = map[string]any{}
				for k, v := range rr.Meta {
					meta[k] = v
				}
			} else {
				meta = map[string]any{metaVersion: V2026, metaClient: map[string]any{"name": "raw-client", "version": "1.0.0"}, metaCaps: map[string]any{}}
				if rr.Capabilities != nil {
					meta[metaCaps] = rr.Capabilities
				}
			}
			if rr.ProgressToken != nil {
				meta["progressToken"] = rr.ProgressToken
			}
			if len(meta) > 0 {
				params["_meta"] = meta
			} else {
				delete(params, "_meta")
			}
			if len(params) > 0 || rr.Params != nil {
				env["params"] = params
			}
		}
		body, _ = json.Marshal(env)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json, text/event-stream")
	hr.Header.Set("Mcp-Protocol-Version", V2026)
	if rr.Method != "" {
		hr.Header.Set("Mcp-Method", rr.Method)
	}
	// The name a request is about travels in a header as well, as the
	// protocol requires. An SDK server refuses a request without it.
	switch rr.Method {
	case "tools/call", "prompts/get":
		if name, ok := rr.Params["name"].(string); ok && name != "" {
			hr.Header.Set("Mcp-Name", name)
		}
	case "resources/read", "resources/directory/read":
		if uri, ok := rr.Params["uri"].(string); ok && uri != "" {
			hr.Header.Set("Mcp-Name", uri)
		}
	}
	if rr.Token != "" {
		hr.Header.Set("Authorization", "Bearer "+rr.Token)
	}
	start := time.Now()
	// A transport of its own, so closing the connection really closes it.
	tr := &http.Transport{DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	resp, err := (&http.Client{Transport: tr}).Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rep := &RawReply{Status: resp.StatusCode, Header: resp.Header.Clone()}
	if !rep.Stream() {
		rep.Body, rep.ReadErr = io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		return rep, nil
	}
	rd := bufio.NewReaderSize(resp.Body, 64<<10)
	var data []byte
	emit := func(f Frame) bool {
		f.At = time.Since(start)
		rep.Frames = append(rep.Frames, f)
		return rr.OnFrame == nil || rr.OnFrame(f)
	}
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			if !errors.Is(err, io.EOF) {
				rep.ReadErr = err
			}
			return rep, nil
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, ":"):
			if !emit(Frame{Comment: true}) {
				return rep, nil
			}
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")...)
		case line == "" && len(data) > 0:
			var m map[string]any
			dec := json.NewDecoder(bytes.NewReader(data))
			dec.UseNumber() // numbers stay as the wire wrote them
			if err := dec.Decode(&m); err != nil {
				rep.ReadErr = err
				return rep, nil
			}
			data = nil
			if !emit(Frame{Msg: m}) {
				return rep, nil
			}
		}
	}
}

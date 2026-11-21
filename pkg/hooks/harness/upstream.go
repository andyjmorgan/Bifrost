package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Protocol versions the mock can speak.
const (
	V2026 = "2026-07-28"
	V2025 = "2025-11-25"
	V2024 = "2025-06-18"
	// VNone is a server that completes the old handshake but reports no
	// protocol version at all.
	VNone = "none"
)

// Recorded is one request the upstream received.
type Recorded struct {
	// Seq orders requests across every upstream in the test binary.
	Seq    int64
	Method string
	ID     json.RawMessage
	Params json.RawMessage
	Header http.Header
}

// Map decodes the params object.
func (r Recorded) Map() map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(r.Params, &m)
	return m
}

// Exact decodes the params object with numbers kept as written.
func (r Recorded) Exact() map[string]any {
	m := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(r.Params))
	dec.UseNumber()
	_ = dec.Decode(&m)
	return m
}

var recordedSeq atomic.Int64

// Meta is the request's _meta object, empty when absent.
func (r Recorded) Meta() map[string]any {
	m, _ := r.Map()["_meta"].(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

// Args is the request's arguments object, empty when absent.
func (r Recorded) Args() map[string]any {
	m, _ := r.Map()["arguments"].(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

// ProgressToken is the request's progress token, nil when absent.
func (r Recorded) ProgressToken() any { return r.Meta()["progressToken"] }

// Capabilities is what the caller declared on this request: the per-request
// clientCapabilities for 2026-07-28, or the capabilities of an initialize.
func (r Recorded) Capabilities() map[string]any {
	if r.Method == "initialize" {
		m, _ := r.Map()["capabilities"].(map[string]any)
		return m
	}
	m, _ := r.Meta()["io.modelcontextprotocol/clientCapabilities"].(map[string]any)
	return m
}

// Progress is one progress event a script sends.
type Progress struct {
	Progress float64
	Total    float64
	Message  string
}

// Note is any other notification a script sends.
type Note struct {
	Method string
	Params any
}

// RPCErr is a JSON-RPC error a script returns.
type RPCErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Reply scripts how the upstream answers one request. The zero value is an
// empty result in one JSON body.
type Reply struct {
	// Status, when set to anything but 200, answers with that plain HTTP
	// status and Body, and nothing else.
	Status int
	Body   string
	// ErrorStatus answers Error with this HTTP status instead of 200: as
	// one JSON body, or as an SSE event when Stream is set. A server built
	// on the C# SDK refuses a request this way.
	ErrorStatus int
	// Stream answers with an SSE stream instead of one JSON body.
	Stream bool
	// Delay waits before the first byte. Gap waits between events.
	Delay time.Duration
	Gap   time.Duration
	// Progress events are sent first, under the request's progress token.
	// With no token they are skipped, unless ForceProgress is set; then
	// they go out under Token, or "unasked".
	Progress      []Progress
	ForceProgress bool
	Token         any
	// Notes are sent after the progress events.
	Notes []Note
	// Result is the JSON-RPC result. RawResult, when set, is used verbatim
	// instead and need not be valid JSON.
	Result    any
	RawResult string
	// RawBody replaces the whole JSON-RPC envelope with these bytes.
	RawBody string
	Error   *RPCErr
	// NoFinal ends a stream without a response.
	NoFinal bool
	// Hang keeps the request open until the caller goes away.
	Hang bool
}

// Handler computes the reply to one request.
type Handler func(Recorded) Reply

// Text is a tool result with one text block.
func Text(s string) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": s}}}
}

// Contents is a resources/read result with one text item.
func Contents(uri, text string) map[string]any {
	return map[string]any{"contents": []any{map[string]any{"uri": uri, "text": text}}}
}

// Static always answers with the same reply.
func Static(r Reply) Handler { return func(Recorded) Reply { return r } }

// Upstream is a scriptable mock MCP server.
type Upstream struct {
	// Version is the protocol version the server speaks: V2026, an older
	// one, or VNone.
	Version      string
	Name         string
	Instructions string

	srv *httptest.Server

	mu          sync.Mutex
	tools       []map[string]any
	toolH       map[string]Handler
	prompts     []map[string]any
	promptH     map[string]Handler
	resources   []map[string]any
	templates   []map[string]any
	readH       map[string]Handler
	readDefault Handler
	fallback    Handler
	rawLists    map[string]json.RawMessage
	reqs        []Recorded

	cancelled atomic.Int64
}

// NewUpstream starts a mock server speaking the given version. It stops
// when the test ends.
func NewUpstream(t testing.TB, version string) *Upstream {
	u := &Upstream{Version: version, Name: "mock", toolH: map[string]Handler{}, promptH: map[string]Handler{}, readH: map[string]Handler{}}
	u.srv = httptest.NewServer(http.HandlerFunc(u.serve))
	t.Cleanup(u.srv.Close)
	return u
}

// URL is the server's endpoint.
func (u *Upstream) URL() string { return u.srv.URL }

// Tool adds a tool from its JSON definition and the handler that answers
// calls to it. A nil handler answers with the text "ok".
func (u *Upstream) Tool(def string, h Handler) {
	var m map[string]any
	if err := json.Unmarshal([]byte(def), &m); err != nil {
		panic("harness: bad tool definition: " + err.Error())
	}
	name, _ := m["name"].(string)
	if _, ok := m["inputSchema"]; !ok {
		m["inputSchema"] = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.tools = append(u.tools, m)
	if h != nil {
		u.toolH[name] = h
	}
}

// Resource adds a listed resource and the handler that answers reads of it.
// A nil handler returns the URI as text.
func (u *Upstream) Resource(uri string, h Handler) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.resources = append(u.resources, map[string]any{"uri": uri, "name": uri})
	if h == nil {
		h = Static(Reply{Result: Contents(uri, "content of "+uri)})
	}
	u.readH[uri] = h
}

// Unlisted makes a URI readable without listing it.
func (u *Upstream) Unlisted(uri string, h Handler) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if h == nil {
		h = Static(Reply{Result: Contents(uri, "content of "+uri)})
	}
	u.readH[uri] = h
}

// Template adds a listed resource template.
func (u *Upstream) Template(uriTemplate string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.templates = append(u.templates, map[string]any{"uriTemplate": uriTemplate, "name": uriTemplate})
}

// ReadDefault answers reads of any URI without its own handler. Without
// one, such reads get a "not found" error.
func (u *Upstream) ReadDefault(h Handler) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.readDefault = h
}

// Prompt adds a prompt from its JSON definition. A nil handler returns one
// user message.
func (u *Upstream) Prompt(def string, h Handler) {
	var m map[string]any
	if err := json.Unmarshal([]byte(def), &m); err != nil {
		panic("harness: bad prompt definition: " + err.Error())
	}
	name, _ := m["name"].(string)
	u.mu.Lock()
	defer u.mu.Unlock()
	u.prompts = append(u.prompts, m)
	if h != nil {
		u.promptH[name] = h
	}
}

// Default answers any call, read or prompt that has no handler of its own.
func (u *Upstream) Default(h Handler) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.fallback = h
}

// Lists serves the given JSON arrays verbatim as the server's listings. The
// keys are tools, prompts, resources and resourceTemplates.
func (u *Upstream) Lists(raw map[string]json.RawMessage) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.rawLists = raw
}

// Requests returns the recorded requests for a method, oldest first. An
// empty method returns all of them.
func (u *Upstream) Requests(method string) []Recorded {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []Recorded
	for _, r := range u.reqs {
		if method == "" || r.Method == method {
			out = append(out, r)
		}
	}
	return out
}

// Last returns the most recent request for a method.
func (u *Upstream) Last(t testing.TB, method string) Recorded {
	t.Helper()
	rs := u.Requests(method)
	if len(rs) == 0 {
		t.Fatalf("upstream received no %s request", method)
	}
	return rs[len(rs)-1]
}

// Forget drops the recorded requests.
func (u *Upstream) Forget() {
	u.mu.Lock()
	u.reqs = nil
	u.mu.Unlock()
}

// Cancelled counts requests whose caller went away before the reply ended.
func (u *Upstream) Cancelled() int { return int(u.cancelled.Load()) }

// WaitCancelled waits until at least n requests were abandoned.
func (u *Upstream) WaitCancelled(n int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if u.Cancelled() >= n {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return u.Cancelled() >= n
}

func (u *Upstream) modern() bool { return u.Version == V2026 }

func (u *Upstream) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	rec := Recorded{Seq: recordedSeq.Add(1), Method: msg.Method, ID: bytes.Clone(msg.ID), Params: bytes.Clone(msg.Params), Header: r.Header.Clone()}
	u.mu.Lock()
	u.reqs = append(u.reqs, rec)
	u.mu.Unlock()
	if len(msg.ID) == 0 || string(msg.ID) == "null" { // a notification
		w.WriteHeader(http.StatusAccepted)
		return
	}
	u.write(w, r, rec, u.dispatch(rec))
}

func (u *Upstream) dispatch(rec Recorded) Reply {
	info := map[string]any{"name": u.Name, "version": "1.0.0"}
	caps := map[string]any{"tools": map[string]any{}, "resources": map[string]any{}, "prompts": map[string]any{}}
	notFound := Reply{Error: &RPCErr{Code: -32601, Message: "method not found: " + rec.Method}}
	u.mu.Lock()
	defer u.mu.Unlock()
	if key, ok := map[string]string{"tools/list": "tools", "prompts/list": "prompts", "resources/list": "resources", "resources/templates/list": "resourceTemplates"}[rec.Method]; ok && u.rawLists != nil {
		list := u.rawLists[key]
		if len(list) == 0 {
			list = json.RawMessage("[]")
		}
		return Reply{RawResult: fmt.Sprintf(`{%q:%s}`, key, list)}
	}
	if u.fallback != nil {
		switch rec.Method {
		case "tools/call", "prompts/get", "resources/read", "resources/directory/read":
			name, _ := rec.Map()["name"].(string)
			uri, _ := rec.Map()["uri"].(string)
			if u.toolH[name] == nil && u.promptH[name] == nil && u.readH[uri] == nil {
				return callUnlocked(&u.mu, u.fallback, rec)
			}
		}
	}
	switch rec.Method {
	case "server/discover":
		if !u.modern() {
			return notFound
		}
		res := map[string]any{"supportedVersions": []any{V2026}, "capabilities": caps, "_meta": map[string]any{"io.modelcontextprotocol/serverInfo": info}}
		if u.Instructions != "" {
			res["instructions"] = u.Instructions
		}
		return Reply{Result: res}
	case "initialize":
		if u.modern() {
			return notFound
		}
		res := map[string]any{"capabilities": caps, "serverInfo": info}
		if u.Version != VNone {
			res["protocolVersion"] = u.Version
		}
		if u.Instructions != "" {
			res["instructions"] = u.Instructions
		}
		return Reply{Result: res}
	case "tools/list":
		return Reply{Result: map[string]any{"tools": anyList(u.tools)}}
	case "prompts/list":
		return Reply{Result: map[string]any{"prompts": anyList(u.prompts)}}
	case "resources/list":
		return Reply{Result: map[string]any{"resources": anyList(u.resources)}}
	case "resources/templates/list":
		return Reply{Result: map[string]any{"resourceTemplates": anyList(u.templates)}}
	case "tools/call":
		name, _ := rec.Map()["name"].(string)
		if h := u.toolH[name]; h != nil {
			return callUnlocked(&u.mu, h, rec)
		}
		for _, t := range u.tools {
			if t["name"] == name {
				return Reply{Result: Text("ok")}
			}
		}
		return Reply{Error: &RPCErr{Code: -32602, Message: "unknown tool " + name}}
	case "resources/read":
		uri, _ := rec.Map()["uri"].(string)
		if h := u.readH[uri]; h != nil {
			return callUnlocked(&u.mu, h, rec)
		}
		if u.readDefault != nil {
			return callUnlocked(&u.mu, u.readDefault, rec)
		}
		return Reply{Error: &RPCErr{Code: -32002, Message: "resource not found: " + uri}}
	case "prompts/get":
		name, _ := rec.Map()["name"].(string)
		if h := u.promptH[name]; h != nil {
			return callUnlocked(&u.mu, h, rec)
		}
		return Reply{Result: map[string]any{"messages": []any{map[string]any{"role": "user", "content": map[string]any{"type": "text", "text": "prompt " + name}}}}}
	}
	return notFound
}

// callUnlocked runs a script handler without holding the server's lock, so a
// handler may call back into the Upstream.
func callUnlocked(mu *sync.Mutex, h Handler, rec Recorded) Reply {
	mu.Unlock()
	defer mu.Lock()
	return h(rec)
}

func anyList(in []map[string]any) []any {
	out := make([]any, 0, len(in))
	for _, m := range in {
		out = append(out, m)
	}
	return out
}

func (u *Upstream) write(w http.ResponseWriter, r *http.Request, rec Recorded, rep Reply) {
	ctx := r.Context()
	gone := func() bool {
		if ctx.Err() != nil {
			u.cancelled.Add(1)
			return true
		}
		return false
	}
	wait := func(d time.Duration) bool {
		if d <= 0 {
			return !gone()
		}
		select {
		case <-ctx.Done():
			u.cancelled.Add(1)
			return false
		case <-time.After(d):
			return true
		}
	}
	if !wait(rep.Delay) {
		return
	}
	if rep.Status != 0 && rep.Status != http.StatusOK {
		w.WriteHeader(rep.Status)
		_, _ = io.WriteString(w, rep.Body)
		return
	}
	final := u.envelope(rec, rep)
	if rep.ErrorStatus != 0 {
		if rep.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(rep.ErrorStatus)
			_, _ = io.WriteString(w, "event: message\n")
			_ = writeSSEData(w, final)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.ErrorStatus)
		_, _ = w.Write(final)
		return
	}
	if !rep.Stream {
		if rep.Hang {
			<-ctx.Done()
			u.cancelled.Add(1)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(final)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	send := func(b []byte) bool {
		if err := writeSSEData(w, b); err != nil {
			u.cancelled.Add(1)
			return false
		}
		if fl != nil {
			fl.Flush()
		}
		return true
	}
	if fl != nil {
		fl.Flush()
	}
	token := rec.ProgressToken()
	if token == nil && rep.ForceProgress {
		token = rep.Token
		if token == nil {
			token = "unasked"
		}
	}
	if token != nil {
		for _, p := range rep.Progress {
			params := map[string]any{"progressToken": token, "progress": p.Progress}
			if p.Total != 0 {
				params["total"] = p.Total
			}
			if p.Message != "" {
				params["message"] = p.Message
			}
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress", "params": params})
			if !send(b) || !wait(rep.Gap) {
				return
			}
		}
	}
	for _, n := range rep.Notes {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": n.Method, "params": n.Params})
		if !send(b) || !wait(rep.Gap) {
			return
		}
	}
	if rep.Hang {
		<-ctx.Done()
		u.cancelled.Add(1)
		return
	}
	if rep.NoFinal {
		return
	}
	send(final)
}

// envelope builds the final JSON-RPC message for a reply.
func (u *Upstream) envelope(rec Recorded, rep Reply) []byte {
	if rep.RawBody != "" {
		return []byte(rep.RawBody)
	}
	id := rec.ID
	if rep.Error != nil {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": rep.Error})
		return b
	}
	if rep.RawResult != "" {
		return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, id, rep.RawResult))
	}
	res := rep.Result
	if res == nil {
		res = map[string]any{}
	}
	// A 2026-07-28 server marks ordinary results complete. Older servers
	// know nothing of resultType.
	if m, ok := res.(map[string]any); ok && u.modern() {
		if _, has := m["resultType"]; !has {
			cp := make(map[string]any, len(m)+1)
			for k, v := range m {
				cp[k] = v
			}
			cp["resultType"] = "complete"
			res = cp
		}
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": res})
	return b
}

// writeSSEData writes one server-sent event. Each line of the data gets its
// own "data:" prefix, as the format requires: a scripted result that spans
// several lines would otherwise end the event at its first line break, and
// the reader would see a broken message and then nothing.
func writeSSEData(w io.Writer, b []byte) error {
	for _, line := range bytes.Split(b, []byte("\n")) {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "\n")
	return err
}

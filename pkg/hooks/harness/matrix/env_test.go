package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness"
	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness/clients"
)

const opTimeout = 30 * time.Second

// env is one case running on one client and one server.
type env struct {
	t        *testing.T
	client   string
	server   string
	d        clients.Driver
	raw      *rawDriver // set for the raw client
	g        *harness.Gateway
	endpoint string
}

// Client identities as each client declares them, for server/connect.
var clientNames = map[string]string{
	"raw":        rawClientName,
	"go":         "harness-go-client",
	"python":     "harness-python-client",
	"typescript": "harness-typescript-client",
}

func (e *env) connect(caps map[string]any) {
	e.t.Helper()
	off := false
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	rep, err := e.d.Connect(ctx, clients.ConnectOptions{URL: e.endpoint, Capabilities: caps, Cache: &off, ReadTimeoutSeconds: 15})
	if err != nil {
		e.t.Fatalf("%s driver failed to connect: %v", e.client, err)
	}
	if rep.Error != nil {
		e.t.Fatalf("%s client could not connect to %s: %s", e.client, e.endpoint, rep.Error)
	}
	if rep.SDK.ProtocolVersion != harness.V2026 {
		e.t.Fatalf("%s client negotiated %q, want %s", e.client, rep.SDK.ProtocolVersion, harness.V2026)
	}
}

func (e *env) do(o clients.CallOptions) clients.Reply {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	rep, err := e.d.Call(ctx, o)
	if err != nil {
		e.t.Fatalf("%s driver failed on %s: %v", e.client, o.Method, err)
	}
	rep.Result = normalize(rep.Result)
	return rep
}

// call options
type opt func(*clients.CallOptions)

func withProgress(o *clients.CallOptions) { o.Progress = true }

func answers(a ...clients.Answer) opt {
	return func(o *clients.CallOptions) { o.Elicit = a }
}

func (e *env) tool(name string, args map[string]any, opts ...opt) clients.Reply {
	e.t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	o := clients.CallOptions{Method: "tools/call", Params: map[string]any{"name": name, "arguments": args}}
	for _, f := range opts {
		f(&o)
	}
	return e.do(o)
}

func (e *env) list(method string) clients.Reply {
	e.t.Helper()
	return e.do(clients.CallOptions{Method: method})
}

func (e *env) prompt(name string, args map[string]any, opts ...opt) clients.Reply {
	e.t.Helper()
	o := clients.CallOptions{Method: "prompts/get", Params: map[string]any{"name": name}}
	if args != nil {
		o.Params["arguments"] = args
	}
	for _, f := range opts {
		f(&o)
	}
	return e.do(o)
}

func (e *env) read(uri string, opts ...opt) clients.Reply {
	e.t.Helper()
	o := clients.CallOptions{Method: "resources/read", Params: map[string]any{"uri": uri}}
	for _, f := range opts {
		f(&o)
	}
	return e.do(o)
}

// ---- assertions ---------------------------------------------------------------------

// result fails the case when the reply is an error, and returns the result.
func (e *env) result(what string, rep clients.Reply) map[string]any {
	e.t.Helper()
	if rep.Error != nil {
		e.t.Fatalf("%s: want a result, got %s", what, rep.Error)
	}
	if rep.Result == nil {
		e.t.Fatalf("%s: no result", what)
	}
	return rep.Result
}

// okTool requires a tool result that is not an error.
func (e *env) okTool(what string, rep clients.Reply) map[string]any {
	e.t.Helper()
	res := e.result(what, rep)
	if isError(res) {
		e.t.Fatalf("%s: want a successful tool result, got an error result: %s", what, asJSON(res))
	}
	return res
}

// wantText requires a successful tool result with exactly this text.
func (e *env) wantText(what string, rep clients.Reply, want string) map[string]any {
	e.t.Helper()
	res := e.okTool(what, rep)
	e.eq(what+": text", textOf(res), want)
	return res
}

// wantToolError requires a tool error result with exactly this text.
func (e *env) wantToolError(what string, rep clients.Reply, want string) {
	e.t.Helper()
	res := e.result(what, rep)
	if !isError(res) {
		e.t.Errorf("%s: want an error result %q, got %s", what, want, asJSON(res))
		return
	}
	e.eq(what+": error text", textOf(res), want)
}

// wantRPC requires a JSON-RPC error with this code whose message contains
// every part.
func (e *env) wantRPC(what string, rep clients.Reply, code int, parts ...string) {
	e.t.Helper()
	if rep.Error == nil {
		e.t.Errorf("%s: want JSON-RPC error %d, got result %s", what, code, asJSON(rep.Result))
		return
	}
	if rep.Error.Kind != "rpc" || rep.Error.RPCCode() != code {
		e.t.Errorf("%s: want JSON-RPC error %d, got %s", what, code, rep.Error)
		return
	}
	for _, p := range parts {
		if !strings.Contains(rep.Error.Message, p) {
			e.t.Errorf("%s: error message %q does not contain %q", what, rep.Error.Message, p)
		}
	}
}

// wantRefused requires a refusal: an error result or a JSON-RPC error.
func (e *env) wantRefused(what string, rep clients.Reply) {
	e.t.Helper()
	if rep.Error != nil {
		return
	}
	if !isError(rep.Result) {
		e.t.Errorf("%s: want a refusal, got %s", what, asJSON(rep.Result))
	}
}

func (e *env) eq(what string, got, want any) {
	e.t.Helper()
	if g, w := asJSON(normalize(got)), asJSON(normalize(want)); g != w {
		e.t.Errorf("%s:\n  got  %s\n  want %s", what, g, w)
	}
}

func (e *env) contains(what string, list []string, want ...string) {
	e.t.Helper()
	set := map[string]bool{}
	for _, s := range list {
		set[s] = true
	}
	for _, w := range want {
		if !set[w] {
			e.t.Errorf("%s: %v lacks %q", what, list, w)
		}
	}
}

func (e *env) lacks(what string, list []string, unwanted ...string) {
	e.t.Helper()
	for _, s := range list {
		for _, u := range unwanted {
			if s == u {
				e.t.Errorf("%s: %v has %q", what, list, u)
			}
		}
	}
}

func (e *env) noText(what, text string, unwanted ...string) {
	e.t.Helper()
	for _, u := range unwanted {
		if strings.Contains(text, u) {
			e.t.Errorf("%s: %q contains %q", what, text, u)
		}
	}
}

// echoed is the arguments the search tool received, from its structured
// content.
func (e *env) echoed(what string, rep clients.Reply) map[string]any {
	e.t.Helper()
	res := e.okTool(what, rep)
	sc, _ := res["structuredContent"].(map[string]any)
	args, ok := sc["args"].(map[string]any)
	if !ok {
		e.t.Fatalf("%s: the search tool did not echo its arguments: %s", what, asJSON(res))
	}
	return args
}

// inspected is what the inspect tool reported.
func (e *env) inspected(what string, rep clients.Reply) map[string]any {
	e.t.Helper()
	res := e.okTool(what, rep)
	sc, ok := res["structuredContent"].(map[string]any)
	if !ok {
		e.t.Fatalf("%s: the inspect tool returned no structured content: %s", what, asJSON(res))
	}
	return sc
}

// item finds one entry of a listing by a field.
func (e *env) item(what string, rep clients.Reply, list, field, value string) map[string]any {
	e.t.Helper()
	res := e.result(what, rep)
	items, _ := res[list].([]any)
	for _, it := range items {
		if m, ok := it.(map[string]any); ok && m[field] == value {
			return m
		}
	}
	e.t.Fatalf("%s: no %s with %s %q in %s", what, list, field, value, asJSON(names(res, list, field)))
	return nil
}

// hit reports whether the gateway recorded a hit for a hook with an effect
// on the last request of a method.
func (e *env) hit(method, hook, effect string) bool {
	e.t.Helper()
	rec := e.g.LastRecord(e.t, method)
	for _, h := range rec.Hits() {
		if h.Hook == hook && h.Effect == effect {
			return true
		}
	}
	return false
}

// wantAnyHit requires a hit on any request of a method so far, for hooks
// that run on an earlier round than the last.
func (e *env) wantAnyHit(method, hook, effect string) {
	e.t.Helper()
	for _, rec := range e.g.Records() {
		if rec.Method != method {
			continue
		}
		for _, h := range rec.Hits() {
			if h.Hook == hook && h.Effect == effect {
				return
			}
		}
	}
	e.t.Errorf("the gateway recorded no %q hit for %s on any %s", effect, hook, method)
}

func (e *env) wantHit(method, hook, effect string) {
	e.t.Helper()
	if !e.hit(method, hook, effect) {
		e.t.Errorf("the gateway recorded no %q hit for %s on the last %s: %s", effect, hook, method, e.g.LastRecord(e.t, method).JSON())
	}
}

// ---- small helpers ------------------------------------------------------------------

func rule(hook, when string, then ...string) hooks.Rule {
	r := hooks.Rule{Hook: hook, When: when, Then: map[string]string{}}
	for i := 0; i+1 < len(then); i += 2 {
		r.Then[then[i]] = then[i+1]
	}
	return r
}

func audit(r hooks.Rule) hooks.Rule {
	r.Mode = "audit"
	return r
}

func rules(rs ...hooks.Rule) []hooks.Rule { return rs }

// normalize makes a value plain JSON: maps, lists, strings, float64, bool.
func normalize(v any) map[string]any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{"unencodable": fmt.Sprint(v)}
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		var other any
		_ = json.Unmarshal(b, &other)
		return map[string]any{"value": other}
	}
	return m
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func isError(res map[string]any) bool { v, _ := res["isError"].(bool); return v }

// textOf joins the text blocks of a tool result.
func textOf(res map[string]any) string {
	var b strings.Builder
	blocks, _ := res["content"].([]any)
	for _, c := range blocks {
		if m, ok := c.(map[string]any); ok && m["type"] == "text" {
			s, _ := m["text"].(string)
			b.WriteString(s)
		}
	}
	return b.String()
}

// names pulls one string field out of every entry of a list, sorted.
func names(res map[string]any, list, field string) []string {
	out := []string{}
	items, _ := res[list].([]any)
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			s, _ := m[field].(string)
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// promptText is the text of the first message of a prompts/get result.
func promptText(res map[string]any) string {
	msgs, _ := res["messages"].([]any)
	if len(msgs) == 0 {
		return ""
	}
	m, _ := msgs[0].(map[string]any)
	c, _ := m["content"].(map[string]any)
	s, _ := c["text"].(string)
	return s
}

// readText is the text and URI of the first content item of a read result.
func readText(res map[string]any) (text, uri string) {
	cs, _ := res["contents"].([]any)
	if len(cs) == 0 {
		return "", ""
	}
	m, _ := cs[0].(map[string]any)
	text, _ = m["text"].(string)
	uri, _ = m["uri"].(string)
	return text, uri
}

func progressMessages(events []map[string]any) []string {
	out := []string{}
	for _, p := range events {
		s, _ := p["message"].(string)
		out = append(out, s)
	}
	return out
}

func get(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func strs(v any) []string {
	out := []string{}
	l, _ := v.([]any)
	for _, x := range l {
		s, _ := x.(string)
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func keysOf(v any) []string {
	out := []string{}
	m, _ := v.(map[string]any)
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

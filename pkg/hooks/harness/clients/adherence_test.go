package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness"
)

// The client adherence battery: every check runs against every official
// client SDK, through the harness gateway, to scripted mock servers.
//
// A check ends in one of three states for each client:
//
//	conforms       the client did what MCP 2026-07-28 and the gateway need
//	differs        it works, or partly works, but not as the spec or the
//	               design note assumes; the note says what the client did
//	not supported  the client cannot do this at all
//
// The test fails only where a difference would make the gateway unusable
// for a client that is meant to work with it (the checks marked critical).
// Every other difference is a finding, reported in the table the test logs.

const (
	conforms    = "conforms"
	differs     = "differs"
	unsupported = "not supported"
)

type outcome struct {
	status string
	note   string
}

func ok(note string, a ...any) outcome   { return outcome{conforms, fmt.Sprintf(note, a...)} }
func diff(note string, a ...any) outcome { return outcome{differs, fmt.Sprintf(note, a...)} }
func none(note string, a ...any) outcome { return outcome{unsupported, fmt.Sprintf(note, a...)} }

// client is one SDK under test.
type client struct {
	name string
	new  func() (Driver, error)
	// legacy marks an SDK that predates 2026-07-28. It is expected to fail
	// to connect; that is recorded, and does not fail the test.
	legacy bool
}

var allClients = []client{
	{name: "go", new: NewGo},
	{name: "python", new: NewPython},
	{name: "typescript", new: NewTypeScript},
	{name: "typescript-v1", new: NewTypeScriptV1, legacy: true},
}

// env is one check running against one client.
type env struct {
	t *testing.T
	d Driver
	g *harness.Gateway
	r *Recorder
}

const opTimeout = 20 * time.Second

func newEnv(t *testing.T, d Driver) *env {
	g := harness.NewGateway(t)
	base := strings.TrimSuffix(g.Mux(), "/mux")
	// The recorder only records. Every client sees the gateway's replies
	// exactly as the gateway sent them.
	return &env{t: t, d: d, g: g, r: NewRecorder(t, base)}
}

// connect opens the client against a gateway endpoint, through the recorder.
func (e *env) connect(endpoint string, o ConnectOptions) Reply {
	e.t.Helper()
	o.URL = e.r.Via(endpoint)
	if o.ReadTimeoutSeconds == 0 {
		o.ReadTimeoutSeconds = 8
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	rep, err := e.d.Connect(ctx, o)
	if err != nil {
		e.t.Fatalf("driver failed on connect: %v", err)
	}
	return rep
}

// mustConnect connects and requires 2026-07-28. A client that cannot is
// reported through the returned outcome.
func (e *env) mustConnect(endpoint string, o ConnectOptions) *outcome {
	e.t.Helper()
	rep := e.connect(endpoint, o)
	if rep.Error != nil {
		out := none("cannot connect: %s", rep.Error.Message)
		return &out
	}
	if rep.SDK.ProtocolVersion != harness.V2026 {
		out := none("connected, but negotiated %q", rep.SDK.ProtocolVersion)
		return &out
	}
	return nil
}

func (e *env) call(o CallOptions) Reply {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	rep, err := e.d.Call(ctx, o)
	if err != nil {
		e.t.Fatalf("driver failed on %s: %v", o.Method, err)
	}
	return rep
}

func (e *env) tool(name string, args map[string]any) Reply {
	if args == nil {
		args = map[string]any{}
	}
	return e.call(CallOptions{Method: "tools/call", Params: map[string]any{"name": name, "arguments": args}})
}

// ---- small helpers ----------------------------------------------------------------

func rule(hook, when string, then ...string) hooks.Rule {
	m := map[string]string{}
	for i := 0; i+1 < len(then); i += 2 {
		m[then[i]] = then[i+1]
	}
	return hooks.Rule{Hook: hook, When: when, Then: m}
}

const plainTool = `{"name":"t","inputSchema":{"type":"object","properties":{}}}`

var withElicitation = map[string]any{"elicitation": map[string]any{}}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

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

func isError(res map[string]any) bool { v, _ := res["isError"].(bool); return v }

// names pulls one string field out of a list in a result.
func names(res map[string]any, list, field string) []string {
	var out []string
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

func has(list []string, want ...string) bool {
	set := map[string]bool{}
	for _, s := range list {
		set[s] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

func messages(events []map[string]any) []string {
	out := []string{}
	for _, p := range events {
		s, _ := p["message"].(string)
		out = append(out, s)
	}
	return out
}

func errText(e *Error) string {
	if e == nil {
		return "no error"
	}
	return fmt.Sprintf("%s error, code %v: %s", e.Kind, e.Code, trim(e.Message, 160))
}

func trim(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// ---- the checks -------------------------------------------------------------------

type check struct {
	id       string
	title    string
	critical bool
	run      func(e *env) outcome
}

var checks = []check{
	{id: "connect", title: "Connects and negotiates 2026-07-28", critical: true, run: func(e *env) outcome {
		u := harness.NewUpstream(e.t, harness.V2026)
		u.Tool(plainTool, nil)
		e.g.AddServer(e.t, "a", u)
		rep := e.connect(e.g.Direct("a"), ConnectOptions{})
		var first []string
		for _, s := range e.r.Seen("") {
			first = append(first, s.RPCMethod)
		}
		if rep.Error != nil {
			return none("cannot connect: %s. It sent: %s", trim(rep.Error.Message, 200), strings.Join(first, ", "))
		}
		if rep.SDK.ProtocolVersion != harness.V2026 {
			return none("connected, but negotiated %q. It sent: %s", rep.SDK.ProtocolVersion, strings.Join(first, ", "))
		}
		return ok("%s %s; on connect it sent: %s", rep.SDK.Name, rep.SDK.Version, strings.Join(first, ", "))
	}},

	{id: "capabilities", title: "Declares its capabilities on every request", critical: true, run: func(e *env) outcome {
		u := harness.NewUpstream(e.t, harness.V2026)
		u.Tool(plainTool, nil)
		e.g.AddServer(e.t, "a", u)
		caps := func(s Seen) (map[string]any, bool) {
			m, ok := s.Meta()["io.modelcontextprotocol/clientCapabilities"].(map[string]any)
			return m, ok
		}
		// With an elicitation handler.
		if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{Capabilities: withElicitation}); out != nil {
			return *out
		}
		e.r.Forget()
		e.tool("t", nil)
		e.tool("t", nil)
		calls := e.r.Seen("tools/call")
		if len(calls) != 2 {
			return diff("2 calls made, %d reached the gateway", len(calls))
		}
		var with map[string]any
		for i, s := range calls {
			c, ok := caps(s)
			if !ok {
				return diff("call %d carried no clientCapabilities in _meta; _meta was %s", i+1, asJSON(s.Meta()))
			}
			if _, ok := c["elicitation"]; !ok {
				return diff("call %d declared no elicitation although a handler is registered: %s", i+1, asJSON(c))
			}
			if s.Meta()["io.modelcontextprotocol/protocolVersion"] != harness.V2026 {
				return diff("call %d carried protocolVersion %v in _meta", i+1, s.Meta()["io.modelcontextprotocol/protocolVersion"])
			}
			if _, ok := s.Meta()["io.modelcontextprotocol/clientInfo"].(map[string]any); !ok {
				return diff("call %d carried no clientInfo in _meta", i+1)
			}
			with = c
		}
		// Without one.
		if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{}); out != nil {
			return *out
		}
		e.r.Forget()
		e.tool("t", nil)
		calls = e.r.Seen("tools/call")
		if len(calls) != 1 {
			return diff("1 call made, %d reached the gateway", len(calls))
		}
		without, _ := caps(calls[0])
		if _, ok := without["elicitation"]; ok {
			return diff("declares elicitation with no handler registered: %s", asJSON(without))
		}
		return ok("with a handler: %s; without: %s", asJSON(with), asJSON(without))
	}},

	{id: "always-stream", title: "Accepts a stream as the reply to every request", critical: true, run: func(e *env) outcome {
		u := harness.NewUpstream(e.t, harness.V2026)
		u.Tool(plainTool, nil)
		u.Tool(`{"name":"blocked"}`, nil)
		e.g.AddServer(e.t, "a", u, rule("tools/call", "name == 'blocked'", "decision", "'deny'", "reason", "'Not allowed.'"))
		if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{}); out != nil {
			return *out
		}
		list := e.call(CallOptions{Method: "tools/list"})
		if list.Error != nil || !has(names(list.Result, "tools", "name"), "t", "blocked") {
			return diff("listing over a stream: %s, tools %v", errText(list.Error), names(list.Result, "tools", "name"))
		}
		if rep := e.tool("t", nil); rep.Error != nil || textOf(rep.Result) != "ok" {
			return diff("a call over a stream: %s, text %q", errText(rep.Error), textOf(rep.Result))
		}
		refused := e.tool("blocked", nil)
		if refused.Error != nil || !isError(refused.Result) || textOf(refused.Result) != "Not allowed." {
			return diff("a refusal over a stream: %s, result %s", errText(refused.Error), asJSON(refused.Result))
		}
		n := 0
		for _, s := range e.r.Seen("") {
			if len(s.ID) == 0 || string(s.ID) == "null" {
				continue // a notification gets a plain 202
			}
			n++
			if !strings.HasPrefix(s.ContentType, "text/event-stream") {
				return diff("%s was answered with %q, not a stream", s.RPCMethod, s.ContentType)
			}
		}
		return ok("%d requests, every reply a stream", n)
	}},

	{id: "listings", title: "Lists tools, prompts, resources and templates", critical: true, run: func(e *env) outcome {
		u := harness.NewUpstream(e.t, harness.V2026)
		u.Tool(plainTool, nil)
		u.Prompt(`{"name":"greet"}`, nil)
		u.Resource("docs://a", nil)
		u.Template("notes://{id}")
		e.g.AddServer(e.t, "a", u)
		if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{}); out != nil {
			return *out
		}
		for _, l := range []struct{ method, list, field, want string }{
			{"tools/list", "tools", "name", "t"},
			{"prompts/list", "prompts", "name", "greet"},
			{"resources/list", "resources", "uri", "docs://a"},
			{"resources/templates/list", "resourceTemplates", "uriTemplate", "notes://{id}"},
		} {
			rep := e.call(CallOptions{Method: l.method})
			if rep.Error != nil {
				return diff("%s: %s", l.method, errText(rep.Error))
			}
			if got := names(rep.Result, l.list, l.field); !has(got, l.want) {
				return diff("%s returned %v, want %s", l.method, got, l.want)
			}
		}
		return ok("all four listings")
	}},

	{id: "call-get-read", title: "Calls a tool, gets a prompt, reads a resource", critical: true, run: func(e *env) outcome {
		u := harness.NewUpstream(e.t, harness.V2026)
		u.Tool(`{"name":"echo","inputSchema":{"type":"object","properties":{"q":{"type":"string"}}}}`, func(r harness.Recorded) harness.Reply {
			q, _ := r.Args()["q"].(string)
			return harness.Reply{Result: harness.Text("echo " + q)}
		})
		u.Prompt(`{"name":"greet","arguments":[{"name":"who"}]}`, func(r harness.Recorded) harness.Reply {
			who, _ := r.Args()["who"].(string)
			return harness.Reply{Result: map[string]any{"messages": []any{map[string]any{"role": "user", "content": map[string]any{"type": "text", "text": "hello " + who}}}}}
		})
		u.Resource("docs://a", nil)
		e.g.AddServer(e.t, "a", u)
		if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{}); out != nil {
			return *out
		}
		if rep := e.tool("echo", map[string]any{"q": "hi"}); rep.Error != nil || textOf(rep.Result) != "echo hi" {
			return diff("tools/call: %s, result %s", errText(rep.Error), asJSON(rep.Result))
		}
		p := e.call(CallOptions{Method: "prompts/get", Params: map[string]any{"name": "greet", "arguments": map[string]any{"who": "ana"}}})
		if p.Error != nil || !strings.Contains(asJSON(p.Result), "hello ana") {
			return diff("prompts/get: %s, result %s", errText(p.Error), asJSON(p.Result))
		}
		r := e.call(CallOptions{Method: "resources/read", Params: map[string]any{"uri": "docs://a"}})
		if r.Error != nil || !strings.Contains(asJSON(r.Result), "content of docs://a") {
			return diff("resources/read: %s, result %s", errText(r.Error), asJSON(r.Result))
		}
		return ok("all three")
	}},

	{id: "mux", title: "Works on the mux: prefixed names and <slug>+ URIs", critical: true, run: func(e *env) outcome {
		for _, slug := range []string{"a", "b"} {
			u := harness.NewUpstream(e.t, harness.V2026)
			u.Tool(plainTool, harness.Static(harness.Reply{Result: harness.Text("from " + slug)}))
			u.Resource("docs://shared", harness.Static(harness.Reply{Result: harness.Contents("docs://shared", "doc of "+slug)}))
			e.g.AddServer(e.t, slug, u)
		}
		if out := e.mustConnect(e.g.Mux(), ConnectOptions{}); out != nil {
			return *out
		}
		list := e.call(CallOptions{Method: "tools/list"})
		if list.Error != nil || !has(names(list.Result, "tools", "name"), "a__t", "b__t") {
			return diff("tools/list: %s, tools %v", errText(list.Error), names(list.Result, "tools", "name"))
		}
		if rep := e.tool("b__t", nil); rep.Error != nil || textOf(rep.Result) != "from b" {
			return diff("call b__t: %s, text %q", errText(rep.Error), textOf(rep.Result))
		}
		res := e.call(CallOptions{Method: "resources/list"})
		if res.Error != nil || !has(names(res.Result, "resources", "uri"), "a+docs://shared", "b+docs://shared") {
			return diff("resources/list: %s, uris %v", errText(res.Error), names(res.Result, "resources", "uri"))
		}
		read := e.call(CallOptions{Method: "resources/read", Params: map[string]any{"uri": "b+docs://shared"}})
		if read.Error != nil || !strings.Contains(asJSON(read.Result), "doc of b") {
			return diff("read b+docs://shared: %s, result %s", errText(read.Error), asJSON(read.Result))
		}
		if !strings.Contains(asJSON(read.Result), `"uri":"b+docs://shared"`) {
			return diff("the content's URI lost its prefix: %s", asJSON(read.Result))
		}
		return ok("listed, called and read through prefixed names and URIs")
	}},

	{id: "rpc-error", title: "Reports a JSON-RPC error delivered on a stream", critical: true, run: func(e *env) outcome {
		u := harness.NewUpstream(e.t, harness.V2026)
		u.Tool(`{"name":"boom"}`, harness.Static(harness.Reply{Stream: true, Error: &harness.RPCErr{Code: -32000, Message: "upstream exploded"}}))
		u.Tool(`{"name":"down"}`, harness.Static(harness.Reply{Status: 503, Body: "unavailable"}))
		e.g.AddServer(e.t, "a", u)
		if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{}); out != nil {
			return *out
		}
		boom := e.tool("boom", nil)
		if boom.Error == nil {
			return diff("the upstream's error came back as a result: %s", asJSON(boom.Result))
		}
		if boom.Error.Kind != "rpc" || boom.Error.RPCCode() != -32000 || !strings.Contains(boom.Error.Message, "upstream exploded") {
			return diff("the upstream's -32000 was reported as: %s", errText(boom.Error))
		}
		down := e.tool("down", nil)
		if down.Error == nil || down.Error.Kind != "rpc" {
			return diff("an upstream outage was reported as: %s, result %s", errText(down.Error), asJSON(down.Result))
		}
		return ok("upstream error: code %d; upstream outage: code %d", boom.Error.RPCCode(), down.Error.RPCCode())
	}},

	// 2026-07-28 requires resultType on every result, and the Python and
	// TypeScript SDKs reject a result without it. The engine once left it
	// off its own listings and refusals; this keeps that from coming back.
	{id: "result-type", title: "Accepts the gateway's own results: a listing, a refusal, a forwarded result", critical: true,
		run: func(e *env) outcome {
			u := harness.NewUpstream(e.t, harness.V2026)
			u.Tool(plainTool, nil)
			u.Tool(`{"name":"blocked"}`, nil)
			e.g.AddServer(e.t, "a", u, rule("tools/call", "name == 'blocked'", "decision", "'deny'", "reason", "'Not allowed.'"))
			if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{}); out != nil {
				return *out
			}
			var bad []string
			if rep := e.call(CallOptions{Method: "tools/list"}); rep.Error != nil {
				bad = append(bad, "tools/list: "+trim(rep.Error.Message, 150))
			}
			if rep := e.tool("blocked", nil); rep.Error != nil {
				bad = append(bad, "a refused call: "+trim(rep.Error.Message, 150))
			}
			if rep := e.tool("t", nil); rep.Error != nil {
				bad = append(bad, "a forwarded call: "+trim(rep.Error.Message, 150))
			}
			if len(bad) > 0 {
				return diff("the SDK rejects the gateway's results: %s", strings.Join(bad, " | "))
			}
			return ok("accepted a listing, a refusal and a forwarded result as the gateway sends them")
		}},

	// The engine once answered an unknown tool with -32004, which is the Go
	// SDK's own code for "server is closing", so that client reported a
	// closed connection. The spec's code for an unknown tool is -32602.
	{id: "unknown-tool", title: "An unknown tool is reported as an error the caller can act on", critical: true, run: func(e *env) outcome {
		u := harness.NewUpstream(e.t, harness.V2026)
		u.Tool(plainTool, nil)
		e.g.AddServer(e.t, "a", u)
		if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{}); out != nil {
			return *out
		}
		e.call(CallOptions{Method: "tools/list"})
		unknown := e.tool("no_such_tool", nil)
		after := e.tool("t", nil)
		usable := after.Error == nil && textOf(after.Result) == "ok"
		switch {
		case unknown.Error == nil && isError(unknown.Result):
			return ok("an error result: %q", trim(textOf(unknown.Result), 100))
		case unknown.Error == nil:
			return diff("returned a normal result: %s", asJSON(unknown.Result))
		case unknown.Error.Kind != "rpc":
			return diff("reported as %s. The connection still works afterwards: %v", errText(unknown.Error), usable)
		case !usable:
			return diff("reported as %s, and the connection did not work afterwards: %s", errText(unknown.Error), errText(after.Error))
		case unknown.Error.RPCCode() != -32602:
			return diff("reported with code %d, want -32602: %s", unknown.Error.RPCCode(), errText(unknown.Error))
		}
		return ok("%s; the connection works afterwards", errText(unknown.Error))
	}},

	{id: "tool-error", title: "Returns a refused call as an error result, not an exception", run: func(e *env) outcome {
		u := harness.NewUpstream(e.t, harness.V2026)
		u.Tool(plainTool, nil)
		e.g.AddServer(e.t, "a", u, rule("tools/call", "true", "decision", "'deny'", "reason", "'Production is blocked.'"))
		if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{}); out != nil {
			return *out
		}
		rep := e.tool("t", nil)
		if rep.Error != nil {
			return diff("the SDK raised instead of returning the result: %s", errText(rep.Error))
		}
		if !isError(rep.Result) || textOf(rep.Result) != "Production is blocked." {
			return diff("result was %s", asJSON(rep.Result))
		}
		return ok("isError result with the rule's reason")
	}},

	{id: "progress", title: "Receives progress in order, under its own token", run: func(e *env) outcome {
		u := harness.NewUpstream(e.t, harness.V2026)
		u.Tool(plainTool, harness.Static(harness.Reply{Stream: true, Result: harness.Text("done"), Progress: []harness.Progress{
			{Progress: 1, Total: 3, Message: "step 1"}, {Progress: 2, Total: 3, Message: "step 2"}, {Progress: 3, Total: 3, Message: "step 3"},
		}}))
		e.g.AddServer(e.t, "a", u)
		if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{}); out != nil {
			return *out
		}
		e.r.Forget()
		rep := e.call(CallOptions{Method: "tools/call", Params: map[string]any{"name": "t", "arguments": map[string]any{}}, Progress: true})
		if rep.Error != nil || textOf(rep.Result) != "done" {
			return diff("the call failed: %s", errText(rep.Error))
		}
		sent := e.r.Seen("tools/call")
		var token any
		if len(sent) > 0 {
			token = sent[0].Meta()["progressToken"]
		}
		if token == nil {
			return none("asked for progress, but the request carried no progressToken; received %d events", len(rep.Progress))
		}
		got := messages(rep.Progress)
		if strings.Join(got, "|") != "step 1|step 2|step 3" {
			return diff("received %v under token %v", got, token)
		}
		for i, p := range rep.Progress {
			if p["progress"] != float64(i+1) || p["total"] != float64(3) {
				return diff("event %d had progress %v of %v", i+1, p["progress"], p["total"])
			}
		}
		if up := u.Last(e.t, "tools/call").ProgressToken(); up == token {
			return diff("the server saw the client's own token %v", token)
		}
		return ok("3 events in order; its token was %v", token)
	}},

	{id: "progress-rules", title: "Receives rule-reshaped and rule-dropped progress", run: func(e *env) outcome {
		u := harness.NewUpstream(e.t, harness.V2026)
		u.Tool(plainTool, harness.Static(harness.Reply{Stream: true, Result: harness.Text("done"), Progress: []harness.Progress{
			{Progress: 1, Total: 3, Message: "Querying db-internal-7"}, {Progress: 2, Total: 3, Message: "noise"}, {Progress: 3, Total: 3, Message: "Finishing"},
		}}))
		e.g.AddServer(e.t, "a", u,
			rule("tools/progress", "progress.message.contains('db-internal')", "message", "regex.replace(progress.message, 'db-internal-[0-9]+', 'the database')"),
			rule("tools/progress", "progress.message == 'noise'", "decision", "'deny'"))
		if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{}); out != nil {
			return *out
		}
		rep := e.call(CallOptions{Method: "tools/call", Params: map[string]any{"name": "t", "arguments": map[string]any{}}, Progress: true})
		if rep.Error != nil || textOf(rep.Result) != "done" {
			return diff("the call failed: %s", errText(rep.Error))
		}
		if got := messages(rep.Progress); strings.Join(got, "|") != "Querying the database|Finishing" {
			return diff("received %v", got)
		}
		if len(rep.Progress) == 2 && (rep.Progress[0]["progress"] != float64(1) || rep.Progress[1]["progress"] != float64(3)) {
			return diff("the numbers changed: %s", asJSON(rep.Progress))
		}
		return ok("one rewritten, one dropped, one untouched")
	}},
}

// TestClientAdherence runs every check against every client and logs the
// table.
func TestClientAdherence(t *testing.T) {
	table := map[string]map[string]outcome{}
	var ran []string
	for _, c := range allClients {
		d, err := c.new()
		if err != nil {
			if errors.Is(err, ErrNotInstalled) {
				t.Logf("SKIPPED CLIENT %s: %v", strings.ToUpper(c.name), err)
				continue
			}
			t.Fatalf("%s: %v", c.name, err)
		}
		ran = append(ran, c.name)
		dead := "" // set when the client cannot connect at all
		t.Run(c.name, func(t *testing.T) {
			defer d.Close()
			for _, ch := range checks {
				var out outcome
				if dead != "" {
					out = none("%s", dead)
				} else {
					t.Run(ch.id, func(t *testing.T) {
						out = outcome{differs, "the check did not finish"}
						out = ch.run(newEnv(t, d))
					})
				}
				if table[ch.id] == nil {
					table[ch.id] = map[string]outcome{}
				}
				table[ch.id][c.name] = out
				if ch.id == "connect" && out.status != conforms {
					dead = "cannot connect, so not run: " + out.note
				}
				switch {
				case out.status == conforms:
				case c.legacy:
					t.Logf("%s / %s: %s: %s (expected: this SDK predates 2026-07-28)", c.name, ch.id, out.status, out.note)
				case ch.critical:
					t.Errorf("%s / %s: %s: %s", c.name, ch.id, out.status, out.note)
				default:
					t.Logf("FINDING %s / %s: %s: %s", c.name, ch.id, out.status, out.note)
				}
			}
		})
	}
	if len(ran) == 0 {
		t.Fatal("no client could be run")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nCLIENT ADHERENCE (%s)\n", strings.Join(ran, ", "))
	for _, ch := range checks {
		tag := ""
		if ch.critical {
			tag = "  [critical]"
		}
		fmt.Fprintf(&b, "\n%s%s\n", ch.title, tag)
		for _, name := range ran {
			out := table[ch.id][name]
			fmt.Fprintf(&b, "  %-14s %-14s %s\n", name, out.status, out.note)
		}
	}
	t.Log(b.String())
}

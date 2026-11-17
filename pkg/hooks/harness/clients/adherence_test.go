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

// Client outliers, in code. Every case that fits the common format lives in
// JSON under pkg/hooks/testdata and runs through the golden runner
// (TestGolden in the harness package) on every client harness. What is left
// here are the few checks that only make sense for one client at a time and
// need code to drive it: they hold one session across several calls, cancel
// a call mid-flight, read every HTTP exchange an SDK makes off a recording
// proxy, point the client straight at a server, or generate megabytes of
// payload. Each check says in a comment why it is an outlier.
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
// The pre-2026 TypeScript package (typescript-v1) is run here too, to show
// that it cannot connect to a 2026-only front door.

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
	// Not a case: the precondition every other check here needs, and the one place the pre-2026 TypeScript package is shown failing to connect.
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

	// Outlier, in code: it needs two calls in one client session and reads each request off a recording proxy; the golden runner connects afresh for every step.
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

	// Outlier, in code: it reads the content type of every HTTP exchange an SDK makes, including the ones the SDK makes on its own, off a recording proxy.
	{id: "always-stream", title: "Accepts a stream as the reply to every request", critical: true, run: func(e *env) outcome {
		u := harness.NewUpstream(e.t, harness.V2026)
		u.Tool(plainTool, nil)
		u.Tool(`{"name":"blocked"}`, nil)
		e.g.AddServer(e.t, "a", u, rule("tools/call.pre", "tool.name == 'blocked'", "decision", "'deny'", "reason", "'Not allowed.'"))
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

	// 2026-07-28 requires resultType on every result, and the Python and
	// TypeScript SDKs reject a result without it. The engine once left it
	// off its own listings and refusals; this keeps that from coming back.

	// The engine once answered an unknown tool with -32004, which is the Go
	// SDK's own code for "server is closing", so that client reported a
	// closed connection. The spec's code for an unknown tool is -32602.

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

package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types/ref"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// These tests pin one promise: whatever a client or an upstream server
// sends, the engine returns an outcome. It may refuse; it must not panic.
// The tests further down cover the safety net for a panic that happens
// anyway: the request fails closed and the process carries on.

// oddValues is every JSON kind, including the shapes real clients and
// servers get wrong: null for "not given", strings for numbers, scalars
// where an object is expected.
var oddValues = []string{
	`null`, `true`, `false`, `0`, `-1`, `1.5`, `1e30`, `9007199254740993`, `""`, `"x"`, `"50"`, `"2026-01-01T00:00:00Z"`,
	`[]`, `[null]`, `[1,"a",{}]`, `{}`, `{"a":null}`, `{"a":{"b":[null,{"c":1}]}}`,
}

type panicLog struct {
	t     *testing.T
	mu    sync.Mutex
	count int
}

// run calls fn and records a panic instead of letting it end the test.
func (p *panicLog) run(label string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.count++
			if p.count <= 10 {
				p.t.Errorf("panic on %s: %v\n%s", label, r, firstFrames(debug.Stack()))
			}
		}
	}()
	fn()
}

func (p *panicLog) done() {
	if p.count > 10 {
		p.t.Errorf("%d panics in total; the first 10 are shown", p.count)
	}
}

func firstFrames(stack []byte) string {
	lines := strings.Split(string(stack), "\n")
	var out []string
	for i, l := range lines {
		if strings.Contains(l, "pkg/hooks/") && !strings.Contains(l, "fable_review_test.go") {
			if i > 0 {
				out = append(out, strings.TrimSpace(lines[i-1]))
			}
			out = append(out, strings.TrimSpace(l))
			if len(out) >= 8 {
				break
			}
		}
	}
	return strings.Join(out, "\n")
}

func mustCompile(t *testing.T, rules []Rule) *Set {
	t.Helper()
	set, probs := Compile(rules)
	if probs != nil {
		t.Fatalf("rules do not compile: %v", probs)
	}
	return set
}

// typedRules compare, measure and rewrite arguments and results as if every
// value had the type its schema declares.
func typedRules() []Rule {
	return []Rule{
		{Hook: "tools/modify", When: "'limit' in params", Then: map[string]string{"params.limit.maximum": "100"}},
		{Hook: "tools/modify", When: "'org' in params", Then: map[string]string{"params.org.default": "'acme'"}},
		{Hook: "tools/modify", When: "'q' in params", Then: map[string]string{"params.q.description": "'Search text'"}},
		{Hook: "tools/call", When: "has(args.limit) && args.limit > 100", Then: map[string]string{"args.limit": "100"}},
		{Hook: "tools/call", When: "has(args.q) && size(args.q) > 3", Then: map[string]string{"args.q": "regex.replace(args.q, 'a', 'b')"}},
		{Hook: "tools/call", When: "has(args.tags) && args.tags.exists(t, t == 'x')", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/call", When: "has(args.opts) && args.opts.depth > 1", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/call", When: "has(args.from) && timestamp(args.from) > timestamp('2030-01-01T00:00:00Z')", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/call", When: "has(args.flag) && args.flag", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/result", When: "has(result.structuredContent)", Then: map[string]string{"structuredContent": "drop(result.structuredContent, ['a.b', 'rows.*.secret'])"}},
		{Hook: "tools/result", When: "true", Then: map[string]string{"content": "result.content.map(c, c.type == 'text' ? {'type': 'text', 'text': regex.replace(c.text, 'x', 'y')} : c)"}},
		{Mode: "audit", Hook: "tools/call", When: "args.limit > 1", Then: map[string]string{"decision": "'deny'"}},
	}
}

func typedTool() *mcp.Tool {
	return &mcp.Tool{Name: "t", InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"limit": map[string]any{"type": "integer"},
			"q":     map[string]any{"type": "string"},
			"flag":  map[string]any{"type": "boolean"},
			"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"opts":  map[string]any{"type": "object", "properties": map[string]any{"depth": map[string]any{"type": "integer"}}},
			"from":  map[string]any{"type": "string", "format": "date-time"},
			"org":   map[string]any{"type": "string"},
			"mode":  map[string]any{"enum": []any{"a", "b"}},
		},
		"required":             []any{"q"},
		"additionalProperties": false,
	}}
}

func reviewRequest(route string, servers ...*Server) Request {
	return Request{Route: route, Servers: servers, Caller: map[string]any{"userId": "u", "role": "member"},
		Client: map[string]any{"capabilities": map[string]any{"elicitation": map[string]any{}}}}
}

// Every JSON kind in every argument, and as the arguments value itself.
func TestOddArgumentsNeverPanic(t *testing.T) {
	e := &Engine{Signer: Signer{Key: []byte("k")}}
	set := mustCompile(t, typedRules())
	p := &panicLog{t: t}
	defer p.done()
	ctx := context.Background()
	names := []string{"limit", "q", "flag", "tags", "opts", "from", "org", "mode", "unknown"}
	n := 0
	for _, route := range []string{"direct", "mux"} {
		name := "t"
		if route == "mux" {
			name = "a" + Separator + "t"
		}
		call := func(raw string) {
			n++
			p.run(route+" arguments "+raw, func() {
				s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: set, Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
				out := e.CallTool(ctx, reviewRequest(route, s), &mcp.CallToolParamsRaw{Name: name, Arguments: json.RawMessage(raw)})
				if out.Forward == nil && out.Result == nil && out.Error == nil {
					t.Errorf("no outcome for %s arguments %s", route, raw)
				}
			})
		}
		for _, v := range append(oddValues, ``, `{`, `{"q":"a"}{"q":"b"}`, `{"q":"a","q":"b"}`) {
			call(v) // the arguments value itself
		}
		for _, k := range names {
			for _, v := range oddValues {
				call(fmt.Sprintf(`{"q":"abcd",%q:%s}`, k, v))
				call(fmt.Sprintf(`{%q:%s}`, k, v))
			}
		}
	}
	t.Logf("%d calls, %d panics", n, p.count)
}

// Every JSON kind in every part of an upstream tool result.
func TestOddToolResultsNeverPanic(t *testing.T) {
	e := &Engine{Signer: Signer{Key: []byte("k")}}
	set := mustCompile(t, typedRules())
	p := &panicLog{t: t}
	defer p.done()
	ctx := context.Background()
	var replies []string
	for _, v := range oddValues {
		replies = append(replies,
			v,
			fmt.Sprintf(`{"content":%s}`, v),
			fmt.Sprintf(`{"content":[%s]}`, v),
			fmt.Sprintf(`{"content":[{"type":%s,"text":%s}]}`, v, v),
			fmt.Sprintf(`{"content":[{"type":"text","text":"x"}],"structuredContent":%s}`, v),
			fmt.Sprintf(`{"content":[{"type":"text","text":"x"}],"structuredContent":{"a":%s,"rows":%s}}`, v, v),
			fmt.Sprintf(`{"content":[{"type":"resource_link","uri":%s,"name":%s}]}`, v, v),
			fmt.Sprintf(`{"content":[{"type":"resource","resource":%s}]}`, v),
			fmt.Sprintf(`{"content":[{"type":"image","data":%s,"mimeType":%s}]}`, v, v),
			fmt.Sprintf(`{"content":[],"isError":%s}`, v),
			fmt.Sprintf(`{"content":[],"_meta":%s}`, v),
			fmt.Sprintf(`{"resultType":%s,"content":[]}`, v),
			fmt.Sprintf(`{"resultType":"input_required","inputRequests":%s,"requestState":%s}`, v, v),
			fmt.Sprintf(`{"resultType":"input_required","inputRequests":{"k":%s}}`, v),
			fmt.Sprintf(`{"resultType":"input_required","inputRequests":{"k":{"method":%s,"params":%s}}}`, v, v),
			fmt.Sprintf(`{"resultType":"task","task":%s}`, v),
		)
	}
	replies = append(replies, ``, `{`, `[`, `nul`)
	n := 0
	for _, route := range []string{"direct", "mux"} {
		name := "t"
		if route == "mux" {
			name = "a" + Separator + "t"
		}
		for _, raw := range replies {
			n++
			p.run(route+" result "+raw, func() {
				s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: set, Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
				r := reviewRequest(route, s)
				params := &mcp.CallToolParamsRaw{Name: name, Arguments: json.RawMessage(`{"q":"abc"}`)}
				first := e.CallTool(ctx, r, params)
				if first.Forward == nil {
					t.Fatalf("the plain call was not forwarded: %+v", first.Error)
				}
				out := e.UpstreamToolReply(ctx, r, first.Call, params, json.RawMessage(raw))
				if out.Result == nil && out.Error == nil {
					t.Errorf("no outcome for %s result %s", route, raw)
				}
			})
		}
	}
	t.Logf("%d replies, %d panics", n, p.count)
}

// Every JSON kind in every part of a tool's input schema, through listing
// and calling. Schemas come from upstream servers and are the least
// reliable input the engine has.
func TestOddSchemasNeverPanic(t *testing.T) {
	e := &Engine{Signer: Signer{Key: []byte("k")}}
	rules := append(typedRules(),
		Rule{Hook: "tools/modify", When: "true", Then: map[string]string{"outputSchema": "dropSchema(outputSchema, ['limit', 'opts.depth', 'tags.*', '*'])"}},
	)
	set := mustCompile(t, rules)
	p := &panicLog{t: t}
	defer p.done()
	ctx := context.Background()
	var schemas []string
	for _, v := range oddValues {
		schemas = append(schemas,
			v,
			fmt.Sprintf(`{"type":%s}`, v),
			fmt.Sprintf(`{"type":"object","properties":%s}`, v),
			fmt.Sprintf(`{"type":"object","properties":{"limit":%s,"q":%s}}`, v, v),
			fmt.Sprintf(`{"type":"object","properties":{"limit":{"type":%s,"maximum":%s,"minimum":%s,"enum":%s,"const":%s,"default":%s}}}`, v, v, v, v, v, v),
			fmt.Sprintf(`{"type":"object","properties":{"limit":{"type":"integer"}},"required":%s}`, v),
			fmt.Sprintf(`{"type":"object","properties":{"limit":{"type":"integer"}},"required":[%s]}`, v),
			fmt.Sprintf(`{"type":"object","properties":{"limit":{"type":"integer"}},"additionalProperties":%s}`, v),
			fmt.Sprintf(`{"type":"object","properties":{"limit":{"type":"integer"}},"patternProperties":%s}`, v),
			fmt.Sprintf(`{"type":"object","properties":{"limit":{"type":"integer"}},"patternProperties":{"(":%s}}`, v),
			fmt.Sprintf(`{"type":"object","properties":{"opts":{"type":"object","properties":%s}}}`, v),
			fmt.Sprintf(`{"type":"object","properties":{"tags":{"type":"array","items":%s}}}`, v),
			fmt.Sprintf(`{"type":"object","allOf":%s,"anyOf":%s,"oneOf":%s}`, v, v, v),
			fmt.Sprintf(`{"type":"object","properties":{"limit":{"$ref":%s}},"$defs":%s}`, v, v),
		)
	}
	n := 0
	for _, route := range []string{"direct", "mux"} {
		name := "t"
		if route == "mux" {
			name = "a" + Separator + "t"
		}
		for _, raw := range schemas {
			var schema any
			if json.Unmarshal([]byte(raw), &schema) != nil {
				continue
			}
			n++
			p.run(route+" schema "+raw, func() {
				tool := &mcp.Tool{Name: "t", InputSchema: schema, OutputSchema: schema}
				s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: set, Catalog: Catalog{Tools: []*mcp.Tool{tool}}}
				r := reviewRequest(route, s)
				if out := e.ListTools(ctx, r); out.Result == nil && out.Error == nil {
					t.Errorf("no listing outcome for schema %s", raw)
				}
				for _, args := range []string{`{}`, `{"limit":5,"q":"abcd"}`, `{"limit":null}`, `{"limit":"5","opts":{"depth":2},"tags":["x"]}`} {
					out := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: name, Arguments: json.RawMessage(args)})
					if out.Forward == nil && out.Result == nil && out.Error == nil {
						t.Errorf("no call outcome for schema %s", raw)
					}
				}
			})
		}
	}
	t.Logf("%d schemas, %d panics", n, p.count)
}

// Every JSON kind in every part of a resource read reply.
func TestOddReadRepliesNeverPanic(t *testing.T) {
	e := &Engine{Signer: Signer{Key: []byte("k")}}
	set := mustCompile(t, []Rule{
		{Hook: "resources/result", When: "true", Then: map[string]string{"text": "regex.replace(result.text, 'x', 'y')"}},
	})
	p := &panicLog{t: t}
	defer p.done()
	ctx := context.Background()
	var replies []string
	for _, v := range oddValues {
		replies = append(replies,
			v,
			fmt.Sprintf(`{"contents":%s}`, v),
			fmt.Sprintf(`{"contents":[%s]}`, v),
			fmt.Sprintf(`{"contents":[{"uri":%s,"text":%s,"mimeType":%s}]}`, v, v, v),
			fmt.Sprintf(`{"contents":[{"uri":"file:///doc","blob":%s}]}`, v),
			fmt.Sprintf(`{"contents":[{"uri":"file:///doc","text":"x","_meta":%s}]}`, v),
			fmt.Sprintf(`{"resultType":%s,"contents":[]}`, v),
			fmt.Sprintf(`{"resultType":"input_required","inputRequests":%s}`, v),
		)
	}
	replies = append(replies, ``, `{`)
	n := 0
	for _, route := range []string{"direct", "mux"} {
		uri := "file:///doc"
		if route == "mux" {
			uri = PrefixURI("a", uri)
		}
		for _, raw := range replies {
			n++
			p.run(route+" read reply "+raw, func() {
				s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: set, Catalog: Catalog{Resources: []*mcp.Resource{{URI: "file:///doc", Name: "doc"}}}}
				r := reviewRequest(route, s)
				params := &mcp.ReadResourceParams{URI: uri}
				first := e.ReadResource(ctx, r, params)
				if first.Forward == nil {
					t.Fatalf("the plain read was not forwarded: %+v", first.Error)
				}
				out := e.UpstreamReadReply(ctx, r, first.Call, params, json.RawMessage(raw))
				if out.Result == nil && out.Error == nil {
					t.Errorf("no outcome for %s read reply %s", route, raw)
				}
			})
		}
	}
	t.Logf("%d replies, %d panics", n, p.count)
}

// The real catalog: every top-level argument of every real tool, set to
// one value of each JSON kind in turn.
func TestOddArgumentsOnRealToolsNeverPanic(t *testing.T) {
	tools := realTools(t)
	e := &Engine{Signer: Signer{Key: []byte("k")}}
	set := mustCompile(t, typedRules())
	p := &panicLog{t: t}
	defer p.done()
	ctx := context.Background()
	s := &Server{Slug: "dd", Generation: "1", Rules: set, Catalog: Catalog{Tools: tools}}
	r := reviewRequest("direct", s)
	var n, forwarded, refused atomic.Int64
	parallel(len(tools), 0, func(i int) {
		tool := tools[i]
		schema, _ := tool.InputSchema.(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		for k := range props {
			for _, v := range []string{`null`, `true`, `1.5`, `"50"`, `[null]`, `{"a":null}`} {
				n.Add(1)
				raw := fmt.Sprintf(`{%q:%s}`, k, v)
				p.run(tool.Name+" "+raw, func() {
					out := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: tool.Name, Arguments: json.RawMessage(raw)})
					switch {
					case out.Forward != nil:
						forwarded.Add(1)
					case out.Result != nil || out.Error != nil:
						refused.Add(1)
					default:
						t.Errorf("no outcome for %s %s", tool.Name, raw)
					}
				})
			}
		}
	})
	if forwarded.Load() == 0 || refused.Load() == 0 {
		t.Errorf("expected both forwards and refusals, got %d and %d", forwarded.Load(), refused.Load())
	}
	t.Logf("%d calls on %d real tools: %d forwarded, %d refused, %d panics", n.Load(), len(tools), forwarded.Load(), refused.Load(), p.count)
}

// knownGap skips a test that asserts behaviour the engine does not have yet,
// so the suite stays green. Run them with HOOKS_KNOWN_GAPS=1.
func knownGap(t *testing.T) {
	t.Helper()
	if os.Getenv("HOOKS_KNOWN_GAPS") == "" {
		t.Skip("known gap; set HOOKS_KNOWN_GAPS=1 to run")
	}
}

var _ = knownGap

// ---- panics fail closed and the process carries on --------------------------------

// faultLog collects what the engine reports through OnFault.
type faultLog struct {
	mu     sync.Mutex
	faults []Fault
}

func (l *faultLog) add(f Fault) {
	l.mu.Lock()
	l.faults = append(l.faults, f)
	l.mu.Unlock()
}

func (l *faultLog) all() []Fault {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Fault(nil), l.faults...)
}

// mustNotPanic runs fn and fails the test if a panic reaches the caller.
func mustNotPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("%s: the panic reached the caller: %v", what, p)
		}
	}()
	fn()
}

// refused asserts an outcome that fails closed with a fault record.
func refused(t *testing.T, what string, out Outcome) *Fault {
	t.Helper()
	if out.Forward != nil || out.Result != nil {
		t.Fatalf("%s: expected a refusal, got forward=%v result=%v", what, out.Forward, out.Result)
	}
	if out.Error == nil || out.Error.Code != CodeInternal {
		t.Fatalf("%s: expected the internal error code, got %+v", what, out.Error)
	}
	if strings.Contains(out.Error.Message, "panic") || strings.Contains(out.Error.Message, "injected") {
		t.Errorf("%s: the client message leaks the cause: %q", what, out.Error.Message)
	}
	if out.Fault == nil {
		t.Fatalf("%s: no fault record", what)
	}
	if !strings.Contains(strings.Join(out.Issues, "\n"), "panic") {
		t.Errorf("%s: no issue reports the panic: %v", what, out.Issues)
	}
	return out.Fault
}

// A panic in the engine's own code, outside any rule: injected through the
// engine's clock while it wraps an upstream's input request.
func TestEnginePanicFailsClosed(t *testing.T) {
	log := &faultLog{}
	e := &Engine{Signer: Signer{Key: []byte("k")}, OnFault: log.add}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: mustCompile(t, nil), Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
	r := reviewRequest("direct", s)
	params := &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"abc"}`)}
	first := e.CallTool(context.Background(), r, params)
	if first.Forward == nil {
		t.Fatalf("the plain call was not forwarded: %+v", first.Error)
	}
	reply := `{"resultType":"input_required","inputRequests":{"k":{"method":"elicitation/create","params":{"message":"ok?","requestedSchema":{"type":"object","properties":{}}}}}}`

	old := timeNow
	timeNow = func() time.Time { panic("injected") }
	var out Outcome
	mustNotPanic(t, "reply", func() {
		out = e.UpstreamToolReply(context.Background(), r, first.Call, params, json.RawMessage(reply))
	})
	timeNow = old

	f := refused(t, "reply", out)
	if f.Op != "tools/call reply" || f.Panic != "injected" {
		t.Errorf("fault op and panic = %q, %q", f.Op, f.Panic)
	}
	if !strings.Contains(f.Stack, "wrapState") {
		t.Errorf("the stack does not show where the panic happened:\n%s", f.Stack)
	}
	if f.Call == nil || f.Call.Method != "tools/call" || f.Call.Target != "t" || f.Call.Arguments != `{"q":"abc"}` {
		t.Errorf("fault call = %+v", f.Call)
	}
	if got := log.all(); len(got) != 1 || got[0].Panic != "injected" {
		t.Errorf("OnFault got %d faults", len(got))
	}
	// The engine carries on: the same reply now works.
	if again := e.UpstreamToolReply(context.Background(), r, first.Call, params, json.RawMessage(reply)); again.Error != nil {
		t.Errorf("the engine did not recover for the next request: %+v", again.Error)
	}
}

// panicSchema is a tool schema that panics when the engine reads it.
type panicSchema struct{}

func (panicSchema) MarshalJSON() ([]byte, error) { panic("bad schema") }

// A panic on a listing worker goroutine. Without recovery inside the worker
// this ends the process, so the test would not fail: it would crash.
func TestListingWorkerPanicFailsClosed(t *testing.T) {
	for _, workers := range []int{0, 1, 4} {
		log := &faultLog{}
		e := &Engine{Signer: Signer{Key: []byte("k")}, OnFault: log.add, Limits: Limits{Workers: workers}}
		var tools []*mcp.Tool
		for i := 0; i < 40; i++ {
			tools = append(tools, &mcp.Tool{Name: fmt.Sprintf("t%02d", i), InputSchema: map[string]any{"type": "object"}})
		}
		tools[17] = &mcp.Tool{Name: "bad", InputSchema: panicSchema{}}
		s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: mustCompile(t, typedRules()), Catalog: Catalog{Tools: tools}}
		var out Outcome
		mustNotPanic(t, "listing", func() { out = e.ListTools(context.Background(), reviewRequest("mux", s)) })
		f := refused(t, fmt.Sprintf("listing with %d workers", workers), out)
		if f.Op != "tools/list" || f.Panic != "bad schema" {
			t.Errorf("fault op and panic = %q, %q", f.Op, f.Panic)
		}
		if !strings.Contains(f.Stack, "MarshalJSON") {
			t.Errorf("the stack is not the worker's:\n%s", f.Stack)
		}
		if n := len(log.all()); n != 1 {
			t.Errorf("OnFault called %d times, want 1", n)
		}
		// A call to the bad tool is refused the same way.
		mustNotPanic(t, "call", func() {
			out = e.CallTool(context.Background(), reviewRequest("mux", s), &mcp.CallToolParamsRaw{Name: "a" + Separator + "bad", Arguments: json.RawMessage(`{}`)})
		})
		refused(t, "call to the bad tool", out)
		// The engine carries on: the same catalog without the bad tool lists.
		good := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: s.Rules, Catalog: Catalog{Tools: append(append([]*mcp.Tool(nil), tools[:17]...), tools[18:]...)}}
		if ok := e.ListTools(context.Background(), reviewRequest("mux", good)); ok.Error != nil || ok.Fault != nil {
			t.Errorf("the engine did not recover for the next listing: %+v", ok.Error)
		}
	}
}

// boomSet is a rule set with one rule whose expression panics inside CEL,
// as a bug in one of the engine's own helpers would.
func boomSet(t *testing.T, hook string, audit bool) *Set {
	t.Helper()
	env, err := cel.NewEnv(cel.Function("boom", cel.Overload("boom", nil, cel.BoolType,
		cel.FunctionBinding(func(...ref.Val) ref.Val { panic("helper bug") }))))
	if err != nil {
		t.Fatal(err)
	}
	ast, iss := env.Compile("boom()")
	if iss.Err() != nil {
		t.Fatal(iss.Err())
	}
	prog, err := env.Program(ast)
	if err != nil {
		t.Fatal(err)
	}
	mode := ModeEnforce
	if audit {
		mode = ModeAudit
	}
	rule := Rule{Hook: hook, When: "boom()", Then: map[string]string{"decision": "'deny'"}, Mode: mode}
	c := &compiledRule{rule: rule, label: "Boom rule", audit: audit, when: prog, vars: map[string]bool{}}
	return &Set{byHook: map[string][]*compiledRule{hook: {c}}, opts: Options{CostLimit: DefaultCostLimit}, Fingerprint: "boom"}
}

// A panic inside a rule. CEL catches it and the engine treats it as a rule
// error, so the call is refused as policy; the fault record must still carry
// the rule's text and the call.
func TestRulePanicFailsClosedWithRuleTextAndCall(t *testing.T) {
	log := &faultLog{}
	e := &Engine{Signer: Signer{Key: []byte("k")}, OnFault: log.add}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: boomSet(t, "tools/call", false), Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
	var out Outcome
	mustNotPanic(t, "call", func() {
		out = e.CallTool(context.Background(), reviewRequest("direct", s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"abc"}`)})
	})
	if out.Forward != nil {
		t.Fatal("the call was forwarded although its rule panicked")
	}
	f := out.Fault
	if f == nil {
		t.Fatalf("no fault record; hits: %+v", out.Hits)
	}
	if f.Rule == nil || f.Rule.Hook != "tools/call" || f.Rule.Label != "Boom rule" || f.Rule.When != "boom()" || f.Rule.Then["decision"] != "'deny'" || f.Rule.Item != "t" {
		t.Errorf("fault rule = %+v", f.Rule)
	}
	if f.Call == nil || f.Call.Target != "t" || f.Call.Arguments != `{"q":"abc"}` {
		t.Errorf("fault call = %+v", f.Call)
	}
	if !strings.Contains(f.Panic, "helper bug") {
		t.Errorf("fault panic = %q", f.Panic)
	}
	if n := len(log.all()); n != 1 {
		t.Errorf("OnFault called %d times, want 1", n)
	}
	// The client sees the ordinary refusal, with nothing about the rule.
	b, _ := json.Marshal(map[string]any{"result": out.Result, "error": out.Error})
	if strings.Contains(string(b), "Boom") || strings.Contains(string(b), "helper bug") {
		t.Errorf("the reply leaks the rule or the panic: %s", b)
	}
}

// The same panic in an audit rule is reported but changes nothing.
func TestAuditRulePanicIsReportedAndChangesNothing(t *testing.T) {
	log := &faultLog{}
	e := &Engine{Signer: Signer{Key: []byte("k")}, OnFault: log.add}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: boomSet(t, "tools/call", true), Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
	out := e.CallTool(context.Background(), reviewRequest("direct", s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"abc"}`)})
	if out.Forward == nil {
		t.Fatalf("an audit rule changed the outcome: %+v", out.Error)
	}
	if out.Fault == nil || out.Fault.Rule == nil || out.Fault.Rule.When != "boom()" {
		t.Errorf("the audit panic was not reported: %+v", out.Fault)
	}
	if n := len(log.all()); n != 1 {
		t.Errorf("OnFault called %d times, want 1", n)
	}
}

// A panic in a filter rule during a parallel listing: every tool panics on
// its own worker, and the listing still fails closed with one fault.
func TestRulePanicInListingFailsClosed(t *testing.T) {
	log := &faultLog{}
	e := &Engine{Signer: Signer{Key: []byte("k")}, OnFault: log.add}
	var tools []*mcp.Tool
	for i := 0; i < 40; i++ {
		tools = append(tools, &mcp.Tool{Name: fmt.Sprintf("t%02d", i), InputSchema: map[string]any{"type": "object"}})
	}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: boomSet(t, "tools/filter", false), Catalog: Catalog{Tools: tools}}
	var out Outcome
	mustNotPanic(t, "listing", func() { out = e.ListTools(context.Background(), reviewRequest("mux", s)) })
	// The gateway's own tool may still be listed; none of the server's may.
	if res, ok := out.Result.(*mcp.ListToolsResult); ok {
		for _, tool := range res.Tools {
			if strings.HasPrefix(tool.Name, "a"+Separator) {
				t.Fatalf("%s was listed although its filter rule panicked", tool.Name)
			}
		}
	}
	if out.Fault == nil || out.Fault.Rule == nil || out.Fault.Rule.When != "boom()" {
		t.Errorf("no fault with the rule text: %+v", out.Fault)
	}
}

// Arguments in the fault record are cut at the cap.
func TestFaultArgumentsAreCapped(t *testing.T) {
	e := &Engine{Signer: Signer{Key: []byte("k")}}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: boomSet(t, "tools/call", false), Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
	big := `{"q":"` + strings.Repeat("é", 100_000) + `"}`
	out := e.CallTool(context.Background(), reviewRequest("direct", s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(big)})
	if out.Fault == nil || out.Fault.Call == nil {
		t.Fatalf("no fault call: %+v", out.Fault)
	}
	c := out.Fault.Call
	if !c.Truncated || len(c.Arguments) > FaultArgsCap || len(c.Arguments) < FaultArgsCap-4 {
		t.Errorf("arguments length %d, truncated %v; cap %d", len(c.Arguments), c.Truncated, FaultArgsCap)
	}
	if !utf8.ValidString(c.Arguments) {
		t.Error("the cut left invalid text")
	}
}

// The server hooks a gateway may call on their own fail closed too.
func TestServerHookPanicsFailClosed(t *testing.T) {
	// A hand-built rule whose value has the wrong type for its switch, so
	// the engine's own code panics while applying it.
	env, _ := cel.NewEnv()
	ast, _ := env.Compile("true")
	yes, _ := env.Program(ast)
	ast, _ = env.Compile("'not a bool'")
	text, _ := env.Program(ast)
	any := &Field{Pattern: "tools", Type: TAny}
	rule := Rule{Hook: "server/config", When: "true", Then: map[string]string{"tools": "'not a bool'"}}
	c := &compiledRule{rule: rule, label: "Bad switch", when: yes, then: []compiledThen{{key: "tools", spec: any, prog: text}}, vars: map[string]bool{}}
	set := &Set{byHook: map[string][]*compiledRule{"server/config": {c}}, opts: Options{CostLimit: DefaultCostLimit}, Fingerprint: "bad"}

	log := &faultLog{}
	e := &Engine{Signer: Signer{Key: []byte("k")}, OnFault: log.add}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: set, Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
	r := reviewRequest("direct", s)

	// Called directly by the gateway: every switch off, one fault.
	var sw map[string]bool
	var hits []Hit
	mustNotPanic(t, "Config", func() { sw, hits = e.Config(context.Background(), r, s) })
	for k, v := range sw {
		if v {
			t.Errorf("switch %s stayed on after a panic", k)
		}
	}
	if len(sw) != len(switchDefaults) || len(hits) != 1 || !hits[0].Panic {
		t.Errorf("switches %v, hits %+v", sw, hits)
	}
	got := log.all()
	if len(got) != 1 || got[0].Rule == nil || got[0].Rule.Label != "Bad switch" || got[0].Rule.Then["tools"] != "'not a bool'" {
		t.Fatalf("faults after a direct call: %+v", got)
	}

	// Reached from inside an entry point: the request is refused, and the
	// fault has the rule and the call.
	var out Outcome
	mustNotPanic(t, "CallTool", func() {
		out = e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"abc"}`)})
	})
	f := refused(t, "call under a panicking server/config", out)
	if f.Rule == nil || f.Rule.Label != "Bad switch" || f.Call == nil || f.Call.Target != "t" {
		t.Errorf("fault = %+v", f)
	}
	if n := len(log.all()); n != 2 {
		t.Errorf("OnFault called %d times in total, want 2", n)
	}
}

// A fault callback that panics does not undo the recovery.
func TestPanickingFaultCallbackIsIgnored(t *testing.T) {
	e := &Engine{Signer: Signer{Key: []byte("k")}, OnFault: func(Fault) { panic("logger down") }}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: boomSet(t, "tools/call", false), Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
	var out Outcome
	mustNotPanic(t, "call", func() {
		out = e.CallTool(context.Background(), reviewRequest("direct", s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"abc"}`)})
	})
	if out.Forward != nil || out.Fault == nil {
		t.Errorf("forward=%v fault=%v", out.Forward != nil, out.Fault != nil)
	}
}

// ---- confirmations ---------------------------------------------------------------

// The in-memory store hands a challenge out once and forgets it after its
// lifetime.
func TestMemoryStatesTakeOnceAndExpire(t *testing.T) {
	m := &MemoryStates{}
	m.Put("c_one", State{Target: "t"}, time.Minute)
	if st, ok := m.Take("c_one"); !ok || st.Target != "t" {
		t.Fatalf("first take: %+v %v", st, ok)
	}
	if _, ok := m.Take("c_one"); ok {
		t.Error("a challenge was handed out twice")
	}
	if _, ok := m.Take("c_never"); ok {
		t.Error("an unknown key was found")
	}
	m.Put("c_old", State{}, -time.Second)
	if _, ok := m.Take("c_old"); ok {
		t.Error("an expired challenge was handed out")
	}
	// Unanswered challenges are swept, so they cannot pile up.
	m.Put("c_stale", State{}, -time.Second)
	m.swept = time.Now().Add(-2 * time.Minute)
	m.Put("c_new", State{}, time.Minute)
	if n := m.Len(); n != 1 {
		t.Errorf("%d challenges held after a sweep, want 1", n)
	}
}

// Many clients answering at once: each challenge is redeemed exactly once.
func TestConfirmationIsRedeemedOnceUnderConcurrency(t *testing.T) {
	e := &Engine{Signer: Signer{Key: []byte("k")}}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: soakElicitSet, Catalog: Catalog{Tools: []*mcp.Tool{{Name: "t", InputSchema: map[string]any{"type": "object"}}}}}
	r := reviewRequest("direct", s)
	ctx := context.Background()
	var yes mcp.InputResponseMap
	_ = json.Unmarshal([]byte(`{"bifrost.ok":{"action":"accept","content":{"confirm":true}}}`), &yes)
	for round := 0; round < 50; round++ {
		first := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"id":1}`)})
		ir, ok := first.Result.(InputRequired)
		if !ok {
			t.Fatalf("no confirmation asked: %+v", first.Error)
		}
		if strings.Contains(ir.RequestState, ".") || len(ir.RequestState) < 20 {
			t.Fatalf("the request state is not an opaque key: %q", ir.RequestState)
		}
		var forwarded atomic.Int64
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"id":1}`), RequestState: ir.RequestState, InputResponses: yes})
				if out.Forward != nil {
					forwarded.Add(1)
				}
			}()
		}
		wg.Wait()
		if n := forwarded.Load(); n != 1 {
			t.Fatalf("round %d: the call was forwarded %d times for one confirmation", round, n)
		}
	}
}

// The gateway can supply its own store; the engine uses it for every
// confirmation and never falls back to memory.
type countingStates struct {
	MemoryStates
	puts, takes atomic.Int64
}

func (c *countingStates) Put(k string, st State, ttl time.Duration) {
	c.puts.Add(1)
	if ttl != StateTTL {
		panic("unexpected lifetime")
	}
	c.MemoryStates.Put(k, st, ttl)
}

func (c *countingStates) Take(k string) (State, bool) {
	c.takes.Add(1)
	return c.MemoryStates.Take(k)
}

func TestConfirmationsUseTheConfiguredStore(t *testing.T) {
	store := &countingStates{}
	e := &Engine{Signer: Signer{Key: []byte("k")}, States: store}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: soakElicitSet, Catalog: Catalog{Tools: []*mcp.Tool{{Name: "t", InputSchema: map[string]any{"type": "object"}}}}}
	r := reviewRequest("direct", s)
	ctx := context.Background()
	first := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"id":1}`)})
	ir := first.Result.(InputRequired)
	var no mcp.InputResponseMap
	_ = json.Unmarshal([]byte(`{"bifrost.ok":{"action":"decline"}}`), &no)
	out := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"id":1}`), RequestState: ir.RequestState, InputResponses: no})
	if out.Forward != nil {
		t.Fatal("a declined confirmation was forwarded")
	}
	found := false
	for _, h := range out.Hits {
		if h.Rule == "(confirmation)" && h.Effect == "deny" {
			found = true
		}
	}
	if !found {
		t.Errorf("the decline left no audit hit: %+v", out.Hits)
	}
	if store.puts.Load() != 1 || store.takes.Load() != 1 || e.mem != nil {
		t.Errorf("puts %d, takes %d, fallback store used: %v", store.puts.Load(), store.takes.Load(), e.mem != nil)
	}
}

// ---- the version gate -------------------------------------------------------------

// A server that is not on the front door's protocol version gets no
// elicitation and no progress, whatever the rules say. A missing version
// counts as old. Tasks are off for every server.
func TestVersionGate(t *testing.T) {
	rules := []Rule{
		{Hook: "server/connect", When: "true", Then: map[string]string{"capabilities.elicitation": "true"}},
		{Hook: "server/config", When: "true", Then: map[string]string{"tasks": "true", "progress": "true"}},
	}
	ask := `{"resultType":"input_required","requestState":"up1","inputRequests":{"q":{"method":"elicitation/create","params":{"message":"Which?","requestedSchema":{"type":"object","properties":{}}}}}}`
	for _, tc := range []struct {
		version string
		current bool
	}{{"2026-07-28", true}, {"2025-11-25", false}, {"2025-06-18", false}, {"2024-11-05", false}, {"", false}, {"2027-01-01", false}} {
		e := &Engine{Signer: Signer{Key: []byte("k")}}
		s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: tc.version}, Rules: mustCompile(t, rules), Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
		r := reviewRequest("direct", s)
		ctx := context.Background()
		name := "version " + tc.version

		sw, hits := e.Config(ctx, r, s)
		if sw["tasks"] {
			t.Errorf("%s: tasks are on", name)
		}
		if sw["progress"] != tc.current {
			t.Errorf("%s: progress = %v, want %v", name, sw["progress"], tc.current)
		}
		if !sw["tools"] {
			t.Errorf("%s: the gate switched tools off", name)
		}
		gated := 0
		for _, h := range hits {
			if h.Rule == "(gateway)" {
				gated++
			}
		}
		if want := map[bool]int{true: 1, false: 2}[tc.current]; gated != want {
			t.Errorf("%s: %d gate hits, want %d: %+v", name, gated, want, hits)
		}

		ci, _ := e.Connect(ctx, r, s, "test")
		if _, on := ci.Capabilities["elicitation"]; on != tc.current {
			t.Errorf("%s: elicitation advertised = %v, want %v", name, on, tc.current)
		}

		p := &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"abc"}`)}
		first := e.CallTool(ctx, r, p)
		if first.Forward == nil {
			t.Fatalf("%s: the call was not forwarded: %+v", name, first.Error)
		}
		// An input request from the server reaches the client only from a current server.
		out := e.UpstreamToolReply(ctx, r, first.Call, p, json.RawMessage(ask))
		if _, passed := out.Result.(InputRequired); passed != tc.current {
			t.Errorf("%s: input request passed on = %v, want %v", name, passed, tc.current)
		}
		// Progress is relayed only from a current server; the result always arrives.
		got := collect(e.ToolReplyStream(ctx, r, first.Call, p, (&upstreamSeq{events: []UpstreamEvent{
			progressEvent(`{"progressToken":"p","progress":1}`), resultEvent(`{"content":[{"type":"text","text":"done"}]}`)}}).seq()))
		if (len(got.notes) == 1) != tc.current {
			t.Errorf("%s: %d progress events relayed", name, len(got.notes))
		}
		// Every result is marked complete, whether or not the server said so.
		b, _ := json.Marshal(got.final.Result)
		if !strings.Contains(string(b), `"resultType":"complete"`) || !strings.Contains(string(b), "done") {
			t.Errorf("%s: result = %s", name, b)
		}
	}
}

// Elicitation is declared upstream only on requests where the client
// declared it, and a server's question never reaches a client that cannot
// answer.
func TestElicitationFollowsTheRequestsClient(t *testing.T) {
	rules := []Rule{{Hook: "server/connect", When: "true", Then: map[string]string{"capabilities.elicitation": "true"}}}
	e := &Engine{Signer: Signer{Key: []byte("k")}}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: mustCompile(t, rules), Catalog: Catalog{
		Tools: []*mcp.Tool{typedTool()}, Prompts: []*mcp.Prompt{{Name: "p"}}, Resources: []*mcp.Resource{{URI: "file:///doc", Name: "doc"}}}}
	ctx := context.Background()
	ask := json.RawMessage(`{"resultType":"input_required","inputRequests":{"q":{"method":"elicitation/create","params":{"message":"Which?","requestedSchema":{"type":"object","properties":{}}}}}}`)
	for _, can := range []bool{true, false} {
		r := Request{Route: "direct", Servers: []*Server{s}, Caller: map[string]any{"userId": "u"}, Client: map[string]any{"capabilities": map[string]any{}}}
		if can {
			r.Client = map[string]any{"capabilities": map[string]any{"elicitation": map[string]any{}}}
		}
		p := &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"abc"}`)}
		first := e.CallTool(ctx, r, p)
		if first.Forward == nil || first.Forward.Connect == nil {
			t.Fatalf("no forward: %+v", first.Error)
		}
		if _, on := first.Forward.Connect.Capabilities["elicitation"]; on != can {
			t.Errorf("client can elicit = %v, but the server is told %v", can, on)
		}
		if _, passed := e.UpstreamToolReply(ctx, r, first.Call, p, ask).Result.(InputRequired); passed != can {
			t.Errorf("tool: client can elicit = %v, question passed on = %v", can, passed)
		}
		pp := &mcp.GetPromptParams{Name: "p"}
		pf := e.GetPrompt(ctx, r, pp)
		if _, passed := e.UpstreamPromptReply(ctx, r, pf.Call, pp, ask).Result.(InputRequired); passed != can {
			t.Errorf("prompt: client can elicit = %v, question passed on = %v", can, passed)
		}
		rp := &mcp.ReadResourceParams{URI: "file:///doc"}
		rf := e.ReadResource(ctx, r, rp)
		if _, passed := e.UpstreamReadReply(ctx, r, rf.Call, rp, ask).Result.(InputRequired); passed != can {
			t.Errorf("read: client can elicit = %v, question passed on = %v", can, passed)
		}
	}
}

// ---- unknown things are passed on and recorded by shape ----------------------------

func findUnknown(list []Unknown, kind, name string) *Unknown {
	for i := range list {
		if list[i].Kind == kind && list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

func TestUnknownsAreRecordedByShapeNeverByValue(t *testing.T) {
	var mu sync.Mutex
	var reported []Unknown
	e := &Engine{Signer: Signer{Key: []byte("k")}, OnUnknown: func(u Unknown) {
		mu.Lock()
		reported = append(reported, u)
		mu.Unlock()
	}}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: mustCompile(t, nil), Catalog: Catalog{
		Tools:     []*mcp.Tool{typedTool()},
		Prompts:   []*mcp.Prompt{{Name: "p"}},
		Templates: []*mcp.ResourceTemplate{{URITemplate: "repo://{owner}/contents{/path*}", Name: "contents"}, {URITemplate: "file:///docs/{name}", Name: "docs"}},
	}}
	r := reviewRequest("direct", s)
	ctx := context.Background()
	p := &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"abc"}`)}
	first := e.CallTool(ctx, r, p)
	if first.Forward == nil {
		t.Fatalf("not forwarded: %+v", first.Error)
	}

	// Content blocks of types the protocol version does not define.
	result := `{"content":[
		{"type":"text","text":"fine"},
		{"type":"video","url":"https://secret.example/clip","frames":[1,2,3],"meta":{"a":1,"b":2}},
		{"type":"video","url":"https://secret.example/other","frames":[]},
		{"text":"a block with no type"},
		"not even an object"]}`
	out := e.UpstreamToolReply(ctx, r, first.Call, p, json.RawMessage(result))
	b, _ := json.Marshal(out.Result)
	for _, want := range []string{`"video"`, "https://secret.example/clip", "a block with no type", "not even an object"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the gateway changed content it did not recognise; missing %s in %s", want, b)
		}
	}
	video := findUnknown(out.Unknown, UnknownContentBlock, "video")
	if video == nil || video.Count != 2 || video.Action != "passed" || video.Server != "a" || video.Target != "t" {
		t.Fatalf("video record = %+v in %+v", video, out.Unknown)
	}
	if video.Shape["type"] != "string" || video.Shape["url"] != "string" || video.Shape["frames"] != "array[3]" || video.Shape["meta"] != "object{2}" {
		t.Errorf("video shape = %v", video.Shape)
	}
	untyped := findUnknown(out.Unknown, UnknownContentBlock, "")
	if untyped == nil || untyped.Count != 2 {
		t.Errorf("untyped blocks record = %+v", untyped)
	}
	rec, _ := json.Marshal(out.Unknown)
	for _, secret := range []string{"secret.example", "a block with no type", "not even an object", "fine"} {
		if strings.Contains(string(rec), secret) {
			t.Errorf("a record carries a value: %q in %s", secret, rec)
		}
	}

	// A result type the engine does not handle is passed on and recorded; a
	// task handle is refused and recorded.
	out = e.UpstreamToolReply(ctx, r, first.Call, p, json.RawMessage(`{"resultType":"partial","content":[{"type":"text","text":"x"}]}`))
	if u := findUnknown(out.Unknown, UnknownResultType, "partial"); u == nil || u.Action != "passed" {
		t.Errorf("partial record = %+v", out.Unknown)
	}
	out = e.UpstreamToolReply(ctx, r, first.Call, p, json.RawMessage(`{"resultType":"task","taskId":"abc","status":"working"}`))
	if u := findUnknown(out.Unknown, UnknownResultType, "task"); u == nil || u.Action != "refused" || u.Shape["taskId"] != "string" {
		t.Errorf("task record = %+v", out.Unknown)
	}

	// An input request that is not an elicitation is refused and recorded.
	out = e.UpstreamToolReply(ctx, r, first.Call, p, json.RawMessage(`{"resultType":"input_required","inputRequests":{"s":{"method":"sampling/createMessage","params":{"messages":[],"maxTokens":5}}}}`))
	if _, passed := out.Result.(InputRequired); passed {
		t.Error("a sampling request was passed to the client")
	}
	if u := findUnknown(out.Unknown, UnknownInputRequest, "sampling/createMessage"); u == nil || u.Action != "refused" || u.Shape["maxTokens"] != "number" {
		t.Errorf("sampling record = %+v", out.Unknown)
	}

	// A prompt message with an unknown content type.
	pp := &mcp.GetPromptParams{Name: "p"}
	pf := e.GetPrompt(ctx, r, pp)
	out = e.UpstreamPromptReply(ctx, r, pf.Call, pp, json.RawMessage(`{"messages":[{"role":"user","content":{"type":"hologram","depth":3}},{"role":"user","content":{"type":"text","text":"hi"}}]}`))
	if u := findUnknown(out.Unknown, UnknownContentBlock, "hologram"); u == nil || u.Target != "p" || u.Shape["depth"] != "number" {
		t.Errorf("hologram record = %+v", out.Unknown)
	}
	if b, _ := json.Marshal(out.Result); !strings.Contains(string(b), "hologram") {
		t.Errorf("the unknown prompt content was not passed on: %s", b)
	}

	// Notifications on a stream: unknown ones are dropped and recorded; a log
	// message is a known type and is only counted.
	got := collect(e.ToolReplyStream(ctx, r, first.Call, p, (&upstreamSeq{events: []UpstreamEvent{
		{Method: "notifications/vendor/heartbeat", Params: json.RawMessage(`{"beat":1,"host":"db-internal-7"}`)},
		{Method: "notifications/vendor/heartbeat", Params: json.RawMessage(`{"beat":2,"host":"db-internal-7"}`)},
		{Method: "notifications/message", Params: json.RawMessage(`{"level":"info","data":"x"}`)},
		resultEvent(`{"content":[{"type":"text","text":"done"}]}`),
	}}).seq()))
	if len(got.notes) != 0 {
		t.Errorf("%d unknown notifications were relayed", len(got.notes))
	}
	hb := findUnknown(got.final.Unknown, UnknownNotification, "notifications/vendor/heartbeat")
	if hb == nil || hb.Count != 2 || hb.Action != "dropped" || hb.Shape["beat"] != "number" || hb.Shape["host"] != "string" {
		t.Errorf("heartbeat record = %+v", got.final.Unknown)
	}
	if findUnknown(got.final.Unknown, UnknownNotification, "notifications/message") != nil {
		t.Error("a log message was recorded as unknown")
	}
	if rec, _ := json.Marshal(got.final.Unknown); strings.Contains(string(rec), "db-internal") {
		t.Errorf("a record carries a value: %s", rec)
	}

	// A template form the gateway cannot parse.
	lt := e.ListTemplates(ctx, r)
	if u := findUnknown(lt.Unknown, UnknownTemplateForm, "expression {/path*}"); u == nil || u.Target != "repo://{owner}/contents{/path*}" || u.Action != "passed" {
		t.Errorf("template record = %+v", lt.Unknown)
	}
	if len(lt.Unknown) != 1 {
		t.Errorf("a template the gateway can parse was recorded: %+v", lt.Unknown)
	}

	// Every record also went to the callback, once.
	mu.Lock()
	defer mu.Unlock()
	counts := map[string]int{}
	for _, u := range reported {
		counts[u.Kind+" "+u.Name]++
	}
	for key, want := range map[string]int{
		"content_block video": 1, "content_block ": 1, "result_type partial": 1, "result_type task": 1,
		"input_request sampling/createMessage": 1, "content_block hologram": 1,
		"notification notifications/vendor/heartbeat": 1, "template_form expression {/path*}": 1,
	} {
		if counts[key] != want {
			t.Errorf("callback got %q %d times, want %d (all: %v)", key, counts[key], want, counts)
		}
	}
	// The key groups the same thing from the same server across requests.
	if (Unknown{Server: "a", Kind: "content_block", Name: "video", Target: "t"}).Key() != (Unknown{Server: "a", Kind: "content_block", Name: "video", Target: "other"}).Key() {
		t.Error("the key depends on the target")
	}
}

// Nothing known is ever reported, and a callback that panics is ignored.
func TestKnownThingsAreNotReportedAsUnknown(t *testing.T) {
	calls := 0
	e := &Engine{Signer: Signer{Key: []byte("k")}, OnUnknown: func(Unknown) { calls++; panic("logger down") }}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: mustCompile(t, nil), Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
	r := reviewRequest("direct", s)
	ctx := context.Background()
	p := &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"abc"}`)}
	first := e.CallTool(ctx, r, p)
	known := `{"resultType":"complete","content":[{"type":"text","text":"a"},{"type":"image","data":"AA==","mimeType":"image/png"},{"type":"audio","data":"AA==","mimeType":"audio/wav"},{"type":"resource_link","uri":"x://y","name":"y"},{"type":"resource","resource":{"uri":"x://y","text":"z"}}],"structuredContent":{"a":1}}`
	out := e.UpstreamToolReply(ctx, r, first.Call, p, json.RawMessage(known))
	if len(out.Unknown) != 0 || calls != 0 {
		t.Fatalf("known content was reported: %+v", out.Unknown)
	}
	var got Outcome
	mustNotPanic(t, "a panicking callback", func() {
		got = e.UpstreamToolReply(ctx, r, first.Call, p, json.RawMessage(`{"content":[{"type":"video"}]}`))
	})
	if calls != 1 || len(got.Unknown) != 1 || got.Error != nil {
		t.Errorf("calls %d, unknown %+v, error %v", calls, got.Unknown, got.Error)
	}
}

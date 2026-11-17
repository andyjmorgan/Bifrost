package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"cel.dev/cel-go/cel"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The safety net for a panic: the request fails closed, the fault is reported
// with the rule and the call, and the process carries on.

// refusedWithFault asserts an outcome that fails closed with a fault record.
func refusedWithFault(t *testing.T, what string, out Outcome) *Fault {
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
	e := &Engine{OnFault: log.add}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: mustCompile(t, nil), Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
	r := newRequest("direct", s)
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

	f := refusedWithFault(t, "reply", out)
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
		e := &Engine{OnFault: log.add, Limits: Limits{Workers: workers}}
		var tools []*mcp.Tool
		for i := 0; i < 40; i++ {
			tools = append(tools, &mcp.Tool{Name: fmt.Sprintf("t%02d", i), InputSchema: map[string]any{"type": "object"}})
		}
		tools[17] = &mcp.Tool{Name: "bad", InputSchema: panicSchema{}}
		s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: mustCompile(t, typedRules()), Catalog: Catalog{Tools: tools}}
		var out Outcome
		mustNotPanic(t, "listing", func() { out = e.ListTools(context.Background(), newRequest("mux", s)) })
		f := refusedWithFault(t, fmt.Sprintf("listing with %d workers", workers), out)
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
			out = e.CallTool(context.Background(), newRequest("mux", s), &mcp.CallToolParamsRaw{Name: "a" + Separator + "bad", Arguments: json.RawMessage(`{}`)})
		})
		refusedWithFault(t, "call to the bad tool", out)
		// The engine carries on: the same catalog without the bad tool lists.
		good := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: s.Rules, Catalog: Catalog{Tools: append(append([]*mcp.Tool(nil), tools[:17]...), tools[18:]...)}}
		if ok := e.ListTools(context.Background(), newRequest("mux", good)); ok.Error != nil || ok.Fault != nil {
			t.Errorf("the engine did not recover for the next listing: %+v", ok.Error)
		}
	}
}

// A panic inside a rule. CEL catches it and the engine treats it as a rule
// error, so the call is refused as policy; the fault record must still carry
// the rule's text and the call.
func TestRulePanicFailsClosedWithRuleTextAndCall(t *testing.T) {
	log := &faultLog{}
	e := &Engine{OnFault: log.add}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: boomSet(t, "tools/call.pre", false), Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
	var out Outcome
	mustNotPanic(t, "call", func() {
		out = e.CallTool(context.Background(), newRequest("direct", s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"abc"}`)})
	})
	if out.Forward != nil {
		t.Fatal("the call was forwarded although its rule panicked")
	}
	f := out.Fault
	if f == nil {
		t.Fatalf("no fault record; hits: %+v", out.Hits)
	}
	if f.Rule == nil || f.Rule.Hook != "tools/call.pre" || f.Rule.Label != "Boom rule" || f.Rule.When != "boom()" || f.Rule.Then["decision"] != "'deny'" || f.Rule.Item != "t" {
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

// A panic inside an audit rule is reported but changes nothing.
func TestAuditRulePanicIsReportedAndChangesNothing(t *testing.T) {
	log := &faultLog{}
	e := &Engine{OnFault: log.add}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: boomSet(t, "tools/call.pre", true), Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
	out := e.CallTool(context.Background(), newRequest("direct", s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"abc"}`)})
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
	e := &Engine{OnFault: log.add}
	var tools []*mcp.Tool
	for i := 0; i < 40; i++ {
		tools = append(tools, &mcp.Tool{Name: fmt.Sprintf("t%02d", i), InputSchema: map[string]any{"type": "object"}})
	}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: boomSet(t, "tools/list.post", false), Catalog: Catalog{Tools: tools}}
	var out Outcome
	mustNotPanic(t, "listing", func() { out = e.ListTools(context.Background(), newRequest("mux", s)) })
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
	e := &Engine{}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: boomSet(t, "tools/call.pre", false), Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
	big := `{"q":"` + strings.Repeat("é", 100_000) + `"}`
	out := e.CallTool(context.Background(), newRequest("direct", s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(big)})
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
	any := &Field{Pattern: "settings.tools", Type: TAny}
	rule := Rule{Hook: "server.settings", When: "true", Then: map[string]string{"settings.tools": "'not a bool'"}}
	c := &compiledRule{rule: rule, label: "Bad switch", when: yes, then: []compiledThen{{key: "settings.tools", spec: any, prog: text}}, vars: map[string]bool{}}
	set := &Set{byHook: map[string][]*compiledRule{"server.settings": {c}}, opts: Options{CostLimit: DefaultCostLimit}, Fingerprint: "bad"}

	log := &faultLog{}
	e := &Engine{OnFault: log.add}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: set, Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
	r := newRequest("direct", s)

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
	if len(got) != 1 || got[0].Rule == nil || got[0].Rule.Label != "Bad switch" || got[0].Rule.Then["settings.tools"] != "'not a bool'" {
		t.Fatalf("faults after a direct call: %+v", got)
	}

	// Reached from inside an entry point: the request is refused, and the
	// fault has the rule and the call.
	var out Outcome
	mustNotPanic(t, "CallTool", func() {
		out = e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"abc"}`)})
	})
	f := refusedWithFault(t, "call under a panicking server.settings", out)
	if f.Rule == nil || f.Rule.Label != "Bad switch" || f.Call == nil || f.Call.Target != "t" {
		t.Errorf("fault = %+v", f)
	}
	if n := len(log.all()); n != 2 {
		t.Errorf("OnFault called %d times in total, want 2", n)
	}
}

// A fault callback that panics does not undo the recovery.
func TestPanickingFaultCallbackIsIgnored(t *testing.T) {
	e := &Engine{OnFault: func(Fault) { panic("logger down") }}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: boomSet(t, "tools/call.pre", false), Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
	var out Outcome
	mustNotPanic(t, "call", func() {
		out = e.CallTool(context.Background(), newRequest("direct", s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"abc"}`)})
	})
	if out.Forward != nil || out.Fault == nil {
		t.Errorf("forward=%v fault=%v", out.Forward != nil, out.Fault != nil)
	}
}

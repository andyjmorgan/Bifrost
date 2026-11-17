package hooks

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types/ref"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The helpers every test file in this package shares: how a rule set, a
// catalog, a server and a request are built, how a reply is played back, and
// how an outcome is read. A helper that only one file uses lives in that
// file.

// ---- rules ----------------------------------------------------------------------

// mustCompile compiles rules under the default limits and fails the test if
// they do not compile.
func mustCompile(t *testing.T, rules []Rule) *Set {
	t.Helper()
	set, probs := Compile(rules)
	if probs != nil {
		t.Fatalf("rules do not compile: %v", probs)
	}
	return set
}

// mustCompileUncapped compiles rules with the CEL cost limit out of the way,
// for tests that are about time or shape and not about cost.
func mustCompileUncapped(t testing.TB, rules ...Rule) *Set {
	t.Helper()
	s, p := Compile(rules, Options{CostLimit: 1 << 40})
	if p != nil {
		t.Fatal(p)
	}
	return s
}

// saveRule compiles one rule as saving it does, and returns its problems and
// its notes. With no `then` the rule denies.
func saveRule(hook, when string, then map[string]string) (Problems, Problems) {
	if then == nil {
		then = map[string]string{KeyDecision: "'deny'"}
	}
	c, probs := compileRule(0, Rule{Hook: hook, When: when, Then: then}, Options{CostLimit: DefaultCostLimit})
	if c == nil {
		return probs, nil
	}
	return probs, c.warnings
}

// confirmThen is the `then` of a rule that asks the user to confirm, under a
// key.
func confirmThen(key, msg string) map[string]string {
	return map[string]string{KeyConfirm: "elicit('" + key + "', '" + msg + "')"}
}

// confirmEveryCallSet asks for a confirmation of every tool call, under the
// key "ok", and shows the id argument in the question.
var confirmEveryCallSet = func() *Set {
	s, _ := Compile([]Rule{{Hook: "tools/call.pre", When: "true", Then: map[string]string{"confirm": "elicit('ok', 'Go {id}?', {'id': string(request.params.arguments.id)})"}}})
	return s
}()

// typedRules compare, measure and rewrite arguments and results as if every
// value had the type its schema declares.
func typedRules() []Rule {
	return []Rule{
		{Hook: "tools/list.post", When: "'limit' in tool.params", Then: map[string]string{"tool.params.limit.maximum": "100"}},
		{Hook: "tools/list.post", When: "'org' in tool.params", Then: map[string]string{"tool.params.org.default": "'acme'"}},
		{Hook: "tools/list.post", When: "'q' in tool.params", Then: map[string]string{"tool.params.q.description": "'Search text'"}},
		{Hook: "tools/call.pre", When: "has(request.params.arguments.limit) && request.params.arguments.limit > 100", Then: map[string]string{"request.params.arguments.limit": "100"}},
		{Hook: "tools/call.pre", When: "has(request.params.arguments.q) && size(request.params.arguments.q) > 3", Then: map[string]string{"request.params.arguments.q": "regex.replace(request.params.arguments.q, 'a', 'b')"}},
		{Hook: "tools/call.pre", When: "has(request.params.arguments.tags) && request.params.arguments.tags.exists(t, t == 'x')", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/call.pre", When: "has(request.params.arguments.opts) && request.params.arguments.opts.depth > 1", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/call.pre", When: "has(request.params.arguments.from) && timestamp(request.params.arguments.from) > timestamp('2030-01-01T00:00:00Z')", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/call.pre", When: "has(request.params.arguments.flag) && request.params.arguments.flag", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/call.post.result", When: "has(response.result.structuredContent)", Then: map[string]string{"response.result.structuredContent": "drop(response.result.structuredContent, ['a.b', 'rows.*.secret'])"}},
		{Hook: "tools/call.post.result", When: "true", Then: map[string]string{"response.result.content": "response.result.content.map(c, c.type == 'text' ? {'type': 'text', 'text': regex.replace(c.text, 'x', 'y')} : c)"}},
		{Mode: "audit", Hook: "tools/call.pre", When: "request.params.arguments.limit > 1", Then: map[string]string{"decision": "'deny'"}},
	}
}

// realisticRules is a 20-rule policy of the kind an administrator writes.
func realisticRules() []Rule {
	return []Rule{
		{Hook: "tools/list.post", When: "tool.name.matches('^(delete|drop|purge|destroy)_')", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/list.post", When: "request.caller.role != 'admin' && tool.name.startsWith('admin')", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/list.post", When: "tool.?annotations.?destructiveHint.orValue(false) && request.caller.role != 'admin'", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/list.post", When: "tool.?description.orValue('').contains('DEPRECATED')", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/list.post", When: "size(tool.name) > 64", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/list.post", When: "size(tool.?description.orValue('')) > 1000", Then: map[string]string{"tool.description": "tool.description.substring(0, 1000)"}},
		{Hook: "tools/list.post", When: "tool.name.startsWith('datadog_')", Then: map[string]string{"tool.name": "regex.replace(tool.name, '^datadog_', 'dd_')"}},
		{Hook: "tools/list.post", When: "has(tool.description)", Then: map[string]string{"tool.description": "regex.replace(tool.description, '(?i)confluence', 'the wiki')"}},
		{Hook: "tools/list.post", When: "'limit' in tool.params", Then: map[string]string{"tool.params.limit.maximum": "100"}},
		{Hook: "tools/list.post", When: "'query' in tool.params", Then: map[string]string{"tool.params.query.description": "'Search text'"}},
		{Hook: "tools/list.post", When: "!tool.?annotations.?readOnlyHint.hasValue() && tool.name.startsWith('get_')", Then: map[string]string{"tool.annotations.readOnlyHint": "true"}},
		{Hook: "tools/list.post", When: "tool.name.contains('search')", Then: map[string]string{"tool.title": "'Search: ' + tool.name"}},
		{Hook: "tools/list.post", When: "'org' in tool.params", Then: map[string]string{"tool.params.org.default": "'acme'"}},
		{Hook: "tools/list.post", When: "true", Then: map[string]string{"tool._meta.ai\\.bifrost/reviewed": "true"}},
		{Hook: "tools/list.post", When: "tool.name.endsWith('_v2')", Then: map[string]string{"tool.name": "tool.name.substring(0, size(tool.name) - 3)"}},
		{Hook: "tools/call.pre", When: "has(request.params.arguments.limit) && request.params.arguments.limit > 100", Then: map[string]string{"request.params.arguments.limit": "100"}},
		{Hook: "tools/call.pre", When: "has(request.params.arguments.env) && request.params.arguments.env == 'prod' && request.caller.role != 'admin'", Then: map[string]string{"decision": "'deny'", "reason": "'Production is blocked.'"}},
		{Hook: "tools/call.pre", When: "has(request.params.arguments.from) && has(request.params.arguments.to) && timestamp(request.params.arguments.to) - timestamp(request.params.arguments.from) > duration('168h')", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/call.post.result", When: "has(response.result.structuredContent)", Then: map[string]string{"response.result.content": "[]"}},
		{Mode: "audit", Hook: "tools/list.post", When: "tool.name.contains('write')", Then: map[string]string{"decision": "'deny'"}},
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

// ---- catalogs and servers -------------------------------------------------------

// qSchema is an input schema with one string argument, q.
var qSchema = map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}

// oneTool is a catalog of one tool, t, that takes q.
func oneTool() Catalog { return Catalog{Tools: []*mcp.Tool{{Name: "t", InputSchema: qSchema}}} }

// anyArgsTool is a catalog of one tool, t, whose schema takes any arguments.
var anyArgsTool = Catalog{Tools: []*mcp.Tool{{Name: "t", InputSchema: map[string]any{"type": "object"}}}}

// typedTool is a tool, t, with an argument of each type and a closed schema.
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

// twoArgTool is a tool, t, with two string arguments, q and tenant.
func twoArgTool() *mcp.Tool {
	return &mcp.Tool{Name: "t", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}, "tenant": map[string]any{"type": "string"}}}}
}

// realTools is the corpus of real tool definitions. A test that needs it is
// skipped when the corpus is not there.
func realTools(t testing.TB) []*mcp.Tool {
	t.Helper()
	tools, err := loadToolRef("../../internal/e2e/testdata/real", "corpus.json#tools", 0)
	if err != nil {
		t.Skip("real corpus not present: run scripts/fixtures")
	}
	return tools
}

// newServer is a server on slug "a", at the current protocol version, with a
// catalog and its rules compiled.
func newServer(t *testing.T, cat Catalog, rules ...Rule) *Server {
	t.Helper()
	return &Server{Slug: "a", Generation: "1", Info: ServerInfo{Name: "Docs", ProtocolVersion: ProtocolCurrent}, Rules: mustCompile(t, rules), Catalog: cat}
}

// newFullServer is newServer with one of each: the tool t, a prompt p with
// one argument, and the resource docs://a.
func newFullServer(t *testing.T, rules ...Rule) *Server {
	t.Helper()
	return newServer(t, Catalog{
		Tools:     oneTool().Tools,
		Prompts:   []*mcp.Prompt{{Name: "p", Arguments: []*mcp.PromptArgument{{Name: "tone"}}}},
		Resources: []*mcp.Resource{{URI: "docs://a", Name: "a"}},
	}, rules...)
}

// ---- requests -------------------------------------------------------------------

// newRequest is a request on a route from user "u", a member, under
// principal "p", through a client that can answer questions, with one
// allow-listed header.
func newRequest(route string, servers ...*Server) Request {
	return Request{Route: route, Servers: servers, Principal: "p",
		Caller:  map[string]any{"userId": "u", "role": "member"},
		Client:  map[string]any{"clientInfo": map[string]any{"name": "claude-code", "version": "2.1"}, "capabilities": map[string]any{"elicitation": map[string]any{}}},
		Headers: map[string]string{"X-Team": "sre"}}
}

// entitledRequest is an engine and a direct request to a server that holds an
// entitlement and has one tool, one prompt and one resource. The caller
// belongs to an organization, the clock is fixed, the server names no
// protocol version, and the rules are compiled without a cost limit.
func entitledRequest(t testing.TB, rules ...Rule) (*Engine, Request) {
	t.Helper()
	s := &Server{Slug: "a", Entitlement: map[string]any{"id": "entitlement-a"}, Generation: "1", Rules: mustCompileUncapped(t, rules...), Catalog: Catalog{
		Tools: []*mcp.Tool{twoArgTool()}, Prompts: []*mcp.Prompt{{Name: "p"}}, Resources: []*mcp.Resource{{Name: "r", URI: "file:///r"}},
	}}
	e := &Engine{}
	r := Request{Route: "direct", Servers: []*Server{s}, Caller: map[string]any{"userId": "u", "orgId": "org-a"}, Client: map[string]any{"capabilities": map[string]any{"elicitation": map[string]any{}}}, Now: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)}
	return e, r
}

// unlistedRead is an engine, a direct request and the params of a read of
// file:///secret.txt on a server that lists nothing, so the resource has a
// URI and no other field.
func unlistedRead(t *testing.T, rules ...Rule) (*Engine, Request, *mcp.ReadResourceParams) {
	t.Helper()
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: mustCompileUncapped(t, rules...)}
	r := Request{Route: "direct", Principal: "principal-1", Servers: []*Server{s}, Client: map[string]any{"capabilities": map[string]any{"elicitation": map[string]any{}}}}
	return &Engine{}, r, &mcp.ReadResourceParams{URI: "file:///secret.txt"}
}

// ---- questions and answers ------------------------------------------------------

// yesTo is the client's answer that accepts the gateway's confirmation asked
// under a key.
func yesTo(key string) mcp.InputResponseMap {
	return mcp.InputResponseMap{KeyPrefix + key: &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}}
}

// serverQuestion is a server's reply that asks its own question, k, and gives
// the request state UP.
const serverQuestion = `{"resultType":"input_required","requestState":"UP","inputRequests":{"k":{"method":"elicitation/create","params":{"message":"which?","requestedSchema":{"type":"object","properties":{}}}}}}`

// serverAnswer is the client's answer to serverQuestion.
func serverAnswer() mcp.InputResponseMap {
	return mcp.InputResponseMap{"k": &mcp.ElicitResult{Action: "accept", Content: map[string]any{}}}
}

// requestState is the request state of an input_required result, or "".
func requestState(o Outcome) string {
	if ir, ok := o.Result.(InputRequired); ok {
		return ir.RequestState
	}
	return ""
}

// ---- replies and streams --------------------------------------------------------

// toolReply calls tool t and hands the engine the server's reply.
func toolReply(t *testing.T, s *Server, reply string) Outcome {
	t.Helper()
	e, r, p := &Engine{}, newRequest("direct", s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"client"}`)}
	o := e.CallTool(context.Background(), r, p)
	if o.Call == nil {
		t.Fatalf("the call was not forwarded: %s", outcomeText(o))
	}
	return e.UpstreamToolReply(context.Background(), r, o.Call, p, json.RawMessage(reply))
}

// readReply reads a URI and hands the engine the server's reply.
func readReply(t *testing.T, s *Server, uri, reply string) Outcome {
	t.Helper()
	e, r, p := &Engine{}, newRequest("direct", s), &mcp.ReadResourceParams{URI: uri}
	o := e.ReadResource(context.Background(), r, p)
	if o.Call == nil {
		t.Fatalf("the read of %s was not forwarded: %s", uri, outcomeText(o))
	}
	return e.UpstreamReadReply(context.Background(), r, o.Call, p, json.RawMessage(reply))
}

// upstreamSeq plays events as an upstream would, and records how far the
// engine read and whether it let go of the stream.
type upstreamSeq struct {
	events []UpstreamEvent
	fail   error // yielded after the events, when set
	pulled int
	closed bool
}

func (u *upstreamSeq) seq() iter.Seq2[UpstreamEvent, error] {
	return func(yield func(UpstreamEvent, error) bool) {
		defer func() { u.closed = true }()
		for _, ev := range u.events {
			u.pulled++
			if !yield(ev, nil) {
				return
			}
		}
		if u.fail != nil {
			yield(UpstreamEvent{}, u.fail)
		}
	}
}

// progressEvent is a progress notification from the server.
func progressEvent(params string) UpstreamEvent {
	return UpstreamEvent{Method: ProgressMethod, Params: json.RawMessage(params)}
}

// resultEvent is the server's final result.
func resultEvent(result string) UpstreamEvent {
	return UpstreamEvent{Result: json.RawMessage(result)}
}

// reply is everything one stream yielded.
type reply struct {
	notes []ReplyEvent
	final *Outcome
	err   error
	order []string // "note" or "final", in the order yielded
}

// collect drains a reply stream.
func collect(seq iter.Seq2[ReplyEvent, error]) reply {
	var r reply
	for ev, err := range seq {
		switch {
		case err != nil:
			r.err = err
		case ev.Final != nil:
			r.final = ev.Final
			r.order = append(r.order, "final")
		default:
			r.notes = append(r.notes, ev)
			r.order = append(r.order, "note")
		}
	}
	return r
}

// streamCase is one tool behind one rule set, with a forwarded call ready
// for its reply.
type streamCase struct {
	e      *Engine
	r      Request
	call   *CallContext
	params *mcp.CallToolParamsRaw
}

// newStreamCase forwards a call to typedTool on a route, under rules and
// limits.
func newStreamCase(t *testing.T, route string, rules []Rule, limits Limits) *streamCase {
	t.Helper()
	e := &Engine{Limits: limits}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: mustCompile(t, rules), Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
	r := newRequest(route, s)
	name := "t"
	if route == "mux" {
		name = "a" + Separator + "t"
	}
	p := &mcp.CallToolParamsRaw{Name: name, Arguments: json.RawMessage(`{"q":"abc","limit":5}`)}
	first := e.CallTool(context.Background(), r, p)
	if first.Forward == nil {
		t.Fatalf("the call was not forwarded: %+v %+v", first.Error, first.Result)
	}
	return &streamCase{e: e, r: r, call: first.Call, params: p}
}

// run plays an upstream's events as the reply to the case's call.
func (c *streamCase) run(u *upstreamSeq) reply {
	return collect(c.e.ToolReplyStream(context.Background(), c.r, c.call, c.params, u.seq()))
}

// ---- reading an outcome ---------------------------------------------------------

// outcomeText is the result and the error of an outcome as JSON, as a client
// gets them.
func outcomeText(o Outcome) string {
	b, _ := json.Marshal(struct {
		Result any       `json:"result"`
		Error  *RPCError `json:"error"`
	}{o.Result, o.Error})
	return string(b)
}

// asJSON is a value as JSON.
func asJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// plainJSON is a value as JSON with <, > and & left as they are.
func plainJSON(v any) string {
	b, _ := json.Marshal(v)
	var x any
	_ = json.Unmarshal(b, &x)
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(x)
	return sb.String()
}

// hitErrors lists the effect and the error of each hit.
func hitErrors(hs []Hit) string {
	var out []string
	for _, h := range hs {
		out = append(out, h.Effect+":"+h.Error)
	}
	return strings.Join(out, "; ")
}

// schemaError validates a document against the JSON Schema of a hook's tree.
func schemaError(t *testing.T, hook string, doc map[string]any) error {
	t.Helper()
	raw, _ := json.Marshal(Hooks[hook].JSONSchema())
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("%s: the schema does not parse: %v", hook, err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatalf("%s: the schema does not resolve: %v", hook, err)
	}
	b, _ := json.Marshal(doc)
	var instance any
	_ = json.Unmarshal(b, &instance)
	return resolved.Validate(instance)
}

// ---- panics ---------------------------------------------------------------------

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

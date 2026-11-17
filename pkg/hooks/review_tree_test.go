package hooks

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tests from the independent review of the rule language as a tree
// (docs/rules/review-findings.md). A test whose name starts with
// TestReviewGap asserts behaviour the engine does not have today: it is
// skipped unless HOOKS_KNOWN_GAPS=1 is set. The others pass and show an area
// that was attacked and held.
//
//	go test ./pkg/hooks/ -run 'TestReview' -count=1
//	HOOKS_KNOWN_GAPS=1 go test ./pkg/hooks/ -run 'TestReviewGap' -count=1

// ---- helpers ---------------------------------------------------------------------

func rvEngine() *Engine { return &Engine{} }

func rvServer(t *testing.T, cat Catalog, rules ...Rule) *Server {
	t.Helper()
	return &Server{Slug: "a", Generation: "1", Info: ServerInfo{Name: "Docs", ProtocolVersion: ProtocolCurrent}, Rules: mustCompile(t, rules), Catalog: cat}
}

var rvSchema = map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}

func rvTools() Catalog { return Catalog{Tools: []*mcp.Tool{{Name: "t", InputSchema: rvSchema}}} }

func rvJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// rvPlain is a value as JSON with <, > and & left as they are.
func rvPlain(v any) string {
	b, _ := json.Marshal(v)
	var x any
	_ = json.Unmarshal(b, &x)
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(x)
	return sb.String()
}

func rvErrs(hs []Hit) string {
	var out []string
	for _, h := range hs {
		out = append(out, h.Effect+":"+h.Error)
	}
	return strings.Join(out, "; ")
}

// rvSave compiles one rule and returns its problems and its notes.
func rvSave(hook, when string, then map[string]string) (Problems, Problems) {
	if then == nil {
		then = map[string]string{KeyDecision: "'deny'"}
	}
	c, probs := compileRule(0, Rule{Hook: hook, When: when, Then: then}, Options{CostLimit: DefaultCostLimit})
	if c == nil {
		return probs, nil
	}
	return probs, c.warnings
}

var rvYes = mcp.InputResponseMap{KeyPrefix + "sure": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}}

const rvQuestion = `{"resultType":"input_required","requestState":"UP","inputRequests":{"k":{"method":"elicitation/create","params":{"message":"which?","requestedSchema":{"type":"object","properties":{}}}}}}`

func rvAnswer() mcp.InputResponseMap {
	return mcp.InputResponseMap{"k": &mcp.ElicitResult{Action: "accept", Content: map[string]any{}}}
}

// rvToolReply calls tool t and hands the engine the server's reply.
func rvToolReply(t *testing.T, s *Server, reply string) Outcome {
	t.Helper()
	r, p := treeRequest(s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"client"}`)}
	o := rvEngine().CallTool(context.Background(), r, p)
	if o.Call == nil {
		t.Fatalf("the call was not forwarded: %s", outcomeText(o))
	}
	return rvEngine().UpstreamToolReply(context.Background(), r, o.Call, p, json.RawMessage(reply))
}

// rvRead reads a URI and hands the engine the server's reply.
func rvRead(t *testing.T, s *Server, uri, reply string) Outcome {
	t.Helper()
	r, p := treeRequest(s), &mcp.ReadResourceParams{URI: uri}
	o := rvEngine().ReadResource(context.Background(), r, p)
	if o.Call == nil {
		t.Fatalf("the read of %s was not forwarded: %s", uri, outcomeText(o))
	}
	return rvEngine().UpstreamReadReply(context.Background(), r, o.Call, p, json.RawMessage(reply))
}

func rvSchemaErr(t *testing.T, hook string, doc map[string]any) error {
	t.Helper()
	raw, _ := json.Marshal(Hooks[hook].JSONSchema())
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("%s: %v", hook, err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatalf("%s: %v", hook, err)
	}
	b, _ := json.Marshal(doc)
	var instance any
	_ = json.Unmarshal(b, &instance)
	return resolved.Validate(instance)
}

// ==== sound: areas that were attacked and held =====================================

// After a rename of the tool and of a parameter, with a default, a hidden pin
// and a pre rule that adds an argument, every reply hook reads the client's
// names on one side and the server's on the other. That holds on the first
// round and on the round after the server asked its own question, where the
// request arrives again with a request state.
func TestReviewBothSidesOnEveryToolHookAfterAServersQuestion(t *testing.T) {
	ctx := context.Background()
	sides := "tool.name + '<' + tool.upstream.name + ' ' + request.params.name + '<' + request.upstream.params.name" +
		" + ' client=' + request.params.arguments.query + '/' + string(has(request.params.arguments.cql)) + '/' + string(has(request.params.arguments.space))" +
		" + ' server=' + request.upstream.params.arguments.cql + '/' + request.upstream.params.arguments.space + '/' + string(request.upstream.params.arguments.limit) + '/' + string(has(request.upstream.params.arguments.query))" +
		" + ' params=' + string('query' in tool.params) + string('cql' in tool.params) + ' up=' + string('cql' in tool.upstream.inputSchema.properties) + string('query' in tool.upstream.inputSchema.properties)" +
		" + ' desc=' + tool.description + '<' + tool.upstream.description"
	want := "baz<foo baz<foo client=q+later/false/false server=q+later/ENG/20/false params=truefalse up=truefalse desc=gateway words<server words"
	cat := Catalog{Tools: []*mcp.Tool{{Name: "foo", Description: "server words", InputSchema: map[string]any{"type": "object", "properties": map[string]any{
		"cql": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}, "space": map[string]any{"type": "string"}, "note": map[string]any{"type": "string"}}}}}}
	s := rvServer(t, cat,
		Rule{Hook: "tools/list.post", When: "tool.upstream.name == 'foo'", Then: map[string]string{"tool.name": "'baz'", "tool.params.cql.name": "'query'", "tool.params.limit.default": "20", "tool.params.space.pin": "'ENG'", "tool.params.space.hide": "true", "tool.description": "'gateway words'"}},
		Rule{Hook: "tools/call.pre", When: "tool.name == 'baz' && tool.upstream.name == 'foo' && request.params.name == 'baz'", Then: map[string]string{"request.params.arguments.note": "'pre'"}},
		// A later rule sees the earlier rule's change.
		Rule{Hook: "tools/call.pre", When: "request.params.arguments.note == 'pre'", Then: map[string]string{"request.params.arguments.query": "request.params.arguments.query + '+later'"}},
		Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{KeyConfirm: "elicit('sure', 'Really?')"}},
		Rule{Hook: "tools/call.post.progress", When: "true", Then: map[string]string{"progress.params.message": sides}},
		Rule{Hook: "tools/call.post.progress", When: "true", Then: map[string]string{"progress.params.message": "progress.params.message + ' | server said ' + progress.upstream.params.message"}},
		Rule{Hook: "tools/call.post.elicit", When: "true", Then: map[string]string{"elicitation.params.message": sides}},
		Rule{Hook: "tools/call.post.elicit", When: "true", Then: map[string]string{"elicitation.params.message": "elicitation.params.message + ' | server said ' + elicitation.upstream.params.message"}},
		Rule{Hook: "tools/call.post.result", When: "true", Then: map[string]string{"response.result.content": "[{'type': 'text', 'text': " + sides + " + ' | server said ' + response.upstream.result.content[0].text}]"}},
	)
	e, r := rvEngine(), treeRequest(s)
	for name, args := range map[string]string{"foo": `{"query":"q"}`, "baz": `{"cql":"q"}`} {
		if o := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: name, Arguments: json.RawMessage(args)}); o.Forward != nil || o.Error == nil {
			t.Errorf("a call to %s with %s was not refused: %s", name, args, outcomeText(o))
		}
	}
	args := json.RawMessage(`{"query":"q"}`)
	asked := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "baz", Arguments: args})
	ir, ok := asked.Result.(InputRequired)
	if !ok {
		t.Fatalf("the gateway did not ask: %s", outcomeText(asked))
	}
	p2 := &mcp.CallToolParamsRaw{Name: "baz", Arguments: args, RequestState: ir.RequestState, InputResponses: rvYes}
	o2 := e.CallTool(ctx, r, p2)
	if o2.Forward == nil {
		t.Fatalf("the confirmed call was not forwarded: %s", outcomeText(o2))
	}
	if got := rvJSON(o2.Forward.Params); !strings.Contains(got, `"name":"foo"`) || !strings.Contains(got, `"cql":"q+later"`) || !strings.Contains(got, `"space":"ENG"`) || !strings.Contains(got, `"limit":20`) {
		t.Errorf("forwarded %s", got)
	}
	first := collect(e.ToolReplyStream(ctx, r, o2.Call, p2, (&upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progress":1,"message":"half"}`), resultEvent(rvQuestion)}}).seq()))
	if len(first.notes) != 1 || !strings.Contains(rvPlain(first.notes[0].Params), want+" | server said half") {
		t.Errorf("progress on the first round: %s", rvJSON(first.notes))
	}
	if got := rvPlain(first.final.Result); !strings.Contains(got, want+" | server said which?") {
		t.Errorf("elicit hook on the first round: %s", got)
	}
	ir3, ok := first.final.Result.(InputRequired)
	if !ok {
		t.Fatalf("the server's question did not reach the client: %s", outcomeText(*first.final))
	}
	p3 := &mcp.CallToolParamsRaw{Name: "baz", Arguments: args, RequestState: ir3.RequestState, InputResponses: rvAnswer()}
	o3 := e.CallTool(ctx, r, p3)
	if o3.Forward == nil {
		t.Fatalf("the answer to the server's question was not forwarded: %s", outcomeText(o3))
	}
	second := collect(e.ToolReplyStream(ctx, r, o3.Call, p3, (&upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progress":2,"message":"done"}`), resultEvent(`{"content":[{"type":"text","text":"ok"}]}`)}}).seq()))
	if len(second.notes) != 1 || !strings.Contains(rvPlain(second.notes[0].Params), want+" | server said done") {
		t.Errorf("progress after the continuation: %s", rvJSON(second.notes))
	}
	if got := rvPlain(second.final.Result); !strings.Contains(got, want+" | server said ok") {
		t.Errorf("result hook after the continuation: %s", got)
	}
}

// The same for a prompt whose name and argument are renamed: the list hook,
// the pre hook, the progress hook and the elicit hook, before and after a
// continuation round.
func TestReviewBothSidesOnEveryPromptHook(t *testing.T) {
	ctx := context.Background()
	sides := "prompt.name + '<' + prompt.upstream.name + ' ' + request.params.name + '<' + request.upstream.params.name" +
		" + ' client=' + request.params.arguments.area + '/' + string(has(request.params.arguments.space))" +
		" + ' server=' + request.upstream.params.arguments.space + '/' + string(has(request.upstream.params.arguments.area))" +
		" + ' args=' + prompt.arguments.map(a, a.name).join(',') + '<' + prompt.upstream.arguments.map(a, a.name).join(',')" +
		" + ' params=' + string('area' in prompt.params) + string('space' in prompt.params) + ' desc=' + prompt.description + '<' + prompt.upstream.description"
	want := "summary<confluence_summary summary<confluence_summary client=ENG+later/false server=ENG+later/false args=area,tone<space,tone params=truefalse desc=gateway words<server words"
	cat := Catalog{Prompts: []*mcp.Prompt{{Name: "confluence_summary", Description: "server words", Arguments: []*mcp.PromptArgument{{Name: "space", Required: true}, {Name: "tone"}}}}}
	s := rvServer(t, cat,
		Rule{Hook: "prompts/list.post", When: "prompt.upstream.name == 'confluence_summary'", Then: map[string]string{"prompt.name": "'summary'", "prompt.params.space.name": "'area'", "prompt.description": "'gateway words'"}},
		Rule{Hook: "prompts/list.post", When: "prompt.name == 'summary'", Then: map[string]string{"prompt.title": "prompt.name + '<' + prompt.upstream.name + ' ' + prompt.arguments.map(a, a.name).join(',') + '<' + prompt.upstream.arguments.map(a, a.name).join(',')"}},
		Rule{Hook: "prompts/get.pre", When: "prompt.name == 'summary' && prompt.upstream.name == 'confluence_summary' && request.params.name == 'summary'", Then: map[string]string{"request.params.arguments.tone": "'pre'"}},
		Rule{Hook: "prompts/get.pre", When: "request.params.arguments.tone == 'pre'", Then: map[string]string{"request.params.arguments.area": "request.params.arguments.area + '+later'"}},
		Rule{Hook: "prompts/get.post.progress", When: "true", Then: map[string]string{"progress.params.message": sides}},
		Rule{Hook: "prompts/get.post.elicit", When: "true", Then: map[string]string{"elicitation.params.message": sides}},
	)
	e, r := rvEngine(), treeRequest(s)
	if got := rvPlain(e.ListPrompts(ctx, r).Result); !strings.Contains(got, `"title":"summary<confluence_summary area,tone<space,tone"`) {
		t.Errorf("list hook: %s", got)
	}
	if o := e.GetPrompt(ctx, r, &mcp.GetPromptParams{Name: "confluence_summary", Arguments: map[string]string{"area": "ENG"}}); o.Forward != nil {
		t.Errorf("a get by the server's name was forwarded")
	}
	if o := e.GetPrompt(ctx, r, &mcp.GetPromptParams{Name: "summary", Arguments: map[string]string{"space": "ENG"}}); o.Forward != nil {
		t.Errorf("a get with the server's argument name was forwarded")
	}
	p := &mcp.GetPromptParams{Name: "summary", Arguments: map[string]string{"area": "ENG"}}
	o := e.GetPrompt(ctx, r, p)
	if o.Forward == nil {
		t.Fatalf("not forwarded: %s", outcomeText(o))
	}
	if got := rvJSON(o.Forward.Params); !strings.Contains(got, `"name":"confluence_summary"`) || !strings.Contains(got, `"space":"ENG+later"`) || strings.Contains(got, `"area"`) {
		t.Errorf("forwarded %s", got)
	}
	first := collect(e.PromptReplyStream(ctx, r, o.Call, p, (&upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progress":1,"message":"half"}`), resultEvent(rvQuestion)}}).seq()))
	if len(first.notes) != 1 || !strings.Contains(rvPlain(first.notes[0].Params), want) {
		t.Errorf("progress hook: %s", rvJSON(first.notes))
	}
	if got := rvPlain(first.final.Result); !strings.Contains(got, want) {
		t.Errorf("elicit hook: %s", got)
	}
	ir, ok := first.final.Result.(InputRequired)
	if !ok {
		t.Fatalf("the server's question did not reach the client")
	}
	p2 := &mcp.GetPromptParams{Name: "summary", Arguments: map[string]string{"area": "ENG"}, RequestState: ir.RequestState, InputResponses: rvAnswer()}
	o2 := e.GetPrompt(ctx, r, p2)
	if o2.Forward == nil {
		t.Fatalf("the continuation was not forwarded: %s", outcomeText(o2))
	}
	second := collect(e.PromptReplyStream(ctx, r, o2.Call, p2, (&upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progress":2,"message":"done"}`), resultEvent(`{"messages":[]}`)}}).seq()))
	if len(second.notes) != 1 || !strings.Contains(rvPlain(second.notes[0].Params), want) {
		t.Errorf("progress hook after the continuation: %s", rvJSON(second.notes))
	}
}

// A resource a list rule retitled: every read hook sees the gateway's title
// on the item and the server's under upstream, on the direct route and on
// the mux, where the client's URI carries the slug and rules see it without.
func TestReviewBothSidesOnEveryResourceHook(t *testing.T) {
	ctx := context.Background()
	sides := "resource.uri + ' ' + resource.title + '<' + resource.upstream.title + ' ' + request.params.uri + '<' + request.upstream.params.uri"
	want := "docs://a gateway title<server title docs://a<docs://a"
	cat := Catalog{Resources: []*mcp.Resource{{URI: "docs://a", Name: "a", Title: "server title"}}}
	s := rvServer(t, cat,
		Rule{Hook: "resources/list.post", When: "true", Then: map[string]string{"resource.title": "'gateway title'"}},
		Rule{Hook: "resources/list.post", When: "resource.title == 'gateway title' && resource.upstream.title == 'server title'", Then: map[string]string{"resource.description": "'later rule saw both'"}},
		Rule{Hook: "resources/read.pre", When: "resource.title != 'gateway title' || resource.upstream.title != 'server title' || request.params.uri != resource.uri", Then: map[string]string{KeyDecision: "'deny'", KeyReason: "'pre saw the wrong sides'"}},
		Rule{Hook: "resources/read.post.progress", When: "true", Then: map[string]string{"progress.params.message": sides}},
		Rule{Hook: "resources/read.post.elicit", When: "true", Then: map[string]string{"elicitation.params.message": sides}},
		Rule{Hook: "resources/read.post.result", When: "true", Then: map[string]string{"response.result.contents": "response.result.contents.map(c, {'uri': c.uri, 'text': " + sides + " + ' | server said ' + response.upstream.result.contents[0].text})"}},
	)
	e := rvEngine()
	for route, uri := range map[string]string{"direct": "docs://a", "mux": "a+docs://a"} {
		r := treeRequest(s)
		r.Route = route
		if got := rvJSON(e.ListResources(ctx, r).Result); !strings.Contains(got, "later rule saw both") {
			t.Errorf("%s list hook: %s", route, got)
		}
		p := &mcp.ReadResourceParams{URI: uri}
		o := e.ReadResource(ctx, r, p)
		if o.Forward == nil {
			t.Fatalf("%s: not forwarded: %s", route, outcomeText(o))
		}
		first := collect(e.ReadReplyStream(ctx, r, o.Call, p, (&upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progress":1,"message":"half"}`), resultEvent(rvQuestion)}}).seq()))
		if len(first.notes) != 1 || !strings.Contains(rvPlain(first.notes[0].Params), want) {
			t.Errorf("%s progress hook: %s", route, rvJSON(first.notes))
		}
		if got := rvPlain(first.final.Result); !strings.Contains(got, want) {
			t.Errorf("%s elicit hook: %s", route, got)
		}
		ir, ok := first.final.Result.(InputRequired)
		if !ok {
			t.Fatalf("%s: the server's question did not reach the client", route)
		}
		p2 := &mcp.ReadResourceParams{URI: uri, RequestState: ir.RequestState, InputResponses: rvAnswer()}
		o2 := e.ReadResource(ctx, r, p2)
		if o2.Call == nil {
			t.Fatalf("%s: the continuation was not forwarded: %s", route, outcomeText(o2))
		}
		done := e.UpstreamReadReply(ctx, r, o2.Call, p2, json.RawMessage(`{"contents":[{"uri":"docs://a","text":"A"}]}`))
		if got := rvPlain(done.Result); !strings.Contains(got, want+" | server said A") {
			t.Errorf("%s result hook after the continuation: %s", route, got)
		}
	}
}

// The save-time check does not see a branch reached through a computed key,
// cel.bind, a macro over a list, a ternary or a literal. Each of those still
// fails when the request arrives, and a failed enforce rule refuses the call.
func TestReviewPathsTheSaveCheckMissesStillDeny(t *testing.T) {
	for _, when := range []string{
		"request[('up' + 'stream')].params.name == 'x'",
		"(true ? request : tool).nosuch == 1",
		"{'a': request}.a.nosuch == 1",
		"[request][0].nosuch == 1",
		"cel.bind(t, dyn(tool), t.title == 'x')",
	} {
		if probs, _ := rvSave("tools/call.pre", when, nil); probs != nil {
			t.Errorf("%s: refused when saved: %v", when, probs)
			continue
		}
		s := rvServer(t, rvTools(), Rule{Hook: "tools/call.pre", When: when, Then: map[string]string{KeyDecision: "'deny'", KeyReason: "'matched'"}})
		o := rvEngine().CallTool(context.Background(), treeRequest(s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`)})
		if o.Forward != nil || !strings.Contains(outcomeText(o), "refused by policy") {
			t.Errorf("%s: forwarded=%v %s", when, o.Forward != nil, outcomeText(o))
		}
	}
	// A leading dot names the branch from the root and is checked like any other read.
	if probs, _ := rvSave("tools/call.pre", ".request.upstream.params.name == 'x'", nil); probs == nil {
		t.Error(".request.upstream compiled on a pre hook")
	}
	// Flags are still read by name through the forms the walker sees.
	for _, when := range []string{"'search' in request.flags", "cel.bind(f, request.flags, 'serach' in f)", "request.flags[tool.name]", "request.flags['serach']", "request.?flags.hasValue()"} {
		if probs, _ := rvSave("tools/call.pre", when, nil); probs == nil {
			t.Errorf("%s compiled", when)
		}
	}
}

// Nothing outside the hook's own branch can be set, and nothing inside it
// that the tree does not mark.
func TestReviewOnlyTheHooksOwnBranchCanBeSet(t *testing.T) {
	for _, c := range [][3]string{
		{"tools/list.post", "tool.upstream.name", "'x'"}, {"tools/list.post", "tool._meta", "{}"}, {"tools/list.post", "tool._meta.a.b", "'x'"},
		{"tools/list.post", "tool.annotations", "{}"}, {"tools/list.post", "tool.inputSchema", "{}"}, {"tools/list.post", "tool.inputSchema.properties.q.description", "'x'"},
		{"tools/list.post", "tool.params.q", "{}"}, {"tools/list.post", "tool.params.q.type", "'x'"}, {"tools/list.post", "tool.params.*.hide", "true"}, {"tools/list.post", "tool.params..hide", "true"},
		{"tools/list.post", "tool.icons", "[]"}, {"tools/list.post", "request.params.arguments.x", "1"}, {"tools/list.post", "timeout", "duration('1s')"}, {"tools/list.post", KeyConfirm, "{}"},
		{"tools/call.pre", "tool.name", "'x'"}, {"tools/call.pre", "request.params.name", "'x'"}, {"tools/call.pre", "request.params.arguments", "{}"}, {"tools/call.pre", "request.params._meta.x", "'x'"},
		{"tools/call.pre", "request.params.arguments.a.b", "'x'"}, {"tools/call.pre", "request.caller.role", "'x'"}, {"tools/call.pre", "request.confirmations.x", "{}"}, {"tools/call.pre", "request.headers.x", "'x'"},
		{"tools/call.pre", "request.upstream.params.arguments.x", "'x'"}, {"tools/call.pre", "request.params.timeout", "duration('1s')"}, {"tools/call.pre", "timeout.x", "'x'"},
		{"tools/call.post.result", "response.upstream.result.content", "[]"}, {"tools/call.post.result", "response.result.isError", "false"}, {"tools/call.post.result", "response.result._meta.x", "1"},
		{"tools/call.post.result", "response.result.content.0", "{}"}, {"tools/call.post.result", "response.result.structuredContent.a", "1"}, {"tools/call.post.result", "response.result", "{}"},
		{"tools/call.post.result", "request.params.arguments.x", "1"}, {"tools/call.post.result", "tool.name", "'x'"},
		{"tools/call.post.elicit", "elicitation.params.requestedSchema", "{}"}, {"tools/call.post.elicit", "elicitation.params.foo", "1"}, {"tools/call.post.elicit", "elicitation.upstream.params.message", "'x'"}, {"tools/call.post.elicit", "elicitation.key", "'x'"},
		{"tools/call.post.progress", "progress.params.progress", "1"}, {"tools/call.post.progress", "progress.upstream.params.message", "'x'"},
		{"resources/read.post.result", "response.result.contents.0.text", "'x'"}, {"resources/read.post.result", "response.result._meta.x", "'x'"}, {"resources/read.post.result", "response.upstream.result.contents", "[]"},
		{"resources/read.pre", "request.params.uri", "'x'"}, {"resources/list.post", "resource.uri", "'x'"}, {"resources/list.post", "resource.name", "'x'"}, {"resources/list.post", "resource.upstream.title", "'x'"},
		{"prompts/list.post", "prompt.arguments", "[]"}, {"prompts/list.post", "prompt.params.tone.required", "true"}, {"prompts/list.post", "prompt.params.tone", "{}"}, {"prompts/list.post", "prompt.upstream.name", "'x'"},
		{"server/discover.post", "server.slug", "'x'"}, {"server/discover.post", "server.upstream.title", "'x'"}, {"server/discover.post", "server.name", "'x'"},
		{"server.settings", "settings.nosuch", "true"}, {"server.session", "session.capabilities", "{}"}, {"server.session", "session.capabilities.sampling", "true"}, {"skills/list.post", "skill.name", "'x'"},
	} {
		if probs, _ := rvSave(c[0], "true", map[string]string{c[1]: c[2]}); probs == nil {
			t.Errorf("%s: set %s compiled", c[0], c[1])
		}
	}
}

// The read result hook runs once over the whole response. A rule cannot
// bring in an entry the read did not return, in any of the forms tried; an
// entry the caller may not read is gone before rules run and is not under
// response.upstream either; one entry that makes a rule fail refuses the
// whole read; an audit rule changes nothing.
func TestReviewAReadResultCannotBringInAnEntry(t *testing.T) {
	cat := Catalog{Resources: []*mcp.Resource{{URI: "docs://a", Name: "a"}, {URI: "docs://b", Name: "b"}, {URI: "docs://hidden", Name: "hidden"}}}
	hide := Rule{Hook: "resources/list.post", When: "resource.uri == 'docs://hidden'", Then: map[string]string{KeyDecision: "'deny'"}}
	set := func(expr string) Rule {
		return Rule{Hook: "resources/read.post.result", When: "true", Then: map[string]string{"response.result.contents": expr}}
	}
	reply := `{"contents":[{"uri":"docs://a","text":"A"},{"uri":"docs://b","text":"B"},{"uri":"docs://hidden","text":"HIDDEN"}]}`
	for name, rule := range map[string]Rule{
		"the hidden entry":        set("response.result.contents + [{'uri': 'docs://hidden', 'text': 'x'}]"),
		"a new URI":               set("response.result.contents + [{'uri': 'docs://new', 'text': 'x'}]"),
		"an entry with no URI":    set("response.result.contents + [{'text': 'x'}]"),
		"an entry that is no map": set("response.result.contents + ['x']"),
		"a URI that is a number":  set("[{'uri': 5, 'text': 'x'}]"),
		"a trailing slash":        set("[{'uri': 'docs://a/', 'text': 'x'}]"),
	} {
		o := rvRead(t, rvServer(t, cat, hide, rule), "docs://a", reply)
		if o.Error == nil || strings.Contains(outcomeText(o), "HIDDEN") {
			t.Errorf("a rule added %s: %s", name, outcomeText(o))
		}
	}
	o := rvRead(t, rvServer(t, cat, hide, set("response.upstream.result.contents")), "docs://a", reply)
	if got := outcomeText(o); o.Error != nil || strings.Contains(got, "HIDDEN") || !strings.Contains(got, `"text":"B"`) {
		t.Errorf("response.upstream held an entry the caller may not read: %s", got)
	}
	o = rvRead(t, rvServer(t, cat, hide, Rule{Hook: "resources/read.post.result", When: "response.result.contents.exists(c, c.text.contains('secret'))", Then: map[string]string{KeyDecision: "'deny'"}}),
		"docs://a", `{"contents":[{"uri":"docs://a","text":"A"},{"uri":"docs://b","blob":"QUJD"}]}`)
	if o.Error == nil {
		t.Errorf("an entry the rule could not evaluate passed with the rest: %s", outcomeText(o))
	}
	o = rvRead(t, rvServer(t, cat, hide, set("response.result.contents.filter(c, !c.text.contains('secret'))")),
		"docs://a", `{"contents":[{"uri":"docs://a","text":"A"},{"uri":"docs://b","blob":"QUJD"}]}`)
	if o.Error == nil {
		t.Errorf("a set whose value could not be evaluated passed: %s", outcomeText(o))
	}
	o = rvRead(t, rvServer(t, cat, hide, Rule{Hook: "resources/read.post.result", Mode: ModeAudit, When: "true", Then: map[string]string{"response.result.contents": "[]"}}), "docs://a", reply)
	if got := outcomeText(o); !strings.Contains(got, `"text":"A"`) || !strings.Contains(got, `"text":"B"`) {
		t.Errorf("an audit rule changed the contents: %s", got)
	}
}

// A file a skill's manifest vouches for keeps its bytes whatever a rule that
// depends on the request sets: new text, a blob in place of the text, or the
// file's URI written another way. A rule can still drop the entry.
func TestReviewAManifestFileKeepsItsBytes(t *testing.T) {
	text := "---\nname: demo\ndescription: d\n---\nbody"
	cat := Catalog{Skills: []Skill{{URI: "skill://demo/SKILL.md", Frontmatter: map[string]any{"name": "demo", "description": "d"},
		Resources: json.RawMessage(`[{"uri":"skill://demo/SKILL.md","digest":"sha256:x","size":10}]`), Files: map[string]File{"skill://demo/SKILL.md": {Text: &text}}}}}
	reply := rvJSON(map[string]any{"contents": []any{map[string]any{"uri": "skill://demo/SKILL.md", "text": text}}})
	for name, expr := range map[string]string{
		"new text":         "response.result.contents.map(c, {'uri': c.uri, 'text': 'EVIL'})",
		"a blob":           "response.result.contents.map(c, {'uri': c.uri, 'blob': 'RVZJTA=='})",
		"the URI in caps":  "response.result.contents.map(c, {'uri': 'SKILL://demo/SKILL.md', 'text': 'EVIL'})",
		"a second entry":   "response.result.contents + [{'uri': 'skill://demo/SKILL.md', 'text': 'EVIL'}]",
		"a dotted variant": "[{'uri': 'skill://demo/x/../SKILL.md', 'text': 'EVIL'}]",
	} {
		s := rvServer(t, cat, Rule{Hook: "resources/read.post.result", When: "request.caller.role == 'member'", Then: map[string]string{"response.result.contents": expr}})
		o := rvRead(t, s, "skill://demo/SKILL.md", reply)
		if got := outcomeText(o); strings.Contains(got, "EVIL") || strings.Contains(got, "RVZJTA") || (o.Error == nil && !strings.Contains(got, `body`)) {
			t.Errorf("%s changed the file: %s", name, got)
		}
	}
	s := rvServer(t, cat, Rule{Hook: "resources/read.post.result", When: "request.caller.role == 'member'", Then: map[string]string{"response.result.contents": "[]"}})
	if got := outcomeText(rvRead(t, s, "skill://demo/SKILL.md", reply)); !strings.Contains(got, `"contents":[]`) {
		t.Errorf("a rule could not drop the entry: %s", got)
	}
}

// A gateway confirmation is satisfied only through its own state: not by
// input responses alone, not for other arguments, not by another principal,
// not twice, and not by anything but accept with the box ticked.
func TestReviewAConfirmationNeedsItsOwnState(t *testing.T) {
	ctx := context.Background()
	s := rvServer(t, rvTools(), Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{KeyConfirm: "elicit('sure', 'Really?')"}})
	e, r := rvEngine(), treeRequest(s)
	args := json.RawMessage(`{"q":"x"}`)
	ask := func() string {
		t.Helper()
		ir, ok := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args}).Result.(InputRequired)
		if !ok {
			t.Fatal("the gateway did not ask")
		}
		return ir.RequestState
	}
	refused := func(what string, o Outcome) {
		t.Helper()
		if o.Forward != nil {
			t.Errorf("%s: the call was forwarded", what)
		}
	}
	refused("input responses with no state", e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, InputResponses: rvYes}))
	refused("a made-up state key", e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: "c_00000000000000000000000000000000", InputResponses: rvYes}))
	refused("the state with no answer", e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: ask()}))
	refused("other arguments", e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"y"}`), RequestState: ask(), InputResponses: rvYes}))
	other := treeRequest(s)
	other.Principal = "someone-else"
	state := ask()
	refused("another principal", e.CallTool(ctx, other, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: state, InputResponses: rvYes}))
	refused("the owner, after another principal spent the state", e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: state, InputResponses: rvYes}))
	for _, ans := range []*mcp.ElicitResult{{Action: "decline"}, {Action: "accept"}, {Action: "accept", Content: map[string]any{"confirm": false}}, {Action: "accept", Content: map[string]any{"confirm": "true"}}, {Action: "ACCEPT", Content: map[string]any{"confirm": true}}} {
		refused("the answer "+rvJSON(ans), e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: ask(), InputResponses: mcp.InputResponseMap{KeyPrefix + "sure": ans}}))
	}
	// State lives in the gateway's store. Anything a client writes for
	// itself is not a key the store issued.
	forged := "eyJtIjoidG9vbHMvY2FsbCIsImEiOnsic3VyZSI6eyJhY3Rpb24iOiJhY2NlcHQifX19.c2lnbmVk"
	refused("state the client wrote itself", e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: forged}))
	state = ask()
	p := &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: state, InputResponses: rvYes}
	if o := e.CallTool(ctx, r, p); o.Forward == nil {
		t.Fatalf("a confirmed call was not forwarded: %s", outcomeText(o))
	}
	refused("the same state a second time", e.CallTool(ctx, r, p))
}

// A value of the wrong type fails the rule when the request arrives, and the
// failure is closed: the switches go off, the tool is hidden, the call is refused.
func TestReviewAWrongTypedSetFailsClosed(t *testing.T) {
	ctx := context.Background()
	s := rvServer(t, rvTools(), Rule{Hook: "server.settings", When: "true", Then: map[string]string{"settings.prompts": "dyn('off')"}})
	if got := rvJSON(rvEngine().ListTools(ctx, treeRequest(s)).Result); !strings.Contains(got, `"tools":[]`) {
		t.Errorf("a settings rule that failed left tools listed: %s", got)
	}
	s = rvServer(t, rvTools(), Rule{Hook: "tools/list.post", When: "true", Then: map[string]string{"tool.annotations.readOnlyHint": "dyn('yes')"}})
	if got := rvJSON(rvEngine().ListTools(ctx, treeRequest(s)).Result); !strings.Contains(got, `"tools":[]`) {
		t.Errorf("a list rule that failed left the tool listed: %s", got)
	}
	s = rvServer(t, rvTools(), Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{"timeout": "dyn('30s')"}})
	if o := rvEngine().CallTool(ctx, treeRequest(s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`)}); o.Forward != nil {
		t.Error("a call rule that failed let the call through")
	}
	s = rvServer(t, rvTools(), Rule{Hook: "tools/call.post.result", When: "true", Then: map[string]string{"response.result.content": "null"}})
	if got := outcomeText(rvToolReply(t, s, `{"content":[{"type":"text","text":"SECRET"}]}`)); strings.Contains(got, "SECRET") {
		t.Errorf("a result rule that failed let the result through: %s", got)
	}
}

// CEL takes // comments. The engine compiles a condition that holds one as
// the text before it. This is the engine's half of the text-layer finding
// "a // comment swallows the continuation lines": the text layer joins
// continuation lines with a space, so everything after the comment is lost.
func TestReviewACELCommentRunsToTheEndOfTheExpression(t *testing.T) {
	cat := Catalog{Tools: []*mcp.Tool{{Name: "a", InputSchema: rvSchema}, {Name: "b", InputSchema: rvSchema}}}
	s := rvServer(t, cat, Rule{Hook: "tools/list.post", When: "tool.name == 'a' // or the other one || tool.name == 'b'", Then: map[string]string{KeyDecision: "'deny'"}})
	if got := rvJSON(rvEngine().ListTools(context.Background(), treeRequest(s)).Result); !strings.Contains(got, `"name":"b"`) || strings.Contains(got, `"name":"a"`) {
		t.Errorf("listed %s", got)
	}
}

// ==== known gaps: each fails today =================================================

// Finding 1. One confirmation, one forwarded call. When the server answers a
// confirmed call with its own question, the gateway hands the client a
// signed envelope that carries the confirmation. Nothing spends it, so the
// client can present it again and the call is forwarded again.
func TestReviewFixedConfirmationReplayedThroughAServersQuestion(t *testing.T) {
	ctx := context.Background()
	s := rvServer(t, rvTools(), Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{KeyConfirm: "elicit('sure', 'Really?')"}})
	e, r := rvEngine(), treeRequest(s)
	args := json.RawMessage(`{"q":"x"}`)
	ir := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args}).Result.(InputRequired)
	p := &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: ir.RequestState, InputResponses: rvYes}
	confirmed := e.CallTool(ctx, r, p)
	if confirmed.Call == nil {
		t.Fatalf("the confirmed call was not forwarded: %s", outcomeText(confirmed))
	}
	envelope := e.UpstreamToolReply(ctx, r, confirmed.Call, p, json.RawMessage(rvQuestion)).Result.(InputRequired).RequestState
	forwards := 0
	for i := 0; i < 3; i++ {
		if o := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: envelope, InputResponses: rvAnswer()}); o.Forward != nil {
			forwards++
		}
	}
	if o := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: envelope}); o.Forward != nil {
		forwards++
	}
	if forwards > 1 {
		t.Errorf("one confirmation forwarded the call %d more times through the same envelope; want at most 1", forwards)
	}
}

// Finding 2. A confirmation is bound to the arguments the client sent, not
// to the ones a pre rule set before the question. A rule that takes an
// argument from the request's _meta or headers is asked about one value and
// forwards another.
func TestReviewFixedConfirmationCoversArgumentsARuleSet(t *testing.T) {
	ctx := context.Background()
	cat := Catalog{Tools: []*mcp.Tool{{Name: "deploy", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"env": map[string]any{"type": "string"}}}}}}
	s := rvServer(t, cat,
		Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{"request.params.arguments.env": "request.params.?_meta.?env.orValue('staging')"}},
		Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{KeyConfirm: "elicit('sure', 'Deploy to {env}?', {'env': request.params.arguments.env})"}})
	e, r := rvEngine(), treeRequest(s)
	asked := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "deploy", Arguments: json.RawMessage(`{}`), Meta: mcp.Meta{"env": "staging"}})
	ir := asked.Result.(InputRequired)
	if !strings.Contains(outcomeText(asked), `Deploy to \"staging\"?`) {
		t.Fatalf("asked %s", outcomeText(asked))
	}
	o := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "deploy", Arguments: json.RawMessage(`{}`), Meta: mcp.Meta{"env": "production"}, RequestState: ir.RequestState, InputResponses: rvYes})
	if o.Forward != nil && strings.Contains(rvJSON(o.Forward.Params), `"env":"production"`) {
		t.Errorf("the user confirmed staging and the gateway forwarded %s", rvJSON(o.Forward.Params))
	}
}

// Finding 3. A scheme is not case sensitive, and the engine says so itself
// when it compares a URI with the listing. A rule and a denied template are
// given the URI as the client typed it, so FILE:///secrets/key passes a deny
// of file:///secrets/ and is forwarded as typed. A server that parses the
// URI reads the same file.
func TestReviewFixedSchemeInCapitalsPassesADeny(t *testing.T) {
	ctx := context.Background()
	if u, err := url.Parse("FILE:///secrets/key"); err != nil || u.Scheme != "file" {
		t.Fatalf("the stated condition does not hold: net/url reads the scheme as %q", u.Scheme)
	}
	cat := Catalog{
		Resources: []*mcp.Resource{{URI: "file:///listed/secret.txt", Name: "listed"}},
		Templates: []*mcp.ResourceTemplate{{URITemplate: "file:///vault/{name}", Name: "vault"}},
	}
	s := rvServer(t, cat,
		Rule{Hook: "resources/list.post", When: "resource.uri.startsWith('file:///secrets/')", Then: map[string]string{KeyDecision: "'deny'"}},
		Rule{Hook: "resources/list.post", When: "resource.uri == 'file:///listed/secret.txt'", Then: map[string]string{KeyDecision: "'deny'"}},
		Rule{Hook: "resources/templates/list.post", When: "template.uriTemplate.startsWith('file:///vault/')", Then: map[string]string{KeyDecision: "'deny'"}},
	)
	for _, uri := range []string{"file:///secrets/key", "file:///vault/a", "file:///listed/secret.txt", "FILE:///listed/secret.txt"} {
		if o := rvEngine().ReadResource(ctx, treeRequest(s), &mcp.ReadResourceParams{URI: uri}); o.Forward != nil {
			t.Fatalf("control: %s was forwarded", uri)
		}
	}
	for _, uri := range []string{"FILE:///secrets/key", "File:///secrets/key", "FILE:///vault/a"} {
		if o := rvEngine().ReadResource(ctx, treeRequest(s), &mcp.ReadResourceParams{URI: uri}); o.Forward != nil {
			t.Errorf("%s was forwarded as %s", uri, rvJSON(o.Forward.Params))
		}
	}
}

// Finding 4. A `then` key is split with backslash escapes when it is checked
// against the tree, and matched as plain text when the hook's branch is
// taken off it. A key with an escape in the branch part passes the check and
// then reaches the setter whole: the set does nothing, or lands on a key of
// that literal name, and the hit still says "set".
func TestReviewFixedEscapedSetKey(t *testing.T) {
	ctx := context.Background()
	saved := func(hook, key, val string) bool {
		probs, _ := rvSave(hook, "true", map[string]string{key: val})
		return probs == nil
	}
	if saved("tools/call.post.result", `respons\e.result.content`, "[]") {
		s := rvServer(t, rvTools(), Rule{Hook: "tools/call.post.result", When: "true", Then: map[string]string{`respons\e.result.content`: "[{'type': 'text', 'text': 'REDACTED'}]"}})
		if got := outcomeText(rvToolReply(t, s, `{"content":[{"type":"text","text":"SECRET"}]}`)); strings.Contains(got, "SECRET") || strings.Contains(got, `respons\\e`) {
			t.Errorf("result hook: the set was accepted and the client got %s", got)
		}
	}
	if saved("server.settings", `setting\s.tools`, "false") {
		s := rvServer(t, rvTools(), Rule{Hook: "server.settings", When: "true", Then: map[string]string{`setting\s.tools`: "false"}})
		if got := rvJSON(rvEngine().ListTools(ctx, treeRequest(s)).Result); strings.Contains(got, `"name":"t"`) {
			t.Errorf("settings: tools were switched off and are still listed: %s", got)
		}
	}
	if saved("tools/call.pre", `reques\t.params.arguments.q`, "'x'") {
		s := rvServer(t, rvTools(), Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{`reques\t.params.arguments.q`: "'pinned'"}})
		o := rvEngine().CallTool(ctx, treeRequest(s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"client"}`)})
		if o.Forward != nil && !strings.Contains(rvJSON(o.Forward.Params), `"q":"pinned"`) {
			t.Errorf("pre hook: the argument was set to 'pinned' and the server got %s", rvJSON(o.Forward.Params))
		}
	}
	if saved("tools/list.post", `to\ol.name`, "'x'") {
		s := rvServer(t, rvTools(), Rule{Hook: "tools/list.post", When: "true", Then: map[string]string{`to\ol.name`: "'renamed'"}})
		if got := rvJSON(rvEngine().ListTools(ctx, treeRequest(s)).Result); !strings.Contains(got, `"name":"renamed"`) {
			t.Errorf("list hook: the tool was renamed and is listed as %s", got)
		}
	}
	if saved("tools/call.post.elicit", `elicitatio\n.params.message`, "'x'") {
		s := rvServer(t, rvTools(), Rule{Hook: "tools/call.post.elicit", When: "true", Then: map[string]string{`elicitatio\n.params.message`: "'SAFE'"}})
		if got := outcomeText(rvToolReply(t, s, rvQuestion)); !strings.Contains(got, "SAFE") {
			t.Errorf("elicit hook: the message was set and the client got %s", got)
		}
	}
}

// Finding 5. tool.annotations takes any key on the list hook. A key MCP does
// not name is kept for the next list rule and then lost: it is not on the
// wire and not on the tool the call hooks read.
func TestReviewFixedUnknownAnnotationIsDropped(t *testing.T) {
	ctx := context.Background()
	rules := []Rule{
		{Hook: "tools/list.post", When: "true", Then: map[string]string{"tool.annotations.risk": "'high'"}},
		{Hook: "tools/list.post", When: "true", Then: map[string]string{"tool.description": "'risk=' + tool.annotations.risk"}},
		{Hook: "tools/call.pre", When: "cel.bind(a, tool.?annotations.orValue({}), a.?risk.orValue('low')) == 'high'", Then: map[string]string{KeyDecision: "'deny'", KeyReason: "'high risk'"}},
	}
	if _, probs := Compile(rules); probs != nil {
		return // refusing the set when the rule is saved is a fix
	}
	s := rvServer(t, rvTools(), rules...)
	listed := rvJSON(rvEngine().ListTools(ctx, treeRequest(s)).Result)
	if !strings.Contains(listed, "risk=high") {
		t.Fatalf("the next list rule did not see the annotation: %s", listed)
	}
	if !strings.Contains(listed, `"risk":"high"`) {
		t.Errorf("the client's listing does not carry the annotation: %s", listed)
	}
	if o := rvEngine().CallTool(ctx, treeRequest(s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`)}); o.Forward != nil {
		t.Errorf("the call hook did not see the annotation a list rule set: the call was forwarded")
	}
}

// Finding 6. The catalog is held in the SDK's types, which drop a zero. A
// priority of 0 and a `required` of false, which the server sent, are absent
// to rules and gone from the wire. A rule that hides low-priority resources
// with its own default lets the lowest one through.
func TestReviewGapAZeroTheServerSentIsAbsent(t *testing.T) {
	knownGap(t)
	ctx := context.Background()
	var low mcp.Resource
	if err := json.Unmarshal([]byte(`{"uri":"docs://low","name":"low","annotations":{"priority":0}}`), &low); err != nil {
		t.Fatal(err)
	}
	var p mcp.Prompt
	if err := json.Unmarshal([]byte(`{"name":"p","arguments":[{"name":"tone","required":false}]}`), &p); err != nil {
		t.Fatal(err)
	}
	s := rvServer(t, Catalog{Resources: []*mcp.Resource{&low}, Prompts: []*mcp.Prompt{&p}},
		Rule{Hook: "resources/list.post", When: "resource.?annotations.?priority.orValue(1.0) < 0.5", Then: map[string]string{KeyDecision: "'deny'"}},
		Rule{Hook: "prompts/list.post", When: "true", Then: map[string]string{"prompt.description": "prompt.arguments.map(a, has(a.required) ? 'sent' : 'absent').join(',')"}},
	)
	if got := rvJSON(rvEngine().ListResources(ctx, treeRequest(s)).Result); strings.Contains(got, "docs://low") {
		t.Errorf("a resource the server gave priority 0 read as having no priority and was listed: %s", got)
	}
	if got := rvJSON(rvEngine().ListPrompts(ctx, treeRequest(s)).Result); !strings.Contains(got, `"description":"sent"`) {
		t.Errorf("an argument the server marked required: false read as %s", got)
	}
}

// Finding 7. The tree and the document on a result hook. (a) MCP requires
// content, so the tree marks it always there; a server that leaves it out
// has sent a result that is not MCP's, and a rule that reads content fails,
// which refuses the result. (b) the result carries resultType, which the
// tree now has. (c) elicitation.upstream holds the server's params only.
func TestReviewFixedResultTreeAndDocument(t *testing.T) {
	when := "response.result.content.exists(c, c.type == 'image')"
	s := rvServer(t, rvTools(), Rule{Hook: "tools/call.post.result", When: when, Then: map[string]string{KeyDecision: "'deny'"}})
	o := rvToolReply(t, s, `{"structuredContent":{"a":1}}`)
	if !strings.Contains(outcomeText(o), "refused by policy") {
		t.Errorf("(a) a result with no content passed a rule that reads content: %s", outcomeText(o))
	}

	r := treeRequest(s)
	call := rvEngine().CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`)})
	doc := call.Call.roots(r)
	doc["tool"] = subject(KindTool, call.Call.resolve)
	result, _ := decodeExact([]byte(`{"content":[{"type":"text","text":"hi"}],"resultType":"complete"}`)).(map[string]any)
	up := map[string]any{"result": celMap(result)}
	doc["response"] = map[string]any{"result": up["result"], "upstream": up} // as UpstreamToolReply builds it
	if err := rvSchemaErr(t, "tools/call.post.result", doc); err != nil {
		t.Errorf("(b) the document of a result that has resultType does not match the hook's schema: %v", err)
	}

	for _, read := range []string{"elicitation.upstream.method == 'elicitation/create'", "elicitation.upstream.key == 'k'"} {
		probs, notes := rvSave("tools/call.post.elicit", read, map[string]string{"elicitation.params.message": "'CHANGED'"})
		if probs != nil || len(notes) > 0 {
			continue // refused or noted when saved is a fix
		}
		s := rvServer(t, rvTools(), Rule{Hook: "tools/call.post.elicit", When: read, Then: map[string]string{"elicitation.params.message": "'CHANGED'"}})
		if o := rvToolReply(t, s, rvQuestion); !strings.Contains(outcomeText(o), "CHANGED") {
			t.Errorf("(c) %s compiled with no note and failed on a real question: %s", read, rvErrs(o.Hits))
		}
	}
}

// Finding 8. A macro variable that has the name of a branch is checked as if
// it were the branch, so a valid rule is refused.
func TestReviewFixedMacroVariableNamedLikeABranch(t *testing.T) {
	for hook, when := range map[string]string{
		"resources/read.post.result": "response.result.contents.exists(resource, resource.text.contains('x'))",
		"tools/call.post.result":     "response.result.content.filter(tool, tool.type == 'text').size() > 0",
		"tools/call.pre":             "[{'nosuch': 1}].exists(request, request.nosuch == 1)",
	} {
		if probs, _ := rvSave(hook, when, nil); probs != nil {
			t.Errorf("%s: %s was refused: %v", hook, when, probs)
		}
	}
}

// Finding 9. The note on an optional read misses the common forms and fires
// on guarded ones. Missed: a field of a list entry read through a macro
// variable, a header, a confirmation. Fired: a read behind has() in the same
// condition, and a plain step after .?, which CEL treats as optional too.
func TestReviewFixedNotesMissAndMisfire(t *testing.T) {
	for hook, when := range map[string]string{
		"tools/call.post.result":     "response.result.content.exists(c, c.text.contains('secret'))",
		"resources/read.post.result": "response.result.contents.exists(c, c.text.contains('secret'))",
		"tools/call.pre":             "request.headers['x-team'] == 'sre'",
	} {
		if probs, notes := rvSave(hook, when, nil); probs == nil && len(notes) == 0 {
			t.Errorf("missed: %s on %s reads a value that may be absent and has no note", when, hook)
		}
	}
	for _, when := range []string{"has(tool.title) && tool.title == 'x'", "tool.?annotations.destructiveHint.orValue(true)"} {
		if _, notes := rvSave("tools/list.post", when, nil); len(notes) > 0 {
			t.Errorf("misfired: %s is guarded and was noted: %v", when, notes)
		}
	}
	// !has() in the condition hides the note on a read that then always fails.
	if _, notes := rvSave("tools/list.post", "!has(tool.title)", map[string]string{"tool.description": "tool.title"}); len(notes) == 0 {
		t.Error("missed: a value read only when has() says it is absent has no note")
	}
}

// Finding 10. A value whose type is known when the rule is saved is not
// checked against the path's type until a request arrives.
func TestReviewFixedWrongTypedConstantIsSaved(t *testing.T) {
	for _, c := range [][3]string{
		{"server.settings", "settings.tools", "'off'"},
		{"tools/call.pre", "timeout", "'30s'"},
		{"tools/list.post", "tool.annotations.readOnlyHint", "'yes'"},
		{"tools/list.post", "tool.name", "5"},
		{"prompts/get.pre", "request.params.arguments.tone", "5"},
		{"tools/call.post.result", "response.result.content", "'text'"},
	} {
		if probs, _ := rvSave(c[0], "true", map[string]string{c[1]: c[2]}); probs == nil {
			t.Errorf("%s: set %s = %s was saved", c[0], c[1], c[2])
		}
	}
}

// Finding 11. Every entry under a skill's folder has its bytes put back
// after the rules run, not only the files the manifest lists. A rule's
// change to an entry the manifest does not vouch for is lost, with a hit
// that says "set" and no issue.
func TestReviewFixedChangeToAFileUnderASkillFolderIsLost(t *testing.T) {
	text := "---\nname: demo\ndescription: d\n---\nbody"
	cat := Catalog{Skills: []Skill{{URI: "skill://demo/SKILL.md", Frontmatter: map[string]any{"name": "demo", "description": "d"},
		Resources: json.RawMessage(`[{"uri":"skill://demo/SKILL.md","digest":"sha256:x","size":10}]`), Files: map[string]File{"skill://demo/SKILL.md": {Text: &text}}}}}
	s := rvServer(t, cat, Rule{Hook: "resources/read.post.result", When: "true", Then: map[string]string{
		"response.result.contents": "response.result.contents.map(c, {'uri': c.uri, 'text': c.text.replace('hunter2', '[redacted]')})"}})
	o := rvRead(t, s, "skill://demo/notes.md", `{"contents":[{"uri":"skill://demo/notes.md","text":"the password is hunter2"}]}`)
	if got := outcomeText(o); strings.Contains(got, "hunter2") && len(o.Issues) == 0 {
		t.Errorf("the manifest does not list notes.md, the rule redacted it, and the client got %s with no issue", got)
	}
}

// Finding 12. response.upstream is the server's response. For a skill file
// that the gateway rewrote ahead of time it holds the rewritten text.
func TestReviewFixedResponseUpstreamOfASkillFile(t *testing.T) {
	text := "---\nname: demo\ndescription: d\n---\nserver body"
	cat := Catalog{Skills: []Skill{{URI: "skill://demo/SKILL.md", Frontmatter: map[string]any{"name": "demo", "description": "d"},
		Resources: json.RawMessage(`[{"uri":"skill://demo/SKILL.md","digest":"sha256:x","size":10}]`), Files: map[string]File{"skill://demo/SKILL.md": {Text: &text}}}}}
	s := rvServer(t, cat,
		Rule{Hook: "resources/read.post.result", When: "true", Then: map[string]string{"response.result.contents": "response.result.contents.map(c, c.uri.endsWith('/SKILL.md') ? {'uri': c.uri, 'text': c.text.replace('server body', 'gateway body')} : c)"}},
		Rule{Hook: "resources/read.post.result", When: "true", Then: map[string]string{"response.result.contents": "response.result.contents.map(c, c.uri.endsWith('/probe') ? {'uri': c.uri, 'text': response.upstream.result.contents[0].text} : c)"}},
	)
	reply := rvJSON(map[string]any{"contents": []any{map[string]any{"uri": "skill://demo/SKILL.md", "text": text}, map[string]any{"uri": "docs://other/probe", "text": "-"}}})
	o := rvRead(t, s, "skill://demo/SKILL.md", reply)
	contents, _ := o.Result.(map[string]any)["contents"].([]any)
	if len(contents) != 2 {
		t.Fatalf("got %s", outcomeText(o))
	}
	if seen, _ := contents[1].(map[string]any)["text"].(string); !strings.Contains(seen, "server body") {
		t.Errorf("response.upstream held %q, not what the server sent", seen)
	}
}

// Finding 13. The variable that carries the mux context to slugUris, and the
// function behind the macro, can be named by a rule. Neither is in the tree.
func TestReviewFixedInternalVariableIsReadable(t *testing.T) {
	for _, when := range []string{"__mux.slug == 'a'", "size(__mux.owned.exact) > 0", "__slugUris('see docs://x', {'route': 'mux', 'slug': 'other', 'owned': {'exact': ['docs://x']}}) != ''"} {
		if probs, _ := rvSave("resources/read.post.result", when, nil); probs == nil {
			t.Errorf("%s compiled", when)
		}
	}
}

// Finding 14, recorded and not changed. On a read of a URI the server does
// not list, only the list rules that deny are run: there is no listing for
// a rule to change. A deny that reads a mark an earlier rule set, with its
// own default, does not see the mark there. A deny written against the URI
// itself covers both.
func TestReviewAnUnlistedReadRunsOnlyTheRulesThatDeny(t *testing.T) {
	ctx := context.Background()
	cat := Catalog{Resources: []*mcp.Resource{{URI: "file:///private/listed", Name: "x"}}}
	s := rvServer(t, cat,
		Rule{Hook: "resources/list.post", When: "resource.uri.startsWith('file:///private/')", Then: map[string]string{"resource._meta.private": "true"}},
		Rule{Hook: "resources/list.post", When: "resource.?_meta.?private.orValue(false)", Then: map[string]string{KeyDecision: "'deny'"}},
	)
	if o := rvEngine().ReadResource(ctx, treeRequest(s), &mcp.ReadResourceParams{URI: "file:///private/listed"}); o.Forward != nil {
		t.Fatal("control: the listed resource was forwarded")
	}
	if o := rvEngine().ReadResource(ctx, treeRequest(s), &mcp.ReadResourceParams{URI: "file:///private/unlisted"}); o.Forward == nil {
		t.Error("the mark was seen on an unlisted URI; the docs say it is not")
	}
	s = rvServer(t, cat, Rule{Hook: "resources/list.post", When: "resource.uri.startsWith('file:///private/')", Then: map[string]string{KeyDecision: "'deny'"}})
	for _, uri := range []string{"file:///private/listed", "file:///private/unlisted"} {
		if o := rvEngine().ReadResource(ctx, treeRequest(s), &mcp.ReadResourceParams{URI: uri}); o.Forward != nil {
			t.Errorf("%s passed a deny written against the URI", uri)
		}
	}
}

// Finding 15. An absent container reads as null, not as absent: a tool with
// no inputSchema, and a skill with no frontmatter. has() is true for both.
func TestReviewFixedAbsentContainerReadsAsNull(t *testing.T) {
	tool := subject(KindTool, newToolState(prepare(&mcp.Tool{Name: "bare"})).resolve)
	if v, ok := tool["inputSchema"]; ok {
		t.Errorf("tool.inputSchema is present as %v; tool.upstream has it: %v", v, tool["upstream"].(map[string]any)["inputSchema"] != nil)
	}
	sk := Skill{URI: "skill://dyn/SKILL.md", Resources: json.RawMessage(`"dynamic"`)}
	it := &item{kind: KindSkill, id: sk.URI, view: NewView(skillItem(sk))}
	if v, ok := subject(KindSkill, it.resolve)["frontmatter"]; ok {
		t.Errorf("skill.frontmatter is present as %s", rvJSON(v))
	}
}

// Finding 16. A set of a list is checked to be a list and no further. A
// result rule can send content blocks that are not objects, and a read rule
// an entry whose text is a number.
func TestReviewFixedMalformedContentIsAccepted(t *testing.T) {
	s := rvServer(t, rvTools(), Rule{Hook: "tools/call.post.result", When: "true", Then: map[string]string{"response.result.content": "[1, 'two', {'no': 'type'}]"}})
	if o := rvToolReply(t, s, `{"content":[]}`); strings.Contains(outcomeText(o), `"content":[1,"two"`) {
		t.Errorf("the client got %s", outcomeText(o))
	}
	s = rvServer(t, Catalog{Resources: []*mcp.Resource{{URI: "docs://a", Name: "a"}}}, Rule{Hook: "resources/read.post.result", When: "true", Then: map[string]string{"response.result.contents": "[{'uri': 'docs://a', 'text': 5, 'blob': [1]}]"}})
	if o := rvRead(t, s, "docs://a", `{"contents":[{"uri":"docs://a","text":"A"}]}`); strings.Contains(outcomeText(o), `"text":5`) {
		t.Errorf("the client got %s", outcomeText(o))
	}
}

// Finding 17. An engine with nothing configured does not take state a
// client wrote. There is no signing key to leave out: state is kept in the
// store and the client holds only a key the store issued.
func TestReviewFixedEngineWithNothingConfigured(t *testing.T) {
	s := rvServer(t, rvTools(), Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{KeyConfirm: "elicit('sure', 'Really?')"}})
	forged := "eyJtIjoidG9vbHMvY2FsbCIsImEiOnsic3VyZSI6eyJhY3Rpb24iOiJhY2NlcHQifX19."
	o := (&Engine{}).CallTool(context.Background(), treeRequest(s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"x"}`), RequestState: forged})
	if o.Forward != nil {
		t.Error("a client wrote its own confirmation and the call was forwarded")
	}
}

// Finding 18. A message the client can receive still uses a retired name.
func TestReviewFixedRetiredNameInAClientMessage(t *testing.T) {
	ctx := context.Background()
	s := rvServer(t, rvTools(), Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{KeyConfirm: "elicit('sure', 'Really?')"}})
	e, r := rvEngine(), treeRequest(s)
	args := json.RawMessage(`{"q":"x"}`)
	ir := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args}).Result.(InputRequired)
	o := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: ir.RequestState, InputResponses: rvYes})
	if got := outcomeText(o); strings.Contains(got, "has(input.") {
		t.Errorf("the client was told %s", got)
	}
}

package hooks

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tests from the verification pass over the fixes to the review findings
// (docs/rules/review-verification.md). Each one tries a fix beyond the case
// the first reviewer tested. A test named TestReviewVerifyGap… asserts
// behaviour the engine does not have today and calls knownGap first, so it
// is skipped unless HOOKS_KNOWN_GAPS=1 is set. The others pass and show
// where a fix held.
//
//	go test ./pkg/hooks/ -run 'TestReviewVerify' -count=1
//	HOOKS_KNOWN_GAPS=1 go test ./pkg/hooks/ -run 'TestReviewVerifyGap' -count=1

// ---- helpers ---------------------------------------------------------------------

func vfYes(key string) mcp.InputResponseMap {
	return mcp.InputResponseMap{KeyPrefix + key: &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}}
}

func vfConfirm(key, msg string) map[string]string {
	return map[string]string{KeyConfirm: "elicit('" + key + "', '" + msg + "')"}
}

// vfState is the request state of an input_required result, or "".
func vfState(o Outcome) string {
	if ir, ok := o.Result.(InputRequired); ok {
		return ir.RequestState
	}
	return ""
}

// vfJSONStore keeps state as JSON, as the gateway's own store does, so a
// test sees what survives being written and read back.
type vfJSONStore struct{ m map[string][]byte }

func (s *vfJSONStore) Put(key string, st State, _ time.Duration) {
	b, _ := json.Marshal(st)
	s.m[key] = b
}

func (s *vfJSONStore) Take(key string) (State, bool) {
	b, ok := s.m[key]
	delete(s.m, key)
	var st State
	if !ok || json.Unmarshal(b, &st) != nil {
		return State{}, false
	}
	return st, true
}

var vfAnyTool = Catalog{Tools: []*mcp.Tool{{Name: "t", InputSchema: map[string]any{"type": "object"}}}}

// ==== sound: where the fixes held ==================================================

// Findings 1 and 17. Every state key works once: the key of each of two
// gateway questions, and the key around the server's own question. That
// holds when the state is written out as JSON and read back, as the
// gateway's store does, with an argument an earlier rule set as a nested
// map. The same is true on prompts/get, resources/read and a directory read.
func TestReviewVerifyEveryStateKeyWorksOnce(t *testing.T) {
	ctx := context.Background()
	args := json.RawMessage(`{"q":"x","n":1,"m":{"a":[1,2.5]}}`)
	s := rvServer(t, vfAnyTool,
		Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{"request.params.arguments.added": "{'k': [1, 2.5, 'x'], 'n': 3}"}},
		Rule{Hook: "tools/call.pre", When: "true", Then: vfConfirm("a", "A?")},
		Rule{Hook: "tools/call.pre", When: "true", Then: vfConfirm("b", "B?")})
	e, r := &Engine{States: &vfJSONStore{m: map[string][]byte{}}}, treeRequest(s)
	call := func(state string, in mcp.InputResponseMap) (Outcome, *mcp.CallToolParamsRaw) {
		p := &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: state, InputResponses: in}
		return e.CallTool(ctx, r, p), p
	}
	o1, _ := call("", nil)
	k1 := vfState(o1)
	o2, _ := call(k1, vfYes("a"))
	k2 := vfState(o2)
	if k1 == "" || k2 == "" || k1 == k2 {
		t.Fatalf("the two questions were not asked in turn: %s / %s", outcomeText(o1), outcomeText(o2))
	}
	if o, _ := call(k1, vfYes("b")); o.Forward != nil {
		t.Error("the first key was taken a second time")
	}
	o3, p3 := call(k2, vfYes("b"))
	if o3.Call == nil {
		t.Fatalf("two confirmations did not forward the call: %s", outcomeText(o3))
	}
	k3 := vfState(e.UpstreamToolReply(ctx, r, o3.Call, p3, json.RawMessage(rvQuestion)))
	o4, _ := call(k3, rvAnswer())
	if o4.Forward == nil || !strings.Contains(rvJSON(o4.Forward.Params), `"requestState":"UP"`) {
		t.Fatalf("the answer to the server's question was not forwarded with the server's state: %s", outcomeText(o4))
	}
	for i, k := range []string{k1, k2, k3} {
		if o, _ := call(k, rvAnswer()); o.Forward != nil {
			t.Errorf("key %d forwarded the call again", i+1)
		}
		if o, _ := call(k, nil); o.Forward != nil {
			t.Errorf("key %d forwarded the call again with no answer", i+1)
		}
	}

	cat := Catalog{Prompts: []*mcp.Prompt{{Name: "p", Arguments: []*mcp.PromptArgument{{Name: "tone"}}}}, Resources: []*mcp.Resource{{URI: "docs://a", Name: "a"}, {URI: "docs://dir/", Name: "d"}}}
	s = rvServer(t, cat,
		Rule{Hook: "prompts/get.pre", When: "true", Then: vfConfirm("sure", "Really?")},
		Rule{Hook: "resources/read.pre", When: "true", Then: vfConfirm("sure", "Really?")})
	e, r = rvEngine(), treeRequest(s)
	pa := map[string]string{"tone": "x"}
	kp := vfState(e.GetPrompt(ctx, r, &mcp.GetPromptParams{Name: "p", Arguments: pa}))
	pp := &mcp.GetPromptParams{Name: "p", Arguments: pa, RequestState: kp, InputResponses: rvYes}
	op := e.GetPrompt(ctx, r, pp)
	if op.Call == nil {
		t.Fatalf("a confirmed prompts/get was not forwarded: %s", outcomeText(op))
	}
	if o := e.GetPrompt(ctx, r, pp); o.Forward != nil {
		t.Error("prompts/get: the key was taken twice")
	}
	ku := vfState(e.UpstreamPromptReply(ctx, r, op.Call, pp, json.RawMessage(rvQuestion)))
	n := 0
	for i := 0; i < 3; i++ {
		if o := e.GetPrompt(ctx, r, &mcp.GetPromptParams{Name: "p", Arguments: pa, RequestState: ku, InputResponses: rvAnswer()}); o.Forward != nil {
			n++
		}
	}
	if n != 1 {
		t.Errorf("prompts/get: the key around the server's question forwarded %d times, want 1", n)
	}
	for _, dir := range []bool{false, true} {
		uri, read := "docs://a", e.ReadResource
		if dir {
			uri, read = "docs://dir/", e.ReadDirectory
		}
		k := vfState(read(ctx, r, &mcp.ReadResourceParams{URI: uri}))
		pr := &mcp.ReadResourceParams{URI: uri, RequestState: k, InputResponses: rvYes}
		or := read(ctx, r, pr)
		if or.Call == nil {
			t.Fatalf("a confirmed read of %s was not forwarded: %s", uri, outcomeText(or))
		}
		if o := read(ctx, r, pr); o.Forward != nil {
			t.Errorf("%s: the key was taken twice", uri)
		}
		if dir {
			continue
		}
		ku := vfState(e.UpstreamReadReply(ctx, r, or.Call, pr, json.RawMessage(rvQuestion)))
		n := 0
		for i := 0; i < 3; i++ {
			if o := read(ctx, r, &mcp.ReadResourceParams{URI: uri, RequestState: ku, InputResponses: rvAnswer()}); o.Forward != nil {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s: the key around the server's question forwarded %d times, want 1", uri, n)
		}
	}
}

// Findings 1 and 2. An answer counts only for the question it was given
// to: not for another tool, another entitlement, another set of rules, an
// expired state, a key that was not asked, a second rule that uses the same
// key for other words, another method, or arguments with any other value.
// The same arguments with their keys in another order, with other white
// space or with an escaped letter are the same arguments. A number written
// another way (1.0 for 1) is refused, which is the safe side.
func TestReviewVerifyAnAnswerIsForItsOwnQuestion(t *testing.T) {
	ctx := context.Background()
	args := json.RawMessage(`{"q":"x"}`)
	cat := Catalog{Tools: []*mcp.Tool{{Name: "t", InputSchema: rvSchema}, {Name: "u", InputSchema: rvSchema}},
		Prompts: []*mcp.Prompt{{Name: "t"}}, Resources: []*mcp.Resource{{URI: "docs://a", Name: "a"}}}
	rule := Rule{Hook: "tools/call.pre", When: "true", Then: vfConfirm("sure", "Really?")}
	more := []Rule{rule, {Hook: "prompts/get.pre", When: "true", Then: vfConfirm("sure", "Really?")}, {Hook: "resources/read.pre", When: "true", Then: vfConfirm("sure", "Really?")}}
	s := rvServer(t, cat, more...)
	e, r := rvEngine(), treeRequest(s)
	ask := func() string {
		t.Helper()
		k := vfState(e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args}))
		if k == "" {
			t.Fatal("the gateway did not ask")
		}
		return k
	}
	refused := func(what string, o Outcome) {
		t.Helper()
		if o.Forward != nil {
			t.Errorf("%s: forwarded", what)
		}
	}
	refused("another tool", e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "u", Arguments: args, RequestState: ask(), InputResponses: rvYes}))
	refused("a prompt of the same name", e.GetPrompt(ctx, r, &mcp.GetPromptParams{Name: "t", RequestState: ask(), InputResponses: rvYes}))
	refused("a read", e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: "docs://a", RequestState: ask(), InputResponses: rvYes}))
	kr := vfState(e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: "docs://a"}))
	refused("a read's answer on a directory read", e.ReadDirectory(ctx, r, &mcp.ReadResourceParams{URI: "docs://a", RequestState: kr, InputResponses: rvYes}))
	s2 := rvServer(t, cat, more...)
	s2.Slug = "b"
	mux := treeRequest(s)
	mux.Route, mux.Servers = "mux", []*Server{s, s2}
	km := vfState(e.CallTool(ctx, mux, &mcp.CallToolParamsRaw{Name: "a__t", Arguments: args}))
	refused("another entitlement", e.CallTool(ctx, mux, &mcp.CallToolParamsRaw{Name: "b__t", Arguments: args, RequestState: km, InputResponses: rvYes}))
	changed := rvServer(t, cat, rule, Rule{Hook: "tools/call.pre", When: "false", Then: map[string]string{KeyDecision: "'deny'"}})
	refused("other rules", e.CallTool(ctx, treeRequest(changed), &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: ask(), InputResponses: rvYes}))
	late := treeRequest(s)
	late.Now = time.Now().Add(StateTTL + time.Minute)
	refused("an expired state", e.CallTool(ctx, late, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: ask(), InputResponses: rvYes}))
	both := vfYes("sure")
	both[KeyPrefix+"other"] = both[KeyPrefix+"sure"]
	refused("an answer to a key that was not asked", e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: ask(), InputResponses: both}))

	// One key, two questions: the answer to the first is not an answer to
	// the second.
	two := rvServer(t, rvTools(),
		Rule{Hook: "tools/call.pre", When: "true", Then: vfConfirm("sure", "Really?")},
		Rule{Hook: "tools/call.pre", When: "true", Then: vfConfirm("sure", "Delete everything?")})
	r2 := treeRequest(two)
	k := vfState(e.CallTool(ctx, r2, &mcp.CallToolParamsRaw{Name: "t", Arguments: args}))
	refused("the same key for other words", e.CallTool(ctx, r2, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: k, InputResponses: rvYes}))

	// The arguments in other encodings.
	enc := rvServer(t, vfAnyTool, rule)
	re := treeRequest(enc)
	base := `{"n":1,"m":{"a":[1,2,{"z":null}],"b":"x"},"big":12345678901234567890}`
	for alt, same := range map[string]bool{
		`{"m":{"b":"x","a":[1,2,{"z":null}]},"big":12345678901234567890,"n":1}`:                                 true,
		` { "n" : 1 , "m" : { "a" : [ 1 , 2 , { "z" : null } ] , "b" : "x" } , "big" : 12345678901234567890 } `: true,
		`{"n":1,"m":{"a":[1,2,{"z":null}],"b":"x"},"big":12345678901234567890}`:                                 true,
		`{"n":1.0,"m":{"a":[1,2,{"z":null}],"b":"x"},"big":12345678901234567890}`:                               true, // the same number, written another way
		`{"n":1e0,"m":{"a":[1,2,{"z":null}],"b":"x"},"big":12345678901234567890}`:                               true,
		`{"n":"1","m":{"a":[1,2,{"z":null}],"b":"x"},"big":12345678901234567890}`:                               false,
		`{"n":1,"m":{"a":[1,2,{"z":null}],"b":"x"},"big":12345678901234567891}`:                                 false,
		`{"n":1,"m":{"a":[2,1,{"z":null}],"b":"x"},"big":12345678901234567890}`:                                 false,
		`{"n":1,"m":{"a":[1,2,{}],"b":"x"},"big":12345678901234567890}`:                                         false,
	} {
		k := vfState(e.CallTool(ctx, re, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(base)}))
		o := e.CallTool(ctx, re, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(alt), RequestState: k, InputResponses: rvYes})
		if (o.Forward != nil) != same {
			t.Errorf("%s: forwarded=%v, want %v", alt, o.Forward != nil, same)
		}
	}
}

// Finding 2. What a question shows is covered, wherever the rule took it
// from: a value read straight from _meta in the question, with the argument
// set by a later rule, and a value cut to 80 characters in the question and
// set in full by the same rule. An audit rule that confirms never asks.
func TestReviewVerifyAQuestionCoversWhatItShows(t *testing.T) {
	ctx := context.Background()
	cat := Catalog{Tools: []*mcp.Tool{{Name: "deploy", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"env": map[string]any{"type": "string"}}}}}}
	swap := func(what, a, b string, rules ...Rule) {
		t.Helper()
		e, r := rvEngine(), treeRequest(rvServer(t, cat, rules...))
		k := vfState(e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "deploy", Arguments: json.RawMessage(`{}`), Meta: mcp.Meta{"env": a}}))
		if k == "" {
			t.Fatalf("%s: the gateway did not ask", what)
		}
		o := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "deploy", Arguments: json.RawMessage(`{}`), Meta: mcp.Meta{"env": b}, RequestState: k, InputResponses: rvYes})
		if o.Forward != nil {
			t.Errorf("%s: asked about %q and forwarded %s", what, a, rvJSON(o.Forward.Params))
		}
	}
	swap("shown from _meta, set later", "staging", "production",
		Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{KeyConfirm: "elicit('sure', 'Deploy to {env}?', {'env': request.params.?_meta.?env.orValue('staging')})"}},
		Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{"request.params.arguments.env": "request.params.?_meta.?env.orValue('staging')"}})
	long := strings.Repeat("a", 80)
	swap("shown cut to 80 characters", long+"-staging", long+"-production",
		Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{KeyConfirm: "elicit('sure', 'Deploy to {env}?', {'env': request.params._meta.env})", "request.params.arguments.env": "request.params._meta.env"}})

	s := rvServer(t, rvTools(), Rule{Hook: "tools/call.pre", Mode: ModeAudit, When: "true", Then: vfConfirm("a", "A?")})
	o := rvEngine().CallTool(ctx, treeRequest(s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"x"}`)})
	if o.Forward == nil || len(o.Hits) != 1 || o.Hits[0].Effect != "would elicit" {
		t.Errorf("an audit rule that confirms: forwarded=%v hits=%s", o.Forward != nil, rvJSON(o.Hits))
	}
}

// Finding 3. The scheme check holds on every form tried: on the mux with
// the slug in front, on a scheme with a "+", and with no "//". A dot
// segment written with escapes is still refused.
func TestReviewVerifyASchemeInCapitalsIsRefusedEverywhere(t *testing.T) {
	ctx := context.Background()
	cat := Catalog{Resources: []*mcp.Resource{{URI: "git+ssh://host/secret", Name: "g"}}}
	s := rvServer(t, cat, Rule{Hook: "resources/list.post", When: "resource.uri.startsWith('git+ssh://host/') || resource.uri.startsWith('file:///secrets/')", Then: map[string]string{KeyDecision: "'deny'"}})
	s2 := rvServer(t, Catalog{})
	s2.Slug = "b"
	mux := treeRequest(s)
	mux.Route, mux.Servers = "mux", []*Server{s, s2}
	for _, uri := range []string{"FILE:/secrets/key", "File:secrets/key", "file:///public/%2E%2E/secrets/key", "GIT+ssh://host/secret", "git+SSH://host/secret"} {
		if o := rvEngine().ReadResource(ctx, treeRequest(s), &mcp.ReadResourceParams{URI: uri}); o.Forward != nil {
			t.Errorf("direct: %s was forwarded", uri)
		}
	}
	for _, uri := range []string{"a+FILE:///secrets/key", "A+file:///secrets/key", "a+git+SSH://host/secret", "a+GIT+ssh://host/secret", "a+git+ssh://host/secret", "git+ssh://host/secret"} {
		o := rvEngine().ReadResource(ctx, mux, &mcp.ReadResourceParams{URI: uri})
		if o.Forward != nil && strings.Contains(strings.ToLower(rvJSON(o.Forward.Params)), `"git+ssh://host/secret"`) {
			t.Errorf("mux: %s reached the denied resource: %s", uri, rvJSON(o.Forward.Params))
		}
		if o.Forward != nil && strings.Contains(strings.ToLower(rvJSON(o.Forward.Params)), "/secrets/") {
			t.Errorf("mux: %s was forwarded: %s", uri, rvJSON(o.Forward.Params))
		}
	}
}

// Findings 8 and 9. The path check follows nested macros, two-variable
// macros, macros over maps, chains of cel.bind and variables that shadow a
// branch, and none of these valid rules is refused.
func TestReviewVerifyTheSaveCheckTakesValidRules(t *testing.T) {
	for _, x := range [][2]string{
		{"tools/call.post.result", "response.result.content.exists(i, c, c.type == 'text' && i < 3)"},
		{"tools/call.post.result", "response.result.content.map(tool, tool.type).exists(request, request == 'image')"},
		{"tools/call.post.result", "response.result.content.exists(c, has(c.annotations) && c.annotations.?audience.orValue([]).exists(response, response == 'user'))"},
		{"tools/call.post.result", "response.result.content.exists(c, c.custom.deep.thing == 1)"},
		{"tools/call.post.result", "cel.bind(r, response.result, cel.bind(c, r.content, c.exists(x, x.type == 'text')))"},
		{"tools/call.post.result", "cel.bind(tool, response.result, tool.content.size() > 0)"},
		{"tools/call.post.result", "cel.bind(request, {'a': 1}, request.a == 1)"},
		{"tools/call.post.result", "has(response.result.structuredContent) && response.result.structuredContent.all(k, v, k != 'secret')"},
		{"tools/call.post.result", "has(response.result.structuredContent) && response.result.structuredContent.items.exists(i, i.id == 1)"},
		{"tools/list.post", "tool.params.exists(n, p, p.?type.orValue('') == 'string')"},
		{"tools/list.post", "tool.inputSchema.?properties.orValue({}).exists(n, p, p.?type.orValue('') == 'string')"},
		{"tools/list.post", "tool.inputSchema.?properties.orValue({}).exists(n, tool.inputSchema.properties[n].?format.orValue('') == 'uri')"},
		{"tools/list.post", "[tool, tool.upstream].exists(x, x.name.startsWith('x'))"},
		{"tools/list.post", "{'a': tool}.exists(k, v, v.name == 'x')"},
		{"tools/list.post", "(true ? tool : tool.upstream).name == 'x'"},
		{"tools/list.post", "tool.?_meta[?'team/owner'].orValue('') == 'x'"},
		{"tools/list.post", "has(tool.inputSchema.oneOf) && tool.inputSchema['$defs'].x.type == 'string'"},
		{"tools/call.pre", "request.params.arguments.all(k, v, v != null) && request.caller.?groups.orValue([]).exists(g, g == 'sre')"},
		{"tools/call.pre", "request.flags.search || request.flags['cli']"},
		{"prompts/get.pre", "prompt.?arguments.orValue([]).exists(a, a.name == 'tone' && a.?required.orValue(false))"},
		{"prompts/get.pre", "prompt.params.exists(n, a, a.name == n)"},
		{"resources/read.post.result", "response.result.contents.exists(resource, resource.?text.orValue('') == resource.uri)"},
		{"skills/list.post", "has(skill.resources) && skill.resources != 'dynamic' && skill.resources.exists(r, r.uri.endsWith('.sh'))"},
	} {
		if probs, _ := rvSave(x[0], x[1], nil); probs != nil {
			t.Errorf("%s: %s was refused: %v", x[0], x[1], probs)
		}
	}
	// And these wrong ones are refused through the same forms.
	for _, x := range [][2]string{
		{"tools/call.post.result", "cel.bind(r, response.result, r.nosuch == 1)"},
		{"tools/call.post.result", "response[?'nosuch'].orValue(1) == 1"},
		{"tools/list.post", "tool.?icons.orValue([]).size() > 0 && tool.icons.exists(i, i.nosuch == 1)"},
		{"tools/call.pre", "cel.bind(f, request.flags, f.serach)"},
		{"tools/call.pre", "request.flags[request.params.arguments.q]"},
		{"tools/call.pre", "has(request.confirmations)"},
	} {
		if probs, _ := rvSave(x[0], x[1], nil); probs == nil {
			t.Errorf("%s: %s was saved", x[0], x[1])
		}
	}
}

// Finding 10. A value of the right type is not refused: null where the docs
// say null removes a key, a number as an int, a double or a uint, a list
// built by a macro, a value the checker cannot type.
func TestReviewVerifyTheTypeCheckTakesValidValues(t *testing.T) {
	for _, x := range [][3]string{
		{"tools/list.post", "tool.title", "tool.?title.orValue('x')"},
		{"tools/list.post", "tool.title", "dyn('x')"},
		{"tools/list.post", "tool.params.q.minimum", "1"},
		{"tools/list.post", "tool.params.q.minimum", "1.5"},
		{"tools/list.post", "tool.params.q.minimum", "1u"},
		{"tools/list.post", "tool.params.q.maximum", "math.least(1, 2.5)"},
		{"tools/list.post", "tool.params.q.enum", "['a', 1, true]"},
		{"tools/list.post", "tool.params.q.enum", "'a,b'.split(',')"},
		{"tools/list.post", "tool.params.q.enum", "tool.params.q.?enum.orValue([]) + ['x']"},
		{"tools/list.post", "tool.params.q.default", "{'a': 1}"},
		{"tools/list.post", "tool.params.q.pin", "null"},
		{"tools/list.post", "tool._meta.x", "null"},
		{"tools/list.post", "tool.outputSchema", "dropSchema(tool.?outputSchema.orValue({}), ['a'])"},
		{"resources/list.post", "resource.annotations", "{'priority': 0.5, 'audience': ['user']}"},
		{"resources/list.post", "resource.annotations.priority", "1"},
		{"tools/call.pre", "timeout", "duration('30s')"},
		{"tools/call.pre", "timeout", "request.time - timestamp('2026-01-01T00:00:00Z')"},
		{"tools/call.pre", "request.params.arguments.q", "[{'a': 1.5}]"},
		{"tools/call.pre", "request.params.arguments.q", "null"},
		{"prompts/get.pre", "request.params.arguments.tone", "string(5)"},
		{"tools/call.post.result", "response.result.structuredContent", "null"},
		{"tools/call.post.result", "response.result.content", "response.result.content.filter(c, c.type == 'text') + [{'type': 'text', 'text': 'x'}]"},
		{"server.session", "session.clientInfo", "request.client.?clientInfo.orValue({'name': 'x'})"},
	} {
		if probs, _ := rvSave(x[0], "true", map[string]string{x[1]: x[2]}); probs != nil {
			t.Errorf("%s: %s = %s was refused: %v", x[0], x[1], x[2], probs)
		}
	}
}

// ==== gaps: each fails today =======================================================

// Finding 3, not fixed beyond the scheme. A deny written against a URI is
// passed by other spellings of the same URI. The gateway forwards each one
// as typed. A server that reads the URI with a URL parser reads the denied
// resource: Go's net/url gives the same path for file:/x and file:///x and
// drops a fragment, and the WHATWG parser (Node, and so the TypeScript
// SDK's McpServer, which looks a resource up by new URL(uri).toString())
// also folds file://localhost/, a leading space, a tab inside the scheme,
// backslashes, the host's case and a default port.
func TestReviewVerifyFixedOtherSpellingsOfAURIPassADeny(t *testing.T) {
	ctx := context.Background()
	for _, pair := range [][2]string{{"file:/secrets/key", "file:///secrets/key"}, {"file:///listed/secret.txt#x", "file:///listed/secret.txt"}, {"file:/secrets/../secrets/key", "file:///secrets/key"}} {
		a, _ := url.Parse(pair[0])
		b, _ := url.Parse(pair[1])
		if a == nil || b == nil || a.Scheme != b.Scheme || a.Host != b.Host || a.JoinPath().Path != b.JoinPath().Path {
			t.Fatalf("the stated condition does not hold: net/url reads %s and %s differently", pair[0], pair[1])
		}
	}
	cat := Catalog{
		Resources: []*mcp.Resource{{URI: "file:///listed/secret.txt", Name: "listed"}, {URI: "https://wiki.example/private", Name: "w"}, {URI: "file:///listed/a%2Fb", Name: "enc"}},
		Templates: []*mcp.ResourceTemplate{{URITemplate: "file:///vault/{name}", Name: "vault"}},
	}
	s := rvServer(t, cat,
		Rule{Hook: "resources/list.post", When: "resource.uri.startsWith('file:///secrets/')", Then: map[string]string{KeyDecision: "'deny'"}},
		Rule{Hook: "resources/list.post", When: "resource.uri == 'file:///listed/secret.txt' || resource.uri == 'https://wiki.example/private' || resource.uri == 'file:///listed/a%2Fb'", Then: map[string]string{KeyDecision: "'deny'"}},
		Rule{Hook: "resources/templates/list.post", When: "template.uriTemplate.startsWith('file:///vault/')", Then: map[string]string{KeyDecision: "'deny'"}},
	)
	s2 := rvServer(t, Catalog{})
	s2.Slug = "b"
	mux := treeRequest(s)
	mux.Route, mux.Servers = "mux", []*Server{s, s2}
	for _, uri := range []string{"file:///secrets/key", "file:///listed/secret.txt", "https://wiki.example/private", "file:///listed/a%2Fb", "file:///vault/a"} {
		if o := rvEngine().ReadResource(ctx, treeRequest(s), &mcp.ReadResourceParams{URI: uri}); o.Forward != nil {
			t.Fatalf("control: %s was forwarded", uri)
		}
	}
	for what, uris := range map[string][]string{
		"a prefix deny": {"file:/secrets/key", "file://localhost/secrets/key", " file:///secrets/key", "fi\tle:///secrets/key", `file:\\\secrets\key`,
			"file:/secrets/../secrets/key", "file:/%73ecrets/key"},
		"an exact deny of a listed resource": {"file:/listed/secret.txt", "file://localhost/listed/secret.txt", "file:///listed/secret.txt#x", "file:///listed/secret.txt?x=1",
			"https://WIKI.example/private", "https://wiki.example:443/private", "https://wiki.example/private#x", "file:///listed/a%2fb"},
		"a denied template": {"file:/vault/a", "file://localhost/vault/a", "file:///vault/a#x", "file:///vault/a?x=1"},
	} {
		for _, uri := range uris {
			if o := rvEngine().ReadResource(ctx, treeRequest(s), &mcp.ReadResourceParams{URI: uri}); o.Forward != nil {
				t.Errorf("%s is passed by %q, forwarded as %s", what, uri, rvJSON(o.Forward.Params))
			}
			if o := rvEngine().ReadResource(ctx, mux, &mcp.ReadResourceParams{URI: "a+" + uri}); o.Forward != nil {
				t.Errorf("%s is passed on the mux by %q", what, "a+"+uri)
			}
		}
	}
	// The same on what a server returns: an entry for the denied file under
	// another spelling is passed to the client.
	o := rvRead(t, s, "file:///ok/a", `{"contents":[{"uri":"file:///ok/a","text":"A"},{"uri":"file:///secrets/key","text":"S0"},{"uri":"file:/secrets/key","text":"S1"},{"uri":"file://localhost/secrets/key","text":"S2"}]}`)
	if got := outcomeText(o); strings.Contains(got, "S1") || strings.Contains(got, "S2") {
		t.Errorf("a read result carried the denied file under another spelling: %s", got)
	}
}

// Finding 4, fixed for the branch only. An escape after the branch is still
// accepted, and on the hooks whose setter takes the key as plain text the
// set does nothing or lands on a key of that literal name. The hit says
// "set". On a result the check of the value's form is skipped too.
func TestReviewVerifyFixedEscapeAfterTheBranchInASetKey(t *testing.T) {
	ctx := context.Background()
	saved := func(hook, key, val string) bool {
		probs, _ := rvSave(hook, "true", map[string]string{key: val})
		return probs == nil
	}
	if k := `response.result.conten\t`; saved("tools/call.post.result", k, "[]") {
		s := rvServer(t, rvTools(), Rule{Hook: "tools/call.post.result", When: "true", Then: map[string]string{k: "[{'type': 'text', 'text': 'REDACTED'}]"}})
		if got := outcomeText(rvToolReply(t, s, `{"content":[{"type":"text","text":"SECRET"}]}`)); strings.Contains(got, "SECRET") {
			t.Errorf("result hook: the set was accepted and the client got %s", got)
		}
	}
	if k := `response.result.structuredConten\t`; saved("tools/call.post.result", k, "null") {
		s := rvServer(t, rvTools(), Rule{Hook: "tools/call.post.result", When: "true", Then: map[string]string{k: "'not an object'"}})
		if got := outcomeText(rvToolReply(t, s, `{"content":[],"structuredContent":{"secret":1}}`)); strings.Contains(got, "not an object") {
			t.Errorf("result hook: a string was accepted as structured content under another key: %s", got)
		}
	}
	if k := `settings.tool\s`; saved("server.settings", k, "false") {
		s := rvServer(t, rvTools(), Rule{Hook: "server.settings", When: "true", Then: map[string]string{k: "false"}})
		if got := rvJSON(rvEngine().ListTools(ctx, treeRequest(s)).Result); strings.Contains(got, `"name":"t"`) {
			t.Errorf("settings: tools were switched off and are still listed: %s", got)
		}
	}
	if k := `elicitation.params.messag\e`; saved("tools/call.post.elicit", k, "'x'") {
		s := rvServer(t, rvTools(), Rule{Hook: "tools/call.post.elicit", When: "true", Then: map[string]string{k: "'SAFE'"}})
		if got := outcomeText(rvToolReply(t, s, rvQuestion)); !strings.Contains(got, "SAFE") {
			t.Errorf("elicit hook: the message was set and the client got %s", got)
		}
	}
	if k := `server.titl\e`; saved("server/discover.post", k, "'x'") {
		s := rvServer(t, rvTools(), Rule{Hook: "server/discover.post", When: "true", Then: map[string]string{k: "'SAFE'"}})
		if got := outcomeText(rvEngine().Discover(ctx, treeRequest(s), "1")); !strings.Contains(got, "SAFE") {
			t.Errorf("discover hook: the title was set and the client got %s", got)
		}
	}
	if k := `session._met\a`; saved("server.session", k, "true") {
		s := rvServer(t, rvTools(), Rule{Hook: "server.session", When: "true", Then: map[string]string{k: "true"}})
		o := rvEngine().CallTool(ctx, treeRequest(s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`), Meta: mcp.Meta{"x": 1}})
		if o.Forward == nil || !o.Forward.Connect.ForwardMeta {
			t.Errorf("session hook: _meta was switched on and is not forwarded: %s", rvJSON(o.Forward))
		}
	}
}

// Finding 5, fixed for tools only. A resource's annotations can be set as a
// whole map. A key MCP does not name in that map is seen by the next list
// rule and then lost on the way to the client.
func TestReviewVerifyFixedUnknownAnnotationThroughTheWholeMap(t *testing.T) {
	then := map[string]string{"resource.annotations": "{'risk': 'high', 'priority': 0.5}"}
	if probs, _ := rvSave("resources/list.post", "true", then); probs != nil {
		return // refused when saved: the gap is closed
	}
	s := rvServer(t, Catalog{Resources: []*mcp.Resource{{URI: "docs://a", Name: "a"}}},
		Rule{Hook: "resources/list.post", When: "true", Then: then},
		Rule{Hook: "resources/list.post", When: "true", Then: map[string]string{"resource.description": "'risk=' + dyn(resource.annotations).?risk.orValue('lost')"}})
	got := rvJSON(rvEngine().ListResources(context.Background(), treeRequest(s)).Result)
	if strings.Contains(got, "risk=high") && !strings.Contains(got, `"risk":"high"`) {
		t.Errorf("the next rule saw the annotation and the client did not get it: %s", got)
	}
}

// Finding 9, fixed in part. The note is missed where a has() sits in the
// expression and does not guard the read, and it fires on reads that the
// usual guards do cover.
func TestReviewVerifyFixedNotesStillMissAndMisfire(t *testing.T) {
	noted := func(hook, when string, then map[string]string) bool {
		_, notes := rvSave(hook, when, then)
		return len(notes) > 0
	}
	// Missed: each of these fails, and so denies, on a tool with no title.
	s := rvServer(t, rvTools(), Rule{Hook: "tools/list.post", When: "has(tool.title) || tool.title == 'x'", Then: map[string]string{KeyDecision: "'deny'"}})
	if o := rvEngine().ListTools(context.Background(), treeRequest(s)); !strings.Contains(rvErrs(o.Hits), "error:") {
		t.Fatalf("control: has(tool.title) || tool.title did not fail on a tool with no title: %s", rvJSON(o.Hits))
	}
	for _, when := range []string{"has(tool.title) || tool.title == 'x'", "has(tool.title) ? false : tool.title == 'x'"} {
		if !noted("tools/list.post", when, nil) {
			t.Errorf("no note: %s", when)
		}
	}
	if !noted("tools/list.post", "tool.name == 'a' || has(tool.title)", map[string]string{"tool.description": "tool.title"}) {
		t.Error("no note: when tool.name == 'a' || has(tool.title), then tool.description: tool.title")
	}
	if !noted("tools/call.post.result", "response.result.content.exists(c, has(c.text)) && response.result.content.exists(c, c.text.contains('x'))", nil) {
		t.Error("no note: a has() in one macro hides the read in another")
	}
	// Fired on a guarded read.
	for _, x := range [][2]string{
		{"tools/list.post", "!has(tool.title) || tool.title == 'x'"},
		{"tools/list.post", "!has(tool.title) ? false : tool.title == 'x'"},
		{"tools/call.post.result", "response.result.content.all(c, !has(c.text) || !c.text.contains('x'))"},
		{"tools/list.post", "tool.?title.hasValue() && tool.title == 'x'"},
	} {
		if noted(x[0], x[1], nil) {
			t.Errorf("a note on a guarded read: %s", x[1])
		}
	}
}

// Findings 8 and 9: what the path check still does not see. The docs say
// every path is checked when the rule is saved and a wrong name never waits
// for a request. These wrong names are saved. They fail when a request
// arrives, which denies.
func TestReviewVerifyGapWrongPathsStillSaved(t *testing.T) {
	knownGap(t)
	for _, x := range [][2]string{
		{"tools/call.post.result", "response.result.content.exists(i, c, i.type == 'text')"}, // i is the index
		{"tools/list.post", "tool.params.exists(n, p, n.type == 'string')"},                  // n is the name
		{"tools/call.post.result", "response.result.content.filter(c, c.type == 'text')[0].nosuch == 1"},
		{"tools/call.post.result", "cel.bind(r, true ? response : response, r.nosuch == 1)"},
		{"tools/call.post.result", "(true ? response : response).nosuch == 1"},
		{"tools/call.post.result", "[response][0].nosuch == 1"},
		{"tools/call.post.result", "{'a': response}.a.nosuch == 1"},
		{"tools/call.post.result", "optional.of(response).value().nosuch == 1"},
		{"tools/call.post.result", "response['res' + 'ult'].nosuch == 1"},
	} {
		if probs, _ := rvSave(x[0], x[1], nil); probs == nil {
			t.Errorf("%s: saved: %s", x[0], x[1])
		}
	}
}

// Findings 7 and 8. An entry of a read result is a closed map in the tree.
// A server can send another key on it, and the gateway passes that key to
// the client and into the document a rule reads. Since macro variables are
// walked, a rule that reads it by name is refused when saved.
func TestReviewVerifyFixedAKeyTheServerSentCannotBeRead(t *testing.T) {
	s := rvServer(t, Catalog{Resources: []*mcp.Resource{{URI: "docs://a", Name: "a"}}})
	o := rvRead(t, s, "docs://a", `{"contents":[{"uri":"docs://a","text":"A","annotations":{"audience":["user"]}}]}`)
	if !strings.Contains(outcomeText(o), `"annotations"`) {
		t.Skip("the gateway no longer passes the key on")
	}
	for _, when := range []string{"response.result.contents.exists(c, has(c.annotations))", "response.result.contents.exists(c, c.?annotations.?audience.orValue([]).size() > 0)"} {
		if probs, _ := rvSave("resources/read.post.result", when, nil); probs != nil {
			t.Errorf("refused: %s: %v", when, probs)
		}
	}
}

// V7, fixed: null and an optional on a path with a type are refused when
// the rule is saved.
func TestReviewVerifyNullOnATypedPathIsRefusedWhenSaved(t *testing.T) {
	for _, x := range [][3]string{
		{"tools/list.post", "tool.title", "null"},
		{"tools/list.post", "tool.outputSchema", "null"},
		{"tools/list.post", "tool.annotations.readOnlyHint", "null"},
		{"server.settings", "settings.tools", "null"},
		{"server.session", "session.clientInfo", "null"},
		{"tools/call.pre", "timeout", "null"},
		{"tools/list.post", "tool.title", "optional.of(5)"},
	} {
		if probs, _ := rvSave(x[0], "true", map[string]string{x[1]: x[2]}); probs == nil {
			t.Errorf("%s: saved: %s = %s", x[0], x[1], x[2])
		}
	}
	// Null still removes structured output: that path takes any value.
	if probs, _ := rvSave("tools/call.post.result", "true", map[string]string{"response.result.structuredContent": "null"}); probs != nil {
		t.Errorf("structuredContent: null was refused: %v", probs)
	}
}

// V7, recorded. A constant whose form is wrong below its type is still
// saved: a duration that is not positive, a list whose elements have the
// wrong form, a value an any-typed path refuses at request time. Each fails
// closed when a request arrives.
func TestReviewVerifyGapWrongConstantsStillSaved(t *testing.T) {
	knownGap(t)
	for _, x := range [][3]string{
		{"tools/call.pre", "timeout", "duration('0s')"},
		{"tools/call.pre", "timeout", "duration('-5s')"},
		{"tools/call.post.result", "response.result.content", "[1]"},
		{"tools/call.post.result", "response.result.content", "['a']"},
		{"tools/call.post.result", "response.result.structuredContent", "'a string'"},
		{"tools/call.post.result", "response.result.structuredContent", "[1]"},
		{"server.session", "session.clientInfo", "{1: 2}"},
	} {
		if probs, _ := rvSave(x[0], "true", map[string]string{x[1]: x[2]}); probs == nil {
			t.Errorf("%s: saved: %s = %s", x[0], x[1], x[2])
		}
	}
}

// Finding 2, the part that is left. A confirmation covers the arguments as
// they stood when the question was asked. A rule after the question still
// runs on the answer round and not on the question round, so what it sets
// was never part of what was confirmed and can differ between the two
// rounds. The same is true of _meta that is forwarded to the server.
func TestReviewVerifyGapWhatIsSetAfterAQuestionIsNotCovered(t *testing.T) {
	knownGap(t)
	ctx := context.Background()
	cat := Catalog{Tools: []*mcp.Tool{{Name: "deploy", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"env": map[string]any{"type": "string"}}}}}}
	s := rvServer(t, cat,
		Rule{Hook: "tools/call.pre", When: "true", Then: vfConfirm("sure", "Deploy?")},
		Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{"request.params.arguments.env": "request.params.?_meta.?env.orValue('staging')"}})
	e, r := rvEngine(), treeRequest(s)
	k := vfState(e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "deploy", Arguments: json.RawMessage(`{}`), Meta: mcp.Meta{"env": "staging"}}))
	o := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "deploy", Arguments: json.RawMessage(`{}`), Meta: mcp.Meta{"env": "production"}, RequestState: k, InputResponses: rvYes})
	if o.Forward != nil && strings.Contains(rvJSON(o.Forward.Params), `"env":"production"`) {
		t.Errorf("asked with _meta env=staging, answered with env=production, forwarded %s", rvJSON(o.Forward.Params))
	}
	s = rvServer(t, rvTools(),
		Rule{Hook: "tools/call.pre", When: "true", Then: vfConfirm("sure", "Really?")},
		Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{"request.params.arguments.q": "request.headers[?'x-target'].orValue('safe')"}})
	r = treeRequest(s)
	r.Headers = map[string]string{"x-target": "safe"}
	k = vfState(e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`)}))
	r.Headers = map[string]string{"x-target": "prod"}
	o = e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`), RequestState: k, InputResponses: rvYes})
	if o.Forward != nil && strings.Contains(rvJSON(o.Forward.Params), `"q":"prod"`) {
		t.Errorf("asked with header x-target=safe, answered with prod, forwarded %s", rvJSON(o.Forward.Params))
	}
	s = rvServer(t, rvTools(),
		Rule{Hook: "server.session", When: "true", Then: map[string]string{"session._meta": "true"}},
		Rule{Hook: "tools/call.pre", When: "true", Then: vfConfirm("sure", "Really?")})
	r = treeRequest(s)
	k = vfState(e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`), Meta: mcp.Meta{"target": "safe"}}))
	o = e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`), Meta: mcp.Meta{"target": "prod"}, RequestState: k, InputResponses: rvYes})
	if o.Forward != nil && strings.Contains(rvJSON(o.Forward.Params), `"target":"prod"`) {
		t.Errorf("asked with _meta target=safe, answered with prod, and the server was sent %s", rvJSON(o.Forward.Params))
	}
}

// New. A gateway question asked on the round that answers a server's
// question throws the server's round away. The state of the new question
// does not carry the server's request state or the user's answer to the
// server, so the call is forwarded as a first call and the server asks
// again. The docs say the gateway and the server never ask in the same
// round.
func TestReviewVerifyFixedAGatewayQuestionDropsTheServersRound(t *testing.T) {
	ctx := context.Background()
	args := json.RawMessage(`{"q":"x"}`)
	s := rvServer(t, rvTools(),
		Rule{Hook: "tools/call.pre", When: "true", Then: vfConfirm("a", "A?")},
		Rule{Hook: "tools/call.pre", When: "request.params.?_meta.?late.orValue(false)", Then: vfConfirm("b", "B?")})
	e, r := rvEngine(), treeRequest(s)
	k1 := vfState(e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args}))
	p2 := &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: k1, InputResponses: vfYes("a")}
	o2 := e.CallTool(ctx, r, p2)
	if o2.Call == nil {
		t.Fatalf("the confirmed call was not forwarded: %s", outcomeText(o2))
	}
	k3 := vfState(e.UpstreamToolReply(ctx, r, o2.Call, p2, json.RawMessage(rvQuestion)))
	late := mcp.Meta{"late": true}
	k4 := vfState(e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: k3, InputResponses: rvAnswer(), Meta: late}))
	if k4 == "" {
		t.Skip("the second rule did not ask on the round that answered the server")
	}
	o5 := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: k4, InputResponses: vfYes("b"), Meta: late})
	if o5.Forward == nil {
		t.Fatalf("the call was not forwarded: %s", outcomeText(o5))
	}
	if got := rvJSON(o5.Forward.Params); !strings.Contains(got, `"requestState":"UP"`) || !strings.Contains(got, `"inputResponses"`) {
		t.Errorf("the server's state and the user's answer to it were dropped: forwarded %s", got)
	}
}

// New. Two rules that ask the same question under the same key are meant to
// share one answer, and the text form gives two confirm lines with the same
// words the same key. With a rule between them that changes an argument,
// the second can never be answered: the first answer is taken as an answer
// to it for other arguments, and the request is refused with "Send it
// again" on every try.
func TestReviewVerifyFixedTheSameQuestionTwiceWithASetBetween(t *testing.T) {
	ctx := context.Background()
	args := json.RawMessage(`{"q":"x"}`)
	s := rvServer(t, rvTools(),
		Rule{Hook: "tools/call.pre", When: "true", Then: vfConfirm("sure", "Really?")},
		Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{"request.params.arguments.q": "'changed'"}},
		Rule{Hook: "tools/call.pre", When: "true", Then: vfConfirm("sure", "Really?")})
	e, r := rvEngine(), treeRequest(s)
	state, in := "", mcp.InputResponseMap(nil)
	for round := 0; round < 4; round++ {
		o := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: state, InputResponses: in})
		if o.Forward != nil {
			return
		}
		if state = vfState(o); state == "" {
			t.Fatalf("round %d: the request can never be confirmed: %s", round+1, outcomeText(o))
		}
		in = rvYes
	}
	t.Error("the gateway kept asking")
}

// Finding 11, and what its fix opened. Only the files a skill's own
// manifest lists keep their bytes now. A file under a nested skill's folder
// that the outer skill's manifest lists is looked up in the nested skill's
// manifest, is not found there, and a rule's change to it stands. The
// outer skill is still listed with the server's digest for that file.
func TestReviewVerifyFixedAFileTheOuterManifestListsIsChanged(t *testing.T) {
	text, inner, extra := "---\nname: demo\ndescription: d\n---\nbody", "---\nname: sub\ndescription: d\n---\ninner", "extra"
	cat := Catalog{Skills: []Skill{
		{URI: "skill://demo/SKILL.md", Frontmatter: map[string]any{"name": "demo", "description": "d"},
			Resources: json.RawMessage(`[{"uri":"skill://demo/SKILL.md","digest":"sha256:x","size":10},{"uri":"skill://demo/sub/x.md","digest":"sha256:y","size":5}]`),
			Files:     map[string]File{"skill://demo/SKILL.md": {Text: &text}, "skill://demo/sub/x.md": {Text: &extra}}},
		{URI: "skill://demo/sub/SKILL.md", Frontmatter: map[string]any{"name": "sub", "description": "d"},
			Resources: json.RawMessage(`[{"uri":"skill://demo/sub/SKILL.md","digest":"sha256:z","size":10}]`),
			Files:     map[string]File{"skill://demo/sub/SKILL.md": {Text: &inner}}},
	}}
	s := rvServer(t, cat, Rule{Hook: "resources/read.post.result", When: "request.caller.role == 'member'", Then: map[string]string{"response.result.contents": "response.result.contents.map(c, {'uri': c.uri, 'text': 'EVIL'})"}})
	listing := rvJSON(rvEngine().ListSkills(context.Background(), treeRequest(s)).Result)
	if !strings.Contains(listing, `{"uri":"skill://demo/sub/x.md","digest":"sha256:y","size":5}`) {
		t.Fatalf("control: the outer manifest does not vouch for the file: %s", listing)
	}
	if got := outcomeText(rvRead(t, s, "skill://demo/sub/x.md", `{"contents":[{"uri":"skill://demo/sub/x.md","text":"extra"}]}`)); strings.Contains(got, "EVIL") {
		t.Errorf("a file the outer manifest lists was changed: %s", got)
	}
}

// Finding 11, the other side. A rule that does not read the request is
// applied to a skill's files ahead of time, from the text the catalog
// holds. A file the manifest lists and the catalog holds no text for is
// skipped there and has its bytes put back on a read, so a redaction that
// applies to every other read is lost on it. The hit says "set" and no
// issue is recorded.
func TestReviewVerifyFixedAManifestFileWithNoHeldTextIsNotRewritten(t *testing.T) {
	text := "---\nname: demo\ndescription: d\n---\nbody"
	cat := Catalog{Skills: []Skill{{URI: "skill://demo/SKILL.md", Frontmatter: map[string]any{"name": "demo", "description": "d"},
		Resources: json.RawMessage(`[{"uri":"skill://demo/SKILL.md","digest":"sha256:x","size":10},{"uri":"skill://demo/extra.md","digest":"sha256:y","size":13}]`),
		Files:     map[string]File{"skill://demo/SKILL.md": {Text: &text}}}}}
	s := rvServer(t, cat, Rule{Hook: "resources/read.post.result", When: "true", Then: map[string]string{
		"response.result.contents": "response.result.contents.map(c, has(c.text) ? {'uri': c.uri, 'text': c.text.replace('hunter2', '[redacted]')} : c)"}})
	o := rvRead(t, s, "skill://demo/extra.md", `{"contents":[{"uri":"skill://demo/extra.md","text":"extra hunter2"}]}`)
	if got := outcomeText(o); strings.Contains(got, "hunter2") && len(o.Issues) == 0 {
		t.Errorf("the rule redacted the file, the client got %s, and no issue was recorded", got)
	}
}

// Finding 16, a form the fix does not reach. An entry of a read result that
// is not an object, or whose uri is not a string, is turned into an entry
// for the URI that was read and sent to the client: a string, a number and
// a null each become {"uri": …} with no text and no blob.
func TestReviewVerifyFixedAnEntryThatIsNotAnObjectIsMadeUp(t *testing.T) {
	s := rvServer(t, Catalog{Resources: []*mcp.Resource{{URI: "docs://a", Name: "a"}}})
	o := rvRead(t, s, "docs://a", `{"contents":[{"uri":"docs://a","text":"A"},"junk",5,null,{"uri":5,"text":"other"}]}`)
	contents, _ := o.Result.(map[string]any)["contents"].([]any)
	if o.Error == nil && len(contents) != 1 {
		t.Errorf("the server sent one well-formed entry and the client got %d: %s", len(contents), outcomeText(o))
	}
}

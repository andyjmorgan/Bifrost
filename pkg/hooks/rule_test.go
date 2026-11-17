package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Saving a rule: its shape, its `then` keys and the types of its values.

// A rule of the wrong shape is refused when it is saved: a misspelt mode, an
// empty condition, no `then`. The mode matters most: if that check went,
// "audt" would not equal "audit" and the rule would enforce.
func TestARuleOfTheWrongShapeIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rule  Rule
		field string
	}{
		{"a misspelt mode", Rule{Hook: "tools/call.pre", Mode: "audt", When: "true", Then: map[string]string{"decision": "'deny'"}}, "mode"},
		{"an empty when", Rule{Hook: "tools/call.pre", When: "  ", Then: map[string]string{"decision": "'deny'"}}, "when"},
		{"a rule with no then", Rule{Hook: "tools/call.pre", When: "true"}, "then"},
		{"a filter rule with no then", Rule{Hook: "tools/list.post", When: "true"}, "then"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set, probs := Compile([]Rule{tc.rule})
			if len(probs) == 0 {
				t.Fatalf("the rule was accepted: %+v", tc.rule)
			}
			found := false
			for _, p := range probs {
				found = found || p.Field == tc.field
			}
			if !found {
				t.Fatalf("no problem names the field %q: %v", tc.field, probs)
			}
			if set != nil && set.has(tc.rule.Hook) {
				t.Fatalf("a rule that was refused is still in the set")
			}
		})
	}
}

// A rules document is one JSON value. Anything after it is refused, not
// ignored.
func TestRulesFollowedByMoreJSONAreRefused(t *testing.T) {
	for _, raw := range []string{`[] [{"hook":"tools/list.post","when":"true","then":{"decision":"'deny'"}}]`, `[] garbage`} {
		if got, err := Parse(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted malformed rule document %q as %v", raw, got)
		}
	}
}

// Parse never accepts text that is not valid JSON. The last two seeds are
// inputs that fuzzing found: a stray bracket, and a vertical tab, which JSON
// does not count as white space.
func FuzzParseRules(f *testing.F) {
	for _, s := range []string{`[]`, `null`, `[] garbage`, `[{"hook":"tools/list.post","when":"true","then":{"decision":"'deny'"}}]`, `[]]`, "null\v"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 8192 {
			t.Skip()
		}
		_, err := Parse(json.RawMessage(s))
		if err == nil && s != "" && !json.Valid([]byte(s)) {
			t.Fatalf("accepted invalid rule JSON: %q", s)
		}
	})
}

// A `then` key that only resembles a path of the tree is refused when the
// rule is saved: an escape inside a name, an escaped dot between two names,
// a trailing dot, and the upstream side.
func TestASetKeyThatIsNotAPathOfTheTreeIsRefused(t *testing.T) {
	for _, key := range []string{`response.result.conten\t`, `response\.result.content`, `response.result.content.`, `response.upstream.result.content`} {
		if p, _ := saveRule("tools/call.post.result", "true", map[string]string{key: "[]"}); len(p) == 0 {
			t.Errorf("invalid set saved: %s", key)
		}
	}
}

// A `then` key is split with backslash escapes when it is checked against the
// tree. A key with an escape in its branch part must not pass that check and
// then reach the setter as plain text, where the set would do nothing, or
// land on a key of that literal name, while the hit still says "set". Each
// key here is either refused when the rule is saved or takes effect.
func TestASetKeyWithAnEscapeInItsBranchIsRefusedOrTakesEffect(t *testing.T) {
	ctx := context.Background()
	saved := func(hook, key, val string) bool {
		probs, _ := saveRule(hook, "true", map[string]string{key: val})
		return probs == nil
	}
	if saved("tools/call.post.result", `respons\e.result.content`, "[]") {
		s := newServer(t, oneTool(), Rule{Hook: "tools/call.post.result", When: "true", Then: map[string]string{`respons\e.result.content`: "[{'type': 'text', 'text': 'REDACTED'}]"}})
		if got := outcomeText(toolReply(t, s, `{"content":[{"type":"text","text":"SECRET"}]}`)); strings.Contains(got, "SECRET") || strings.Contains(got, `respons\\e`) {
			t.Errorf("result hook: the set was accepted and the client got %s", got)
		}
	}
	if saved("server.settings", `setting\s.tools`, "false") {
		s := newServer(t, oneTool(), Rule{Hook: "server.settings", When: "true", Then: map[string]string{`setting\s.tools`: "false"}})
		if got := asJSON((&Engine{}).ListTools(ctx, newRequest("direct", s)).Result); strings.Contains(got, `"name":"t"`) {
			t.Errorf("settings: tools were switched off and are still listed: %s", got)
		}
	}
	if saved("tools/call.pre", `reques\t.params.arguments.q`, "'x'") {
		s := newServer(t, oneTool(), Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{`reques\t.params.arguments.q`: "'pinned'"}})
		o := (&Engine{}).CallTool(ctx, newRequest("direct", s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"client"}`)})
		if o.Forward != nil && !strings.Contains(asJSON(o.Forward.Params), `"q":"pinned"`) {
			t.Errorf("pre hook: the argument was set to 'pinned' and the server got %s", asJSON(o.Forward.Params))
		}
	}
	if saved("tools/list.post", `to\ol.name`, "'x'") {
		s := newServer(t, oneTool(), Rule{Hook: "tools/list.post", When: "true", Then: map[string]string{`to\ol.name`: "'renamed'"}})
		if got := asJSON((&Engine{}).ListTools(ctx, newRequest("direct", s)).Result); !strings.Contains(got, `"name":"renamed"`) {
			t.Errorf("list hook: the tool was renamed and is listed as %s", got)
		}
	}
	if saved("tools/call.post.elicit", `elicitatio\n.params.message`, "'x'") {
		s := newServer(t, oneTool(), Rule{Hook: "tools/call.post.elicit", When: "true", Then: map[string]string{`elicitatio\n.params.message`: "'SAFE'"}})
		if got := outcomeText(toolReply(t, s, serverQuestion)); !strings.Contains(got, "SAFE") {
			t.Errorf("elicit hook: the message was set and the client got %s", got)
		}
	}
}

// An escape after the branch part of a `then` key is held to the same: on
// every hook the key is refused when the rule is saved, or the set lands
// where the plain key would. It never does nothing, or lands on a key of that
// literal name, behind a hit that says "set". On a result the check of the
// value's form is not skipped either.
func TestASetKeyWithAnEscapeAfterItsBranchIsRefusedOrTakesEffect(t *testing.T) {
	ctx := context.Background()
	saved := func(hook, key, val string) bool {
		probs, _ := saveRule(hook, "true", map[string]string{key: val})
		return probs == nil
	}
	if k := `response.result.conten\t`; saved("tools/call.post.result", k, "[]") {
		s := newServer(t, oneTool(), Rule{Hook: "tools/call.post.result", When: "true", Then: map[string]string{k: "[{'type': 'text', 'text': 'REDACTED'}]"}})
		if got := outcomeText(toolReply(t, s, `{"content":[{"type":"text","text":"SECRET"}]}`)); strings.Contains(got, "SECRET") {
			t.Errorf("result hook: the set was accepted and the client got %s", got)
		}
	}
	if k := `response.result.structuredConten\t`; saved("tools/call.post.result", k, "null") {
		s := newServer(t, oneTool(), Rule{Hook: "tools/call.post.result", When: "true", Then: map[string]string{k: "'not an object'"}})
		if got := outcomeText(toolReply(t, s, `{"content":[],"structuredContent":{"secret":1}}`)); strings.Contains(got, "not an object") {
			t.Errorf("result hook: a string was accepted as structured content under another key: %s", got)
		}
	}
	if k := `settings.tool\s`; saved("server.settings", k, "false") {
		s := newServer(t, oneTool(), Rule{Hook: "server.settings", When: "true", Then: map[string]string{k: "false"}})
		if got := asJSON((&Engine{}).ListTools(ctx, newRequest("direct", s)).Result); strings.Contains(got, `"name":"t"`) {
			t.Errorf("settings: tools were switched off and are still listed: %s", got)
		}
	}
	if k := `elicitation.params.messag\e`; saved("tools/call.post.elicit", k, "'x'") {
		s := newServer(t, oneTool(), Rule{Hook: "tools/call.post.elicit", When: "true", Then: map[string]string{k: "'SAFE'"}})
		if got := outcomeText(toolReply(t, s, serverQuestion)); !strings.Contains(got, "SAFE") {
			t.Errorf("elicit hook: the message was set and the client got %s", got)
		}
	}
	if k := `server.titl\e`; saved("server/discover.post", k, "'x'") {
		s := newServer(t, oneTool(), Rule{Hook: "server/discover.post", When: "true", Then: map[string]string{k: "'SAFE'"}})
		if got := outcomeText((&Engine{}).Discover(ctx, newRequest("direct", s), "1")); !strings.Contains(got, "SAFE") {
			t.Errorf("discover hook: the title was set and the client got %s", got)
		}
	}
	if k := `session._met\a`; saved("server.session", k, "true") {
		s := newServer(t, oneTool(), Rule{Hook: "server.session", When: "true", Then: map[string]string{k: "true"}})
		o := (&Engine{}).CallTool(ctx, newRequest("direct", s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`), Meta: mcp.Meta{"x": 1}})
		if o.Forward == nil || !o.Forward.Connect.ForwardMeta {
			t.Errorf("session hook: _meta was switched on and is not forwarded: %s", asJSON(o.Forward))
		}
	}
}

// A value whose type is known when the rule is saved is checked against the
// path's type then, not when a request arrives.
func TestAConstantOfTheWrongTypeIsRefusedWhenSaved(t *testing.T) {
	for _, c := range [][3]string{
		{"server.settings", "settings.tools", "'off'"},
		{"tools/call.pre", "timeout", "'30s'"},
		{"tools/list.post", "tool.annotations.readOnlyHint", "'yes'"},
		{"tools/list.post", "tool.name", "5"},
		{"prompts/get.pre", "request.params.arguments.tone", "5"},
		{"tools/call.post.result", "response.result.content", "'text'"},
	} {
		if probs, _ := saveRule(c[0], "true", map[string]string{c[1]: c[2]}); probs == nil {
			t.Errorf("%s: set %s = %s was saved", c[0], c[1], c[2])
		}
	}
}

// Null and an optional on a path with a type are refused when the rule is
// saved.
func TestNullOnATypedPathIsRefusedWhenSaved(t *testing.T) {
	for _, x := range [][3]string{
		{"tools/list.post", "tool.title", "null"},
		{"tools/list.post", "tool.outputSchema", "null"},
		{"tools/list.post", "tool.annotations.readOnlyHint", "null"},
		{"server.settings", "settings.tools", "null"},
		{"server.session", "session.clientInfo", "null"},
		{"tools/call.pre", "timeout", "null"},
		{"tools/list.post", "tool.title", "optional.of(5)"},
	} {
		if probs, _ := saveRule(x[0], "true", map[string]string{x[1]: x[2]}); probs == nil {
			t.Errorf("%s: saved: %s = %s", x[0], x[1], x[2])
		}
	}
	// Null still removes structured output: that path takes any value.
	if probs, _ := saveRule("tools/call.post.result", "true", map[string]string{"response.result.structuredContent": "null"}); probs != nil {
		t.Errorf("structuredContent: null was refused: %v", probs)
	}
}

// A value of the right type is not refused: null where the docs say null
// removes a key, a number as an int, a double or a uint, a list built by a
// macro, a value the checker cannot type.
func TestTheTypeCheckTakesValidValues(t *testing.T) {
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
		if probs, _ := saveRule(x[0], "true", map[string]string{x[1]: x[2]}); probs != nil {
			t.Errorf("%s: %s = %s was refused: %v", x[0], x[1], x[2], probs)
		}
	}
}

package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The save-time path check: which reads it follows, which rules it takes and
// refuses, and when it notes a read of a value that may be absent.

// The path check follows nested macros, two-variable macros, macros over
// maps, chains of cel.bind and variables that shadow a branch, and none of
// these valid rules is refused. A wrong name behind the same forms is
// refused.
func TestTheSaveCheckTakesValidRules(t *testing.T) {
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
		if probs, _ := saveRule(x[0], x[1], nil); probs != nil {
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
		if probs, _ := saveRule(x[0], x[1], nil); probs == nil {
			t.Errorf("%s: %s was saved", x[0], x[1])
		}
	}
}

// A read behind has(), a negated has(), a ternary, hasValue() or a guard
// inside cel.bind is a guarded read, and the rule is saved. A guard does not
// hide a name the tree does not have.
func TestTheSaveCheckUnderstandsGuards(t *testing.T) {
	for _, expr := range []string{
		"has(tool.title) && tool.title == 'x'", "!has(tool.title) || tool.title == 'x'",
		"has(tool.title) ? tool.title == 'x' : false", "tool.?title.hasValue() && tool.title == 'x'",
		"cel.bind(t, tool, !has(t.title) || t.title == 'x')",
		"[1].exists(tool, tool == 1)",
	} {
		if p, _ := saveRule("tools/list.post", expr, nil); len(p) != 0 {
			t.Errorf("valid guard rejected %s: %v", expr, p)
		}
	}
	for _, expr := range []string{"has(tool.nosuch) && tool.nosuch == 'x'", "!has(tool.nosuch) || tool.nosuch == 'x'", "cel.bind(t, tool, has(t.nosuch))"} {
		if p, _ := saveRule("tools/list.post", expr, nil); len(p) == 0 {
			t.Errorf("guard hid an invalid path: %s", expr)
		}
	}
}

// A macro variable that has the name of a branch is the variable, not the
// branch. It is not checked against the tree, and a valid rule that uses one
// is saved.
func TestAMacroVariableMayHaveTheNameOfABranch(t *testing.T) {
	for hook, when := range map[string]string{
		"resources/read.post.result": "response.result.contents.exists(resource, resource.text.contains('x'))",
		"tools/call.post.result":     "response.result.content.filter(tool, tool.type == 'text').size() > 0",
		"tools/call.pre":             "[{'nosuch': 1}].exists(request, request.nosuch == 1)",
	} {
		if probs, _ := saveRule(hook, when, nil); probs != nil {
			t.Errorf("%s: %s was refused: %v", hook, when, probs)
		}
	}
}

// The save-time check does not see a branch reached through a computed key,
// cel.bind, a macro over a list, a ternary or a literal. Each of those still
// fails when the request arrives, and a failed enforce rule refuses the call.
func TestAPathTheSaveCheckCannotFollowStillDeniesOnTheRequest(t *testing.T) {
	for _, when := range []string{
		"request[('up' + 'stream')].params.name == 'x'",
		"(true ? request : tool).nosuch == 1",
		"{'a': request}.a.nosuch == 1",
		"[request][0].nosuch == 1",
		"cel.bind(t, dyn(tool), t.title == 'x')",
	} {
		if probs, _ := saveRule("tools/call.pre", when, nil); probs != nil {
			t.Errorf("%s: refused when saved: %v", when, probs)
			continue
		}
		s := newServer(t, oneTool(), Rule{Hook: "tools/call.pre", When: when, Then: map[string]string{KeyDecision: "'deny'", KeyReason: "'matched'"}})
		o := (&Engine{}).CallTool(context.Background(), newRequest("direct", s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`)})
		if o.Forward != nil || !strings.Contains(outcomeText(o), "refused by policy") {
			t.Errorf("%s: forwarded=%v %s", when, o.Forward != nil, outcomeText(o))
		}
	}
	// A leading dot names the branch from the root and is checked like any other read.
	if probs, _ := saveRule("tools/call.pre", ".request.upstream.params.name == 'x'", nil); probs == nil {
		t.Error(".request.upstream compiled on a pre hook")
	}
	// Flags are still read by name through the forms the walker sees.
	for _, when := range []string{"'search' in request.flags", "cel.bind(f, request.flags, 'serach' in f)", "request.flags[tool.name]", "request.flags['serach']", "request.?flags.hasValue()"} {
		if probs, _ := saveRule("tools/call.pre", when, nil); probs == nil {
			t.Errorf("%s compiled", when)
		}
	}
}

// The note on an optional read covers the common forms and spares the guarded
// ones. Noted: a field of a list entry read through a macro variable, and a
// header. Not noted: a read behind has() in the same condition, and a plain
// step after .?, which CEL treats as optional too. A value that is read only
// when has() says it is absent always fails, and is noted.
func TestNotesCoverMacroVariablesAndHeadersAndSpareGuardedReads(t *testing.T) {
	for hook, when := range map[string]string{
		"tools/call.post.result":     "response.result.content.exists(c, c.text.contains('secret'))",
		"resources/read.post.result": "response.result.contents.exists(c, c.text.contains('secret'))",
		"tools/call.pre":             "request.headers['x-team'] == 'sre'",
	} {
		if probs, notes := saveRule(hook, when, nil); probs == nil && len(notes) == 0 {
			t.Errorf("missed: %s on %s reads a value that may be absent and has no note", when, hook)
		}
	}
	for _, when := range []string{"has(tool.title) && tool.title == 'x'", "tool.?annotations.destructiveHint.orValue(true)"} {
		if _, notes := saveRule("tools/list.post", when, nil); len(notes) > 0 {
			t.Errorf("misfired: %s is guarded and was noted: %v", when, notes)
		}
	}
	// !has() in the condition does not hide the note on a read that then always fails.
	if _, notes := saveRule("tools/list.post", "!has(tool.title)", map[string]string{"tool.description": "tool.title"}); len(notes) == 0 {
		t.Error("missed: a value read only when has() says it is absent has no note")
	}
}

// A has() counts only where it guards the read. A read is noted when a has()
// sits in the same expression without guarding it, and is not noted when one
// of the usual guards covers it.
func TestANoteDependsOnWhetherHasGuardsTheRead(t *testing.T) {
	noted := func(hook, when string, then map[string]string) bool {
		_, notes := saveRule(hook, when, then)
		return len(notes) > 0
	}
	// Unguarded: each of these fails, and so denies, on a tool with no title.
	s := newServer(t, oneTool(), Rule{Hook: "tools/list.post", When: "has(tool.title) || tool.title == 'x'", Then: map[string]string{KeyDecision: "'deny'"}})
	if o := (&Engine{}).ListTools(context.Background(), newRequest("direct", s)); !strings.Contains(hitErrors(o.Hits), "error:") {
		t.Fatalf("control: has(tool.title) || tool.title did not fail on a tool with no title: %s", asJSON(o.Hits))
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
	// Guarded: no note.
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

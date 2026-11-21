package hooks

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// These tests hold the tree to what the design says of it: one tree per
// hook, MCP's names, one branch a rule may change, an upstream nothing can
// set, and a JSON Schema that comes out of the same definition.

func compiles(hook, when string, then map[string]string) Problems {
	if then == nil {
		then = map[string]string{}
		if f := Hooks[hook].Sets(); len(f) > 0 && !strings.Contains(f[0].Pattern, "*") {
			then[f[0].Pattern] = zero(f[0].Type)
		} else {
			then[KeyDecision] = "'deny'"
		}
	}
	_, probs := compileRule(0, Rule{Hook: hook, When: when, Then: then}, Options{CostLimit: DefaultCostLimit})
	return probs
}

func zero(t ValueType) string {
	switch t {
	case TBool:
		return "true"
	case TNumber:
		return "1"
	case TList:
		return "[]"
	case TMap:
		return "{}"
	case TDuration:
		return "duration('1s')"
	}
	return "'x'"
}

func hasProblem(probs Problems, field string) bool {
	for _, p := range probs {
		if p.Field == field {
			return true
		}
	}
	return false
}

// Every hook has the request, with every flag and both routes, and a rule
// reading a flag that does not exist is refused when it is saved.
func TestEveryHookHasTheRequest(t *testing.T) {
	for _, name := range Flags {
		if FlagDocs[name] == "" {
			t.Errorf("flag %q has no description", name)
		}
	}
	for _, h := range Schema() {
		req := h.Tree.Keys["request"]
		if req == nil {
			t.Fatalf("%s has no request", h.Hook)
		}
		if got := strings.Join(req.Keys["route"].Enum, ","); got != "direct,mux" {
			t.Errorf("%s: request.route is %q", h.Hook, got)
		}
		for _, k := range []string{"method", "params", "caller", "client", "headers", "flags", "time"} {
			if req.Keys[k] == nil {
				t.Errorf("%s: request has no %s", h.Hook, k)
			}
		}
		for _, name := range Flags {
			if req.Keys["flags"].Keys[name] == nil {
				t.Errorf("%s: request.flags has no %s", h.Hook, name)
			}
			if probs := compiles(h.Hook, "request.flags."+name, nil); hasProblem(probs, "when") {
				t.Errorf("%s: request.flags.%s does not compile: %v", h.Hook, name, probs)
			}
		}
		if probs := compiles(h.Hook, "request.flags.madeup", nil); !hasProblem(probs, "when") {
			t.Errorf("%s: a made-up flag compiled", h.Hook)
		}
	}
}

// A hook changes one branch: every path a rule may set on it starts at the
// same place, and a path that exists elsewhere in the tree is refused with a
// message that says so.
func TestEachHookChangesOneBranch(t *testing.T) {
	want := map[string]string{
		"tools/list.post": "tool", "prompts/list.post": "prompt", "resources/list.post": "resource", "resources/templates/list.post": "template",
		"tools/call.pre": "request", "prompts/get.pre": "request",
		"tools/call.post.result": "response", "resources/read.post.result": "response",
		"tools/call.post.progress": "progress", "prompts/get.post.progress": "progress", "resources/read.post.progress": "progress",
		"tools/call.post.elicit": "elicitation", "prompts/get.post.elicit": "elicitation", "resources/read.post.elicit": "elicitation",
		"tools/call.post.task": "task",
		"server/discover.post": "server", "server.settings": "settings", "server.session": "session",
	}
	for name, h := range Hooks {
		branches := map[string]bool{}
		for _, f := range h.Sets() {
			root := strings.SplitN(f.Pattern, ".", 2)[0]
			if root == "timeout" {
				continue // the gateway's own limit on the forward, on tools/call.pre
			}
			branches[root] = true
			if strings.Contains(f.Pattern, ".upstream.") || strings.HasSuffix(f.Pattern, ".upstream") {
				t.Errorf("%s may set %s", name, f.Pattern)
			}
		}
		var got []string
		for b := range branches {
			got = append(got, b)
		}
		sort.Strings(got)
		if w := want[name]; strings.Join(got, ",") != w {
			t.Errorf("%s changes %v, want %q", name, got, w)
		}
	}
}

// What exists and cannot be changed is refused by name; what does not exist
// is refused as missing.
func TestASetOnAPathThatCannotBeChangedIsRefused(t *testing.T) {
	cases := []struct{ hook, key, has string }{
		{"tools/list.post", "tool.upstream.name", "cannot be changed"},
		{"tools/list.post", "request.caller.role", "cannot be changed"},
		{"tools/list.post", "tool.inputSchema", "cannot be changed"},
		{"tools/call.pre", "tool.name", "cannot be changed"},
		{"tools/call.pre", "request.params.name", "cannot be changed"},
		{"tools/call.post.result", "response.upstream.result.content", "cannot be changed"},
		{"tools/call.post.result", "request.params.arguments.q", "cannot be changed"},
		{"tools/call.post.result", "response.result.isError", "cannot be changed"},
		{"tools/call.post.progress", "progress.params.progress", "cannot be changed"},
		{"tools/list.post", "tool.nmae", "there is no tool.nmae"},
		{"tools/list.post", "name", "there is no name"},
		{"tools/call.pre", "args.limit", "there is no args.limit"},
		{"server.settings", "tools", "there is no tools"},
		{"server.settings", "decision", "cannot deny"},
		{"tools/list.post", "confirm", "cannot ask the user to confirm"},
	}
	for _, c := range cases {
		probs := compiles(c.hook, "true", map[string]string{c.key: "'deny'"})
		if !hasProblem(probs, c.key) || !strings.Contains(probs.Error(), c.has) {
			t.Errorf("%s set %s: %v, want it to say %q", c.hook, c.key, probs, c.has)
		}
	}
}

// Nothing outside the hook's own branch can be set, and nothing inside it
// that the tree does not mark.
func TestOnlyTheHooksOwnBranchCanBeSet(t *testing.T) {
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
		if probs, _ := saveRule(c[0], "true", map[string]string{c[1]: c[2]}); probs == nil {
			t.Errorf("%s: set %s compiled", c[0], c[1])
		}
	}
}

// A name the tree does not have is refused when the rule is saved, with the
// names it does have. request.upstream is there on the post hooks and
// refused on the others, where nothing has been sent to the server.
func TestAReadOfSomethingTheTreeDoesNotHaveIsRefused(t *testing.T) {
	bad := []struct{ hook, when, has string }{
		{"tools/call.pre", "tool.nmae == 'x'", `tool has no "nmae"`},
		{"tools/call.pre", "name == 'x'", "undeclared reference"},
		{"tools/call.pre", "args.limit > 1", "undeclared reference"},
		{"tools/call.pre", "request.upstream.params.name == 'x'", "request.upstream is not available on tools/call.pre"},
		{"tools/list.post", "request.upstream.params.name == 'x'", "request.upstream is not available"},
		{"tools/call.pre", "request.params.nmae == 'x'", `request.params has no "nmae"`},
		{"tools/call.pre", "response.result.content.size() > 0", "undeclared reference"},
		{"tools/call.post.result", "response.result.text == 'x'", `response.result has no "text"`},
		{"tools/call.post.result", "tool.annotationsRaw.x", `tool has no "annotationsRaw"`},
		{"tools/call.post.result", "tool.meta.x == 1", `tool has no "meta"`},
		{"tools/call.pre", "request.client.name == 'x'", `request.client has no "name"`},
		{"server.settings", "settings.tools", "undeclared reference"},
		{"prompts/get.pre", "tool.name == 'x'", "undeclared reference"},
	}
	for _, c := range bad {
		probs := compiles(c.hook, c.when, nil)
		if !hasProblem(probs, "when") || !strings.Contains(probs.Error(), c.has) {
			t.Errorf("%s: %s gave %v, want %q", c.hook, c.when, probs, c.has)
		}
	}
	good := []struct{ hook, when string }{
		{"tools/call.pre", "tool.name == 'x' && tool.upstream.name == 'y' && request.params.name == 'x'"},
		{"tools/call.pre", "request.params.arguments.limit > 1 && request.params.arguments.nested.deeper == 2"},
		{"tools/call.pre", "tool.?_meta[?'team/owner'].orValue('none') == 'sre' && tool.params.limit.maximum > 1"},
		{"tools/call.pre", "request.client.?clientInfo.?name.orValue('') == 'x' && has(request.client.capabilities.elicitation)"},
		{"tools/call.pre", "request.caller.role == 'admin' && request.headers['x-team'] == 'sre' && request.route == 'mux'"},
		{"tools/call.post.result", "request.upstream.params.name == 'y' && request.upstream.params.arguments.q == 1"},
		{"tools/call.post.result", "response.result.content.exists(c, c.type == 'image') && response.upstream.result.content[0].text == 'x'"},
		{"tools/call.post.result", "response.result.?structuredContent.?anything.hasValue()"},
		{"tools/call.post.progress", "progress.params.?message.orValue('') == '' && progress.upstream.params.progress > 0"},
		{"tools/call.post.elicit", "elicitation.key == 'k' && elicitation.params.?message.orValue('') != '' && elicitation.upstream.params.?mode.hasValue()"},
		{"resources/read.post.result", "response.result.contents.exists(c, c.uri == resource.uri) && request.upstream.params.uri != ''"},
		{"server/discover.post", "server.?title.orValue('') == '' && server.upstream.?instructions.hasValue()"},
		{"prompts/list.post", "prompt.params.tone.name == 'tone' && prompt.?arguments.orValue([]).size() > 0"},
		{"skills/list.post", "skill.?name.orValue('').startsWith('internal-')"},
	}
	for _, c := range good {
		if probs := compiles(c.hook, c.when, nil); len(probs) > 0 {
			t.Errorf("%s: %s does not compile: %v", c.hook, c.when, probs)
		}
	}
}

// An optional value read with a plain "." is noted when the rule is saved,
// because reading one that is absent fails the rule and a failed rule
// denies. The same value read with .? or under has() is not.
func TestAnUnguardedOptionalReadIsNoted(t *testing.T) {
	notes := func(when string) string {
		set, probs := Compile([]Rule{{Hook: "tools/list.post", When: when, Then: map[string]string{KeyDecision: "'deny'"}}})
		if probs != nil {
			t.Fatalf("%s: %v", when, probs)
		}
		var out []string
		for _, n := range set.Notes {
			out = append(out, n.Message)
		}
		return strings.Join(out, "\n")
	}
	for when, path := range map[string]string{
		"tool.description.contains('x')":                  "tool.description may be absent",
		"tool.annotations.destructiveHint":                "tool.annotations.destructiveHint may be absent",
		"tool.upstream.title == 'x'":                      "tool.upstream.title may be absent",
		"has(tool.annotations.destructiveHint)":           "tool.annotations may be absent",
		"tool.?annotations.destructiveHint.orValue(true)": "",
	} {
		got := notes(when)
		if path == "" && got != "" && !strings.Contains(when, "?annotations.destructiveHint") {
			t.Errorf("%s was noted: %s", when, got)
		}
		if path != "" && !strings.Contains(got, path) {
			t.Errorf("%s: notes %q, want %q", when, got, path)
		}
	}
	for _, when := range []string{
		"tool.name == 'x'",
		"tool.?description.orValue('').contains('x')",
		"has(tool.description)",
		"tool.?annotations.?destructiveHint.orValue(true)",
		"tool.?_meta[?'k'].hasValue()",
		"request.params.?_meta.hasValue()",
	} {
		if got := notes(when); got != "" {
			t.Errorf("%s was noted: %s", when, got)
		}
	}
}

// X.upstream has the same form as X: every key the canonical side has is a
// key of the gateway's side, and nothing under it can be set.
func TestUpstreamMirrorsItsParent(t *testing.T) {
	for name, h := range Hooks {
		for branch, n := range h.Tree().Keys {
			up := n.Keys["upstream"]
			if up == nil {
				continue
			}
			var walk func(a, b *Node, path string)
			walk = func(a, b *Node, path string) {
				if a.Writable {
					t.Errorf("%s: %s can be set", name, path)
				}
				for k, c := range a.Keys {
					if b == nil || b.Keys[k] == nil {
						t.Errorf("%s: %s.%s has no counterpart beside upstream", name, path, k)
						continue
					}
					walk(c, b.Keys[k], path+"."+k)
				}
			}
			if branch == "request" || branch == "server" {
				continue // their upstream is the server's terms for part of them, not a mirror of the whole
			}
			walk(up, n, branch+".upstream")
		}
	}
}

// The tree of every hook is a JSON Schema too: the schema of the document a
// rule reads, and one day the document a webhook is sent. It is valid JSON,
// lists only what can be read, and marks what must be there.
func TestEveryTreeIsAJSONSchema(t *testing.T) {
	for name, h := range Hooks {
		s := h.JSONSchema()
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var back map[string]any
		if err := json.Unmarshal(b, &back); err != nil || back["type"] != "object" || back["title"] != name {
			t.Fatalf("%s: schema %s", name, b)
		}
		props := back["properties"].(map[string]any)
		for _, writeOnly := range []string{"settings", "session", "timeout"} {
			if props[writeOnly] != nil {
				t.Errorf("%s: the schema lists %s, which a rule cannot read", name, writeOnly)
			}
		}
		req := props["request"].(map[string]any)
		required, _ := json.Marshal(req["required"])
		for _, k := range []string{"method", "params", "caller", "client", "headers", "route", "flags", "time"} {
			if !strings.Contains(string(required), `"`+k+`"`) {
				t.Errorf("%s: request.%s is not required in the schema", name, k)
			}
		}
		if h.Item == KindTool {
			tool := props["tool"].(map[string]any)
			tr, _ := json.Marshal(tool["required"])
			if !strings.Contains(string(tr), `"name"`) || strings.Contains(string(tr), `"description"`) || strings.Contains(string(tr), `"annotations"`) {
				t.Errorf("%s: tool requires %s; only what MCP requires may be required", name, tr)
			}
		}
	}
}

// Every hook has a doc page, and the overview links it and documents every
// flag.
func TestEveryHookHasADocPage(t *testing.T) {
	dir := "../../docs/rules/hooks/"
	overview, err := os.ReadFile(dir + "overview.md")
	if err != nil {
		t.Skip("docs not present: " + err.Error())
	}
	for _, name := range Flags {
		if !strings.Contains(string(overview), "`request.flags."+name+"`") {
			t.Errorf("the overview does not document request.flags.%s", name)
		}
	}
	for hook, h := range Hooks {
		file := strings.NewReplacer("/", "-", ".", "-").Replace(hook)
		b, err := os.ReadFile(dir + file + ".md")
		if err != nil {
			t.Errorf("%s has no doc page: %v", hook, err)
			continue
		}
		for _, root := range h.Roots() {
			if !strings.Contains(string(b), "`"+root) {
				t.Errorf("%s.md does not mention %s", file, root)
			}
		}
		if !strings.Contains(string(overview), "("+file+".md)") {
			t.Errorf("the overview does not link %s", hook)
		}
	}
}

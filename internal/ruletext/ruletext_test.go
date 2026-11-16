package ruletext

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
)

// Every stage's example is real: it compiles with the engine's compiler.
func TestEveryExampleCompiles(t *testing.T) {
	for _, s := range Stages() {
		rules, probs := Compile(map[string]string{s.ID: s.Example})
		for _, p := range probs {
			t.Errorf("%s line %d: %s", p.Stage, p.Line, p.Message)
		}
		if len(probs) == 0 && len(rules) == 0 {
			t.Errorf("%s: the example holds no rule", s.ID)
		}
	}
}

// A stage is an engine hook and every hook is a stage, so no rule is out of
// reach and the editor shows the names the engine uses.
func TestEveryHookIsAStage(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range Stages() {
		if seen[s.ID] {
			t.Errorf("%s is listed twice", s.ID)
		}
		seen[s.ID] = true
		if _, ok := hooks.Hooks[s.ID]; !ok {
			t.Errorf("stage %s is not an engine hook", s.ID)
		}
	}
	for _, h := range hooks.Schema() {
		if !seen[h.Hook] {
			t.Errorf("hook %s has no stage", h.Hook)
		}
	}
}

func TestTextCompilesToTheEnginesRules(t *testing.T) {
	text := map[string]string{
		"tools/list.post": "# hide\nwhen name.startsWith('delete_')   deny\n\nwhen name == 'flip'\n  set name = 'toss'\n  set description = 'Toss a coin.'\n",
		"tools/call.pre": "when name == 'echo' deny \"Not today.\"\n" +
			"audit when caller.role != 'admin'  deny 'Admins: ' + name\n" +
			"when name == 'search' &&\n    !has(args.limit)\n  set args.limit = 50\n  set timeout = duration('30s')\n",
		"tools/call.post.result": "when true set structuredContent = drop(result.structuredContent, ['a'])",
		"server.settings":        "when route == 'mux' set prompts = false",
	}
	rules, probs := Compile(text)
	if len(probs) > 0 {
		t.Fatalf("problems: %+v", probs)
	}
	want := []hooks.Rule{
		{Name: "server.settings:1", Hook: "server.settings", When: "route == 'mux'", Then: map[string]string{"prompts": "false"}},
		{Name: "tools/list.post:2", Hook: "tools/list.post", When: "name.startsWith('delete_')", Then: map[string]string{"decision": "'deny'"}},
		{Name: "tools/list.post:4", Hook: "tools/list.post", When: "name == 'flip'", Then: map[string]string{"name": "'toss'", "description": "'Toss a coin.'"}},
		{Name: "tools/call.pre:1", Hook: "tools/call.pre", When: "name == 'echo'", Then: map[string]string{"decision": "'deny'", "reason": `"Not today."`}},
		{Name: "tools/call.pre:2", Mode: "audit", Hook: "tools/call.pre", When: "caller.role != 'admin'", Then: map[string]string{"decision": "'deny'", "reason": "'Admins: ' + name"}},
		{Name: "tools/call.pre:3", Hook: "tools/call.pre", When: "name == 'search' && !has(args.limit)", Then: map[string]string{"args.limit": "50", "timeout": "duration('30s')"}},
		{Name: "tools/call.post.result:1", Hook: "tools/call.post.result", When: "true", Then: map[string]string{"structuredContent": "drop(result.structuredContent, ['a'])"}},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules:\n got %+v\nwant %+v", rules, want)
	}
}

func TestConfirmBecomesTheEnginesQuestion(t *testing.T) {
	rules, probs := Compile(map[string]string{"tools/call.pre": `when name == 'delete_page'  confirm "Delete {args.id} in {args.space}?"`})
	if len(probs) > 0 {
		t.Fatalf("problems: %+v", probs)
	}
	r := rules[0]
	key := strings.TrimSuffix(strings.SplitN(r.When, "input.", 2)[1], ")")
	if !strings.HasPrefix(key, "confirm_") || r.When != "(name == 'delete_page') && !has(input."+key+")" {
		t.Fatalf("when = %s", r.When)
	}
	if want := `elicit("` + key + `", "Delete {v1} in {v2}?", {"v1": args.id, "v2": args.space})`; r.Then["result"] != want {
		t.Fatalf("result = %s\n  want %s", r.Then["result"], want)
	}
	// The key depends only on the stage and the question, so an answer still
	// matches after the rules around it are edited.
	again, _ := Compile(map[string]string{"tools/call.pre": "when true deny\n\n" + `when name == 'delete_page'  confirm "Delete {args.id} in {args.space}?"`})
	if again[1].When != r.When {
		t.Fatalf("the key moved: %s", again[1].When)
	}
}

func TestProblemsAreOnTheLineThatCausedThem(t *testing.T) {
	cases := []struct {
		name, stage, text string
		line              int
		has               string
	}{
		{"bad condition", "tools/call.pre", "when true deny\nwhen nope == 1 deny", 2, "nope"},
		{"bad value", "tools/call.pre", "when true\n  set args.limit = nope", 2, "args.limit"},
		{"unknown field", "tools/list.post", "when true\n\n  set colour = 'red'", 3, "colour"},
		{"action first", "tools/call.pre", "deny \"x\"", 1, "needs a when"},
		{"stray line", "tools/call.pre", "when true deny\nallow everything", 2, "expected a line"},
		{"no action", "tools/call.pre", "when true", 1, "does nothing"},
		{"no condition", "tools/call.pre", "when\n  deny", 1, "needs a condition"},
		{"deny where there is none", "server.settings", "when true deny", 1, "not available"},
		{"confirm where there is none", "tools/list.post", "when true confirm \"Sure?\"", 1, "not available"},
		{"reason with no deny", "tools/list.post", "when true\n  set reason = 'x'", 2, "beside decision"},
		{"confirm unquoted", "tools/call.pre", "when true confirm Sure?", 1, "in quotes"},
		{"set twice", "tools/call.pre", "when true\n  set args.a = 1\n  set args.a = 2", 3, "already sets"},
		{"no such stage", "tools/call.during", "when true deny", 0, "no such stage"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rules, probs := Compile(map[string]string{c.stage: c.text})
			if rules != nil || len(probs) == 0 {
				t.Fatalf("compiled: %+v", rules)
			}
			p := probs[0]
			if p.Stage != c.stage || p.Line != c.line || !strings.Contains(p.Message, c.has) {
				t.Fatalf("problem = %+v, want line %d holding %q", p, c.line, c.has)
			}
		})
	}
}

// Words that start an action do not split a condition when they sit inside
// a string or a bracket.
func TestAnActionWordInsideTheConditionIsNotAnAction(t *testing.T) {
	rules, probs := Compile(map[string]string{"tools/call.pre": `when name == 'x deny y' || [' set a = 1 '].exists(s, s == name)  deny "No."`})
	if len(probs) > 0 {
		t.Fatalf("problems: %+v", probs)
	}
	if want := `name == 'x deny y' || [' set a = 1 '].exists(s, s == name)`; rules[0].When != want {
		t.Fatalf("when = %s", rules[0].When)
	}
}

// Rules saved as JSON are shown as text that compiles back to the same rules.
func TestRenderRoundTrips(t *testing.T) {
	off := false
	in := []hooks.Rule{
		{Hook: "tools/list.post", When: "name == 'a'", Then: map[string]string{"decision": "'deny'", "reason": "'no'"}},
		{Hook: "tools/list.post", When: "true", Then: map[string]string{"name": "'b'", "params.q.hide": "true"}},
		{Hook: "tools/call.pre", Mode: "audit", When: "!has(input.ok)", Then: map[string]string{"result": "elicit('ok', 'Sure?')"}},
		{Hook: "tools/call.pre", When: "true", Enabled: &off, Then: map[string]string{"decision": "'deny'"}},
		{Hook: "server.session", When: "true", Then: map[string]string{"meta": "true", "capabilities.elicitation": "true"}},
		{Hook: "resources/read.post.result", When: "true", Then: map[string]string{"text": "result.text +\n ' (reviewed)'"}},
	}
	text := Render(in)
	if !strings.Contains(text["tools/call.pre"], "# Switched off:\n# when true\n#   deny\n") {
		t.Fatalf("the rule that is off is not a comment:\n%s", text["tools/call.pre"])
	}
	out, probs := Compile(text)
	if len(probs) > 0 {
		t.Fatalf("problems: %+v\n%v", probs, text)
	}
	if len(out) != 5 {
		t.Fatalf("%d rules back, want 5 (the one that is off is a comment)", len(out))
	}
	find := func(hook string) hooks.Rule {
		for i, r := range out {
			if r.Hook == hook {
				out = append(out[:i:i], out[i+1:]...) // each rule is found once, in order
				return r
			}
		}
		t.Fatalf("no %s rule came back", hook)
		return hooks.Rule{}
	}
	for _, w := range []hooks.Rule{in[0], in[1], in[2], in[4]} {
		g := find(w.Hook)
		if g.When != w.When || g.Mode != w.Mode || !reflect.DeepEqual(g.Then, w.Then) {
			t.Errorf("%s came back as %+v, want %+v", w.Hook, g, w)
		}
	}
	if g := find("resources/read.post.result"); g.Then["text"] != "result.text + ' (reviewed)'" {
		t.Errorf("a value over two lines came back as %q", g.Then["text"])
	}
}

// Lint warns about rules that compile but look wrong, and stays quiet about
// rules that are fine.
func TestLintWarnsAboutRulesThatLookWrong(t *testing.T) {
	catalog := map[string][]string{"tool": {"echo", "flip", "search"}}
	text := map[string]string{
		"tools/list.post": "when name == 'flip'  set name = 'toss'\nwhen upstream.name == 'toss'  deny",
		"tools/call.pre": "when name == 'ecoh'  deny \"typo\"\n" + // 1: unknown, near echo
			"when name == 'toss'  deny \"renamed, so fine\"\n" + // 2: fine
			"when name in ['echo', 'serch']  deny\n" + // 3: unknown in a list
			"when false  deny\n" + // 4: never applies
			"audit when true  deny \"an audit rule stops nothing\"\n" + // 5: fine
			"when true  deny \"closed\"\n" + // 6: stops everything
			"when name == 'echo'  set args.x = 1\n", // 7: never runs
		"prompts/get.pre": "when name == 'anything'  deny", // prompts were never listed: not checked
	}
	got := map[string]string{}
	for _, p := range Lint(text, catalog) {
		if p.Level != "warning" {
			t.Errorf("%+v is not a warning", p)
		}
		got[fmt.Sprintf("%s:%d", p.Stage, p.Line)] = p.Message
	}
	want := map[string]string{
		"tools/list.post:2": `no tool named "toss"`, // upstream.name is the server's name, which a rename does not change
		"tools/call.pre:1":  `did you mean "echo"?`,
		"tools/call.pre:3":  `no tool named "serch"; did you mean "search"?`,
		"tools/call.pre:4":  "never applies",
		"tools/call.pre:7":  "line 6 denies everything",
	}
	for at, has := range want {
		if !strings.Contains(got[at], has) {
			t.Errorf("%s: got %q, want it to hold %q", at, got[at], has)
		}
	}
	if len(got) != len(want) {
		t.Errorf("warnings: %v", got)
	}
}

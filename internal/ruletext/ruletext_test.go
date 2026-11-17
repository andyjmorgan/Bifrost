package ruletext

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Every hook's example is real: it compiles with the engine's compiler.
func TestEveryExampleCompiles(t *testing.T) {
	for _, s := range List() {
		rules, probs := Compile(map[string]string{s.Name: s.Example})
		for _, p := range probs {
			t.Errorf("%s line %d: %s", p.Hook, p.Line, p.Message)
		}
		if len(probs) == 0 && len(rules) == 0 {
			t.Errorf("%s: the example holds no rule", s.Name)
		}
	}
}

// Every engine hook is described once, under the engine's name, so no rule
// is out of reach and an editor shows the names the engine uses.
func TestEveryEngineHookIsDescribed(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range List() {
		if seen[s.Name] {
			t.Errorf("%s is listed twice", s.Name)
		}
		seen[s.Name] = true
		if _, ok := hooks.Hooks[s.Name]; !ok {
			t.Errorf("%s is not an engine hook", s.Name)
		}
	}
	for _, h := range hooks.Schema() {
		if !seen[h.Hook] {
			t.Errorf("hook %s is not described", h.Hook)
		}
	}
}

func TestTextCompilesToTheEnginesRules(t *testing.T) {
	text := map[string]string{
		"tools/list.post": "# hide\nwhen tool.name.startsWith('delete_')   deny\n\nwhen tool.name == 'flip'\n  set tool.name = 'toss'\n  set tool.description = 'Toss a coin.'\n",
		"tools/call.pre": "when tool.name == 'echo' deny \"Not today.\"\n" +
			"audit when request.caller.role != 'admin'  deny 'Admins: ' + tool.name\n" +
			"when tool.name == 'search' &&\n    !has(request.params.arguments.limit)\n  set request.params.arguments.limit = 50\n  set timeout = duration('30s')\n",
		"tools/call.post.result": "when true set response.result.structuredContent = drop(response.result.?structuredContent.orValue({}), ['a'])",
		"server.settings":        "when request.route == 'mux' set settings.prompts = false",
	}
	rules, probs := Compile(text)
	if len(probs) > 0 {
		t.Fatalf("problems: %+v", probs)
	}
	want := []hooks.Rule{
		{Name: "server.settings:1", Hook: "server.settings", When: "request.route == 'mux'", Then: map[string]string{"settings.prompts": "false"}},
		{Name: "tools/list.post:2", Hook: "tools/list.post", When: "tool.name.startsWith('delete_')", Then: map[string]string{"decision": "'deny'"}},
		{Name: "tools/list.post:4", Hook: "tools/list.post", When: "tool.name == 'flip'", Then: map[string]string{"tool.name": "'toss'", "tool.description": "'Toss a coin.'"}},
		{Name: "tools/call.pre:1", Hook: "tools/call.pre", When: "tool.name == 'echo'", Then: map[string]string{"decision": "'deny'", "reason": `"Not today."`}},
		{Name: "tools/call.pre:2", Mode: "audit", Hook: "tools/call.pre", When: "request.caller.role != 'admin'", Then: map[string]string{"decision": "'deny'", "reason": "'Admins: ' + tool.name"}},
		{Name: "tools/call.pre:3", Hook: "tools/call.pre", When: "tool.name == 'search' && !has(request.params.arguments.limit)", Then: map[string]string{"request.params.arguments.limit": "50", "timeout": "duration('30s')"}},
		{Name: "tools/call.post.result:1", Hook: "tools/call.post.result", When: "true", Then: map[string]string{"response.result.structuredContent": "drop(response.result.?structuredContent.orValue({}), ['a'])"}},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules:\n got %+v\nwant %+v", rules, want)
	}
}

func TestConfirmBecomesTheEnginesQuestion(t *testing.T) {
	rules, probs := Compile(map[string]string{"tools/call.pre": `when tool.name == 'delete_page'  confirm "Delete {request.params.arguments.id} in {request.params.arguments.space}?"`})
	if len(probs) > 0 {
		t.Fatalf("problems: %+v", probs)
	}
	r := rules[0]
	// The condition is the author's own: the engine, not the rule, decides
	// whether the question has been answered.
	if r.When != "tool.name == 'delete_page'" {
		t.Fatalf("when = %s", r.When)
	}
	key := strings.SplitN(strings.TrimPrefix(r.Then["confirm"], `elicit("`), `"`, 2)[0]
	if !strings.HasPrefix(key, "confirm_") {
		t.Fatalf("key = %s", key)
	}
	if want := `elicit("` + key + `", "Delete {v1} in {v2}?", {"v1": request.params.arguments.id, "v2": request.params.arguments.space})`; r.Then["confirm"] != want {
		t.Fatalf("confirm = %s\n  want %s", r.Then["confirm"], want)
	}
	// The key depends only on the hook and the question, so an answer still
	// matches after the rules around it are edited.
	again, _ := Compile(map[string]string{"tools/call.pre": "when true deny\n\n" + `when tool.name == 'delete_page'  confirm "Delete {request.params.arguments.id} in {request.params.arguments.space}?"`})
	if again[1].When != r.When {
		t.Fatalf("the key moved: %s", again[1].When)
	}
}

func TestProblemsAreOnTheLineThatCausedThem(t *testing.T) {
	cases := []struct {
		name, hook, text string
		line             int
		has              string
	}{
		{"bad condition", "tools/call.pre", "when true deny\nwhen nope == 1 deny", 2, "nope"},
		{"old name", "tools/call.pre", "when name == 'x' deny", 1, "name"},
		{"no such field", "tools/call.pre", "when true deny\n\nwhen tool.nmae == 'x' deny", 3, `tool has no "nmae"`},
		{"upstream before it is sent", "tools/call.pre", "when request.upstream.params.name == 'x' deny", 1, "request.upstream is not available"},
		{"bad value", "tools/call.pre", "when true\n  set request.params.arguments.limit = nope", 2, "request.params.arguments.limit"},
		{"unknown field", "tools/list.post", "when true\n\n  set tool.colour = 'red'", 3, "there is no tool.colour"},
		{"a field that cannot be changed", "tools/list.post", "when true\n  set tool.upstream.name = 'x'", 2, "tool.upstream.name cannot be changed"},
		{"another branch", "tools/call.post.result", "when true\n  set request.params.arguments.q = 'x'", 2, "cannot be changed on tools/call.post.result"},
		{"action first", "tools/call.pre", "deny \"x\"", 1, "needs a when"},
		{"stray line", "tools/call.pre", "when true deny\nallow everything", 2, "expected a line"},
		{"no action", "tools/call.pre", "when true", 1, "does nothing"},
		{"no condition", "tools/call.pre", "when\n  deny", 1, "needs a condition"},
		{"deny where there is none", "server.settings", "when true deny", 1, "not available"},
		{"confirm where there is none", "tools/list.post", "when true confirm \"Sure?\"", 1, "not available"},
		{"confirm unquoted", "tools/call.pre", "when true confirm Sure?", 1, "in quotes"},
		{"set twice", "tools/call.pre", "when true\n  set request.params.arguments.a = 1\n  set request.params.arguments.a = 2", 3, "already sets"},
		{"no such hook", "tools/call.during", "when true deny", 0, "no such hook"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rules, probs := Compile(map[string]string{c.hook: c.text})
			if rules != nil || len(probs) == 0 {
				t.Fatalf("compiled: %+v", rules)
			}
			p := probs[0]
			if p.Hook != c.hook || p.Line != c.line || !strings.Contains(p.Message, c.has) {
				t.Fatalf("problem = %+v, want line %d holding %q", p, c.line, c.has)
			}
		})
	}
}

// Words that start an action do not split a condition when they sit inside
// a string or a bracket.
func TestAnActionWordInsideTheConditionIsNotAnAction(t *testing.T) {
	rules, probs := Compile(map[string]string{"tools/call.pre": `when tool.name == 'x deny y' || [' set a = 1 '].exists(s, s == tool.name)  deny "No."`})
	if len(probs) > 0 {
		t.Fatalf("problems: %+v", probs)
	}
	if want := `tool.name == 'x deny y' || [' set a = 1 '].exists(s, s == tool.name)`; rules[0].When != want {
		t.Fatalf("when = %s", rules[0].When)
	}
}

// Lint warns about rules that compile but look wrong, and stays quiet about
// rules that are fine.
func TestLintWarnsAboutRulesThatLookWrong(t *testing.T) {
	catalog := map[string][]string{"tool": {"echo", "flip", "search"}}
	text := map[string]string{
		"tools/list.post": "when tool.name == 'flip'  set tool.name = 'toss'\nwhen tool.upstream.name == 'toss'  deny\n" +
			"when tool.description.contains('x')  deny\n" + // 3: an optional value, read with no guard
			"when tool.?description.orValue('').contains('x')  deny", // 4: fine
		"tools/call.pre": "when tool.name == 'ecoh'  deny \"typo\"\n" + // 1: unknown, near echo
			"when tool.name == 'toss'  deny \"renamed, so fine\"\n" + // 2: fine
			"when tool.name in ['echo', 'serch']  deny\n" + // 3: unknown in a list
			"when false  deny\n" + // 4: never applies
			"audit when true  deny \"an audit rule stops nothing\"\n" + // 5: fine
			"when true  deny \"closed\"\n" + // 6: stops everything
			"when tool.name == 'echo'  set request.params.arguments.x = 1\n", // 7: never runs
		"prompts/get.pre": "when prompt.name == 'anything'  deny", // prompts were never listed: not checked
	}
	got := map[string]string{}
	for _, p := range Lint(text, catalog) {
		if p.Level != "warning" {
			t.Errorf("%+v is not a warning", p)
		}
		got[fmt.Sprintf("%s:%d", p.Hook, p.Line)] = p.Message
	}
	want := map[string]string{
		"tools/list.post:2": `no tool named "toss"`, // upstream.name is the server's name, which a rename does not change
		"tools/list.post:3": "tool.description may be absent",
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

// No worked example in a hook's help reads an optional value without a
// guard: the examples are what people copy.
func TestNoExampleReadsAnOptionalValueUnguarded(t *testing.T) {
	for _, h := range List() {
		for _, p := range Lint(map[string]string{h.Name: h.Example}, nil) {
			if strings.Contains(p.Message, "may be absent") {
				t.Errorf("%s line %d: %s", h.Name, p.Line, p.Message)
			}
		}
	}
}

// Every hook has exactly one property in Hooks, named by the one rule, and
// Hooks has no property that is not a hook's.
func TestEveryHookHasItsProperty(t *testing.T) {
	seen := map[string]string{}
	for _, s := range List() {
		if s.Property != Property(s.Name) || s.Property == "" {
			t.Errorf("%s: property %q", s.Name, s.Property)
		}
		if other, dup := seen[s.Property]; dup {
			t.Errorf("%s and %s share the property %s", s.Name, other, s.Property)
		}
		seen[s.Property] = s.Name
		if _, ok := fieldOf[s.Property]; !ok {
			t.Errorf("Hooks has no field for %s (%s)", s.Name, s.Property)
		}
	}
	if len(fieldOf) != len(List()) {
		t.Errorf("Hooks has %d properties for %d hooks", len(fieldOf), len(List()))
	}
	for hook, want := range map[string]string{
		"tools/call.post.result": "toolsCallPostResult", "server.settings": "serverSettings",
		"server/discover.post": "serverDiscoverPost", "resources/templates/list.post": "resourcesTemplatesListPost",
	} {
		if got := Property(hook); got != want {
			t.Errorf("Property(%s) = %s, want %s", hook, got, want)
		}
	}
}

// Hooks go to the API with every property, null where a hook has no rules,
// and to the database with only the ones that have text. What comes back is
// the text exactly as written.
func TestHooksAreStoredAsTextByProperty(t *testing.T) {
	text := "# why\nwhen name == 'echo'   deny \"Not today.\"\n\n# when false deny\n"
	in, _ := json.Marshal(map[string]string{"toolsCallPre": text, "toolsListPost": "  \n"})
	h, err := ParseStored(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(h.Stored()) != `{"toolsCallPre":"# why\nwhen name == 'echo'   deny \"Not today.\"\n\n# when false deny\n"}` {
		t.Fatalf("stored = %s", h.Stored())
	}
	wire, _ := json.Marshal(h)
	var all map[string]*string
	if err := json.Unmarshal(wire, &all); err != nil || len(all) != len(List()) || all["toolsListPost"] != nil || *all["toolsCallPre"] != text {
		t.Fatalf("wire = %s", wire)
	}
	back, err := ParseStored(h.Stored())
	if err != nil || !reflect.DeepEqual(back.ByName(), map[string]string{"tools/call.pre": text}) {
		t.Fatalf("back = %v, %v", back.ByName(), err)
	}
	for _, empty := range []string{"", "null", "{}", `{"toolsCallPre":null}`, `{"toolsCallPre":"  "}`} {
		if h, err := ParseStored([]byte(empty)); err != nil || !h.Empty() {
			t.Errorf("%q: %v, %v", empty, h.ByName(), err)
		}
	}
	for _, bad := range []string{`{"toolsCallPer":"when true deny"}`, `{"tools/call.pre":"when true deny"}`, `[]`, `{"toolsCallPre":"a\u0000b"}`, `{"toolsCallPre":5}`} {
		if _, err := ParseStored([]byte(bad)); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

// Rule text is stored, so text that was saved must keep compiling. The
// corpus is text as it was saved: it is added to, never edited. A change to
// the compiler or the tree that breaks an entry here would leave real
// entitlements exposing nothing.
func TestSavedTextKeepsCompiling(t *testing.T) {
	raw, err := os.ReadFile("testdata/saved.json")
	if err != nil {
		t.Fatal(err)
	}
	var saved []struct {
		Name  string          `json:"name"`
		Hooks json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved) == 0 {
		t.Fatal("the corpus is empty")
	}
	for _, s := range saved {
		h, err := ParseStored(s.Hooks)
		if err != nil {
			t.Errorf("%s: %v", s.Name, err)
			continue
		}
		rules, probs := Compile(h.ByName())
		if len(probs) > 0 || len(rules) == 0 {
			t.Errorf("%s: %d rules, problems %+v", s.Name, len(rules), probs)
		}
	}
}

// A // comment ends at the end of its line. The lines of a rule are joined
// into one expression, and CEL reads a comment to the end of that.
func TestACommentDoesNotSwallowAContinuationLine(t *testing.T) {
	rules, probs := Compile(map[string]string{"tools/list.post": "when tool.name == 'a' // or the other one\n  || tool.name == 'b'\n  deny\n"})
	if len(probs) > 0 || rules[0].When != "tool.name == 'a' || tool.name == 'b'" {
		t.Fatalf("when = %q, problems %+v", rules[0].When, probs)
	}
	rules, probs = Compile(map[string]string{"tools/call.pre": "when tool.name == 'a'\n  set request.params.arguments.limit = 5 // cap\n    + 100\n"})
	if len(probs) > 0 || rules[0].Then["request.params.arguments.limit"] != "5 + 100" {
		t.Fatalf("then = %+v, problems %+v", rules[0].Then, probs)
	}
	// A // inside a string is text, and a comment may follow a confirm.
	rules, probs = Compile(map[string]string{"tools/call.pre": "when tool.name.startsWith('http://') // links\n  confirm \"Open http://x?\" // ask first\n"})
	if len(probs) > 0 || rules[0].When != "tool.name.startsWith('http://')" || !strings.Contains(rules[0].Then["confirm"], "Open http://x?") {
		t.Fatalf("rule = %+v, problems %+v", rules, probs)
	}
}

// A rule either denies or asks. With both, the deny would always win and
// the question would never be asked.
func TestARuleCannotBothDenyAndConfirm(t *testing.T) {
	_, probs := Compile(map[string]string{"tools/call.pre": "when true\n  confirm \"Sure?\"\n  deny \"no\"\n"})
	if len(probs) == 0 || !strings.Contains(probs[0].Message, "either denies or asks") {
		t.Fatalf("problems = %+v", probs)
	}
}

// A brace inside a string in a question's {expression} is text.
func TestABraceInAStringInAQuestion(t *testing.T) {
	rules, probs := Compile(map[string]string{"tools/call.pre": "when true\n  confirm \"Run {request.params.arguments['}']}?\"\n"})
	if len(probs) > 0 || !strings.Contains(rules[0].Then["confirm"], `"v1": request.params.arguments['}']`) {
		t.Fatalf("rule = %+v, problems %+v", rules, probs)
	}
}

// A string that runs over lines is refused: the lines of a rule are joined
// with a space, so it would not hold the text that was written.
func TestAStringMustCloseOnItsLine(t *testing.T) {
	_, probs := Compile(map[string]string{"tools/list.post": "when tool.description.contains('''see\n  http://x.example\n  ''')\n  deny\n"})
	if len(probs) == 0 || !strings.Contains(probs[0].Message, "must close on the line") {
		t.Fatalf("problems = %+v", probs)
	}
}

// An escape the question's reader does not know is refused, not read
// without its backslash.
func TestAnUnknownEscapeInAQuestionIsRefused(t *testing.T) {
	_, probs := Compile(map[string]string{"tools/call.pre": "when true\n  confirm \"Caf\\u00e9?\"\n"})
	if len(probs) == 0 || !strings.Contains(probs[0].Message, "backslash") {
		t.Fatalf("problems = %+v", probs)
	}
	rules, probs := Compile(map[string]string{"tools/call.pre": "when true\n  confirm \"It's \\\"fine\\\" \\\\ ok\"\n"})
	if len(probs) > 0 || !strings.Contains(rules[0].Then["confirm"], `It's \"fine\" \\ ok`) {
		t.Fatalf("rules = %+v, problems %+v", rules, probs)
	}
}

// The same question asked twice, with a change to the arguments between,
// is two questions: each is asked and answered.
func TestTheSameQuestionTwiceWithASetBetween(t *testing.T) {
	rules, probs := Compile(map[string]string{"tools/call.pre": "when true\n  confirm \"Sure?\"\nwhen true\n  set request.params.arguments.q = 'changed'\nwhen true\n  confirm \"Sure?\"\n"})
	if len(probs) > 0 {
		t.Fatalf("problems: %v", probs)
	}
	set, eps := hooks.Compile(rules)
	if eps != nil {
		t.Fatalf("problems: %v", eps)
	}
	s := &hooks.Server{Slug: "a", Generation: "1", Info: hooks.ServerInfo{ProtocolVersion: hooks.ProtocolCurrent}, Rules: set,
		Catalog: hooks.Catalog{Tools: []*mcp.Tool{{Name: "t", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}}}}}
	r := hooks.Request{Route: "direct", Servers: []*hooks.Server{s}, Principal: "p", Client: map[string]any{"capabilities": map[string]any{"elicitation": map[string]any{}}}}
	e, ctx := &hooks.Engine{}, context.Background()
	p := &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`)}
	for round := 1; round <= 3; round++ {
		o := e.CallTool(ctx, r, p)
		if o.Forward != nil {
			if round != 3 {
				t.Fatalf("forwarded on round %d, want two questions first", round)
			}
			return
		}
		ir, asked := o.Result.(hooks.InputRequired)
		if !asked {
			b, _ := json.Marshal(o.Result)
			t.Fatalf("round %d: refused, and no question to answer: %s", round, b)
		}
		in := mcp.InputResponseMap{}
		for k := range ir.InputRequests {
			in[k] = &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}
		}
		p = &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`), RequestState: ir.RequestState, InputResponses: in}
	}
	t.Error("the gateway kept asking")
}

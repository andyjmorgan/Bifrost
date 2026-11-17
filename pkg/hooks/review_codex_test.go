package hooks

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Independent review of ba03834. Gap tests assert the intended behavior and
// opt in through knownGap; the other tests exercise boundaries that held.

func TestReviewCodexFixedURIAliases(t *testing.T) {
	pairs := [][2]string{
		{"https://[::1]/private/x", "https://[0:0:0:0:0:0:0:1]/private/x"},
		{"https://xn--bcher-kva.example/private/x", "https://bücher.example/private/x"},
		{"https://wiki.example/private/x", "https://%77iki.example/private/x"},
		{"https://127.0.0.1/private/x", "https://2130706433/private/x"},
		{"https://wiki.example/private/x", "https://wiki.example:0443/private/x"},
		{"https://wiki.example/private/x", "https://ｗｉｋｉ.example/private/x"},
		{"https://wiki.example/caf%C3%A9/x", "https://wiki.example/café/x"},
	}
	for _, pair := range pairs {
		for _, kind := range []string{"prefix", "exact", "hidden", "template"} {
			t.Run(kind+"/"+pair[1], func(t *testing.T) {
				cat := Catalog{}
				rule := Rule{Hook: "resources/list.post", When: "resource.uri == " + fmt.Sprintf("%q", pair[0]), Then: map[string]string{KeyDecision: "'deny'"}}
				switch kind {
				case "prefix":
					rule.When = "resource.uri.startsWith(" + fmt.Sprintf("%q", strings.TrimSuffix(pair[0], "x")) + ")"
				case "hidden":
					cat.Resources = []*mcp.Resource{{URI: pair[0], Name: "secret"}}
					rule.When = "resource.?name.orValue('') == 'secret'"
				case "template":
					cat.Templates = []*mcp.ResourceTemplate{{URITemplate: strings.TrimSuffix(pair[0], "x") + "{id}", Name: "secret"}}
					rule.Hook, rule.When = "resources/templates/list.post", "true"
				}
				s := rvServer(t, cat, rule)
				for _, route := range []string{"direct", "mux"} {
					r := treeRequest(s)
					r.Route = route
					uri := func(u string) string {
						if route == "mux" {
							return "a+" + u
						}
						return u
					}
					e := rvEngine()
					if o := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: uri(pair[0])}); o.Forward != nil {
						t.Fatalf("control forwarded: %s", outcomeText(o))
					}
					if o := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: uri(pair[1])}); o.Forward != nil {
						t.Errorf("%s alias forwarded: %s", route, rvJSON(o.Forward.Params))
					}
				}
			})
		}
	}
}

func TestReviewCodexFixedHiddenVariants(t *testing.T) {
	for _, tc := range []struct {
		name, hidden, alias string
		listedAlias         bool
	}{
		{"authority-root", "https://wiki.example", "https://wiki.example/", false},
		{"unlisted-query", "docs://h/private", "docs://h/private?view=raw", false},
		{"unlisted-slash", "docs://h/private", "docs://h/private/", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cat := Catalog{Resources: []*mcp.Resource{{URI: tc.hidden, Name: "secret"}}}
			if tc.listedAlias {
				cat.Resources = append(cat.Resources, &mcp.Resource{URI: tc.alias, Name: "alias"})
			}
			s := rvServer(t, cat, Rule{Hook: "resources/list.post", When: "resource.?name.orValue('') == 'secret'", Then: map[string]string{KeyDecision: "'deny'"}})
			e, r := rvEngine(), treeRequest(s)
			if o := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: tc.hidden}); o.Forward != nil {
				t.Fatal("control forwarded")
			}
			if o := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: tc.alias}); o.Forward != nil {
				t.Errorf("hidden alias forwarded: %s", outcomeText(o))
			}
			o := rvRead(t, s, "docs://h/public", `{"contents":[{"uri":`+fmt.Sprintf("%q", tc.alias)+`,"text":"SECRET"}]}`)
			if strings.Contains(rvJSON(o.Result), "SECRET") {
				t.Errorf("hidden alias returned: %s", outcomeText(o))
			}
		})
	}
}

func TestReviewCodexFixedSkillRootQuery(t *testing.T) {
	s := rvServer(t, Catalog{Skills: []Skill{{URI: "skill://demo/SKILL.md", Resources: json.RawMessage(`"dynamic"`)}}},
		Rule{Hook: "skills/list.post", When: "true", Then: map[string]string{KeyDecision: "'deny'"}})
	e, r := rvEngine(), treeRequest(s)
	for _, uri := range []string{"skill://demo?view=raw", "skill://demo/?view=raw"} {
		if o := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: uri}); o.Forward != nil {
			t.Errorf("denied skill root read forwarded: %s", rvJSON(o.Forward))
		}
		if o := e.ReadDirectory(context.Background(), r, &mcp.ReadResourceParams{URI: uri}); o.Forward != nil {
			t.Errorf("denied skill directory forwarded: %s", rvJSON(o.Forward))
		}
	}
}

func TestReviewCodexFixedConfirmationRoundedNumbers(t *testing.T) {
	for _, pair := range [][2]string{{"9007199254740992.0", "9007199254740993.0"}, {"0.100000000000000001", "0.100000000000000002"}, {"1e-400", "2e-400"}} {
		t.Run(pair[0], func(t *testing.T) {
			a, _ := new(big.Rat).SetString(pair[0])
			b, _ := new(big.Rat).SetString(pair[1])
			if a.Cmp(b) == 0 {
				t.Fatal("test requires distinct exact numbers")
			}
			s := rvServer(t, vfAnyTool, Rule{Hook: "tools/call.pre", When: "true", Then: vfConfirm("sure", "Proceed?")})
			e, r := rvEngine(), treeRequest(s)
			arg := func(n string) json.RawMessage { return json.RawMessage(`{"nested":{"values":[` + n + `]}}`) }
			first := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", Arguments: arg(pair[0])})
			key := vfState(first)
			if key == "" {
				t.Fatalf("no question: %s", outcomeText(first))
			}
			o := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", Arguments: arg(pair[1]), RequestState: key, InputResponses: rvYes})
			if o.Forward != nil {
				t.Errorf("yes for %s forwarded different number %s: %s", pair[0], pair[1], rvJSON(o.Forward.Params))
			}
		})
	}
}

func TestReviewCodexConfirmationRace(t *testing.T) {
	s := rvServer(t, rvTools(), Rule{Hook: "tools/call.pre", When: "true", Then: vfConfirm("sure", "Proceed?")})
	e, r := rvEngine(), treeRequest(s)
	key := vfState(e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t"}))
	if key == "" {
		t.Fatal("no question")
	}
	start, results := make(chan struct{}), make(chan Outcome, 16)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", RequestState: key, InputResponses: rvYes})
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	n := 0
	for o := range results {
		if o.Forward != nil {
			n++
		} else if o.Error == nil {
			t.Errorf("unexpected losing answer: %s", outcomeText(o))
		}
	}
	if n != 1 {
		t.Errorf("%d answers forwarded, want one", n)
	}
}

func TestReviewCodexFixedPromptSessionBudget(t *testing.T) {
	s := rvServer(t, Catalog{Prompts: []*mcp.Prompt{{Name: "p"}}}, Rule{Hook: "server.session", When: "true", Then: map[string]string{"session._meta": "true"}})
	e := &Engine{Limits: Limits{Budget: time.Nanosecond}}
	o := e.GetPrompt(context.Background(), treeRequest(s), &mcp.GetPromptParams{Name: "p"})
	if o.Forward != nil {
		t.Errorf("prompt forwarded after its only rule exhausted the budget: %s", rvJSON(o))
	}
}

// C8, by design. On the mux a URI that the named server does not list can
// be read two ways: a+docs://h/x is server a and docs://h/x, or a URI of
// the scheme a+docs that another server owns, as git+ssh://host/repo is.
// While another server's catalog is unavailable the gateway cannot tell,
// and it does not guess: case V3-S13 holds it to that. A URI the named
// server lists is not held up.
func TestReviewCodexAnOutageHoldsUpOnlyWhatCannotBeTold(t *testing.T) {
	a, b := rvServer(t, Catalog{Resources: []*mcp.Resource{{URI: "docs://h/listed", Name: "l"}}}), rvServer(t, Catalog{})
	b.Slug, b.Unavailable = "b", true
	r := treeRequest(a)
	r.Route, r.Servers = "mux", []*Server{a, b}
	if o := rvEngine().ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: "a+docs://h/listed"}); o.Forward == nil || o.Forward.Slug != "a" {
		t.Errorf("a listed read on a was held up by b: %s", outcomeText(o))
	}
	o := rvEngine().ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: "a+docs://h/unlisted"})
	if o.Forward != nil || !strings.Contains(outcomeText(o), "b unavailable") {
		t.Errorf("an unlisted read with b unavailable: %s", outcomeText(o))
	}
}

func TestReviewCodexURIFormsAndFallback(t *testing.T) {
	e, s := rvEngine(), rvServer(t, Catalog{})
	for _, route := range []string{"direct", "mux"} {
		r := treeRequest(s)
		r.Route = route
		for _, uri := range []string{"urn:example:a", "data:text/plain,a%20b", "confluence://ENG/home", "git+ssh://host/repo", "file:///a%2Fb", "file:///a%25b", "web://fetch?url=https://example.com/a//b"} {
			p := uri
			if route == "mux" {
				p = "a+" + p
			}
			o := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: p})
			if o.Forward == nil || o.Forward.Params.(*mcp.ReadResourceParams).URI != uri {
				t.Errorf("valid URI %s on %s: %s", uri, route, outcomeText(o))
			}
		}
		for _, uri := range []string{"file:///a%", "file:///a%0", "file:///a%GG", "file:///a%2f", "file:///a%41", "file:///a/../b", "file:///a//b", "file:/a", "https://host:443/a"} {
			if route == "mux" {
				uri = "a+" + uri
			}
			if o := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: uri}); o.Forward != nil {
				t.Errorf("noncanonical URI forwarded: %s", uri)
			}
		}
	}
	a := rvServer(t, Catalog{Resources: []*mcp.Resource{{URI: "docs://h/x", Name: "x"}}})
	b := rvServer(t, a.Catalog)
	b.Slug = "b"
	r := treeRequest(a)
	r.Route, r.Servers = "mux", []*Server{a, b}
	if o := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: "docs://h/x"}); o.Forward != nil || o.Fallback == nil || o.Fallback.Outcome != "ambiguous" {
		t.Errorf("fallback ambiguity guessed: %s", outcomeText(o))
	}
	r.Servers = []*Server{a}
	if o := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: "docs://h/x"}); o.Forward == nil || o.Forward.Slug != "a" {
		t.Errorf("unique exact fallback failed: %s", outcomeText(o))
	}
}

func TestReviewCodexFixedReadResponseFields(t *testing.T) {
	for _, field := range []string{"response.result.resultType", "response.upstream.result.resultType", "response.result._meta.label", "response.upstream.result._meta.label"} {
		t.Run(field, func(t *testing.T) {
			s := rvServer(t, Catalog{}, Rule{Hook: "resources/read.post.result", When: field + " == 'block'", Then: map[string]string{KeyDecision: "'deny'"}})
			o := rvRead(t, s, "docs://a", `{"resultType":"complete","_meta":{"label":"safe"},"contents":[{"uri":"docs://a","text":"OK"}]}`)
			if o.Error != nil {
				t.Errorf("present response field vanished: %s", outcomeText(o))
			}
		})
	}
}

func TestReviewCodexFixedNestedSkillPlans(t *testing.T) {
	outerURI, innerURI, sharedURI := "skill://demo/SKILL.md", "skill://demo/sub/SKILL.md", "skill://demo/sub/x.md"
	outerText, innerText, sharedText := "---\nname: demo\ndescription: outer\n---\n", "---\nname: sub\ndescription: inner\n---\n", "hunter2"
	makeSkill := func(uri string, files map[string]string, name string) Skill {
		var manifest []SkillResource
		fs := map[string]File{}
		for _, u := range sortedKeys(files) {
			text := files[u]
			manifest = append(manifest, SkillResource{URI: u, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(text))), Size: int64(len(text))})
			fs[u] = File{Text: &text}
		}
		return Skill{URI: uri, Frontmatter: map[string]any{"name": name, "description": "test"}, Resources: json.RawMessage(rvJSON(manifest)), Files: fs}
	}
	outer := makeSkill(outerURI, map[string]string{outerURI: outerText, sharedURI: sharedText}, "demo")
	inner := makeSkill(innerURI, map[string]string{innerURI: innerText, sharedURI: sharedText}, "sub")
	s := rvServer(t, Catalog{Skills: []Skill{outer, inner}},
		Rule{Hook: "resources/read.post.result", When: "true", Then: map[string]string{"response.result.contents": "response.result.contents.map(c, {'uri': c.uri, 'text': c.text.replace('name: demo', 'name: renamed').replace('hunter2', 'REDACTED')})"}})
	e, r := rvEngine(), treeRequest(s)
	listed := e.GetSkill(context.Background(), r, innerURI)
	if listed.Error != nil {
		t.Fatalf("skills/get failed: %s", outcomeText(listed))
	}
	// A file two skills answer for is never rewritten, so no manifest can
	// promise bytes a read does not return: both keep the server's.
	wantDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(sharedText)))
	if !strings.Contains(rvJSON(listed.Result), wantDigest) {
		t.Fatalf("inner manifest does not describe the server's bytes: %s", outcomeText(listed))
	}
	o := rvRead(t, s, sharedURI, `{"contents":[{"uri":"skill://demo/sub/x.md","text":"hunter2"}]}`)
	if !strings.Contains(rvJSON(o.Result), "hunter2") {
		t.Errorf("read disagrees with the manifest: %s", outcomeText(o))
	}
	if !strings.Contains(strings.Join(o.Issues, "\n"), "was not applied") {
		t.Errorf("a rule's change was set aside and no issue says so: %v", o.Issues)
	}
}

func TestReviewCodexFixedDynamicOuterSkill(t *testing.T) {
	text := "hunter2"
	inner := Skill{URI: "skill://demo/sub/SKILL.md", Frontmatter: map[string]any{"name": "sub"},
		Resources: json.RawMessage(`[{"uri":"skill://demo/sub/x.md","digest":"original","size":7}]`), Files: map[string]File{"skill://demo/sub/x.md": {Text: &text}}}
	outer := Skill{URI: "skill://demo/SKILL.md", Resources: json.RawMessage(`"dynamic"`)}
	s := rvServer(t, Catalog{Skills: []Skill{outer, inner}}, Rule{Hook: "resources/read.post.result", When: "true", Then: map[string]string{"response.result.contents": "response.result.contents.map(c, {'uri': c.uri, 'text': 'REDACTED'})"}})
	o := rvRead(t, s, "skill://demo/sub/x.md", `{"contents":[{"uri":"skill://demo/sub/x.md","text":"hunter2"}]}`)
	if strings.Contains(rvJSON(o.Result), "REDACTED") {
		t.Errorf("dynamic outer skill's file was rewritten: %s", outcomeText(o))
	}
}

func TestReviewCodexFixedDroppedSkillEntryReturns(t *testing.T) {
	s := rvServer(t, Catalog{Skills: []Skill{{URI: "skill://demo/SKILL.md", Resources: json.RawMessage(`"dynamic"`)}}},
		Rule{Hook: "resources/read.post.result", When: "true", Then: map[string]string{"response.result.contents": "response.result.contents.filter(c, !c.text.contains('SECRET'))"}})
	o := rvRead(t, s, "skill://demo/x.md", `{"contents":[{"uri":"skill://demo/x.md","text":"public"},{"uri":"skill://demo/x.md","text":"SECRET"}]}`)
	if strings.Contains(rvJSON(o.Result), "SECRET") {
		t.Errorf("the dropped entry replaced the retained entry: %s", outcomeText(o))
	}
}

func TestReviewCodexCostBudgetSearchAndTasks(t *testing.T) {
	ctx := context.Background()
	// Each expression draws its cost limit from Compile, as used by the gateway.
	rules := []Rule{{Hook: "tools/call.pre", When: "request.params.arguments.x.all(a, request.params.arguments.x.all(b, a == b))", Then: map[string]string{"request.params.arguments.q": "'changed'"}}}
	set, problems := Compile(rules, Options{CostLimit: 10})
	if problems != nil {
		t.Fatal(problems)
	}
	s := rvServer(t, vfAnyTool)
	s.Rules = set
	o := rvEngine().CallTool(ctx, treeRequest(s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"x":[1,1,1,1,1,1,1,1,1,1]}`)})
	if o.Forward != nil || !strings.Contains(rvErrs(o.Hits), "cost limit") {
		t.Errorf("cost limit did not deny: %s %s", outcomeText(o), rvErrs(o.Hits))
	}
	// A failed list rule excludes the tool from all search entry points.
	s = rvServer(t, rvTools(), Rule{Hook: "tools/list.post", When: "tool.title == 'x'", Then: map[string]string{KeyDecision: "'deny'"}})
	r := treeRequest(s)
	r.Flags = map[string]bool{FlagSearch: true}
	e := rvEngine()
	if o := e.ToolSearch(ctx, r, "t", 10); strings.Contains(rvJSON(o.Result), `"name":"t"`) {
		t.Errorf("failed tool surfaced in search: %s", outcomeText(o))
	}
	if o := e.ToolDefinitions(ctx, r, []string{"t"}); strings.Contains(rvJSON(o.Result), `"name":"t"`) {
		t.Errorf("failed tool surfaced in definitions: %s", outcomeText(o))
	}
	if o := e.ExecuteTool(ctx, r, "t", nil, nil); o.Forward != nil {
		t.Error("failed tool executed through search")
	}
	s = rvServer(t, Catalog{Tools: vfAnyTool.Tools, Prompts: []*mcp.Prompt{{Name: "p"}}}, Rule{Hook: "server.settings", When: "true", Then: map[string]string{"settings.tasks": "true"}})
	r = treeRequest(s)
	if sw, _ := e.Config(ctx, r, s); sw["tasks"] {
		t.Error("tasks switch turned on")
	}
	tp := &mcp.CallToolParamsRaw{Name: "t"}
	tc := e.CallTool(ctx, r, tp)
	pp := &mcp.GetPromptParams{Name: "p"}
	pc := e.GetPrompt(ctx, r, pp)
	rp := &mcp.ReadResourceParams{URI: "docs://a"}
	rc := e.ReadResource(ctx, r, rp)
	if tc.Call == nil || pc.Call == nil || rc.Call == nil {
		t.Fatal("task controls did not forward")
	}
	raw := json.RawMessage(`{"resultType":"task","task":{"taskId":"secret-task"}}`)
	for _, o := range []Outcome{e.UpstreamToolReply(ctx, r, tc.Call, tp, raw), e.UpstreamPromptReply(ctx, r, pc.Call, pp, raw), e.UpstreamReadReply(ctx, r, rc.Call, rp, raw)} {
		if strings.Contains(rvJSON(o.Result), "secret-task") {
			t.Errorf("task handle escaped: %s", outcomeText(o))
		}
	}
	// A cancelled request and exhausted enforce allowance cannot list tools.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if o := e.ListTools(cancelled, r); o.Error == nil {
		t.Error("cancelled listing succeeded")
	}
	s = rvServer(t, rvTools(), Rule{Hook: "tools/list.post", When: "true", Then: map[string]string{"tool.description": "'checked'"}})
	if o := (&Engine{Limits: Limits{Budget: time.Nanosecond}}).ListTools(ctx, treeRequest(s)); o.Error == nil {
		t.Error("exhausted listing succeeded")
	}
}

func TestReviewCodexProgressFailsClosed(t *testing.T) {
	for _, family := range []string{"tools/call", "prompts/get", "resources/read"} {
		t.Run(family, func(t *testing.T) {
			s := rvServer(t, Catalog{Tools: vfAnyTool.Tools, Prompts: []*mcp.Prompt{{Name: "p"}}}, Rule{Hook: family + ".post.progress", When: "progress.params.message == 'secret'", Then: map[string]string{KeyDecision: "'deny'"}})
			e, r, ctx := rvEngine(), treeRequest(s), context.Background()
			seq := (&upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progress":1}`), progressEvent(`{"progress":2,"message":"secret"}`), progressEvent(`{"progress":3,"message":"public"}`), resultEvent(`{"content":[],"messages":[],"contents":[]}`)}}).seq()
			var got reply
			switch family {
			case "tools/call":
				p := &mcp.CallToolParamsRaw{Name: "t"}
				o := e.CallTool(ctx, r, p)
				got = collect(e.ToolReplyStream(ctx, r, o.Call, p, seq))
			case "prompts/get":
				p := &mcp.GetPromptParams{Name: "p"}
				o := e.GetPrompt(ctx, r, p)
				got = collect(e.PromptReplyStream(ctx, r, o.Call, p, seq))
			default:
				p := &mcp.ReadResourceParams{URI: "docs://a"}
				o := e.ReadResource(ctx, r, p)
				got = collect(e.ReadReplyStream(ctx, r, o.Call, p, seq))
			}
			if len(got.notes) != 1 || !strings.Contains(string(got.notes[0].Params), "public") || got.final == nil || got.final.Error != nil {
				t.Errorf("progress did not fail closed independently of the result: %+v", got)
			}
		})
	}
}

func TestReviewCodexWritablePathsAndGuards(t *testing.T) {
	for _, expr := range []string{
		"has(tool.title) && tool.title == 'x'", "!has(tool.title) || tool.title == 'x'",
		"has(tool.title) ? tool.title == 'x' : false", "tool.?title.hasValue() && tool.title == 'x'",
		"cel.bind(t, tool, !has(t.title) || t.title == 'x')",
		"[1].exists(tool, tool == 1)",
	} {
		if p, _ := rvSave("tools/list.post", expr, nil); len(p) != 0 {
			t.Errorf("valid guard rejected %s: %v", expr, p)
		}
	}
	for _, expr := range []string{"has(tool.nosuch) && tool.nosuch == 'x'", "!has(tool.nosuch) || tool.nosuch == 'x'", "cel.bind(t, tool, has(t.nosuch))"} {
		if p, _ := rvSave("tools/list.post", expr, nil); len(p) == 0 {
			t.Errorf("guard hid an invalid path: %s", expr)
		}
	}
	for _, key := range []string{`response.result.conten\t`, `response\.result.content`, `response.result.content.`, `response.upstream.result.content`} {
		if p, _ := rvSave("tools/call.post.result", "true", map[string]string{key: "[]"}); len(p) == 0 {
			t.Errorf("invalid set saved: %s", key)
		}
	}
	for _, hook := range []string{"resources/list.post", "resources/templates/list.post"} {
		root := "resource"
		cat := Catalog{Resources: []*mcp.Resource{{URI: "docs://a", Name: "a"}}}
		if strings.Contains(hook, "templates") {
			root = "template"
			cat.Templates = []*mcp.ResourceTemplate{{URITemplate: "docs://h/{id}", Name: "t"}}
		}
		s := rvServer(t, cat, Rule{Hook: hook, When: "true", Then: map[string]string{root + ".annotations": "{'risk': 'high'}"}})
		var o Outcome
		if root == "resource" {
			o = rvEngine().ListResources(context.Background(), treeRequest(s))
		} else {
			o = rvEngine().ListTemplates(context.Background(), treeRequest(s))
		}
		if !strings.Contains(rvErrs(o.Hits), `has no "risk"`) {
			t.Errorf("whole-map fixed keys not checked: %s %s", hook, outcomeText(o))
		}
	}
	s := rvServer(t, vfAnyTool, Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{`request.params.arguments.a\.b`: "'patched'"}})
	o := rvEngine().CallTool(context.Background(), treeRequest(s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"a":{"b":"nested"}}`)})
	if o.Forward == nil || !strings.Contains(rvJSON(o.Forward.Params), `"a.b":"patched"`) || !strings.Contains(rvJSON(o.Forward.Params), `"a":{"b":"nested"}`) {
		t.Errorf("escaped argument key landed elsewhere: %s", rvJSON(o.Forward))
	}
}

// C2, the two cases that are by design. A hidden resource covers the same
// URI with a query or a trailing slash only when the server does not list
// that URI itself. A resource the server lists is judged by the rules on
// its own: here the alias has another name and no rule hides it, so it is
// listed and readable, and the listing and the read agree.
func TestReviewCodexAListedAliasIsJudgedOnItsOwn(t *testing.T) {
	for _, alias := range []string{"docs://h/private?view=raw", "docs://h/private/"} {
		cat := Catalog{Resources: []*mcp.Resource{{URI: "docs://h/private", Name: "secret"}, {URI: alias, Name: "alias"}}}
		s := rvServer(t, cat, Rule{Hook: "resources/list.post", When: "resource.?name.orValue('') == 'secret'", Then: map[string]string{KeyDecision: "'deny'"}})
		e, r := rvEngine(), treeRequest(s)
		listed := rvJSON(e.ListResources(context.Background(), r).Result)
		if strings.Contains(listed, `"docs://h/private"`) || !strings.Contains(listed, alias) {
			t.Errorf("listing: %s", listed)
		}
		if o := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: alias}); o.Forward == nil {
			t.Errorf("%s is listed and was not readable", alias)
		}
		// Hiding the alias is one more condition in the rule.
		s = rvServer(t, cat, Rule{Hook: "resources/list.post", When: "resource.uri.startsWith('docs://h/private')", Then: map[string]string{KeyDecision: "'deny'"}})
		if o := rvEngine().ReadResource(context.Background(), treeRequest(s), &mcp.ReadResourceParams{URI: alias}); o.Forward != nil {
			t.Errorf("%s passed a prefix deny", alias)
		}
	}
}

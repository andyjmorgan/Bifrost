package hooks

// These tests assert the v3 design, not the implementation observed during
// review. Failures are intentional reproductions; do not invert assertions.

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func codexSet(t testing.TB, rules ...Rule) *Set {
	t.Helper()
	s, p := Compile(rules, Options{CostLimit: 1 << 40})
	if p != nil {
		t.Fatal(p)
	}
	return s
}

func codexTool() *mcp.Tool {
	return &mcp.Tool{Name: "t", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}, "tenant": map[string]any{"type": "string"}}}}
}

func codexRequest(t testing.TB, rules ...Rule) (*Engine, Request) {
	t.Helper()
	s := &Server{Slug: "a", Entitlement: map[string]any{"id": "entitlement-a"}, Generation: "1", Rules: codexSet(t, rules...), Catalog: Catalog{
		Tools: []*mcp.Tool{codexTool()}, Prompts: []*mcp.Prompt{{Name: "p"}}, Resources: []*mcp.Resource{{Name: "r", URI: "file:///r"}},
	}}
	e := &Engine{Signer: Signer{Key: []byte("codex-review-secret")}}
	r := Request{Route: "direct", Servers: []*Server{s}, Caller: map[string]any{"userId": "u", "orgId": "org-a"}, Client: map[string]any{"capabilities": map[string]any{"elicitation": map[string]any{}}}, Now: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)}
	return e, r
}

func codexConfirm() Rule {
	return Rule{Hook: "tools/call.pre", When: "!has(input.ok)", Then: map[string]string{"result": "elicit('ok', 'Continue?')"}}
}

func codexAccepted(t testing.TB) mcp.InputResponseMap {
	t.Helper()
	var out mcp.InputResponseMap
	if err := json.Unmarshal([]byte(`{"bifrost.ok":{"action":"accept","content":{"confirm":true}}}`), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCodexEntitlementStateBinding(t *testing.T) { // CX-G01
	e, r := codexRequest(t, codexConfirm())
	first := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t"})
	ir, ok := first.Result.(InputRequired)
	if !ok {
		t.Fatalf("initial confirmation: %+v", first)
	}
	// The same user holds a same-slug entitlement in another organization.
	// Entitlement IDs differ; target and generation deliberately do not.
	s2 := *r.Servers[0]
	s2.Entitlement = map[string]any{"id": "entitlement-b"}
	r.Servers = []*Server{&s2}
	r.Caller = map[string]any{"userId": "u", "orgId": "org-b"}
	out := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", RequestState: ir.RequestState, InputResponses: codexAccepted(t)})
	if out.Forward != nil || out.Error == nil {
		t.Fatalf("state crossed entitlement identities: %+v", out)
	}
}

func TestCodexValidStateRejectsNewGeneration(t *testing.T) { // CX-G02
	e, r := codexRequest(t, codexConfirm())
	first := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t"})
	ir, ok := first.Result.(InputRequired)
	if !ok {
		t.Fatalf("initial confirmation: %+v", first)
	}
	r.Servers[0].Generation = "2"
	out := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", RequestState: ir.RequestState, InputResponses: codexAccepted(t)})
	if out.Forward != nil || out.Error == nil || !strings.Contains(out.Error.Message, "older policy") {
		t.Fatalf("validly signed stale-generation state was not specifically rejected: %+v", out)
	}
}

func TestCodexExecuteToolAPIPreservesContinuation(t *testing.T) { // CX-G03; isolates CX-53's scenario-runner bug
	e, r := codexRequest(t, codexConfirm())
	r.Route = "search"
	first := e.ExecuteTool(context.Background(), r, "a__t", nil, nil)
	ir, ok := first.Result.(InputRequired)
	if !ok {
		t.Fatalf("initial confirmation: %+v", first)
	}
	out := e.ExecuteTool(context.Background(), r, "a__t", nil, &mcp.CallToolParamsRaw{RequestState: ir.RequestState, InputResponses: codexAccepted(t)})
	if out.Forward == nil || out.Error != nil {
		t.Fatalf("ExecuteTool API lost continuation: %+v", out)
	}
}

func TestCodexChangedToolPreservesUntouchedSchemaPointers(t *testing.T) { // CX-G04
	orig := codexTool()
	other := orig.InputSchema.(map[string]any)["properties"].(map[string]any)["tenant"]
	s := codexSet(t, Rule{Hook: "tools/list.post", When: "true", Then: map[string]string{"params.q.description": "'query'"}})
	ls := s.ListTools(context.Background(), []*mcp.Tool{orig}, Context{}, 1)
	if len(ls.Tools) != 1 {
		t.Fatalf("listing failed: %+v", ls)
	}
	got := ls.Tools[0].Tool.InputSchema.(map[string]any)["properties"].(map[string]any)["tenant"]
	if reflect.ValueOf(got).Pointer() != reflect.ValueOf(other).Pointer() {
		t.Fatal("editing q deep-copied untouched tenant schema; design requires the cached sibling pointer")
	}
}

func TestCodexParallelEntitlementsPreserveCatalogAndReversal(t *testing.T) { // CX-G05
	orig := codexTool()
	before, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var servers [2]*Server
	for i, slug := range []string{"a", "b"} {
		servers[i] = &Server{Slug: slug, Rules: codexSet(t,
			Rule{Hook: "tools/list.post", When: "true", Then: map[string]string{"params.q.name": fmt.Sprintf("'%squery'", slug), "params.tenant.pin": fmt.Sprintf("'%s'", slug)}},
		), Catalog: Catalog{Tools: []*mcp.Tool{orig}}}
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan string, 32)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			s := servers[i%2]
			e := &Engine{}
			r := Request{Route: "direct", Servers: []*Server{s}}
			ls := e.ListTools(context.Background(), r)
			if ls.Error != nil {
				errs <- ls.Error.Error()
				return
			}
			got := ls.Result.(*mcp.ListToolsResult).Tools
			if len(got) != 1 {
				errs <- fmt.Sprintf("%s listing: %d", s.Slug, len(got))
				return
			}
			props := got[0].InputSchema.(map[string]any)["properties"].(map[string]any)
			if props[s.Slug+"query"] == nil || props["q"] != nil {
				errs <- fmt.Sprintf("%s names leaked: %v", s.Slug, props)
				return
			}
			out := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(fmt.Sprintf(`{"%squery":"payload"}`, s.Slug))})
			if out.Forward == nil {
				errs <- fmt.Sprintf("%s call: %+v", s.Slug, out)
				return
			}
			args := out.Forward.Params.(*mcp.CallToolParams).Arguments
			if !reflect.DeepEqual(args, map[string]any{"q": "payload", "tenant": s.Slug}) {
				errs <- fmt.Sprintf("%s wrong reversal: %v", s.Slug, args)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	after, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("shared catalog changed")
	}
}

func TestCodexAuditBudgetNeverChangesCallOutcome(t *testing.T) { // CX-G06
	e, r := codexRequest(t)
	e.Limits.Budget = 5 * time.Millisecond
	p := &mcp.CallToolParamsRaw{Name: "t"}
	baseline := e.CallTool(context.Background(), r, p)
	if baseline.Forward == nil {
		t.Fatalf("baseline did not forward: %+v", baseline)
	}
	r.Servers[0].Rules = codexSet(t, Rule{Mode: "audit", Hook: "tools/call.pre", When: "lists.range(1000000).exists(i, i < 0)", Then: map[string]string{"decision": "'deny'"}})
	out := e.CallTool(context.Background(), r, p)
	if out.Forward == nil {
		b, _ := json.Marshal(out)
		t.Fatalf("audit-only work changed successful call into refusal: %s", b)
	}
}

func TestCodexExpiredBudgetStopsOtherSurfaces(t *testing.T) { // CX-G07
	e, r := codexRequest(t,
		Rule{Hook: "prompts/get.pre", When: "true", Then: map[string]string{"args.q": "'x'"}},
		Rule{Hook: "resources/read.pre", When: "false", Then: map[string]string{"decision": "'deny'"}},
		Rule{Hook: "prompts/list.post", When: "true", Then: map[string]string{"description": "'x'"}},
		Rule{Hook: "resources/list.post", When: "true", Then: map[string]string{"description": "'x'"}},
	)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for name, run := range map[string]func() Outcome{
		"prompts/get":    func() Outcome { return e.GetPrompt(ctx, r, &mcp.GetPromptParams{Name: "p"}) },
		"resources/read": func() Outcome { return e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: "file:///r"}) },
		"prompts/list":   func() Outcome { return e.ListPrompts(ctx, r) },
		"resources/list": func() Outcome { return e.ListResources(ctx, r) },
	} {
		t.Run(name, func(t *testing.T) {
			out := run()
			if out.Error == nil {
				t.Fatalf("expired request returned success or forwarded: %+v", out)
			}
		})
	}
}

func TestCodexHelperCostsScaleWithTraversal(t *testing.T) { // CX-G08
	env, err := envFor(Hooks["tools/call.post.result"])
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := compileExpr(env, "drop(result.structuredContent, ['items.*.secret'])", nil, Options{CostLimit: 1 << 40})
	if err != nil {
		t.Fatal(err)
	}
	cost := func(n int) uint64 {
		rows := make([]any, n)
		for i := range rows {
			rows[i] = map[string]any{"secret": "remove", "public": "keep"}
		}
		_, details, err := p.Eval(map[string]any{"result": map[string]any{"structuredContent": map[string]any{"items": rows}}})
		if err != nil {
			t.Fatal(err)
		}
		if details == nil || details.ActualCost() == nil {
			t.Fatal("no cost tracking")
		}
		return *details.ActualCost()
	}
	small, large := cost(1), cost(10000)
	if large <= small {
		t.Fatalf("helper traversed 10000 rows for the same CEL cost: small=%d large=%d", small, large)
	}
}

func TestCodexScenarioNumericAssertionsAreExact(t *testing.T) { // CX-G09
	var ex Expect
	if err := json.Unmarshal([]byte(`{"path":"id","equals":9007199254740992}`), &ex); err != nil {
		t.Fatal(err)
	}
	got := map[string]any{"id": json.Number("9007199254740993")}
	if ex.check(got, nil) == "" {
		t.Fatal("scenario assertion treats 9007199254740993 as equal to 9007199254740992")
	}
}

func TestCodexHiddenRequiredArgumentCannotRelyOnAuditPatch(t *testing.T) { // CX-G10
	e, r := codexRequest(t,
		Rule{Hook: "tools/list.post", When: "true", Then: map[string]string{"params.tenant.hide": "true"}},
		Rule{Mode: "audit", Hook: "tools/call.pre", When: "true", Then: map[string]string{"args.tenant": "'A'"}},
	)
	r.Servers[0].Catalog.Tools[0].InputSchema.(map[string]any)["required"] = []any{"tenant"}
	out := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t"})
	if out.Forward != nil {
		t.Fatalf("audit patch satisfied hidden-required validation without supplying tenant: %+v", out.Forward)
	}
}

func TestCodexFrontmatterParserRejectsInvalidDescriptionAndFence(t *testing.T) { // CX-G11
	for name, text := range map[string]string{
		"missing-description": "---\nname: widget\n---\nBody\n",
		"bad-closing-fence":   "---\nname: widget\ndescription: x\n---not-a-fence\nBody\n",
	} {
		t.Run(name, func(t *testing.T) {
			if fm, err := ParseFrontmatter(text); err == nil {
				t.Fatalf("invalid frontmatter accepted: %#v", fm)
			}
		})
	}
}

func TestCodexResponseLargeIntegerSurvivesRuleEdits(t *testing.T) { // CX-G12
	e, r := codexRequest(t, Rule{Hook: "tools/list.post", When: "true", Then: map[string]string{"description": "'changed'"}})
	// Native JSON Number is supported by JSON serialization and preserves
	// the original upstream schema integer exactly.
	r.Servers[0].Catalog.Tools[0].InputSchema.(map[string]any)["properties"].(map[string]any)["id"] = map[string]any{"const": json.Number("9007199254740993")}
	out := e.ListTools(context.Background(), r)
	b, err := json.Marshal(out.Result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `9007199254740993`) {
		t.Fatalf("unrelated rule rounded upstream schema constraint: %s", b)
	}
}

func TestCodexDecodeArgsRejectsTrailingJSON(t *testing.T) { // CX-G13
	for _, raw := range []string{`{} {"admin":true}`, `{} []`, `{} null`} {
		if got, err := decodeArgs(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted multiple JSON documents %q as %v", raw, got)
		}
	}
}

func TestCodexRuleParseRejectsTrailingJSON(t *testing.T) { // CX-G14
	for _, raw := range []string{`[] [{"hook":"tools/list.post","when":"true","then":{"decision":"'deny'"}}]`, `[] garbage`} {
		if got, err := Parse(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted malformed rule document %q as %v", raw, got)
		}
	}
}

func TestCodexNilRuleSetAllowsDefaultToolListing(t *testing.T) { // CX-G15
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("nil rules (default allow) panicked: %v", p)
		}
	}()
	// Set.run, Set.Has, Set.Rules and callFilled explicitly support nil sets.
	e := &Engine{}
	r := Request{Route: "direct", Servers: []*Server{{Slug: "a", Catalog: Catalog{Tools: []*mcp.Tool{codexTool()}}}}}
	out := e.ListTools(context.Background(), r)
	if out.Error != nil || out.Result == nil || len(out.Result.(*mcp.ListToolsResult).Tools) != 1 {
		t.Fatalf("default listing failed: %+v", out)
	}
}

func TestCodexJSONClientRepresentationsRemainInteroperable(t *testing.T) { // CX-G16
	// Number spelling and object member order differ among JSON clients.
	// Fixes to exact comparisons must preserve equality by value, not bytes.
	for _, text := range []string{"1", "1.0", "1e0", "1E+0", "0.1e1"} {
		t.Run(text, func(t *testing.T) {
			if !jsonEqual(int64(1), json.Number(text)) {
				t.Fatalf("numerically equal JSON values became unequal: 1 and %s", text)
			}
			args, err := decodeArgs(json.RawMessage(" \n{\"value\": " + text + "}\t"))
			if err != nil || args["value"] != json.Number(text) {
				t.Fatalf("valid number spelling rejected or altered: %#v, %v", args, err)
			}
		})
	}
	if !jsonEqual(json.Number("-0"), json.Number("0.0")) {
		t.Fatal("JSON negative zero should compare numerically equal to zero")
	}
	if jsonEqual("1", json.Number("1")) || jsonEqual(true, json.Number("1")) {
		t.Fatal("number equality must not coerce strings or booleans")
	}
}

func TestCodexIntegralJSONSpellingsHaveSamePolicyOutcome(t *testing.T) { // CX-G17
	e, r := codexRequest(t, Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{"args.n": "args.n + 1"}})
	r.Servers[0].Catalog.Tools[0].InputSchema.(map[string]any)["properties"].(map[string]any)["n"] = map[string]any{"type": "integer"}
	for _, number := range []string{"1", "1.0", "1e0", "1E+0", "0.1e1"} {
		t.Run(number, func(t *testing.T) {
			out := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"n":` + number + `}`)})
			if out.Forward == nil {
				b, _ := json.Marshal(out)
				t.Fatalf("valid integral JSON number %s changed policy outcome: %s", number, b)
			}
			args := out.Forward.Params.(*mcp.CallToolParams).Arguments.(map[string]any)
			if !jsonEqual(args["n"], int64(2)) {
				t.Fatalf("wrong patched value for %s: %v", number, args["n"])
			}
		})
	}
}

// Fuzzers are bounded to short inputs; seed runs are part of ordinary tests.
// A separate fuzz run need not execute the intentionally failing regression tests.
func FuzzCodexDecodeArgs(f *testing.F) {
	for _, s := range []string{`{}`, `{"x":1}`, `{"x":1,"x":2}`, `{} {}`, `{"id":9007199254740993}`, `{"a":[{"b":true}]}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 8192 {
			t.Skip()
		}
		_, err := decodeArgs(json.RawMessage(s))
		trim := strings.TrimSpace(s)
		if err == nil && trim != "" && !json.Valid([]byte(s)) {
			t.Fatalf("accepted invalid JSON: %q", s)
		}
	})
}

func FuzzCodexParse(f *testing.F) {
	for _, s := range []string{`[]`, `null`, `[] garbage`, `[{"hook":"tools/list.post","when":"true","then":{"decision":"'deny'"}}]`} {
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

func FuzzCodexURI(f *testing.F) {
	for _, s := range []string{"file:///a", "git+ssh://x", "skill://a/../private/SKILL.md", "file:///a%2Fb", "file:///a?q=/../x", "urn:example:a"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, u string) {
		if len(u) > 4096 {
			t.Skip()
		}
		_ = nonCanonical(u)
		_ = normalizeURI(u)
		if strings.IndexByte(u, ':') <= 0 {
			return
		}
		slug, rest, ok := SplitURI(PrefixURI("docs", u))
		if !ok || slug != "docs" || rest != u {
			t.Fatalf("prefix round trip lost identity: %q => %q %q %v", u, slug, rest, ok)
		}
	})
}

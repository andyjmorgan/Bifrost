package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestScenarios runs every scenario file. Each scenario names the test case
// IDs it proves (docs/rules/test-cases.md).
func TestScenarios(t *testing.T) {
	files, _ := filepath.Glob("testdata/scenarios/*.json")
	if len(files) == 0 {
		t.Fatal("no scenario files")
	}
	for _, f := range files {
		ss, err := LoadScenarios(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, s := range ss {
			s := s
			t.Run(filepath.Base(f)+"/"+s.Name, func(t *testing.T) {
				rep := s.Run(context.Background())
				switch rep.Status {
				case "skip":
					t.Skip(rep.Reason)
				case "fail":
					b, _ := json.MarshalIndent(rep, "", "  ")
					t.Fatalf("%s\n%s", strings.Join(rep.Failures, "\n"), b)
				}
			})
		}
	}
}

// ---- copy-on-write (RH-COW-01..05, ADV-26, ADV-27, ADV-45) ---------------------------

func TestViewUnchangedSharesUpstream(t *testing.T) { // RH-COW-01
	base := map[string]any{"name": "t", "inputSchema": map[string]any{"type": "object"}}
	v := NewView(base)
	if v.Changed() {
		t.Fatal("new view reports a change")
	}
	m := v.Materialize()
	if reflect.ValueOf(m).Pointer() != reflect.ValueOf(base).Pointer() {
		t.Fatal("an unchanged view must return the upstream map itself")
	}
}

func TestViewCopiesOnlyThePath(t *testing.T) { // RH-COW-02, RH-COW-04
	cql := map[string]any{"type": "string"}
	other := map[string]any{"type": "integer"}
	props := map[string]any{"cql": cql, "limit": other}
	schema := map[string]any{"type": "object", "properties": props}
	base := map[string]any{"name": "t", "inputSchema": schema, "title": "T"}
	v := NewView(base)
	v.Set([]string{"inputSchema", "properties", "cql", "description"}, "query text")
	got, _ := v.Get("inputSchema")
	gs := got.(map[string]any)
	gp := gs["properties"].(map[string]any)
	if gp["cql"].(map[string]any)["description"] != "query text" {
		t.Fatal("change not visible")
	}
	if reflect.ValueOf(gp["limit"]).Pointer() != reflect.ValueOf(other).Pointer() {
		t.Fatal("an untouched sibling must still point at the upstream value")
	}
	if _, ok := cql["description"]; ok {
		t.Fatal("the upstream object was modified")
	}
	m := v.Materialize()
	if m["title"] != "T" {
		t.Fatal("untouched top-level field lost")
	}
}

func TestViewMaterializeEqualsUpstreamPlusOverlay(t *testing.T) { // RH-COW-03
	base := map[string]any{"a": map[string]any{"b": 1, "c": 2}, "d": "x"}
	v := NewView(base)
	v.Set([]string{"a", "b"}, 10)
	v.Delete([]string{"d"})
	v.Set([]string{"e"}, true)
	want := map[string]any{"a": map[string]any{"b": 10, "c": 2}, "e": true}
	if !reflect.DeepEqual(v.Materialize(), want) {
		t.Fatalf("got %v want %v", v.Materialize(), want)
	}
}

func TestViewWholeObjectReplacementDropsStaleChildren(t *testing.T) { // ADV-27
	v := NewView(map[string]any{"s": map[string]any{"x": 1}})
	v.Set([]string{"s", "x"}, 2)
	v.Set([]string{"s"}, map[string]any{"y": 3})
	got, _ := v.Get("s")
	if !reflect.DeepEqual(got, map[string]any{"y": 3}) {
		t.Fatalf("stale child change survived a replacement: %v", got)
	}
	v.Set([]string{"s", "z"}, 4) // a later child change applies on top
	got, _ = v.Get("s")
	if !reflect.DeepEqual(got, map[string]any{"y": 3, "z": 4}) {
		t.Fatalf("got %v", got)
	}
}

func TestParentReadSeesChildOverlay(t *testing.T) { // ADV-26
	ts := newToolState(prepare(&mcp.Tool{Name: "t", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}}))
	if err := ts.set("params.q.description", "the query"); err != nil {
		t.Fatal(err)
	}
	s, _ := ts.resolve("inputSchema")
	if s.(map[string]any)["properties"].(map[string]any)["q"].(map[string]any)["description"] != "the query" {
		t.Fatal("reading the parent schema ignored a parameter change")
	}
	p, _ := ts.resolve("params")
	if p.(map[string]any)["q"].(map[string]any)["description"] != "the query" {
		t.Fatal("params ignored the change")
	}
}

func TestOverlaysPerItemCount(t *testing.T) { // RH-COW-05
	var cat []*mcp.Tool
	for i := 0; i < 1000; i++ {
		cat = append(cat, &mcp.Tool{Name: fmt.Sprintf("tool_%04d", i), InputSchema: map[string]any{"type": "object"}})
	}
	set, probs := Compile([]Rule{{Hook: "tools/list.post", When: "int(name.substring(5)) % 50 == 0", Then: map[string]string{"description": "'changed'"}}})
	if probs != nil {
		t.Fatal(probs)
	}
	tl := set.ListTools(context.Background(), cat, Context{}, 0)
	changed, shared := 0, 0
	for i, et := range tl.Tools {
		if et.Changed {
			changed++
		} else if et.Tool == cat[i] {
			shared++
		}
	}
	if changed != 20 || shared != 980 {
		t.Fatalf("changed=%d shared=%d, want 20 and 980", changed, shared)
	}
}

func TestDropSchemaLeavesSharedDefinitionsIntact(t *testing.T) { // ADV-45
	shared := map[string]any{"type": "object", "properties": map[string]any{"self": map[string]any{"type": "string"}, "key": map[string]any{"type": "string"}}, "required": []any{"self", "key"}}
	schema := map[string]any{"type": "object", "properties": map[string]any{"a": shared, "b": shared}}
	out, _ := dropSchemaPath(schema, splitPath("a.self"))
	if _, ok := shared["properties"].(map[string]any)["self"]; !ok {
		t.Fatal("dropSchema modified a shared definition")
	}
	a := out.(map[string]any)["properties"].(map[string]any)["a"].(map[string]any)
	if _, ok := a["properties"].(map[string]any)["self"]; ok {
		t.Fatal("dropSchema did not remove the property")
	}
	if !reflect.DeepEqual(a["required"], []any{"key"}) {
		t.Fatalf("required not updated: %v", a["required"])
	}
	b := out.(map[string]any)["properties"].(map[string]any)["b"].(map[string]any)
	if _, ok := b["properties"].(map[string]any)["self"]; !ok {
		t.Fatal("dropSchema removed from the wrong branch")
	}
}

func TestDropPathIsImmutable(t *testing.T) { // RH-RES-01
	in := map[string]any{"issues": []any{map[string]any{"self": "u", "key": "A"}}}
	out := dropPath(in, splitPath("issues.*.self"))
	if _, ok := in["issues"].([]any)[0].(map[string]any)["self"]; !ok {
		t.Fatal("drop modified its input")
	}
	if !reflect.DeepEqual(out, map[string]any{"issues": []any{map[string]any{"key": "A"}}}) {
		t.Fatalf("got %v", out)
	}
}

// ---- state (RH-ELI-07, RH-ELI-08, RH-ELI-12) -----------------------------------------

func TestStateTamperingIsRejected(t *testing.T) { // RH-ELI-12
	sg := Signer{Key: []byte("k")}
	tok := sg.Seal(State{Challenge: "c", Caller: "u1", Expires: 99})
	if _, err := sg.Open(tok); err != nil {
		t.Fatal(err)
	}
	payload, sig, _ := strings.Cut(tok, ".")
	forged := Signer{Key: []byte("k")}.Seal(State{Challenge: "c", Caller: "mallory", Expires: 99})
	fp, _, _ := strings.Cut(forged, ".")
	for _, bad := range []string{fp + "." + sig, payload + ".AAAA", payload, "", tok + "x"} {
		if _, err := sg.Open(bad); err == nil {
			t.Fatalf("accepted tampered state %q", bad)
		}
	}
	if _, err := (Signer{Key: []byte("other")}).Open(tok); err == nil {
		t.Fatal("accepted state signed with another key")
	}
}

func TestArgsHashIsCanonical(t *testing.T) {
	a := map[string]any{"b": []any{1.0, map[string]any{"y": 1.0, "x": 2.0}}, "a": "s"}
	b := map[string]any{"a": "s", "b": []any{1.0, map[string]any{"x": 2.0, "y": 1.0}}}
	if argsHash(a) != argsHash(b) {
		t.Fatal("key order changed the hash")
	}
	c := map[string]any{"a": "s", "b": []any{map[string]any{"x": 2.0, "y": 1.0}, 1.0}}
	if argsHash(a) == argsHash(c) {
		t.Fatal("array order must change the hash")
	}
}

// ---- limits and classification (RH-LIM-03, RH-PRF-01) --------------------------------

func TestTimeBudgetDenies(t *testing.T) { // RH-LIM-03
	set, probs := Compile([]Rule{{Hook: "tools/list.post", When: "lists.range(200000).exists(i, i < 0)", Then: map[string]string{"decision": "'deny'"}}}, Options{CostLimit: 1 << 40})
	if probs != nil {
		t.Fatal(probs)
	}
	e := &Engine{Signer: Signer{Key: []byte("k")}, Limits: Limits{Budget: time.Millisecond}}
	s := &Server{Slug: "a", Rules: set, Catalog: Catalog{Tools: []*mcp.Tool{{Name: "t", InputSchema: map[string]any{"type": "object"}}}}}
	out := e.ListTools(context.Background(), Request{Route: "direct", Servers: []*Server{s}})
	if out.Error == nil {
		res := out.Result.(*mcp.ListToolsResult)
		if len(res.Tools) != 0 {
			t.Fatal("a rule that ran out of time must not let the tool through")
		}
	}
}

func TestStaticDynamicClassification(t *testing.T) { // RH-PRF-01
	set, probs := Compile([]Rule{
		{Name: "static", Hook: "tools/list.post", When: "name == 'x' && upstream.description != '' && server.slug == 'a'", Then: map[string]string{"decision": "'deny'"}},
		{Name: "by-caller", Hook: "tools/list.post", When: "caller.role != 'admin'", Then: map[string]string{"decision": "'deny'"}},
		{Name: "by-client", Hook: "tools/list.post", When: "true", Then: map[string]string{"description": "has(client.capabilities.elicitation) ? 'a' : 'b'"}},
		{Name: "by-time", Hook: "tools/call.pre", When: "now > timestamp('2026-01-01T00:00:00Z')", Then: map[string]string{"decision": "'deny'"}},
	})
	if probs != nil {
		t.Fatal(probs)
	}
	d := set.Dynamic()
	want := map[string]bool{"static": false, "by-caller": true, "by-client": true, "by-time": true}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("got %v want %v", d, want)
	}
}

func TestSlugGrammar(t *testing.T) { // RH-MUX-16
	for s, ok := range map[string]bool{"docs": true, "a1": true, "1docs": false, "do-cs": false, "do_cs": false, "do+cs": false, "do.cs": false, "": false, "Docs": false} {
		if ValidSlug(s) != ok {
			t.Errorf("ValidSlug(%q) = %v", s, !ok)
		}
	}
	slug, rest, ok := SplitURI("docs+git+ssh://x")
	if !ok || slug != "docs" || rest != "git+ssh://x" {
		t.Fatalf("got %q %q %v", slug, rest, ok)
	}
	if _, _, ok := SplitURI("git+ssh://x/y"); !ok {
		t.Fatal("a valid slug-looking prefix splits; resolution then refuses unheld slugs")
	}
	if _, _, ok := SplitURI("file:///x"); ok {
		t.Fatal("no prefix must not split")
	}
}

// ---- real corpus (RH-PRF-02, RH-PRF-03, ADV-28) ---------------------------------------

func realTools(t testing.TB) []*mcp.Tool {
	t.Helper()
	tools, err := loadToolRef("../../internal/e2e/testdata/real", "corpus.json#tools", 0)
	if err != nil {
		t.Skip("real corpus not present: run scripts/fixtures")
	}
	return tools
}

// realisticRules is a 20-rule policy of the kind an administrator writes.
func realisticRules() []Rule {
	return []Rule{
		{Hook: "tools/list.post", When: "name.matches('^(delete|drop|purge|destroy)_')", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/list.post", When: "caller.role != 'admin' && name.startsWith('admin')", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/list.post", When: "has(annotationsRaw.destructiveHint) && annotationsRaw.destructiveHint == true && caller.role != 'admin'", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/list.post", When: "description.contains('DEPRECATED')", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/list.post", When: "size(name) > 64", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/list.post", When: "size(description) > 1000", Then: map[string]string{"description": "description.substring(0, 1000)"}},
		{Hook: "tools/list.post", When: "name.startsWith('datadog_')", Then: map[string]string{"name": "regex.replace(name, '^datadog_', 'dd_')"}},
		{Hook: "tools/list.post", When: "true", Then: map[string]string{"description": "regex.replace(description, '(?i)confluence', 'the wiki')"}},
		{Hook: "tools/list.post", When: "'limit' in params", Then: map[string]string{"params.limit.maximum": "100"}},
		{Hook: "tools/list.post", When: "'query' in params", Then: map[string]string{"params.query.description": "'Search text'"}},
		{Hook: "tools/list.post", When: "!has(annotationsRaw.readOnlyHint) && name.startsWith('get_')", Then: map[string]string{"annotations.readOnlyHint": "true"}},
		{Hook: "tools/list.post", When: "name.contains('search')", Then: map[string]string{"title": "'Search: ' + name"}},
		{Hook: "tools/list.post", When: "'org' in params", Then: map[string]string{"params.org.default": "'acme'"}},
		{Hook: "tools/list.post", When: "true", Then: map[string]string{"meta.ai\\.bifrost/reviewed": "true"}},
		{Hook: "tools/list.post", When: "name.endsWith('_v2')", Then: map[string]string{"name": "name.substring(0, size(name) - 3)"}},
		{Hook: "tools/call.pre", When: "has(args.limit) && args.limit > 100", Then: map[string]string{"args.limit": "100"}},
		{Hook: "tools/call.pre", When: "has(args.env) && args.env == 'prod' && caller.role != 'admin'", Then: map[string]string{"decision": "'deny'", "reason": "'Production is blocked.'"}},
		{Hook: "tools/call.pre", When: "has(args.from) && has(args.to) && timestamp(args.to) - timestamp(args.from) > duration('168h')", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/call.post.result", When: "has(result.structuredContent)", Then: map[string]string{"content": "[]"}},
		{Mode: "audit", Hook: "tools/list.post", When: "name.contains('write')", Then: map[string]string{"decision": "'deny'"}},
	}
}

func TestCorpusIdentityWithoutRules(t *testing.T) { // RH-PRF-03
	tools := realTools(t)
	set, _ := Compile(nil)
	tl := set.ListTools(context.Background(), tools, Context{}, 0)
	if len(tl.Tools) != len(tools) || len(tl.Issues) != 0 {
		t.Fatalf("listed %d of %d tools; issues %v", len(tl.Tools), len(tools), tl.Issues)
	}
	byName := map[string]*mcp.Tool{}
	for _, tt := range tools {
		byName[tt.Name] = tt
	}
	for _, et := range tl.Tools {
		if et.Tool != byName[et.Tool.Name] {
			t.Fatalf("%s: with no rules the exposed tool must be the cached upstream object", et.Tool.Name)
		}
	}
}

func TestCorpusRealisticPolicyParallelAndFast(t *testing.T) { // RH-PRF-02, ADV-28
	tools := realTools(t)
	set, probs := Compile(realisticRules())
	if probs != nil {
		t.Fatal(probs)
	}
	ctx := Context{Caller: map[string]any{"role": "member"}, Server: map[string]any{"slug": "dd"}}
	// Several entitlements listing the same shared catalog concurrently:
	// overlays must not leak between them (ADV-28).
	type res struct {
		tl ToolList
		d  time.Duration
	}
	out := make([]res, 4)
	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		go func(i int) {
			c := ctx
			if i%2 == 1 {
				c.Caller = map[string]any{"role": "admin"}
			}
			t0 := time.Now()
			out[i] = res{tl: set.ListTools(context.Background(), tools, c, 0), d: time.Since(t0)}
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	if !reflect.DeepEqual(names(out[0].tl), names(out[2].tl)) || !reflect.DeepEqual(names(out[1].tl), names(out[3].tl)) {
		t.Fatal("concurrent listings with the same context disagree")
	}
	for _, tt := range tools {
		if m, ok := tt.Meta["ai.bifrost/reviewed"]; ok {
			t.Fatalf("a rule's change leaked into the shared upstream catalog: %s %v", tt.Name, m)
		}
	}
	for i, r := range out {
		t.Logf("listing %d: %d tools exposed, %d hidden, %d issues, %s", i, len(r.tl.Tools), len(r.tl.Hidden), len(r.tl.Issues), r.d)
		if r.d > 2*time.Second {
			t.Fatalf("listing %d took %s, over the 2s budget", i, r.d)
		}
	}
}

func names(tl ToolList) []string {
	var out []string
	for _, et := range tl.Tools {
		out = append(out, et.Tool.Name)
	}
	return out
}

// TestRealCallExamplesRoundTrip sends every real tool-call example through
// CallTool with no rules: the forwarded arguments must equal the input
// exactly (big numbers, unicode, nesting and all).
func TestRealCallExamplesRoundTrip(t *testing.T) {
	b, err := os.ReadFile("../../internal/e2e/testdata/real/toolcalls.json")
	if err != nil {
		t.Skip("real call examples not present")
	}
	var f struct {
		Calls []struct {
			Tool      string          `json:"tool"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"calls"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) < 500 {
		t.Fatalf("want at least 500 call examples, have %d", len(f.Calls))
	}
	seen := map[string]bool{}
	var cat []*mcp.Tool
	for _, c := range f.Calls {
		if !seen[c.Tool] {
			seen[c.Tool] = true
			cat = append(cat, &mcp.Tool{Name: c.Tool, InputSchema: map[string]any{"type": "object"}})
		}
	}
	set, _ := Compile(nil)
	e := &Engine{Signer: Signer{Key: []byte("k")}}
	s := &Server{Slug: "h", Rules: set, Catalog: Catalog{Tools: cat}}
	r := Request{Route: "direct", Servers: []*Server{s}, Caller: map[string]any{"userId": "u"}}
	for i, c := range f.Calls {
		out := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: c.Tool, Arguments: c.Arguments})
		if out.Forward == nil {
			t.Fatalf("call %d (%s): not forwarded: %+v", i, c.Tool, out.Error)
		}
		got, _ := json.Marshal(out.Forward.Params.(*mcp.CallToolParams).Arguments)
		if canonJSON(t, got) != canonJSON(t, c.Arguments) {
			t.Fatalf("call %d (%s): arguments changed\n got %s\nwant %s", i, c.Tool, got, c.Arguments)
		}
	}
	t.Logf("%d real calls across %d tools round-tripped exactly", len(f.Calls), len(cat))
}

func canonJSON(t *testing.T, b []byte) string {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return canonical(v)
}

// TestRealCallExamplesUnderPolicy runs every real call example through the
// realistic policy with each tool's real definition where the corpus has it.
// Nothing may panic, and every outcome is one of: forwarded, a deny result,
// or an invalid-arguments error.
func TestRealCallExamplesUnderPolicy(t *testing.T) {
	b, err := os.ReadFile("../../internal/e2e/testdata/real/toolcalls.json")
	if err != nil {
		t.Skip("real call examples not present")
	}
	var f struct {
		Calls []struct {
			Tool      string          `json:"tool"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"calls"`
	}
	_ = json.Unmarshal(b, &f)
	defs := map[string]*mcp.Tool{}
	if tools, err := loadToolRef("../../internal/e2e/testdata/real", "corpus.json#tools", 0); err == nil {
		for _, tt := range tools {
			defs[tt.Name] = tt
		}
	}
	seen := map[string]bool{}
	var cat []*mcp.Tool
	for _, c := range f.Calls {
		if seen[c.Tool] {
			continue
		}
		seen[c.Tool] = true
		if d, ok := defs[c.Tool]; ok {
			cat = append(cat, d)
		} else {
			cat = append(cat, &mcp.Tool{Name: c.Tool, InputSchema: map[string]any{"type": "object"}})
		}
	}
	set, probs := Compile(realisticRules())
	if probs != nil {
		t.Fatal(probs)
	}
	e := &Engine{Signer: Signer{Key: []byte("k")}}
	s := &Server{Slug: "h", Rules: set, Catalog: Catalog{Tools: cat}}
	r := Request{Route: "direct", Servers: []*Server{s}, Caller: map[string]any{"userId": "u", "role": "member"}}
	counts := map[string]int{}
	for _, c := range f.Calls {
		out := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: c.Tool, Arguments: c.Arguments})
		switch {
		case out.Forward != nil:
			counts["forwarded"]++
		case out.Error != nil:
			counts[fmt.Sprintf("error %d", out.Error.Code)]++
		case out.Result != nil:
			counts["refused"]++
		default:
			t.Fatalf("%s: empty outcome", c.Tool)
		}
	}
	t.Logf("outcomes over %d real calls: %v", len(f.Calls), counts)
}

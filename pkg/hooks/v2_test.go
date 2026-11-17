package hooks

import (
	"context"
	"encoding/json"
	"math/rand"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestListingIsByteConsistent lists the real corpus twice, once with the
// catalog shuffled, and requires identical bytes (F35: caches stay
// byte-consistent whatever order the upstream returns).
func TestListingIsByteConsistent(t *testing.T) {
	tools := realTools(t)
	set, probs := Compile(realisticRules())
	if probs != nil {
		t.Fatal(probs)
	}
	// This tests ordering, not the budget; under the race detector the
	// summed rule time of a parallel listing exceeds the 2 s default.
	e := &Engine{Limits: Limits{Budget: time.Minute}}
	list := func(cat []*mcp.Tool) []byte {
		s := &Server{Slug: "dd", Rules: set, Catalog: Catalog{Tools: cat}}
		out := e.ListTools(context.Background(), Request{Route: "mux", Servers: []*Server{s}, Caller: map[string]any{"role": "member"}})
		if out.Error != nil || out.Result == nil {
			t.Fatalf("listing failed: %+v", out.Error)
		}
		if n := len(out.Result.(*mcp.ListToolsResult).Tools); n < 1000 {
			t.Fatalf("only %d tools listed; expected the real corpus", n)
		}
		b, err := json.Marshal(out.Result)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	a := list(tools)
	shuffled := append([]*mcp.Tool(nil), tools...)
	rand.New(rand.NewSource(7)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	b := list(shuffled)
	if string(a) != string(b) {
		t.Fatalf("listing bytes differ with catalog order (%d vs %d bytes)", len(a), len(b))
	}
}

// TestRealCallsPolicyOracle checks exact outcomes for the real call
// examples where the realistic policy's effect is predictable: a limit over
// 100 is clamped to 100, env "prod" is refused for members.
func TestPolicyCallOracle(t *testing.T) {
	set, probs := Compile(realisticRules())
	if probs != nil {
		t.Fatal(probs)
	}
	e := &Engine{}
	cat := []*mcp.Tool{{Name: "search", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"limit": map[string]any{"type": "integer"}, "env": map[string]any{"type": "string"}, "q": map[string]any{"type": "string"}}}}}
	s := &Server{Slug: "h", Rules: set, Catalog: Catalog{Tools: cat}}
	r := Request{Route: "direct", Servers: []*Server{s}, Caller: map[string]any{"userId": "u", "role": "member"}}
	cases := []struct {
		args string
		want string // "forward:<limit>" or "refused"
	}{
		{`{"q":"a","limit":250}`, "error"}, // over the exposed maximum: refused before call rules run
		{`{"q":"a","limit":20}`, "error"},  // over the modify maximum? no: 20 is fine
		{`{"q":"a","env":"prod"}`, "refused"},
		{`{"q":"a","env":"staging","limit":100}`, "forward:100"},
	}
	cases[1].want = "forward:20"
	for _, c := range cases {
		out := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "search", Arguments: json.RawMessage(c.args)})
		got := "error"
		switch {
		case out.Forward != nil:
			args := out.Forward.Params.(*mcp.CallToolParams).Arguments.(map[string]any)
			got = "forward"
			if l, ok := args["limit"]; ok {
				b, _ := json.Marshal(l)
				got += ":" + string(b)
			}
		case out.Result != nil:
			got = "refused"
		}
		if got != c.want {
			t.Errorf("%s: got %s want %s (%+v)", c.args, got, c.want, out.Error)
		}
	}
}

func FuzzScenarioLoader(f *testing.F) {
	for _, s := range []string{`[]`, `{"name":"x","servers":[],"steps":[]}`, `{"name":"x"}{"name":"y"}`, `[{"servers":[{"slug":"a","rules":[{"hook":"tools/list.post","when":"true","then":{"decision":"'deny'"}}]}],"steps":[{"op":"tools/list"}]}]`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 4096 {
			t.Skip()
		}
		ss, err := ParseScenarios([]byte(s))
		if err != nil {
			return
		}
		for _, sc := range ss {
			if sc == nil || len(sc.Steps) > 8 || len(sc.Servers) > 4 {
				continue
			}
			sc.Run(context.Background()) // must not panic
		}
	})
}

// TestExpectRequiresPresentValues: missing, null, empty and wrong-type
// values are not interchangeable in len and notContains (V2-F05).
func TestExpectRequiresPresentValues(t *testing.T) {
	zero, one := 0, 1
	out := map[string]any{"empty": []any{}, "null": nil, "num": 3, "list": []any{"a"}}
	cases := []struct {
		ex   Expect
		pass bool
	}{
		{Expect{Path: "empty", Len: &zero}, true},
		{Expect{Path: "missing", Len: &zero}, false},
		{Expect{Path: "null", Len: &zero}, false},
		{Expect{Path: "num", Len: &zero}, false},
		{Expect{Path: "list", Len: &one}, true},
		{Expect{Path: "list", NotContains: "secret"}, true},
		{Expect{Path: "missing", NotContains: "secret"}, false},
		{Expect{Path: "missing[*].name", NotContains: "secret"}, false},
	}
	for _, c := range cases {
		if got := c.ex.check(out, nil) == ""; got != c.pass {
			t.Errorf("%s len=%v notContains=%v: pass=%v, want %v", c.ex.Path, c.ex.Len, c.ex.NotContains, got, c.pass)
		}
	}
}

// TestCatalogIsNeverModified runs listings, calls and an
// elicitation-free policy that renames, hides, pins, rewrites text and
// changes annotations over the real corpus plus a hand-built tool, then
// requires the catalog to be byte-identical to before and untouched schema
// maps to keep their identity (RH-COW-06).
func TestCatalogIsNeverModified(t *testing.T) {
	tools := realTools(t)
	extra := &mcp.Tool{Name: "confluence_search", Description: "Search Confluence", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"cql": map[string]any{"type": "string", "description": "query"}, "space": map[string]any{"type": "string"}, "debug": map[string]any{"type": "boolean"},
			"limit": map[string]any{"type": "integer"}}, "required": []any{"cql"}}}
	cat := append(append([]*mcp.Tool(nil), tools...), extra)
	rules := append(realisticRules(),
		Rule{Hook: "tools/list.post", When: "tool.name == 'confluence_search'", Then: map[string]string{
			"tool.name": "'wiki_search'", "tool.description": "tool.description + ' (wiki)'", "tool.annotations.readOnlyHint": "false",
			"tool.params.cql.name": "'query'", "tool.params.cql.description": "'CQL query'", "tool.params.debug.hide": "true",
			"tool.params.space.pin": "'ENG'", "tool.params.limit.maximum": "50", "tool.params.why.add": "{'type': 'string'}"}},
		Rule{Hook: "tools/call.pre", When: "tool.name == 'wiki_search'", Then: map[string]string{"request.params.arguments.query": "request.params.arguments.query + ' AND type=page'"}})
	set, probs := Compile(rules)
	if probs != nil {
		t.Fatal(probs)
	}
	before, _ := json.Marshal(cat)
	extraProps := extra.InputSchema.(map[string]any)["properties"].(map[string]any)
	e := &Engine{Limits: Limits{Budget: time.Minute}}
	s := &Server{Slug: "a", Rules: set, Catalog: Catalog{Tools: cat}}
	r := Request{Route: "mux", Servers: []*Server{s}, Caller: map[string]any{"userId": "u", "role": "member"}}
	for i := 0; i < 3; i++ {
		if out := e.ListTools(context.Background(), r); out.Error != nil {
			t.Fatal(out.Error)
		}
		out := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "a__wiki_search", Arguments: json.RawMessage(`{"query":"x","why":"y","limit":5}`)})
		if out.Forward == nil {
			t.Fatalf("call not forwarded: %+v", out.Error)
		}
		got := out.Forward.Params.(*mcp.CallToolParams)
		if got.Name != "confluence_search" {
			t.Fatalf("forwarded %q", got.Name)
		}
		if b, _ := json.Marshal(got.Arguments); string(b) != `{"cql":"x AND type=page","limit":5,"space":"ENG"}` {
			t.Fatalf("forwarded arguments %s", b)
		}
	}
	after, _ := json.Marshal(cat)
	if string(before) != string(after) {
		t.Fatal("the catalog was modified by listing or calling")
	}
	if extra.InputSchema.(map[string]any)["properties"].(map[string]any)["cql"].(map[string]any)["description"] != "query" {
		t.Fatal("upstream parameter description changed")
	}
	if len(extraProps) != 4 {
		t.Fatalf("upstream properties map changed: %v", extraProps)
	}
}

// TestRegexReplaceOnLargeNotebook rewrites "datadog" to "Bits" across a 1 MB
// create_datadog_notebook call (16 markdown cells, 100 occurrences) in a
// tools/call rule, and reports time, allocation and outcome.
func TestRegexReplaceOnLargeNotebook(t *testing.T) {
	tool := &mcp.Tool{Name: "create_datadog_notebook", InputSchema: map[string]any{"type": "object"}}
	if all, err := loadToolRef("../../internal/e2e/testdata/real", "datadog.json", 0); err == nil {
		for _, x := range all {
			if x.Name == "create_datadog_notebook" {
				tool = x
			}
		}
	}
	const cells, total, hits = 16, 1 << 20, 100
	filler := strings.Repeat("Latency is fine across the fleet. ", total/cells/34)
	var cs []any
	n := 0
	for i := 0; i < cells; i++ {
		var b strings.Builder
		per := hits / cells
		if i < hits%cells {
			per++
		}
		chunk := len(filler) / (per + 1)
		for j := 0; j <= per; j++ {
			b.WriteString(filler[j*chunk : (j+1)*chunk])
			if j < per {
				b.WriteString(" Datadog ")
				n++
			}
		}
		cs = append(cs, map[string]any{"type": "notebook_cells", "attributes": map[string]any{"definition": map[string]any{"type": "markdown", "text": b.String()}}})
	}
	args, _ := json.Marshal(map[string]any{"name": "Incident review", "cells": cs})
	if n != hits {
		t.Fatalf("built %d occurrences", n)
	}
	rules := []Rule{
		{Hook: "tools/list.post", When: "'telemetry' in tool.params", Then: map[string]string{"tool.params.telemetry.hide": "true", "tool.params.telemetry.pin": "{'intent': 'mind your own business'}"}},
		{Hook: "tools/call.pre", When: "tool.name == 'create_datadog_notebook'", Then: map[string]string{
			"request.params.arguments.cells": "request.params.arguments.cells.map(c, {'type': c.type, 'attributes': {'definition': {'type': 'markdown', 'text': regex.replace(c.attributes.definition.text, '(?i)datadog', 'Bits')}}})"}},
	}
	// The default CEL cost limit (1,000,000) refuses this call: regex.replace
	// is charged by input size. A 1 MB rewrite needs a higher limit.
	if def, _ := Compile(rules); def != nil {
		e := &Engine{}
		s := &Server{Slug: "dd", Rules: def, Catalog: Catalog{Tools: []*mcp.Tool{tool}}}
		out := e.CallTool(context.Background(), Request{Route: "direct", Servers: []*Server{s}, Caller: map[string]any{"userId": "u"}}, &mcp.CallToolParamsRaw{Name: "create_datadog_notebook", Arguments: args})
		if out.Forward != nil {
			t.Fatal("default cost limit forwarded a 1 MB rewrite; expected refusal")
		}
	}
	set, probs := Compile(rules, Options{CostLimit: 50_000_000})
	if probs != nil {
		t.Fatal(probs)
	}
	e := &Engine{}
	s := &Server{Slug: "dd", Rules: set, Catalog: Catalog{Tools: []*mcp.Tool{tool}}}
	r := Request{Route: "direct", Servers: []*Server{s}, Caller: map[string]any{"userId": "u"}}
	var ms0, ms1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms0)
	t0 := time.Now()
	out := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "create_datadog_notebook", Arguments: args})
	d := time.Since(t0)
	runtime.ReadMemStats(&ms1)
	t.Logf("args %d bytes; call took %v, allocated %.1f MiB in %d allocations", len(args), d, float64(ms1.TotalAlloc-ms0.TotalAlloc)/(1<<20), ms1.Mallocs-ms0.Mallocs)
	if out.Forward == nil {
		t.Fatalf("not forwarded: error=%+v result=%+v hits=%+v", out.Error, out.Result, out.Hits)
	}
	fwd, _ := json.Marshal(out.Forward.Params.(*mcp.CallToolParams).Arguments)
	if c := strings.Count(strings.ToLower(string(fwd)), "datadog"); c != 0 {
		t.Fatalf("%d occurrences of datadog left", c)
	}
	if c := strings.Count(string(fwd), "Bits"); c != hits {
		t.Fatalf("%d replacements, want %d", c, hits)
	}
	if !strings.Contains(string(fwd), `"telemetry":{"intent":"mind your own business"}`) {
		t.Fatal("telemetry pin missing")
	}
}

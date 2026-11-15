package hooks

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestProfileCorpus lists the real corpus once with the realistic policy
// and prints every rule's cost, the phase breakdown and heap allocations.
func TestProfileCorpus(t *testing.T) {
	tools := realTools(t)
	set, probs := Compile(realisticRules())
	if probs != nil {
		t.Fatal(probs)
	}
	c := Context{Caller: map[string]any{"role": "member"}, Server: map[string]any{"slug": "dd"}}
	set.ListTools(context.Background(), tools, c, 1) // warm up regex and env caches
	prof := set.EnableProfile()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	tl := set.ListTools(context.Background(), tools, c, 1) // one worker: clean per-rule timings
	runtime.ReadMemStats(&after)
	t.Logf("listed %d tools (%d exposed) on one worker\n%s", len(tools), len(tl.Tools), prof)
	t.Logf("heap: %.1f MiB allocated, %d allocations, %.0f bytes and %.0f allocations per tool",
		float64(after.TotalAlloc-before.TotalAlloc)/(1<<20), after.Mallocs-before.Mallocs,
		float64(after.TotalAlloc-before.TotalAlloc)/float64(len(tools)), float64(after.Mallocs-before.Mallocs)/float64(len(tools)))
	var cat []byte
	cat, _ = json.Marshal(tools)
	t.Logf("for scale: the catalog itself is %.1f MiB as JSON", float64(len(cat))/(1<<20))
}

func benchList(b *testing.B, rules []Rule, workers int) {
	tools := realTools(b)
	set, probs := Compile(rules)
	if probs != nil {
		b.Fatal(probs)
	}
	c := Context{Caller: map[string]any{"role": "member"}, Server: map[string]any{"slug": "dd"}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		set.ListTools(context.Background(), tools, c, workers)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(len(tools)), "ns/tool")
}

func BenchmarkListCorpusNoRules(b *testing.B)         { benchList(b, nil, 1) }
func BenchmarkListCorpusPolicyOneWorker(b *testing.B) { benchList(b, realisticRules(), 1) }
func BenchmarkListCorpusPolicyParallel(b *testing.B)  { benchList(b, realisticRules(), 0) }

func BenchmarkToolMap(b *testing.B) {
	tools := realTools(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buildPrepared(tools[i%len(tools)])
	}
}

func BenchmarkSingleRuleEval(b *testing.B) {
	set, _ := Compile([]Rule{{Hook: "tools/filter", When: "name.startsWith('delete_') && caller.role != 'admin'", Then: map[string]string{"decision": "'deny'"}}})
	ts := newToolState(prepare(&mcp.Tool{Name: "get_page", InputSchema: map[string]any{"type": "object"}}))
	vars := Context{Caller: map[string]any{"role": "member"}}.vars()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		set.run(context.Background(), "tools/filter", "get_page", ts.resolve, vars, nil)
	}
}

func BenchmarkCallCorpusPolicy(b *testing.B) {
	tools := realTools(b)
	set, _ := Compile(realisticRules())
	e := &Engine{Signer: Signer{Key: []byte("k")}}
	s := &Server{Slug: "dd", Rules: set, Catalog: Catalog{Tools: tools}}
	r := Request{Route: "direct", Servers: []*Server{s}, Caller: map[string]any{"userId": "u", "role": "member"}}
	name := tools[0].Name
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: name, Arguments: json.RawMessage(`{}`)})
	}
}

package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"weak"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The tool catalog and its listing: rules change a caller's view of a tool
// and never the shared catalog.

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

// A rule that changes one parameter copies only what it changes: the schema
// of a parameter it did not touch is still the cached upstream object.
func TestAChangedToolSharesItsUntouchedSchemas(t *testing.T) {
	orig := twoArgTool()
	other := orig.InputSchema.(map[string]any)["properties"].(map[string]any)["tenant"]
	s := mustCompileUncapped(t, Rule{Hook: "tools/list.post", When: "true", Then: map[string]string{"tool.params.q.description": "'query'"}})
	ls := s.ListTools(context.Background(), []*mcp.Tool{orig}, Context{}, 1)
	if len(ls.Tools) != 1 {
		t.Fatalf("listing failed: %+v", ls)
	}
	got := ls.Tools[0].Tool.InputSchema.(map[string]any)["properties"].(map[string]any)["tenant"]
	if reflect.ValueOf(got).Pointer() != reflect.ValueOf(other).Pointer() {
		t.Fatal("editing q deep-copied untouched tenant schema; design requires the cached sibling pointer")
	}
}

// Two entitlements list and call the same shared catalog in parallel, each
// with its own renames and pins. Each sees only its own names, each call is
// turned back into the server's arguments, and the shared catalog is
// unchanged afterwards.
func TestEntitlementsSharingACatalogDoNotLeakIntoEachOther(t *testing.T) {
	orig := twoArgTool()
	before, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var servers [2]*Server
	for i, slug := range []string{"a", "b"} {
		servers[i] = &Server{Slug: slug, Rules: mustCompileUncapped(t,
			Rule{Hook: "tools/list.post", When: "true", Then: map[string]string{"tool.params.q.name": fmt.Sprintf("'%squery'", slug), "tool.params.tenant.pin": fmt.Sprintf("'%s'", slug)}},
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

// TestListingIsByteConsistent lists the real corpus twice, once with the
// catalog shuffled, and requires identical bytes: caches stay byte-consistent
// whatever order the upstream returns.
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

// An integer too large for a float64 in a tool's schema comes out exact when
// a rule changes something else on the tool.
func TestALargeIntegerInASchemaSurvivesARuleEdit(t *testing.T) {
	e, r := entitledRequest(t, Rule{Hook: "tools/list.post", When: "true", Then: map[string]string{"tool.description": "'changed'"}})
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

// tool.annotations takes any key on the list hook. A key MCP does not name
// must not be kept for the next list rule and then lost: either the set is
// refused when the rule is saved, or the key is on the wire and on the tool
// the call hooks read.
func TestAToolAnnotationMCPDoesNotNameIsRefusedOrKept(t *testing.T) {
	ctx := context.Background()
	rules := []Rule{
		{Hook: "tools/list.post", When: "true", Then: map[string]string{"tool.annotations.risk": "'high'"}},
		{Hook: "tools/list.post", When: "true", Then: map[string]string{"tool.description": "'risk=' + tool.annotations.risk"}},
		{Hook: "tools/call.pre", When: "cel.bind(a, tool.?annotations.orValue({}), a.?risk.orValue('low')) == 'high'", Then: map[string]string{KeyDecision: "'deny'", KeyReason: "'high risk'"}},
	}
	if _, probs := Compile(rules); probs != nil {
		return // refused when the rule is saved: nothing is lost
	}
	s := newServer(t, oneTool(), rules...)
	listed := asJSON((&Engine{}).ListTools(ctx, newRequest("direct", s)).Result)
	if !strings.Contains(listed, "risk=high") {
		t.Fatalf("the next list rule did not see the annotation: %s", listed)
	}
	if !strings.Contains(listed, `"risk":"high"`) {
		t.Errorf("the client's listing does not carry the annotation: %s", listed)
	}
	if o := (&Engine{}).CallTool(ctx, newRequest("direct", s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`)}); o.Forward != nil {
		t.Errorf("the call hook did not see the annotation a list rule set: the call was forwarded")
	}
}

// A server with no rule set at all, which is the default allow, lists its
// tools and does not panic.
func TestAServerWithNoRuleSetListsItsTools(t *testing.T) {
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("nil rules (default allow) panicked: %v", p)
		}
	}()
	// Set.run, Set.Has, Set.Rules and callFilled explicitly support nil sets.
	e := &Engine{}
	r := Request{Route: "direct", Servers: []*Server{{Slug: "a", Catalog: Catalog{Tools: []*mcp.Tool{twoArgTool()}}}}}
	out := e.ListTools(context.Background(), r)
	if out.Error != nil || out.Result == nil || len(out.Result.(*mcp.ListToolsResult).Tools) != 1 {
		t.Fatalf("default listing failed: %+v", out)
	}
}

// Set.ListTools is exported and can be called without the engine around it. A
// catalog decoded from JSON can hold a null entry: the listing reports it as
// an issue and no panic reaches the caller, with one worker or with several.
func TestSetListToolsReportsANullCatalogEntry(t *testing.T) {
	for _, workers := range []int{1, 2} {
		t.Run(fmt.Sprint(workers), func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("exported Set.ListTools lets panic reach its caller: %T", p)
				}
			}()
			// JSON catalogs can contain null entries; the Engine wrapper handles it.
			var cat []*mcp.Tool
			if err := json.Unmarshal([]byte(`[{"name":"t","inputSchema":{"type":"object"}},null]`), &cat); err != nil {
				t.Fatal(err)
			}
			got := mustCompileUncapped(t).ListTools(context.Background(), cat, Context{}, workers)
			if len(got.Issues) == 0 {
				t.Error("malformed catalog was not reported")
			}
		})
	}
}

// abandonedCatalog lists a catalog and returns weak references to its tools.
// The catalog, the listing and the compiled set go out of scope when it
// returns, which is why it must not be inlined.
//
//go:noinline
func abandonedCatalog(t *testing.T) []weak.Pointer[mcp.Tool] {
	t.Helper()
	set := mustCompileUncapped(t)
	var cat []*mcp.Tool
	var refs []weak.Pointer[mcp.Tool]
	for i := 0; i < 32; i++ {
		tool := &mcp.Tool{Name: fmt.Sprintf("gc_%d", i), Description: strings.Repeat("x", 128<<10), InputSchema: map[string]any{"type": "object"}}
		cat = append(cat, tool)
		refs = append(refs, weak.Make(tool))
	}
	listed := set.ListTools(context.Background(), cat, Context{}, 1)
	if len(listed.Tools) != len(cat) || len(listed.Issues) != 0 {
		t.Fatalf("setup listing: tools=%d issues=%v", len(listed.Tools), listed.Issues)
	}
	return refs // Catalog, exposed result and compiled set leave scope here.
}

// The cache of prepared tools is keyed weakly by the catalog's tools and must
// not keep them alive: once a catalog is dropped, the garbage collector frees
// it.
func TestThePreparedToolCacheReleasesAnAbandonedCatalog(t *testing.T) {
	refs := abandonedCatalog(t)
	// Remove only this test's entries afterwards, so its abandoned catalog is
	// not left in the cache for the rest of the test process.
	t.Cleanup(func() {
		for _, ref := range refs {
			prepCache.Delete(ref)
		}
	})
	for i := 0; i < 10; i++ {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	alive := 0
	for _, ref := range refs {
		if ref.Value() != nil {
			alive++
		}
	}
	if alive != 0 {
		t.Fatalf("abandoned catalog still has %d/%d live tools after 10 GCs; cache must not own its weak keys through preparedTool.tool", alive, len(refs))
	}
}

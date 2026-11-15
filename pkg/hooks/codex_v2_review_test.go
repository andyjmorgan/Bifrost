package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
	"weak"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// These regressions assert the contracts claimed by docs/rules/v2-report.md.

//go:noinline
func codexV2AbandonedCatalog(t *testing.T) []weak.Pointer[mcp.Tool] {
	t.Helper()
	set := codexSet(t)
	var cat []*mcp.Tool
	var refs []weak.Pointer[mcp.Tool]
	for i := 0; i < 32; i++ {
		tool := &mcp.Tool{Name: fmt.Sprintf("codex_gc_%d", i), Description: strings.Repeat("x", 128<<10), InputSchema: map[string]any{"type": "object"}}
		cat = append(cat, tool)
		refs = append(refs, weak.Make(tool))
	}
	listed := set.ListTools(context.Background(), cat, Context{}, 1)
	if len(listed.Tools) != len(cat) || len(listed.Issues) != 0 {
		t.Fatalf("setup listing: tools=%d issues=%v", len(listed.Tools), listed.Issues)
	}
	return refs // Catalog, exposed result and compiled set leave scope here.
}

func TestCodexV2PreparedCacheReleasesCatalog(t *testing.T) { // V2-CX-G01
	refs := codexV2AbandonedCatalog(t)
	// Remove only this test's entries afterwards, so the regression does not
	// intentionally leave its abandoned generation in a longer test process.
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

func TestCodexV2InterleavedAuditCannotExpireEnforcement(t *testing.T) { // V2-CX-G02
	before := Rule{Hook: "tools/filter", When: "false", Then: map[string]string{"decision": "'deny'"}}
	audit := Rule{Mode: "audit", Hook: "tools/filter", When: "description == 'slow'", Then: map[string]string{"decision": "'deny'"}}
	after := Rule{Hook: "tools/filter", When: "lists.range(1000).exists(i, i < 0)", Then: map[string]string{"decision": "'deny'"}}
	run := func(rs []Rule) outcome {
		set := codexSet(t, rs...)
		ctx := withBudget(context.Background(), 100*time.Millisecond, time.Second)
		resolve := func(name string) (any, bool) {
			if name == "description" {
				// Controlled audit-only work makes this independent of CPU speed.
				time.Sleep(250 * time.Millisecond)
				return "slow", true
			}
			return nil, false
		}
		return set.run(ctx, "tools/filter", "t", resolve, Context{}.vars(), nil)
	}
	if o := run([]Rule{before, after}); o.denied || o.err != nil {
		t.Fatalf("control failed: %+v", o)
	}
	if o := run([]Rule{before, audit, after}); o.denied || o.err != nil {
		t.Fatalf("audit-only work expired a later enforcement evaluation: %+v", o)
	}
}

func TestCodexV2ClosedSchemaStillClosesWithEmptyAllOf(t *testing.T) { // V2-CX-G03
	tool := &mcp.Tool{Name: "t", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}},
		"additionalProperties": false, "allOf": []any{map[string]any{}},
	}}
	s := &Server{Slug: "a", Rules: codexSet(t), Catalog: Catalog{Tools: []*mcp.Tool{tool}}}
	e := &Engine{}
	r := Request{Route: "direct", Servers: []*Server{s}}
	control := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"ok"}`)})
	if control.Forward == nil {
		t.Fatalf("valid control did not forward: %+v", control)
	}
	out := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"ok","extra":"must not forward"}`)})
	if out.Forward != nil || out.Error == nil || out.Error.Code != CodeInvalidParams {
		t.Fatalf("allOf:[{}] disabled explicit additionalProperties:false: %+v", out)
	}
}

func TestCodexV2UnsupportedTemplateDoesNotPanicOnExactRead(t *testing.T) { // V2-CX-G04
	s := &Server{Slug: "a", Rules: codexSet(t), Catalog: Catalog{
		Resources: []*mcp.Resource{{Name: "doc", URI: "file:///doc"}},
		Templates: []*mcp.ResourceTemplate{{Name: "query", URITemplate: "file:///x{?q}"}},
	}}
	e := &Engine{}
	r := Request{Route: "direct", Servers: []*Server{s}}
	p := &mcp.ReadResourceParams{URI: "file:///doc"}
	call := e.ReadResource(context.Background(), r, p)
	if call.Forward == nil {
		t.Fatalf("exact listed resource was not readable: %+v", call)
	}
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("unrelated unsupported template caused a read-result panic: %v", p)
		}
	}()
	out := e.UpstreamReadReply(context.Background(), r, call.Call, p, json.RawMessage(`{"resultType":"complete","contents":[{"uri":"file:///doc","text":"ok"}]}`))
	if out.Error != nil || out.Result == nil {
		t.Fatalf("exact read reply failed: %+v", out)
	}
}

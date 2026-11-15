package clients

import (
	"sort"
	"strings"
	"testing"

	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness"
)

// 2026-07-28 requires resultType on every result. The Python and TypeScript
// SDKs enforce that and reject a result without it; the Go SDK does not
// check, so a missing one goes unnoticed in Go-only tests. This test asks
// the gateway for each kind of result with a plain HTTP client and fails on
// any that comes back without resultType, whatever the SDKs make of it.
func TestGatewayResultsCarryResultType(t *testing.T) {
	u := harness.NewUpstream(t, harness.V2026)
	u.Tool(plainTool, nil)
	u.Tool(`{"name":"blocked"}`, nil)
	u.Tool(`{"name":"confirmed"}`, nil)
	u.Tool(`{"name":"shaped"}`, nil)
	u.Prompt(`{"name":"greet"}`, nil)
	u.Resource("docs://a", nil)
	u.Template("notes://{id}")
	g := harness.NewGateway(t)
	g.AddServer(t, "a", u,
		rule("tools/call", "name == 'blocked'", "decision", "'deny'", "reason", "'Not allowed.'"),
		rule("tools/call", "name == 'confirmed' && !has(input.ok)", "result", "elicit('ok', 'Sure?')"),
		rule("tools/result", "name == 'shaped'", "content", "[{'type': 'text', 'text': 'shaped'}]"))
	call := func(name string) harness.RawRequest {
		return harness.RawRequest{Method: "tools/call", Params: map[string]any{"name": name, "arguments": map[string]any{}}, Capabilities: withElicitation}
	}
	kinds := []struct {
		what     string
		endpoint string
		req      harness.RawRequest
	}{
		{"server/discover", g.Direct("a"), harness.RawRequest{Method: "server/discover"}},
		{"tools/list", g.Direct("a"), harness.RawRequest{Method: "tools/list"}},
		{"prompts/list", g.Direct("a"), harness.RawRequest{Method: "prompts/list"}},
		{"resources/list", g.Direct("a"), harness.RawRequest{Method: "resources/list"}},
		{"resources/templates/list", g.Direct("a"), harness.RawRequest{Method: "resources/templates/list"}},
		{"a forwarded tool result", g.Direct("a"), call("t")},
		{"a tool result shaped by a rule", g.Direct("a"), call("shaped")},
		{"a refused call", g.Direct("a"), call("blocked")},
		{"a confirmation request", g.Direct("a"), call("confirmed")},
		{"a forwarded prompt", g.Direct("a"), harness.RawRequest{Method: "prompts/get", Params: map[string]any{"name": "greet"}}},
		{"a forwarded read", g.Direct("a"), harness.RawRequest{Method: "resources/read", Params: map[string]any{"uri": "docs://a"}}},
		{"tools/list on the mux", g.Mux(), harness.RawRequest{Method: "tools/list"}},
		{"the mux's own instructions tool", g.Mux(), harness.RawRequest{Method: "tools/call", Params: map[string]any{"name": "get_server_instructions", "arguments": map[string]any{"slug": "a"}}}},
		{"tools/list in search mode", g.Search(), harness.RawRequest{Method: "tools/list"}},
		{"a search-mode tool result", g.Search(), harness.RawRequest{Method: "tools/call", Params: map[string]any{"name": "list_tools", "arguments": map[string]any{}}}},
	}
	var missing, present []string
	for _, k := range kinds {
		rep := harness.Raw(t, k.endpoint, k.req)
		res := rep.Result()
		if res == nil {
			t.Errorf("%s: no result came back: %v", k.what, rep.Final())
			continue
		}
		if rt, _ := res["resultType"].(string); rt == "" {
			missing = append(missing, k.what)
		} else {
			present = append(present, k.what+" ("+rt+")")
		}
	}
	sort.Strings(missing)
	t.Logf("results with resultType: %s", strings.Join(present, "; "))
	if len(missing) > 0 {
		t.Errorf("results without resultType: %s", strings.Join(missing, "; "))
	}
}

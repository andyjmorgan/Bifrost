package gateway_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness"
)

func TestIconsSurviveDiscoveryCacheAndToolListings(t *testing.T) {
	for _, version := range []string{harness.V2026, harness.V2025} {
		t.Run(version, func(t *testing.T) {
			r := newRig(t)
			u := harness.NewUpstream(t, version)
			var icons any
			_ = json.Unmarshal([]byte(`[{"src":"https://example.com/icon.svg","mimeType":"image/svg+xml","sizes":["any"],"theme":"dark"}]`), &icons)
			info := map[string]any{"name": "icon-server", "version": "1", "icons": icons}
			caps := map[string]any{"tools": map[string]any{}}
			if version == harness.V2026 {
				u.Method("server/discover", harness.Static(harness.Reply{Result: map[string]any{
					"supportedVersions": []string{version}, "capabilities": caps, "_meta": map[string]any{hooks.MetaServerInfo: info},
				}}))
			} else {
				u.Method("initialize", harness.Static(harness.Reply{Result: map[string]any{
					"protocolVersion": version, "capabilities": caps, "serverInfo": info,
				}}))
			}
			u.Tool(`{"name":"icon-tool","inputSchema":{"type":"object"},"icons":[{"src":"https://example.com/icon.svg","mimeType":"image/svg+xml","sizes":["any"],"theme":"dark"}]}`, nil)
			r.serve("a", u, hooks.Rule{Hook: "server.settings", When: "true", Then: map[string]string{"settings.serverInfo": "true"}})
			// The second discovery reads the cached handshake.
			for range 2 {
				res := harness.Raw(t, r.direct("a"), harness.RawRequest{Method: "server/discover"}).Result()
				got := res["_meta"].(map[string]any)[hooks.MetaServerInfo].(map[string]any)["icons"]
				if !reflect.DeepEqual(got, icons) {
					t.Fatalf("discovery icons = %v, want %v", got, icons)
				}
			}
			for _, endpoint := range []string{r.direct("a"), r.mux()} {
				res := harness.Raw(t, endpoint, harness.RawRequest{Method: "tools/list"}).Result()
				got := res["tools"].([]any)[0].(map[string]any)["icons"]
				if !reflect.DeepEqual(got, icons) {
					t.Fatalf("tool icons = %v, want %v", got, icons)
				}
			}
			res := harness.Raw(t, r.mux(), harness.RawRequest{Method: "server/discover"}).Result()
			if _, ok := res["_meta"].(map[string]any)[hooks.MetaServerInfo].(map[string]any)["icons"]; ok {
				t.Fatal("the mux borrowed one server's icon")
			}
		})
	}
}

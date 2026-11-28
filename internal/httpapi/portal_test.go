package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

// GET /portal lists the servers the caller holds, from internal/portal (the
// Connect API lists from there too), in the shape the web UI reads.
func TestPortalListsOnlyTheCallersServers(t *testing.T) {
	r := newAPIRig(t, nil)
	s := r.server() // registered by Alice, who holds it

	held := func(user string) map[string]map[string]any {
		status, body := r.do(http.MethodGet, "/portal", user, "", nil)
		if status != http.StatusOK {
			t.Fatalf("portal as %s: %d %s", user, status, body)
		}
		var items []map[string]any
		if err := json.Unmarshal([]byte(body), &items); err != nil {
			t.Fatalf("%v in %s", err, body)
		}
		out := map[string]map[string]any{}
		for _, it := range items {
			out[it["slug"].(string)] = it
		}
		return out
	}
	got, ok := held(alice)[s.Slug]
	if !ok {
		t.Fatalf("alice does not see %s", s.Slug)
	}
	for _, k := range []string{"id", "slug", "name", "description", "mcpUrl", "url", "enabled", "selection", "capabilities", "hasRules"} {
		if _, ok := got[k]; !ok {
			t.Errorf("portal item lacks %q: %v", k, got)
		}
	}
	if got["mcpUrl"] != "http://gateway.test/"+s.Slug+"/mcp" {
		t.Errorf("mcpUrl = %v", got["mcpUrl"])
	}
	if _, ok := held(bob)[s.Slug]; ok {
		t.Fatalf("a user without a grant sees %s", s.Slug)
	}
}

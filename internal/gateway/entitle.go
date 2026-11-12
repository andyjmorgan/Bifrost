package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/cache"
	"github.com/andyjmorgan/Bifrost/internal/store"
)

// Entitlements are the gateway's only way to a server. A caller holds some
// entitlements; each is one slug on one server, cut to the entitlement's
// selection. The same cut decides what is listed and what may be called, so
// a capability that is not listed is also not callable, and a refused call
// looks exactly like a call to something that does not exist. What a server
// has been seen to offer is recorded as governance data, never as a filter.

// exposures lists the entitlements the caller holds with the live cache of
// the given namespaces. On a server marked exclusive a caller holding more
// than one entitlement gets none of them; those come back as conflicts.
func (g *Gateway) exposures(ctx context.Context, caller *auth.Caller, ns ...string) ([]*store.Server, map[uuid.UUID]store.CacheBundle, map[uuid.UUID]error, error) {
	servers, cached, err := g.store.ListExposuresWithCache(ctx, caller.OrgID, caller.UserID, string(caller.Role), ns...)
	if err != nil {
		return nil, nil, nil, err
	}
	bySrv := map[uuid.UUID][]string{}
	for _, s := range servers {
		if s.Exclusive {
			bySrv[s.ID] = append(bySrv[s.ID], s.Slug)
		}
	}
	conflicts := map[uuid.UUID]error{}
	for id, slugs := range bySrv {
		if len(slugs) > 1 {
			conflicts[id] = fmt.Errorf("entitlements conflict: %s on an exclusive server", strings.Join(slugs, ", "))
		}
	}
	return servers, cached, conflicts, nil
}

// exposure resolves one slug the caller holds.
func (g *Gateway) exposure(ctx context.Context, caller *auth.Caller, slug string, ns ...string) (*store.Server, store.CacheBundle, error) {
	srv, bundle, err := g.store.GetExposureBySlugWithCache(ctx, caller.OrgID, caller.UserID, string(caller.Role), strings.ToLower(slug), ns...)
	if err != nil {
		return nil, nil, err
	}
	if srv.Exclusive {
		all, _, conflicts, err := g.exposures(ctx, caller)
		if err != nil {
			return nil, nil, err
		}
		_ = all
		if c, ok := conflicts[srv.ID]; ok {
			return nil, nil, c
		}
	}
	return srv, bundle, nil
}

// ---- catalog --------------------------------------------------------------------

// observe records what a server offered, for the server page and the
// entitlement editor. Called only after an upstream fetch, never on a cache
// hit. Bookkeeping: a failure must not fail the listing.
func (g *Gateway) observeTools(ctx context.Context, srv *store.Server, tools []*mcp.Tool) {
	names := make(map[string]string, len(tools))
	for _, t := range tools {
		names[t.Name] = fingerprint(t.Description, t.InputSchema)
	}
	g.observe(ctx, srv, store.KindTool, names)
}

func (g *Gateway) observeRaw(ctx context.Context, srv *store.Server, ns string, raw json.RawMessage) {
	kind, keys := kindOf(ns), namesOf(ns, raw)
	names := make(map[string]string, len(keys))
	for _, k := range keys {
		names[k] = ""
	}
	g.observe(ctx, srv, kind, names)
}

func (g *Gateway) observe(ctx context.Context, srv *store.Server, kind string, names map[string]string) {
	if len(names) == 0 {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), g.conn.Timeout())
	defer cancel()
	if err := g.store.ObserveCapabilities(wctx, srv.ID, kind, names); err != nil {
		g.log.Warn("catalog: observe failed", "server", srv.Name, "kind", kind, "err", err)
	}
}

func fingerprint(desc string, schema any) string {
	b, _ := json.Marshal(schema)
	sum := sha256.Sum256(append([]byte(desc+"\x00"), b...))
	return hex.EncodeToString(sum[:8])
}

func kindOf(ns string) string {
	switch ns {
	case cache.NSPrompts:
		return store.KindPrompt
	case cache.NSResources:
		return store.KindResource
	case cache.NSTemplates:
		return store.KindTemplate
	}
	return store.KindTool
}

// namesOf extracts the identity of each item in a cached list: name for
// prompts, uri for resources, uriTemplate for templates.
func namesOf(ns string, raw json.RawMessage) []string {
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	key := "name"
	switch ns {
	case cache.NSResources:
		key = "uri"
	case cache.NSTemplates:
		key = "uriTemplate"
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		var s string
		if json.Unmarshal(it[key], &s) == nil && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ---- the cut --------------------------------------------------------------------

// allows reports whether an exposure exposes one capability: inside the
// entitlement's selection.
func allows(srv *store.Server, kind, name string) bool {
	sel := srv.Selection
	if sel.All {
		return true
	}
	switch kind {
	case store.KindTool:
		return contains(sel.Tools, name)
	case store.KindPrompt:
		return contains(sel.Prompts, name)
	case store.KindResource:
		return matchesAny(sel.Resources, name)
	case store.KindTemplate:
		return contains(sel.Templates, name)
	}
	return false
}

// allowsURI reports whether a resource read is inside the exposure: a
// selected resource pattern, or a selected template whose literal prefix
// matches. With selection.all everything the server serves is in.
func allowsURI(srv *store.Server, uri string) bool {
	if srv.Selection.All || allows(srv, store.KindResource, uri) {
		return true
	}
	for _, tpl := range srv.Selection.Templates {
		prefix := tpl
		if i := strings.IndexByte(prefix, '{'); i >= 0 {
			prefix = prefix[:i]
		}
		if prefix != "" && strings.HasPrefix(uri, prefix) {
			return true
		}
	}
	return false
}

func cutTools(srv *store.Server, tools []*mcp.Tool) []*mcp.Tool {
	out := make([]*mcp.Tool, 0, len(tools))
	for _, t := range tools {
		if allows(srv, store.KindTool, t.Name) {
			out = append(out, t)
		}
	}
	return out
}

// cutRaw filters a cached JSON list by the exposure.
func cutRaw(srv *store.Server, ns string, raw json.RawMessage) json.RawMessage {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return json.RawMessage("[]")
	}
	kind := kindOf(ns)
	key := "name"
	switch ns {
	case cache.NSResources:
		key = "uri"
	case cache.NSTemplates:
		key = "uriTemplate"
	}
	out := make([]json.RawMessage, 0, len(items))
	for _, it := range items {
		var m map[string]json.RawMessage
		var s string
		if json.Unmarshal(it, &m) == nil && json.Unmarshal(m[key], &s) == nil && allows(srv, kind, s) {
			out = append(out, it)
		}
	}
	b, _ := json.Marshal(out)
	return b
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// matchesAny matches a URI against glob patterns: * within a segment, **
// across segments, exact otherwise.
func matchesAny(patterns []string, uri string) bool {
	for _, p := range patterns {
		if p == uri {
			return true
		}
		if strings.Contains(p, "**") {
			prefix, _, _ := strings.Cut(p, "**")
			if strings.HasPrefix(uri, prefix) {
				return true
			}
			continue
		}
		if ok, _ := path.Match(p, uri); ok {
			return true
		}
	}
	return false
}

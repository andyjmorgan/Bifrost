package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/cache"
	"github.com/andyjmorgan/Bifrost/internal/store"
)

// A caller reaches only the servers granted to them, each under its slug and
// cut to its selection. The same cut decides what is listed and what may be
// called, so a capability that is not listed is also not callable, and a
// refused call looks exactly like a call to something that does not exist.
// What a server has been seen to offer is recorded as governance data,
// never as a filter.

type slugsKey struct{}

// withSlugs records a ?slugs=a,b,c narrowing of the mux route.
func withSlugs(ctx context.Context, raw string) context.Context {
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return ctx
	}
	return context.WithValue(ctx, slugsKey{}, out)
}

// granted lists the servers the caller holds with the live cache of the
// given namespaces, narrowed to ?slugs= when the mux was called with it; a
// slug named there that the caller does not hold is an error rather than a
// silent gap, so a configuration mistake shows.
func (g *Gateway) granted(ctx context.Context, caller *auth.Caller, ns ...string) ([]*store.Server, map[uuid.UUID]store.CacheBundle, error) {
	servers, cached, err := g.store.ListGrantedWithCache(ctx, caller.UserID, ns...)
	if err != nil {
		return nil, nil, err
	}
	if want, ok := ctx.Value(slugsKey{}).([]string); ok {
		held := map[string]*store.Server{}
		for _, s := range servers {
			held[s.Slug] = s
		}
		servers = servers[:0:0]
		for _, w := range want {
			s, ok := held[w]
			if !ok {
				return nil, nil, &jsonrpc.Error{Code: -32004, Message: "unknown slug " + w}
			}
			servers = append(servers, s)
		}
	}
	return servers, cached, nil
}

// rpcErr passes a JSON-RPC error through and wraps anything else as internal.
func rpcErr(err error) error {
	var je *jsonrpc.Error
	if errors.As(err, &je) {
		return je
	}
	return &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
}

// grantedServer resolves one slug the caller holds.
func (g *Gateway) grantedServer(ctx context.Context, caller *auth.Caller, slug string, ns ...string) (*store.Server, store.CacheBundle, error) {
	return g.store.GetGrantedBySlugWithCache(ctx, caller.UserID, strings.ToLower(slug), ns...)
}

// ---- catalog --------------------------------------------------------------------

// observe records what a server offered, for the server page and its rule
// editor. Called only after an upstream fetch, never on a cache
// hit. Bookkeeping: a failure must not fail the listing.
func (g *Gateway) observeTools(ctx context.Context, srv *store.Server, tools []*mcp.Tool) {
	items := make(map[string]store.Seen, len(tools))
	for _, t := range tools {
		def, _ := json.Marshal(t)
		items[t.Name] = store.Seen{Fingerprint: fingerprint(t.Description, t.InputSchema), Definition: def}
	}
	g.observe(ctx, srv, store.KindTool, items)
}

func (g *Gateway) observeRaw(ctx context.Context, srv *store.Server, ns string, raw json.RawMessage) {
	kind := kindOf(ns)
	items := map[string]store.Seen{}
	for name, def := range itemsOf(ns, raw) {
		items[name] = store.Seen{Fingerprint: fingerprint("", def), Definition: def}
	}
	g.observe(ctx, srv, kind, items)
}

func (g *Gateway) observe(ctx context.Context, srv *store.Server, kind string, items map[string]store.Seen) {
	if len(items) == 0 {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), g.conn.Timeout())
	defer cancel()
	if err := g.store.ObserveCapabilities(wctx, srv.ID, kind, items); err != nil {
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

// identityKey is the field that identifies an item of a namespace: name
// for prompts, uri for resources, uriTemplate for templates.
func identityKey(ns string) string {
	switch ns {
	case cache.NSResources:
		return "uri"
	case cache.NSTemplates:
		return "uriTemplate"
	}
	return "name"
}

// itemsOf maps each item of a cached list to its raw definition by identity.
func itemsOf(ns string, raw json.RawMessage) map[string]json.RawMessage {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	key := identityKey(ns)
	out := make(map[string]json.RawMessage, len(items))
	for _, it := range items {
		var m map[string]json.RawMessage
		var s string
		if json.Unmarshal(it, &m) == nil && json.Unmarshal(m[key], &s) == nil && s != "" {
			out[s] = it
		}
	}
	return out
}

// ---- the selection cut ----------------------------------------------------------

// CallerKeys describes each key of request.caller: what callerMap supplies.
// Editors use it to complete request.caller.<key>.
var CallerKeys = map[string]string{
	"userId":  "The caller's user id.",
	"email":   "The caller's email address. Absent when the caller has none, such as an API key.",
	"name":    "The caller's display name. Absent when the caller has none.",
	"type":    "How the caller signed in: 'oidc', 'apikey', 'dev' or 'system'.",
	"keyName": "The API key's name. Absent unless the caller is a key.",
}

func callerMap(c *auth.Caller) map[string]any {
	if c == nil {
		return map[string]any{}
	}
	// Nothing is filled in: a field this caller does not have is left out,
	// so a rule that reads it plainly fails and does not match on "".
	m := map[string]any{"userId": c.UserID, "type": string(c.Type)}
	if c.Email != "" {
		m["email"] = c.Email
	}
	if c.Name != "" {
		m["name"] = c.Name
	}
	if c.Type == auth.ActorAPIKey && c.Subject != "" {
		m["keyName"] = c.Subject
	}
	return m
}

// cutter applies a server's allow lists: its selection. It decides what of a
// server's catalog the rules engine is given at all. The server's rules run
// afterwards, in the engine, on what is left.
type cutter struct {
	srv *store.Server
	// broken is kept for callers that report a broken server; the selection
	// alone cannot be broken.
	broken error
}

func (g *Gateway) cutterFor(_ context.Context, srv *store.Server, _ cache.Init) *cutter {
	return &cutter{srv: srv}
}

// allows reports whether the allow list for a kind names the capability,
// or there is no list for that kind.
func (c *cutter) allows(kind, name string, _ any) bool {
	return inList(c.srv, kind, name)
}

// allowsURI decides whether a URI is inside the selection: a selected
// resource, or a selected template whose literal prefix matches. A
// selection that lists resources but not templates leaves templates open,
// so a URI outside the resource list may still come from a template.
func (c *cutter) allowsURI(uri string, def any) bool {
	if c.allows(store.KindResource, uri, def) {
		return true
	}
	tpls, ok := c.srv.Selection[store.SelTemplates]
	if !ok {
		return true
	}
	for _, tpl := range tpls {
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

func (c *cutter) tools(tools []*mcp.Tool) []*mcp.Tool {
	out := make([]*mcp.Tool, 0, len(tools))
	for _, t := range tools {
		if c.allows(store.KindTool, t.Name, t) {
			out = append(out, t)
		}
	}
	return out
}

// raw filters a cached JSON list by the server's selection.
func (c *cutter) raw(ns string, raw json.RawMessage) json.RawMessage {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return json.RawMessage("[]")
	}
	kind, key := kindOf(ns), identityKey(ns)
	out := make([]json.RawMessage, 0, len(items))
	for _, it := range items {
		var m map[string]any
		if json.Unmarshal(it, &m) != nil {
			continue
		}
		s, _ := m[key].(string)
		if c.allows(kind, s, m) {
			out = append(out, it)
		}
	}
	b, _ := json.Marshal(out)
	return b
}

// ---- the allow lists ----------------------------------------------------------

// inList reports whether the server's allow list for a kind names the
// capability, or there is no list for that kind.
func inList(srv *store.Server, kind, name string) bool {
	list, ok := srv.Selection[store.SelectionKey(kind)]
	if !ok {
		return true
	}
	if kind == store.KindResource {
		return matchesAny(list, name)
	}
	return contains(list, name)
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

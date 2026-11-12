package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/internal/audit"
	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/cache"
	"github.com/andyjmorgan/Bifrost/internal/rules"
	"github.com/andyjmorgan/Bifrost/internal/store"
)

// Entitlements are the gateway's only way to a server. A caller holds some
// entitlements; each is one slug on one server, cut to the entitlement's
// selection. The same cut decides what is listed and what may be called, so
// a capability that is not listed is also not callable, and a refused call
// looks exactly like a call to something that does not exist. What a server
// has been seen to offer is recorded as governance data, never as a filter.

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

// exposures lists the entitlements the caller holds with the live cache of
// the given namespaces, narrowed to ?slugs= when the mux was called with
// it; a slug named there that the caller does not hold is an error rather
// than a silent gap, so a configuration mistake shows. On a server marked
// exclusive a caller holding more than one entitlement gets none of them;
// those come back as conflicts.
func (g *Gateway) exposures(ctx context.Context, caller *auth.Caller, ns ...string) ([]*store.Server, map[uuid.UUID]store.CacheBundle, map[uuid.UUID]error, error) {
	servers, cached, err := g.store.ListExposuresWithCache(ctx, caller.OrgID, caller.UserID, string(caller.Role), ns...)
	if err != nil {
		return nil, nil, nil, err
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
				return nil, nil, nil, &jsonrpc.Error{Code: -32004, Message: "unknown slug " + w}
			}
			servers = append(servers, s)
		}
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

// rpcErr passes a JSON-RPC error through and wraps anything else as internal.
func rpcErr(err error) error {
	var je *jsonrpc.Error
	if errors.As(err, &je) {
		return je
	}
	return &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
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

// ---- rules ----------------------------------------------------------------------

type clientKey struct{}

// withClient records the inbound session's initialize params so rules can
// see the client's name and capabilities.
func withClient(ctx context.Context, ip *mcp.InitializeParams) context.Context {
	if ip == nil {
		return ctx
	}
	return context.WithValue(ctx, clientKey{}, ip)
}

func clientMap(ctx context.Context) map[string]any {
	ip, _ := ctx.Value(clientKey{}).(*mcp.InitializeParams)
	out := map[string]any{"name": "", "version": "", "protocolVersion": "", "capabilities": map[string]any{"elicitation": false, "sampling": false, "roots": false}}
	if ip == nil {
		return out
	}
	out["protocolVersion"] = ip.ProtocolVersion
	if ip.ClientInfo != nil {
		out["name"], out["version"] = ip.ClientInfo.Name, ip.ClientInfo.Version
	}
	if c := ip.Capabilities; c != nil {
		out["capabilities"] = map[string]any{"elicitation": c.Elicitation != nil, "sampling": c.Sampling != nil, "roots": c.Roots.ListChanged || c.RootsV2 != nil}
	}
	return out
}

func callerMap(c *auth.Caller) map[string]any {
	if c == nil {
		return map[string]any{}
	}
	return map[string]any{"userId": c.UserID, "email": c.Email, "name": c.Name, "role": string(c.Role), "type": string(c.Type), "keyName": func() string {
		if c.Type == auth.ActorAPIKey {
			return c.Subject
		}
		return ""
	}()}
}

func serverMap(srv *store.Server, in cache.Init) map[string]any {
	return map[string]any{"slug": srv.Slug, "name": srv.Name, "description": srv.Description, "url": srv.URL, "instructions": in.Instructions, "protocolVersion": in.ProtocolVersion}
}

type compiledRules struct {
	key string
	c   *rules.Compiled
	err error
}

// rulesFor compiles an exposure's rules once per generation.
func (g *Gateway) rulesFor(srv *store.Server) (*rules.Compiled, error) {
	key := srv.EntitlementID.String() + "@" + fmt.Sprint(srv.Generation)
	if v, ok := g.rules.Load(srv.EntitlementID); ok {
		if cr := v.(compiledRules); cr.key == key {
			return cr.c, cr.err
		}
	}
	rs, err := rules.Parse(srv.Policies)
	var c *rules.Compiled
	if err == nil {
		var probs rules.Problems
		if c, probs = rules.Compile(rs); probs != nil {
			err = probs
		}
	}
	g.rules.Store(srv.EntitlementID, compiledRules{key: key, c: c, err: err})
	return c, err
}

// cutter decides, for one exposure in one request, what is exposed: the
// allow lists first, then the rules in order. Rules that do not compile
// fail the exposure closed: nothing is exposed and the listing reports it.
type cutter struct {
	ctx    context.Context
	srv    *store.Server
	rules  *rules.Compiled
	broken error
	server map[string]any
	caller map[string]any
	client map[string]any
	route  string
}

func (g *Gateway) cutterFor(ctx context.Context, srv *store.Server, in cache.Init) *cutter {
	c := &cutter{ctx: ctx, srv: srv, route: routeFrom(ctx), server: serverMap(srv, in), caller: callerMap(auth.CallerFrom(ctx)), client: clientMap(ctx)}
	c.rules, c.broken = g.rulesFor(srv)
	if c.broken != nil {
		audit.AddIssue(ctx, srv.Slug+": rules do not compile: "+c.broken.Error())
	}
	return c
}

func routeFrom(ctx context.Context) string {
	if r, ok := ctx.Value(routeKey{}).(string); ok {
		return r
	}
	return ""
}

// allows decides one capability: in the allow list (or no list), then the
// rules, last match winning.
func (c *cutter) allows(kind, name string, def any) bool {
	if c.broken != nil {
		return false
	}
	base := inList(c.srv, kind, name)
	if c.rules.Empty() {
		return base
	}
	ok, _ := c.rules.Decide(c.ctx, base, rules.Subject{Kind: kind, Name: name, Def: def, Server: c.server, Caller: c.caller, Client: c.client, Route: c.route})
	return ok
}

// allowsURI decides a resource read: an allowed resource, or an allowed
// template whose literal prefix matches.
func (c *cutter) allowsURI(uri string, def any) bool {
	if c.allows(store.KindResource, uri, def) {
		return true
	}
	tpls, ok := c.srv.Selection[store.SelTemplates]
	if !ok {
		return c.broken == nil
	}
	for _, tpl := range tpls {
		prefix := tpl
		if i := strings.IndexByte(prefix, '{'); i >= 0 {
			prefix = prefix[:i]
		}
		if prefix != "" && strings.HasPrefix(uri, prefix) && c.allows(store.KindTemplate, tpl, map[string]any{"uriTemplate": tpl}) {
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

// raw filters a cached JSON list by the exposure.
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

// defOf finds an item's definition in a cached list, or a stub.
func defOf(ns string, raw json.RawMessage, name string) any {
	if it, ok := itemsOf(ns, raw)[name]; ok {
		var m map[string]any
		if json.Unmarshal(it, &m) == nil {
			return m
		}
	}
	return map[string]any{identityKey(ns): name}
}

// ---- the allow lists ----------------------------------------------------------

// inList reports whether the entitlement's allow list for a kind names the
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

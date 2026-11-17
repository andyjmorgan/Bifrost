package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/internal/audit"
	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/cache"
	"github.com/andyjmorgan/Bifrost/internal/store"
	"github.com/andyjmorgan/Bifrost/internal/upstream"
	"github.com/andyjmorgan/Bifrost/pkg/hooks"
)

// This file is where the gateway meets the rules engine (pkg/hooks). The
// gateway does transport, credentials, caching and the entitlement's allow
// lists. Everything that decides policy is the engine's: what is listed,
// what a call may carry, what a reply may hold.
//
// One request goes like this:
//
//  1. load: the caller's entitlements on the route become engine servers,
//     each with its compiled rules and the part of its catalog the request
//     needs, cut to the entitlement's selection.
//  2. the engine answers the request, or says what to forward.
//  3. relay: the forward is sent upstream and the reply, every event of it,
//     goes back through the engine before the client sees it.

// rawResult carries a result to the SDK server exactly as the engine wrote
// it. The engine's results are JSON the typed result structs would reshape:
// a question for the user, an unfamiliar content block, resultType.
type rawResult struct {
	mcp.ResultBase
	body json.RawMessage
}

// MarshalJSON writes the engine's body, plus whatever the SDK server set on
// the result after the handler returned: its own identity, under
// io.modelcontextprotocol/serverInfo. A key the body already has wins, so
// discovery keeps the identity the rules chose.
func (r *rawResult) MarshalJSON() ([]byte, error) {
	if len(r.Meta) == 0 {
		return r.body, nil
	}
	m, err := decodeMap(r.body)
	if err != nil {
		return r.body, nil
	}
	meta, _ := m["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	for k, v := range r.Meta {
		if _, ok := meta[k]; !ok {
			meta[k] = v
		}
	}
	m["_meta"] = meta
	return json.Marshal(m)
}

// Meta keys a current-version request carries for itself.
const (
	metaClientInfo = "io.modelcontextprotocol/clientInfo"
	metaClientCaps = "io.modelcontextprotocol/clientCapabilities"
)

// nsConfirm is the cache namespace that holds pending confirmations.
const nsConfirm = "confirm"

func newEngine(g *Gateway, version string) *hooks.Engine {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return &hooks.Engine{
		Version: version,
		Signer:  hooks.Signer{Key: key},
		States:  &cacheStates{g: g},
		OnFault: func(f hooks.Fault) {
			// The engine failed closed. This is the record to investigate from.
			b, _ := json.Marshal(f)
			g.log.Error("rules engine fault", "op", f.Op, "panic", f.Panic, "fault", string(b))
		},
		OnUnknown: func(u hooks.Unknown) {
			// Shapes only, but names can still be customer data: this log is
			// for the gateway's operators.
			g.log.Info("rules engine: unknown shape", "kind", u.Kind, "name", u.Name, "server", u.Server, "target", u.Target, "action", u.Action, "count", u.Count, "shape", u.Shape)
		},
	}
}

// SetMetaAllow sets the two lists of _meta keys the gateway lets through.
// Both are empty unless set, so no _meta crosses the gateway either way.
//
// in is input: the keys of a client's request _meta that may go on to a
// server. A server.session rule still has to turn forwarding on for the
// server; the list bounds what that rule can forward.
//
// out is output: the keys of a server's _meta that may reach a client, on
// the items of its catalog and on the results of its calls, prompts and
// reads. Rules run after this and see what is left; they can still add
// keys of their own. The Go SDK server stamps its name and version into
// _meta of every result, so without this a client would learn the
// upstream's identity there even when the rules hide it in discovery.
//
// The protocol's own keys and the progress token are not _meta in this
// sense: the gateway always writes those itself.
func (g *Gateway) SetMetaAllow(in, out []string) {
	g.metaIn, g.metaOut = append([]string(nil), in...), append([]string(nil), out...)
}

// keepMeta reduces a result's top-level _meta to the allowed keys.
func keepMeta(result json.RawMessage, allow []string) json.RawMessage {
	if !bytes.Contains(result, []byte(`"_meta"`)) {
		return result
	}
	m, err := decodeMap(result)
	if err != nil {
		return result
	}
	meta, ok := m["_meta"].(map[string]any)
	if !ok {
		return result
	}
	kept := map[string]any{}
	for _, k := range allow {
		// The protocol's own keys are the gateway's to write, list or no list.
		if strings.HasPrefix(k, "io.modelcontextprotocol/") {
			continue
		}
		if v, ok := meta[k]; ok {
			kept[k] = v
		}
	}
	if len(kept) == 0 {
		delete(m, "_meta")
	} else {
		m["_meta"] = kept
	}
	out, err := json.Marshal(m)
	if err != nil {
		return result
	}
	return out
}

// SetStateKey sets the key that signs the state the gateway wraps around a
// server's own question. Every replica must use the same key. Without it
// the gateway uses a key of its own, which is only right for one replica.
func (g *Gateway) SetStateKey(key []byte) {
	if len(key) > 0 {
		g.engine.Signer = hooks.Signer{Key: key}
	}
}

// cacheStates keeps pending confirmations in the gateway's TTL cache, so
// any replica can redeem a confirmation another replica asked for. Taking
// one deletes it: a confirmation works once.
type cacheStates struct{ g *Gateway }

func (s *cacheStates) Put(key string, st hooks.State, ttl time.Duration) {
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.g.store.CacheSet(ctx, nsConfirm, key, nil, time.Time{}, b, ttl); err != nil {
		// The answer will find nothing and be refused: closed, not open.
		s.g.log.Error("confirmation could not be stored", "err", err)
	}
}

func (s *cacheStates) Take(key string) (hooks.State, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := s.g.store.CacheTake(ctx, nsConfirm, key)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.g.log.Error("confirmation could not be read", "err", err)
		}
		return hooks.State{}, false
	}
	var st hooks.State
	if json.Unmarshal(raw, &st) != nil {
		return hooks.State{}, false
	}
	return st, true
}

// ---- rules ----------------------------------------------------------------------

// ParseRules reads an entitlement's stored rules. Rules written for the
// retired allow/deny engine are refused: the entitlement then exposes
// nothing until an admin rewrites them.
func ParseRules(raw json.RawMessage) ([]hooks.Rule, error) {
	rs, _, err := ParseStored(raw)
	return rs, err
}

// StoredRules is how an entitlement's rules are kept when they were written
// as text: the text, by stage, beside the engine rules it compiled to. The
// engine runs Rules; Text is what the admin sees again.
type StoredRules struct {
	Text  map[string]string `json:"text"`
	Rules []hooks.Rule      `json:"rules"`
}

// ParseStored reads an entitlement's stored rules and, when they were
// written as text, the text. Rules saved as a bare list have no text.
func ParseStored(raw json.RawMessage) ([]hooks.Rule, map[string]string, error) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" || string(t) == "[]" || string(t) == "{}" {
		return nil, nil, nil
	}
	var text map[string]string
	if t[0] == '{' {
		var st struct {
			Text  map[string]string `json:"text"`
			Rules json.RawMessage   `json:"rules"`
		}
		if err := json.Unmarshal(t, &st); err != nil {
			return nil, nil, fmt.Errorf("rules: %w", err)
		}
		text, t = st.Text, bytes.TrimSpace(st.Rules)
		if len(t) == 0 || string(t) == "null" {
			return nil, text, nil
		}
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(t, &items); err != nil {
		return nil, text, fmt.Errorf("rules are not a list: %w", err)
	}
	for i, it := range items {
		if _, ok := it["hook"]; !ok {
			return nil, text, fmt.Errorf("rule %d is in the retired allow/deny format; rewrite it as a hook rule", i+1)
		}
	}
	var rs []hooks.Rule
	if err := json.Unmarshal(t, &rs); err != nil {
		return nil, text, fmt.Errorf("rules: %w", err)
	}
	return rs, text, nil
}

type compiledHooks struct {
	key string
	set *hooks.Set
	err error
}

var noRules, _ = hooks.Compile(nil)

// hookRules compiles an exposure's rules once per generation.
func (g *Gateway) hookRules(srv *store.Server) (*hooks.Set, error) {
	key := srv.EntitlementID.String() + "@" + strconv.Itoa(srv.Generation)
	if v, ok := g.rules.Load(srv.EntitlementID); ok {
		if c := v.(compiledHooks); c.key == key {
			return c.set, c.err
		}
	}
	set := noRules
	rs, err := ParseRules(srv.Policies)
	if err == nil {
		c, probs := hooks.Compile(rs)
		if len(probs) > 0 {
			err = probs
		} else {
			set = c
		}
	}
	g.rules.Store(srv.EntitlementID, compiledHooks{key: key, set: set, err: err})
	return set, err
}

// ---- load -----------------------------------------------------------------------

// exposed is one entitlement on the route, as the gateway stores it and as
// the engine sees it.
type exposed struct {
	srv  *store.Server
	init cache.Init
	hs   *hooks.Server
	err  error // why its catalog could not be had, if it could not
}

// loaded is a request's view: the engine request and the entitlements
// behind it, by slug.
type loaded struct {
	req  hooks.Request
	by   map[string]*exposed
	down []map[string]string // entitlements that could not be listed
}

// only is the one entitlement of a direct request.
func (l *loaded) only() *exposed {
	for _, x := range l.by {
		return x
	}
	return nil
}

var listFetchers = map[string]func(*upstream.Session) (json.RawMessage, error){
	cache.NSPrompts:   fetchPrompts,
	cache.NSResources: fetchResources,
	cache.NSTemplates: fetchTemplates,
}

// load builds the engine's request: who is calling, how, and every
// entitlement on the route with the catalog namespaces the request needs.
func (g *Gateway) load(ctx context.Context, caller *auth.Caller, route, slug string, meta map[string]any, ns ...string) (*loaded, error) {
	want := append([]string{cache.NSInit}, ns...)
	var (
		servers   []*store.Server
		cached    map[uuid.UUID]store.CacheBundle
		conflicts map[uuid.UUID]error
	)
	if route == "direct" {
		srv, bundle, err := g.exposure(ctx, caller, slug, want...)
		if err != nil {
			return nil, &jsonrpc.Error{Code: -32004, Message: "unknown server"}
		}
		if !srv.Enabled {
			return nil, &jsonrpc.Error{Code: -32004, Message: "server " + srv.Slug + " is disabled"}
		}
		audit.SetTarget(ctx, srv.ID, srv.Slug)
		servers, cached = []*store.Server{srv}, map[uuid.UUID]store.CacheBundle{srv.ID: bundle}
	} else {
		var err error
		if servers, cached, conflicts, err = g.exposures(ctx, caller, want...); err != nil {
			return nil, rpcErr(err)
		}
	}
	ld := &loaded{by: map[string]*exposed{}}
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, srv := range servers {
		if !srv.Enabled {
			continue
		}
		wg.Add(1)
		go func(srv *store.Server) {
			defer wg.Done()
			x := g.expose(ctx, srv, cached[srv.ID], conflicts[srv.ID], ns)
			mu.Lock()
			ld.by[srv.Slug] = x
			mu.Unlock()
		}(srv)
	}
	wg.Wait()
	slugs := make([]string, 0, len(ld.by))
	for s := range ld.by {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)
	req := hooks.Request{
		Route:     route,
		Flags:     map[string]bool{hooks.FlagSearch: searchMode(ctx)},
		Caller:    callerMap(caller),
		Principal: caller.OrgID + "/" + caller.UserID,
		Client:    clientOf(meta),
		Now:       time.Now(),
	}
	for _, s := range slugs {
		x := ld.by[s]
		req.Servers = append(req.Servers, x.hs)
		if x.err != nil {
			g.log.Warn("catalog: server unavailable", "slug", s, "err", x.err)
			audit.AddIssue(ctx, s+": "+x.err.Error())
			if g.rec != nil {
				g.rec.RecordSkipped(ctx, x.srv, route, "catalog", "upstream", x.err.Error())
			}
			ld.down = append(ld.down, map[string]string{"server": s, "error": oneLine(x.err.Error(), 200)})
		}
	}
	ld.req = req
	return ld, nil
}

// expose turns one stored entitlement into an engine server: its rules, what
// the upstream said about itself, and the requested parts of its catalog
// after the entitlement's allow lists.
func (g *Gateway) expose(ctx context.Context, srv *store.Server, bundle store.CacheBundle, conflict error, ns []string) *exposed {
	x := &exposed{srv: srv}
	hs := &hooks.Server{
		Slug:        srv.Slug,
		Entitlement: map[string]any{"id": srv.EntitlementID.String(), "slug": srv.Slug, "name": srv.EntitlementName},
		Generation:  strconv.Itoa(srv.Generation),
		Info:        hooks.ServerInfo{Name: srv.Name, Description: srv.Description, URL: srv.URL},
	}
	x.hs = hs
	set, err := g.hookRules(srv)
	hs.Rules = set
	if err != nil {
		// Rules that do not compile fail the entitlement closed.
		hs.Broken = err
		audit.AddIssue(ctx, srv.Slug+": rules do not compile: "+err.Error())
	}
	if conflict != nil {
		// More than one entitlement on an exclusive server: none of them.
		hs.Broken, x.err = conflict, conflict
		return x
	}
	in, err := g.initFor(ctx, srv, bundle)
	if err != nil {
		hs.Unavailable, x.err = true, err
		return x
	}
	x.init = in
	hs.Info.Version, hs.Info.Title, hs.Info.Instructions, hs.Info.ProtocolVersion = in.ServerVersion, in.Title, in.Instructions, in.ProtocolVersion
	cut := g.cutterFor(ctx, srv, in)
	for _, n := range ns {
		raw, err := g.listFor(ctx, srv, bundle, in, n)
		if err != nil {
			hs.Unavailable, x.err = true, err
			return x
		}
		switch n {
		case cache.NSTools:
			var ts []*mcp.Tool
			_ = json.Unmarshal(raw, &ts)
			hs.Catalog.Tools = cut.tools(ts)
		case cache.NSPrompts:
			_ = json.Unmarshal(cut.raw(n, raw), &hs.Catalog.Prompts)
		case cache.NSResources:
			_ = json.Unmarshal(cut.raw(n, raw), &hs.Catalog.Resources)
		case cache.NSTemplates:
			_ = json.Unmarshal(cut.raw(n, raw), &hs.Catalog.Templates)
		}
	}
	// Upstream _meta is reduced to the allowed keys before any rule runs.
	hs.Catalog = hs.Catalog.StripMeta(g.metaOut)
	return x
}

// InitFrom is what the gateway keeps of a server's handshake.
func InitFrom(ir *mcp.InitializeResult) cache.Init {
	in := cache.Init{Caps: &cache.Caps{}}
	if ir == nil {
		return in
	}
	in.ProtocolVersion, in.Instructions = ir.ProtocolVersion, ir.Instructions
	if c := ir.Capabilities; c != nil {
		in.Caps.Tools, in.Caps.Prompts, in.Caps.Resources = c.Tools != nil, c.Prompts != nil, c.Resources != nil
	}
	if ir.ServerInfo != nil {
		in.ServerName, in.ServerVersion, in.Title = ir.ServerInfo.Name, ir.ServerInfo.Version, ir.ServerInfo.Title
	}
	return in
}

// initFor is what the server said about itself: from the cache, or by
// connecting once. The protocol version decides what the server may be
// offered, so a request cannot go on without it.
func (g *Gateway) initFor(ctx context.Context, srv *store.Server, bundle store.CacheBundle) (cache.Init, error) {
	if raw := bundle[cache.NSInit]; raw != nil {
		var in cache.Init
		if json.Unmarshal(raw, &in) == nil && in.ProtocolVersion != "" {
			return in, nil
		}
	}
	v, err, _ := g.flights.Do(srv.ID.String()+"|init", func() (any, error) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), g.conn.Timeout())
		defer cancel()
		sess, err := g.conn.OpenWith(fctx, srv, "")
		if err != nil {
			return nil, err
		}
		defer sess.Close()
		return InitFrom(sess.InitializeResult()), nil
	})
	if err != nil {
		return cache.Init{}, err
	}
	return v.(cache.Init), nil
}

// listFor is one namespace of a server's catalog as JSON: from the cache,
// or fetched and cached.
func (g *Gateway) listFor(ctx context.Context, srv *store.Server, bundle store.CacheBundle, in cache.Init, ns string) (json.RawMessage, error) {
	if !in.Has(ns) {
		return json.RawMessage("[]"), nil
	}
	if raw := bundle[ns]; raw != nil {
		return raw, nil
	}
	if ns == cache.NSTools {
		tools, _, err := g.fetchTools(ctx, srv, in.ProtocolVersion, false, nil)
		if err != nil {
			return nil, err
		}
		g.observeTools(ctx, srv, tools)
		return json.Marshal(tools)
	}
	return g.fetchList(ctx, srv, ns, in.ProtocolVersion, false, listFetchers[ns])
}

// clientOf is the client as it described itself on this request.
func clientOf(meta map[string]any) map[string]any {
	out := map[string]any{"name": "", "version": "", "capabilities": map[string]any{}}
	if ci, ok := meta[metaClientInfo].(map[string]any); ok {
		if s, ok := ci["name"].(string); ok {
			out["name"] = s
		}
		if s, ok := ci["version"].(string); ok {
			out["version"] = s
		}
	}
	if caps, ok := meta[metaClientCaps].(map[string]any); ok {
		out["capabilities"] = caps
	}
	return out
}

// metaOf is the request's _meta, or an empty map.
func metaOf(req mcp.Request) (meta map[string]any) {
	meta = map[string]any{}
	defer func() { _ = recover() }() // a typed nil params value
	if p := req.GetParams(); p != nil {
		if m := p.GetMeta(); m != nil {
			meta = m
		}
	}
	return meta
}

// ---- outcomes -------------------------------------------------------------------

// note records what the engine reported beside its answer: its issues, and
// every rule that applied, which the request's audit event keeps.
func (g *Gateway) note(ctx context.Context, o hooks.Outcome) {
	for _, is := range o.Issues {
		audit.AddIssue(ctx, is)
	}
	for _, h := range o.Hits {
		g.log.Debug("rule hit", "hook", h.Hook, "rule", h.Rule, "server", h.Server, "item", h.Item, "mode", h.Mode, "effect", h.Effect)
		audit.AddRuleHits(ctx, store.RuleHit{Stage: h.Hook, Rule: h.Rule, Server: h.Server, Item: h.Item, Mode: h.Mode, Effect: h.Effect, Fields: h.Fields, Reason: h.Reason, Error: h.Error})
	}
}

// done turns an engine outcome into what the SDK server sends. edit, if
// set, may add to a successful result before it goes out.
func (g *Gateway) done(ctx context.Context, o hooks.Outcome, edit func(map[string]any)) (mcp.Result, error) {
	g.note(ctx, o)
	if o.Error != nil {
		je := &jsonrpc.Error{Code: o.Error.Code, Message: o.Error.Message}
		if o.Error.Data != nil {
			je.Data, _ = json.Marshal(o.Error.Data)
		}
		return nil, je
	}
	if o.Result == nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "the request produced no outcome"}
	}
	b, err := json.Marshal(o.Result)
	if err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "the result could not be encoded"}
	}
	if edit != nil {
		m, err := decodeMap(b)
		if err != nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "the result could not be encoded"}
		}
		edit(m)
		if b, err = json.Marshal(m); err != nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "the result could not be encoded"}
		}
	}
	return &rawResult{body: b}, nil
}

// decodeMap decodes a JSON object keeping numbers exactly as written.
func decodeMap(b []byte) (map[string]any, error) {
	m := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

func toMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return decodeMap(b)
}

// ---- the methods ----------------------------------------------------------------

func (g *Gateway) hookDiscover(ctx context.Context, caller *auth.Caller, route, slug string, meta map[string]any) (mcp.Result, error) {
	ld, err := g.load(ctx, caller, route, slug, meta)
	if err != nil {
		return nil, err
	}
	return g.done(ctx, g.engine.Discover(ctx, ld.req, g.version), nil)
}

func (g *Gateway) hookList(ctx context.Context, caller *auth.Caller, route, slug, method string, meta map[string]any) (mcp.Result, error) {
	ns := map[string]string{"tools/list": cache.NSTools, "prompts/list": cache.NSPrompts, "resources/list": cache.NSResources, "resources/templates/list": cache.NSTemplates}[method]
	ld, err := g.load(ctx, caller, route, slug, meta, ns)
	if err != nil {
		return nil, err
	}
	if route == "direct" {
		if x := ld.only(); x != nil && x.err != nil && x.hs.Unavailable {
			return nil, upstreamError(x.srv, x.err)
		}
	}
	if method == "tools/list" && searchMode(ctx) {
		return g.hookSearchList(ctx, ld, route)
	}
	var out hooks.Outcome
	switch method {
	case "tools/list":
		out = g.engine.ListTools(ctx, ld.req)
	case "prompts/list":
		out = g.engine.ListPrompts(ctx, ld.req)
	case "resources/list":
		out = g.engine.ListResources(ctx, ld.req)
	default:
		out = g.engine.ListTemplates(ctx, ld.req)
	}
	return g.done(ctx, out, func(m map[string]any) {
		if method == "tools/list" && route == "mux" {
			// The gateway's own tool sits beside the servers' tools.
			if def, err := toMap(refreshToolDef()); err == nil {
				tools, _ := m["tools"].([]any)
				m["tools"] = append(tools, def)
			}
		}
		withDown(m, ld.down)
	})
}

// withDown reports, on a listing, the entitlements that could not be listed.
func withDown(m map[string]any, down []map[string]string) {
	if len(down) == 0 {
		return
	}
	meta, _ := m["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	meta[MetaKeyUnavailable] = down
	m["_meta"] = meta
}

// lowerSlug lowercases the slug of a mux name. Slugs are lowercase by
// construction; models are not always.
func lowerSlug(route, name string) string {
	if route != "mux" {
		return name
	}
	if slug, rest, ok := strings.Cut(name, Separator); ok {
		return strings.ToLower(slug) + Separator + rest
	}
	return name
}

func (g *Gateway) hookCall(ctx context.Context, caller *auth.Caller, route, slug string, req mcp.Request, p *mcp.CallToolParamsRaw) (mcp.Result, error) {
	if route == "mux" && p.Name == RefreshTool {
		return g.refreshServices(ctx, caller, route)
	}
	meta := metaOf(req)
	ld, err := g.load(ctx, caller, route, slug, meta, cache.NSTools)
	if err != nil {
		return nil, err
	}
	e, r := g.engine, ld.req
	call := *p
	call.Name = lowerSlug(route, p.Name)
	switch {
	case route == "mux" && !searchMode(ctx) && p.Name == hooks.InstructionsTool:
		var a struct {
			Slug string `json:"slug"`
		}
		_ = json.Unmarshal(p.Arguments, &a)
		return g.done(ctx, e.ServerInstructions(ctx, r, strings.ToLower(a.Slug)), nil)
	case searchMode(ctx):
		return g.hookSearchCall(ctx, req, ld, route, &call)
	}
	out := e.CallTool(ctx, r, &call)
	return g.relay(ctx, req, ld, out, meta, func(ctx context.Context, in iter.Seq2[hooks.UpstreamEvent, error]) iter.Seq2[hooks.ReplyEvent, error] {
		return e.ToolReplyStream(ctx, r, out.Call, &call, in)
	})
}

func (g *Gateway) hookPrompt(ctx context.Context, caller *auth.Caller, route, slug string, req mcp.Request, p *mcp.GetPromptParams) (mcp.Result, error) {
	meta := metaOf(req)
	ld, err := g.load(ctx, caller, route, slug, meta, cache.NSPrompts)
	if err != nil {
		return nil, err
	}
	e, r := g.engine, ld.req
	get := *p
	get.Name = lowerSlug(route, p.Name)
	out := e.GetPrompt(ctx, r, &get)
	return g.relay(ctx, req, ld, out, meta, func(ctx context.Context, in iter.Seq2[hooks.UpstreamEvent, error]) iter.Seq2[hooks.ReplyEvent, error] {
		return e.PromptReplyStream(ctx, r, out.Call, &get, in)
	})
}

func (g *Gateway) hookRead(ctx context.Context, caller *auth.Caller, route, slug string, req mcp.Request, p *mcp.ReadResourceParams) (mcp.Result, error) {
	meta := metaOf(req)
	ld, err := g.load(ctx, caller, route, slug, meta, cache.NSResources, cache.NSTemplates)
	if err != nil {
		return nil, err
	}
	e, r := g.engine, ld.req
	out := e.ReadResource(ctx, r, p)
	if out.Forward != nil {
		// The entitlement's allow lists bound reads as well as listings: a
		// URI outside the selection does not exist on this entitlement,
		// whatever the rules would have said about it.
		if x := ld.by[out.Forward.Slug]; x != nil {
			uri := p.URI
			if fp, err := toMap(out.Forward.Params); err == nil {
				if s, ok := fp["uri"].(string); ok {
					uri = s
				}
			}
			if !g.cutterFor(ctx, x.srv, x.init).allowsURI(uri, map[string]any{"uri": uri}) {
				g.note(ctx, out)
				return nil, &jsonrpc.Error{Code: -32002, Message: "resource not found: " + p.URI}
			}
		}
	}
	return g.relay(ctx, req, ld, out, meta, func(ctx context.Context, in iter.Seq2[hooks.UpstreamEvent, error]) iter.Seq2[hooks.ReplyEvent, error] {
		return e.ReadReplyStream(ctx, r, out.Call, p, in)
	})
}

// ---- search mode ----------------------------------------------------------------

// hookSearchList is tools/list in search mode: the gateway's search tools in
// place of the servers' tools. The roster in the search tool's description
// is built from what the rules expose, so it never names what a caller
// cannot reach.
func (g *Gateway) hookSearchList(ctx context.Context, ld *loaded, route string) (mcp.Result, error) {
	e, r := g.engine, ld.req
	listed := e.ListTools(ctx, r)
	g.note(ctx, listed)
	counts := map[string]int{}
	if m, err := toMap(listed.Result); err == nil && listed.Error == nil {
		tools, _ := m["tools"].([]any)
		for _, t := range tools {
			tm, _ := t.(map[string]any)
			name, _ := tm["name"].(string)
			slug := ""
			if route == "mux" {
				slug, _, _ = strings.Cut(name, Separator)
			} else if x := ld.only(); x != nil {
				slug = x.srv.Slug
			}
			counts[slug]++
		}
	}
	var sb strings.Builder
	for _, hs := range r.Servers {
		x := ld.by[hs.Slug]
		sw, _ := e.Config(ctx, r, hs)
		if !sw["tools"] {
			continue
		}
		fmt.Fprintf(&sb, "- %s", hs.Slug)
		if sw["searchRoster"] {
			title, desc, instr := hs.Info.Name, hs.Info.Description, ""
			if meta, _, err := e.Modify(ctx, r, hs); err == nil {
				if meta.Title != "" {
					title = meta.Title
				}
				desc = meta.Description
				if sw["instructions"] {
					instr = meta.Instructions
				}
			}
			if title != "" {
				sb.WriteString(": " + title)
			}
			if desc != "" {
				sb.WriteString(" — " + strings.TrimSpace(desc))
			}
			if instr != "" {
				sb.WriteString(" Server notes: " + oneLine(instr, 300))
			}
		}
		switch n := counts[hs.Slug]; {
		case x != nil && x.err != nil:
			sb.WriteString(" (unavailable)")
		case n == 1:
			sb.WriteString(" (1 tool)")
		default:
			fmt.Fprintf(&sb, " (%d tools)", n)
		}
		sb.WriteByte('\n')
	}
	tools := searchDefs(sb.String(), route)
	if route == "mux" {
		tools = append(tools, refreshToolDef())
	}
	out := hooks.Outcome{Result: map[string]any{"tools": tools, "ttlMs": 0, "cacheScope": "private", "resultType": "complete"}}
	return g.done(ctx, out, func(m map[string]any) { withDown(m, ld.down) })
}

// searchDefs are search mode's tools. They carry no output schema: their
// results are the engine's, and a client that checks a result against a
// schema must not be given one that could drift from it.
func searchDefs(roster, route string) []*mcp.Tool {
	obj := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	desc := "Search the tools behind this gateway and get their full definitions, ranked by relevance to your query " +
		"(names, descriptions, input schemas and the server each belongs to). Name a server in the query to see its tools. " +
		"Call results with " + ExecuteTool + " using the returned name.\n\nConnected servers:\n" + roster
	if route == "direct" {
		desc = "Search this server's tools and get their full definitions, ranked by relevance to your query. Call results with " + ExecuteTool + ".\n\n" + roster
	}
	return []*mcp.Tool{
		{Name: SearchTool, Title: "Search tools", Annotations: gatewayHints(true), Description: desc,
			InputSchema: obj(map[string]any{
				"query": map[string]any{"type": "string", "description": "What you want to do, or a tool or server name."},
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 10, "description": "Maximum results."},
			}, "query")},
		{Name: ListTool, Title: "List tools", Annotations: gatewayHints(true),
			Description: "List every tool you can reach on this gateway, grouped by server, as names and one-line summaries.",
			InputSchema: obj(map[string]any{})},
		{Name: DefineTool, Title: "Get tool definitions", Annotations: gatewayHints(true),
			Description: "Return the full definitions of tools by exact name, as returned by " + SearchTool + " or " + ListTool + ".",
			InputSchema: obj(map[string]any{"names": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1}}, "names")},
		{Name: ExecuteTool, Title: "Execute a tool", Annotations: gatewayHints(false),
			Description: "Call a tool by exact name with its arguments.",
			InputSchema: obj(map[string]any{
				"name":      map[string]any{"type": "string", "description": "The tool's name, as returned by " + SearchTool + "."},
				"arguments": map[string]any{"type": "object", "description": "The tool's arguments."},
			}, "name")},
	}
}

// hookSearchCall runs one of search mode's tools. The engine implements
// them over the same exposed catalog as the flat listing.
func (g *Gateway) hookSearchCall(ctx context.Context, req mcp.Request, ld *loaded, route string, p *mcp.CallToolParamsRaw) (mcp.Result, error) {
	e, r := g.engine, ld.req
	own := func(out hooks.Outcome) (mcp.Result, error) {
		if out.Error == nil && out.Result != nil {
			text, isText := out.Result.(string)
			if !isText {
				b, _ := json.Marshal(out.Result)
				text = string(b)
			}
			res := map[string]any{"resultType": "complete", "content": []any{map[string]any{"type": "text", "text": text}}}
			if !isText {
				res["structuredContent"] = out.Result
			}
			out.Result = res
		}
		return g.done(ctx, out, nil)
	}
	switch p.Name {
	case SearchTool:
		var q struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		_ = json.Unmarshal(p.Arguments, &q)
		if strings.TrimSpace(q.Query) == "" {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "query is required"}
		}
		return own(e.ToolSearch(ctx, r, q.Query, q.Limit))
	case ListTool:
		return own(e.ListToolsYAML(ctx, r))
	case DefineTool:
		var q struct {
			Names []string `json:"names"`
		}
		_ = json.Unmarshal(p.Arguments, &q)
		if len(q.Names) == 0 {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "names is required"}
		}
		return own(e.ToolDefinitions(ctx, r, q.Names))
	case ExecuteTool:
		var q struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(p.Arguments, &q)
		q.Name = lowerSlug(route, q.Name)
		out := e.ExecuteTool(ctx, r, q.Name, q.Arguments, p)
		call := &mcp.CallToolParamsRaw{Name: q.Name, Arguments: q.Arguments, RequestState: p.RequestState, InputResponses: p.InputResponses}
		return g.relay(ctx, req, ld, out, metaOf(req), func(ctx context.Context, in iter.Seq2[hooks.UpstreamEvent, error]) iter.Seq2[hooks.ReplyEvent, error] {
			return e.ToolReplyStream(ctx, r, out.Call, call, in)
		})
	case hooks.InstructionsTool:
		var a struct {
			Slug string `json:"slug"`
		}
		_ = json.Unmarshal(p.Arguments, &a)
		return g.done(ctx, e.ServerInstructions(ctx, r, strings.ToLower(a.Slug)), nil)
	}
	return nil, &jsonrpc.Error{Code: hooks.CodeUnknownTool, Message: "unknown tool " + p.Name}
}

// ---- relay ----------------------------------------------------------------------

// relay finishes a request the engine answered itself, or sends its forward
// upstream and takes the reply back through the engine, event by event.
// Progress is relayed as it arrives; the final message ends the request.
func (g *Gateway) relay(ctx context.Context, req mcp.Request, ld *loaded, out hooks.Outcome, meta map[string]any,
	stream func(context.Context, iter.Seq2[hooks.UpstreamEvent, error]) iter.Seq2[hooks.ReplyEvent, error]) (mcp.Result, error) {
	if out.Forward == nil {
		return g.done(ctx, out, nil)
	}
	g.note(ctx, out)
	fwd := out.Forward
	x := ld.by[fwd.Slug]
	if x == nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "the request could not be routed"}
	}
	audit.SetTarget(ctx, x.srv.ID, x.srv.Slug)
	if fwd.Timeout != "" {
		if d, err := time.ParseDuration(fwd.Timeout); err == nil && d > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, d)
			defer cancel()
		}
	}
	// Progress is relayed only when the client asked for it and the server
	// may send it. The server is handed the gateway's own token, never the
	// client's.
	clientToken := meta["progressToken"]
	var upToken any
	if clientToken != nil {
		if sw, _ := g.engine.Config(ctx, ld.req, x.hs); sw["progress"] {
			upToken = "gw-" + uuid.NewString()
		}
	}
	events, err := g.send(ctx, x, fwd, upToken)
	if err != nil {
		// A credential that needs the operator's attention is said plainly;
		// it is the gateway's own state, not the upstream's reply.
		return nil, upstreamError(x.srv, err)
	}
	sess, _ := req.GetSession().(*mcp.ServerSession)
	for ev, err := range stream(ctx, events) {
		if err != nil {
			g.log.Error("reply stream failed", "slug", x.srv.Slug, "err", err)
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "the reply could not be processed"}
		}
		if ev.Final != nil {
			return g.done(ctx, *ev.Final, nil)
		}
		if sess == nil || upToken == nil || ev.Method != hooks.ProgressMethod {
			continue
		}
		var p struct {
			Token    any     `json:"progressToken"`
			Progress float64 `json:"progress"`
			Total    float64 `json:"total"`
			Message  string  `json:"message"`
		}
		if json.Unmarshal(ev.Params, &p) != nil || fmt.Sprint(p.Token) != fmt.Sprint(upToken) {
			continue // not under the token the gateway handed out
		}
		_ = sess.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: clientToken, Progress: p.Progress, Total: p.Total, Message: p.Message})
	}
	return nil, &jsonrpc.Error{Code: hooks.CodeUpstreamLost, Message: "The server's reply was lost before it completed. The request may or may not have run."}
}

// send forwards one request upstream, through an SDK client session, and
// returns the reply as events: the progress the server sent, in order, then
// its result. An error is returned only for a credential the operator has
// to fix. Every other failure is an event of the stream, so the engine ends
// the call with its one answer for a lost reply and keeps the cause for the
// record. That includes a reply the client library cannot read, such as a
// content block of a type it does not know.
func (g *Gateway) send(ctx context.Context, x *exposed, fwd *hooks.Forward, upToken any) (iter.Seq2[hooks.UpstreamEvent, error], error) {
	params, err := toMap(fwd.Params)
	if err != nil {
		return nil, err
	}
	// A server on the current version may be offered what the rules allow.
	// An older one gets no progress and no questions, whatever they say.
	current := x.init.ProtocolVersion == hooks.ProtocolCurrent
	if meta, ok := params["_meta"].(map[string]any); ok {
		// What is left of the client's _meta here is what a server.session
		// rule chose to forward. Only keys on the input list go on. The
		// client's progress token and the protocol's own keys never do.
		kept := map[string]any{}
		for _, k := range g.metaIn {
			if v, ok := meta[k]; ok && k != "progressToken" && !strings.HasPrefix(k, "io.modelcontextprotocol/") {
				kept[k] = v
			}
		}
		if len(kept) == 0 {
			delete(params, "_meta")
		} else {
			params["_meta"] = kept
		}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	type item struct {
		ev  hooks.UpstreamEvent
		err error
	}
	// Progress arrives on the client library's own goroutine while the call
	// is in flight. The channel puts it in line ahead of the result.
	ch := make(chan item, 256)
	gone := make(chan struct{})
	opts := upstream.SessionOptions{Capabilities: &mcp.ClientCapabilities{}, PassQuestions: true}
	if c := fwd.Connect; c != nil {
		if name, _ := c.ClientInfo["name"].(string); name != "" {
			version, _ := c.ClientInfo["version"].(string)
			opts.Identity = &mcp.Implementation{Name: name, Version: version}
		}
		if _, ok := c.Capabilities["elicitation"]; ok && current {
			opts.Capabilities.Elicitation = &mcp.ElicitationCapabilities{}
		}
	}
	if current && upToken != nil {
		opts.OnProgress = func(p *mcp.ProgressNotificationParams) {
			b, err := json.Marshal(p)
			if err != nil {
				return
			}
			select {
			case ch <- item{ev: hooks.UpstreamEvent{Method: hooks.ProgressMethod, Params: b}}:
			case <-gone:
			}
		}
	}
	sess, err := g.conn.OpenFor(ctx, x.srv, x.init.ProtocolVersion, opts)
	if err != nil {
		if errors.Is(err, upstream.ErrCredentialBroken) || errors.Is(err, upstream.ErrCredentialPending) {
			return nil, err
		}
		cause := err
		return func(yield func(hooks.UpstreamEvent, error) bool) { yield(hooks.UpstreamEvent{}, cause) }, nil
	}
	token, _ := upToken.(string)
	var closing sync.Once
	closeSession := func() { closing.Do(func() { _ = sess.Close() }) }
	go func() {
		res, err := callUpstream(sess, fwd.Method, raw, token, current)
		// The client library hands progress to its handler on a queue of
		// its own, so the call can return before the last notifications
		// have been handled. Closing the session waits for that queue. Only
		// then is the result put in line, behind every progress event.
		closeSession()
		var final item
		switch {
		case err != nil:
			var je *jsonrpc.Error
			if errors.As(err, &je) && !localFailure(je) {
				// The server answered with an error of its own.
				final.ev = hooks.UpstreamEvent{Error: &hooks.RPCError{Code: je.Code, Message: je.Message}}
			} else {
				final.err = err
			}
		default:
			if b, merr := json.Marshal(res); merr != nil {
				final.err = merr
			} else {
				final.ev = hooks.UpstreamEvent{Result: keepMeta(b, g.metaOut)}
			}
		}
		select {
		case ch <- final:
		case <-gone:
		}
	}()
	return func(yield func(hooks.UpstreamEvent, error) bool) {
		// Closing the session ends a call that is still in flight, which is
		// how a client that went away, or a timeout, reaches the server.
		defer closeSession()
		defer close(gone)
		for {
			select {
			case it := <-ch:
				if it.err != nil {
					yield(hooks.UpstreamEvent{}, it.err)
					return
				}
				if !yield(it.ev, nil) || it.ev.Method == "" {
					return
				}
			case <-ctx.Done():
				yield(hooks.UpstreamEvent{}, ctx.Err())
				return
			}
		}
	}, nil
}

// localFailure reports whether an error that looks like a JSON-RPC error
// was made by the client library itself, for a failure of the transport,
// and not sent by the server. The library reports a refused connection or
// an HTTP error this way. Such a failure is a lost reply, not an answer.
func localFailure(e *jsonrpc.Error) bool {
	switch e.Code {
	case -32005: // rejected by transport
		return true
	case -32003, -32004: // client is closing, server is closing
		return strings.Contains(e.Message, "closing")
	}
	return false
}

// callUpstream makes the forwarded call on a session. The forward's params
// are the engine's; they are decoded into the client library's own types,
// with the gateway's progress token when the server may send progress.
func callUpstream(sess *upstream.Session, method string, raw json.RawMessage, token string, current bool) (any, error) {
	switch method {
	case "tools/call":
		var p struct {
			Name           string               `json:"name"`
			Arguments      json.RawMessage      `json:"arguments"`
			Meta           mcp.Meta             `json:"_meta"`
			InputResponses mcp.InputResponseMap `json:"inputResponses"`
			RequestState   string               `json:"requestState"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		cp := &mcp.CallToolParams{Name: p.Name, Meta: p.Meta}
		if len(p.Arguments) > 0 {
			cp.Arguments = p.Arguments
		}
		if current {
			cp.InputResponses, cp.RequestState = p.InputResponses, p.RequestState
			if token != "" {
				cp.SetProgressToken(token)
			}
		}
		return sess.CallTool(sess.Ctx, cp)
	case "prompts/get":
		var p mcp.GetPromptParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if !current {
			p.InputResponses, p.RequestState = nil, ""
		} else if token != "" {
			p.SetProgressToken(token)
		}
		return sess.GetPrompt(sess.Ctx, &p)
	case "resources/read":
		var p mcp.ReadResourceParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if !current {
			p.InputResponses, p.RequestState = nil, ""
		} else if token != "" {
			p.SetProgressToken(token)
		}
		return sess.ReadResource(sess.Ctx, &p)
	}
	return nil, fmt.Errorf("%s cannot be forwarded", method)
}

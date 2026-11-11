// Package gateway is the front door: an MCP server (go-sdk, 2026-07-28 only)
// that lists and calls tools on the caller's registered servers. /mcp
// multiplexes every server with slug__tool names; /{slug}/mcp is 1:1.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sync/singleflight"

	"github.com/andyjmorgan/Bifrost/internal/audit"
	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/cache"
	"github.com/andyjmorgan/Bifrost/internal/policy"
	"github.com/andyjmorgan/Bifrost/internal/store"
	"github.com/andyjmorgan/Bifrost/internal/upstream"
)

// ProtocolVersion is the only version the front door accepts.
const ProtocolVersion = "2026-07-28"

// Separator joins slug and tool name on the mux route.
const Separator = "__"

// Store is the slice of persistence the gateway reads.
type Store interface {
	ListServers(ctx context.Context, owner string) ([]*store.Server, error)
	GetServerBySlug(ctx context.Context, owner, slug string) (*store.Server, error)
	ListServersWithCache(ctx context.Context, owner string, ns ...string) ([]*store.Server, map[uuid.UUID]store.CacheBundle, error)
	GetServerBySlugWithCache(ctx context.Context, owner, slug string, ns ...string) (*store.Server, store.CacheBundle, error)
	CacheGet(ctx context.Context, ns, key string) (json.RawMessage, error)
	CacheSet(ctx context.Context, ns, key string, serverID *uuid.UUID, ifUpdatedAt time.Time, value json.RawMessage, ttl time.Duration) error
	ListBindings(ctx context.Context, owner string) ([]store.Binding, error)
}

// Gateway serves the two MCP routes.
type Gateway struct {
	store    Store
	conn     *upstream.Connector
	rec      *audit.Recorder
	cache    *cache.Writer
	toolsTTL time.Duration
	log      *slog.Logger
	version  string
	// flights collapses concurrent tools/list refills for the same server
	// into one upstream fetch per process. Across replicas the worst case is
	// one fetch per replica per cold key; writes are idempotent and guarded
	// by the server's updated_at, so no cross-process lock is needed.
	flights singleflight.Group
	// policies compiles each bound policy once per version.
	policies policy.Cache
}

// MetaKeyUnavailable carries, on a mux tools/list result, the servers that
// could not be listed: [{slug, name, error}].
const MetaKeyUnavailable = "ai.andrewmorgan.bifrost/unavailable"

// New builds a gateway.
func New(s Store, conn *upstream.Connector, rec *audit.Recorder, cw *cache.Writer, toolsTTL time.Duration, log *slog.Logger, version string) *Gateway {
	return &Gateway{store: s, conn: conn, rec: rec, cache: cw, toolsTTL: toolsTTL, log: log, version: version}
}

type routeKey struct{}

// MuxHandler serves /mcp.
func (g *Gateway) MuxHandler() http.Handler { return g.handler("mux") }

// DirectHandler serves /{slug}/mcp; the slug is read from the chi route.
func (g *Gateway) DirectHandler() http.Handler { return g.handler("direct") }

func (g *Gateway) handler(route string) http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		srv := mcp.NewServer(&mcp.Implementation{Name: "bifrost", Version: g.version}, &mcp.ServerOptions{
			SupportedProtocolVersions: []string{ProtocolVersion},
			Logger:                    g.log,
			// Advertise the surfaces the middleware forwards; nothing is
			// registered on this server itself.
			HasTools:     true,
			HasPrompts:   true,
			HasResources: true,
		})
		srv.AddReceivingMiddleware(g.middleware(route, strings.ToLower(chi.URLParam(r, "slug"))))
		return srv
	}, &mcp.StreamableHTTPOptions{Stateless: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if route == "direct" {
			// Resolve early so the inbound audit event carries the server even
			// when the request fails before reaching a method handler.
			caller := auth.CallerFrom(r.Context())
			srv, err := g.store.GetServerBySlug(r.Context(), caller.OwnerID, strings.ToLower(chi.URLParam(r, "slug")))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			audit.SetTarget(r.Context(), srv.ID, srv.Slug)
		}
		ctx := context.WithValue(r.Context(), routeKey{}, route)
		if r.URL.Query().Get("search") == "true" {
			ctx = withSearchMode(ctx)
		}
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (g *Gateway) middleware(route, slug string) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			caller := auth.CallerFrom(ctx)
			if caller == nil {
				return nil, &jsonrpc.Error{Code: -32001, Message: "unauthenticated"}
			}
			switch method {
			case "tools/list":
				if searchMode(ctx) {
					return g.searchToolsList(ctx, caller, route, slug)
				}
				return g.listTools(ctx, caller, route, slug)
			case "tools/call":
				// Server-side params arrive raw: arguments stay as JSON bytes and
				// are forwarded untouched.
				p, ok := req.GetParams().(*mcp.CallToolParamsRaw)
				if !ok {
					return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid tools/call params"}
				}
				sess := req.GetSession().(*mcp.ServerSession)
				ctx = withSession(ctx, sess)
				if tok, ok := p.Meta["progressToken"]; ok && tok != nil {
					ctx = withProgress(ctx, func(done, total float64, msg string) {
						_ = sess.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: tok, Progress: done, Total: total, Message: msg})
					})
				}
				if searchMode(ctx) && isSearchTool(p.Name) {
					return g.searchCall(ctx, caller, route, slug, p)
				}
				return g.callTool(ctx, caller, route, slug, p)
			case "prompts/list":
				return g.listPrompts(ctx, caller, route, slug)
			case "prompts/get":
				p, ok := req.GetParams().(*mcp.GetPromptParams)
				if !ok {
					return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid prompts/get params"}
				}
				return g.getPrompt(ctx, caller, route, slug, p)
			case "resources/list":
				return g.listResources(ctx, caller, route, slug)
			case "resources/templates/list":
				return g.listResourceTemplates(ctx, caller, route, slug)
			case "resources/read":
				p, ok := req.GetParams().(*mcp.ReadResourceParams)
				if !ok {
					return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid resources/read params"}
				}
				return g.readResource(ctx, caller, route, slug, p)
			}
			return next(ctx, method, req)
		}
	}
}

// ---- tools/list -------------------------------------------------------------

func (g *Gateway) listTools(ctx context.Context, caller *auth.Caller, route, slug string) (mcp.Result, error) {
	if route == "direct" {
		entries, err := g.catalog(ctx, caller, route, slug, false, nil)
		if err != nil {
			return nil, err
		}
		e := entries[0]
		if e.err != nil {
			return nil, upstreamError(e.srv, e.err)
		}
		return &mcp.ListToolsResult{Tools: e.tools}, nil
	}
	return g.muxTools(ctx, caller, route, false)
}

// RefreshTool is the gateway's own tool on the mux route: re-read every
// server's tools, prompts and resources bypassing the cache, refresh it,
// and report per server.
const RefreshTool = "refresh_services"

func refreshToolDef() *mcp.Tool {
	f, t := false, true
	return &mcp.Tool{
		Name:        RefreshTool,
		Title:       "Refresh services",
		Description: "Re-fetch every server's tools, prompts, resources and resource templates, bypassing and refreshing the gateway cache. Returns a per-server summary; list afterwards for the fresh definitions.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"refreshed":   map[string]any{"type": "integer", "description": "Servers refreshed successfully."},
				"unavailable": map[string]any{"type": "integer", "description": "Servers that could not be reached; their cached entries are kept."},
				"tools":       map[string]any{"type": "integer"},
				"prompts":     map[string]any{"type": "integer"},
				"resources":   map[string]any{"type": "integer"},
				"templates":   map[string]any{"type": "integer"},
				"servers": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"server":    map[string]any{"type": "string"},
							"tools":     map[string]any{"type": "integer"},
							"prompts":   map[string]any{"type": "integer"},
							"resources": map[string]any{"type": "integer"},
							"templates": map[string]any{"type": "integer"},
							"error":     map[string]any{"type": "string"},
						},
						"required": []string{"server", "tools", "prompts", "resources", "templates"},
					},
				},
			},
			"required": []string{"refreshed", "unavailable", "tools", "prompts", "resources", "templates", "servers"},
		},
		// Rewrites the gateway's own cache, nothing beyond it: not read-only,
		// not destructive, safe to repeat, closed world.
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &f, IdempotentHint: t, OpenWorldHint: &f},
	}
}

// refreshServices is the refresh_services tool: every surface of every
// server re-fetched (tools first, with progress; then prompts, resources
// and templates concurrently) and a per-server summary. Definitions come
// from the list methods afterwards.
func (g *Gateway) refreshServices(ctx context.Context, caller *auth.Caller, route string) (mcp.Result, error) {
	entries, err := g.catalog(ctx, caller, route, "", true, nil)
	if err != nil {
		return nil, err
	}
	type surface struct {
		ns    string
		fetch func(*upstream.Session) (json.RawMessage, error)
		got   []listing
		err   error
	}
	surfaces := []*surface{
		{ns: cache.NSPrompts, fetch: fetchPrompts},
		{ns: cache.NSResources, fetch: fetchResources},
		{ns: cache.NSTemplates, fetch: fetchTemplates},
	}
	var wg sync.WaitGroup
	for _, sf := range surfaces {
		wg.Add(1)
		go func(sf *surface) {
			defer wg.Done()
			sf.got, sf.err = g.listings(ctx, caller, route, "", sf.ns, true, sf.fetch)
		}(sf)
	}
	wg.Wait()
	type line struct {
		Server    string `json:"server"`
		Tools     int    `json:"tools"`
		Prompts   int    `json:"prompts"`
		Resources int    `json:"resources"`
		Templates int    `json:"templates"`
		Error     string `json:"error,omitempty"`
	}
	count := func(sf *surface, slug string) int {
		for _, l := range sf.got {
			if l.srv.Slug == slug && l.err == nil {
				var items []json.RawMessage
				_ = json.Unmarshal(l.raw, &items)
				return len(items)
			}
		}
		return 0
	}
	var (
		lines  []line
		totals line
		bad    int
	)
	for _, e := range entries {
		l := line{Server: e.srv.Slug, Tools: len(e.tools)}
		if e.err != nil {
			l.Error = oneLine(e.err.Error(), 200)
			bad++
		} else {
			l.Prompts, l.Resources, l.Templates = count(surfaces[0], e.srv.Slug), count(surfaces[1], e.srv.Slug), count(surfaces[2], e.srv.Slug)
			totals.Tools += l.Tools
			totals.Prompts += l.Prompts
			totals.Resources += l.Resources
			totals.Templates += l.Templates
		}
		lines = append(lines, l)
	}
	if lines == nil {
		lines = []line{}
	}
	return structured(map[string]any{
		"refreshed":   len(entries) - bad,
		"unavailable": bad,
		"tools":       totals.Tools,
		"prompts":     totals.Prompts,
		"resources":   totals.Resources,
		"templates":   totals.Templates,
		"servers":     lines,
	}, false), nil
}

// muxTools lists tools across all enabled servers. With force, cached
// entries are ignored and every server is asked afresh.
func (g *Gateway) muxTools(ctx context.Context, caller *auth.Caller, route string, force bool) (*mcp.ListToolsResult, error) {
	entries, err := g.catalog(ctx, caller, route, "", force, nil)
	if err != nil {
		return nil, err
	}
	out := &mcp.ListToolsResult{Tools: []*mcp.Tool{refreshToolDef()}}
	for _, e := range entries {
		if e.err != nil {
			continue
		}
		for _, t := range e.tools {
			// Copy: cached tool objects must not be mutated with the prefix.
			c := *t
			c.Name = e.srv.Slug + Separator + t.Name
			out.Tools = append(out.Tools, &c)
		}
	}
	if u := unavailableOf(entries); len(u) > 0 {
		out.Meta = mcp.Meta{MetaKeyUnavailable: u}
	}
	return out, nil
}

// toolsOf opens a session and pages through its tools.
// decodeTools turns a cached tools/list back into tools; ok is false on a miss.
func decodeTools(raw json.RawMessage) ([]*mcp.Tool, bool) {
	if raw == nil {
		return nil, false
	}
	var tools []*mcp.Tool
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, false
	}
	if tools == nil {
		tools = []*mcp.Tool{}
	}
	return tools, true
}

// canonicalToolName returns the cached tool whose name matches ignoring case,
// or name unchanged when there is no such tool (or an exact match exists).
func canonicalToolName(tools []*mcp.Tool, name string) string {
	for _, t := range tools {
		if t.Name == name {
			return name
		}
	}
	for _, t := range tools {
		if strings.EqualFold(t.Name, name) {
			return t.Name
		}
	}
	return name
}

// initHint returns the cached negotiated protocol version, or "" (probe latest).
func initHint(b store.CacheBundle) string {
	var in cache.Init
	if raw := b[cache.NSInit]; raw != nil && json.Unmarshal(raw, &in) == nil {
		return in.ProtocolVersion
	}
	return ""
}

// plural renders "1 tool" / "3 tools".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// fetchTools fills a server's tools/list. Concurrent misses for the same
// server in this process share one upstream fetch (singleflight); the leader
// fetches on a context detached from its own request so a client that gives
// up does not fail the followers, then writes the row synchronously. Nothing
// is held on a database connection while the upstream is in flight.
//
// steps is how many progress stages this caller reported through stage (0
// when it joined another caller's fetch), so fan-outs can keep their counters
// honest.
func (g *Gateway) fetchTools(ctx context.Context, srv *store.Server, protocolVersion string, force bool, stage func(string)) (tools []*mcp.Tool, steps int, err error) {
	key := srv.ID.String()
	if force {
		key += "!" // a forced refill must not be answered by a leader that re-read the cache
	}
	counted := func(msg string) {
		steps++
		if stage != nil {
			stage(msg)
		}
	}
	ch := g.flights.DoChan(key, func() (any, error) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), g.conn.Timeout())
		defer cancel()
		return g.refill(fctx, srv, protocolVersion, force, counted)
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			return nil, steps, r.Err
		}
		return r.Val.([]*mcp.Tool), steps, nil
	case <-ctx.Done():
		return nil, steps, ctx.Err()
	}
}

// refill is the leader's half of fetchTools: re-check the cache (unless
// forced), list upstream, write the row if the server has not changed since.
func (g *Gateway) refill(ctx context.Context, srv *store.Server, protocolVersion string, force bool, stage func(string)) ([]*mcp.Tool, error) {
	if !force {
		if raw, err := g.store.CacheGet(ctx, cache.NSTools, srv.ID.String()); err == nil {
			if cached, ok := decodeTools(raw); ok {
				return cached, nil // another process filled it meanwhile
			}
		}
	}
	tools, err := g.toolsOf(ctx, srv, protocolVersion, stage)
	if err != nil {
		return nil, err
	}
	if g.toolsTTL > 0 {
		if b, err := json.Marshal(tools); err == nil {
			id := srv.ID
			switch err := g.store.CacheSet(ctx, cache.NSTools, id.String(), &id, srv.UpdatedAt, b, g.toolsTTL); {
			case errors.Is(err, store.ErrStale):
				g.log.Info("tools cache write skipped: server changed during fetch", "slug", srv.Slug)
			case err != nil:
				g.log.Warn("cache write failed", "slug", srv.Slug, "err", err)
			}
		}
	}
	return tools, nil
}

// RefreshServer lists a server's tools now, bypassing and rewriting the
// cache: the admin paths (create, edit, credential change, OAuth consent)
// call it so the row reflects the current configuration, and a failure
// leaves whatever row was there in place.
func (g *Gateway) RefreshServer(ctx context.Context, srv *store.Server) ([]*mcp.Tool, error) {
	hint := ""
	if raw, err := g.store.CacheGet(ctx, cache.NSInit, srv.ID.String()); err == nil {
		var in cache.Init
		if json.Unmarshal(raw, &in) == nil {
			hint = in.ProtocolVersion
		}
	}
	tools, _, err := g.fetchTools(ctx, srv, hint, true, nil)
	return tools, err
}

func (g *Gateway) toolsOf(ctx context.Context, srv *store.Server, protocolVersion string, stage ...func(string)) ([]*mcp.Tool, error) {
	report := func(string) {}
	if len(stage) > 0 && stage[0] != nil {
		report = stage[0]
	}
	if protocolVersion != "" {
		report("connecting (" + protocolVersion + ")")
	} else {
		report("connecting (discovering protocol version)")
	}
	sess, err := g.conn.OpenWith(ctx, srv, protocolVersion)
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	if ir := sess.InitializeResult(); ir != nil && ir.ServerInfo != nil {
		report(fmt.Sprintf("connected to %s %s (%s), listing tools", ir.ServerInfo.Name, ir.ServerInfo.Version, ir.ProtocolVersion))
	} else {
		report("connected, listing tools")
	}
	var tools []*mcp.Tool
	cursor := ""
	for {
		res, err := sess.ListTools(sess.Ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		tools = append(tools, res.Tools...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	if tools == nil {
		tools = []*mcp.Tool{}
	}
	return tools, nil
}

// ---- tools/call -------------------------------------------------------------

func (g *Gateway) callTool(ctx context.Context, caller *auth.Caller, route, slug string, p *mcp.CallToolParamsRaw) (mcp.Result, error) {
	name := p.Name
	if route == "mux" && p.Name == RefreshTool {
		return g.refreshServices(ctx, caller, route)
	}
	if route == "mux" {
		var ok bool
		slug, name, ok = strings.Cut(p.Name, Separator)
		if !ok || slug == "" || name == "" {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("tool name must be <slug>%s<tool>", Separator)}
		}
	}
	// Slugs are lowercase by construction; models are not always.
	slug = strings.ToLower(slug)
	srv, cached, err := g.store.GetServerBySlugWithCache(ctx, caller.OwnerID, slug, cache.NSInit, cache.NSTools)
	if err != nil {
		return nil, &jsonrpc.Error{Code: -32004, Message: "unknown server " + slug}
	}
	if !srv.Enabled {
		return nil, &jsonrpc.Error{Code: -32004, Message: "server " + slug + " is disabled"}
	}
	b, err := g.loadPolicies(ctx, caller.OwnerID)
	if err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "policies: " + err.Error()}
	}
	if len(b.forRoute(route, srv.ID)) > 0 {
		// Policies decide whether the tool exists on this route, whether it
		// needs confirming, and which upstream name it maps to. They need the
		// tool list, so a cold cache costs one listing here.
		tools, ok := decodeTools(cached[cache.NSTools])
		if !ok {
			if tools, _, err = g.fetchTools(ctx, srv, initHint(cached), false, nil); err != nil {
				return nil, upstreamError(srv, err)
			}
		}
		var in cache.Init
		if raw := cached[cache.NSInit]; raw != nil {
			_ = json.Unmarshal(raw, &in)
		}
		d := findDecision(shape(ctx, b, route, srv, in, tools), name)
		if d == nil || !d.Visible {
			return nil, &jsonrpc.Error{Code: -32004, Message: "unknown tool " + p.Name}
		}
		if d.Confirm {
			if res, err := g.confirm(ctx, p, d, srv); err != nil || res != nil {
				return res, err
			}
		}
		name = d.Upstream
	} else if tools, ok := decodeTools(cached[cache.NSTools]); ok {
		// Tool names are passed through verbatim, except that a case-only mismatch
		// against the cached list is corrected (upstreams are case-sensitive).
		name = canonicalToolName(tools, name)
	}
	audit.SetTarget(ctx, srv.ID, srv.Slug)
	sess, err := g.conn.OpenWith(ctx, srv, initHint(cached))
	if err != nil {
		return nil, upstreamError(srv, err)
	}
	defer sess.Close()
	var args any
	if len(p.Arguments) > 0 {
		args = json.RawMessage(p.Arguments)
	}
	// Forward client metadata (progress tokens and the like) but never the
	// inbound protocol marker: the upstream session negotiated its own version.
	var meta mcp.Meta
	for k, v := range p.Meta {
		if k == mcp.MetaKeyProtocolVersion {
			continue
		}
		if meta == nil {
			meta = mcp.Meta{}
		}
		meta[k] = v
	}
	res, err := sess.CallTool(sess.Ctx, &mcp.CallToolParams{Name: name, Arguments: args, Meta: meta})
	if err != nil {
		return nil, upstreamError(srv, err)
	}
	return res, nil
}

func upstreamError(srv *store.Server, err error) error {
	var je *jsonrpc.Error
	if errors.As(err, &je) {
		return je
	}
	code := int64(-32010)
	switch {
	case errors.Is(err, upstream.ErrCredentialBroken):
		code = -32011
	case errors.Is(err, upstream.ErrCredentialPending):
		code = -32012
	}
	return &jsonrpc.Error{Code: code, Message: fmt.Sprintf("upstream %s: %v", srv.Slug, err)}
}

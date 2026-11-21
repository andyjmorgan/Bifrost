// Package gateway is the front door: an MCP server (go-sdk, 2026-07-28 only)
// in front of the servers granted to the caller. /mcp multiplexes every one
// with slug__name names; /{slug}/mcp is 1:1.
//
// The gateway does transport, credentials, caching and each server's allow
// lists. Every listing, call and reply then goes through the rules
// engine (pkg/hooks), which decides what a caller sees and may do. hooks.go
// is where the two meet.
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
	"github.com/andyjmorgan/Bifrost/internal/store"
	"github.com/andyjmorgan/Bifrost/internal/upstream"
	"github.com/andyjmorgan/Bifrost/pkg/hooks"
)

// ProtocolVersion is the only version the front door accepts.
const ProtocolVersion = "2026-07-28"

// Separator joins slug and tool name on the mux route.
const Separator = "__"

// Store is the slice of persistence the gateway reads.
type Store interface {
	GetGrantedBySlug(ctx context.Context, userID, slug string) (*store.Server, error)
	ListGrantedWithCache(ctx context.Context, userID string, ns ...string) ([]*store.Server, map[uuid.UUID]store.CacheBundle, error)
	GetGrantedBySlugWithCache(ctx context.Context, userID, slug string, ns ...string) (*store.Server, store.CacheBundle, error)
	ObserveCapabilities(ctx context.Context, serverID uuid.UUID, kind string, items map[string]store.Seen) error
	CacheGet(ctx context.Context, ns, key string) (json.RawMessage, error)
	CacheSet(ctx context.Context, ns, key string, serverID *uuid.UUID, ifUpdatedAt time.Time, value json.RawMessage, ttl time.Duration) error
	// CacheTake returns a live entry and removes it in one step, or
	// ErrNotFound. It is how a confirmation is redeemed exactly once.
	CacheTake(ctx context.Context, ns, key string) (json.RawMessage, error)
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
	// rules caches each server's compiled rules by generation.
	rules sync.Map
	// engine is the rules engine: every listing, call and reply goes
	// through it. See hooks.go.
	engine *hooks.Engine
	// metaIn and metaOut are the _meta keys allowed through to servers and
	// back to clients; see SetMetaAllow.
	metaIn, metaOut []string
	// ruleHeaders are the HTTP request headers rules may read; see
	// SetRuleHeaders.
	ruleHeaders []string
}

// MetaKeyUnavailable carries, on a mux tools/list result, the servers that
// could not be listed: [{slug, name, error}].
const MetaKeyUnavailable = "ai.andrewmorgan.bifrost/unavailable"

// New builds a gateway.
func New(s Store, conn *upstream.Connector, rec *audit.Recorder, cw *cache.Writer, toolsTTL time.Duration, log *slog.Logger, version string) *Gateway {
	g := &Gateway{store: s, conn: conn, rec: rec, cache: cw, toolsTTL: toolsTTL, log: log, version: version}
	g.engine = newEngine(g, version)
	return g
}

type routeKey struct{}

type headersKey struct{}

// credentialHeader reports a header that carries a credential, by what its
// name says. Such a header is never shown to a rule, even if an operator
// puts it on the allow-list: authorization, cookie, x-api-key, x-auth-token,
// x-amz-security-token and the like.
func credentialHeader(name string) bool {
	for _, part := range []string{"auth", "cookie", "token", "secret", "password", "credential", "signature", "api-key", "apikey", "session"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	return false
}

// SetRuleHeaders sets the HTTP request headers rules may read, as
// request.headers. The list starts empty: a rule sees no header unless the
// operator names it. A header that carries a credential is never shown.
func (g *Gateway) SetRuleHeaders(names []string) {
	g.ruleHeaders = nil
	for _, n := range names {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" && !credentialHeader(n) {
			g.ruleHeaders = append(g.ruleHeaders, n)
		}
	}
}

// headersFor picks the allow-listed headers out of a request, by lower-case
// name. A header sent more than once is joined with ", ", as HTTP reads it.
func (g *Gateway) headersFor(h http.Header) map[string]string {
	out := map[string]string{}
	for _, n := range g.ruleHeaders {
		if vs := h.Values(n); len(vs) > 0 {
			out[n] = strings.Join(vs, ", ")
		}
	}
	return out
}

func ruleHeaders(ctx context.Context) map[string]string {
	h, _ := ctx.Value(headersKey{}).(map[string]string)
	return h
}

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
		if route == "direct" {
			// Extensions are passed through on the direct route only.
			addTaskMethods(srv)
			addSkillMethods(srv)
		}
		srv.AddReceivingMiddleware(g.middleware(route, strings.ToLower(chi.URLParam(r, "slug"))))
		return srv
	}, &mcp.StreamableHTTPOptions{Stateless: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if route == "direct" {
			// Resolve early so the inbound audit event carries the server even
			// when the request fails before reaching a method handler.
			caller := auth.CallerFrom(r.Context())
			srv, err := g.store.GetGrantedBySlug(r.Context(), caller.UserID, strings.ToLower(chi.URLParam(r, "slug")))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			audit.SetTarget(r.Context(), srv.ID, srv.Slug)
		}
		ctx := context.WithValue(r.Context(), routeKey{}, route)
		ctx = context.WithValue(ctx, headersKey{}, g.headersFor(r.Header))
		if r.URL.Query().Get("search") == "true" {
			ctx = withSearchMode(ctx)
		}
		if route == "mux" {
			ctx = withSlugs(ctx, r.URL.Query().Get("slugs"))
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
			// Every method the gateway serves goes through the rules engine.
			switch method {
			case "server/discover":
				return g.hookDiscover(ctx, caller, route, slug, metaOf(req))
			case "tools/list", "prompts/list", "resources/list", "resources/templates/list":
				return g.hookList(ctx, caller, route, slug, method, metaOf(req))
			case "tools/call":
				// Server-side params arrive raw: arguments stay as JSON bytes.
				p, ok := req.GetParams().(*mcp.CallToolParamsRaw)
				if !ok {
					return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid tools/call params"}
				}
				if sess, ok := req.GetSession().(*mcp.ServerSession); ok {
					if tok, ok := p.Meta["progressToken"]; ok && tok != nil {
						// The gateway's own work (refreshing catalogs) reports
						// progress under the client's token.
						ctx = withProgress(ctx, func(done, total float64, msg string) {
							_ = sess.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: tok, Progress: done, Total: total, Message: msg})
						})
					}
				}
				return g.hookCall(ctx, caller, route, slug, req, p)
			case "prompts/get":
				p, ok := req.GetParams().(*mcp.GetPromptParams)
				if !ok {
					return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid prompts/get params"}
				}
				return g.hookPrompt(ctx, caller, route, slug, req, p)
			case "skills/list", "skills/get":
				if route != "direct" {
					break
				}
				return g.hookSkills(ctx, caller, route, slug, method, req)
			case "tasks/get", "tasks/update", "tasks/cancel":
				if route != "direct" {
					break
				}
				return g.hookTask(ctx, caller, route, slug, method, req)
			case "resources/read":
				p, ok := req.GetParams().(*mcp.ReadResourceParams)
				if !ok {
					return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid resources/read params"}
				}
				return g.hookRead(ctx, caller, route, slug, req, p)
			}
			return next(ctx, method, req)
		}
	}
}

// ---- tools/list -------------------------------------------------------------

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
	entries, err := g.catalog(ctx, caller, route, "", true)
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
	if err == nil {
		g.observeTools(ctx, srv, tools)
	}
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

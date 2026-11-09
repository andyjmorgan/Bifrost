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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Mcp-Bifrost/internal/audit"
	"github.com/andyjmorgan/Mcp-Bifrost/internal/auth"
	"github.com/andyjmorgan/Mcp-Bifrost/internal/cache"
	"github.com/andyjmorgan/Mcp-Bifrost/internal/store"
	"github.com/andyjmorgan/Mcp-Bifrost/internal/upstream"
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
	CacheSet(ctx context.Context, ns, key string, serverID *uuid.UUID, value json.RawMessage, ttl time.Duration) error
	WithAdvisoryLock(ctx context.Context, key string, fn func(ctx context.Context) error) error
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
}

// MetaKeyUnavailable carries, on a mux tools/list result, the servers that
// could not be listed: [{slug, name, error}].
const MetaKeyUnavailable = "ai.andrewmorgan.mcp-bifrost/unavailable"

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
		srv := mcp.NewServer(&mcp.Implementation{Name: "mcp-bifrost", Version: g.version}, &mcp.ServerOptions{
			SupportedProtocolVersions: []string{ProtocolVersion},
			Logger:                    g.log,
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
				if tok, ok := p.Meta["progressToken"]; ok && tok != nil {
					sess := req.GetSession().(*mcp.ServerSession)
					ctx = withProgress(ctx, func(done, total float64, msg string) {
						_ = sess.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: tok, Progress: done, Total: total, Message: msg})
					})
				}
				if searchMode(ctx) && isSearchTool(p.Name) {
					return g.searchCall(ctx, caller, route, slug, p)
				}
				return g.callTool(ctx, caller, route, slug, p)
			}
			return next(ctx, method, req)
		}
	}
}

// ---- tools/list -------------------------------------------------------------

func (g *Gateway) listTools(ctx context.Context, caller *auth.Caller, route, slug string) (mcp.Result, error) {
	if route == "direct" {
		srv, cached, err := g.store.GetServerBySlugWithCache(ctx, caller.OwnerID, slug, cache.NSTools, cache.NSInit)
		if err != nil {
			return nil, &jsonrpc.Error{Code: -32004, Message: "unknown server"}
		}
		if tools, ok := decodeTools(cached[cache.NSTools]); ok {
			return &mcp.ListToolsResult{Tools: tools}, nil
		}
		tools, err := g.fetchTools(ctx, srv, initHint(cached), false, nil)
		if err != nil {
			return nil, upstreamError(srv, err)
		}
		return &mcp.ListToolsResult{Tools: tools}, nil
	}
	return g.muxTools(ctx, caller, route, false)
}

// RefreshTool is the gateway's own tool on the mux route: re-list every
// server bypassing the cache, refresh it, and return the fresh list.
const RefreshTool = "refresh_tools"

func refreshToolDef() *mcp.Tool {
	return &mcp.Tool{
		Name:        RefreshTool,
		Description: "Re-fetch every server's tools, bypassing and refreshing the gateway cache. Returns a per-server summary; call tools/list afterwards for the fresh definitions.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
	}
}

// muxTools lists tools across all enabled servers. With force, cached
// entries are ignored and every server is asked afresh.
func (g *Gateway) muxTools(ctx context.Context, caller *auth.Caller, route string, force bool) (*mcp.ListToolsResult, error) {
	entries, err := g.catalog(ctx, caller, route, "", force)
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

// fetchTools fills a server's tools/list under a Postgres advisory lock so
// concurrent misses, in this process or any other replica, cost one upstream
// connection: the lock holder re-checks the cache (unless forced), fetches,
// writes the row synchronously and releases; waiters then find the row.
func (g *Gateway) fetchTools(ctx context.Context, srv *store.Server, protocolVersion string, force bool, stage func(string)) ([]*mcp.Tool, error) {
	var tools []*mcp.Tool
	err := g.store.WithAdvisoryLock(ctx, "tools:"+srv.ID.String(), func(ctx context.Context) error {
		if !force {
			if raw, err := g.store.CacheGet(ctx, cache.NSTools, srv.ID.String()); err == nil {
				if cached, ok := decodeTools(raw); ok {
					tools = cached // another caller filled it while we waited
					return nil
				}
			}
		}
		var err error
		if tools, err = g.toolsOf(ctx, srv, protocolVersion, stage); err != nil {
			return err
		}
		if g.toolsTTL > 0 {
			if b, err := json.Marshal(tools); err == nil {
				id := srv.ID
				if err := g.store.CacheSet(ctx, cache.NSTools, id.String(), &id, b, g.toolsTTL); err != nil {
					g.log.Warn("cache write failed", "slug", srv.Slug, "err", err)
				}
			}
		}
		return nil
	})
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
		// A method, not a listing: refresh every server and report what
		// happened. Definitions come from tools/list or get_tool_definition.
		entries, err := g.catalog(ctx, caller, route, "", true)
		if err != nil {
			return nil, err
		}
		type line struct {
			Server string `json:"server"`
			Tools  int    `json:"tools"`
			Error  string `json:"error,omitempty"`
		}
		var (
			lines      []line
			total, bad int
			sb         strings.Builder
		)
		for _, e := range entries {
			l := line{Server: e.srv.Slug, Tools: len(e.tools)}
			if e.err != nil {
				l.Error = e.err.Error()
				bad++
				fmt.Fprintf(&sb, "%s: unavailable (%s)\n", e.srv.Slug, oneLine(e.err.Error(), 120))
			} else {
				total += len(e.tools)
				fmt.Fprintf(&sb, "%s: %s\n", e.srv.Slug, plural(len(e.tools), "tool"))
			}
			lines = append(lines, l)
		}
		summary := fmt.Sprintf("Refreshed %d tools across %d servers", total, len(entries)-bad)
		if bad > 0 {
			summary += fmt.Sprintf(", %d unavailable", bad)
		}
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: summary + ".\n" + sb.String()}},
			StructuredContent: map[string]any{"servers": lines, "tools": total, "unavailable": bad},
		}, nil
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
	// Tool names are passed through verbatim, except that a case-only mismatch
	// against the cached list is corrected (upstreams are case-sensitive).
	if tools, ok := decodeTools(cached[cache.NSTools]); ok {
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

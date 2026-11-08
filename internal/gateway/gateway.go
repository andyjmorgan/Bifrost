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
	"sort"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/mcp-gateway/internal/audit"
	"github.com/andyjmorgan/mcp-gateway/internal/auth"
	"github.com/andyjmorgan/mcp-gateway/internal/store"
	"github.com/andyjmorgan/mcp-gateway/internal/upstream"
)

// ProtocolVersion is the only version the front door accepts.
const ProtocolVersion = "2026-07-28"

// Separator joins slug and tool name on the mux route.
const Separator = "__"

// Store is the slice of persistence the gateway reads.
type Store interface {
	ListServers(ctx context.Context, owner string) ([]*store.Server, error)
	GetServerBySlug(ctx context.Context, owner, slug string) (*store.Server, error)
}

// Gateway serves the two MCP routes.
type Gateway struct {
	store   Store
	conn    *upstream.Connector
	log     *slog.Logger
	version string
}

// New builds a gateway.
func New(s Store, conn *upstream.Connector, log *slog.Logger, version string) *Gateway {
	return &Gateway{store: s, conn: conn, log: log, version: version}
}

type routeKey struct{}

// MuxHandler serves /mcp.
func (g *Gateway) MuxHandler() http.Handler { return g.handler("mux") }

// DirectHandler serves /{slug}/mcp; the slug is read from the chi route.
func (g *Gateway) DirectHandler() http.Handler { return g.handler("direct") }

func (g *Gateway) handler(route string) http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		srv := mcp.NewServer(&mcp.Implementation{Name: "mcp-gateway", Version: g.version}, &mcp.ServerOptions{
			SupportedProtocolVersions: []string{ProtocolVersion},
			Logger:                    g.log,
		})
		srv.AddReceivingMiddleware(g.middleware(route, chi.URLParam(r, "slug")))
		return srv
	}, &mcp.StreamableHTTPOptions{Stateless: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if route == "direct" {
			// Resolve early so the inbound audit event carries the server even
			// when the request fails before reaching a method handler.
			caller := auth.CallerFrom(r.Context())
			srv, err := g.store.GetServerBySlug(r.Context(), caller.OwnerID, chi.URLParam(r, "slug"))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			audit.SetTarget(r.Context(), srv.ID, srv.Slug)
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), routeKey{}, route)))
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
				return g.listTools(ctx, caller, route, slug)
			case "tools/call":
				// Server-side params arrive raw: arguments stay as JSON bytes and
				// are forwarded untouched.
				p, ok := req.GetParams().(*mcp.CallToolParamsRaw)
				if !ok {
					return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid tools/call params"}
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
		srv, err := g.store.GetServerBySlug(ctx, caller.OwnerID, slug)
		if err != nil {
			return nil, &jsonrpc.Error{Code: -32004, Message: "unknown server"}
		}
		tools, err := g.toolsOf(ctx, srv)
		if err != nil {
			return nil, upstreamError(srv, err)
		}
		return &mcp.ListToolsResult{Tools: tools}, nil
	}
	servers, err := g.store.ListServers(ctx, caller.OwnerID)
	if err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
	}
	type part struct {
		slug  string
		tools []*mcp.Tool
	}
	var (
		mu    sync.Mutex
		parts []part
		wg    sync.WaitGroup
	)
	for _, srv := range servers {
		if !srv.Enabled {
			continue
		}
		wg.Add(1)
		go func(srv *store.Server) {
			defer wg.Done()
			tools, err := g.toolsOf(ctx, srv)
			if err != nil {
				// One broken server must not take the mux down; it is simply absent.
				// Credentials awaiting consent or marked broken are expected states.
				if errors.Is(err, upstream.ErrCredentialPending) || errors.Is(err, upstream.ErrCredentialBroken) {
					g.log.Debug("mux: server skipped", "slug", srv.Slug, "err", err)
				} else {
					g.log.Warn("mux: list tools failed", "slug", srv.Slug, "err", err)
				}
				return
			}
			for _, t := range tools {
				t.Name = srv.Slug + Separator + t.Name
				if t.Description != "" {
					t.Description = "[" + srv.Name + "] " + t.Description
				} else {
					t.Description = "[" + srv.Name + "]"
				}
			}
			mu.Lock()
			parts = append(parts, part{srv.Slug, tools})
			mu.Unlock()
		}(srv)
	}
	wg.Wait()
	sort.Slice(parts, func(i, j int) bool { return parts[i].slug < parts[j].slug })
	out := &mcp.ListToolsResult{Tools: []*mcp.Tool{}}
	for _, p := range parts {
		out.Tools = append(out.Tools, p.tools...)
	}
	return out, nil
}

// toolsOf opens a session and pages through its tools.
func (g *Gateway) toolsOf(ctx context.Context, srv *store.Server) ([]*mcp.Tool, error) {
	sess, err := g.conn.Open(ctx, srv)
	if err != nil {
		return nil, err
	}
	defer sess.Close()
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
	if route == "mux" {
		var ok bool
		slug, name, ok = strings.Cut(p.Name, Separator)
		if !ok || slug == "" || name == "" {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("tool name must be <slug>%s<tool>", Separator)}
		}
	}
	srv, err := g.store.GetServerBySlug(ctx, caller.OwnerID, slug)
	if err != nil {
		return nil, &jsonrpc.Error{Code: -32004, Message: "unknown server " + slug}
	}
	if !srv.Enabled {
		return nil, &jsonrpc.Error{Code: -32004, Message: "server " + slug + " is disabled"}
	}
	audit.SetTarget(ctx, srv.ID, srv.Slug)
	sess, err := g.conn.Open(ctx, srv)
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

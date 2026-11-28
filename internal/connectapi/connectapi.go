// Package connectapi is the gateway's optional Connect API (connect-go: the
// Connect, gRPC and gRPC-Web protocols over one handler). It is a second way
// in to what the REST API already answers, for services that would rather
// call the gateway over typed RPC. It serves on its own listener and only
// when MCPGW_CONNECT_ENABLED is set.
//
// Callers authenticate exactly as on the REST API: an OIDC bearer token or a
// gateway API key, resolved by the same Authenticator, with the same scopes.
package connectapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/portal"
	"github.com/andyjmorgan/Bifrost/internal/store"
	bifrostv1 "github.com/andyjmorgan/Bifrost/pkg/api/bifrost/v1"
	"github.com/andyjmorgan/Bifrost/pkg/api/bifrost/v1/bifrostv1connect"
)

// Authenticator resolves a request's credentials to a caller. The gateway's
// *auth.Authenticator is one.
type Authenticator interface {
	AuthenticateHeader(ctx context.Context, h http.Header, resource string) (*auth.Caller, error)
}

// Deps wires a Server.
type Deps struct {
	Authn      Authenticator
	Store      portal.Store
	PublicBase string // the gateway's external origin, for each server's MCP URL
	Log        *slog.Logger
}

// Server implements every service of the Connect API.
type Server struct {
	authn      Authenticator
	store      portal.Store
	publicBase string
	log        *slog.Logger
}

var _ bifrostv1connect.PortalServiceHandler = (*Server)(nil)

// New builds the Connect API.
func New(d Deps) *Server {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	return &Server{authn: d.Authn, store: d.Store, publicBase: d.PublicBase, log: log}
}

// Handler routes every service, each behind authentication and the scope
// its procedures need.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	path, h := bifrostv1connect.NewPortalServiceHandler(s)
	mux.Handle(path, s.require(auth.ScopeAPIRead, h))
	return mux
}

// require authenticates the request and checks the caller holds scope,
// answering in whichever protocol the client spoke when they do not.
func (s *Server) require(scope string, next http.Handler) http.Handler {
	errw := connect.NewErrorWriter()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := s.authn.AuthenticateHeader(r.Context(), r.Header, "")
		switch {
		case errors.Is(err, auth.ErrForbidden):
			err = connect.NewError(connect.CodePermissionDenied, errors.New("caller lacks the required role"))
		case err != nil:
			err = connect.NewError(connect.CodeUnauthenticated, errors.New("a valid bearer token or API key is required"))
		case scope != "" && !c.Has(scope):
			err = connect.NewError(connect.CodePermissionDenied, errors.New("insufficient scope: needs "+scope))
		}
		if err != nil {
			_ = errw.Write(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithCaller(r.Context(), c)))
	})
}

// ListServers lists the servers granted to the caller, as GET /api/v1/portal does.
func (s *Server) ListServers(ctx context.Context, _ *connect.Request[bifrostv1.ListServersRequest]) (*connect.Response[bifrostv1.ListServersResponse], error) {
	c := auth.CallerFrom(ctx)
	if c == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no caller"))
	}
	held, err := portal.List(ctx, s.store, s.publicBase, c.UserID)
	if err != nil {
		s.log.Error("connect: list servers", "user", c.UserID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not list servers"))
	}
	out := &bifrostv1.ListServersResponse{Servers: make([]*bifrostv1.Server, 0, len(held))}
	for _, h := range held {
		out.Servers = append(out.Servers, toProto(h))
	}
	return connect.NewResponse(out), nil
}

func toProto(h portal.Server) *bifrostv1.Server {
	p := &bifrostv1.Server{
		Id: h.ID, Slug: h.Slug, Name: h.Name, Description: h.Description, McpUrl: h.MCPURL, Url: h.URL,
		Enabled: h.Enabled, HasRules: h.HasRules,
		Selection: &bifrostv1.Selection{
			Tools:     allowList(h.Selection, store.SelTools),
			Prompts:   allowList(h.Selection, store.SelPrompts),
			Resources: allowList(h.Selection, store.SelResources),
			Templates: allowList(h.Selection, store.SelTemplates),
		},
		Capabilities: make([]*bifrostv1.Capability, 0, len(h.Capabilities)),
	}
	var icons []mcp.Icon
	if len(h.Icons) > 0 && json.Unmarshal(h.Icons, &icons) == nil {
		for _, ic := range icons {
			p.Icons = append(p.Icons, &bifrostv1.Icon{Src: ic.Source, MimeType: ic.MIMEType, Sizes: ic.Sizes, Theme: string(ic.Theme)})
		}
	}
	for _, kn := range h.Capabilities {
		kind, name, _ := strings.Cut(kn, ":") // names may hold colons themselves
		p.Capabilities = append(p.Capabilities, &bifrostv1.Capability{Kind: kind, Name: name})
	}
	return p
}

// allowList is the list for key, or nil (every name allowed) when there is none.
func allowList(sel store.Selection, key string) *bifrostv1.AllowList {
	names, ok := sel[key]
	if !ok {
		return nil
	}
	return &bifrostv1.AllowList{Names: append([]string{}, names...)}
}

// Serve listens on addr and serves h until ctx is done, then shuts down
// gracefully. It speaks HTTP/1.1 and cleartext HTTP/2 (h2c), so gRPC
// clients can call it without TLS; put TLS in front of it as you would the
// main listener. It returns the bound address once listening.
func Serve(ctx context.Context, addr string, h http.Handler, log *slog.Logger) (net.Addr, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	var protos http.Protocols
	protos.SetHTTP1(true)
	protos.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: h, Protocols: &protos, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("connect server", "err", err)
		}
	}()
	return ln.Addr(), nil
}

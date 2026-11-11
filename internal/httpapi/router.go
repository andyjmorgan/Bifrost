package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/andyjmorgan/Mcp-Bifrost/internal/audit"
	"github.com/andyjmorgan/Mcp-Bifrost/internal/auth"
	"github.com/andyjmorgan/Mcp-Bifrost/internal/cache"
	"github.com/andyjmorgan/Mcp-Bifrost/internal/gateway"
	"github.com/andyjmorgan/Mcp-Bifrost/internal/oauthclient"
	"github.com/andyjmorgan/Mcp-Bifrost/internal/store"
	"github.com/andyjmorgan/Mcp-Bifrost/internal/upstream"
)

// API holds the dependencies of every handler.
type API struct {
	store      *store.Postgres
	keys       *auth.Keys
	authn      *auth.Authenticator
	conn       *upstream.Connector
	cache      *cache.Writer
	toolsTTL   time.Duration
	oauth      *oauthclient.Client
	gw         *gateway.Gateway
	rec        *audit.Recorder
	log        *slog.Logger
	publicBase string
	selfBase   string // loopback base the e2e handler calls
	webRoot    string
	cfgDTO     ConfigDTO
}

// Deps wires an API.
type Deps struct {
	Store      *store.Postgres
	Keys       *auth.Keys
	Authn      *auth.Authenticator
	Conn       *upstream.Connector
	Cache      *cache.Writer
	ToolsTTL   time.Duration
	OAuth      *oauthclient.Client
	Gateway    *gateway.Gateway
	Recorder   *audit.Recorder
	Log        *slog.Logger
	PublicBase string
	SelfBase   string
	WebRoot    string
	Config     ConfigDTO
}

// New builds the API.
func New(d Deps) *API {
	return &API{store: d.Store, keys: d.Keys, authn: d.Authn, conn: d.Conn, cache: d.Cache, toolsTTL: d.ToolsTTL, oauth: d.OAuth, gw: d.Gateway, rec: d.Recorder,
		log: d.Log, publicBase: d.PublicBase, selfBase: d.SelfBase, webRoot: d.WebRoot, cfgDTO: d.Config}
}

// Router assembles every route: MCP front door, REST API, OAuth callback,
// well-known documents, health, and the SPA.
func (a *API) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RealIP, middleware.Recoverer, requestIDMiddleware)
	r.Use(middleware.Timeout(5 * time.Minute))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.Get("/.well-known/mcp-client-metadata.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(a.oauth.CIMDDocument())
	})
	r.Handle("/.well-known/oauth-protected-resource", a.authn.ProtectedResourceMetadata())
	r.Handle("/.well-known/oauth-protected-resource/*", a.authn.ProtectedResourceMetadata())
	r.Get("/oauth/callback", a.oauthCallback)

	// MCP front door. Inbound audit wraps auth so denied attempts are recorded.
	r.Group(func(r chi.Router) {
		r.Use(a.rec.Inbound("mux", true), a.authn.RequireMCP(), withTarget("mux"))
		r.Handle("/mcp", a.gw.MuxHandler())
	})
	r.Group(func(r chi.Router) {
		r.Use(a.rec.Inbound("direct", true), a.authn.RequireMCP(), withTarget("direct"))
		r.Handle("/{slug:[A-Za-z0-9]{1,12}}/mcp", a.gw.DirectHandler())
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/config", a.config)
		r.With(a.authn.RequireAPI("")).Get("/me", a.me)

		r.Group(func(r chi.Router) {
			r.Use(a.authn.RequireAPI(auth.ScopeAPIRead))
			r.Get("/servers", a.listServers)
			r.Get("/servers/{id}", a.getServer)
			r.Get("/api-keys", a.listKeys)
			r.Get("/audit", a.listAudit)
			r.Get("/audit/{id}", a.getAudit)
			r.Get("/audit/{id}/trace", a.getAuditTrace)
			r.Get("/policies", a.listPolicies)
			r.Get("/policies/fields", a.policyFields)
			r.Get("/policies/{id}", a.getPolicy)
			r.Post("/policies/validate", a.validatePolicy)
			r.Get("/bindings", a.listBindings)
			r.Get("/bindings/{route:[A-Za-z0-9]{1,12}}", a.getBindings)
		})
		r.Group(func(r chi.Router) {
			r.Use(a.authn.RequireAPI(auth.ScopeMCPEdit))
			r.Post("/servers", a.createServer)
			r.Patch("/servers/{id}", a.updateServer)
			r.Delete("/servers/{id}", a.deleteServer)
			r.Put("/servers/{id}/credential", a.putCredential)
			r.Post("/servers/{id}/oauth/start", a.oauthStart)
			r.Post("/policies", a.createPolicy)
			r.Patch("/policies/{id}", a.updatePolicy)
			r.Delete("/policies/{id}", a.deletePolicy)
			r.Put("/bindings/{route:[A-Za-z0-9]{1,12}}", a.putBindings)
		})
		r.Group(func(r chi.Router) {
			r.Use(a.authn.RequireAPI(auth.ScopeMCPExecute))
			r.Post("/servers/{id}/ping", a.ping)
			r.Post("/e2e/{route:[a-z0-9]{1,12}}/tools/{action:list|call}", a.e2e)
			r.Post("/policies/preview", a.previewPolicy)
		})
		r.Group(func(r chi.Router) {
			r.Use(a.authn.RequireAPI(auth.ScopeAPIEdit))
			r.Post("/api-keys", a.createKey)
			r.Delete("/api-keys/{id}", a.revokeKey)
		})
		r.NotFound(func(w http.ResponseWriter, _ *http.Request) { writeErr(w, http.StatusNotFound, "not found") })
	})

	if a.webRoot != "" {
		r.NotFound(a.spa())
	}
	return r
}

func withTarget(route string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(audit.WithTarget(r.Context(), audit.Target{Route: route})))
		})
	}
}

// spa serves built assets and falls back to index.html for client routes.
func (a *API) spa() http.HandlerFunc {
	fs := http.FileServer(http.Dir(a.webRoot))
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		p := filepath.Join(a.webRoot, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			if strings.HasPrefix(r.URL.Path, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fs.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, filepath.Join(a.webRoot, "index.html"))
	}
}

package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/mcp-gateway/internal/audit"
	"github.com/andyjmorgan/mcp-gateway/internal/auth"
)

// e2e drives the gateway's own front door as one of the caller's API keys,
// so the UI exercises exactly the path an external MCP client takes.
// route is "mux" or a slug; action is "list" or "call".
func (a *API) e2e(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	route, action := chi.URLParam(r, "route"), chi.URLParam(r, "action")
	var in E2EInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	keyID, err := uuid.Parse(in.APIKeyID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "apiKeyId is required")
		return
	}
	secret, err := a.keys.Reveal(r.Context(), c.OwnerID, keyID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "api key not usable: "+err.Error())
		return
	}
	endpoint := a.selfBase + "/mcp"
	if route != "mux" {
		endpoint = a.selfBase + "/" + route + "/mcp"
	}
	// An operator listing from the UI is a "what is there now" question:
	// drop the cache for the target first (synchronously, so the front-door
	// call below misses and refills it) instead of serving the cached list.
	if action == "list" {
		if route == "mux" {
			if err := a.store.CacheInvalidateOwner(r.Context(), c.OwnerID); err != nil {
				a.log.Warn("e2e: cache invalidate", "err", err)
			}
		} else if s, err := a.store.GetServerBySlug(r.Context(), c.OwnerID, route); err == nil {
			if err := a.store.CacheInvalidateServer(r.Context(), s.ID); err != nil {
				a.log.Warn("e2e: cache invalidate", "slug", route, "err", err)
			}
		}
	}
	if in.Search {
		endpoint += "?search=true"
	}
	public := a.publicBase + endpoint[len(a.selfBase):]
	reqID := uuid.NewString()
	res := E2EResult{Endpoint: public, RequestID: reqID}
	start := time.Now()

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	hc := &http.Client{Transport: &headerRT{base: http.DefaultTransport, headers: map[string]string{"Authorization": "Bearer " + secret, "X-Request-Id": reqID}}, Timeout: 60 * time.Second}
	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-gateway-ui", Version: a.cfgDTO.Version}, nil)
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: hc}, nil)
	if err != nil {
		res.Error = err.Error()
		res.LatencyMS = time.Since(start).Milliseconds()
		writeJSON(w, http.StatusOK, res)
		return
	}
	defer sess.Close()
	switch action {
	case "list":
		out, err := sess.ListTools(ctx, &mcp.ListToolsParams{})
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK, res.Result = true, out
		}
	case "call":
		if in.Name == "" {
			writeErr(w, http.StatusBadRequest, "name is required")
			return
		}
		out, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: in.Name, Arguments: in.Arguments})
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK, res.Result = !out.IsError, out
			if out.IsError {
				res.Error = "tool returned an error"
			}
		}
	default:
		writeErr(w, http.StatusNotFound, "unknown action")
		return
	}
	res.LatencyMS = time.Since(start).Milliseconds()
	writeJSON(w, http.StatusOK, res)
}

type headerRT struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h *headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.headers {
		r.Header.Set(k, v)
	}
	return h.base.RoundTrip(r)
}

// requestIDMiddleware honours X-Request-Id so an e2e run can be found in the audit log.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := r.Header.Get("X-Request-Id"); id != "" && len(id) <= 64 {
			r = r.WithContext(audit.WithRequestID(r.Context(), id))
		}
		next.ServeHTTP(w, r)
	})
}

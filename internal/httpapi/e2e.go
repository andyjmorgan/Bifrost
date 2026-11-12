package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/internal/audit"
	"github.com/andyjmorgan/Bifrost/internal/auth"
)

// e2e drives the gateway's own front door as one of the caller's API keys,
// so the UI exercises exactly the path an external MCP client takes.
// route is "mux" or a slug; surface is tools, prompts or resources; action is
// list or call (tools), list or get (prompts), list, templates or read
// (resources).
func (a *API) e2e(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	route, surface, action := chi.URLParam(r, "route"), chi.URLParam(r, "surface"), chi.URLParam(r, "action")
	if surface == "" {
		surface = "tools"
	}
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
	if !c.IsAdmin() {
		if k, err := a.store.GetAPIKey(r.Context(), c.OrgID, keyID); err != nil || k.CreatedBy != c.UserID {
			writeErr(w, http.StatusNotFound, "api key not found")
			return
		}
	}
	secret, err := a.keys.Reveal(r.Context(), c.OrgID, keyID)
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
			if err := a.store.CacheInvalidateOwner(r.Context(), c.OrgID); err != nil {
				a.log.Warn("e2e: cache invalidate", "err", err)
			}
		} else if s, err := a.store.GetServerBySlug(r.Context(), c.OrgID, route); err == nil {
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

	// The whole exchange streams to the browser as SSE: the upstream may
	// elect to send progress and log notifications before its result, and
	// the UI shows them as they arrive. The final event is the E2EResult.
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	var emitMu sync.Mutex
	emit := func(event string, v any) {
		b, _ := json.Marshal(v)
		emitMu.Lock()
		defer emitMu.Unlock()
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	finish := func() {
		res.LatencyMS = time.Since(start).Milliseconds()
		emit("result", res)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	hc := &http.Client{Transport: &headerRT{base: http.DefaultTransport, headers: map[string]string{"Authorization": "Bearer " + secret, "X-Request-Id": reqID}}, Timeout: 60 * time.Second}
	client := mcp.NewClient(&mcp.Implementation{Name: "bifrost-ui", Version: a.cfgDTO.Version}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, n *mcp.ProgressNotificationClientRequest) {
			emit("progress", map[string]any{"progress": n.Params.Progress, "total": n.Params.Total, "message": n.Params.Message})
		},
		LoggingMessageHandler: func(_ context.Context, n *mcp.LoggingMessageRequest) {
			emit("log", map[string]any{"level": n.Params.Level, "logger": n.Params.Logger, "data": n.Params.Data})
		},
	})
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: hc}, nil)
	if err != nil {
		res.Error = err.Error()
		finish()
		return
	}
	defer sess.Close()
	switch surface + "/" + action {
	case "tools/list":
		params := &mcp.ListToolsParams{}
		params.SetProgressToken(reqID)
		out, err := sess.ListTools(ctx, params)
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK, res.Result = true, out
		}
	case "tools/call":
		if in.Name == "" {
			res.Error = "name is required"
			finish()
			return
		}
		params := &mcp.CallToolParams{Name: in.Name, Arguments: in.Arguments}
		params.SetProgressToken(reqID)
		out, err := sess.CallTool(ctx, params)
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK, res.Result = !out.IsError, out
			if out.IsError {
				res.Error = "tool returned an error"
			}
		}
	case "prompts/list":
		out, err := sess.ListPrompts(ctx, &mcp.ListPromptsParams{})
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK, res.Result = true, out
		}
	case "prompts/get":
		if in.Name == "" {
			res.Error = "name is required"
			finish()
			return
		}
		args := map[string]string{}
		for k, v := range in.Arguments {
			if sv, ok := v.(string); ok {
				args[k] = sv
			} else if b, err := json.Marshal(v); err == nil {
				args[k] = string(b)
			}
		}
		out, err := sess.GetPrompt(ctx, &mcp.GetPromptParams{Name: in.Name, Arguments: args})
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK, res.Result = true, out
		}
	case "resources/list":
		out, err := sess.ListResources(ctx, &mcp.ListResourcesParams{})
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK, res.Result = true, out
		}
	case "resources/templates":
		out, err := sess.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{})
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK, res.Result = true, out
		}
	case "resources/read":
		if in.URI == "" {
			res.Error = "uri is required"
			finish()
			return
		}
		out, err := sess.ReadResource(ctx, &mcp.ReadResourceParams{URI: in.URI})
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK, res.Result = true, out
		}
	default:
		res.Error = "unknown action"
	}
	finish()
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

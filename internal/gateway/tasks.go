package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/cache"
	"github.com/andyjmorgan/Bifrost/internal/store"
	"github.com/andyjmorgan/Bifrost/internal/upstream"
	"github.com/andyjmorgan/Bifrost/pkg/hooks"
)

// Tasks: on the direct route the gateway serves the tasks extension's
// methods. The engine decides who may follow a task and shapes what comes
// back; the gateway sends each request to the task's server on its own, as
// the stateless protocol allows, since the client library has no call for
// these methods.

// nsTask is the cache namespace that holds task records.
const nsTask = "task"

// taskMethods are the tasks extension's methods a client sends.
var taskMethods = []string{"tasks/get", "tasks/update", "tasks/cancel"}

// taskParams are the params of a tasks/* request.
type taskParams struct {
	mcp.ParamsBase
	TaskID         string                     `json:"taskId"`
	InputResponses map[string]json.RawMessage `json:"inputResponses,omitempty"`
}

// addTaskMethods lets the front door's server receive the tasks methods.
// The middleware answers them; the handler registered here never runs.
func addTaskMethods(srv *mcp.Server) {
	for _, m := range taskMethods {
		_ = mcp.AddReceivingCustomMethod(srv, m, func(context.Context, *mcp.ServerSession, *taskParams) (*rawResult, error) {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found"}
		})
	}
}

// taskHandle is the raw result of a tools/call the server answered with a
// task handle, or nil.
func taskHandle(c *upstream.Capture) json.RawMessage {
	if c == nil {
		return nil
	}
	raw := c.Result()
	var m struct {
		ResultType string `json:"resultType"`
	}
	if raw == nil || json.Unmarshal(raw, &m) != nil || m.ResultType != "task" {
		return nil
	}
	return raw
}

func (g *Gateway) hookTask(ctx context.Context, caller *auth.Caller, route, slug, method string, req mcp.Request) (mcp.Result, error) {
	p, ok := req.GetParams().(*taskParams)
	if !ok || p == nil || p.TaskID == "" {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "taskId is required"}
	}
	ld, err := g.load(ctx, caller, route, slug, metaOf(req), cache.NSTools)
	if err != nil {
		return nil, err
	}
	e, r := g.engine, ld.req
	var out hooks.Outcome
	switch method {
	case "tasks/get":
		out = e.GetTask(ctx, r, p.TaskID)
	case "tasks/update":
		out = e.UpdateTask(ctx, r, p.TaskID, p.InputResponses)
	default:
		out = e.CancelTask(ctx, r, p.TaskID)
	}
	g.cancelTask(ctx, ld, out.Cancel)
	if out.Forward == nil {
		return g.done(ctx, out, nil)
	}
	g.note(ctx, out)
	x := ld.by[out.Forward.Slug]
	if x == nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "the request could not be routed"}
	}
	raw, rpcErr, err := g.sendTask(ctx, x, out.Forward)
	if err != nil {
		if errors.Is(err, upstream.ErrCredentialBroken) || errors.Is(err, upstream.ErrCredentialPending) {
			return nil, upstreamError(x.srv, err)
		}
		g.log.Warn("task request failed", "slug", x.srv.Slug, "method", method, "err", err)
		return nil, &jsonrpc.Error{Code: hooks.CodeUpstreamLost, Message: "The server's reply was lost before it completed."}
	}
	if rpcErr != nil {
		// The server's own error passes through, as it does for a call.
		return nil, rpcErr
	}
	final := e.UpstreamTaskReply(ctx, r, out.Task, keepMeta(raw, g.metaOut))
	g.cancelTask(ctx, ld, final.Cancel)
	return g.done(ctx, final, nil)
}

// sendTask sends a tasks/* forward to its server, with the identity and
// capabilities the engine decided, and the client's tasks opt-in.
func (g *Gateway) sendTask(ctx context.Context, x *exposed, fwd *hooks.Forward) (json.RawMessage, *jsonrpc.Error, error) {
	params, err := toMap(fwd.Params)
	if err != nil {
		return nil, nil, err
	}
	identity := map[string]any{"name": "bifrost", "version": g.version}
	caps := map[string]any{}
	if c := fwd.Connect; c != nil {
		if name, _ := c.ClientInfo["name"].(string); name != "" {
			identity = map[string]any{"name": name, "version": c.ClientInfo["version"]}
		}
		if _, ok := c.Capabilities["elicitation"]; ok {
			caps["elicitation"] = map[string]any{}
		}
	}
	if len(fwd.Extensions) > 0 {
		caps["extensions"] = fwd.Extensions
	}
	params["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion": hooks.ProtocolCurrent,
		metaClientInfo: identity,
		metaClientCaps: caps,
	}
	name, _ := params["taskId"].(string)
	return g.conn.Call(ctx, x.srv, hooks.ProtocolCurrent, fwd.Method, name, params, 0)
}

// cancelTask sends a tasks/cancel the engine asked for, when a task ends at
// the gateway. It does not wait for the server's reply, and a failure only
// leaves the task to expire at the server.
func (g *Gateway) cancelTask(ctx context.Context, ld *loaded, fwd *hooks.Forward) {
	if fwd == nil {
		return
	}
	x := ld.by[fwd.Slug]
	if x == nil {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), g.conn.Timeout())
	go func() {
		defer cancel()
		if _, rpcErr, err := g.sendTask(cctx, x, fwd); err != nil || rpcErr != nil {
			g.log.Info("task could not be cancelled at the server", "slug", x.srv.Slug, "err", err, "rpcErr", rpcErr)
		}
	}()
}

// cacheTasks keeps task records in the gateway's TTL cache, so any replica
// can answer a poll for a task another replica started.
type cacheTasks struct{ g *Gateway }

func (s *cacheTasks) Put(id string, rec hooks.TaskRecord, ttl time.Duration) {
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.g.store.CacheSet(ctx, nsTask, id, nil, time.Time{}, b, ttl); err != nil {
		// The next poll will find nothing and be told the task is unknown.
		s.g.log.Error("task could not be stored", "err", err)
	}
}

func (s *cacheTasks) Get(id string) (hooks.TaskRecord, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := s.g.store.CacheGet(ctx, nsTask, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.g.log.Error("task could not be read", "err", err)
		}
		return hooks.TaskRecord{}, false
	}
	var rec hooks.TaskRecord
	if json.Unmarshal(raw, &rec) != nil {
		return hooks.TaskRecord{}, false
	}
	return rec, true
}

func (s *cacheTasks) Delete(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.g.store.CacheTake(ctx, nsTask, id); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.g.log.Error("task could not be deleted", "err", err)
	}
}

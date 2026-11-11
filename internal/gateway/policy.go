package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Mcp-Bifrost/internal/audit"
	"github.com/andyjmorgan/Mcp-Bifrost/internal/auth"
	"github.com/andyjmorgan/Mcp-Bifrost/internal/cache"
	"github.com/andyjmorgan/Mcp-Bifrost/internal/policy"
	"github.com/andyjmorgan/Mcp-Bifrost/internal/store"
)

// bound is every policy of an owner, compiled and keyed by route: mux for the
// global front door, byServer for each server's own route. On the mux route
// a server's policies run first, then the global ones.
type bound struct {
	mux      []*policy.Compiled
	byServer map[uuid.UUID][]*policy.Compiled
}

func (b *bound) empty() bool { return b == nil || (len(b.mux) == 0 && len(b.byServer) == 0) }

// forRoute is the ordered policy chain for one server on one route.
func (b *bound) forRoute(route string, serverID uuid.UUID) []*policy.Compiled {
	if b == nil {
		return nil
	}
	own := b.byServer[serverID]
	if route != "mux" {
		return own
	}
	if len(own) == 0 {
		return b.mux
	}
	out := make([]*policy.Compiled, 0, len(own)+len(b.mux))
	out = append(out, own...)
	return append(out, b.mux...)
}

// loadPolicies reads the owner's bindings (one query) and compiles each
// policy through the cache. A policy that no longer compiles is skipped and
// reported as an issue rather than failing the request.
func (g *Gateway) loadPolicies(ctx context.Context, owner string) (*bound, error) {
	bindings, err := g.store.ListBindings(ctx, owner)
	if err != nil {
		return nil, err
	}
	b := &bound{byServer: map[uuid.UUID][]*policy.Compiled{}}
	for _, bd := range bindings {
		c, err := g.policies.Get(bd.Policy)
		if err != nil {
			g.log.Warn("policy skipped: does not compile", "policy", bd.Policy.Name, "err", err)
			audit.AddIssue(ctx, "policy "+bd.Policy.Name+" skipped: "+err.Error())
			continue
		}
		if bd.ServerID == nil {
			b.mux = append(b.mux, c)
		} else {
			b.byServer[*bd.ServerID] = append(b.byServer[*bd.ServerID], c)
		}
	}
	return b, nil
}

func policyServer(srv *store.Server, in cache.Init) policy.Server {
	return policy.Server{Slug: srv.Slug, Name: srv.Name, Description: srv.Description, Instructions: in.Instructions, ProtocolVersion: in.ProtocolVersion, URL: srv.URL}
}

// shape runs the route's policy chain over a server's tools. With no policies
// bound it returns pass-through decisions, so callers need not special-case.
func shape(ctx context.Context, b *bound, route string, srv *store.Server, in cache.Init, tools []*mcp.Tool) []policy.Decision {
	chain := b.forRoute(route, srv.ID)
	if len(chain) == 0 {
		out := make([]policy.Decision, len(tools))
		for i, t := range tools {
			out[i] = policy.Decision{Tool: t, Upstream: t.Name, Visible: true}
		}
		return out
	}
	return policy.Apply(ctx, chain, tools, policyServer(srv, in), route)
}

func visibleTools(ds []policy.Decision) []*mcp.Tool {
	out := make([]*mcp.Tool, 0, len(ds))
	for i := range ds {
		if ds[i].Visible {
			out = append(out, ds[i].Tool)
		}
	}
	return out
}

// findDecision resolves the name a client used (case-insensitively, since
// models are careless with case) to the decision that produced it.
func findDecision(ds []policy.Decision, name string) *policy.Decision {
	for i := range ds {
		if ds[i].Tool.Name == name {
			return &ds[i]
		}
	}
	for i := range ds {
		if strings.EqualFold(ds[i].Tool.Name, name) {
			return &ds[i]
		}
	}
	return nil
}

type sessionKey struct{}

func withSession(ctx context.Context, s *mcp.ServerSession) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

func sessionFrom(ctx context.Context) *mcp.ServerSession {
	s, _ := ctx.Value(sessionKey{}).(*mcp.ServerSession)
	return s
}

// CodeConfirmation is the JSON-RPC error for a guarded tool the client
// cannot confirm (no elicitation support).
const CodeConfirmation = -32013

// confirmKey is the input request id a guarded call is answered under.
const confirmKey = "confirm"

// inputRequired is a tools/call result that asks for input (SEP-2322). The
// SDK only marks results produced by its own tool handlers, and the gateway
// answers from middleware, so this adds the resultType marker on the wire.
// The embedded result keeps the SDK's private interface methods, so the
// server and client middleware treat it as a normal CallToolResult.
type inputRequired struct{ *mcp.CallToolResult }

func (r inputRequired) MarshalJSON() ([]byte, error) {
	b, err := r.CallToolResult.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	m["resultType"] = json.RawMessage(`"input_required"`)
	if string(m["content"]) == "null" {
		delete(m, "content") // an input-required result carries no content
	}
	return json.Marshal(m)
}

// confirm guards a call. On the first attempt it returns an input-required
// result carrying an elicitation the client must answer; the client then
// retries the call with the answer in inputResponses. An accepted answer
// lets the call through (nil, nil); a decline is returned as a tool error
// result so the model sees it.
func (g *Gateway) confirm(ctx context.Context, p *mcp.CallToolParamsRaw, d *policy.Decision, srv *store.Server) (mcp.Result, error) {
	if resp, ok := p.InputResponses[confirmKey]; ok {
		er, _ := resp.(*mcp.ElicitResult)
		if er != nil && er.Action == "accept" {
			if yes, _ := er.Content[confirmKey].(bool); yes {
				return nil, nil
			}
		}
		audit.AddIssue(ctx, d.Tool.Name+": declined by the user")
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%s was not run: the user declined.", d.Tool.Name)}}}, nil
	}
	sess := sessionFrom(ctx)
	if sess == nil || sess.InitializeParams() == nil || sess.InitializeParams().Capabilities == nil || sess.InitializeParams().Capabilities.Elicitation == nil {
		return nil, &jsonrpc.Error{Code: CodeConfirmation, Message: fmt.Sprintf("%s requires confirmation and this client does not support elicitation", d.Tool.Name)}
	}
	msg := d.ConfirmMessage
	if msg == "" {
		msg = fmt.Sprintf("Run %s on %s?", d.Tool.Name, srv.Name)
	}
	return inputRequired{&mcp.CallToolResult{InputRequests: mcp.InputRequestMap{confirmKey: &mcp.ElicitParams{
		Mode:    "form",
		Message: msg,
		RequestedSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{confirmKey: map[string]any{"type": "boolean", "title": "Confirm", "description": msg}},
			"required":   []string{confirmKey},
		},
	}}}}, nil
}

// PreviewServer is one server's outcome in a policy preview.
type PreviewServer struct {
	Slug      string
	Decisions []policy.Decision
	Err       error
}

// Preview evaluates an unsaved policy against a route's current tools: on
// its own, or layered after the policies already bound there (extra).
// route is "mux" or a server slug. Cached tools are used; misses fetch.
func (g *Gateway) Preview(ctx context.Context, caller *auth.Caller, route string, pol *policy.Policy, extra bool) ([]PreviewServer, error) {
	c, err := policy.Compile(pol)
	if err != nil {
		return nil, err
	}
	b := &bound{byServer: map[uuid.UUID][]*policy.Compiled{}}
	if extra {
		if b, err = g.loadPolicies(ctx, caller.OwnerID); err != nil {
			return nil, err
		}
	}
	kind, slug := "mux", ""
	if route != "mux" {
		kind, slug = "direct", strings.ToLower(route)
		srv, err := g.store.GetServerBySlug(ctx, caller.OwnerID, slug)
		if err != nil {
			return nil, err
		}
		b.byServer[srv.ID] = append(b.byServer[srv.ID], c)
	} else {
		b.mux = append(b.mux, c)
	}
	entries, err := g.catalog(ctx, caller, kind, slug, false, b)
	if err != nil {
		return nil, err
	}
	out := make([]PreviewServer, 0, len(entries))
	for _, e := range entries {
		out = append(out, PreviewServer{Slug: e.srv.Slug, Decisions: e.decisions, Err: e.err})
	}
	return out, nil
}

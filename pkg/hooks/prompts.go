package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Prompts: the listing, and prompts/get on its way to the server and back.

func promptItems(ps []*mcp.Prompt) []map[string]any {
	out := make([]map[string]any, len(ps))
	for i, p := range ps {
		out[i] = toMap(p)
	}
	return out
}

// ListPrompts lists every server's exposed prompts, slug a-z then name a-z.
func (e *Engine) ListPrompts(parent context.Context, r Request) (ret Outcome) {
	defer e.recovered(&ret, "prompts/list", callRef{method: "prompts/list"})
	ctx, cancel := e.ctx(parent)
	defer cancel()
	var o Outcome
	res := &mcp.ListPromptsResult{Prompts: []*mcp.Prompt{}}
	res.TTLMs, res.CacheScope = e.Limits.ttl(), "private"
	for _, s := range sortedServers(r.Servers) {
		if !e.config(ctx, r, s, "prompts", &o) {
			continue
		}
		ls, issues, hits := s.Rules.listItems(ctx, KindPrompt, "prompts/list.post", "name", promptItems(s.Catalog.Prompts), r.context(s), e.Limits.Workers)
		o.Hits = append(o.Hits, hits...)
		o.Issues = append(o.Issues, prefixed(s.Slug, issues)...)
		for _, l := range ls {
			var p mcp.Prompt
			b, _ := json.Marshal(l.value)
			_ = json.Unmarshal(b, &p)
			if r.mux() {
				p.Name = s.Slug + Separator + p.Name
			}
			res.Prompts = append(res.Prompts, &p)
		}
	}
	o.Result = res
	return e.finishOutcome(ctx, o)
}

func prefixed(slug string, xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = slug + ": " + x
	}
	return out
}

func (e *Engine) resolvePrompt(ctx context.Context, r Request, name string) (*Server, *listed, Outcome, error) {
	var o Outcome
	slug := ""
	if r.mux() {
		var ok bool
		slug, name, ok = strings.Cut(name, Separator)
		if !ok || slug == "" || name == "" {
			return nil, nil, o, &RPCError{Code: CodeInvalidParams, Message: "prompt name must be <slug>" + Separator + "<prompt>"}
		}
	} else if len(r.Servers) == 1 {
		slug = r.Servers[0].Slug
	}
	s := r.server(slug)
	if s == nil || !e.config(ctx, r, s, "prompts", &o) {
		return nil, nil, o, &RPCError{Code: CodeInvalidParams, Message: "unknown prompt " + name}
	}
	ls, _, hits := s.Rules.listItems(ctx, KindPrompt, "prompts/list.post", "name", promptItems(s.Catalog.Prompts), r.context(s), e.Limits.Workers)
	o.Hits = append(o.Hits, hits...)
	for i := range ls {
		if ls[i].exposedID == name {
			return s, &ls[i], o, nil
		}
	}
	return s, nil, o, &RPCError{Code: CodeInvalidParams, Message: "unknown prompt " + name}
}

func upstreamPrompt(s *Server, name string) map[string]any {
	for _, p := range s.Catalog.Prompts {
		if p.Name == name {
			return toMap(p)
		}
	}
	return map[string]any{"name": name}
}

// GetPrompt runs the request side of prompts/get.
func (e *Engine) GetPrompt(parent context.Context, r Request, p *mcp.GetPromptParams) (ret Outcome) {
	defer e.recovered(&ret, "prompts/get", promptCallRef(p))
	ctx, cancel := e.ctx(parent)
	defer cancel()
	s, l, o, err := e.resolvePrompt(ctx, r, p.Name)
	if err != nil {
		o.Error = err.(*RPCError)
		return o
	}
	args := map[string]any{}
	for k, v := range p.Arguments {
		args[k] = v
	}
	if _, err := l.args.reverse(p.Arguments); err != nil {
		o.Error = &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		return o
	}
	it := &item{kind: KindPrompt, view: NewView(l.value), args: l.args}
	up := upstreamPrompt(s, l.upstreamID)
	resolve := func(name string) (any, bool) {
		if name == "upstream" {
			return up, true
		}
		return it.resolve(name)
	}
	roots := r.context(s).roots("prompts/get", callParams(l.exposedID, copyMap(args), p.Meta))
	c := preCall{
		server: s, method: "prompts/get", hook: "prompts/get.pre", target: l.upstreamID, label: l.exposedID,
		args: args, state: p.RequestState, answers: p.InputResponses, resolve: resolve, roots: roots,
		refuse: rpcRefusal, denied: "prompts/get was refused by policy",
		set: func(key string, val any) error {
			args[splitPath(key)[1]] = val // arguments.<name>
			setParam(roots, "arguments", copyMap(args))
			return nil
		},
	}
	pass, goes := e.pre(ctx, r, &o, c)
	if !goes {
		return o
	}
	strArgs := map[string]string{}
	for k, v := range args {
		if sv, isStr := v.(string); isStr {
			strArgs[k] = sv
		} else {
			strArgs[k] = fmt.Sprint(v)
		}
	}
	upArgs, err := l.args.reverse(strArgs)
	if err != nil {
		o.Error = &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		return o
	}
	fp := &mcp.GetPromptParams{Name: l.upstreamID, Arguments: upArgs}
	if !e.send(ctx, r, &o, c, pass, fp, p.Meta, func(meta mcp.Meta, state string, answers mcp.InputResponseMap) {
		fp.Meta, fp.RequestState, fp.InputResponses = meta, state, answers
	}) {
		return o
	}
	o.Call = &CallContext{Server: s, Method: "prompts/get", Target: l.upstreamID, Exposed: l.exposedID, Item: l.value, Params: callParams(l.exposedID, copyMap(args), p.Meta), Args: toAnyMap(upArgs), Hash: pass.hash, State: pass.state, Confirmed: pass.confirmed, resolve: resolve}
	return o
}

// UpstreamPromptReply handles the upstream's answer to a forwarded
// prompts/get, using the CallContext GetPrompt returned with the forward.
func (e *Engine) UpstreamPromptReply(parent context.Context, r Request, call *CallContext, p *mcp.GetPromptParams, raw json.RawMessage) (ret Outcome) {
	defer e.recovered(&ret, "prompts/get reply", promptCallRef(p))
	ctx, cancel := e.ctx(parent)
	defer cancel()
	if call == nil {
		return errOutcome(-32603, "no forwarded request to reply to")
	}
	if len(raw) > e.Limits.maxBytes() {
		return errOutcome(CodeTooLarge, "upstream result exceeds gateway size limit")
	}
	s := call.Server
	var o Outcome
	m, ok := decodeExact(raw).(map[string]any)
	if !ok {
		return errOutcome(-32603, "upstream result is not a JSON object")
	}
	var seen unknowns
	defer func() { ret.Unknown = append(ret.Unknown, seen.list()...) }()
	if rt, _ := m["resultType"].(string); rt == "task" {
		return refuseTask(e, ctx, &seen, o, s.Slug, call.Target, m)
	} else if rt != "" && rt != "complete" && rt != "input_required" {
		seen.add(UnknownResultType, rt, s.Slug, call.Target, "passed", m)
	}
	inputUnknowns(&seen, s.Slug, call.Target, m)
	if msgs, ok := m["messages"].([]any); ok {
		for _, x := range msgs {
			if mm, ok := x.(map[string]any); ok {
				// A prompt message holds one content block, or a list of them.
				if list, isList := mm["content"].([]any); isList {
					contentUnknowns(&seen, s.Slug, call.Target, list)
				} else if mm["content"] != nil {
					contentUnknowns(&seen, s.Slug, call.Target, []any{mm["content"]})
				}
			}
		}
	}
	if isInputRequired(m) {
		if msg, ok := inputAllowed(r, s); !ok {
			o.Issues = append(o.Issues, s.Slug+": "+call.Target+" asked for input and was refused: "+msg)
			o.Error = &RPCError{Code: CodeInvalidParams, Message: msg}
			return e.finishOutcome(ctx, o)
		}
		out, hits, err := e.elicitHook(ctx, r, call, "prompts/get.post.elicit", call.Exposed, replyResolve(call), rawRequests(m))
		o.Hits = append(o.Hits, hits...)
		if err != nil {
			o.Error = &RPCError{Code: CodeInvalidParams, Message: err.Error()}
			return e.finishOutcome(ctx, o)
		}
		upState, _ := m["requestState"].(string)
		st, err := e.wrapState(r, s, call, p.RequestState, upState, out)
		if err != nil {
			o.Error = &RPCError{Code: CodeInvalidParams, Message: err.Error()}
			return e.finishOutcome(ctx, o)
		}
		o.Result = InputRequired{InputRequests: out, RequestState: st}
		return e.finishOutcome(ctx, o)
	}
	if r.mux() {
		m = prefixResultURIs(m, s.Slug)
	}
	o.Result = complete(m)
	return e.finishOutcome(ctx, o)
}

func toAnyMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

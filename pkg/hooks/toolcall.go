package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools: the listing, and tools/call on its way to the server and back.
// How a tool is shaped for a server is in tools.go.

// ListedTools is a tools/list result plus the gateway's working data.
type ListedTools struct {
	Result *mcp.ListToolsResult `json:"result"`
	Hits   []Hit                `json:"hits,omitempty"`
	Issues []string             `json:"issues,omitempty"`
}

// ListTools lists every server's exposed tools. On the mux, names are
// prefixed slug__name. Output is ordered by slug a-z, then name a-z.
func (e *Engine) ListTools(parent context.Context, r Request) (ret Outcome) {
	defer e.recovered(&ret, "tools/list", callRef{method: "tools/list"})
	ctx, cancel := e.ctx(parent)
	defer cancel()
	res := &mcp.ListToolsResult{Tools: []*mcp.Tool{}}
	res.TTLMs, res.CacheScope = e.Limits.ttl(), "private"
	var o Outcome
	servers := sortedServers(r.Servers)
	for _, s := range servers {
		if !e.config(ctx, r, s, "tools", &o) {
			continue
		}
		tl := s.Rules.listTools(ctx, s.Catalog.Tools, r.context(s), e.Limits.Workers, "")
		o.Hits = append(o.Hits, tl.Hits...)
		for _, is := range tl.Issues {
			o.Issues = append(o.Issues, s.Slug+": "+is)
		}
		for _, et := range tl.Tools {
			t := et.Tool
			if r.mux() {
				c := *t
				c.Name = s.Slug + Separator + t.Name
				t = &c
			}
			res.Tools = append(res.Tools, t)
		}
	}
	if r.plainMux() {
		res.Tools = append(res.Tools, instructionsTool())
	}
	o.Result = res
	return e.finishOutcome(ctx, o)
}

// resolveTool finds a tool by exposed name on the route. It runs filter and
// modify again (rules are never cached), so the mapping is always current.
func (e *Engine) resolveTool(ctx context.Context, r Request, name string) (*Server, *ExposedTool, []Hit, error) {
	slug := ""
	if r.mux() {
		var ok bool
		slug, name, ok = strings.Cut(name, Separator)
		if !ok || slug == "" || name == "" {
			return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: "tool name must be <slug>" + Separator + "<tool>"}
		}
	} else if len(r.Servers) == 1 {
		slug = r.Servers[0].Slug
	}
	s := r.server(slug)
	if s == nil {
		return nil, nil, nil, &RPCError{Code: CodeUnknownTool, Message: "unknown tool " + name}
	}
	var o Outcome
	if !e.config(ctx, r, s, "tools", &o) {
		return nil, nil, o.Hits, &RPCError{Code: CodeUnknownTool, Message: "unknown tool " + name}
	}
	only := ""
	if !s.Rules.renames() {
		// No rule can rename a tool, so the exposed name is the upstream
		// name: evaluate just that tool (and any duplicates of it).
		only = name
	}
	tl := s.Rules.listTools(ctx, s.Catalog.Tools, r.context(s), e.Limits.Workers, only)
	for i := range tl.Tools {
		if tl.Tools[i].Tool.Name == name {
			return s, &tl.Tools[i], append(o.Hits, tl.Hits...), nil
		}
	}
	return s, nil, append(o.Hits, tl.Hits...), &RPCError{Code: CodeUnknownTool, Message: "unknown tool " + name}
}

func upstreamTool(s *Server, name string) *mcp.Tool {
	for _, t := range s.Catalog.Tools {
		if t.Name == name {
			return t
		}
	}
	return &mcp.Tool{Name: name}
}

// CallTool runs the call side of a tools/call: resolve, effective arguments,
// call rules, reversal. It returns either a result for the client (a deny,
// an elicitation, an error) or the request to forward upstream.
func (e *Engine) CallTool(parent context.Context, r Request, p *mcp.CallToolParamsRaw) (ret Outcome) {
	defer e.recovered(&ret, "tools/call", toolCallRef(p))
	ctx, cancel := e.ctx(parent)
	defer cancel()
	s, et, hits, err := e.resolveTool(ctx, r, p.Name)
	if err != nil {
		var re *RPCError
		errors.As(err, &re)
		return Outcome{Error: re, Hits: hits}
	}
	o := Outcome{Hits: hits}
	in, err := decodeArgs(p.Arguments)
	if err != nil {
		o.Error = &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		return o
	}
	tr := et.Transform
	args, issue, err := tr.effectiveArgs(in)
	if err != nil {
		o.Error = &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		return o
	}
	if issue != "" {
		o.Issues = append(o.Issues, s.Slug+": "+issue)
	}
	var ept *preparedTool
	if et.Changed {
		ept = buildPrepared(et.Tool) // a per-request object: do not cache it
	} else {
		ept = prepare(et.Tool)
	}
	ts := newToolState(ept)
	upBase := prepare(upstreamTool(s, tr.Upstream)).base
	resolve := func(name string) (any, bool) {
		if name == "upstream" {
			return upBase, true
		}
		return ts.resolve(name)
	}
	roots := r.context(s).roots("tools/call", callParams(tr.Exposed, celArgs(args), p.Meta))
	var timeout time.Duration
	c := preCall{
		server: s, method: "tools/call", hook: "tools/call.pre", target: tr.Upstream, label: tr.Exposed,
		args: args, state: p.RequestState, answers: p.InputResponses, resolve: resolve, roots: roots,
		// A refused call is an error result, not a JSON-RPC error, so the
		// model can read the reason and change its arguments.
		refuse: func(o *Outcome, message string) { o.Result = toolError(message) },
		denied: toolRefused(tr.Exposed),
		set: func(key string, val any) error {
			parts := splitPath(key)
			switch parts[0] {
			case "arguments":
				if err := tr.patchable(parts[1]); err != nil {
					return err
				}
				args[parts[1]] = val
				setParam(roots, "arguments", celArgs(args))
			case "timeout":
				timeout = val.(time.Duration)
			}
			return nil
		},
	}
	pass, goes := e.pre(ctx, r, &o, c)
	if !goes {
		return o
	}
	// Pins always win, including over call-rule patches.
	for k, pv := range tr.VisiblePins {
		args[k] = pv
	}
	if err := tr.checkConstraints(args); err != nil {
		o.Error = &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		return o
	}
	upArgs := tr.upstreamArgs(args)
	if missing := tr.missingRequired(upArgs); len(missing) > 0 {
		o.Result = toolError(fmt.Sprintf("The call to %s was refused: required parameters were not supplied by policy.", tr.Exposed))
		o.Issues = append(o.Issues, fmt.Sprintf("%s: hidden required parameters %v were not set by any rule", s.Slug, missing))
		return o
	}
	fp := &mcp.CallToolParams{Name: tr.Upstream, Arguments: upArgs}
	if !e.send(ctx, r, &o, c, pass, fp, p.Meta, func(meta mcp.Meta, state string, answers mcp.InputResponseMap) {
		fp.Meta, fp.RequestState, fp.InputResponses = meta, state, answers
	}) {
		return o
	}
	if timeout > 0 {
		o.Forward.Timeout = timeout.String()
	}
	o.Call = &CallContext{Server: s, Method: "tools/call", Target: tr.Upstream, Exposed: tr.Exposed, Transform: tr, Params: callParams(tr.Exposed, celArgs(args), p.Meta), Args: upArgs, Hash: pass.hash, State: pass.state, Confirmed: pass.confirmed, resolve: resolve}
	return o
}

// decodeArgs parses arguments keeping numbers exact (big integers survive)
// and rejecting duplicate keys, which different JSON parsers resolve
// differently.
func decodeArgs(raw json.RawMessage) (map[string]any, error) {
	if t := jsonTrim(string(raw)); t == "" || t == "null" {
		if t == "" && len(raw) > 0 {
			return nil, errors.New("arguments: not JSON")
		}
		return map[string]any{}, nil
	}
	if err := noDuplicateKeys(raw); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("arguments: %w", err)
	}
	if jsonTrim(string(raw[dec.InputOffset():])) != "" {
		return nil, errors.New("arguments: unexpected data after the JSON object")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("arguments must be an object")
	}
	return m, nil
}

func noDuplicateKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	type frame struct {
		obj  bool
		keys map[string]bool
		key  bool // next token is a key
	}
	var stack []*frame
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("arguments: %w", err)
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				if top != nil && top.obj {
					top.key = true
				}
				if len(stack) >= MaxArgDepth {
					return fmt.Errorf("arguments: nested deeper than %d levels", MaxArgDepth)
				}
				stack = append(stack, &frame{obj: true, keys: map[string]bool{}, key: true})
				continue
			case '[':
				if top != nil && top.obj {
					top.key = true
				}
				if len(stack) >= MaxArgDepth {
					return fmt.Errorf("arguments: nested deeper than %d levels", MaxArgDepth)
				}
				stack = append(stack, &frame{})
				continue
			case '}', ']':
				stack = stack[:len(stack)-1]
				continue
			}
		case string:
			if top != nil && top.obj && top.key {
				if top.keys[t] {
					return fmt.Errorf("arguments: duplicate key %q", t)
				}
				top.keys[t] = true
				top.key = false
				continue
			}
		}
		if top != nil && top.obj {
			top.key = true
		}
	}
}

// toolRefused is what a client is told when a rule refuses a tool call or
// its result and gives no reason.
func toolRefused(tool string) string {
	return "The call to " + tool + " was refused by policy."
}

func toolError(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// UpstreamToolReply handles what the upstream answered to a forwarded
// tools/call. call is the CallContext CallTool returned with the forward:
// reply hooks see exactly the arguments that were sent. An input_required
// (with input requests, state, or both) goes through tools/call.post.elicit and is
// wrapped in gateway state; a result goes through tools/call.post.result and then,
// on the mux, gets the slug prefix on structured URIs (last, so URIs rules
// add are prefixed too). p is the client's call, for its request state.
func (e *Engine) UpstreamToolReply(parent context.Context, r Request, call *CallContext, p *mcp.CallToolParamsRaw, raw json.RawMessage) (ret Outcome) {
	defer e.recovered(&ret, "tools/call reply", toolCallRef(p))
	ctx, cancel := e.ctx(parent)
	defer cancel()
	if call == nil || call.Transform == nil {
		return errOutcome(-32603, "no forwarded call to reply to")
	}
	if len(raw) > e.Limits.maxBytes() {
		return errOutcome(CodeTooLarge, "upstream result exceeds gateway size limit")
	}
	s, tr := call.Server, call.Transform
	var o Outcome
	result, ok := decodeExact(raw).(map[string]any)
	if !ok {
		return errOutcome(-32603, "upstream result is not a JSON object")
	}
	itemResolve := replyResolve(call)
	var seen unknowns
	defer func() { ret.Unknown = append(ret.Unknown, seen.list()...) }()
	if rt, _ := result["resultType"].(string); rt != "" && rt != "complete" && rt != "input_required" {
		action := "passed"
		if rt == "task" {
			action = "refused"
		}
		seen.add(UnknownResultType, rt, s.Slug, tr.Upstream, action, result)
	}
	inputUnknowns(&seen, s.Slug, tr.Upstream, result)
	contentUnknowns(&seen, s.Slug, tr.Upstream, result["content"])
	if rt, _ := result["resultType"].(string); rt == "task" {
		// Tasks are an extension the gateway does not support and never
		// declares upstream. A handle returned anyway is refused: its result
		// would later be fetched without passing through any result rule.
		o.Issues = append(o.Issues, s.Slug+": "+tr.Upstream+" returned a task handle; tasks are not supported, so it was refused")
		o.Result = toolError("This tool started a background task, which this gateway does not support.")
		return e.finishOutcome(ctx, o)
	}
	if isInputRequired(result) {
		if msg, ok := inputAllowed(r, s); !ok {
			o.Issues = append(o.Issues, s.Slug+": "+tr.Upstream+" asked for input and was refused: "+msg)
			o.Result = toolError(msg)
			return e.finishOutcome(ctx, o)
		}
		reqs, hits, err := e.elicitHook(ctx, r, call, "tools/call.post.elicit", tr.Exposed, itemResolve, rawRequests(result))
		o.Hits = append(o.Hits, hits...)
		if err != nil {
			o.Result = toolError(err.Error())
			return e.finishOutcome(ctx, o)
		}
		upState, _ := result["requestState"].(string)
		st, err := e.wrapState(r, s, call, p.RequestState, upState, reqs)
		if err != nil {
			o.Result = toolError(err.Error())
			return e.finishOutcome(ctx, o)
		}
		o.Result = InputRequired{InputRequests: reqs, RequestState: st}
		return e.finishOutcome(ctx, o)
	}
	view := NewView(result)
	roots := call.roots(r)
	upstream := map[string]any{"result": celMap(result)}
	roots["response"] = map[string]any{"result": upstream["result"], "upstream": upstream}
	setKeys := map[string]bool{}
	ro := s.Rules.run(ctx, "tools/call.post.result", tr.Exposed, itemResolve, roots, func(key string, val any) error {
		if err := wellFormedResult(key, val); err != nil {
			return err
		}
		setKeys[key] = true
		view.Set([]string{key}, val)
		roots["response"] = map[string]any{"result": celMap(view.Materialize()), "upstream": upstream}
		return nil
	})
	o.Hits = append(o.Hits, ro.hits...)
	if ro.denied {
		o.Result = toolError(denyOr(ro.reason, toolRefused(tr.Exposed)))
		return e.finishOutcome(ctx, o)
	}
	final := view.Materialize()
	if v, ok := final["structuredContent"]; ok && v == nil && setKeys["structuredContent"] {
		final = copyMap(final)
		delete(final, "structuredContent") // a rule set it to null: plain output only
	}
	if r.mux() {
		final = prefixResultURIs(final, s.Slug)
	}
	o.Result = complete(final)
	return e.finishOutcome(ctx, o)
}

// wellFormedResult refuses a value a rule sets on a tool result that MCP
// would not accept there: content is a list of blocks, each a map with a
// type, and structuredContent is an object or nothing.
func wellFormedResult(key string, val any) error {
	switch key {
	case "content":
		for i, c := range val.([]any) {
			m, ok := c.(map[string]any)
			if t, isStr := m["type"].(string); !ok || !isStr || t == "" {
				return fmt.Errorf("content[%d] must be a content block: a map with a type", i)
			}
		}
	case "structuredContent":
		if _, ok := val.(map[string]any); !ok && val != nil {
			return fmt.Errorf("structuredContent must be an object")
		}
	}
	return nil
}

package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A reply is a stream of events: zero or more notifications, then one final
// response. An upstream that answers with a single JSON body is a stream of
// one event. The gateway pulls events from the upstream and hands them to
// the engine; the engine yields what the client should receive.
//
// Each event is routed by its type to the hook for the request's family:
//
//	notifications/progress  ->  tools/progress, prompts/progress, resources/progress
//	the final response      ->  the existing reply path, which picks tools/result,
//	                            tools/input and so on from the result's type
//	anything else           ->  dropped
//
// Progress is the only notification type handled today. A new type is a new
// case in route and a new hook; nothing else changes.
//
// Nothing sent can be taken back. A rule on an event can drop or reshape
// that event only; it can never refuse the call. The final response still
// fails closed as it always has.

// ProgressMethod is the one notification type the engine relays.
const ProgressMethod = "notifications/progress"

// UpstreamEvent is one message an upstream sent in answer to a forwarded
// request.
type UpstreamEvent struct {
	Method string          // a notification's method; empty for the final response
	Params json.RawMessage // a notification's params
	Result json.RawMessage // the final result, as the upstream sent it
	Error  *RPCError       // the final error, when the upstream answered with one
}

// ReplyEvent is one message for the client.
type ReplyEvent struct {
	Method string          // a notification to relay; empty for the final outcome
	Params json.RawMessage // the notification's params, as rules left them
	Final  *Outcome        // the final outcome; set on the last event only
}

// ErrNoResponse is the cause recorded when the upstream's stream ended
// without a final response.
var ErrNoResponse = errors.New("the upstream stream ended without a response")

// CodeUpstreamLost is the JSON-RPC code for a reply that was lost: the
// upstream's stream broke, or ended, before its final response. The request
// may or may not have run. The gateway sends this error and then closes the
// client's stream, so no client is left waiting for a response that will
// never come.
const CodeUpstreamLost = -32014

// lost is the final outcome for a reply that never completed. The client is
// told the reply was lost and that the request may have run; the cause goes
// to the administrator, not the client.
func lost(cause error, ns *notifications) *Outcome {
	out := &Outcome{Error: &RPCError{Code: CodeUpstreamLost, Message: "The server's reply was lost before it completed. The request may or may not have run."}}
	out.Hits = ns.hits
	out.Issues = append([]string{"upstream reply lost: " + cause.Error()}, ns.issues()...)
	return out
}

// ToolReplyStream handles the upstream's answer to a forwarded tools/call,
// event by event. It is UpstreamToolReply for a streamed reply.
func (e *Engine) ToolReplyStream(parent context.Context, r Request, call *CallContext, p *mcp.CallToolParamsRaw, upstream iter.Seq2[UpstreamEvent, error]) iter.Seq2[ReplyEvent, error] {
	if p == nil {
		p = &mcp.CallToolParamsRaw{}
	}
	return e.replyStream(parent, r, call, replyKind{
		op: "tools/call reply", ref: toolCallRef(p), hook: "tools/progress",
		resolve: func() func(string) (any, bool) { return toolReplyResolve(call) },
		final:   func(raw json.RawMessage) Outcome { return e.UpstreamToolReply(parent, r, call, p, raw) },
	}, upstream)
}

// PromptReplyStream handles the upstream's answer to a forwarded
// prompts/get, event by event.
func (e *Engine) PromptReplyStream(parent context.Context, r Request, call *CallContext, p *mcp.GetPromptParams, upstream iter.Seq2[UpstreamEvent, error]) iter.Seq2[ReplyEvent, error] {
	if p == nil {
		p = &mcp.GetPromptParams{}
	}
	return e.replyStream(parent, r, call, replyKind{
		op: "prompts/get reply", ref: promptCallRef(p), hook: "prompts/progress",
		resolve: func() func(string) (any, bool) {
			it := &item{kind: KindPrompt, view: NewView(call.Item), args: newArgState(call.Item)}
			return it.resolve
		},
		final: func(raw json.RawMessage) Outcome { return e.UpstreamPromptReply(parent, r, call, p, raw) },
	}, upstream)
}

// ReadReplyStream handles the upstream's answer to a forwarded resource or
// directory read, event by event.
func (e *Engine) ReadReplyStream(parent context.Context, r Request, call *CallContext, p *mcp.ReadResourceParams, upstream iter.Seq2[UpstreamEvent, error]) iter.Seq2[ReplyEvent, error] {
	if p == nil {
		p = &mcp.ReadResourceParams{}
	}
	return e.replyStream(parent, r, call, replyKind{
		op: "resources/read reply", ref: readCallRef("resources/read", p), hook: "resources/progress",
		resolve: func() func(string) (any, bool) {
			it := &item{kind: KindResource, id: call.Target, view: NewView(call.Item), label: call.Target}
			return it.resolve
		},
		final: func(raw json.RawMessage) Outcome { return e.UpstreamReadReply(parent, r, call, p, raw) },
	}, upstream)
}

// replyKind is what differs between the three request families.
type replyKind struct {
	op      string                          // for fault records
	ref     callRef                         // the request, for fault records
	hook    string                          // the family's progress hook
	resolve func() func(string) (any, bool) // the item's fields, for rules
	final   func(json.RawMessage) Outcome   // the existing path for the final result
}

func (e *Engine) replyStream(parent context.Context, r Request, call *CallContext, k replyKind, upstream iter.Seq2[UpstreamEvent, error]) iter.Seq2[ReplyEvent, error] {
	return func(yield func(ReplyEvent, error) bool) {
		// The consumer's loop body runs inside yield. A panic there is the
		// consumer's, and must pass through untouched; a panic anywhere else
		// is the engine's, and becomes a refusal.
		inYield, stopped := false, false
		emit := func(ev ReplyEvent, err error) bool {
			inYield = true
			ok := yield(ev, err)
			inYield = false
			if !ok {
				stopped = true
			}
			return ok
		}
		var fault *Outcome
		func() {
			defer func() {
				p := recover()
				if p == nil {
					return
				}
				if inYield {
					panic(p)
				}
				ep := capture(p)
				f := &Fault{Op: k.op, Panic: fmt.Sprint(ep.val), Stack: string(ep.stack), Rule: ep.rule, Call: k.ref.fault()}
				fault = &Outcome{Error: refusal(), Issues: []string{"panic in " + k.op + ": " + f.Panic}, Fault: f}
				e.report(f)
			}()
			e.pump(parent, r, call, k, upstream, emit)
		}()
		if fault != nil && !stopped {
			emit(ReplyEvent{Final: fault}, nil)
		}
	}
}

// pump routes each upstream event and stops at the final response.
func (e *Engine) pump(parent context.Context, r Request, call *CallContext, k replyKind, upstream iter.Seq2[UpstreamEvent, error], emit func(ReplyEvent, error) bool) {
	ns := &notifications{e: e, r: r, call: call, kind: k, parent: parent}
	for ev, err := range upstream {
		if err != nil {
			emit(ReplyEvent{Final: lost(err, ns)}, nil)
			return
		}
		if ev.Method == "" {
			var out Outcome
			if ev.Error != nil {
				out = Outcome{Error: ev.Error} // the upstream's own error passes through
			} else {
				out = k.final(ev.Result)
			}
			out.Hits = append(ns.hits, out.Hits...)
			out.Issues = append(out.Issues, ns.issues()...)
			if found := ns.seen.list(); len(found) > 0 {
				out.Unknown = append(out.Unknown, found...)
				e.reportUnknowns(found)
			}
			emit(ReplyEvent{Final: &out}, nil)
			return // anything after the response is ignored
		}
		if params, ok := ns.route(ev); ok {
			if !emit(ReplyEvent{Method: ev.Method, Params: params}, nil) {
				return // the client went away; the upstream read stops with us
			}
		}
	}
	emit(ReplyEvent{Final: lost(ErrNoResponse, ns)}, nil)
}

// notifications holds what the events of one reply share: whether progress
// is allowed, the rules' own time allowance, and what was dropped.
type notifications struct {
	e      *Engine
	r      Request
	call   *CallContext
	kind   replyKind
	parent context.Context

	ready     bool
	ctx       context.Context // carries the progress allowance
	on        bool            // the server/config progress switch
	rules     bool            // the family's progress hook has rules
	resolve   func(string) (any, bool)
	exhausted bool // the allowance is spent; later events are dropped unseen

	hits    []Hit
	seen    unknowns       // what the engine did not recognise, by shape
	unknown map[string]int // dropped notifications of types the engine does not relay
	spent   int            // progress events dropped after the allowance ran out
	invalid int            // progress events that were not usable
}

func (n *notifications) init() {
	n.ready = true
	if n.call == nil || n.call.Server == nil {
		return
	}
	parent := n.parent
	if parent == nil {
		parent = context.Background()
	}
	// Progress rules have their own allowance, as audit rules do. A server
	// that sends a great many events spends this, never the budget of the
	// final response.
	n.ctx = withBudget(parent, n.e.Limits.progressBudget(), n.e.Limits.auditBudget())
	sw, _ := n.e.Config(n.ctx, n.r, n.call.Server)
	n.on = sw["progress"]
	n.rules = n.call.Server.Rules.has(n.kind.hook)
}

// route decides what happens to one notification. It returns the params to
// relay, or false to drop the event.
func (n *notifications) route(ev UpstreamEvent) (json.RawMessage, bool) {
	if !n.ready {
		n.init()
	}
	switch ev.Method {
	case ProgressMethod:
		return n.progress(ev.Params)
	default:
		// Log messages and anything the engine does not know are dropped.
		if n.unknown == nil {
			n.unknown = map[string]int{}
		}
		n.unknown[ev.Method]++
		// A log message is a known type the gateway chooses not to relay.
		// Anything else is recorded by shape, for investigation.
		if ev.Method != "notifications/message" && n.call != nil && n.call.Server != nil {
			var sample any
			if n.unknown[ev.Method] == 1 && len(ev.Params) > 0 && len(ev.Params) <= 64<<10 {
				sample = decodeExact(ev.Params)
			}
			n.seen.add(UnknownNotification, ev.Method, n.call.Server.Slug, n.label(), "dropped", sample)
		}
		return nil, false
	}
}

// progress runs the family's progress hook over one event. A rule can drop
// the event or rewrite its message; it cannot touch the numbers or the
// token. A rule that errors drops the event.
func (n *notifications) progress(params json.RawMessage) (json.RawMessage, bool) {
	if !n.on {
		return nil, false
	}
	if len(params) > n.e.Limits.maxBytes() {
		n.invalid++
		return nil, false
	}
	if !n.rules {
		return params, true // nothing to evaluate: relay as received
	}
	if n.exhausted {
		n.spent++
		return nil, false
	}
	m, ok := decodeExact(params).(map[string]any)
	if !ok {
		n.invalid++
		return nil, false
	}
	if n.resolve == nil {
		n.resolve = n.kind.resolve()
	}
	s := n.call.Server
	msg, _ := m["message"].(string)
	pv := map[string]any{"message": msg, "progress": m["progress"]}
	if t, ok := m["total"]; ok {
		pv["total"] = t
	}
	vars := n.r.context(s).vars()
	vars["args"] = celArgs(n.call.Args)
	vars["progress"] = pv
	changed := false
	o := s.Rules.run(n.ctx, n.kind.hook, n.label(), n.resolve, vars, func(key string, val any) error {
		if key == "message" {
			msg, changed = val.(string), true
			pv = copyMap(pv)
			pv["message"] = msg
			vars["progress"] = pv
		}
		return nil
	})
	n.hits = append(n.hits, o.hits...)
	if o.err != nil {
		if errors.Is(o.err, errBudget) || expired(n.ctx) {
			n.exhausted = true
			n.spent++
		}
		return nil, false
	}
	if o.denied {
		return nil, false
	}
	if !changed {
		return params, true
	}
	out := copyMap(m)
	out["message"] = msg
	b, err := json.Marshal(out)
	if err != nil {
		n.invalid++
		return nil, false
	}
	return b, true
}

func (n *notifications) label() string {
	if n.call.Exposed != "" {
		return n.call.Exposed
	}
	return n.call.Target
}

// issues summarizes what was dropped, for the administrator.
func (n *notifications) issues() []string {
	var out []string
	for _, m := range sortedKeys(n.unknown) {
		out = append(out, fmt.Sprintf("dropped %d %s notifications: this type is not relayed", n.unknown[m], m))
	}
	if n.spent > 0 {
		out = append(out, fmt.Sprintf("dropped %d progress events: the progress rules' time allowance was spent", n.spent))
	}
	if n.invalid > 0 {
		out = append(out, fmt.Sprintf("dropped %d progress events: malformed or too large", n.invalid))
	}
	return out
}

// toolReplyResolve gives rules the tool's fields on the reply side, as the
// result hook sees them.
func toolReplyResolve(call *CallContext) func(string) (any, bool) {
	exposed := prepare(upstreamTool(call.Server, call.Transform.Upstream))
	return func(name string) (any, bool) {
		if name == "upstream" {
			return exposed.derived, true
		}
		if toolFieldSet[name] {
			return exposed.derived[name], true
		}
		return nil, false
	}
}

// has reports whether a hook has any rules.
func (s *Set) has(hook string) bool {
	return s != nil && len(s.byHook[hook]) > 0
}

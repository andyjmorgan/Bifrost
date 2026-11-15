package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The engine must never take its process down. Every entry point recovers a
// panic, refuses that one request and returns a Fault for the gateway to
// log. Worker goroutines recover their own panics and hand them back to the
// entry point, because a panic on any other goroutine cannot be recovered
// from here and would end the process.

// CodeInternal is the JSON-RPC code for a request the engine could not
// complete.
const CodeInternal = -32603

// FaultArgsCap bounds the arguments kept in a fault record.
const FaultArgsCap = 64 << 10

// Fault records a panic the engine caught. The request was refused; the
// record is for the gateway's log, so the rule and the call can be inspected
// and the panic reproduced. Arguments may hold customer data: send faults to
// a restricted log.
type Fault struct {
	Op    string     `json:"op"`              // the entry point, for example tools/call
	Panic string     `json:"panic"`           // the panic value
	Stack string     `json:"stack,omitempty"` // where it happened; empty when CEL caught it inside a rule
	Rule  *FaultRule `json:"rule,omitempty"`  // the rule being evaluated, when there was one
	Call  *FaultCall `json:"call,omitempty"`  // the request, when the entry point has one
}

// FaultRule is the rule a panic happened in, with its source text.
type FaultRule struct {
	Hook  string            `json:"hook"`
	Label string            `json:"label"`
	Item  string            `json:"item,omitempty"`
	When  string            `json:"when"`
	Then  map[string]string `json:"then,omitempty"`
}

// FaultCall is the request a panic happened in.
type FaultCall struct {
	Method    string `json:"method"`
	Target    string `json:"target,omitempty"`    // tool or prompt name, or resource URI, as the client sent it
	Arguments string `json:"arguments,omitempty"` // as received, cut at FaultArgsCap
	Truncated bool   `json:"truncated,omitempty"`
}

// enginePanic carries a recovered panic up to the entry point, with the
// stack of the goroutine it happened on and the rule in play, if any.
type enginePanic struct {
	val   any
	stack []byte
	rule  *FaultRule
}

// capture wraps a recovered value once; a value that is already wrapped
// keeps its original stack.
func capture(p any) *enginePanic {
	if ep, ok := p.(*enginePanic); ok {
		return ep
	}
	return &enginePanic{val: p, stack: debug.Stack()}
}

func faultRule(hook, item string, r *compiledRule) *FaultRule {
	if r == nil {
		return nil
	}
	return &FaultRule{Hook: hook, Label: r.label, Item: item, When: r.rule.When, Then: r.rule.Then}
}

// celPanic reports an evaluation error that is really a panic: CEL recovers
// panics inside a program, including in the engine's own helpers, and
// returns them with this prefix.
func celPanic(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "internal error: ")
}

// callRef is the cheap, lazily rendered form of a FaultCall. Building one
// costs nothing on the normal path.
type callRef struct {
	method, target string
	args           json.RawMessage
	strArgs        map[string]string
}

func (c callRef) fault() *FaultCall {
	if c.method == "" {
		return nil
	}
	fc := &FaultCall{Method: c.method, Target: c.target}
	raw := []byte(c.args)
	if raw == nil && c.strArgs != nil {
		raw, _ = json.Marshal(c.strArgs)
	}
	if len(raw) > FaultArgsCap {
		raw, fc.Truncated = raw[:FaultArgsCap], true
	}
	fc.Arguments = strings.ToValidUTF8(string(raw), "")
	return fc
}

func toolCallRef(p *mcp.CallToolParamsRaw) callRef {
	if p == nil {
		return callRef{method: "tools/call"}
	}
	return callRef{method: "tools/call", target: p.Name, args: p.Arguments}
}

func promptCallRef(p *mcp.GetPromptParams) callRef {
	if p == nil {
		return callRef{method: "prompts/get"}
	}
	return callRef{method: "prompts/get", target: p.Name, strArgs: p.Arguments}
}

func readCallRef(method string, p *mcp.ReadResourceParams) callRef {
	if p == nil {
		return callRef{method: method}
	}
	return callRef{method: method, target: p.URI}
}

// refusal is what a client sees when the engine could not complete a
// request. It says nothing about why.
func refusal() *RPCError {
	return &RPCError{Code: CodeInternal, Message: "The request could not be completed."}
}

// recovered is deferred by every entry point that returns an Outcome. A
// panic becomes a refusal with a Fault. A panic CEL already caught inside a
// rule has refused the request as a rule error; it gets a Fault as well, so
// both kinds reach the log with the rule text and the call.
func (e *Engine) recovered(ret *Outcome, op string, call callRef) {
	if p := recover(); p != nil {
		ep := capture(p)
		f := &Fault{Op: op, Panic: fmt.Sprint(ep.val), Stack: string(ep.stack), Rule: ep.rule, Call: call.fault()}
		*ret = Outcome{Error: refusal(), Issues: []string{"panic in " + op + ": " + f.Panic}, Fault: f}
		e.report(f)
		return
	}
	if !payloadOps[op] {
		ret.Result = stampResult(ret.Result)
	}
	e.reportUnknowns(ret.Unknown)
	if ret.Fault != nil {
		return
	}
	for i := range ret.Hits {
		if h := &ret.Hits[i]; h.Panic {
			f := &Fault{Op: op, Panic: h.Error, Rule: h.rule, Call: call.fault()}
			ret.Fault = f
			ret.Issues = append(ret.Issues, "panic in a rule during "+op+": "+h.Error)
			e.report(f)
			return
		}
	}
}

// report hands a fault to the gateway's callback, if it set one. A callback
// that panics is ignored: reporting must not undo the recovery.
func (e *Engine) report(f *Fault) {
	if e == nil || e.OnFault == nil {
		return
	}
	defer func() { _ = recover() }()
	e.OnFault(*f)
}

// direct reports whether a server hook was called by the gateway itself
// rather than from inside an entry point. Entry points put the request
// budget in the context; they also recover, so nested calls let the panic
// travel up to where the call is known.
func direct(ctx context.Context) bool { return ctx == nil || budgetFrom(ctx) == nil }

// recoveredDirect is deferred by the server hooks the gateway may call on
// their own (Config, Modify, Connect). Nested inside an entry point it
// passes the panic up. Called directly it reports the fault and lets the
// caller's fail-closed values stand; failClosed sets those values.
func (e *Engine) recoveredDirect(ctx context.Context, op string, s *Server, failClosed func(Hit)) {
	p := recover()
	if p == nil {
		return
	}
	ep := capture(p)
	if !direct(ctx) {
		panic(ep)
	}
	slug := ""
	if s != nil {
		slug = s.Slug
	}
	f := &Fault{Op: op, Panic: fmt.Sprint(ep.val), Stack: string(ep.stack), Rule: ep.rule}
	e.report(f)
	failClosed(Hit{Hook: op, Rule: "(engine)", Item: slug, Mode: ModeEnforce, Effect: "error", Error: "panic: " + f.Panic, Panic: true, rule: ep.rule})
}

// payloadOps are the entry points whose result is the payload of one of the
// gateway's own search-mode tools, not an MCP result. The gateway wraps
// that payload in a tool result, and the wrapper carries the resultType.
var payloadOps = map[string]bool{"tools/search": true, "tools/definitions": true, "tools/list as text": true}

// stampResult marks a result as complete. The front door's protocol version
// requires resultType on every result, and the Python and TypeScript SDK
// clients reject a result without it. It runs on the way out of every entry
// point, so no path can forget it. A result that already has a type, such
// as an input request, keeps it.
func stampResult(v any) any {
	switch t := v.(type) {
	case nil, string, InputRequired, typedResult:
		return v
	case map[string]any:
		return complete(t)
	case *mcp.ListToolsResult:
		if t != nil && t.ResultType == "" {
			t.ResultType = "complete"
		}
		return t
	case *mcp.ListPromptsResult:
		if t != nil && t.ResultType == "" {
			t.ResultType = "complete"
		}
		return t
	case *mcp.ListResourcesResult:
		if t != nil && t.ResultType == "" {
			t.ResultType = "complete"
		}
		return t
	case *mcp.ListResourceTemplatesResult:
		if t != nil && t.ResultType == "" {
			t.ResultType = "complete"
		}
		return t
	case DiscoverResult:
		return t
	}
	return typedResult{v}
}

// typedResult adds resultType to a result whose Go type cannot carry one,
// such as the SDK's tool result, when it is written out.
type typedResult struct{ v any }

func (t typedResult) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(t.v)
	if err != nil || len(b) < 2 || b[0] != '{' {
		return b, err
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(b, &keys) != nil {
		return b, nil
	}
	if _, ok := keys["resultType"]; ok {
		return b, nil
	}
	if len(keys) == 0 {
		return []byte(`{"resultType":"complete"}`), nil
	}
	out := make([]byte, 0, len(b)+26)
	out = append(out, `{"resultType":"complete",`...)
	return append(out, b[1:]...), nil
}

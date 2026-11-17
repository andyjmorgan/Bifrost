package hooks

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// tools/call, prompts/get and resources/read reach a server by the same
// steps: take up an earlier round if the request continues one, run the
// pre hook's rules, ask the user if a rule says to, connect, and forward.
// preCall and send are those steps, written once. What differs between the
// three is how they name the item, what their arguments are and how they
// say no, and each says that in a preCall.

// preCall is one call on its way to a server, before its pre hook has run.
type preCall struct {
	server *Server
	method string // tools/call, prompts/get, resources/read or resources/directory/read
	hook   string // the pre hook
	target string // the server's name for the item, or the URI
	label  string // the item as the client names it, for the record
	// args is what a confirmation covers. A rule's set changes it.
	args map[string]any
	// state and answers are what the client sent back to continue an
	// earlier round, if it did.
	state   string
	answers mcp.InputResponseMap

	resolve func(string) (any, bool)
	roots   map[string]any
	set     setter

	// refuse tells the client no in this method's own way: a tool call
	// gives an error result the model can read, the others a JSON-RPC error.
	refuse func(o *Outcome, message string)
	// denied is what to say when a rule denies and gives no reason.
	denied string
}

// cleared is a call that passed its pre hook: what ties it to this request,
// for the rounds that may follow.
type cleared struct {
	hash      string // of the arguments the client sent
	state     *State
	confirmed map[string]bool
	// answers are the client's answers to a server's own question.
	answers mcp.InputResponseMap
}

// rpcRefusal is refuse for the methods that say no with a JSON-RPC error.
func rpcRefusal(o *Outcome, message string) {
	o.Error = &RPCError{Code: CodeInvalidParams, Message: message}
}

// pre takes a call through its pre hook. It reports false when the call
// goes no further: the outcome then holds the refusal, or the question the
// client is to put to the user.
func (e *Engine) pre(ctx context.Context, r Request, o *Outcome, c preCall) (cleared, bool) {
	pass := cleared{hash: argsHash(c.args)}
	st, confirmed, answers, serr := e.continuation(r, c.server, c.method, c.target, pass.hash, c.state, c.answers)
	if serr == errDeclined {
		o.Hits = append(o.Hits, declinedHit(c.hook, c.label))
		c.refuse(o, errDeclined.Message)
		return pass, false
	}
	if serr != nil {
		o.Error = serr
		return pass, false
	}
	pass.state, pass.confirmed, pass.answers = st, confirmed, answers
	run := c.server.Rules.runAsking(ctx, c.hook, c.label, c.resolve, c.roots, asked(confirmed, c.args), c.set)
	o.Hits = append(o.Hits, run.hits...)
	switch {
	case expired(ctx):
		*o = budgetOutcome(*o)
		return pass, false
	case run.denied:
		c.refuse(o, denyOr(run.reason, c.denied))
		return pass, false
	case run.elicit != nil:
		question, err := e.elicitation(r, c.server, c.method, c.target, pass.hash, st, confirmed, c.args, run.elicit)
		if err != nil {
			c.refuse(o, err.Error())
			return pass, false
		}
		o.Result = question
		return pass, false
	}
	return pass, true
}

// send is the last step: it runs the session rules and fills in what every
// forwarded call carries. carry sets, on the method's own params, the
// client's _meta when the session forwards it, and the state and answers
// of a server's own question when this round answers one. It reports false
// when the session rules spent what was left of the request's budget.
func (e *Engine) send(ctx context.Context, r Request, o *Outcome, c preCall, pass cleared, params any, clientMeta mcp.Meta, carry func(meta mcp.Meta, state string, answers mcp.InputResponseMap)) bool {
	session, hits := e.Connect(ctx, r, c.server, e.version())
	o.Hits = append(o.Hits, hits...)
	if expired(ctx) {
		*o = budgetOutcome(*o)
		return false
	}
	var meta mcp.Meta
	if session.ForwardMeta {
		meta = forwardMeta(clientMeta)
	}
	var state string
	var answers mcp.InputResponseMap
	if pass.state != nil && pass.state.UpstreamRound {
		state, answers = pass.state.Upstream, pass.answers
	}
	carry(meta, state, answers)
	o.Forward = &Forward{Slug: c.server.Slug, Method: c.method, Params: params, Connect: &session}
	return true
}

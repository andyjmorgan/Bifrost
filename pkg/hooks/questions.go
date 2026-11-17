package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Questions put to the user while a request waits. The gateway asks one
// when a rule says confirm. A server asks its own by replying
// input_required. Either way the request comes back as a new round, with
// the state the gateway kept and the answers. State and what a yes covers
// are in state.go; where state is kept is in statestore.go.

// InputRequired is an input_required result (2026-07-28 multi round trip).
type InputRequired struct {
	InputRequests mcp.InputRequestMap
	RequestState  string
}

// MarshalJSON writes the wire shape.
func (ir InputRequired) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"resultType": "input_required", "inputRequests": ir.InputRequests, "requestState": ir.RequestState})
}

// errNoPrincipal refuses a confirmation that could not be bound to anyone.
const errNoPrincipal = clientMessage("This request needs confirmation, which is not available without a caller identity.")

// errDeclined is returned when the user answered a gateway confirmation
// with anything but yes. The request stops here: it is never forwarded and
// no rule sees the answer.
var errDeclined = &RPCError{Code: CodeInvalidParams, Message: "Declined."}

// asked returns the check a pre hook's rules use to learn whether a
// question has been confirmed on this request. The engine decides that, not
// the rule: a rule only says when to ask and what. confirmed holds what the
// user has said yes to, each as a binding, so a yes counts only for the
// question it answered. The same key asked about other arguments, or with
// other words, is a new question and is asked.
func asked(confirmed map[string]bool, args map[string]any) func(map[string]any) bool {
	return func(marker map[string]any) bool { return confirmed[binding(marker, args)] }
}

// continuation validates a presented requestState and splits inputResponses
// into the gateway's and the upstream's. It returns what the user has
// confirmed on this request so far, as bindings.
func (e *Engine) continuation(r Request, s *Server, method, target, hash, token string, responses mcp.InputResponseMap) (*State, map[string]bool, mcp.InputResponseMap, *RPCError) {
	done := map[string]bool{}
	var upstream mcp.InputResponseMap
	gatewayKeys := false
	for k := range responses {
		if strings.HasPrefix(k, KeyPrefix) {
			gatewayKeys = true
		}
	}
	if token == "" {
		if gatewayKeys {
			return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: "input responses for the gateway require the gateway's request state"}
		}
		if len(responses) > 0 {
			return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: "input responses require a request state"}
		}
		return nil, done, nil, nil
	}
	// Taking the state removes it, so a question can be answered once,
	// whether the gateway asked it or the server did, and whether or not the
	// answer turns out to be valid.
	if !isStateKey(token) {
		return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: "invalid request state"}
	}
	st, ok := e.states().Take(token)
	if !ok {
		return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: "invalid request state"}
	}
	now := r.Now
	if now.IsZero() {
		now = time.Now()
	}
	switch {
	case now.Unix() > st.Expires:
		return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: "request state expired"}
	case st.Method != method || st.Target != target:
		return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: "request state belongs to another request"}
	case r.principal() == "" || st.Caller != r.principal() || st.Entitlement != entitlementID(s):
		return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: "request state belongs to another caller"}
	case st.Generation != generation(s):
		return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: "request state is from an older policy"}
	case st.ArgsHash != hash:
		return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: "arguments changed since the confirmation was requested"}
	}
	for _, b := range st.Confirmed {
		done[b] = true
	}
	pending := map[string]bool{}
	for _, k := range st.Pending {
		pending[k] = true
	}
	upPending := map[string]bool{}
	for _, k := range st.UpPending {
		upPending[k] = true
	}
	for k, v := range responses {
		if strings.HasPrefix(k, KeyPrefix) {
			key := strings.TrimPrefix(k, KeyPrefix)
			if !pending[key] {
				return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("no pending input request %q", k)}
			}
			if !confirmed(responseMap(v)) {
				return nil, nil, nil, errDeclined
			}
			done[st.Asks[key]] = true // a yes to this question, about these arguments
			continue
		}
		if !upPending[k] {
			return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("no pending input request %q", k)}
		}
		if upstream == nil {
			upstream = mcp.InputResponseMap{}
		}
		upstream[k] = v
	}
	return &st, done, upstream, nil
}

// confirmed reports a gateway confirmation answered with accept and the
// confirm box ticked. A decline, a cancel, an accept with the box unticked
// and an accept with no content are all a no.
func confirmed(answer map[string]any) bool {
	if answer["action"] != "accept" {
		return false
	}
	content, _ := answer["content"].(map[string]any)
	yes, _ := content["confirm"].(bool)
	return yes
}

// declinedHit records a refused confirmation for the audit trail.
func declinedHit(hook, item string) Hit {
	return Hit{Hook: hook, Rule: "(confirmation)", Item: item, Mode: ModeEnforce, Effect: "deny", Reason: "the user did not confirm"}
}

func responseMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}

// elicitation turns an elicit() marker into an input_required result, or an
// error when the client cannot elicit.
func bindings(confirmed map[string]bool) []string {
	out := make([]string, 0, len(confirmed))
	for b := range confirmed {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

func (e *Engine) elicitation(r Request, s *Server, method, target, hash string, prev *State, confirmed map[string]bool, args, marker map[string]any) (any, error) {
	key, _ := marker[ElicitKey].(string)
	msg, _ := marker["message"].(string)
	if prev != nil && prev.UpstreamRound {
		// This round answers the server's question, and the rules now ask
		// one they did not ask before the call was forwarded. Asking it would
		// lose the server's state and the user's answer to the server.
		return nil, clientMessage("This request needs a confirmation it did not need when it was first sent. Send it again.")
	}
	if !r.clientCan("elicitation") {
		if em, _ := marker["error"].(string); em != "" {
			return nil, clientMessage(em)
		}
		return nil, clientMessage(fmt.Sprintf("This request needs confirmation (%s), which this client does not support.", msg))
	}
	if r.principal() == "" {
		return nil, errNoPrincipal
	}
	now := r.Now
	if now.IsZero() {
		now = time.Now()
	}
	st := State{Challenge: fmt.Sprintf("%x", now.UnixNano()), Method: method, Caller: r.principal(), Entitlement: entitlementID(s),
		Target: target, ArgsHash: hash, Generation: generation(s), Expires: now.Add(StateTTL).Unix(), Pending: []string{key},
		Asks: map[string]string{key: binding(marker, args)}, Confirmed: bindings(confirmed)}
	reqs := mcp.InputRequestMap{KeyPrefix + key: &mcp.ElicitParams{
		Mode:    "form",
		Message: msg,
		RequestedSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"confirm": map[string]any{"type": "boolean", "title": "Confirm", "description": msg}},
			"required":   []string{"confirm"},
		},
	}}
	key = newStateKey()
	e.states().Put(key, st, StateTTL)
	return InputRequired{InputRequests: reqs, RequestState: key}, nil
}

// elicitHook runs an elicit hook over each question the upstream asks. A deny
// refuses the whole round; message changes apply to the request.
func (e *Engine) elicitHook(ctx context.Context, r Request, call *CallContext, hook, label string, resolve func(string) (any, bool), in map[string]json.RawMessage) (mcp.InputRequestMap, []Hit, error) {
	s := call.Server
	out := mcp.InputRequestMap{}
	var hits []Hit
	for _, key := range sortedKeys(in) {
		if strings.HasPrefix(key, KeyPrefix) {
			return nil, hits, fmt.Errorf("upstream input request %q uses the gateway's reserved prefix", key)
		}
		var wire struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(in[key], &wire); err != nil || wire.Params == nil {
			return nil, hits, fmt.Errorf("upstream input request %q is invalid", key)
		}
		if wire.Method != "elicitation/create" {
			return nil, hits, fmt.Errorf("upstream input request %q uses %q; only elicitation is supported", key, wire.Method)
		}
		view := NewView(copyMap(wire.Params))
		roots := call.roots(r)
		upstream := map[string]any{"params": celMap(wire.Params)}
		elicitation := func() map[string]any {
			return map[string]any{"key": key, "method": wire.Method, "params": celMap(view.Materialize()), "upstream": upstream}
		}
		roots["elicitation"] = elicitation()
		o := s.Rules.run(ctx, hook, label, resolve, roots, func(k string, val any) error {
			view.Set([]string{k}, val)
			roots["elicitation"] = elicitation()
			return nil
		})
		hits = append(hits, o.hits...)
		if o.denied {
			return nil, hits, fmt.Errorf("%s", denyOr(o.reason, "The upstream server asked for input, which policy does not allow."))
		}
		b, _ := json.Marshal(view.Materialize())
		var ep mcp.ElicitParams
		if err := json.Unmarshal(b, &ep); err != nil {
			return nil, hits, fmt.Errorf("upstream input request %q is not an elicitation", key)
		}
		out[key] = &ep
	}
	return out, hits, nil
}

// wrapState builds the gateway's envelope around an upstream round: the
// forwarded call's binding, earlier gateway answers, and the upstream's own
// state (which may be absent). It needs a policy generation.
func (e *Engine) wrapState(r Request, s *Server, call *CallContext, clientToken, upstreamState string, reqs mcp.InputRequestMap) (string, error) {
	if r.principal() == "" {
		return "", clientMessage("The server asked for input, which is not available without a caller identity.")
	}
	now := r.Now
	if now.IsZero() {
		now = timeNow()
	}
	st := State{Challenge: fmt.Sprintf("u%x", now.UnixNano()), Method: call.Method, Caller: r.principal(), Entitlement: entitlementID(s),
		Target: call.Target, ArgsHash: call.Hash, Generation: generation(s), Expires: now.Add(StateTTL).Unix(), Upstream: upstreamState, UpstreamRound: true}
	st.Confirmed = bindings(call.Confirmed)
	for k := range reqs {
		st.UpPending = append(st.UpPending, k)
	}
	sort.Strings(st.UpPending)
	key := newStateKey()
	e.states().Put(key, st, StateTTL)
	return key, nil
}

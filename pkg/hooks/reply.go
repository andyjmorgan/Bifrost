package hooks

import (
	"context"
	"encoding/json"
)

// What the three reply paths share: reading what a server sent back, and
// the last checks before a result leaves.

// isInputRequired reports a reply that asks the client for input: one
// whose resultType says so, or one that carries input requests without
// saying. A reply with state and no input requests counts only when its
// resultType says input_required.
func isInputRequired(m map[string]any) bool {
	if m["resultType"] == "input_required" {
		return true
	}
	if reqs, ok := m["inputRequests"].(map[string]any); ok && len(reqs) > 0 {
		return true
	}
	return false
}

func rawRequests(m map[string]any) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	reqs, _ := m["inputRequests"].(map[string]any)
	for k, v := range reqs {
		b, _ := json.Marshal(v)
		out[k] = b
	}
	return out
}

// inputAllowed reports whether an upstream's input request may be passed to
// this request's client, and what to say when it may not.
func inputAllowed(r Request, s *Server) (string, bool) {
	if !s.current() {
		return "The server asked for input, which the gateway only supports for servers on " + ProtocolCurrent + ".", false
	}
	if !r.clientCan("elicitation") {
		return "The server asked for input, which this client does not support.", false
	}
	return "", true
}

// inputUnknowns reports input requests that are not elicitations. The
// gateway refuses them: it can only relay a question it can show a rule.
func inputUnknowns(u *unknowns, server, target string, result map[string]any) {
	reqs, _ := result["inputRequests"].(map[string]any)
	for _, k := range sortedKeys(reqs) {
		m, _ := reqs[k].(map[string]any)
		if method, _ := m["method"].(string); method != "elicitation/create" {
			u.add(UnknownInputRequest, method, server, target, "refused", m["params"])
		}
	}
}

// refuseTask refuses a task handle returned for a prompt or a read. Tasks
// are an extension the gateway does not support and never declares
// upstream. A handle passed on would have its result fetched later, past
// every result rule and size limit, so it is refused for every kind of
// request, as it is for a tool call.
func refuseTask(e *Engine, ctx context.Context, seen *unknowns, o Outcome, slug, target string, reply map[string]any) Outcome {
	seen.add(UnknownResultType, "task", slug, target, "refused", reply)
	o.Issues = append(o.Issues, slug+": "+target+" returned a task handle; tasks are not supported, so it was refused")
	o.Error = &RPCError{Code: CodeInvalidParams, Message: "The server started a background task, which this gateway does not support."}
	return e.finishOutcome(ctx, o)
}

// complete marks a result as final. The front door's version requires
// resultType on every result; older servers do not send it.
func complete(m map[string]any) map[string]any {
	if _, ok := m["resultType"]; ok {
		return m
	}
	out := copyMap(m)
	out["resultType"] = "complete"
	return out
}

// finishOutcome is the last step of every reply: the budget check and the
// size cap on the serialized result.
func (e *Engine) finishOutcome(ctx context.Context, o Outcome) Outcome {
	if expired(ctx) {
		return budgetOutcome(o)
	}
	if o.Result != nil {
		if b, err := json.Marshal(o.Result); err == nil && len(b) > e.Limits.maxBytes() {
			o.Result = nil
			o.Error = &RPCError{Code: CodeTooLarge, Message: "result exceeds gateway size limit after shaping"}
		}
	}
	return o
}

func denyOr(reason, def string) string {
	if reason != "" {
		return reason
	}
	return def
}

// celMap is a JSON object as rules read it: the same keys, with numbers
// kept exact. Nothing is added to it.
func celMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = celValue(v)
	}
	return out
}

// celArgs converts exact JSON numbers for CEL (int64 when integral and in
// range, float64 otherwise); the forwarded arguments keep the originals.
func celArgs(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = celValue(v)
	}
	return out
}

func celValue(v any) any {
	switch t := v.(type) {
	case json.Number:
		return celNumber(t)
	case map[string]any:
		return celArgs(t)
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = celValue(x)
		}
		return out
	}
	return v
}

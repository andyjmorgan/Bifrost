package harness

import (
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var confirmRule = rule("tools/call", "!has(input.ok)", "result", "elicit('ok', 'Delete it?')")

func deleteUpstream(t *testing.T) *Upstream {
	u := NewUpstream(t, V2026)
	u.Tool(`{"name":"delete_page","inputSchema":{"type":"object","properties":{"id":{"type":"string"}}}}`, Static(Reply{Result: Text("deleted")}))
	return u
}

// A gateway confirmation, answered by a real client's elicitation handler.
// Accept and tick the box: the call goes through, once.
func TestConfirmationAccepted(t *testing.T) {
	u := deleteUpstream(t)
	g := NewGateway(t)
	g.AddServer(t, "a", u, confirmRule)
	var asked atomic.Int64
	var message atomic.Value
	cs := Connect(t, g.Direct("a"), ClientOptions{Elicit: func(r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		asked.Add(1)
		message.Store(r.Params.Message)
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, nil
	}})

	res := mustCall(t, cs, "delete_page", map[string]any{"id": "42"})
	eq(t, "result", TextOf(res), "deleted")
	eq(t, "times the user was asked", asked.Load(), 1)
	eq(t, "the question", message.Load(), "Delete it?")
	eq(t, "calls that reached the server", len(u.Requests("tools/call")), 1)
	eq(t, "upstream arguments", u.Last(t, "tools/call").Args(), map[string]any{"id": "42"})
	lacks(t, "the server's view of the request", asJSON(u.Last(t, "tools/call").Map()), "bifrost.ok")
}

// Anything but accept-and-ticked refuses the call. The server is never
// called.
func TestConfirmationNotAccepted(t *testing.T) {
	answers := map[string]*mcp.ElicitResult{
		"declined":          {Action: "decline"},
		"cancelled":         {Action: "cancel"},
		"accepted unticked": {Action: "accept", Content: map[string]any{"confirm": false}},
	}
	for name, answer := range answers {
		t.Run(name, func(t *testing.T) {
			u := deleteUpstream(t)
			g := NewGateway(t)
			g.AddServer(t, "a", u, confirmRule)
			cs := Connect(t, g.Direct("a"), ClientOptions{Elicit: func(*mcp.ElicitRequest) (*mcp.ElicitResult, error) { return answer, nil }})
			res, err := call(t, cs, "delete_page", map[string]any{"id": "42"})
			if !refused(res, err) {
				t.Errorf("the call went through: %s", TextOf(res))
			}
			eq(t, "calls that reached the server", len(u.Requests("tools/call")), 0)
		})
	}
}

// A client that declares no elicitation cannot be asked, so the call is
// refused and the client is never sent a question it cannot answer.
func TestConfirmationWithoutElicitation(t *testing.T) {
	u := deleteUpstream(t)
	g := NewGateway(t)
	g.AddServer(t, "a", u, confirmRule)

	cs := Connect(t, g.Direct("a"), ClientOptions{})
	res, err := call(t, cs, "delete_page", map[string]any{"id": "42"})
	if !refused(res, err) {
		t.Errorf("the call went through without confirmation: %s", TextOf(res))
	}
	eq(t, "calls that reached the server", len(u.Requests("tools/call")), 0)

	raw := Raw(t, g.Direct("a"), RawRequest{Method: "tools/call", Params: map[string]any{"name": "delete_page", "arguments": map[string]any{"id": "42"}}})
	if rt, _ := raw.Result()["resultType"].(string); rt == "input_required" {
		t.Errorf("a client with no elicitation capability was sent input_required: %v", raw.Result())
	}
}

// A confirmation answered for one set of arguments does not carry over to
// another.
func TestConfirmationIsBoundToArguments(t *testing.T) {
	u := deleteUpstream(t)
	g := NewGateway(t)
	g.AddServer(t, "a", u, confirmRule)
	caps := map[string]any{"elicitation": map[string]any{}}
	first := Raw(t, g.Direct("a"), RawRequest{Method: "tools/call", Capabilities: caps,
		Params: map[string]any{"name": "delete_page", "arguments": map[string]any{"id": "42"}}})
	state, _ := first.Result()["requestState"].(string)
	if first.Result()["resultType"] != "input_required" || state == "" {
		t.Fatalf("expected input_required with state, got %v", first.Final())
	}
	replay := Raw(t, g.Direct("a"), RawRequest{Method: "tools/call", Capabilities: caps, Params: map[string]any{
		"name": "delete_page", "arguments": map[string]any{"id": "43"}, "requestState": state,
		"inputResponses": map[string]any{"bifrost.ok": map[string]any{"action": "accept", "content": map[string]any{"confirm": true}}}}})
	eq(t, "calls that reached the server", len(u.Requests("tools/call")), 0)
	if replay.Error() == nil && replay.Result()["isError"] != true {
		t.Errorf("an answer for other arguments was accepted: %v", replay.Final())
	}
}

func askingUpstream(t *testing.T) *Upstream {
	u := NewUpstream(t, V2026)
	u.Tool(`{"name":"create_issue","inputSchema":{"type":"object","properties":{"title":{"type":"string"}}}}`, func(r Recorded) Reply {
		answers, _ := r.Map()["inputResponses"].(map[string]any)
		if answers == nil {
			return Reply{Result: map[string]any{"resultType": "input_required", "requestState": "up1", "inputRequests": map[string]any{
				"q": map[string]any{"method": "elicitation/create", "params": map[string]any{"mode": "form", "message": "Which project?",
					"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{"project": map[string]any{"type": "string"}}}}}}}}
		}
		q, _ := answers["q"].(map[string]any)
		action, _ := q["action"].(string)
		return Reply{Result: Text("server saw " + action)}
	})
	return u
}

// The server's own question is not the gateway's to judge. A decline goes
// back to the server with the server's state, and the server decides.
func TestUpstreamQuestionDeclineIsPassedUp(t *testing.T) {
	u := askingUpstream(t)
	g := NewGateway(t)
	g.AddServer(t, "a", u, rule("server/connect", "true", "capabilities.elicitation", "true"))
	var message atomic.Value
	cs := Connect(t, g.Direct("a"), ClientOptions{Elicit: func(r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		message.Store(r.Params.Message)
		return &mcp.ElicitResult{Action: "decline"}, nil
	}})

	res := mustCall(t, cs, "create_issue", map[string]any{"title": "x"})
	eq(t, "result", TextOf(res), "server saw decline")
	eq(t, "the question", message.Load(), "Which project?")
	calls := u.Requests("tools/call")
	if len(calls) != 2 {
		t.Fatalf("the server should be called twice, got %d", len(calls))
	}
	second := calls[1].Map()
	eq(t, "the server's own state comes back", second["requestState"], "up1")
	eq(t, "the answer the server received", second["inputResponses"], map[string]any{"q": map[string]any{"action": "decline"}})
	eq(t, "the server was told the client can be asked", calls[0].Capabilities()["elicitation"] != nil, true)
}

// A client that never declared elicitation must not have a server told it
// can be asked, whatever a server/connect rule says.
func TestUpstreamIsNotToldAboutElicitationTheClientLacks(t *testing.T) {
	u := askingUpstream(t)
	g := NewGateway(t)
	g.AddServer(t, "a", u, rule("server/connect", "true", "capabilities.elicitation", "true"))
	raw := Raw(t, g.Direct("a"), RawRequest{Method: "tools/call", Params: map[string]any{"name": "create_issue", "arguments": map[string]any{"title": "x"}}})
	if caps := u.Last(t, "tools/call").Capabilities(); caps["elicitation"] != nil {
		t.Errorf("the server was told elicitation is supported: %v", caps)
	}
	if rt, _ := raw.Result()["resultType"].(string); rt == "input_required" {
		t.Errorf("a client with no elicitation capability was sent input_required")
	}
}

// Confirmations work the same on every route. In search mode the question
// and the answer travel through execute_tool.
func TestConfirmationOnEveryRoute(t *testing.T) {
	for _, route := range []string{"direct", "mux", "search"} {
		for _, accept := range []bool{true, false} {
			name := route + " declined"
			if accept {
				name = route + " accepted"
			}
			t.Run(name, func(t *testing.T) {
				u := deleteUpstream(t)
				g := NewGateway(t)
				g.AddServer(t, "a", u, confirmRule)
				answer := &mcp.ElicitResult{Action: "decline"}
				if accept {
					answer = &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}
				}
				elicit := func(*mcp.ElicitRequest) (*mcp.ElicitResult, error) { return answer, nil }
				var res *mcp.CallToolResult
				var err error
				switch route {
				case "direct":
					res, err = call(t, Connect(t, g.Direct("a"), ClientOptions{Elicit: elicit}), "delete_page", map[string]any{"id": "42"})
				case "mux":
					res, err = call(t, Connect(t, g.Mux(), ClientOptions{Elicit: elicit}), "a__delete_page", map[string]any{"id": "42"})
				case "search":
					res, err = call(t, Connect(t, g.Search(), ClientOptions{Elicit: elicit}), "execute_tool",
						map[string]any{"name": "a__delete_page", "arguments": map[string]any{"id": "42"}})
				}
				if accept {
					if refused(res, err) {
						t.Fatalf("an accepted call was refused: %v %s", err, asJSON(res))
					}
					eq(t, "result", TextOf(res), "deleted")
					eq(t, "calls that reached the server", len(u.Requests("tools/call")), 1)
					return
				}
				if !refused(res, err) {
					t.Errorf("a declined call went through: %s", TextOf(res))
				}
				eq(t, "calls that reached the server", len(u.Requests("tools/call")), 0)
			})
		}
	}
}

// Reads and prompts can be confirmed too.
func TestConfirmationOnReadsAndPrompts(t *testing.T) {
	for _, accept := range []bool{true, false} {
		name := "declined"
		if accept {
			name = "accepted"
		}
		t.Run(name, func(t *testing.T) {
			u := NewUpstream(t, V2026)
			u.Resource("docs://a", nil)
			u.Prompt(`{"name":"greet"}`, nil)
			g := NewGateway(t)
			g.AddServer(t, "a", u,
				rule("resources/read", "!has(input.ok)", "result", "elicit('ok', 'Read ' + uri + '?')"),
				rule("prompts/get", "!has(input.ok)", "result", "elicit('ok', 'Use ' + name + '?')"))
			answer := &mcp.ElicitResult{Action: "decline"}
			if accept {
				answer = &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}
			}
			var asked []string
			cs := Connect(t, g.Direct("a"), ClientOptions{Elicit: func(r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				asked = append(asked, r.Params.Message)
				return answer, nil
			}})
			_, rerr := read(t, cs, "docs://a")
			_, perr := cs.GetPrompt(bg(t), &mcp.GetPromptParams{Name: "greet"})
			eq(t, "questions", asked, []string{"Read docs://a?", "Use greet?"})
			if accept {
				if rerr != nil || perr != nil {
					t.Fatalf("accepted requests failed: %v, %v", rerr, perr)
				}
				eq(t, "reads that reached the server", len(u.Requests("resources/read")), 1)
				eq(t, "prompts that reached the server", len(u.Requests("prompts/get")), 1)
				return
			}
			if rerr == nil || perr == nil {
				t.Errorf("declined requests went through: read %v, prompt %v", rerr, perr)
			}
			eq(t, "reads that reached the server", len(u.Requests("resources/read")), 0)
			eq(t, "prompts that reached the server", len(u.Requests("prompts/get")), 0)
		})
	}
}

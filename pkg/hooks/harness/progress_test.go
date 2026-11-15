package harness

import (
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// End-to-end tests of progress rules: a mock server streams progress, the
// engine drops or reshapes each event inside the test gateway, and a client
// reads what is left.

// progressOf returns every progress event a raw reply carried, in order.
func progressOf(rep *RawReply) []map[string]any {
	var out []map[string]any
	for _, m := range rep.Messages() {
		if m["method"] == "notifications/progress" {
			p, _ := m["params"].(map[string]any)
			out = append(out, p)
		}
	}
	return out
}

func messagesOf(events []map[string]any) []any {
	out := []any{}
	for _, p := range events {
		out = append(out, p["message"])
	}
	return out
}

// lastIsResult asserts the reply ended with a result, after any progress.
func lastIsResult(t testing.TB, rep *RawReply) map[string]any {
	t.Helper()
	msgs := rep.Messages()
	if len(msgs) == 0 {
		t.Fatal("the reply carried no messages")
	}
	res, ok := msgs[len(msgs)-1]["result"].(map[string]any)
	if !ok {
		t.Fatalf("the last message is not a result: %s", asJSON(msgs[len(msgs)-1]))
	}
	return res
}

// The official SDK client receives progress as the rules left it: one event
// rewritten, one dropped, one untouched, with the numbers as the server sent
// them.
func TestProgressMutationsReachTheSDKClient(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, Static(Reply{Stream: true, Result: Text("done"), Progress: []Progress{
		{Progress: 1, Total: 3, Message: "Querying db-internal-7"},
		{Progress: 2, Total: 3, Message: "noise"},
		{Progress: 3, Total: 3, Message: "Finishing"},
	}}))
	g := NewGateway(t)
	g.AddServer(t, "a", u,
		rule("tools/progress", "progress.message.contains('db-internal')", "message", "regex.replace(progress.message, 'db-internal-[0-9]+', 'the database')"),
		rule("tools/progress", "progress.message == 'noise'", "decision", "'deny'"))

	var mu sync.Mutex
	var got []mcp.ProgressNotificationParams
	cs := Connect(t, g.Direct("a"), ClientOptions{Progress: func(p *mcp.ProgressNotificationParams) {
		mu.Lock()
		got = append(got, *p)
		mu.Unlock()
	}})
	p := &mcp.CallToolParams{Name: "t", Arguments: map[string]any{}}
	p.SetProgressToken("sdk-token")
	res, err := cs.CallTool(bg(t), p)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "result", TextOf(res), "done")
	eventually(t, "two progress events at the SDK client", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) >= 2
	})
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("the SDK client received %d progress events, want 2: %s", len(got), asJSON(got))
	}
	eq(t, "first message", got[0].Message, "Querying the database")
	eq(t, "first progress", got[0].Progress, float64(1))
	eq(t, "first total", got[0].Total, float64(3))
	eq(t, "second message", got[1].Message, "Finishing")
	eq(t, "second progress", got[1].Progress, float64(3))
	lacks(t, "what the client received", asJSON(got), "db-internal")
	lacks(t, "what the client received", asJSON(got), "noise")

	// The audit trail has both rules.
	rec := g.LastRecord(t, "tools/call")
	if !hit(rec, "tools/progress", "set") || !hit(rec, "tools/progress", "deny") {
		t.Errorf("the progress rules left no hits: %s", rec.JSON())
	}
}

// On the mux each server's progress goes through that server's own rules.
func TestProgressMutationsOnTheMux(t *testing.T) {
	mk := func(msg string) *Upstream {
		u := NewUpstream(t, V2026)
		u.Tool(plainTool, Static(Reply{Stream: true, Result: Text("done " + msg), Progress: []Progress{{Progress: 1, Message: msg}, {Progress: 2, Message: msg + " again"}}}))
		return u
	}
	g := NewGateway(t)
	g.AddServer(t, "a", mk("from a"), rule("tools/progress", "true", "message", "'A says: ' + progress.message"))
	g.AddServer(t, "b", mk("from b"), rule("tools/progress", "true", "decision", "'deny'"))
	g.AddServer(t, "c", mk("from c"))

	for _, tc := range []struct {
		tool string
		want []any
		done string
	}{
		{"a__t", []any{"A says: from a", "A says: from a again"}, "done from a"},
		{"b__t", []any{}, "done from b"},
		{"c__t", []any{"from c", "from c again"}, "done from c"},
	} {
		rr := callReq(tc.tool)
		rr.ProgressToken = "ct-" + tc.tool
		rep := Raw(t, g.Mux(), rr)
		events := progressOf(rep)
		eq(t, tc.tool+" progress messages", messagesOf(events), tc.want)
		for _, p := range events {
			eq(t, tc.tool+" token", p["progressToken"], "ct-"+tc.tool)
		}
		res := lastIsResult(t, rep)
		eq(t, tc.tool+" result", res["content"].([]any)[0].(map[string]any)["text"], tc.done)
	}
}

// Prompts and reads have their own progress hooks. A tools rule does not
// touch them.
func TestProgressMutationsForPromptsAndReads(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Prompt(`{"name":"greet"}`, Static(Reply{Stream: true, Progress: []Progress{{Progress: 1, Message: "loading template /srv/prompts/greet.tmpl"}},
		Result: map[string]any{"messages": []any{map[string]any{"role": "user", "content": map[string]any{"type": "text", "text": "hi"}}}}}))
	u.Resource("docs://a", Static(Reply{Stream: true, Progress: []Progress{{Progress: 1, Message: "reading /srv/docs/a.md"}}, Result: Contents("docs://a", "body")}))
	g := NewGateway(t)
	g.AddServer(t, "a", u,
		rule("prompts/progress", "name == 'greet'", "message", "'Building the prompt'"),
		rule("resources/progress", "uri == 'docs://a'", "message", "'Reading the document'"),
		rule("tools/progress", "true", "decision", "'deny'"))

	prompt := Raw(t, g.Direct("a"), RawRequest{Method: "prompts/get", Params: map[string]any{"name": "greet"}, ProgressToken: "pt"})
	eq(t, "prompt progress", messagesOf(progressOf(prompt)), []any{"Building the prompt"})
	lacks(t, "the prompt reply", asJSON(prompt.Messages()), "/srv/prompts")
	if _, ok := lastIsResult(t, prompt)["messages"]; !ok {
		t.Errorf("the prompt result did not arrive: %s", asJSON(prompt.Messages()))
	}

	read := Raw(t, g.Direct("a"), RawRequest{Method: "resources/read", Params: map[string]any{"uri": "docs://a"}, ProgressToken: "rt"})
	eq(t, "read progress", messagesOf(progressOf(read)), []any{"Reading the document"})
	lacks(t, "the read reply", asJSON(read.Messages()), "/srv/docs")
	if _, ok := lastIsResult(t, read)["contents"]; !ok {
		t.Errorf("the read result did not arrive: %s", asJSON(read.Messages()))
	}
}

// A progress rule that fails on one event drops that event only. The call
// completes and later events still flow.
func TestProgressRuleErrorDoesNotBreakTheCall(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, Static(Reply{Stream: true, Result: Text("done"), Progress: []Progress{
		{Progress: 1, Message: "no total here"}, // the rule reads progress.total, which is absent
		{Progress: 2, Total: 4, Message: "half"},
		{Progress: 4, Total: 4, Message: "all"},
	}}))
	g := NewGateway(t)
	g.AddServer(t, "a", u, rule("tools/progress", "progress.progress >= progress.total", "message", "'complete'"))
	rr := callReq("t")
	rr.ProgressToken = "ct-1"
	rep := Raw(t, g.Direct("a"), rr)
	eq(t, "progress messages", messagesOf(progressOf(rep)), []any{"half", "complete"})
	res := lastIsResult(t, rep)
	eq(t, "result", res["content"].([]any)[0].(map[string]any)["text"], "done")
	if !hit(g.LastRecord(t, "tools/call"), "tools/progress", "error") {
		t.Errorf("the failed rule left no hit: %s", g.LastRecord(t, "tools/call").JSON())
	}
}

// A result rule and progress rules on the same call: progress is reshaped
// as it flows, and the result is reshaped at the end.
func TestProgressAndResultRulesTogether(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, Static(Reply{Stream: true, Result: Text("account 4111 1111 1111 1111 charged"), Progress: []Progress{
		{Progress: 1, Message: "charging 4111 1111 1111 1111"},
	}}))
	g := NewGateway(t)
	g.AddServer(t, "a", u,
		rule("tools/progress", "true", "message", "regex.replace(progress.message, '[0-9]{4}( [0-9]{4}){3}', '[card]')"),
		rule("tools/result", "true", "content", "result.content.map(c, c.type == 'text' ? {'type': 'text', 'text': regex.replace(c.text, '[0-9]{4}( [0-9]{4}){3}', '[card]')} : c)"))
	rr := callReq("t")
	rr.ProgressToken = "ct-1"
	rep := Raw(t, g.Direct("a"), rr)
	eq(t, "progress messages", messagesOf(progressOf(rep)), []any{"charging [card]"})
	eq(t, "result", lastIsResult(t, rep)["content"].([]any)[0].(map[string]any)["text"], "account [card] charged")
	lacks(t, "everything the client received", asJSON(rep.Messages()), "4111")
}

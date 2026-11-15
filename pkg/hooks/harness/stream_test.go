package harness

import (
	"context"
	"github.com/andyjmorgan/Bifrost/pkg/hooks"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func callReq(name string) RawRequest {
	return RawRequest{Method: "tools/call", Params: map[string]any{"name": name, "arguments": map[string]any{}}}
}

func isStream(t testing.TB, what string, r *RawReply) {
	t.Helper()
	if r.Status != http.StatusOK || !r.Stream() {
		t.Errorf("%s: want a 200 SSE stream, got %d %q", what, r.Status, r.Header.Get("Content-Type"))
	}
	if r.Header.Get("X-Accel-Buffering") != "no" {
		t.Errorf("%s: X-Accel-Buffering is %q, want no", what, r.Header.Get("X-Accel-Buffering"))
	}
}

// The gateway always answers an accepted request with a stream: whatever
// the server chose, and whether or not the server was called at all.
func TestEveryAcceptedRequestIsAStream(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(`{"name":"body","inputSchema":{"type":"object","properties":{}}}`, Static(Reply{Result: Text("from a body")}))
	u.Tool(`{"name":"stream","inputSchema":{"type":"object","properties":{}}}`, Static(Reply{Stream: true, Result: Text("from a stream")}))
	u.Tool(`{"name":"denied","inputSchema":{"type":"object","properties":{}}}`, nil)
	u.Resource("docs://a", nil)
	u.Prompt(`{"name":"greet"}`, nil)
	g := NewGateway(t)
	g.AddServer(t, "a", u, rule("tools/call", "name == 'denied'", "decision", "'deny'", "reason", "'No.'"))

	for _, route := range []string{g.Direct("a"), g.Mux()} {
		prefix, uriPrefix := "", ""
		if route == g.Mux() {
			prefix, uriPrefix = "a__", "a+"
		}
		body := Raw(t, route, callReq(prefix+"body"))
		isStream(t, "server answered with a body", body)
		eq(t, "result from a body", body.Result()["content"].([]any)[0].(map[string]any)["text"], "from a body")

		stream := Raw(t, route, callReq(prefix+"stream"))
		isStream(t, "server answered with a stream", stream)
		eq(t, "result from a stream", stream.Result()["content"].([]any)[0].(map[string]any)["text"], "from a stream")

		denied := Raw(t, route, callReq(prefix+"denied"))
		isStream(t, "a deny before the server was called", denied)
		eq(t, "a deny is the final message", denied.Result()["isError"], true)
		eq(t, "messages on a denied call", len(denied.Messages()), 1)

		for _, rr := range []RawRequest{
			{Method: "server/discover"},
			{Method: "tools/list"},
			{Method: "resources/list"},
			{Method: "prompts/list"},
			{Method: "resources/read", Params: map[string]any{"uri": uriPrefix + "docs://a"}},
			{Method: "prompts/get", Params: map[string]any{"name": prefix + "greet"}},
			{Method: "no/such/method"},
		} {
			rep := Raw(t, route, rr)
			isStream(t, rr.Method, rep)
			if rep.Final() == nil {
				t.Errorf("%s: the stream ended without a final message", rr.Method)
			}
		}
	}
	eq(t, "calls that reached the server, none of them the denied one", len(u.Requests("tools/call")), 4)
}

// Plain HTTP statuses are only for requests refused before a stream opens.
func TestPlainStatusesBeforeTheStream(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, nil)
	g := NewGateway(t)
	g.Token = "secret-token"
	g.AddServer(t, "a", u)

	noAuth := Raw(t, g.Direct("a"), callReq("t"))
	eq(t, "no token", noAuth.Status, http.StatusUnauthorized)
	eq(t, "no token is not a stream", noAuth.Stream(), false)

	bad := Raw(t, g.Direct("a"), RawRequest{Token: "secret-token", Body: `{"jsonrpc":"2.0","id":1,"method":`})
	eq(t, "malformed body", bad.Status, http.StatusBadRequest)

	unknown := Raw(t, g.srv.URL+"/direct/nope", RawRequest{Method: "tools/list", Token: "secret-token"})
	eq(t, "unknown route", unknown.Status, http.StatusNotFound)

	note := Raw(t, g.Direct("a"), RawRequest{Token: "secret-token", Body: `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{}}`})
	eq(t, "a notification", note.Status, http.StatusAccepted)

	ok := Raw(t, g.Direct("a"), RawRequest{Method: "tools/call", Token: "secret-token", Params: map[string]any{"name": "t", "arguments": map[string]any{}}})
	isStream(t, "an authenticated call", ok)

	cs := Connect(t, g.Direct("a"), ClientOptions{Token: "secret-token"})
	mustCall(t, cs, "t", nil)
	if _, err := TryConnect(t, g.Direct("a"), ClientOptions{}); err == nil {
		t.Error("the SDK client connected without a token")
	}
}

func steps(n int) []Progress {
	var out []Progress
	for i := 1; i <= n; i++ {
		out = append(out, Progress{Progress: float64(i), Total: float64(n), Message: "step " + string(rune('0'+i))})
	}
	return out
}

// Progress reaches the client in order, under the client's own token, before
// the result. The server only ever sees the gateway's token.
func TestProgressIsRelayedInOrder(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, Static(Reply{Stream: true, Progress: steps(3), Result: Text("done")}))
	g := NewGateway(t)
	g.AddServer(t, "a", u)

	rr := callReq("t")
	rr.ProgressToken = "ct-1"
	rep := Raw(t, g.Direct("a"), rr)
	msgs := rep.Messages()
	if len(msgs) != 4 {
		t.Fatalf("want 3 progress events and a result, got %d messages: %s", len(msgs), asJSON(msgs))
	}
	for i, m := range msgs[:3] {
		p, _ := m["params"].(map[string]any)
		eq(t, "method", m["method"], "notifications/progress")
		eq(t, "token", p["progressToken"], "ct-1")
		eq(t, "progress", p["progress"], i+1)
		eq(t, "message", p["message"], "step "+string(rune('1'+i)))
	}
	eq(t, "the result comes last", msgs[3]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"], "done")
	tok, _ := u.Last(t, "tools/call").ProgressToken().(string)
	if !strings.HasPrefix(tok, "gw-") || tok == "ct-1" {
		t.Errorf("the server should see the gateway's own token, got %q", tok)
	}

	// The same through the SDK client.
	var mu sync.Mutex
	var got []string
	cs := Connect(t, g.Direct("a"), ClientOptions{Progress: func(p *mcp.ProgressNotificationParams) {
		mu.Lock()
		got = append(got, p.Message)
		mu.Unlock()
	}})
	p := &mcp.CallToolParams{Name: "t", Arguments: map[string]any{}}
	p.SetProgressToken("sdk-token")
	res, err := cs.CallTool(bg(t), p)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "result", TextOf(res), "done")
	eventually(t, "three progress events at the SDK client", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) >= 3
	})
	mu.Lock()
	defer mu.Unlock()
	eq(t, "progress messages the SDK client received", got, []string{"step 1", "step 2", "step 3"})
}

// A client that asked for no progress gets none, and the server is given no
// token. Progress a server sends anyway is dropped.
func TestNoProgressWithoutAToken(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, Static(Reply{Stream: true, Progress: steps(2), ForceProgress: true, Result: Text("done")}))
	g := NewGateway(t)
	g.AddServer(t, "a", u)
	rep := Raw(t, g.Direct("a"), callReq("t"))
	eq(t, "messages", len(rep.Messages()), 1)
	if tok := u.Last(t, "tools/call").ProgressToken(); tok != nil {
		t.Errorf("the server was given a progress token: %v", tok)
	}
}

// With the progress switch off, the server gets no token and the client no
// progress, even if it asked.
func TestProgressSwitchOff(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, Static(Reply{Stream: true, Progress: steps(2), ForceProgress: true, Result: Text("done")}))
	g := NewGateway(t)
	g.AddServer(t, "a", u, rule("server/config", "true", "progress", "false"))
	rr := callReq("t")
	rr.ProgressToken = "ct-1"
	rep := Raw(t, g.Direct("a"), rr)
	eq(t, "messages", len(rep.Messages()), 1)
	if tok := u.Last(t, "tools/call").ProgressToken(); tok != nil {
		t.Errorf("the server was given a progress token: %v", tok)
	}
}

// Nothing sent can be taken back. A result refused by a rule still follows
// the progress already relayed, and the refusal is the final message, not a
// status code.
func TestRefusedResultAfterProgress(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, Static(Reply{Stream: true, Progress: steps(2), Result: Text("card 4111 1111 1111 1111")}))
	g := NewGateway(t)
	g.AddServer(t, "a", u, rule("tools/result", "result.content.exists(c, c.type == 'text' && c.text.contains('4111'))", "decision", "'deny'", "reason", "'Card numbers are blocked.'"))
	rr := callReq("t")
	rr.ProgressToken = "ct-1"
	rep := Raw(t, g.Direct("a"), rr)
	isStream(t, "a refused result", rep)
	msgs := rep.Messages()
	if len(msgs) != 3 {
		t.Fatalf("want 2 progress events and a refusal, got %s", asJSON(msgs))
	}
	eq(t, "the refusal is an error result", rep.Result()["isError"], true)
	has(t, "the refusal", asJSON(rep.Result()), "Card numbers are blocked.")
	lacks(t, "everything the client received", asJSON(msgs), "4111")
	if !hit(g.LastRecord(t, "tools/call"), "tools/result", "deny") {
		t.Errorf("the deny left no hit: %s", g.LastRecord(t, "tools/call").JSON())
	}
}

// Progress text is server-written and reaches the client before the result.
// A progress rule can drop the event or rewrite its message.
func TestProgressRulesShapeTheMessage(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, Static(Reply{Stream: true, Result: Text("done"),
		Progress: []Progress{{Progress: 1, Message: "Processing card 4111 1111 1111 1111"}, {Progress: 2, Message: "drop me"}, {Progress: 3, Message: "fine"}}}))
	g := NewGateway(t)
	g.AddServer(t, "a", u,
		rule("tools/progress", "progress.message.contains('4111')", "message", "'Processing'"),
		rule("tools/progress", "progress.message == 'drop me'", "decision", "'deny'"))
	rr := callReq("t")
	rr.ProgressToken = "ct-1"
	rep := Raw(t, g.Direct("a"), rr)
	var messages []any
	for _, m := range rep.Messages() {
		if m["method"] == "notifications/progress" {
			messages = append(messages, m["params"].(map[string]any)["message"])
		}
	}
	eq(t, "progress messages", messages, []any{"Processing", "fine"})
	lacks(t, "everything the client received", asJSON(rep.Messages()), "4111")
}

// Log messages and notifications nobody defined are not relayed.
func TestLogsAndUnknownNotificationsAreDropped(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, Static(Reply{Stream: true, Result: Text("done"), Notes: []Note{
		{Method: "notifications/message", Params: map[string]any{"level": "info", "data": "secret log line"}},
		{Method: "notifications/vendor/custom", Params: map[string]any{"x": 1}},
		{Method: "notifications/tools/list_changed", Params: map[string]any{}},
	}}))
	g := NewGateway(t)
	g.AddServer(t, "a", u)
	rr := callReq("t")
	rr.ProgressToken = "ct-1"
	rep := Raw(t, g.Direct("a"), rr)
	eq(t, "messages", len(rep.Messages()), 1)
	lacks(t, "everything the client received", asJSON(rep.Messages()), "secret log line")

	var logs atomic.Int64
	cs := Connect(t, g.Direct("a"), ClientOptions{Logs: func(*mcp.LoggingMessageParams) { logs.Add(1) }})
	mustCall(t, cs, "t", nil)
	eq(t, "log messages the SDK client received", logs.Load(), 0)
}

// A server that closes its stream without answering loses the request. The
// gateway tells the client so with a JSON-RPC error, then closes the stream.
func TestUpstreamStreamEndsWithoutAResponse(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, Static(Reply{Stream: true, NoFinal: true, Progress: steps(1)}))
	u.Tool(`{"name":"ok","inputSchema":{"type":"object","properties":{}}}`, nil)
	g := NewGateway(t)
	g.AddServer(t, "a", u)

	rr := callReq("t")
	rr.ProgressToken = "ct-1"
	rep := Raw(t, g.Direct("a"), rr)
	isStream(t, "a lost request", rep)
	// The client gets the progress that arrived, then a JSON-RPC error as
	// the last message, so it is not left waiting for a response.
	msgs := rep.Messages()
	if len(msgs) != 2 || msgs[0]["method"] != "notifications/progress" {
		t.Fatalf("want one progress event then an error, got %s", asJSON(msgs))
	}
	lostErr, _ := msgs[1]["error"].(map[string]any)
	if lostErr == nil || msgs[1]["result"] != nil {
		t.Fatalf("the last message is not an error: %s", asJSON(msgs[1]))
	}
	eq(t, "error code", lostErr["code"], hooks.CodeUpstreamLost)
	has(t, "error message", asJSON(lostErr["message"]), "may or may not have run")
	rec := g.LastRecord(t, "tools/call")
	has(t, "the gateway's record of the lost request", rec.JSON(), "upstream reply lost")

	cs := Connect(t, g.Direct("a"), ClientOptions{})
	_, err := call(t, cs, "t", nil)
	if err == nil {
		t.Fatal("the SDK client got a result for a lost request")
	}
	has(t, "what the SDK client reports", err.Error(), "may or may not have run")
	// The same session keeps working.
	eq(t, "the next call", TextOf(mustCall(t, cs, "ok", nil)), "ok")
}

// The client closing its stream is cancellation, and it reaches the server.
func TestClientCancelReachesTheServer(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, Static(Reply{Stream: true, Hang: true, Progress: steps(1)}))
	g := NewGateway(t)
	g.AddServer(t, "a", u)

	rr := callReq("t")
	rr.ProgressToken = "ct-1"
	rr.OnFrame = func(f Frame) bool { return f.Method() != "notifications/progress" } // hang up at the first progress event
	rep := Raw(t, g.Direct("a"), rr)
	if rep.Final() != nil {
		t.Fatalf("a hung call produced a final message: %v", rep.Final())
	}
	if !u.WaitCancelled(1, 5*time.Second) {
		t.Error("the server never saw the client go away")
	}
}

// A slow server: the stream opens at once and carries keep-alives until the
// result arrives.
func TestKeepAlivesOnASlowServer(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, Static(Reply{Delay: 250 * time.Millisecond, Result: Text("slow")}))
	g := NewGateway(t)
	g.KeepAlive = 20 * time.Millisecond
	g.AddServer(t, "a", u)

	rep := Raw(t, g.Direct("a"), callReq("t"))
	isStream(t, "a slow call", rep)
	if rep.Comments() < 3 {
		t.Errorf("want keep-alive comments while waiting, got %d", rep.Comments())
	}
	if len(rep.Frames) == 0 || !rep.Frames[0].Comment || rep.Frames[0].At > 200*time.Millisecond {
		t.Errorf("the first keep-alive should arrive before the result: %+v", rep.Frames)
	}
	last := rep.Frames[len(rep.Frames)-1]
	if last.Comment {
		t.Error("the final message should be the last frame")
	}
	eq(t, "result", rep.Result()["content"].([]any)[0].(map[string]any)["text"], "slow")

	// The SDK client ignores the comments.
	cs := Connect(t, g.Direct("a"), ClientOptions{})
	eq(t, "result through the SDK client", TextOf(mustCall(t, cs, "t", nil)), "slow")
}

// A server failure after the stream opened is a JSON-RPC error on a 200
// stream, not a status code.
func TestUpstreamFailuresAreErrorsOnTheStream(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(`{"name":"bad_gateway","inputSchema":{"type":"object","properties":{}}}`, Static(Reply{Status: 502, Body: "bad gateway"}))
	u.Tool(`{"name":"rpc_error","inputSchema":{"type":"object","properties":{}}}`, Static(Reply{Error: &RPCErr{Code: -32050, Message: "backend down"}}))
	g := NewGateway(t)
	g.AddServer(t, "a", u)
	for _, name := range []string{"bad_gateway", "rpc_error"} {
		rep := Raw(t, g.Direct("a"), callReq(name))
		isStream(t, name, rep)
		if rep.Error() == nil {
			t.Errorf("%s: want a JSON-RPC error, got %v", name, rep.Final())
		}
	}
	lacks(t, "the client's view of a 502", asJSON(Raw(t, g.Direct("a"), callReq("bad_gateway")).Final()), "bad gateway")
}

// The same through the SDK client: cancelling the call's context closes the
// stream, and the server sees its caller go away.
func TestSDKClientCancelReachesTheServer(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, Static(Reply{Stream: true, Hang: true}))
	u.Tool(`{"name":"ok","inputSchema":{"type":"object","properties":{}}}`, nil)
	g := NewGateway(t)
	g.AddServer(t, "a", u)
	cs := Connect(t, g.Direct("a"), ClientOptions{})

	ctx, cancel := context.WithCancel(bg(t))
	done := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "t", Arguments: map[string]any{}})
		done <- err
	}()
	eventually(t, "the call to reach the server", func() bool { return len(u.Requests("tools/call")) == 1 })
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a cancelled call returned a result")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled call never returned")
	}
	if !u.WaitCancelled(1, 5*time.Second) {
		t.Error("the server never saw the client go away")
	}
	eq(t, "the session still works after a cancel", TextOf(mustCall(t, cs, "ok", nil)), "ok")
	t.Logf("notifications the SDK client posted: %v", g.Notifications())
}

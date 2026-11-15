package harness

import (
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func progressTool(u *Upstream) {
	u.Tool(plainTool, Static(Reply{Stream: true, Result: Text("done"),
		Progress: []Progress{{Progress: 1, Total: 2, Message: "half"}, {Progress: 2, Total: 2, Message: "all"}}}))
}

// The front door speaks only 2026-07-28. A server on anything older, or on
// no stated version, gets no elicitation, no tasks and no progress, whatever
// the rules say. Tools still work.
func TestOlderServersGetNoElicitationTasksOrProgress(t *testing.T) {
	for _, version := range []string{V2024, V2025, VNone} {
		t.Run(version, func(t *testing.T) {
			u := NewUpstream(t, version)
			progressTool(u)
			g := NewGateway(t)
			g.AddServer(t, "old", u, rule("server/connect", "true", "capabilities.elicitation", "true"),
				rule("server/config", "true", "tasks", "true", "progress", "true"))
			var progress atomic.Int64
			cs := Connect(t, g.Direct("old"), ClientOptions{
				Elicit:   func(*mcp.ElicitRequest) (*mcp.ElicitResult, error) { return &mcp.ElicitResult{Action: "accept"}, nil },
				Progress: func(*mcp.ProgressNotificationParams) { progress.Add(1) },
			})
			p := &mcp.CallToolParams{Name: "t", Arguments: map[string]any{}}
			p.SetProgressToken("client-token")
			res, err := cs.CallTool(bg(t), p)
			if err != nil {
				t.Fatalf("tools must still work on an older server: %v", err)
			}
			eq(t, "result", TextOf(res), "done")

			// These hold today: the server was reached with the old handshake.
			if n := len(u.Requests("initialize")); n == 0 {
				t.Fatal("an older server should be reached through initialize")
			}

			sw := g.Switches("old")
			eq(t, "tasks switch", sw["tasks"], false)
			eq(t, "progress switch", sw["progress"], false)
			eq(t, "tools switch", sw["tools"], true)
			for _, init := range u.Requests("initialize") {
				if init.Capabilities()["elicitation"] != nil {
					t.Errorf("the server was told elicitation is supported: %v", init.Capabilities())
				}
			}
			if tok := u.Last(t, "tools/call").ProgressToken(); tok != nil {
				t.Errorf("the server was given a progress token: %v", tok)
			}
			eq(t, "progress events the client received", progress.Load(), 0)
		})
	}
}

// A 2026-07-28 server can be given elicitation and progress. Tasks are
// deferred, so that switch stays off whatever the rules say.
func TestCurrentServerCanHaveElicitationAndProgress(t *testing.T) {
	u := NewUpstream(t, V2026)
	progressTool(u)
	g := NewGateway(t)
	g.AddServer(t, "new", u, rule("server/connect", "true", "capabilities.elicitation", "true"),
		rule("server/config", "true", "tasks", "true", "progress", "true"))
	var progress atomic.Int64
	cs := Connect(t, g.Direct("new"), ClientOptions{
		Elicit:   func(*mcp.ElicitRequest) (*mcp.ElicitResult, error) { return &mcp.ElicitResult{Action: "accept"}, nil },
		Progress: func(*mcp.ProgressNotificationParams) { progress.Add(1) },
	})
	p := &mcp.CallToolParams{Name: "t", Arguments: map[string]any{}}
	p.SetProgressToken("client-token")
	if _, err := cs.CallTool(bg(t), p); err != nil {
		t.Fatal(err)
	}
	sw := g.Switches("new")
	eq(t, "tasks switch (tasks are deferred, so it stays off for every server)", sw["tasks"], false)
	eq(t, "progress switch", sw["progress"], true)
	up := u.Last(t, "tools/call")
	if up.Capabilities()["elicitation"] == nil {
		t.Errorf("the server was not told elicitation is supported: %v", up.Capabilities())
	}
	if up.ProgressToken() == nil {
		t.Error("the server was given no progress token")
	}
	eventually(t, "two progress events at the client", func() bool { return progress.Load() >= 2 })
	eq(t, "progress events the client received", progress.Load(), 2)
}

// 2026-07-28 requires resultType on every result. Older servers do not send
// it, so the gateway's reply must add it.
func TestOlderResultsGainResultType(t *testing.T) {
	for _, version := range []string{V2024, V2025} {
		t.Run(version, func(t *testing.T) {
			u := NewUpstream(t, version)
			u.Tool(plainTool, nil)
			u.Resource("docs://a", nil)
			g := NewGateway(t)
			g.AddServer(t, "old", u)
			call := Raw(t, g.Direct("old"), RawRequest{Method: "tools/call", Params: map[string]any{"name": "t", "arguments": map[string]any{}}})
			eq(t, "tools/call resultType", call.Result()["resultType"], "complete")
			read := Raw(t, g.Direct("old"), RawRequest{Method: "resources/read", Params: map[string]any{"uri": "docs://a"}})
			eq(t, "resources/read resultType", read.Result()["resultType"], "complete")
		})
	}
}

package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The server hooks, settings and session, and the gate on a server's protocol
// version.

// A server that is not on the front door's protocol version gets no
// elicitation, no progress and no tasks, whatever the rules say. A missing
// version counts as old.
func TestVersionGate(t *testing.T) {
	rules := []Rule{
		{Hook: "server.session", When: "true", Then: map[string]string{"session.capabilities.elicitation": "true"}},
		{Hook: "server.settings", When: "true", Then: map[string]string{"settings.tasks": "true", "settings.progress": "true"}},
	}
	ask := `{"resultType":"input_required","requestState":"up1","inputRequests":{"q":{"method":"elicitation/create","params":{"message":"Which?","requestedSchema":{"type":"object","properties":{}}}}}}`
	for _, tc := range []struct {
		version string
		current bool
	}{{"2026-07-28", true}, {"2025-11-25", false}, {"2025-06-18", false}, {"2024-11-05", false}, {"", false}, {"2027-01-01", false}} {
		e := &Engine{}
		s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: tc.version}, Rules: mustCompile(t, rules), Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
		r := newRequest("direct", s)
		ctx := context.Background()
		name := "version " + tc.version

		sw, hits := e.Config(ctx, r, s)
		if sw["tasks"] != tc.current {
			t.Errorf("%s: tasks = %v, want %v", name, sw["tasks"], tc.current)
		}
		if sw["progress"] != tc.current {
			t.Errorf("%s: progress = %v, want %v", name, sw["progress"], tc.current)
		}
		if !sw["tools"] {
			t.Errorf("%s: the gate switched tools off", name)
		}
		gated := 0
		for _, h := range hits {
			if h.Rule == "(gateway)" {
				gated++
			}
		}
		if want := map[bool]int{true: 0, false: 2}[tc.current]; gated != want {
			t.Errorf("%s: %d gate hits, want %d: %+v", name, gated, want, hits)
		}

		ci, _ := e.Connect(ctx, r, s, "test")
		if _, on := ci.Capabilities["elicitation"]; on != tc.current {
			t.Errorf("%s: elicitation advertised = %v, want %v", name, on, tc.current)
		}

		p := &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"abc"}`)}
		first := e.CallTool(ctx, r, p)
		if first.Forward == nil {
			t.Fatalf("%s: the call was not forwarded: %+v", name, first.Error)
		}
		// An input request from the server reaches the client only from a current server.
		out := e.UpstreamToolReply(ctx, r, first.Call, p, json.RawMessage(ask))
		if _, passed := out.Result.(InputRequired); passed != tc.current {
			t.Errorf("%s: input request passed on = %v, want %v", name, passed, tc.current)
		}
		// Progress is relayed only from a current server; the result always arrives.
		got := collect(e.ToolReplyStream(ctx, r, first.Call, p, (&upstreamSeq{events: []UpstreamEvent{
			progressEvent(`{"progressToken":"p","progress":1}`), resultEvent(`{"content":[{"type":"text","text":"done"}]}`)}}).seq()))
		if (len(got.notes) == 1) != tc.current {
			t.Errorf("%s: %d progress events relayed", name, len(got.notes))
		}
		// Every result is marked complete, whether or not the server said so.
		b, _ := json.Marshal(got.final.Result)
		if !strings.Contains(string(b), `"resultType":"complete"`) || !strings.Contains(string(b), "done") {
			t.Errorf("%s: result = %s", name, b)
		}
	}
}

// A server whose protocol version is empty, unrecognised, older, or the
// current one with a stray space is not current: no elicitation is
// advertised, progress and tasks stay off whatever the rules say, and the
// result is still marked complete.
func TestTheVersionGateTreatsAnyOtherVersionStringAsOld(t *testing.T) {
	for _, version := range []string{"", "unknown", "2025-03-26", "2026-07-28 "} {
		t.Run(fmt.Sprintf("version=%q", version), func(t *testing.T) {
			c := newStreamCase(t, "direct", []Rule{
				{Hook: "server.session", When: "true", Then: map[string]string{"session.capabilities.elicitation": "true"}},
				{Hook: "server.settings", When: "true", Then: map[string]string{"settings.progress": "true", "settings.tasks": "true"}},
			}, Limits{})
			c.call.Server.Info.ProtocolVersion = version
			ci, _ := c.e.Connect(context.Background(), c.r, c.call.Server, "test")
			if _, yes := ci.Capabilities["elicitation"]; yes {
				t.Fatal("advertised elicitation")
			}
			sw, _ := c.e.Config(context.Background(), c.r, c.call.Server)
			if sw["progress"] || sw["tasks"] {
				t.Fatalf("gate overriden: %v", sw)
			}
			got := c.run(&upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progressToken":"p","progress":1}`), resultEvent(`{"content":[]}`)}})
			if len(got.notes) != 0 || got.final == nil || got.final.Error != nil {
				t.Fatalf("version should suppress progress and preserve result: %+v", got)
			}
			b, _ := json.Marshal(got.final.Result)
			if !strings.Contains(string(b), `"resultType":"complete"`) {
				t.Fatalf("missing discriminator: %s", b)
			}
		})
	}
}

// Elicitation is declared upstream only on requests where the client
// declared it, and a server's question never reaches a client that cannot
// answer.
func TestElicitationFollowsTheRequestsClient(t *testing.T) {
	rules := []Rule{{Hook: "server.session", When: "true", Then: map[string]string{"session.capabilities.elicitation": "true"}}}
	e := &Engine{}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: mustCompile(t, rules), Catalog: Catalog{
		Tools: []*mcp.Tool{typedTool()}, Prompts: []*mcp.Prompt{{Name: "p"}}, Resources: []*mcp.Resource{{URI: "file:///doc", Name: "doc"}}}}
	ctx := context.Background()
	ask := json.RawMessage(`{"resultType":"input_required","inputRequests":{"q":{"method":"elicitation/create","params":{"message":"Which?","requestedSchema":{"type":"object","properties":{}}}}}}`)
	for _, can := range []bool{true, false} {
		r := Request{Route: "direct", Servers: []*Server{s}, Caller: map[string]any{"userId": "u"}, Client: map[string]any{"capabilities": map[string]any{}}}
		if can {
			r.Client = map[string]any{"capabilities": map[string]any{"elicitation": map[string]any{}}}
		}
		p := &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"abc"}`)}
		first := e.CallTool(ctx, r, p)
		if first.Forward == nil || first.Forward.Connect == nil {
			t.Fatalf("no forward: %+v", first.Error)
		}
		if _, on := first.Forward.Connect.Capabilities["elicitation"]; on != can {
			t.Errorf("client can elicit = %v, but the server is told %v", can, on)
		}
		if _, passed := e.UpstreamToolReply(ctx, r, first.Call, p, ask).Result.(InputRequired); passed != can {
			t.Errorf("tool: client can elicit = %v, question passed on = %v", can, passed)
		}
		pp := &mcp.GetPromptParams{Name: "p"}
		pf := e.GetPrompt(ctx, r, pp)
		if _, passed := e.UpstreamPromptReply(ctx, r, pf.Call, pp, ask).Result.(InputRequired); passed != can {
			t.Errorf("prompt: client can elicit = %v, question passed on = %v", can, passed)
		}
		rp := &mcp.ReadResourceParams{URI: "file:///doc"}
		rf := e.ReadResource(ctx, r, rp)
		if _, passed := e.UpstreamReadReply(ctx, r, rf.Call, rp, ask).Result.(InputRequired); passed != can {
			t.Errorf("read: client can elicit = %v, question passed on = %v", can, passed)
		}
	}
}

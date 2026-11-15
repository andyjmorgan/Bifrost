package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
)

// Tasks are deferred. Until they are built, a task handle a server returns
// unasked is refused and never reaches the client.
func TestUnaskedTaskHandleIsRefused(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, Static(Reply{Result: map[string]any{"resultType": "task", "taskId": "task-123", "status": "working",
		"createdAt": "2026-11-15T00:00:00Z", "lastUpdatedAt": "2026-11-15T00:00:00Z", "ttlMs": 60000, "pollIntervalMs": 1000}}))
	g := NewGateway(t)
	g.AddServer(t, "a", u)
	eq(t, "tasks switch", g.Switches("a")["tasks"], false)

	rep := Raw(t, g.Direct("a"), callReq("t"))
	lacks(t, "everything the client received", asJSON(rep.Messages()), "task-123")
	if rep.Error() == nil && rep.Result()["isError"] != true {
		t.Errorf("the task handle was not refused: %v", rep.Final())
	}
	if rt, _ := rep.Result()["resultType"].(string); rt == "task" {
		t.Error("the client received a task result")
	}
}

// Whatever a server sends back, the client gets one final message and the
// gateway keeps serving.
func TestOddUpstreamRepliesNeverBreakTheGateway(t *testing.T) {
	big := `{"content":[{"type":"text","text":"` + strings.Repeat("x", 8<<10) + `"}]}`
	cases := map[string]Reply{
		"garbage body":             {RawBody: `{"jsonrpc":"2.0","id":1,"result":{"content":[`},
		"not json at all":          {RawBody: `<html>502</html>`},
		"empty body":               {RawBody: ` `},
		"garbage on a stream":      {Stream: true, RawBody: `{"jsonrpc":`},
		"result is a string":       {RawResult: `"hello"`},
		"result is a list":         {RawResult: `[1,2,3]`},
		"result is null":           {RawResult: `null`},
		"content is a string":      {RawResult: `{"content":"hello"}`},
		"content items are odd":    {RawResult: `{"content":[1,null,"x",{"type":7}]}`},
		"structured is a list":     {RawResult: `{"content":[],"structuredContent":[1,2]}`},
		"isError is a string":      {RawResult: `{"content":[],"isError":"yes"}`},
		"input requests are odd":   {RawResult: `{"resultType":"input_required","inputRequests":[1,2]}`},
		"unknown result type":      {RawResult: `{"resultType":"mystery","content":[]}`},
		"deeply nested":            {RawResult: `{"content":[],"structuredContent":` + strings.Repeat(`{"a":`, 20000) + `1` + strings.Repeat(`}`, 20000) + `}`},
		"over the size limit":      {RawResult: big},
		"over the limit, streamed": {Stream: true, RawResult: big},
		"status 500":               {Status: 500, Body: "boom"},
		"status 404":               {Status: 404},
		"rpc error":                {Error: &RPCErr{Code: -32000, Message: "nope"}},
		"no answer on a stream":    {Stream: true, NoFinal: true},
	}
	u := NewUpstream(t, V2026)
	for name, rep := range cases {
		u.Tool(fmt.Sprintf(`{"name":%q,"inputSchema":{"type":"object","properties":{}}}`, name), Static(rep))
	}
	u.Tool(`{"name":"ok","inputSchema":{"type":"object","properties":{}}}`, nil)
	u.Resource("docs://odd", Static(Reply{RawResult: `{"contents":"nope"}`}))
	u.Resource("docs://odd2", Static(Reply{RawResult: `{"contents":[1,{"uri":5,"text":[]}]}`}))
	u.Prompt(`{"name":"odd"}`, Static(Reply{RawResult: `{"messages":"nope"}`}))
	g := NewGateway(t)
	g.Engine.Limits.MaxResponseBytes = 4 << 10
	g.AddServer(t, "a", u,
		rule("tools/result", "has(result.structuredContent)", "structuredContent", "drop(result.structuredContent, ['a.b'])"),
		rule("tools/result", "true", "content", rewriteText))

	for name := range cases {
		rep := Raw(t, g.Direct("a"), callReq(name))
		if rep.Status != 200 || !rep.Stream() {
			t.Errorf("%s: want a 200 stream, got %d", name, rep.Status)
		}
		// Every reply ends with a final message, including a lost one: the
		// gateway sends an error so no client is left waiting.
		if rep.Final() == nil {
			t.Errorf("%s: the client got no final message", name)
		}
		if name == "no answer on a stream" {
			if e := rep.Error(); e == nil || fmt.Sprint(e["code"]) != fmt.Sprint(hooks.CodeUpstreamLost) {
				t.Errorf("%s: want the lost-reply error, got %v", name, rep.Final())
			}
		}
		if strings.HasPrefix(name, "over the") && rep.Error() == nil {
			t.Errorf("%s: an oversized result was not refused: %d bytes", name, len(asJSON(rep.Final())))
		}
		// The gateway is still serving.
		ok := Raw(t, g.Direct("a"), callReq("ok"))
		if ok.Result() == nil || ok.Result()["isError"] == true {
			t.Fatalf("after %q the gateway stopped serving: %v", name, ok.Final())
		}
	}
	for _, rr := range []RawRequest{
		{Method: "resources/read", Params: map[string]any{"uri": "docs://odd"}},
		{Method: "resources/read", Params: map[string]any{"uri": "docs://odd2"}},
		{Method: "prompts/get", Params: map[string]any{"name": "odd"}},
	} {
		if rep := Raw(t, g.Direct("a"), rr); rep.Final() == nil {
			t.Errorf("%s %v: no final message", rr.Method, rr.Params)
		}
	}
	eq(t, "panics that escaped the engine", g.EscapedPanics(), 0)
	cs := Connect(t, g.Direct("a"), ClientOptions{})
	eq(t, "the SDK client still works", TextOf(mustCall(t, cs, "ok", nil)), "ok")
}

// Odd requests from a client: wrong shapes where the protocol expects an
// object. Each gets one answer, and the gateway keeps serving.
func TestOddClientRequestsNeverBreakTheGateway(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(limitTool, nil)
	g := NewGateway(t)
	g.AddServer(t, "a", u, clamp)
	bodies := []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":null}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":[1,2]}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":"x"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":7}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list","arguments":[1]}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list","arguments":"x"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list","arguments":{"limit":{"a":[null]}}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list","_meta":"x"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list","_meta":{"io.modelcontextprotocol/clientCapabilities":"x","io.modelcontextprotocol/clientInfo":7}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list","requestState":"not-ours","inputResponses":{"bifrost.ok":{"action":"accept"}}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list","inputResponses":"x"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":7}}`,
		`{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"x","arguments":{"a":1}}}`,
		`{"jsonrpc":"2.0","id":{"a":1},"method":"tools/list"}`,
	}
	for _, body := range bodies {
		rep := Raw(t, g.Direct("a"), RawRequest{Body: body})
		if rep.Status != 200 || rep.Final() == nil {
			t.Errorf("no single answer for %s: status %d, final %v", body, rep.Status, rep.Final())
		}
	}
	eq(t, "panics that escaped the engine", g.EscapedPanics(), 0)
	ok := Raw(t, g.Direct("a"), RawRequest{Method: "tools/call", Params: map[string]any{"name": "list", "arguments": map[string]any{"limit": 5}}})
	if ok.Result() == nil || ok.Result()["isError"] == true {
		t.Fatalf("the gateway stopped serving: %v", ok.Final())
	}
}

// A panic inside the engine fails that request closed. The process and the
// gateway carry on, and the engine reports the fault with the call.
func TestEnginePanicFailsClosedAndTheGatewayKeepsServing(t *testing.T) {
	u := NewUpstream(t, V2026)
	for i := 0; i < 24; i++ { // enough tools for the listing to use its workers
		u.Tool(fmt.Sprintf(`{"name":"t%02d","inputSchema":{"type":"object","properties":{"q":{"type":"string"}}}}`, i), nil)
	}
	healthy := NewUpstream(t, V2026)
	healthy.Tool(plainTool, nil)
	g := NewGateway(t)
	g.AddServer(t, "a", u, rule("tools/modify", "true", "description", "'changed'"))
	g.AddServer(t, "b", healthy)
	cs := Connect(t, g.Direct("a"), ClientOptions{})
	other := Connect(t, g.Direct("b"), ClientOptions{})

	// Inject: a corrupt catalog entry, which the engine dereferences.
	var kept []*mcp.Tool
	g.Edit("a", func(s *hooks.Server) {
		kept = s.Catalog.Tools
		s.Catalog.Tools = append(append([]*mcp.Tool(nil), kept[:12]...), nil)
		s.Catalog.Tools = append(s.Catalog.Tools, kept[12:]...)
	})

	list := Raw(t, g.Direct("a"), RawRequest{Method: "tools/list"})
	if list.Status != 200 || list.Error() == nil {
		t.Fatalf("a listing over a corrupt catalog should fail closed with an error, got %v", list.Final())
	}
	lacks(t, "the client's view of the failure", asJSON(list.Final()), "nil pointer")
	lacks(t, "the client's view of the failure", asJSON(list.Final()), "goroutine")
	rec := g.LastRecord(t, "tools/list")
	has(t, "the engine's record", rec.JSON(), `"fault"`)
	has(t, "the engine's record", rec.JSON(), `"stack"`)

	res, err := call(t, cs, "t03", map[string]any{"q": "secret-argument"})
	if !refused(res, err) {
		t.Errorf("a call over a corrupt catalog went through: %s", asJSON(res))
	}
	eq(t, "calls that reached the server", len(u.Requests("tools/call")), 0)
	callRec := g.LastRecord(t, "tools/call")
	has(t, "the fault names the call", callRec.JSON(), `"fault"`)
	has(t, "the fault carries the call's arguments", callRec.JSON(), "secret-argument")

	eq(t, "panics that escaped the engine", g.EscapedPanics(), 0)

	// Another server on the same gateway is unaffected, and the broken one
	// recovers once the catalog is sound again.
	eq(t, "a healthy server during the fault", TextOf(mustCall(t, other, "t", nil)), "ok")
	g.Edit("a", func(s *hooks.Server) { s.Catalog.Tools = kept })
	eq(t, "the same server after the fault", TextOf(mustCall(t, cs, "t03", map[string]any{"q": "x"})), "ok")
	after := Raw(t, g.Direct("a"), RawRequest{Method: "tools/list"})
	eq(t, "tools listed after the fault", len(after.Result()["tools"].([]any)), 24)
}

// Many real clients at once, on both routes, under the race detector.
func TestManyClientsInParallel(t *testing.T) {
	ua, ub := docsUpstream(t, "a"), docsUpstream(t, "b")
	g := NewGateway(t)
	g.AddServer(t, "a", ua, clamp, rule("tools/modify", "true", "description", "'Search the docs'"))
	g.AddServer(t, "b", ub, rule("tools/result", "true", "content", rewriteText))
	const clients, rounds = 12, 15
	var wg sync.WaitGroup
	errs := make(chan string, clients*rounds*4)
	for c := 0; c < clients; c++ {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			mux := c%2 == 0
			endpoint, tool, uri := g.Direct("a"), "search", "docs://a"
			if mux {
				endpoint, tool, uri = g.Mux(), "b__search", "b+docs://b"
			}
			cs, err := TryConnect(t, endpoint, ClientOptions{})
			if err != nil {
				errs <- err.Error()
				return
			}
			for i := 0; i < rounds; i++ {
				q := fmt.Sprintf("c%d-r%d", c, i)
				res, err := cs.CallTool(bg(t), &mcp.CallToolParams{Name: tool, Arguments: map[string]any{"q": q}})
				if err != nil {
					errs <- err.Error()
					continue
				}
				want := "a found " + q
				if mux {
					want = "b found " + q
				}
				if TextOf(res) != want {
					errs <- fmt.Sprintf("client %d got %q, want %q", c, TextOf(res), want)
				}
				rr, err := cs.ReadResource(bg(t), &mcp.ReadResourceParams{URI: uri})
				if err != nil || len(rr.Contents) != 1 || rr.Contents[0].URI != uri {
					errs <- fmt.Sprintf("client %d read %s: %v %s", c, uri, err, asJSON(rr))
				}
				raw, err := RawDo(bg(t), endpoint, RawRequest{Method: "tools/list"})
				if err != nil || raw.Result() == nil {
					errs <- fmt.Sprintf("client %d raw list: %v", c, err)
				}
			}
		}(c)
	}
	wg.Wait()
	close(errs)
	n := 0
	for e := range errs {
		if n < 10 {
			t.Error(e)
		}
		n++
	}
	eq(t, "panics that escaped the engine", g.EscapedPanics(), 0)
}

// The real Datadog catalog, when the fixture is present: telemetry hidden on
// every tool that has it and pinned for the server.
func TestRealDatadogCatalogThroughTheGateway(t *testing.T) {
	b, err := os.ReadFile("../../../internal/e2e/testdata/real/datadog.json")
	if err != nil {
		t.Skip("the real Datadog fixture is not present")
	}
	var f struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(b, &f); err != nil || len(f.Tools) == 0 {
		t.Skip("the real Datadog fixture has no tools array")
	}
	u := NewUpstream(t, V2026)
	withTelemetry := 0
	var names []string
	for _, raw := range f.Tools {
		var w struct {
			Tool json.RawMessage `json:"tool"`
		}
		if json.Unmarshal(raw, &w) == nil && len(w.Tool) > 0 {
			raw = w.Tool
		}
		var probe struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
			} `json:"inputSchema"`
		}
		if json.Unmarshal(raw, &probe) != nil || probe.Name == "" {
			continue
		}
		u.Tool(string(raw), nil)
		if _, ok := probe.InputSchema.Properties["telemetry"]; ok {
			withTelemetry++
			if len(probe.InputSchema.Required) == 1 && probe.InputSchema.Required[0] == "telemetry" {
				names = append(names, probe.Name)
			}
		}
	}
	if withTelemetry == 0 {
		t.Skip("no tool in the fixture has a telemetry parameter")
	}
	g := NewGateway(t)
	g.AddServer(t, "dd", u, rule("tools/modify", "'telemetry' in params",
		"params.telemetry.hide", "true", "params.telemetry.pin", "{'intent': 'mind your own business'}"))
	cs := Connect(t, g.Direct("dd"), ClientOptions{})
	lt, err := cs.ListTools(bg(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range lt.Tools {
		s, _ := tool.InputSchema.(map[string]any)
		props, _ := s["properties"].(map[string]any)
		if _, ok := props["telemetry"]; ok {
			t.Errorf("%s still shows telemetry", tool.Name)
		}
	}
	t.Logf("%d tools listed, %d had telemetry", len(lt.Tools), withTelemetry)
	// Tools whose only required parameter is telemetry can be called bare.
	for i, name := range names {
		if i == 5 {
			break
		}
		res, err := call(t, cs, name, nil)
		if refused(res, err) {
			t.Errorf("%s was refused: %v %s", name, err, asJSON(res))
			continue
		}
		eq(t, name+" upstream telemetry", u.Last(t, "tools/call").Args()["telemetry"], map[string]any{"intent": "mind your own business"})
	}
}

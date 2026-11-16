package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
)

// Every case lives in JSON under pkg/hooks/testdata and runs through
// TestGolden. The tests in this file are not cases. Each says why: it tests
// the harness's own machinery, it needs to reach inside the process, or it
// is an outlier that drives one client harness in a way data cannot say.

// Not a case: a test of the harness gateway's routing. A route the gateway
// does not have is refused with a plain 404 before any stream opens, and an
// SDK client cannot connect without the bearer token.
func TestGatewayRefusesUnknownRoutesAndMissingTokens(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, nil)
	g := NewGateway(t)
	g.Token = "secret-token"
	g.AddServer(t, "a", u)
	unknown := Raw(t, g.URL()+"/direct/nope", RawRequest{Method: "tools/list", Token: "secret-token"})
	eq(t, "unknown route", unknown.Status, http.StatusNotFound)
	cs := Connect(t, g.Direct("a"), ClientOptions{Token: "secret-token"})
	mustCall(t, cs, "t", nil)
	if _, err := TryConnect(t, g.Direct("a"), ClientOptions{}); err == nil {
		t.Error("the SDK client connected without a token")
	}
}

// Not a case: a test of the harness gateway's upstream transport. A server
// that does not speak 2026-07-28 is reached through the older handshake,
// once per forwarded request.
func TestOlderServerIsReachedThroughTheHandshake(t *testing.T) {
	for _, version := range []string{V2024, V2025, VNone} {
		u := NewUpstream(t, version)
		u.Tool(plainTool, nil)
		g := NewGateway(t)
		g.AddServer(t, "old", u)
		cs := Connect(t, g.Direct("old"), ClientOptions{})
		eq(t, version+": result", TextOf(mustCall(t, cs, "t", nil)), "ok")
		if n := len(u.Requests("initialize")); n == 0 {
			t.Errorf("%s: the server was not reached through initialize", version)
		}
	}
}

// Not a case: a test of the harness gateway's own upstream transport, which
// expectations cannot see because the runner removes these additions from
// what the upstream received. The server is told the gateway's protocol
// version and identity on every request, in the 2026-07-28 form.
func TestUpstreamSeesPerRequestIdentity(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, nil)
	g := NewGateway(t)
	g.AddServer(t, "a", u)
	cs := Connect(t, g.Direct("a"), ClientOptions{})
	mustCall(t, cs, "t", nil)
	up := u.Last(t, "tools/call")
	eq(t, "protocol version in _meta", up.Meta()[metaVersion], V2026)
	eq(t, "protocol version header", up.Header.Get("Mcp-Protocol-Version"), V2026)
	if n := len(u.Requests("initialize")); n != 0 {
		t.Errorf("a 2026-07-28 server got %d initialize requests", n)
	}
}

func callReq(name string) RawRequest {
	return RawRequest{Method: "tools/call", Params: map[string]any{"name": name, "arguments": map[string]any{}}}
}

func steps(n int) []Progress {
	var out []Progress
	for i := 1; i <= n; i++ {
		out = append(out, Progress{Progress: float64(i), Total: float64(n), Message: "step " + string(rune('0'+i))})
	}
	return out
}

// Outlier for the raw client, in code: it hangs up in the middle of a call,
// which is process control, not data. The client closing its stream is
// cancellation, and it reaches the server.
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

// Outlier for the Go SDK client, in code: it cancels a call's context
// mid-flight and records what that SDK then sends, which differs per SDK.
// Cancelling the call's context closes the stream, and the server sees its
// caller go away.
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

// Outlier for the Go SDK client, in code: it is about that SDK's own cache,
// which lives in one session across calls; the golden runner connects afresh
// for each step. The SDK client caches listings and reads for the ttlMs the
// gateway states. A deny added later is not visible in a cached listing, but it
// still stops the call, because rules run on every request.
func TestDenyAppliesAtCallTimeDespiteACachedListing(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, nil)
	g := NewGateway(t)
	g.AddServer(t, "a", u)
	cs := Connect(t, g.Direct("a"), ClientOptions{})

	lt, err := cs.ListTools(bg(t), nil)
	if err != nil || len(lt.Tools) != 1 {
		t.Fatalf("first listing: %v %v", err, lt)
	}
	mustCall(t, cs, "t", nil)

	g.SetRules(t, "a", rule("tools/list.post", "true", "decision", "'deny'"))

	again, err := cs.ListTools(bg(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("the same session lists %d tools after the deny (its cache holds the first listing for ttlMs)", len(again.Tools))
	fresh, err := Connect(t, g.Direct("a"), ClientOptions{}).ListTools(bg(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "tools a new session sees after the deny", len(fresh.Tools), 0)

	u.Forget()
	res, err := call(t, cs, "t", nil)
	if !refused(res, err) {
		t.Error("a call to a tool denied after the listing went through")
	}
	eq(t, "calls that reached the server", len(u.Requests("tools/call")), 0)
}

// Outlier for the Go SDK client, in code, for the same reason: one session,
// two calls. Reads: how many reach the gateway when one session reads the
// same URI twice. This records what the SDK client does with the ttlMs on a read
// result; it asserts only that a deny is honoured for a session without the
// content cached.
func TestReadCachingInTheSDKClient(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Resource("docs://a", nil)
	g := NewGateway(t)
	g.AddServer(t, "a", u)
	cs := Connect(t, g.Direct("a"), ClientOptions{})
	if _, err := read(t, cs, "docs://a"); err != nil {
		t.Fatal(err)
	}
	if _, err := read(t, cs, "docs://a"); err != nil {
		t.Fatal(err)
	}
	n := len(u.Requests("resources/read"))
	t.Logf("two reads of one URI on one session reached the server %d time(s)", n)

	g.SetRules(t, "a", rule("resources/list.post", "true", "decision", "'deny'"))
	_, cachedErr := read(t, cs, "docs://a")
	t.Logf("after a deny, the same session's read returns error=%v", cachedErr)
	if _, err := read(t, Connect(t, g.Direct("a"), ClientOptions{}), "docs://a"); err == nil {
		t.Error("a new session read a denied resource")
	}
}

// Not a case: it injects a panic by corrupting the engine's catalog in
// process, which no request can do. A panic inside the engine fails that
// request closed. The process and the
// gateway carry on, and the engine reports the fault with the call.
func TestEnginePanicFailsClosedAndTheGatewayKeepsServing(t *testing.T) {
	u := NewUpstream(t, V2026)
	for i := 0; i < 24; i++ { // enough tools for the listing to use its workers
		u.Tool(fmt.Sprintf(`{"name":"t%02d","inputSchema":{"type":"object","properties":{"q":{"type":"string"}}}}`, i), nil)
	}
	healthy := NewUpstream(t, V2026)
	healthy.Tool(plainTool, nil)
	g := NewGateway(t)
	g.AddServer(t, "a", u, rule("tools/list.post", "true", "description", "'changed'"))
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

// Not a case: a concurrency test. Many real clients at once, on both routes,
// under the race detector.
func TestManyClientsInParallel(t *testing.T) {
	ua, ub := docsUpstream(t, "a"), docsUpstream(t, "b")
	g := NewGateway(t)
	g.AddServer(t, "a", ua, clamp, rule("tools/list.post", "true", "description", "'Search the docs'"))
	g.AddServer(t, "b", ub, rule("tools/call.post.result", "true", "content", rewriteText))
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

func docsUpstream(t *testing.T, tag string) *Upstream {
	u := NewUpstream(t, V2026)
	u.Name = "docs-" + tag
	u.Instructions = "Instructions of " + tag
	u.Tool(`{"name":"search","inputSchema":{"type":"object","properties":{"q":{"type":"string"}}}}`, func(r Recorded) Reply {
		q, _ := r.Args()["q"].(string)
		return Reply{Result: map[string]any{"content": []any{
			map[string]any{"type": "text", "text": tag + " found " + q},
			map[string]any{"type": "resource_link", "uri": "docs://" + tag, "name": "doc"},
		}}}
	})
	u.Resource("docs://"+tag, nil)
	u.Resource("docs://shared", nil)
	u.Template("notes://" + tag + "/{id}")
	u.ReadDefault(func(r Recorded) Reply {
		uri, _ := r.Map()["uri"].(string)
		return Reply{Result: Contents(uri, tag+" read "+uri)}
	})
	u.Prompt(`{"name":"greet","arguments":[{"name":"who","required":true}]}`, func(r Recorded) Reply {
		who, _ := r.Args()["who"].(string)
		return Reply{Result: map[string]any{"messages": []any{map[string]any{"role": "user", "content": map[string]any{"type": "text", "text": tag + " greets " + who}}}}}
	})
	return u
}

func read(t testing.TB, cs *mcp.ClientSession, uri string) (*mcp.ReadResourceResult, error) {
	t.Helper()
	return cs.ReadResource(bg(t), &mcp.ReadResourceParams{URI: uri})
}

// Rules the concurrency test runs under.
var clamp = rule("tools/call.pre", "has(args.limit) && args.limit > 100", "args.limit", "100")

const rewriteText = "result.content.map(c, c.type == 'text' ? {'type': 'text', 'text': regex.replace(c.text, 'Datadog', 'Bits')} : c)"

// A client may set the search flag on its own request. It may not set the
// cli flag: that one says how the request arrived, the gateway sets it, and
// rules rely on it. A client that appends ?cli=true gets nothing for it.
func TestClientCannotSetTheCLIFlag(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(`{"name":"t","inputSchema":{"type":"object"}}`, Static(Reply{Result: Text("done")}))
	g := NewGateway(t)
	g.AddServer(t, "a", u,
		hooks.Rule{Hook: "tools/call.pre", When: "flags.cli", Then: map[string]string{"decision": "'deny'", "reason": "'cli flag seen'"}})
	call := RawRequest{Method: "tools/call", Params: map[string]any{"name": "t", "arguments": map[string]any{}}}
	text := func(rep *RawReply) string {
		b, _ := json.Marshal(rep.Messages())
		return string(b)
	}

	if got := text(Raw(t, g.Direct("a")+"?cli=true", call)); strings.Contains(got, "cli flag seen") || !strings.Contains(got, "done") {
		t.Fatalf("a client set flags.cli from its query string: %s", got)
	}
	// The search flag is a client's to set, on the same request path.
	if got := text(Raw(t, g.Direct("a")+"?search=true", RawRequest{Method: "tools/list"})); !strings.Contains(got, "tool_search") {
		t.Fatalf("the search flag, which a client may set, was not applied: %s", got)
	}
	// The gateway's own side can set cli; in this rig that is the test switch.
	g.TestHooks = true
	if got := text(Raw(t, g.Direct("a")+"?cli=true", call)); !strings.Contains(got, "cli flag seen") {
		t.Fatalf("the gateway's own cli flag did not reach the rules: %s", got)
	}
}

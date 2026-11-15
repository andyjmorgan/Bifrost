package clients

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness"
)

// The second half of the battery: confirmations, input requests, how calls
// end, caching, headers, content, and the gateway's own results.

var confirmRule = rule("tools/call", "!has(input.ok)", "result", "elicit('ok', 'Delete it?')")

func init() {
	checks = append(checks, []check{

		// Outlier, in code: it cancels a call mid-flight and records what each SDK then sends, which differs per SDK.
		{id: "cancel", title: "Cancel: what the client sends, and whether the server sees it", run: func(e *env) outcome {
			u := harness.NewUpstream(e.t, harness.V2026)
			u.Tool(plainTool, harness.Static(harness.Reply{Stream: true, Hang: true}))
			e.g.AddServer(e.t, "a", u)
			if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{}); out != nil {
				return *out
			}
			e.r.Forget()
			rep := e.call(CallOptions{Method: "tools/call", Params: map[string]any{"name": "t", "arguments": map[string]any{}}, CancelAfterMs: 300})
			sawCancel := u.WaitCancelled(1, 3*time.Second)
			time.Sleep(100 * time.Millisecond)
			var signals []string
			for _, s := range e.r.Seen("tools/call") {
				if s.ClientGone {
					signals = append(signals, "closed the stream")
				}
			}
			for _, s := range e.r.Seen("notifications/cancelled") {
				signals = append(signals, fmt.Sprintf("sent notifications/cancelled (answered %d)", s.Status))
			}
			if len(signals) == 0 {
				signals = []string{"sent nothing"}
			}
			what := strings.Join(signals, " and ")
			if rep.Error == nil {
				return diff("the cancelled call returned a result: %s; it %s", asJSON(rep.Result), what)
			}
			if !sawCancel {
				return diff("it %s, but the server never saw the call end", what)
			}
			return ok("it %s; the server saw the call end", what)
		}},

		// Outlier, in code: the client talks straight to a server, with no gateway in between, to see what the SDK itself tolerates.
		{id: "stray-notifications", title: "Unknown notifications and log lines on a stream (client straight to a server)", run: func(e *env) outcome {
			// The gateway drops these, so this check points the client at the
			// mock server itself to see what the client does with them.
			u := harness.NewUpstream(e.t, harness.V2026)
			u.Tool(plainTool, harness.Static(harness.Reply{Stream: true, Result: harness.Text("done"), Notes: []harness.Note{
				{Method: "notifications/vendor/custom", Params: map[string]any{"x": 1}},
				{Method: "notifications/message", Params: map[string]any{"level": "info", "data": "a log line nobody asked for"}},
				{Method: "notifications/tools/list_changed", Params: map[string]any{}},
			}}))
			// The mock leaves ttlMs and cacheScope off its listings, and the
			// Python SDK requires them. This is the only patch in the package,
			// and it is on the mock server's replies, never the gateway's.
			direct := NewRecorder(e.t, u.URL())
			direct.PatchCacheHints(true)
			ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
			defer cancel()
			rep, err := e.d.Connect(ctx, ConnectOptions{URL: direct.Via(u.URL()), ReadTimeoutSeconds: 8})
			if err != nil {
				e.t.Fatalf("driver failed on connect: %v", err)
			}
			if rep.Error != nil {
				return none("could not connect to the mock server directly: %s", trim(rep.Error.Message, 200))
			}
			call := e.tool("t", nil)
			if call.Error != nil || textOf(call.Result) != "done" {
				return diff("%s, result %s", errText(call.Error), asJSON(call.Result))
			}
			return ok("ignored all three and got the result")
		}},

		// Outlier, in code: each SDK's cache lives in one session across calls and is configured per SDK.
		{id: "caching", title: "Caching: does a second list or read reach the gateway within ttlMs", run: func(e *env) outcome {
			u := harness.NewUpstream(e.t, harness.V2026)
			u.Tool(plainTool, nil)
			u.Resource("docs://a", nil)
			e.g.AddServer(e.t, "a", u)
			if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{}); out != nil {
				return *out
			}
			e.r.Forget()
			var parts []string
			cached := false
			for _, m := range []struct {
				method string
				params map[string]any
			}{{"tools/list", nil}, {"resources/list", nil}, {"resources/read", map[string]any{"uri": "docs://a"}}} {
				for i := 0; i < 2; i++ {
					if rep := e.call(CallOptions{Method: m.method, Params: m.params}); rep.Error != nil {
						return diff("%s: %s", m.method, errText(rep.Error))
					}
				}
				n := len(e.r.Seen(m.method))
				if n < 2 {
					cached = true
				}
				parts = append(parts, fmt.Sprintf("%s reached the gateway %d of 2 times", m.method, n))
			}
			if cached {
				return ok("caches: %s. A new deny does not reach content this client already holds until ttlMs runs out", strings.Join(parts, "; "))
			}
			return ok("does not cache: %s", strings.Join(parts, "; "))
		}},

		// Outlier, in code: Mcp-Name is filled from a listing made earlier in the same session, so it needs one session across two calls.
		{id: "headers", title: "HTTP headers on a request", run: func(e *env) outcome {
			u := harness.NewUpstream(e.t, harness.V2026)
			u.Tool(plainTool, nil)
			e.g.AddServer(e.t, "a", u)
			if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{}); out != nil {
				return *out
			}
			e.call(CallOptions{Method: "tools/list"}) // some clients need the listing to fill Mcp-Name
			e.r.Forget()
			e.tool("t", nil)
			calls := e.r.Seen("tools/call")
			if len(calls) == 0 {
				return diff("the call did not reach the gateway")
			}
			h := calls[0].Header
			got := fmt.Sprintf("Mcp-Method=%q Mcp-Name=%q MCP-Protocol-Version=%q Accept=%q", h.Get("Mcp-Method"), h.Get("Mcp-Name"), h.Get("Mcp-Protocol-Version"), h.Get("Accept"))
			var missing []string
			if h.Get("Mcp-Method") != "tools/call" {
				missing = append(missing, "Mcp-Method")
			}
			if h.Get("Mcp-Name") != "t" {
				missing = append(missing, "Mcp-Name")
			}
			if h.Get("Mcp-Protocol-Version") != harness.V2026 {
				missing = append(missing, "MCP-Protocol-Version")
			}
			if !strings.Contains(h.Get("Accept"), "application/json") || !strings.Contains(h.Get("Accept"), "text/event-stream") {
				missing = append(missing, "Accept with both forms")
			}
			if len(missing) > 0 {
				return diff("missing or wrong: %s. Sent: %s", strings.Join(missing, ", "), got)
			}
			return ok("%s", got)
		}},

		// Outlier, in code: the payloads are megabytes, generated on the fly, and each SDK has its own size limit (Python stops at 1 MiB per event).
		{id: "large-result", title: "A large result on a stream", run: func(e *env) outcome {
			u := harness.NewUpstream(e.t, harness.V2026)
			u.Tool(`{"name":"big","inputSchema":{"type":"object","properties":{"kb":{"type":"integer"}}}}`, func(r harness.Recorded) harness.Reply {
				kb, _ := r.Args()["kb"].(float64)
				return harness.Reply{Result: harness.Text(strings.Repeat("x", int(kb)<<10))}
			})
			e.g.AddServer(e.t, "a", u)
			if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{}); out != nil {
				return *out
			}
			var passed []string
			for _, kb := range []int{512, 1100, 4096, 9000} {
				rep := e.tool("big", map[string]any{"kb": kb})
				if rep.Error != nil || len(textOf(rep.Result)) != kb<<10 {
					fail := errText(rep.Error)
					if rep.Error == nil {
						fail = fmt.Sprintf("got %d bytes", len(textOf(rep.Result)))
					}
					return diff("passed %v; failed at %d KiB: %s", passed, kb, fail)
				}
				passed = append(passed, fmt.Sprintf("%d KiB", kb))
			}
			return ok("up to %s", passed[len(passed)-1])
		}},
	}...)
}

func init() {
	checks = append(checks, []check{}...)
}

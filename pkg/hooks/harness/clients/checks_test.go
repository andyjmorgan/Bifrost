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

// confirmCheck answers the gateway's confirmation one way and checks the
// outcome: a yes forwards the call, anything else refuses it.
func confirmCheck(answer Answer, yes bool) func(e *env) outcome {
	return func(e *env) outcome {
		u := harness.NewUpstream(e.t, harness.V2026)
		u.Tool(plainTool, nil)
		e.g.AddServer(e.t, "a", u, confirmRule)
		if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{Capabilities: withElicitation}); out != nil {
			return *out
		}
		rep := e.call(CallOptions{Method: "tools/call", Params: map[string]any{"name": "t", "arguments": map[string]any{}}, Elicit: []Answer{answer}})
		reached := len(u.Requests("tools/call"))
		if len(rep.Elicitations) != 1 {
			return diff("the SDK's handler was asked %d times; reply: %s %s", len(rep.Elicitations), errText(rep.Error), asJSON(rep.Result))
		}
		asked := rep.Elicitations[0]
		if asked["message"] != "Delete it?" {
			return diff("the handler was asked %q", asked["message"])
		}
		schema, _ := asked["requestedSchema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		confirm, _ := props["confirm"].(map[string]any)
		if confirm["type"] != "boolean" {
			return diff("the form did not reach the handler intact: %s", asJSON(asked))
		}
		if yes {
			if rep.Error != nil || textOf(rep.Result) != "ok" || reached != 1 {
				return diff("a confirmed call: %s, result %s, server reached %d times", errText(rep.Error), asJSON(rep.Result), reached)
			}
			return ok("the call went ahead once")
		}
		if reached != 0 {
			return diff("the server was called %d times after a no", reached)
		}
		if rep.Error != nil {
			return diff("the refusal arrived as %s, not as an error result", errText(rep.Error))
		}
		if !isError(rep.Result) || textOf(rep.Result) != "Declined." {
			return diff("the refusal was %s", asJSON(rep.Result))
		}
		return ok("refused with Declined.; the server was not called")
	}
}

func init() {
	checks = append(checks, []check{
		{id: "confirm-accept", title: "Gateway confirmation: accept with the box ticked", run: confirmCheck(Answer{Action: "accept", Content: map[string]any{"confirm": true}}, true)},
		{id: "confirm-decline", title: "Gateway confirmation: decline", run: confirmCheck(Answer{Action: "decline"}, false)},
		{id: "confirm-cancel", title: "Gateway confirmation: cancel", run: confirmCheck(Answer{Action: "cancel"}, false)},
		{id: "confirm-unticked", title: "Gateway confirmation: accept with the box unticked", run: confirmCheck(Answer{Action: "accept", Content: map[string]any{"confirm": false}}, false)},

		{id: "confirm-no-handler", title: "Gateway confirmation: a client with no elicitation handler", run: func(e *env) outcome {
			u := harness.NewUpstream(e.t, harness.V2026)
			u.Tool(plainTool, nil)
			e.g.AddServer(e.t, "a", u, confirmRule)
			if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{}); out != nil {
				return *out
			}
			rep := e.tool("t", nil)
			if n := len(u.Requests("tools/call")); n != 0 {
				return diff("the server was called %d times with no confirmation", n)
			}
			if rep.Error != nil {
				return diff("the refusal arrived as %s", errText(rep.Error))
			}
			if !isError(rep.Result) || !strings.Contains(textOf(rep.Result), "does not support") {
				return diff("the refusal was %s", asJSON(rep.Result))
			}
			return ok("refused: %q", trim(textOf(rep.Result), 90))
		}},

		{id: "upstream-input", title: "A server's own input request: the answer reaches the server", run: func(e *env) outcome {
			u := harness.NewUpstream(e.t, harness.V2026)
			u.Tool(`{"name":"create_issue","inputSchema":{"type":"object","properties":{"title":{"type":"string"}}}}`, func(r harness.Recorded) harness.Reply {
				answers, _ := r.Map()["inputResponses"].(map[string]any)
				if answers == nil {
					return harness.Reply{Result: map[string]any{"resultType": "input_required", "requestState": "up1", "inputRequests": map[string]any{
						"q": map[string]any{"method": "elicitation/create", "params": map[string]any{"mode": "form", "message": "Which project?",
							"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{"project": map[string]any{"type": "string"}}}}}}}}
				}
				q, _ := answers["q"].(map[string]any)
				action, _ := q["action"].(string)
				return harness.Reply{Result: harness.Text("server saw " + action)}
			})
			e.g.AddServer(e.t, "a", u, rule("server/connect", "true", "capabilities.elicitation", "true"))
			if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{Capabilities: withElicitation}); out != nil {
				return *out
			}
			rep := e.call(CallOptions{Method: "tools/call", Params: map[string]any{"name": "create_issue", "arguments": map[string]any{"title": "x"}},
				Elicit: []Answer{{Action: "accept", Content: map[string]any{"project": "OPS"}}}})
			if rep.Error != nil || textOf(rep.Result) != "server saw accept" {
				return diff("the call: %s, result %s", errText(rep.Error), asJSON(rep.Result))
			}
			calls := u.Requests("tools/call")
			if len(calls) != 2 {
				return diff("the server was called %d times, want 2", len(calls))
			}
			second := calls[1].Map()
			got := asJSON(second["inputResponses"])
			if second["requestState"] != "up1" || !strings.Contains(got, `"project":"OPS"`) {
				return diff("the server's retry carried state %v and answers %s", second["requestState"], got)
			}
			if len(rep.Elicitations) != 1 || rep.Elicitations[0]["message"] != "Which project?" {
				return diff("the handler was asked %s", asJSON(rep.Elicitations))
			}
			return ok("the server got its own state back and the answer")
		}},

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

		{id: "no-response", title: "A stream that ends with no response", run: func(e *env) outcome {
			u := harness.NewUpstream(e.t, harness.V2026)
			u.Tool(plainTool, harness.Static(harness.Reply{Stream: true, NoFinal: true, Progress: []harness.Progress{{Progress: 1}}}))
			e.g.AddServer(e.t, "a", u)
			if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{ReadTimeoutSeconds: 4}); out != nil {
				return *out
			}
			e.r.Forget()
			start := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			rep, err := e.d.Call(ctx, CallOptions{Method: "tools/call", Params: map[string]any{"name": "t", "arguments": map[string]any{}}})
			took := time.Since(start).Round(100 * time.Millisecond)
			sent := len(e.r.Seen("tools/call"))
			reached := len(u.Requests("tools/call"))
			if err != nil {
				return diff("the call never returned (waited %s); it sent the request %d times and the server ran it %d times", took, sent, reached)
			}
			if rep.Error == nil {
				return diff("it returned a result although none was sent: %s", asJSON(rep.Result))
			}
			note := fmt.Sprintf("failed after %s with: %s", took, errText(rep.Error))
			if reached > 1 {
				return diff("it sent the request again by itself: the server ran the tool %d times; %s", reached, note)
			}
			if took > 2*time.Second {
				return diff("it did not notice the stream ending and waited for its own timeout; %s", note)
			}
			return ok("%s; the tool ran once", note)
		}},

		{id: "keep-alive", title: "A slow server, with comment keep-alives on the stream", run: func(e *env) outcome {
			u := harness.NewUpstream(e.t, harness.V2026)
			u.Tool(plainTool, harness.Static(harness.Reply{Delay: 700 * time.Millisecond, Result: harness.Text("slow but done")}))
			e.g.KeepAlive = 50 * time.Millisecond
			e.g.AddServer(e.t, "a", u)
			if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{}); out != nil {
				return *out
			}
			rep := e.tool("t", nil)
			if rep.Error != nil || textOf(rep.Result) != "slow but done" {
				return diff("%s, result %s", errText(rep.Error), asJSON(rep.Result))
			}
			return ok("ignored the keep-alives and got the result")
		}},

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

		{id: "content", title: "A result with image, audio, link, embedded resource and structured content", run: func(e *env) outcome {
			u := harness.NewUpstream(e.t, harness.V2026)
			u.Tool(`{"name":"rich"}`, harness.Static(harness.Reply{Result: map[string]any{
				"content": []any{
					map[string]any{"type": "text", "text": "hello"},
					map[string]any{"type": "image", "data": "iVBORw0KGgo=", "mimeType": "image/png"},
					map[string]any{"type": "audio", "data": "UklGRg==", "mimeType": "audio/wav"},
					map[string]any{"type": "resource_link", "uri": "docs://a", "name": "a"},
					map[string]any{"type": "resource", "resource": map[string]any{"uri": "docs://b", "text": "embedded", "mimeType": "text/plain"}},
				},
				"structuredContent": map[string]any{"count": 3, "rows": []any{map[string]any{"id": 1}}},
			}}))
			u.Tool(`{"name":"odd"}`, harness.Static(harness.Reply{Result: map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "before"},
				map[string]any{"type": "hologram", "url": "https://example.com/h"},
			}}}))
			e.g.AddServer(e.t, "a", u)
			if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{}); out != nil {
				return *out
			}
			rich := e.tool("rich", nil)
			if rich.Error != nil {
				return diff("the result was rejected: %s", errText(rich.Error))
			}
			var types []string
			blocks, _ := rich.Result["content"].([]any)
			for _, b := range blocks {
				m, _ := b.(map[string]any)
				ty, _ := m["type"].(string)
				types = append(types, ty)
			}
			if strings.Join(types, ",") != "text,image,audio,resource_link,resource" {
				return diff("the blocks came through as %v", types)
			}
			sc, _ := rich.Result["structuredContent"].(map[string]any)
			if sc["count"] != float64(3) {
				return diff("structuredContent came through as %s", asJSON(rich.Result["structuredContent"]))
			}
			odd := e.tool("odd", nil)
			if odd.Error != nil {
				return ok("all five kinds and structured content; a block of an unknown type makes it reject the whole result (%s)", trim(odd.Error.Message, 110))
			}
			return ok("all five kinds and structured content; a block of an unknown type is passed through")
		}},

		{id: "auth-status", title: "A plain HTTP status before the stream opens (401)", run: func(e *env) outcome {
			u := harness.NewUpstream(e.t, harness.V2026)
			u.Tool(plainTool, nil)
			e.g.Token = "s3cret"
			e.g.AddServer(e.t, "a", u)
			bad := e.connect(e.g.Direct("a"), ConnectOptions{})
			if bad.Error == nil {
				return diff("connected without a token")
			}
			if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{Token: "s3cret"}); out != nil {
				return diff("could not connect with the token: %s", out.note)
			}
			if rep := e.tool("t", nil); rep.Error != nil || textOf(rep.Result) != "ok" {
				return diff("with the token: %s", errText(rep.Error))
			}
			return ok("without a token: %s", errText(bad.Error))
		}},

		{id: "rule-mutations", title: "Rule changes reach the client: rename, pin, description, result rewrite", run: func(e *env) outcome {
			u := harness.NewUpstream(e.t, harness.V2026)
			u.Tool(`{"name":"find","description":"Find pages in Confluence","inputSchema":{"type":"object","properties":{"q":{"type":"string"},"space":{"type":"string"}},"required":["q","space"]}}`,
				func(r harness.Recorded) harness.Reply {
					return harness.Reply{Result: map[string]any{
						"content":           []any{map[string]any{"type": "text", "text": "token=abc123 found in " + fmt.Sprint(r.Args()["space"])}},
						"structuredContent": map[string]any{"space": r.Args()["space"], "token": "abc123"},
					}}
				})
			e.g.AddServer(e.t, "a", u,
				rule("tools/modify", "upstream.name == 'find'", "name", "'search'", "description", "'Search the wiki'", "params.space.pin", "'ENG'", "params.space.hide", "true"),
				rule("tools/result", "true",
					"content", "result.content.map(c, c.type == 'text' ? {'type': 'text', 'text': regex.replace(c.text, 'token=[a-z0-9]+', 'token=[hidden]')} : c)",
					"structuredContent", "drop(result.structuredContent, ['token'])"))
			if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{}); out != nil {
				return *out
			}
			list := e.call(CallOptions{Method: "tools/list"})
			if list.Error != nil {
				return diff("tools/list: %s", errText(list.Error))
			}
			tools, _ := list.Result["tools"].([]any)
			if len(tools) != 1 {
				return diff("listed %d tools", len(tools))
			}
			tool, _ := tools[0].(map[string]any)
			schema := asJSON(tool["inputSchema"])
			if tool["name"] != "search" || tool["description"] != "Search the wiki" || strings.Contains(schema, "space") {
				return diff("the listed tool was %s", asJSON(tool))
			}
			rep := e.tool("search", map[string]any{"q": "runbook"})
			if rep.Error != nil {
				return diff("call: %s", errText(rep.Error))
			}
			got := u.Last(e.t, "tools/call").Map()
			if got["name"] != "find" || asJSON(got["arguments"]) != `{"q":"runbook","space":"ENG"}` {
				return diff("the server received %s %s", got["name"], asJSON(got["arguments"]))
			}
			if textOf(rep.Result) != "token=[hidden] found in ENG" || strings.Contains(asJSON(rep.Result), "abc123") {
				return diff("the result was %s", asJSON(rep.Result))
			}
			return ok("renamed, pinned, reworded and rewritten end to end")
		}},

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
	checks = append(checks, []check{
		{id: "refusal-errors", title: "A refused prompt or read arrives as a JSON-RPC error with the rule's reason", run: func(e *env) outcome {
			u := harness.NewUpstream(e.t, harness.V2026)
			u.Prompt(`{"name":"greet"}`, nil)
			u.Resource("docs://a", nil)
			e.g.AddServer(e.t, "a", u,
				rule("prompts/get", "true", "decision", "'deny'", "reason", "'Prompts are off.'"),
				rule("resources/read", "true", "decision", "'deny'", "reason", "'Reads are off.'"))
			if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{}); out != nil {
				return *out
			}
			p := e.call(CallOptions{Method: "prompts/get", Params: map[string]any{"name": "greet"}})
			r := e.call(CallOptions{Method: "resources/read", Params: map[string]any{"uri": "docs://a"}})
			for _, x := range []struct {
				what, want string
				rep        Reply
			}{{"prompt", "Prompts are off.", p}, {"read", "Reads are off.", r}} {
				if x.rep.Error == nil {
					return diff("the refused %s returned a result: %s", x.what, asJSON(x.rep.Result))
				}
				if x.rep.Error.Kind != "rpc" || !strings.Contains(x.rep.Error.Message, x.want) {
					return diff("the refused %s was reported as %s", x.what, errText(x.rep.Error))
				}
			}
			if n := len(u.Requests("prompts/get")) + len(u.Requests("resources/read")); n != 0 {
				return diff("%d refused requests reached the server", n)
			}
			return ok("both as code %d with the reason", p.Error.RPCCode())
		}},

		{id: "confirm-prompt-read", title: "Gateway confirmation on a prompt and on a read", run: func(e *env) outcome {
			u := harness.NewUpstream(e.t, harness.V2026)
			u.Prompt(`{"name":"greet"}`, nil)
			u.Resource("docs://a", nil)
			e.g.AddServer(e.t, "a", u,
				rule("prompts/get", "!has(input.ok)", "result", "elicit('ok', 'Use this prompt?')"),
				rule("resources/read", "!has(input.ok)", "result", "elicit('ok', 'Read this?')"))
			if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{Capabilities: withElicitation}); out != nil {
				return *out
			}
			yes := []Answer{{Action: "accept", Content: map[string]any{"confirm": true}}}
			no := []Answer{{Action: "decline"}}
			p := e.call(CallOptions{Method: "prompts/get", Params: map[string]any{"name": "greet"}, Elicit: yes})
			if p.Error != nil || len(u.Requests("prompts/get")) != 1 || len(p.Elicitations) != 1 || p.Elicitations[0]["message"] != "Use this prompt?" {
				return diff("a confirmed prompt: %s; server reached %d times; asked %s", errText(p.Error), len(u.Requests("prompts/get")), asJSON(p.Elicitations))
			}
			r := e.call(CallOptions{Method: "resources/read", Params: map[string]any{"uri": "docs://a"}, Elicit: yes})
			if r.Error != nil || len(u.Requests("resources/read")) != 1 || !strings.Contains(asJSON(r.Result), "content of docs://a") {
				return diff("a confirmed read: %s; server reached %d times; result %s", errText(r.Error), len(u.Requests("resources/read")), asJSON(r.Result))
			}
			u.Forget()
			pn := e.call(CallOptions{Method: "prompts/get", Params: map[string]any{"name": "greet"}, Elicit: no})
			if pn.Error == nil || !strings.Contains(pn.Error.Message, "Declined.") || len(u.Requests("prompts/get")) != 0 {
				return diff("a declined prompt: %s; server reached %d times", errText(pn.Error), len(u.Requests("prompts/get")))
			}
			return ok("confirmed ones went ahead; a declined prompt was refused with Declined.")
		}},

		{id: "confirm-two-rounds", title: "Two gateway confirmations in a row", run: func(e *env) outcome {
			u := harness.NewUpstream(e.t, harness.V2026)
			u.Tool(plainTool, nil)
			e.g.AddServer(e.t, "a", u,
				rule("tools/call", "!has(input.first)", "result", "elicit('first', 'First?')"),
				rule("tools/call", "has(input.first) && !has(input.second)", "result", "elicit('second', 'Second?')"))
			if out := e.mustConnect(e.g.Direct("a"), ConnectOptions{Capabilities: withElicitation}); out != nil {
				return *out
			}
			yes := Answer{Action: "accept", Content: map[string]any{"confirm": true}}
			rep := e.call(CallOptions{Method: "tools/call", Params: map[string]any{"name": "t", "arguments": map[string]any{}}, Elicit: []Answer{yes, yes}})
			var asked []string
			for _, q := range rep.Elicitations {
				asked = append(asked, fmt.Sprint(q["message"]))
			}
			if rep.Error != nil || textOf(rep.Result) != "ok" || strings.Join(asked, "|") != "First?|Second?" || len(u.Requests("tools/call")) != 1 {
				return diff("%s, result %s, asked %v, server reached %d times", errText(rep.Error), asJSON(rep.Result), asked, len(u.Requests("tools/call")))
			}
			u.Forget()
			second := e.call(CallOptions{Method: "tools/call", Params: map[string]any{"name": "t", "arguments": map[string]any{}}, Elicit: []Answer{yes, {Action: "decline"}}})
			if second.Error != nil || textOf(second.Result) != "Declined." || len(u.Requests("tools/call")) != 0 {
				return diff("yes then no: %s, result %s, server reached %d times", errText(second.Error), asJSON(second.Result), len(u.Requests("tools/call")))
			}
			return ok("asked twice in order and went ahead once; yes then no was refused")
		}},

		{id: "search-mode", title: "Search mode: the gateway's own tools", run: func(e *env) outcome {
			u := harness.NewUpstream(e.t, harness.V2026)
			u.Tool(`{"name":"find_pages","description":"Find wiki pages by text","inputSchema":{"type":"object","properties":{"q":{"type":"string"}}}}`, func(r harness.Recorded) harness.Reply {
				return harness.Reply{Result: harness.Text("pages for " + fmt.Sprint(r.Args()["q"]))}
			})
			e.g.AddServer(e.t, "a", u)
			if out := e.mustConnect(e.g.Search(), ConnectOptions{}); out != nil {
				return *out
			}
			list := e.call(CallOptions{Method: "tools/list"})
			if list.Error != nil || !has(names(list.Result, "tools", "name"), "tool_search", "list_tools", "get_tool_definition", "execute_tool") {
				return diff("tools/list: %s, tools %v", errText(list.Error), names(list.Result, "tools", "name"))
			}
			found := e.tool("tool_search", map[string]any{"query": "wiki pages"})
			if found.Error != nil || !strings.Contains(asJSON(found.Result), "find_pages") {
				return diff("tool_search: %s, result %s", errText(found.Error), trim(asJSON(found.Result), 200))
			}
			var name string
			if sc, ok := found.Result["structuredContent"].(map[string]any); ok {
				if ts, ok := sc["tools"].([]any); ok && len(ts) > 0 {
					name, _ = ts[0].(map[string]any)["name"].(string)
				}
			}
			if name == "" {
				return diff("tool_search gave no tool name to run: %s", trim(asJSON(found.Result), 200))
			}
			run := e.tool("execute_tool", map[string]any{"name": name, "arguments": map[string]any{"q": "runbook"}})
			if run.Error != nil || textOf(run.Result) != "pages for runbook" {
				return diff("execute_tool %s: %s, result %s", name, errText(run.Error), trim(asJSON(run.Result), 200))
			}
			return ok("listed the four tools, searched, and ran %s through execute_tool", name)
		}},
	}...)
}

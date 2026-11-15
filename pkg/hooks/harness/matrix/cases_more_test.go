package matrix

import (
	"context"
	"strings"
)

const (
	instructions = "Reference server.\n\n## Notes\nUse search first."
	guideText    = "# Guide\nSee ref://docs/faq and https://example.com/x."
)

var promptCases = []tcase{
	{id: "prompts/filter", title: "hide a prompt; getting it is refused",
		rules: rules(rule("prompts/filter", "name == 'summarize'", "decision", "'deny'")),
		run: func(e *env) {
			e.eq("prompts", names(e.result("prompts/list", e.list("prompts/list")), "prompts", "name"), []string{"greet"})
			e.wantRPC("get the hidden prompt", e.prompt("summarize", map[string]any{"topic": "x"}), -32602, "unknown prompt summarize")
		}},
	{id: "prompts/modify", title: "rename a prompt and an argument, change its text",
		rules: rules(rule("prompts/modify", "upstream.name == 'greet'",
			"name", "'hello'", "title", "'Hello'", "description", "'Say hello.'",
			"arguments.who.name", "'person'", "arguments.who.description", "'Who to greet.'", "meta.kind", "'greeting'")),
		run: func(e *env) {
			p := e.item("prompts/list", e.list("prompts/list"), "prompts", "name", "hello")
			e.eq("title", p["title"], "Hello")
			e.eq("description", p["description"], "Say hello.")
			e.eq("_meta.kind", get(p, "_meta", "kind"), "greeting")
			args, _ := p["arguments"].([]any)
			if len(args) != 1 {
				e.t.Fatalf("arguments: %s", asJSON(p["arguments"]))
			}
			e.eq("argument name", get(args[0], "name"), "person")
			e.eq("argument description", get(args[0], "description"), "Who to greet.")
			e.eq("argument required", get(args[0], "required"), true)
			e.eq("get by the new names", promptText(e.result("get", e.prompt("hello", map[string]any{"person": "Ana"}))), "Hello, Ana!")
			e.wantRPC("the old argument name", e.prompt("hello", map[string]any{"who": "Ana"}), -32602, `argument "who" is not available on this prompt`)
			e.wantRPC("the old prompt name", e.prompt("greet", map[string]any{"who": "Ana"}), -32602, "unknown prompt greet")
		}},
	{id: "prompts/get patch", title: "fill a missing argument",
		rules: rules(rule("prompts/get", "upstream.name == 'summarize' && !has(args.tone)", "args.tone", "'formal'")),
		run: func(e *env) {
			e.eq("filled", promptText(e.result("get", e.prompt("summarize", map[string]any{"topic": "oncall"}))), "Summarize oncall in a formal tone.")
			e.eq("kept", promptText(e.result("get", e.prompt("summarize", map[string]any{"topic": "oncall", "tone": "dry"}))), "Summarize oncall in a dry tone.")
		}},
	{id: "prompts/get deny", title: "deny on an argument value, with a reason",
		rules: rules(rule("prompts/get", "has(args.who) && args.who == 'root'", "decision", "'deny'", "reason", "'Greeting root is not allowed.'")),
		run: func(e *env) {
			e.wantRPC("denied", e.prompt("greet", map[string]any{"who": "root"}), -32602, "Greeting root is not allowed.")
			e.eq("allowed", promptText(e.result("get", e.prompt("greet", map[string]any{"who": "Ana"}))), "Hello, Ana!")
		}},
	{id: "prompts/get confirm", title: "confirm before a prompt: accept, then decline",
		rules: rules(rule("prompts/get", "upstream.name == 'greet' && !has(input.ok)", "result", "elicit('ok', 'Greet {who}?', {'who': args.who})")),
		caps:  withElicitation,
		run: func(e *env) {
			rep := e.prompt("greet", map[string]any{"who": "Ana"}, answers(accept(map[string]any{"confirm": true})))
			e.eq("accepted", promptText(e.result("accepted", rep)), "Hello, Ana!")
			e.wantQuestion("accepted", rep.Elicitations, `Greet "Ana"?`)
			e.wantRPC("declined", e.prompt("greet", map[string]any{"who": "Ana"}, answers(decline)), -32602, "Declined.")
		}},
}

var confirmSecret = rule("resources/read", "uri.startsWith('ref://secrets/') && !has(input.ok)", "result", "elicit('ok', 'Share {uri}?', {'uri': uri})")

var resourceCases = []tcase{
	{id: "resources/filter", title: "hide a resource; reading it is not found",
		rules: rules(rule("resources/filter", "uri.startsWith('ref://secrets/')", "decision", "'deny'")),
		run: func(e *env) {
			e.eq("resources", names(e.result("resources/list", e.list("resources/list")), "resources", "uri"), []string{"ref://docs/faq", "ref://docs/guide"})
			e.wantRPC("read the hidden resource", e.read("ref://secrets/key"), -32602, "not found: ref://secrets/key")
		}},
	{id: "resources/modify", title: "change a resource's title, description and annotations",
		rules: rules(rule("resources/modify", "uri == 'ref://docs/guide'",
			"title", "'The guide'", "description", "'How to start.'", "annotations.priority", "0.5", "annotations.audience", "['assistant']", "meta.source", "'wiki'")),
		run: func(e *env) {
			r := e.item("resources/list", e.list("resources/list"), "resources", "uri", "ref://docs/guide")
			e.eq("title", r["title"], "The guide")
			e.eq("description", r["description"], "How to start.")
			e.eq("annotations", r["annotations"], map[string]any{"audience": []any{"assistant"}, "priority": 0.5})
			e.eq("_meta.source", get(r, "_meta", "source"), "wiki")
			e.eq("name is unchanged", r["name"], "guide")
			text, _ := readText(e.result("read", e.read("ref://docs/guide")))
			e.eq("content is unchanged", text, guideText)
		}},
	{id: "resources/read deny", title: "deny a read with a reason; the resource stays listed",
		rules: rules(rule("resources/read", "uri == 'ref://docs/faq'", "decision", "'deny'", "reason", "'Use the search tool.'")),
		run: func(e *env) {
			e.contains("resources", names(e.result("resources/list", e.list("resources/list")), "resources", "uri"), "ref://docs/faq")
			e.wantRPC("read", e.read("ref://docs/faq"), -32602, "Use the search tool.")
		}},
	{id: "resources/read confirm", title: "confirm before a read: decline, then accept",
		rules: rules(confirmSecret),
		caps:  withElicitation,
		run: func(e *env) {
			rep := e.read("ref://secrets/key", answers(decline))
			e.wantRPC("declined", rep, -32602, "Declined.")
			e.wantQuestion("declined", rep.Elicitations, `Share "ref://secrets/key"?`)
			rep = e.read("ref://secrets/key", answers(accept(map[string]any{"confirm": true})))
			text, _ := readText(e.result("accepted", rep))
			e.eq("accepted", text, "sk-test-123")
			e.wantQuestion("accepted", rep.Elicitations, `Share "ref://secrets/key"?`)
		}},
	{id: "resources/read confirm again", title: "after an accepted confirmation, the next read asks again",
		rules: rules(confirmSecret),
		caps:  withElicitation,
		run: func(e *env) {
			text, _ := readText(e.result("accepted", e.read("ref://secrets/key", answers(accept(map[string]any{"confirm": true})))))
			e.eq("accepted", text, "sk-test-123")
			rep := e.read("ref://secrets/key", answers(decline))
			e.wantRPC("declined the second time", rep, -32602, "Declined.")
			e.wantQuestion("second read", rep.Elicitations, `Share "ref://secrets/key"?`)
		}},
	{id: "resources/result", title: "rewrite read text; slugUris changes nothing on the direct route",
		rules: rules(rule("resources/result", "true", "text", "slugUris(regex.replace(result.text, 'FAQ', 'Questions'))")),
		run: func(e *env) {
			text, uri := readText(e.result("read faq", e.read("ref://docs/faq")))
			e.eq("faq text", text, "Questions")
			e.eq("faq uri", uri, "ref://docs/faq")
			text, _ = readText(e.result("read guide", e.read("ref://docs/guide")))
			e.eq("guide text", text, guideText)
		}},
	{id: "resources/result deny", title: "withhold read content by pattern, with a reason",
		rules: rules(rule("resources/result", "result.text.startsWith('sk-')", "decision", "'deny'", "reason", "'The file holds a key and was withheld.'")),
		run: func(e *env) {
			rep := e.read("ref://secrets/key")
			e.wantRPC("withheld", rep, -32602, "The file holds a key and was withheld.")
			e.noText("withheld", asJSON(rep), "sk-test-123")
			text, _ := readText(e.result("other reads pass", e.read("ref://docs/faq")))
			e.eq("other reads pass", text, "FAQ")
		}},
	{id: "templates/filter", title: "hide a template; reads through it are not found",
		rules: rules(rule("templates/filter", "uriTemplate.startsWith('ref://notes/')", "decision", "'deny'")),
		run: func(e *env) {
			e.eq("templates", names(e.result("templates", e.list("resources/templates/list")), "resourceTemplates", "uriTemplate"), []string{})
			e.wantRPC("read through it", e.read("ref://notes/7"), -32602, "not found: ref://notes/7")
		}},
	{id: "templates/modify", title: "change a template's title and description",
		rules: rules(rule("templates/modify", "uriTemplate == 'ref://notes/{id}'", "title", "'A note'", "description", "'Replace {id} with the note number.'",
			"annotations", "{'audience': ['assistant'], 'priority': 0.1}", "meta.kind", "'note'")),
		run: func(e *env) {
			tp := e.item("templates", e.list("resources/templates/list"), "resourceTemplates", "uriTemplate", "ref://notes/{id}")
			e.eq("title", tp["title"], "A note")
			e.eq("description", tp["description"], "Replace {id} with the note number.")
			e.eq("annotations", tp["annotations"], map[string]any{"audience": []any{"assistant"}, "priority": 0.1})
			e.eq("_meta.kind", get(tp, "_meta", "kind"), "note")
			text, _ := readText(e.result("read", e.read("ref://notes/7")))
			e.eq("read through it", text, "note 7")
		}},
	{id: "resources unlisted", title: "an unlisted resource is readable unless denied",
		run: func(e *env) {
			e.lacks("resources", names(e.result("resources/list", e.list("resources/list")), "resources", "uri"), "ref://hidden/thing")
			text, _ := readText(e.result("read", e.read("ref://hidden/thing")))
			e.eq("unlisted", text, "hidden thing")
		}},
	{id: "resources close folder", title: "a prefix deny closes a folder: templated and unlisted URIs alike",
		rules: rules(rule("resources/filter", "uri.startsWith('ref://notes/') || uri.startsWith('ref://hidden/')", "decision", "'deny'")),
		run: func(e *env) {
			e.wantRPC("templated", e.read("ref://notes/7"), -32602, "not found: ref://notes/7")
			e.wantRPC("unlisted", e.read("ref://hidden/thing"), -32602, "not found: ref://hidden/thing")
			text, _ := readText(e.result("outside the folder", e.read("ref://docs/faq")))
			e.eq("outside the folder", text, "FAQ")
		}},
}

var serverCases = []tcase{
	{id: "server/config off", title: "prompts, resources and progress switched off",
		rules: rules(rule("server/config", "true", "prompts", "false", "resources", "false", "progress", "false")),
		run: func(e *env) {
			e.contains("tools", names(e.result("tools/list", e.list("tools/list")), "tools", "name"), "echo")
			e.wantRPC("get a prompt", e.prompt("greet", map[string]any{"who": "Ana"}), -32602, "unknown prompt greet")
			e.wantRPC("read", e.read("ref://docs/faq"), -32602, "not found: ref://docs/faq")
			rep := e.tool("slow_count", map[string]any{"steps": 2, "delayMs": 10}, withProgress)
			e.wantText("progress off", rep, "counted 2")
			e.eq("progress events", len(rep.Progress), 0)
		}},
	{id: "server/config lists off", title: "listings of switched-off kinds are empty",
		rules: rules(rule("server/config", "true", "tools", "false", "prompts", "false", "resources", "false")),
		run: func(e *env) {
			e.eq("tools", names(e.result("tools/list", e.list("tools/list")), "tools", "name"), []string{})
			e.eq("prompts", names(e.result("prompts/list", e.list("prompts/list")), "prompts", "name"), []string{})
			e.eq("resources", names(e.result("resources/list", e.list("resources/list")), "resources", "uri"), []string{})
			e.eq("templates", names(e.result("templates", e.list("resources/templates/list")), "resourceTemplates", "uriTemplate"), []string{})
		}},
	{id: "server/config tools off", title: "tools switched off: a call is an unknown tool",
		rules: rules(rule("server/config", "true", "tools", "false")),
		run: func(e *env) {
			e.wantRPC("call", e.tool("echo", map[string]any{"text": "hi"}), -32602, "unknown tool echo")
		}},
	{id: "server/config discover", title: "serverInfo on and instructions off, in discover",
		rules:   rules(rule("server/config", "true", "serverInfo", "true", "instructions", "false")),
		rawOnly: "reads the server/discover result, which the SDK drivers do not expose",
		run: func(e *env) {
			res := e.discover()
			e.eq("serverInfo name", get(res, "_meta", "io.modelcontextprotocol/serverInfo", "name"), "refserver-"+e.server)
			e.eq("serverInfo version", get(res, "_meta", "io.modelcontextprotocol/serverInfo", "version"), "1.0.0")
			if v, ok := res["instructions"]; ok && v != "" {
				e.t.Errorf("instructions are still present: %q", v)
			}
		}},
	{id: "server/connect default", title: "the server sees the gateway's identity and no capabilities",
		run: func(e *env) {
			sc := e.inspected("inspect", e.tool("inspect", nil))
			e.eq("protocol version", sc["protocolVersion"], "2026-07-28")
			e.eq("client name", get(sc, "clientInfo", "name"), "bifrost")
			e.lacks("capabilities", keysOf(sc["capabilities"]), "elicitation")
			e.eq("Mcp-Name header", get(sc, "headers", "mcp-name"), "inspect")
		}},
	{id: "server/connect identity", title: "pass the client's identity through and advertise elicitation",
		rules: rules(connectIdentity), caps: withElicitation,
		run: func(e *env) {
			sc := e.inspected("inspect", e.tool("inspect", nil))
			e.eq("client name", get(sc, "clientInfo", "name"), clientNames[e.client])
			e.contains("capabilities", keysOf(sc["capabilities"]), "elicitation")
		}},
	{id: "server/connect no elicit", title: "elicitation is never advertised upstream for a client that cannot answer",
		rules: rules(connectIdentity),
		run: func(e *env) {
			sc := e.inspected("inspect", e.tool("inspect", nil))
			e.eq("client name", get(sc, "clientInfo", "name"), clientNames[e.client])
			e.lacks("capabilities", keysOf(sc["capabilities"]), "elicitation")
		}},
	{id: "server/connect meta", title: "meta on forwards the client's _meta, never its identity keys",
		rules:   rules(rule("server/connect", "true", "meta", "true")),
		rawOnly: "sends extra _meta keys, which the SDK drivers cannot add",
		run: func(e *env) {
			ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
			defer cancel()
			rep, err := e.raw.Send(ctx, "tools/call", map[string]any{"name": "inspect", "arguments": map[string]any{}}, map[string]any{"trace": "t-1"})
			if err != nil {
				e.t.Fatal(err)
			}
			sc, _ := normalize(rep.Result())["structuredContent"].(map[string]any)
			e.eq("forwarded trace", get(sc, "meta", "trace"), "t-1")
			e.eq("identity in _meta", get(sc, "meta", "io.modelcontextprotocol/clientInfo", "name"), "bifrost")
		}},
	{id: "server/modify discover", title: "title and instructions in discover",
		rules:   rules(rule("server/modify", "true", "title", "'Reference (gated)'", "instructions", "instructions + '\\nAll writes need a ticket.'")),
		rawOnly: "reads the server/discover result, which the SDK drivers do not expose",
		run: func(e *env) {
			res := e.discover()
			e.eq("title", get(res, "_meta", "io.modelcontextprotocol/serverInfo", "title"), "Reference (gated)")
			e.eq("instructions", res["instructions"], instructions+"\nAll writes need a ticket.")
		}},
}

var connectIdentity = rule("server/connect", "true", "clientInfo", "{'name': client.name, 'version': client.version}", "capabilities.elicitation", "true")

// discover is the endpoint's server/discover result, through the raw client.
func (e *env) discover() map[string]any {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	rep, err := e.raw.Discover(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	if rep.Result() == nil {
		e.t.Fatalf("server/discover: no result: %s", asJSON(rep.Messages()))
	}
	return normalize(rep.Result())
}

var routeCases = []tcase{
	{id: "mux names", title: "name prefixes on the mux, with a rename and a link in a result",
		route: "mux",
		rules: rules(rule("tools/modify", "upstream.name == 'echo'", "name", "'say'")),
		run: func(e *env) {
			got := names(e.result("tools/list", e.list("tools/list")), "tools", "name")
			e.contains("tools", got, "alpha__say", "beta__echo", "get_server_instructions")
			e.lacks("tools", got, "alpha__echo", "echo")
			e.wantText("beta", e.tool("beta__echo", map[string]any{"text": "hi"}), "hi")
			e.wantText("alpha renamed", e.tool("alpha__say", map[string]any{"text": "yo"}), "yo")
			e.wantRPC("no prefix", e.tool("echo", map[string]any{"text": "hi"}), -32602)
			e.contains("prompts", names(e.result("prompts/list", e.list("prompts/list")), "prompts", "name"), "alpha__greet", "beta__greet")
			e.eq("prompt", promptText(e.result("prompt", e.prompt("beta__greet", map[string]any{"who": "Ana"}))), "Hello, Ana!")
			res := e.okTool("picture", e.tool("alpha__picture", nil))
			blocks, _ := res["content"].([]any)
			if len(blocks) == 3 {
				e.eq("link uri", get(blocks[2], "uri"), "alpha+ref://docs/guide")
			} else {
				e.t.Errorf("picture blocks: %s", asJSON(res))
			}
		}},
	{id: "mux uris", title: "URI prefixes on the mux; slugUris rewrites this server's URIs in text",
		route:  "mux",
		rulesB: rules(rule("resources/result", "true", "text", "slugUris(result.text)")),
		run: func(e *env) {
			got := names(e.result("resources/list", e.list("resources/list")), "resources", "uri")
			e.contains("resources", got, "alpha+ref://docs/guide", "beta+ref://docs/guide", "alpha+ref://secrets/key")
			e.contains("templates", names(e.result("templates", e.list("resources/templates/list")), "resourceTemplates", "uriTemplate"), "alpha+ref://notes/{id}", "beta+ref://notes/{id}")
			text, uri := readText(e.result("read a template URI", e.read("beta+ref://notes/7")))
			e.eq("template read", []string{text, uri}, []string{"note 7", "beta+ref://notes/7"})
			text, _ = readText(e.result("read alpha guide", e.read("alpha+ref://docs/guide")))
			e.eq("alpha guide (no rule)", text, guideText)
			text, uri = readText(e.result("read beta guide", e.read("beta+ref://docs/guide")))
			e.eq("beta guide (slugUris)", text, "# Guide\nSee beta+ref://docs/faq and https://example.com/x.")
			e.eq("beta guide uri", uri, "beta+ref://docs/guide")
		}},
	{id: "mux bare uri", title: "a bare URI resolves to the one server that can read it",
		route:  "mux",
		rulesB: rules(rule("resources/filter", "uri.startsWith('ref://secrets/')", "decision", "'deny'")),
		run: func(e *env) {
			text, uri := readText(e.result("bare read", e.read("ref://secrets/key")))
			e.eq("bare read", []string{text, uri}, []string{"sk-test-123", "alpha+ref://secrets/key"})
			e.wantRPC("prefixed read of the denied copy", e.read("beta+ref://secrets/key"), -32602, "not found")
		}},
	{id: "mux ambiguous uri", title: "a bare URI two servers claim is refused with the candidates",
		route: "mux",
		run: func(e *env) {
			rep := e.read("ref://docs/faq")
			e.wantRPC("ambiguous", rep, -32602, "Ambiguous URI ref://docs/faq", "alpha+ref://docs/faq", "beta+ref://docs/faq")
			e.wantRPC("unclaimed", e.read("ref://nowhere/x"), -32602, "not found: ref://nowhere/x")
		}},
	{id: "mux instructions", title: "server/modify instructions through get_server_instructions",
		route:  "mux",
		rules:  rules(rule("server/modify", "true", "instructions", "instructions + '\\nAll writes need a ticket.'")),
		rulesB: rules(rule("server/config", "true", "instructions", "false")),
		run: func(e *env) {
			res := e.okTool("alpha", e.tool("get_server_instructions", map[string]any{"slug": "alpha"}))
			if !strings.Contains(textOf(res), "Use search first.\nAll writes need a ticket.") {
				e.t.Errorf("alpha instructions: %q", textOf(res))
			}
			rep := e.tool("get_server_instructions", map[string]any{"slug": "beta"})
			e.wantRefused("beta, instructions off", rep)
			e.noText("beta, instructions off", asJSON(rep), "Use search first.")
		}},
	{id: "search mode", title: "search mode exposes only what the flat list would, and executes",
		route: "search",
		rules: rules(
			rule("tools/filter", "name == 'delete_page'", "decision", "'deny'"),
			rule("server/modify", "true", "description", "'zebra quartz archive'")),
		run: func(e *env) {
			e.eq("tools", names(e.result("tools/list", e.list("tools/list")), "tools", "name"), []string{"execute_tool", "get_tool_definition", "list_tools", "tool_search"})
			found := e.okTool("tool_search", e.tool("tool_search", map[string]any{"query": "echo"}))
			if !strings.Contains(textOf(found), "alpha__echo") || !strings.Contains(textOf(found), "beta__echo") {
				e.t.Errorf("tool_search echo: %s", textOf(found))
			}
			roster := e.okTool("tool_search roster", e.tool("tool_search", map[string]any{"query": "zebra quartz"}))
			hits := names(normalize(roster["structuredContent"]), "tools", "name")
			if len(hits) == 0 {
				e.t.Errorf("tool_search found nothing by the modified server description: %s", textOf(roster))
			}
			for _, h := range hits {
				if !strings.HasPrefix(h, "alpha__") {
					e.t.Errorf("tool_search by alpha's description found %s", h)
				}
			}
			def := e.okTool("get_tool_definition", e.tool("get_tool_definition", map[string]any{"names": []any{"alpha__delete_page", "beta__delete_page"}}))
			e.eq("unknown definitions", get(def, "structuredContent", "unknown"), []any{"alpha__delete_page"})
			e.wantText("execute", e.tool("execute_tool", map[string]any{"name": "beta__echo", "arguments": map[string]any{"text": "hi"}}), "hi")
			e.wantRPC("execute a filtered tool", e.tool("execute_tool", map[string]any{"name": "alpha__delete_page", "arguments": map[string]any{"id": "1"}}), -32602, "unknown tool")
		}},
}

var wireCases = []tcase{
	{id: "wire progress order", title: "progress frames precede the result on the request's own SSE stream, under the client's token",
		rawOnly: "reads the SSE frames and progress tokens, which the SDKs hide",
		run: func(e *env) {
			ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
			defer cancel()
			rep, err := e.raw.Send(ctx, "tools/call", map[string]any{"name": "slow_count", "arguments": map[string]any{"steps": 3, "delayMs": 10}}, map[string]any{"progressToken": "wire-1"})
			if err != nil {
				e.t.Fatal(err)
			}
			e.eq("status", rep.Status, 200)
			if !rep.Stream() {
				e.t.Fatalf("the reply is not an SSE stream: %s", rep.Header.Get("Content-Type"))
			}
			msgs := rep.Messages()
			if len(msgs) != 4 {
				e.t.Fatalf("want 3 progress frames and a result, got %s", asJSON(msgs))
			}
			for i, m := range msgs[:3] {
				e.eq("frame method", m["method"], "notifications/progress")
				e.eq("frame token", get(m, "params", "progressToken"), "wire-1")
				e.eq("frame progress", asJSON(get(m, "params", "progress")), asJSON(i+1))
			}
			e.eq("last frame", textOf(normalize(msgs[3]["result"])), "counted 3")
		}},
}

var rosterCase = tcase{id: "search roster off", title: "with searchRoster off, the server description does not rank tools",
	route: "search",
	rules: rules(
		rule("server/config", "true", "searchRoster", "false"),
		rule("server/modify", "true", "description", "'zebra quartz archive'")),
	run: func(e *env) {
		roster := e.okTool("tool_search", e.tool("tool_search", map[string]any{"query": "zebra quartz"}))
		e.eq("found", names(normalize(roster["structuredContent"]), "tools", "name"), []string{})
	}}

var errorCases = []tcase{
	{id: "errors fail", title: "a tool error reaches the client as an error result",
		run: func(e *env) {
			e.wantToolError("fail", e.tool("fail", nil), "boom")
		}},
	{id: "errors crash", title: "a protocol error reaches the client as a JSON-RPC error",
		run: func(e *env) {
			e.wantRPC("crash", e.tool("crash", nil), -32603)
		}},
	{id: "audit", title: "audit-mode rules change nothing and leave hits",
		rules: rules(
			audit(rule("tools/filter", "name == 'echo'", "decision", "'deny'")),
			audit(rule("tools/call", "upstream.name == 'echo'", "decision", "'deny'", "reason", "'would refuse'")),
			audit(rule("tools/result", "upstream.name == 'echo'", "content", "[]"))),
		run: func(e *env) {
			e.contains("tools", names(e.result("tools/list", e.list("tools/list")), "tools", "name"), "echo")
			e.wantHit("tools/list", "tools/filter", "would deny")
			res := e.wantText("call", e.tool("echo", map[string]any{"text": "hi"}), "hi")
			e.eq("structured content", res["structuredContent"], map[string]any{"text": "hi"})
			e.wantHit("tools/call", "tools/call", "would deny")
			e.wantHit("tools/call", "tools/result", "would set")
		}},
}

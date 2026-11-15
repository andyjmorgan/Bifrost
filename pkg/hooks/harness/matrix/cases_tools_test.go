package matrix

import (
	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness/clients"
)

// The cases, in table order.
var cases = concat(toolCases, confirmCases, inputCases, resultCases, progressCases, promptCases, resourceCases, serverCases, routeCases, []tcase{rosterCase}, wireCases, errorCases)

func concat(groups ...[]tcase) []tcase {
	var out []tcase
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// tel is the search tool's required telemetry argument, for cases that do
// not pin it.
var tel = map[string]any{"intent": "matrix"}

func searchArgs(kv ...any) map[string]any {
	m := map[string]any{"query": "q", "telemetry": tel}
	for i := 0; i+1 < len(kv); i += 2 {
		k := kv[i].(string)
		if kv[i+1] == nil && k != "limit" {
			delete(m, k)
			continue
		}
		m[k] = kv[i+1]
	}
	return m
}

const pixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

var withElicitation = map[string]any{"elicitation": map[string]any{}}

func accept(content map[string]any) clients.Answer {
	return clients.Answer{Action: "accept", Content: content}
}

var (
	decline = clients.Answer{Action: "decline"}
	cancel  = clients.Answer{Action: "cancel"}
)

var toolCases = []tcase{
	// ---- tools/filter ----
	{id: "tools/filter name", title: "hide a tool by name; a hidden tool cannot be called",
		rules: rules(rule("tools/filter", "name == 'delete_page'", "decision", "'deny'", "reason", "'deleting is restricted'")),
		run: func(e *env) {
			got := names(e.result("tools/list", e.list("tools/list")), "tools", "name")
			e.lacks("tools", got, "delete_page")
			e.contains("tools", got, "echo", "search", "inspect")
			e.wantRPC("call the hidden tool", e.tool("delete_page", map[string]any{"id": "42"}), -32602, "unknown tool delete_page")
			e.wantText("call a visible tool", e.tool("echo", map[string]any{"text": "hi"}), "hi")
		}},
	{id: "tools/filter annotation", title: "hide tools that are not read-only",
		rules: rules(rule("tools/filter", "!annotations.readOnlyHint", "decision", "'deny'")),
		run: func(e *env) {
			got := names(e.result("tools/list", e.list("tools/list")), "tools", "name")
			e.eq("tools", got, []string{"add", "echo", "get_report", "inspect", "picture", "search", "slow_count"})
			e.wantRPC("call a hidden tool", e.tool("fail", nil), -32602, "unknown tool fail")
		}},
	{id: "tools/filter description", title: "hide tools by description",
		rules: rules(rule("tools/filter", "description.startsWith('Always')", "decision", "'deny'")),
		run: func(e *env) {
			got := names(e.result("tools/list", e.list("tools/list")), "tools", "name")
			e.lacks("tools", got, "fail", "crash")
			e.contains("tools", got, "echo")
			e.wantRPC("call a hidden tool", e.tool("crash", nil), -32602, "unknown tool crash")
		}},

	// ---- tools/modify ----
	{id: "tools/modify rename", title: "rename a tool and call it by the new name",
		rules: rules(rule("tools/modify", "upstream.name == 'echo'", "name", "'say'")),
		run: func(e *env) {
			got := names(e.result("tools/list", e.list("tools/list")), "tools", "name")
			e.contains("tools", got, "say")
			e.lacks("tools", got, "echo")
			res := e.wantText("call the new name", e.tool("say", map[string]any{"text": "hi"}), "hi")
			e.eq("structured content", res["structuredContent"], map[string]any{"text": "hi"})
			e.wantRPC("call the old name", e.tool("echo", map[string]any{"text": "hi"}), -32602, "unknown tool echo")
		}},
	{id: "tools/modify text", title: "change title, description and _meta",
		rules: rules(rule("tools/modify", "upstream.name == 'echo'",
			"title", "'Echo'", "description", "description + ' Via the gateway.'", "meta.owner", "'platform'")),
		run: func(e *env) {
			tool := e.item("tools/list", e.list("tools/list"), "tools", "name", "echo")
			e.eq("title", tool["title"], "Echo")
			e.eq("description", tool["description"], "Return the text. Via the gateway.")
			e.eq("_meta.owner", get(tool, "_meta", "owner"), "platform")
		}},
	{id: "tools/modify annotations", title: "set annotations",
		rules: rules(rule("tools/modify", "upstream.name == 'delete_page'",
			"annotations.readOnlyHint", "true", "annotations.destructiveHint", "false", "annotations.title", "'Delete (safe)'")),
		run: func(e *env) {
			tool := e.item("tools/list", e.list("tools/list"), "tools", "name", "delete_page")
			e.eq("readOnlyHint", get(tool, "annotations", "readOnlyHint"), true)
			e.eq("destructiveHint", get(tool, "annotations", "destructiveHint"), false)
			e.eq("annotations.title", get(tool, "annotations", "title"), "Delete (safe)")
		}},
	{id: "tools/modify hide param", title: "hide an optional parameter; a value for it is refused",
		rules: rules(rule("tools/modify", "upstream.name == 'search'", "params.space.hide", "true")),
		run: func(e *env) {
			tool := e.item("tools/list", e.list("tools/list"), "tools", "name", "search")
			e.lacks("properties", keysOf(get(tool, "inputSchema", "properties")), "space")
			e.wantRPC("send the hidden parameter", e.tool("search", searchArgs("space", "ENG")), -32602, `parameter "space" is not available on this tool`)
			e.eq("upstream args", e.echoed("call without it", e.tool("search", searchArgs())), searchArgs())
		}},
	{id: "tools/modify pin hidden", title: "hide and pin the required telemetry object: injected upstream",
		rules: rules(rule("tools/modify", "'telemetry' in params", "params.telemetry.hide", "true", "params.telemetry.pin", "{'intent': 'mind your own business'}")),
		run: func(e *env) {
			tool := e.item("tools/list", e.list("tools/list"), "tools", "name", "search")
			e.lacks("properties", keysOf(get(tool, "inputSchema", "properties")), "telemetry")
			e.eq("required", strs(get(tool, "inputSchema", "required")), []string{"query"})
			e.eq("upstream args", e.echoed("call", e.tool("search", map[string]any{"query": "q"})),
				map[string]any{"query": "q", "telemetry": map[string]any{"intent": "mind your own business"}})
			e.wantRPC("send telemetry", e.tool("search", searchArgs()), -32602, `parameter "telemetry" is not available on this tool`)
		}},
	{id: "tools/modify pin visible", title: "pin a visible parameter: const in the schema, other values refused",
		rules: rules(rule("tools/modify", "upstream.name == 'search'", "params.space.pin", "'ENG'")),
		run: func(e *env) {
			tool := e.item("tools/list", e.list("tools/list"), "tools", "name", "search")
			e.eq("space schema const", get(tool, "inputSchema", "properties", "space", "const"), "ENG")
			e.eq("upstream args without space", e.echoed("call", e.tool("search", searchArgs())), searchArgs("space", "ENG"))
			e.eq("upstream args with the pinned value", e.echoed("call", e.tool("search", searchArgs("space", "ENG"))), searchArgs("space", "ENG"))
			e.wantRPC("another value", e.tool("search", searchArgs("space", "HR")), -32602, `parameter "space" must be "ENG"`)
		}},
	{id: "tools/modify default", title: "a default fills a missing argument",
		rules: rules(rule("tools/modify", "upstream.name == 'search'", "params.limit.default", "10")),
		run: func(e *env) {
			tool := e.item("tools/list", e.list("tools/list"), "tools", "name", "search")
			e.eq("limit default", get(tool, "inputSchema", "properties", "limit", "default"), 10)
			e.eq("upstream args without limit", e.echoed("call", e.tool("search", searchArgs())), searchArgs("limit", 10))
			e.eq("upstream args with limit", e.echoed("call", e.tool("search", searchArgs("limit", 3))), searchArgs("limit", 3))
		}},
	{id: "tools/modify enum", title: "narrow a parameter to an enum",
		rules: rules(rule("tools/modify", "upstream.name == 'search'", "params.space.enum", "['ENG', 'OPS']")),
		run: func(e *env) {
			tool := e.item("tools/list", e.list("tools/list"), "tools", "name", "search")
			e.eq("space enum", get(tool, "inputSchema", "properties", "space", "enum"), []any{"ENG", "OPS"})
			e.eq("allowed value", e.echoed("call", e.tool("search", searchArgs("space", "OPS"))), searchArgs("space", "OPS"))
			e.wantRPC("value outside the enum", e.tool("search", searchArgs("space", "HR")), -32602, `parameter "space" must be one of`)
		}},
	{id: "tools/modify min max", title: "minimum and maximum",
		rules: rules(rule("tools/modify", "upstream.name == 'search'", "params.limit.minimum", "1", "params.limit.maximum", "50")),
		run: func(e *env) {
			tool := e.item("tools/list", e.list("tools/list"), "tools", "name", "search")
			e.eq("limit minimum", get(tool, "inputSchema", "properties", "limit", "minimum"), 1)
			e.eq("limit maximum", get(tool, "inputSchema", "properties", "limit", "maximum"), 50)
			e.eq("in range", e.echoed("call", e.tool("search", searchArgs("limit", 20))), searchArgs("limit", 20))
			e.wantRPC("above the maximum", e.tool("search", searchArgs("limit", 100)), -32602, `parameter "limit" must be at most 50`)
			e.wantRPC("below the minimum", e.tool("search", searchArgs("limit", 0)), -32602, `parameter "limit" must be at least 1`)
		}},
	{id: "tools/modify rename param", title: "rename and describe a parameter; the upstream name is refused",
		rules: rules(rule("tools/modify", "upstream.name == 'search'", "params.query.name", "'q'", "params.query.description", "'What to look for.'")),
		run: func(e *env) {
			tool := e.item("tools/list", e.list("tools/list"), "tools", "name", "search")
			props := keysOf(get(tool, "inputSchema", "properties"))
			e.contains("properties", props, "q")
			e.lacks("properties", props, "query")
			e.eq("q description", get(tool, "inputSchema", "properties", "q", "description"), "What to look for.")
			e.contains("required", strs(get(tool, "inputSchema", "required")), "q")
			e.eq("upstream args", e.echoed("call", e.tool("search", map[string]any{"q": "x", "telemetry": tel})), map[string]any{"query": "x", "telemetry": tel})
			e.wantRPC("the upstream name", e.tool("search", searchArgs()), -32602, `parameter "query" is not available on this tool`)
		}},
	{id: "tools/modify add param", title: "add a parameter: listed, readable by call rules, never forwarded",
		rules: rules(
			rule("tools/modify", "upstream.name == 'search'", "params.dryRun.add", "{'type': 'boolean', 'description': 'Only show what would run.'}"),
			rule("tools/call", "upstream.name == 'search' && has(args.dryRun) && args.dryRun", "decision", "'deny'", "reason", "'dry run: nothing was searched'")),
		run: func(e *env) {
			tool := e.item("tools/list", e.list("tools/list"), "tools", "name", "search")
			e.eq("dryRun schema", get(tool, "inputSchema", "properties", "dryRun", "type"), "boolean")
			e.wantToolError("dryRun true", e.tool("search", searchArgs("dryRun", true)), "dry run: nothing was searched")
			e.eq("upstream args with dryRun false", e.echoed("call", e.tool("search", searchArgs("dryRun", false))), searchArgs())
		}},
	{id: "tools/modify output schema", title: "set and trim an output schema, with the matching result drop",
		rules: rules(
			rule("tools/modify", "upstream.name == 'get_report'", "outputSchema",
				"{'type': 'object', 'properties': {'user': {'type': 'object', 'properties': {'email': {'type': 'string'}, 'name': {'type': 'string'}}, 'required': ['email', 'name']}, 'rows': {'type': 'array'}, 'note': {}}, 'required': ['user', 'rows']}"),
			rule("tools/modify", "upstream.name == 'get_report'", "outputSchema", "dropSchema(outputSchema, ['rows', 'user.email'])"),
			rule("tools/result", "upstream.name == 'get_report'", "structuredContent", "drop(result.structuredContent, ['rows', 'user.email'])", "content", "[]")),
		run: func(e *env) {
			tool := e.item("tools/list", e.list("tools/list"), "tools", "name", "get_report")
			schema := get(tool, "outputSchema")
			e.eq("output schema properties", keysOf(get(schema, "properties")), []string{"note", "user"})
			e.eq("output schema user properties", keysOf(get(schema, "properties", "user", "properties")), []string{"name"})
			e.eq("output schema required", strs(get(schema, "required")), []string{"user"})
			e.eq("user required", strs(get(schema, "properties", "user", "required")), []string{"name"})
			res := e.okTool("call", e.tool("get_report", nil))
			e.eq("structured user", get(res, "structuredContent", "user"), map[string]any{"name": "Ana"})
			e.eq("structured keys", keysOf(res["structuredContent"]), []string{"note", "user"})
			e.noText("result", asJSON(res), "ana@example.com", "secret")
		}},

	// ---- tools/call ----
	{id: "tools/call deny", title: "deny on an argument value, with a reason",
		rules: rules(rule("tools/call", "upstream.name == 'search' && has(args.space) && args.space == 'HR'", "decision", "'deny'", "reason", "'The HR space is restricted.'")),
		run: func(e *env) {
			e.wantToolError("space HR", e.tool("search", searchArgs("space", "HR")), "The HR space is restricted.")
			e.wantHit("tools/call", "tools/call", "deny")
			e.eq("space ENG goes through", e.echoed("call", e.tool("search", searchArgs("space", "ENG"))), searchArgs("space", "ENG"))
		}},
	{id: "tools/call clamp", title: "clamp an argument and set a timeout",
		rules: rules(
			rule("tools/call", "has(args.limit) && args.limit > 100", "args.limit", "100"),
			rule("tools/call", "upstream.name == 'search'", "timeout", "duration('10s')")),
		run: func(e *env) {
			e.eq("over the limit", e.echoed("call", e.tool("search", searchArgs("limit", 5000))), searchArgs("limit", 100))
			e.wantHit("tools/call", "tools/call", "set")
			e.eq("under the limit", e.echoed("call", e.tool("search", searchArgs("limit", 7))), searchArgs("limit", 7))
		}},
	{id: "tools/call timeout", title: "a call-rule timeout ends a slow upstream call with an error",
		rules: rules(rule("tools/call", "upstream.name == 'slow_count'", "timeout", "duration('300ms')")),
		run: func(e *env) {
			// With a progress token, so every server paces its steps.
			rep := e.tool("slow_count", map[string]any{"steps": 20, "delayMs": 100}, withProgress)
			// The engine reports a reply cut off by the timeout as lost
			// (CodeUpstreamLost): the call may or may not have run.
			e.wantRPC("slow call", rep, -32014, "reply was lost")
			e.wantText("fast call", e.tool("slow_count", map[string]any{"steps": 1, "delayMs": 1}), "counted 1")
		}},
	{id: "tools/call from caller", title: "hide the required telemetry and set it from the caller; refused when no rule sets it",
		rules: rules(
			rule("tools/modify", "'telemetry' in params", "params.telemetry.hide", "true"),
			rule("tools/call", "upstream.name == 'search' && args.query != 'anonymous'", "args.telemetry", "{'intent': 'caller ' + caller.userId}")),
		run: func(e *env) {
			tool := e.item("tools/list", e.list("tools/list"), "tools", "name", "search")
			e.lacks("properties", keysOf(get(tool, "inputSchema", "properties")), "telemetry")
			e.eq("upstream args", e.echoed("call", e.tool("search", map[string]any{"query": "q"})),
				map[string]any{"query": "q", "telemetry": map[string]any{"intent": "caller u1"}})
			e.wantRPC("send telemetry", e.tool("search", searchArgs()), -32602, `parameter "telemetry" is not available on this tool`)
			e.wantToolError("no rule supplies it", e.tool("search", map[string]any{"query": "anonymous"}),
				"The call to search was refused: required parameters were not supplied by policy.")
		}},
	{id: "tools/call coerce clamp", title: "convert a string sent for a number, then clamp",
		rules: rules(
			rule("tools/call", "has(args.limit) && type(args.limit) == string", "args.limit", "int(args.limit)"),
			rule("tools/call", "has(args.limit) && args.limit > 100", "args.limit", "100")),
		run: func(e *env) {
			e.eq(`"50"`, e.echoed("call", e.tool("search", searchArgs("limit", "50"))), searchArgs("limit", 50))
			e.eq(`"250"`, e.echoed("call", e.tool("search", searchArgs("limit", "250"))), searchArgs("limit", 100))
			e.eq(`250`, e.echoed("call", e.tool("search", searchArgs("limit", 250))), searchArgs("limit", 100))
			e.wantToolError(`"abc"`, e.tool("search", searchArgs("limit", "abc")), "The call to search was refused by policy.")
		}},
	{id: "tools/call wrong type", title: "a rule written for a number refuses a string or null",
		rules: rules(rule("tools/call", "has(args.limit) && args.limit > 100", "args.limit", "100")),
		run: func(e *env) {
			e.wantToolError("a string", e.tool("search", searchArgs("limit", "50")), "The call to search was refused by policy.")
			e.wantHit("tools/call", "tools/call", "error")
			e.wantToolError("null", e.tool("search", searchArgs("limit", nil)), "The call to search was refused by policy.")
			e.eq("a number", e.echoed("call", e.tool("search", searchArgs("limit", 50))), searchArgs("limit", 50))
		}},
}

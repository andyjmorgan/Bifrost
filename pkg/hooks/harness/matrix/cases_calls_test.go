package matrix

import (
	"context"

	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness"
)

// The confirmation rule of the elicit() cases.
var confirmDelete = rule("tools/call", "upstream.name == 'delete_page' && !has(input.confirm)",
	"result", "elicit('confirm', 'Delete page {id}?', {'id': args.id})")

const deleteQuestion = `Delete page "42"?`

// wantQuestion requires that the client's elicitation handler was asked
// exactly one question, with this message.
func (e *env) wantQuestion(what string, asked []map[string]any, want string) {
	e.t.Helper()
	if len(asked) != 1 {
		e.t.Errorf("%s: the client was asked %d questions, want 1: %s", what, len(asked), asJSON(asked))
		return
	}
	e.eq(what+": question", asked[0]["message"], want)
}

var confirmCases = []tcase{
	{id: "elicit accept", title: "confirmation accepted with the box ticked: the call goes ahead",
		rules: rules(confirmDelete), caps: withElicitation,
		run: func(e *env) {
			rep := e.tool("delete_page", map[string]any{"id": "42"}, answers(accept(map[string]any{"confirm": true})))
			e.wantText("accepted", rep, "deleted 42")
			e.wantQuestion("accepted", rep.Elicitations, deleteQuestion)
		}},
	{id: "elicit decline", title: "confirmation declined: Declined., the upstream is not called",
		rules: rules(confirmDelete), caps: withElicitation,
		run: func(e *env) {
			rep := e.tool("delete_page", map[string]any{"id": "42"}, answers(decline))
			e.wantToolError("declined", rep, "Declined.")
			e.wantQuestion("declined", rep.Elicitations, deleteQuestion)
		}},
	{id: "elicit cancel", title: "confirmation cancelled: Declined.",
		rules: rules(confirmDelete), caps: withElicitation,
		run: func(e *env) {
			rep := e.tool("delete_page", map[string]any{"id": "42"}, answers(cancel))
			e.wantToolError("cancelled", rep, "Declined.")
		}},
	{id: "elicit unticked", title: "accepted with the box unticked: Declined.",
		rules: rules(confirmDelete), caps: withElicitation,
		run: func(e *env) {
			rep := e.tool("delete_page", map[string]any{"id": "42"}, answers(accept(map[string]any{"confirm": false})))
			e.wantToolError("unticked", rep, "Declined.")
		}},
	{id: "elicit no handler", title: "a client without elicitation gets an error result, no question",
		rules: rules(confirmDelete),
		run: func(e *env) {
			rep := e.tool("delete_page", map[string]any{"id": "42"})
			e.wantToolError("no handler", rep, "This request needs confirmation ("+deleteQuestion+"), which this client does not support.")
			if len(rep.Elicitations) != 0 {
				e.t.Errorf("the client was asked: %s", asJSON(rep.Elicitations))
			}
		}},
	{id: "elicit single use", title: "a challenge is answered once; changed arguments are refused",
		rules: rules(confirmDelete), caps: withElicitation,
		rawOnly: "replays a request state by hand; SDKs never expose it",
		run: func(e *env) {
			ctx, cancelCtx := context.WithTimeout(context.Background(), opTimeout)
			defer cancelCtx()
			first := e.rawSend(ctx, "tools/call", map[string]any{"name": "delete_page", "arguments": map[string]any{"id": "42"}})
			res := first.Result()
			e.eq("first reply", res["resultType"], "input_required")
			state := res["requestState"]
			yes := map[string]any{"bifrost.confirm": map[string]any{"action": "accept", "content": map[string]any{"confirm": true}}}
			retry := map[string]any{"name": "delete_page", "arguments": map[string]any{"id": "42"}, "inputResponses": yes, "requestState": state}
			ok := e.rawSend(ctx, "tools/call", retry).Result()
			e.eq("accepted retry", textOf(normalize(ok)), "deleted 42")
			replay := e.rawSend(ctx, "tools/call", retry)
			e.eq("replay error code", fmtCode(replay.Error()), "-32602")
			e.eq("replay error", replay.Error()["message"], "invalid request state")

			// A fresh challenge, answered for other arguments.
			state2 := e.rawSend(ctx, "tools/call", map[string]any{"name": "delete_page", "arguments": map[string]any{"id": "42"}}).Result()["requestState"]
			changed := e.rawSend(ctx, "tools/call", map[string]any{"name": "delete_page", "arguments": map[string]any{"id": "43"}, "inputResponses": yes, "requestState": state2})
			e.eq("changed arguments error", changed.Error()["message"], "arguments changed since the confirmation was requested")
		}},
}

func fmtCode(e map[string]any) string { return asJSON(e["code"]) }

// rawSend makes one request with the raw client, no input rounds.
func (e *env) rawSend(ctx context.Context, method string, params map[string]any) *harness.RawReply {
	e.t.Helper()
	rep, err := e.raw.Send(ctx, method, params, nil)
	if err != nil {
		e.t.Fatalf("raw %s: %v", method, err)
	}
	return rep
}

// The tools/input cases need the gateway to advertise elicitation upstream,
// or a compliant server would never ask.
var connectElicit = rule("server/connect", "true", "capabilities.elicitation", "true")

var inputCases = []tcase{
	{id: "tools/input reword", title: "reword the server's own question; the answer reaches the server",
		rules: rules(connectElicit, rule("tools/input", "request.message == 'Which project?'", "message", "'Gateway asks: ' + request.message")),
		caps:  withElicitation,
		run: func(e *env) {
			rep := e.tool("ask", nil, answers(accept(map[string]any{"project": "apollo"})))
			e.wantText("answered", rep, "project=apollo")
			e.wantQuestion("answered", rep.Elicitations, "Gateway asks: Which project?")
			e.wantAnyHit("tools/call", "tools/input", "set")
		}},
	{id: "tools/input deny", title: "deny the server's question with a reason",
		rules: rules(connectElicit, rule("tools/input", "true", "decision", "'deny'", "reason", "'This tool needs input, which policy does not allow here.'")),
		caps:  withElicitation,
		run: func(e *env) {
			rep := e.tool("ask", nil, answers(accept(map[string]any{"project": "apollo"})))
			e.wantToolError("denied", rep, "This tool needs input, which policy does not allow here.")
			if len(rep.Elicitations) != 0 {
				e.t.Errorf("the client was asked: %s", asJSON(rep.Elicitations))
			}
		}},
	{id: "tools/input decline", title: "a decline and a cancel reach the server",
		rules: rules(connectElicit), caps: withElicitation,
		run: func(e *env) {
			e.wantText("declined", e.tool("ask", nil, answers(decline)), "declined")
			e.wantText("cancelled", e.tool("ask", nil, answers(cancel)), "cancelled")
		}},
}

const reportText = "Report for ana@example.com. Card 4111 1111 1111 1111. Host db-internal-7."

var resultCases = []tcase{
	{id: "tools/result drop", title: "drop fields from structured content",
		rules: rules(rule("tools/result", "upstream.name == 'get_report'", "structuredContent", "drop(result.structuredContent, ['user.email', 'rows.*.secret'])")),
		run: func(e *env) {
			res := e.okTool("call", e.tool("get_report", nil))
			e.eq("user", get(res, "structuredContent", "user"), map[string]any{"name": "Ana"})
			e.eq("rows", get(res, "structuredContent", "rows"), []any{map[string]any{"id": 1}, map[string]any{"id": 2}})
			e.eq("text is untouched", textOf(res), reportText)
			e.wantHit("tools/call", "tools/result", "set")
		}},
	{id: "tools/result regex", title: "rewrite text with a regex",
		rules: rules(rule("tools/result", "upstream.name == 'get_report'", "content",
			"result.content.map(c, c.type == 'text' ? {'type': 'text', 'text': regex.replace(c.text, '[0-9]{4}( [0-9]{4}){3}', '[card]')} : c)")),
		run: func(e *env) {
			e.wantText("call", e.tool("get_report", nil), "Report for ana@example.com. Card [card]. Host db-internal-7.")
		}},
	{id: "tools/result keep image", title: "the guarded rewrite changes text and keeps the image and link",
		rules: rules(rule("tools/result", "upstream.name == 'picture'", "content",
			"result.content.map(c, c.type == 'text' ? {'type': 'text', 'text': regex.replace(c.text, 'picture', 'photo')} : c)")),
		run: func(e *env) {
			res := e.okTool("call", e.tool("picture", nil))
			blocks, _ := res["content"].([]any)
			if len(blocks) != 3 {
				e.t.Fatalf("got %d content blocks, want 3: %s", len(blocks), asJSON(res))
			}
			e.eq("text block", blocks[0], map[string]any{"type": "text", "text": "a photo"})
			e.eq("image type", get(blocks[1], "type"), "image")
			e.eq("image data", get(blocks[1], "data"), pixelPNG)
			e.eq("image mime", get(blocks[1], "mimeType"), "image/png")
			e.eq("link type", get(blocks[2], "type"), "resource_link")
			e.eq("link uri", get(blocks[2], "uri"), "ref://docs/guide")
			e.eq("link name", get(blocks[2], "name"), "guide")
		}},
	{id: "tools/result deny", title: "deny a result by its text",
		rules: rules(rule("tools/result", "result.text.contains('4111')", "decision", "'deny'", "reason", "'The result held a card number and was withheld.'")),
		run: func(e *env) {
			rep := e.tool("get_report", nil)
			e.wantToolError("withheld", rep, "The result held a card number and was withheld.")
			e.noText("withheld", asJSON(rep.Result), "4111", "ana@example.com")
			e.wantText("other results pass", e.tool("echo", map[string]any{"text": "hi"}), "hi")
		}},
	{id: "tools/result plain", title: "remove structured content, keep the text",
		rules: rules(rule("tools/result", "upstream.name == 'echo'", "structuredContent", "null")),
		run: func(e *env) {
			res := e.wantText("call", e.tool("echo", map[string]any{"text": "hi"}), "hi")
			if v, ok := res["structuredContent"]; ok {
				e.t.Errorf("structured content is still present: %s", asJSON(v))
			}
		}},
}

var progressCases = []tcase{
	{id: "tools/progress", title: "drop one progress event and reshape the rest",
		rules: rules(
			rule("tools/progress", "progress.progress == 2", "decision", "'deny'"),
			rule("tools/progress", "progress.message.contains('db-internal')", "message", "regex.replace(progress.message, 'db-internal-[0-9]+', 'the database')")),
		run: func(e *env) {
			rep := e.tool("slow_count", map[string]any{"steps": 3, "delayMs": 10}, withProgress)
			e.wantText("result", rep, "counted 3")
			e.eq("progress messages", progressMessages(rep.Progress), []string{"step 1 of 3: reading the database", "step 3 of 3: reading the database"})
			if len(rep.Progress) == 2 {
				e.eq("progress numbers", []any{rep.Progress[0]["progress"], rep.Progress[1]["progress"]}, []any{1, 3})
				e.eq("totals", []any{rep.Progress[0]["total"], rep.Progress[1]["total"]}, []any{3, 3})
			}
			e.wantHit("tools/call", "tools/progress", "deny")
		}},
	{id: "tools/progress then refusal", title: "progress is relayed, then the result is refused",
		rules: rules(rule("tools/result", "upstream.name == 'slow_count'", "decision", "'deny'", "reason", "'Counting is not allowed.'")),
		run: func(e *env) {
			rep := e.tool("slow_count", map[string]any{"steps": 2, "delayMs": 10}, withProgress)
			e.wantToolError("refused", rep, "Counting is not allowed.")
			e.eq("progress messages", progressMessages(rep.Progress), []string{"step 1 of 2: reading db-internal-7", "step 2 of 2: reading db-internal-7"})
		}},
}

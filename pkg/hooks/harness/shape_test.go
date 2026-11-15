package harness

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const limitTool = `{"name":"list","inputSchema":{"type":"object","properties":{"limit":{"type":"integer"},"q":{"type":"string"}}}}`

var clamp = rule("tools/call", "has(args.limit) && args.limit > 100", "args.limit", "100")

// Types are the customer's contract. A rule written for a number refuses a
// null or a string; the engine does not guess or convert.
func TestTypedRuleRefusesWrongTypes(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(limitTool, nil)
	g := NewGateway(t)
	g.AddServer(t, "a", u, clamp)
	cs := Connect(t, g.Direct("a"), ClientOptions{})

	mustCall(t, cs, "list", map[string]any{"limit": 250, "q": "x"})
	eq(t, "a number over the limit is clamped", u.Last(t, "tools/call").Args(), map[string]any{"limit": 100, "q": "x"})
	if !hit(g.LastRecord(t, "tools/call"), "tools/call", "set") {
		t.Errorf("the clamp left no hit: %s", g.LastRecord(t, "tools/call").JSON())
	}
	mustCall(t, cs, "list", map[string]any{"limit": 7})
	eq(t, "a number under the limit passes", u.Last(t, "tools/call").Args(), map[string]any{"limit": 7})

	u.Forget()
	for name, v := range map[string]any{"null": nil, "a string": "50", "a list": []any{1}} {
		res, err := call(t, cs, "list", map[string]any{"limit": v})
		if !refused(res, err) {
			t.Errorf("%s for a number was not refused", name)
		}
	}
	eq(t, "wrong-typed calls that reached the server", len(u.Requests("tools/call")), 0)
	if !hit(g.LastRecord(t, "tools/call"), "tools/call", "error") {
		t.Errorf("the refusal left no error hit: %s", g.LastRecord(t, "tools/call").JSON())
	}
}

// A customer who wants to accept text for a number writes two rules:
// convert, then clamp.
func TestCoerceThenClamp(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(limitTool, nil)
	g := NewGateway(t)
	g.AddServer(t, "a", u,
		rule("tools/call", "has(args.limit) && type(args.limit) == string", "args.limit", "int(args.limit)"),
		clamp)
	cs := Connect(t, g.Direct("a"), ClientOptions{})

	for _, c := range []struct {
		in   any
		want int
	}{{"50", 50}, {"250", 100}, {250, 100}, {7, 7}} {
		res := mustCall(t, cs, "list", map[string]any{"limit": c.in})
		if res.IsError {
			t.Errorf("limit %v was refused: %s", c.in, TextOf(res))
			continue
		}
		eq(t, "upstream limit", u.Last(t, "tools/call").Args(), map[string]any{"limit": c.want})
	}
	u.Forget()
	res, err := call(t, cs, "list", map[string]any{"limit": "abc"})
	if !refused(res, err) {
		t.Error("text that is not a number was not refused")
	}
	eq(t, "calls that reached the server", len(u.Requests("tools/call")), 0)
}

func findResult() map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": `{"owner":{"email":"a@b.c"}}`}},
		"structuredContent": map[string]any{
			"owner": map[string]any{"name": "ana", "email": "a@b.c"},
			"items": []any{map[string]any{"id": 1, "secret": "s1"}, map[string]any{"id": 2, "secret": "s2"}},
		},
	}
}

// A rule changes only the field it names. Dropping from structured content
// leaves the text alone.
func TestStructuredDrop(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(`{"name":"find","inputSchema":{"type":"object","properties":{}}}`, Static(Reply{Result: findResult()}))
	g := NewGateway(t)
	g.AddServer(t, "a", u, rule("tools/result", "upstream.name == 'find'",
		"structuredContent", "drop(result.structuredContent, ['owner.email', 'items.*.secret'])"))
	cs := Connect(t, g.Direct("a"), ClientOptions{})

	res := mustCall(t, cs, "find", nil)
	eq(t, "structured content", res.StructuredContent, map[string]any{
		"owner": map[string]any{"name": "ana"},
		"items": []any{map[string]any{"id": 1}, map[string]any{"id": 2}},
	})
	eq(t, "text content is untouched", TextOf(res), `{"owner":{"email":"a@b.c"}}`)
	if !hit(g.LastRecord(t, "tools/call"), "tools/result", "set") {
		t.Errorf("the result rule left no hit: %s", g.LastRecord(t, "tools/call").JSON())
	}
}

// Setting structured content to null removes it: plain output only.
func TestPlainOutputOnly(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(`{"name":"find","inputSchema":{"type":"object","properties":{}}}`, Static(Reply{Result: findResult()}))
	g := NewGateway(t)
	g.AddServer(t, "a", u, rule("tools/result", "true", "structuredContent", "null"))
	raw := Raw(t, g.Direct("a"), RawRequest{Method: "tools/call", Params: map[string]any{"name": "find", "arguments": map[string]any{}}})
	if _, ok := raw.Result()["structuredContent"]; ok {
		t.Errorf("structured content is still present: %v", raw.Result())
	}
	if raw.Result()["content"] == nil {
		t.Errorf("the text content is gone: %v", raw.Result())
	}
}

const rewriteText = "result.content.map(c, c.type == 'text' ? {'type': 'text', 'text': regex.replace(c.text, 'Datadog', 'Bits')} : c)"

// The guarded text rewrite: text blocks are rewritten, an image block is
// passed through whole.
func TestTextRewriteKeepsOtherBlocks(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(`{"name":"report","inputSchema":{"type":"object","properties":{}}}`, Static(Reply{Result: map[string]any{"content": []any{
		map[string]any{"type": "text", "text": "Datadog says hi. Datadog again."},
		map[string]any{"type": "image", "data": "aGVsbG8=", "mimeType": "image/png"},
		map[string]any{"type": "text", "text": "no change"},
	}}}))
	g := NewGateway(t)
	g.AddServer(t, "a", u, rule("tools/result", "true", "content", rewriteText))
	cs := Connect(t, g.Direct("a"), ClientOptions{})

	res := mustCall(t, cs, "report", nil)
	if res.IsError || len(res.Content) != 3 {
		t.Fatalf("result: %s", asJSON(res))
	}
	eq(t, "first block", res.Content[0].(*mcp.TextContent).Text, "Bits says hi. Bits again.")
	img, ok := res.Content[1].(*mcp.ImageContent)
	if !ok || string(img.Data) != "hello" || img.MIMEType != "image/png" {
		t.Errorf("the image block changed: %s", asJSON(res.Content[1]))
	}
	eq(t, "last block", res.Content[2].(*mcp.TextContent).Text, "no change")
}

// The unguarded rewrite reads text on every block. On a result with an
// image it errors, and an erroring result rule refuses the result.
func TestUnguardedTextRewriteFailsClosed(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(`{"name":"report","inputSchema":{"type":"object","properties":{}}}`, Static(Reply{Result: map[string]any{"content": []any{
		map[string]any{"type": "text", "text": "secret"},
		map[string]any{"type": "image", "data": "aGVsbG8=", "mimeType": "image/png"},
	}}}))
	g := NewGateway(t)
	g.AddServer(t, "a", u, rule("tools/result", "true", "content",
		"result.content.map(c, {'type': 'text', 'text': regex.replace(c.text, 'secret', 'xxx')})"))
	cs := Connect(t, g.Direct("a"), ClientOptions{})
	res, err := call(t, cs, "report", nil)
	if !refused(res, err) {
		t.Fatalf("the result was not refused: %s", asJSON(res))
	}
	if res != nil {
		lacks(t, "the refusal", asJSON(res), "secret")
		lacks(t, "the refusal", asJSON(res), "aGVsbG8=")
	}
}

// Resource content is shaped by resources/result rules.
func TestReadResultRewrite(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Resource("docs://a", Static(Reply{Result: Contents("docs://a", "Confluence page")}))
	g := NewGateway(t)
	g.AddServer(t, "a", u, rule("resources/result", "true", "text", "regex.replace(result.text, 'Confluence', 'the wiki')"))
	cs := Connect(t, g.Direct("a"), ClientOptions{})
	rr, err := read(t, cs, "docs://a")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "text", rr.Contents[0].Text, "the wiki page")
}

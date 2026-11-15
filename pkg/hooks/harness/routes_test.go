package harness

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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

// One server on the 1:1 route: names and URIs are the server's own.
func TestDirectRoute(t *testing.T) {
	u := docsUpstream(t, "a")
	g := NewGateway(t)
	g.AddServer(t, "a", u)
	cs := Connect(t, g.Direct("a"), ClientOptions{})
	ctx := bg(t)

	lt, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "tools", toolNames(lt.Tools), []string{"search"})

	res := mustCall(t, cs, "search", map[string]any{"q": "x"})
	eq(t, "tool text", TextOf(res), "a found x")
	link, _ := res.Content[1].(*mcp.ResourceLink)
	if link == nil || link.URI != "docs://a" {
		t.Errorf("resource link on 1:1 should be unprefixed, got %+v", res.Content[1])
	}
	up := u.Last(t, "tools/call")
	eq(t, "upstream tool name", up.Map()["name"], "search")
	eq(t, "upstream arguments", up.Args(), map[string]any{"q": "x"})

	lr, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var uris []string
	for _, r := range lr.Resources {
		uris = append(uris, r.URI)
	}
	eq(t, "resources", uris, []string{"docs://a", "docs://shared"})

	rr, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "docs://a"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "read uri", rr.Contents[0].URI, "docs://a")
	eq(t, "read text", rr.Contents[0].Text, "content of docs://a")

	tr, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "notes://a/42"})
	if err != nil {
		t.Fatalf("read through a {id} template: %v", err)
	}
	eq(t, "template read", tr.Contents[0].Text, "a read notes://a/42")

	lp, err := cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(lp.Prompts) != 1 || lp.Prompts[0].Name != "greet" {
		t.Fatalf("prompts: %+v", lp.Prompts)
	}
	gp, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "greet", Arguments: map[string]string{"who": "ana"}})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "prompt text", gp.Messages[0].Content.(*mcp.TextContent).Text, "a greets ana")
	eq(t, "upstream prompt arguments", u.Last(t, "prompts/get").Args(), map[string]any{"who": "ana"})
}

// Two servers on the mux: names get <slug>__, URIs get <slug>+, and each
// request reaches only its own server under the server's own names.
func TestMuxRoute(t *testing.T) {
	ua, ub := docsUpstream(t, "a"), docsUpstream(t, "b")
	g := NewGateway(t)
	g.AddServer(t, "a", ua)
	g.AddServer(t, "b", ub)
	cs := Connect(t, g.Mux(), ClientOptions{})
	ctx := bg(t)

	lt, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "tools", toolNames(lt.Tools), []string{"a__search", "b__search", "get_server_instructions"})

	res := mustCall(t, cs, "b__search", map[string]any{"q": "x"})
	eq(t, "tool text", TextOf(res), "b found x")
	eq(t, "upstream b got the bare name", ub.Last(t, "tools/call").Map()["name"], "search")
	if n := len(ua.Requests("tools/call")); n != 0 {
		t.Errorf("server a received %d calls meant for b", n)
	}
	link, _ := res.Content[1].(*mcp.ResourceLink)
	if link == nil || link.URI != "b+docs://b" {
		t.Errorf("resource link on the mux should carry the slug prefix, got %+v", res.Content[1])
	}

	lr, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var uris []string
	for _, r := range lr.Resources {
		uris = append(uris, r.URI)
	}
	eq(t, "resources", uris, []string{"a+docs://a", "a+docs://shared", "b+docs://b", "b+docs://shared"})

	ltm, err := cs.ListResourceTemplates(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var tmpls []string
	for _, r := range ltm.ResourceTemplates {
		tmpls = append(tmpls, r.URITemplate)
	}
	eq(t, "templates", tmpls, []string{"a+notes://a/{id}", "b+notes://b/{id}"})

	t.Run("prefixed read routes to its server", func(t *testing.T) {
		rr, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "b+docs://shared"})
		if err != nil {
			t.Fatal(err)
		}
		eq(t, "read uri", rr.Contents[0].URI, "b+docs://shared")
		eq(t, "upstream b got the bare uri", ub.Last(t, "resources/read").Map()["uri"], "docs://shared")
		if n := len(ua.Requests("resources/read")); n != 0 {
			t.Errorf("server a received %d reads meant for b", n)
		}
	})

	t.Run("a bare uri with one owner resolves", func(t *testing.T) {
		rr, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "docs://a"})
		if err != nil {
			t.Fatal(err)
		}
		eq(t, "the reply carries the prefixed uri", rr.Contents[0].URI, "a+docs://a")
		rec := g.LastRecord(t, "resources/read")
		if rec.Outcome.Fallback == nil || rec.Outcome.Fallback.Outcome != "resolved" || rec.Outcome.Fallback.Slug != "a" {
			t.Errorf("fallback event: %+v", rec.Outcome.Fallback)
		}
	})

	t.Run("a bare uri with two owners is ambiguous", func(t *testing.T) {
		ua.Forget()
		ub.Forget()
		_, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "docs://shared"})
		if err == nil {
			t.Fatal("an ambiguous read succeeded")
		}
		rec := g.LastRecord(t, "resources/read")
		if rec.Outcome.Fallback == nil || rec.Outcome.Fallback.Outcome != "ambiguous" {
			t.Errorf("fallback event: %+v", rec.Outcome.Fallback)
		}
		if n := len(ua.Requests("resources/read")) + len(ub.Requests("resources/read")); n != 0 {
			t.Errorf("an ambiguous read reached %d upstreams", n)
		}
	})

	t.Run("prompts", func(t *testing.T) {
		lp, err := cs.ListPrompts(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, p := range lp.Prompts {
			names = append(names, p.Name)
		}
		eq(t, "prompts", names, []string{"a__greet", "b__greet"})
		gp, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "a__greet", Arguments: map[string]string{"who": "ana"}})
		if err != nil {
			t.Fatal(err)
		}
		eq(t, "prompt text", gp.Messages[0].Content.(*mcp.TextContent).Text, "a greets ana")
		eq(t, "upstream a got the bare prompt name", ua.Last(t, "prompts/get").Map()["name"], "greet")
	})

	t.Run("the gateway's own instructions tool", func(t *testing.T) {
		res := mustCall(t, cs, "get_server_instructions", map[string]any{"slug": "b"})
		eq(t, "instructions", TextOf(res), "Instructions of b")
	})

	t.Run("a name without a slug is refused", func(t *testing.T) {
		if _, err := call(t, cs, "search", nil); err == nil {
			t.Error("a bare tool name was accepted on the mux")
		}
	})
}

// Search mode exposes the gateway's own tools, and execute_tool is exactly a
// tool call.
func TestSearchRoute(t *testing.T) {
	ua, ub := docsUpstream(t, "a"), docsUpstream(t, "b")
	g := NewGateway(t)
	g.AddServer(t, "a", ua)
	g.AddServer(t, "b", ub)
	cs := Connect(t, g.Search(), ClientOptions{})

	lt, err := cs.ListTools(bg(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "tools", toolNames(lt.Tools), []string{"tool_search", "list_tools", "get_tool_definition", "execute_tool"})

	found := mustCall(t, cs, "tool_search", map[string]any{"query": "search"})
	has(t, "search results", TextOf(found), "a__search")
	has(t, "search results", TextOf(found), "b__search")

	listed := mustCall(t, cs, "list_tools", nil)
	if !strings.Contains(TextOf(listed), "a:") || !strings.Contains(TextOf(listed), "- search") {
		t.Errorf("list_tools text: %q", TextOf(listed))
	}

	def := mustCall(t, cs, "get_tool_definition", map[string]any{"names": []string{"a__search", "nope"}})
	has(t, "definition", TextOf(def), `"name":"a__search"`)
	has(t, "unknown names", TextOf(def), `"unknown":["nope"]`)

	res := mustCall(t, cs, "execute_tool", map[string]any{"name": "b__search", "arguments": map[string]any{"q": "x"}})
	eq(t, "execute_tool text", TextOf(res), "b found x")
	eq(t, "upstream b got the bare name", ub.Last(t, "tools/call").Map()["name"], "search")
}

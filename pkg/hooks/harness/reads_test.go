package harness

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func read(t testing.TB, cs *mcp.ClientSession, uri string) (*mcp.ReadResourceResult, error) {
	t.Helper()
	return cs.ReadResource(bg(t), &mcp.ReadResourceParams{URI: uri})
}

// A tool hands out a link to a resource the server never lists. The spec
// allows that, and the harness reads it like a client would.
func TestReadOfUnlistedLinkedResource(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Resource("nb://index", nil)
	u.Unlisted("nb://notebook/123", nil)
	u.Tool(`{"name":"open","inputSchema":{"type":"object","properties":{}}}`, Static(Reply{Result: map[string]any{"content": []any{
		map[string]any{"type": "resource_link", "uri": "nb://notebook/123", "name": "nb"}}}}))
	g := NewGateway(t)
	g.AddServer(t, "a", u)
	cs := Connect(t, g.Direct("a"), ClientOptions{})

	res := mustCall(t, cs, "open", nil)
	link := res.Content[0].(*mcp.ResourceLink)
	rr, err := read(t, cs, link.URI)
	if err != nil {
		t.Fatalf("reading the linked resource: %v", err)
	}
	eq(t, "content", rr.Contents[0].Text, "content of nb://notebook/123")
	eq(t, "upstream uri", u.Last(t, "resources/read").Map()["uri"], "nb://notebook/123")
}

// The same on the mux: the link carries the slug, so the read routes.
func TestReadOfUnlistedLinkedResourceOnTheMux(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Unlisted("nb://notebook/123", nil)
	u.Tool(`{"name":"open","inputSchema":{"type":"object","properties":{}}}`, Static(Reply{Result: map[string]any{"content": []any{
		map[string]any{"type": "resource_link", "uri": "nb://notebook/123", "name": "nb"}}}}))
	other := NewUpstream(t, V2026)
	other.Tool(plainTool, nil)
	g := NewGateway(t)
	g.AddServer(t, "a", u)
	g.AddServer(t, "b", other)
	cs := Connect(t, g.Mux(), ClientOptions{})

	res := mustCall(t, cs, "a__open", nil)
	link := res.Content[0].(*mcp.ResourceLink)
	eq(t, "link", link.URI, "a+nb://notebook/123")
	rr, err := read(t, cs, link.URI)
	if err != nil {
		t.Fatalf("reading the linked resource: %v", err)
	}
	eq(t, "content", rr.Contents[0].Text, "content of nb://notebook/123")
	eq(t, "reads that reached the other server", len(other.Requests("resources/read")), 0)
}

// Template forms real servers use: GitHub's {/path*} and a {?query}.
func TestReadsThroughRicherTemplates(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Template("repo://{owner}/{repo}/contents{/path*}")
	u.Template("search://issues{?q,limit}")
	u.ReadDefault(func(r Recorded) Reply {
		uri, _ := r.Map()["uri"].(string)
		return Reply{Result: Contents(uri, "read "+uri)}
	})
	g := NewGateway(t)
	g.AddServer(t, "gh", u)
	cs := Connect(t, g.Direct("gh"), ClientOptions{})
	for _, uri := range []string{"repo://o/r/contents/docs/a.md", "repo://o/r/contents", "search://issues?q=bug&limit=5", "search://issues"} {
		rr, err := read(t, cs, uri)
		if err != nil {
			t.Errorf("read %s: %v", uri, err)
			continue
		}
		eq(t, "content of "+uri, rr.Contents[0].Text, "read "+uri)
	}
}

// A deny always wins: on a listed resource, on an unlisted one, and on a
// URI under a template. The server never sees the read.
func TestDeniedReadsNeverReachTheServer(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Resource("nb://index", nil)
	u.Resource("nb://secret/listed", nil)
	u.Template("nb://{folder}/{name}")
	u.ReadDefault(func(r Recorded) Reply {
		uri, _ := r.Map()["uri"].(string)
		return Reply{Result: Contents(uri, "read "+uri)}
	})
	g := NewGateway(t)
	g.AddServer(t, "a", u, rule("resources/filter", "uri.startsWith('nb://secret/')", "decision", "'deny'"))
	cs := Connect(t, g.Direct("a"), ClientOptions{})

	lr, err := cs.ListResources(bg(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	var uris []string
	for _, r := range lr.Resources {
		uris = append(uris, r.URI)
	}
	eq(t, "listed resources", uris, []string{"nb://index"})

	if _, err := read(t, cs, "nb://public/doc"); err != nil {
		t.Errorf("a read outside the denied folder failed: %v", err)
	}
	u.Forget()
	for _, uri := range []string{"nb://secret/listed", "nb://secret/key", "nb://secret/deeper/key"} {
		if _, err := read(t, cs, uri); err == nil {
			t.Errorf("read of %s was allowed", uri)
		}
	}
	eq(t, "denied reads that reached the server", len(u.Requests("resources/read")), 0)
}

// The server answers an allowed read with content labelled with another
// URI, as a redirect or a canonical address. The client still gets it.
func TestContentUnderAnotherURIIsKept(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Resource("docs://guide", Static(Reply{Result: Contents("https://docs.example.com/guide", "hello")}))
	g := NewGateway(t)
	g.AddServer(t, "a", u)
	cs := Connect(t, g.Direct("a"), ClientOptions{})
	rr, err := read(t, cs, "docs://guide")
	if err != nil {
		t.Fatal(err)
	}
	if len(rr.Contents) != 1 || rr.Contents[0].Text != "hello" {
		t.Errorf("the content was dropped: %s", asJSON(rr.Contents))
	}
}

// URIs are never translated. An encoded slash is not a slash, so the
// encoded form of a listed URI is a different resource: it is never granted
// or shaped as the listed one, and the server only ever sees the client's
// own spelling.
func TestEncodedSlashIsNotASlash(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Resource("file:///a/b", Static(Reply{Result: Contents("file:///a/b", "listed")}))
	u.ReadDefault(func(r Recorded) Reply {
		uri, _ := r.Map()["uri"].(string)
		return Reply{Result: Contents(uri, "other")}
	})
	g := NewGateway(t)
	g.AddServer(t, "a", u, rule("resources/result", "uri == 'file:///a/b'", "text", "'shaped as the listed resource'"))
	cs := Connect(t, g.Direct("a"), ClientOptions{})

	rr, err := read(t, cs, "file:///a/b")
	if err != nil {
		t.Fatalf("the listed form failed: %v", err)
	}
	eq(t, "the listed form", rr.Contents[0].Text, "shaped as the listed resource")

	u.Forget()
	enc, err := read(t, cs, "file:///a%2Fb")
	if err == nil {
		// Allowed as its own, unlisted resource: then it is the server's
		// answer for that spelling, untouched by the listed resource's rule.
		if len(enc.Contents) != 1 || enc.Contents[0].Text != "other" {
			t.Errorf("the encoded form was treated as the listed resource: %s", asJSON(enc.Contents))
		}
	}
	for _, r := range u.Requests("resources/read") {
		eq(t, "the spelling the server received", r.Map()["uri"], "file:///a%2Fb")
	}
}

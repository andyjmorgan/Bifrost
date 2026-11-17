package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A URI the gateway accepts is its own canonical spelling, and the
// spellings a URL parser folds together have one.
func TestCanonicalURI(t *testing.T) {
	for _, u := range []string{
		"file:///secrets/key", "https://wiki.example/private/x", "https://wiki.example:8443/a%2Fb", "https://[::1]/x", "https://127.0.0.1/x",
		"confluence://ENG/home", "git+ssh://host/repo", "urn:example:a", "data:text/plain,a%20b", "web://fetch?url=https://example.com/a//b&n=%7e",
		"https://xn--bcher-kva.example/caf%C3%A9/", "skill://demo/SKILL.md", "docs://h:8080/x",
	} {
		if nonCanonical(u) {
			t.Errorf("%s is refused", u)
		}
		if got := canonicalURI(u); got != u {
			t.Errorf("canonicalURI(%s) = %s; a URI the gateway accepts must come back as it is", u, got)
		}
	}
	for odd, want := range map[string]string{
		"FILE:///secrets/key":                 "file:///secrets/key",
		"file:/secrets/key":                   "file:///secrets/key",
		"file://localhost/secrets/key":        "file:///secrets/key",
		"file:///secrets/./a/../key":          "file:///secrets/key",
		"file:///secrets//key":                "file:///secrets/key",
		"file:///%73ecrets/key":               "file:///secrets/key",
		"file:///a%2fb":                       "file:///a%2Fb",
		"https://WIKI.example/private/x":      "https://wiki.example/private/x",
		"https://wiki.example:443/private/x":  "https://wiki.example/private/x",
		"https://wiki.example:0443/private/x": "https://wiki.example/private/x",
		"https://wiki.example:08443/x":        "https://wiki.example:8443/x",
		"https://[0:0:0:0:0:0:0:1]/private/x": "https://[::1]/private/x",
		"https://wiki.example/café/x":         "https://wiki.example/caf%C3%A9/x",
		"https://wiki.example/a b{c}":         "https://wiki.example/a%20b%7Bc%7D",
		"https://wiki.example/private/x#frag": "https://wiki.example/private/x",
		"docs:/x":                             "docs:///x",
		"docs://h:0080/x":                     "docs://h:80/x",
	} {
		got := canonicalURI(odd)
		if got != want {
			t.Errorf("canonicalURI(%s) = %s, want %s", odd, got, want)
		}
		if nonCanonical(got) {
			t.Errorf("canonicalURI(%s) = %s, which the gateway refuses", odd, got)
		}
		if canonicalURI(got) != got {
			t.Errorf("canonicalURI is not stable on %s", got)
		}
	}
	// Spellings it cannot fold stay refused: nothing is guessed.
	for _, u := range []string{"https://user@wiki.example/x", "https://2130706433/x", "https://wiki.example./x", "https://%77iki.example/x", "file:///a%zzb"} {
		if got := canonicalURI(u); !nonCanonical(got) {
			t.Errorf("canonicalURI(%s) = %s was accepted", u, got)
		}
	}
}

// A server may list a resource under a spelling that is not canonical. The
// rules see its canonical spelling, so a deny by prefix or by name covers
// the URI a client can ask for. The read goes to the server in the server's
// own spelling, and its answer under that spelling is the answer.
func TestAServersOddSpellingIsOneResource(t *testing.T) {
	ctx := context.Background()
	cat := Catalog{Resources: []*mcp.Resource{
		{URI: "https://WIKI.example/private/x", Name: "secret"},
		{URI: "https://WIKI.example/public/y", Name: "open"},
	}}
	for what, rule := range map[string]Rule{
		"a deny by name":   {Hook: "resources/list.post", When: "resource.?name.orValue('') == 'secret'", Then: map[string]string{KeyDecision: "'deny'"}},
		"a deny by prefix": {Hook: "resources/list.post", When: "resource.uri.startsWith('https://wiki.example/private/')", Then: map[string]string{KeyDecision: "'deny'"}},
	} {
		set, probs := Compile([]Rule{rule})
		if probs != nil {
			t.Fatal(probs)
		}
		s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Catalog: cat, Rules: set}
		e, r := &Engine{}, treeRequest(s)
		listed, _ := json.Marshal(e.ListResources(ctx, r).Result)
		if strings.Contains(string(listed), "/private/") || !strings.Contains(string(listed), `"https://wiki.example/public/y"`) {
			t.Errorf("%s: listing %s", what, listed)
		}
		if o := e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: "https://wiki.example/private/x"}); o.Forward != nil {
			t.Errorf("%s: the canonical spelling of a hidden resource was forwarded", what)
		}
		o := e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: "https://wiki.example/public/y"})
		if o.Forward == nil || o.Forward.Params.(*mcp.ReadResourceParams).URI != "https://WIKI.example/public/y" {
			t.Fatalf("%s: the open resource was not asked for in the server's spelling: %+v", what, o.Error)
		}
		p := &mcp.ReadResourceParams{URI: "https://wiki.example/public/y"}
		out := e.UpstreamReadReply(ctx, r, o.Call, p, json.RawMessage(`{"contents":[{"uri":"https://WIKI.example/public/y","text":"hello"},{"uri":"https://WIKI.example/private/x","text":"SECRET"}]}`))
		got, _ := json.Marshal(out.Result)
		if !strings.Contains(string(got), `"uri":"https://wiki.example/public/y"`) || !strings.Contains(string(got), "hello") || strings.Contains(string(got), "SECRET") {
			t.Errorf("%s: reply %s", what, got)
		}
		if o := e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: "https://WIKI.example/public/y"}); o.Forward != nil {
			t.Errorf("%s: a client's odd spelling was forwarded", what)
		}
	}
}

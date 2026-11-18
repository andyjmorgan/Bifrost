package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
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
		e, r := &Engine{}, newRequest("direct", s)
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

// A scheme is not case sensitive: a server that parses FILE:///secrets/key
// reads the same file as file:///secrets/key. A URI whose scheme is written
// in capitals is therefore refused, so it cannot pass a rule or a denied
// template written against the lower-case spelling and be forwarded as typed.
func TestAURIWithASchemeInCapitalsIsRefused(t *testing.T) {
	ctx := context.Background()
	if u, err := url.Parse("FILE:///secrets/key"); err != nil || u.Scheme != "file" {
		t.Fatalf("the stated condition does not hold: net/url reads the scheme as %q", u.Scheme)
	}
	cat := Catalog{
		Resources: []*mcp.Resource{{URI: "file:///listed/secret.txt", Name: "listed"}},
		Templates: []*mcp.ResourceTemplate{{URITemplate: "file:///vault/{name}", Name: "vault"}},
	}
	s := newServer(t, cat,
		Rule{Hook: "resources/list.post", When: "resource.uri.startsWith('file:///secrets/')", Then: map[string]string{KeyDecision: "'deny'"}},
		Rule{Hook: "resources/list.post", When: "resource.uri == 'file:///listed/secret.txt'", Then: map[string]string{KeyDecision: "'deny'"}},
		Rule{Hook: "resources/templates/list.post", When: "template.uriTemplate.startsWith('file:///vault/')", Then: map[string]string{KeyDecision: "'deny'"}},
	)
	for _, uri := range []string{"file:///secrets/key", "file:///vault/a", "file:///listed/secret.txt", "FILE:///listed/secret.txt"} {
		if o := (&Engine{}).ReadResource(ctx, newRequest("direct", s), &mcp.ReadResourceParams{URI: uri}); o.Forward != nil {
			t.Fatalf("control: %s was forwarded", uri)
		}
	}
	for _, uri := range []string{"FILE:///secrets/key", "File:///secrets/key", "FILE:///vault/a"} {
		if o := (&Engine{}).ReadResource(ctx, newRequest("direct", s), &mcp.ReadResourceParams{URI: uri}); o.Forward != nil {
			t.Errorf("%s was forwarded as %s", uri, asJSON(o.Forward.Params))
		}
	}
}

// A scheme in capitals is refused in every form: on the mux with the slug in
// front, on a scheme with a "+", and with no "//". A dot segment written with
// escapes is refused too.
func TestASchemeInCapitalsIsRefusedInEveryForm(t *testing.T) {
	ctx := context.Background()
	cat := Catalog{Resources: []*mcp.Resource{{URI: "git+ssh://host/secret", Name: "g"}}}
	s := newServer(t, cat, Rule{Hook: "resources/list.post", When: "resource.uri.startsWith('git+ssh://host/') || resource.uri.startsWith('file:///secrets/')", Then: map[string]string{KeyDecision: "'deny'"}})
	s2 := newServer(t, Catalog{})
	s2.Slug = "b"
	mux := newRequest("direct", s)
	mux.Route, mux.Servers = "mux", []*Server{s, s2}
	for _, uri := range []string{"FILE:/secrets/key", "File:secrets/key", "file:///public/%2E%2E/secrets/key", "GIT+ssh://host/secret", "git+SSH://host/secret"} {
		if o := (&Engine{}).ReadResource(ctx, newRequest("direct", s), &mcp.ReadResourceParams{URI: uri}); o.Forward != nil {
			t.Errorf("direct: %s was forwarded", uri)
		}
	}
	for _, uri := range []string{"a+FILE:///secrets/key", "A+file:///secrets/key", "a+git+SSH://host/secret", "a+GIT+ssh://host/secret", "a+git+ssh://host/secret", "git+ssh://host/secret"} {
		o := (&Engine{}).ReadResource(ctx, mux, &mcp.ReadResourceParams{URI: uri})
		if o.Forward != nil && strings.Contains(strings.ToLower(asJSON(o.Forward.Params)), `"git+ssh://host/secret"`) {
			t.Errorf("mux: %s reached the denied resource: %s", uri, asJSON(o.Forward.Params))
		}
		if o.Forward != nil && strings.Contains(strings.ToLower(asJSON(o.Forward.Params)), "/secrets/") {
			t.Errorf("mux: %s was forwarded: %s", uri, asJSON(o.Forward.Params))
		}
	}
}

// A deny written against a URI is not passed by another spelling of the same
// URI. A server that reads a URI with a URL parser reads the denied resource
// under each of them: Go's net/url gives the same path for file:/x and
// file:///x and drops a fragment, and the WHATWG parser (Node, and so the
// TypeScript SDK's McpServer, which looks a resource up by new
// URL(uri).toString()) also folds file://localhost/, a leading space, a tab
// inside the scheme, backslashes, the host's case and a default port. The
// gateway refuses each such spelling on a read, on both routes, and drops it
// from what a server returns.
func TestAnotherSpellingOfADeniedURIIsRefused(t *testing.T) {
	ctx := context.Background()
	for _, pair := range [][2]string{{"file:/secrets/key", "file:///secrets/key"}, {"file:///listed/secret.txt#x", "file:///listed/secret.txt"}, {"file:/secrets/../secrets/key", "file:///secrets/key"}} {
		a, _ := url.Parse(pair[0])
		b, _ := url.Parse(pair[1])
		if a == nil || b == nil || a.Scheme != b.Scheme || a.Host != b.Host || a.JoinPath().Path != b.JoinPath().Path {
			t.Fatalf("the stated condition does not hold: net/url reads %s and %s differently", pair[0], pair[1])
		}
	}
	cat := Catalog{
		Resources: []*mcp.Resource{{URI: "file:///listed/secret.txt", Name: "listed"}, {URI: "https://wiki.example/private", Name: "w"}, {URI: "file:///listed/a%2Fb", Name: "enc"}},
		Templates: []*mcp.ResourceTemplate{{URITemplate: "file:///vault/{name}", Name: "vault"}},
	}
	s := newServer(t, cat,
		Rule{Hook: "resources/list.post", When: "resource.uri.startsWith('file:///secrets/')", Then: map[string]string{KeyDecision: "'deny'"}},
		Rule{Hook: "resources/list.post", When: "resource.uri == 'file:///listed/secret.txt' || resource.uri == 'https://wiki.example/private' || resource.uri == 'file:///listed/a%2Fb'", Then: map[string]string{KeyDecision: "'deny'"}},
		Rule{Hook: "resources/templates/list.post", When: "template.uriTemplate.startsWith('file:///vault/')", Then: map[string]string{KeyDecision: "'deny'"}},
	)
	s2 := newServer(t, Catalog{})
	s2.Slug = "b"
	mux := newRequest("direct", s)
	mux.Route, mux.Servers = "mux", []*Server{s, s2}
	for _, uri := range []string{"file:///secrets/key", "file:///listed/secret.txt", "https://wiki.example/private", "file:///listed/a%2Fb", "file:///vault/a"} {
		if o := (&Engine{}).ReadResource(ctx, newRequest("direct", s), &mcp.ReadResourceParams{URI: uri}); o.Forward != nil {
			t.Fatalf("control: %s was forwarded", uri)
		}
	}
	for what, uris := range map[string][]string{
		"a prefix deny": {"file:/secrets/key", "file://localhost/secrets/key", " file:///secrets/key", "fi\tle:///secrets/key", `file:\\\secrets\key`,
			"file:/secrets/../secrets/key", "file:/%73ecrets/key"},
		"an exact deny of a listed resource": {"file:/listed/secret.txt", "file://localhost/listed/secret.txt", "file:///listed/secret.txt#x", "file:///listed/secret.txt?x=1",
			"https://WIKI.example/private", "https://wiki.example:443/private", "https://wiki.example/private#x", "file:///listed/a%2fb"},
		"a denied template": {"file:/vault/a", "file://localhost/vault/a", "file:///vault/a#x", "file:///vault/a?x=1"},
	} {
		for _, uri := range uris {
			if o := (&Engine{}).ReadResource(ctx, newRequest("direct", s), &mcp.ReadResourceParams{URI: uri}); o.Forward != nil {
				t.Errorf("%s is passed by %q, forwarded as %s", what, uri, asJSON(o.Forward.Params))
			}
			if o := (&Engine{}).ReadResource(ctx, mux, &mcp.ReadResourceParams{URI: "a+" + uri}); o.Forward != nil {
				t.Errorf("%s is passed on the mux by %q", what, "a+"+uri)
			}
		}
	}
	// The same on what a server returns: an entry for the denied file under
	// another spelling is not passed to the client.
	o := readReply(t, s, "file:///ok/a", `{"contents":[{"uri":"file:///ok/a","text":"A"},{"uri":"file:///secrets/key","text":"S0"},{"uri":"file:/secrets/key","text":"S1"},{"uri":"file://localhost/secrets/key","text":"S2"}]}`)
	if got := outcomeText(o); strings.Contains(got, "S1") || strings.Contains(got, "S2") {
		t.Errorf("a read result carried the denied file under another spelling: %s", got)
	}
}

// A denied URI is not reached through an alias of its host or path: the long
// form of an IPv6 address, a Unicode host for its punycode, a percent-encoded
// or full-width host, an IP address written as one number, a port with a
// leading zero, or a path left unencoded. This holds for a deny by prefix, by
// exact URI, by the name of a listed resource and by template, on the direct
// route and on the mux.
func TestAHostOrPathAliasOfADeniedURIIsRefused(t *testing.T) {
	pairs := [][2]string{
		{"https://[::1]/private/x", "https://[0:0:0:0:0:0:0:1]/private/x"},
		{"https://xn--bcher-kva.example/private/x", "https://bücher.example/private/x"},
		{"https://wiki.example/private/x", "https://%77iki.example/private/x"},
		{"https://127.0.0.1/private/x", "https://2130706433/private/x"},
		{"https://wiki.example/private/x", "https://wiki.example:0443/private/x"},
		{"https://wiki.example/private/x", "https://ｗｉｋｉ.example/private/x"},
		{"https://wiki.example/caf%C3%A9/x", "https://wiki.example/café/x"},
	}
	for _, pair := range pairs {
		for _, kind := range []string{"prefix", "exact", "hidden", "template"} {
			t.Run(kind+"/"+pair[1], func(t *testing.T) {
				cat := Catalog{}
				rule := Rule{Hook: "resources/list.post", When: "resource.uri == " + fmt.Sprintf("%q", pair[0]), Then: map[string]string{KeyDecision: "'deny'"}}
				switch kind {
				case "prefix":
					rule.When = "resource.uri.startsWith(" + fmt.Sprintf("%q", strings.TrimSuffix(pair[0], "x")) + ")"
				case "hidden":
					cat.Resources = []*mcp.Resource{{URI: pair[0], Name: "secret"}}
					rule.When = "resource.?name.orValue('') == 'secret'"
				case "template":
					cat.Templates = []*mcp.ResourceTemplate{{URITemplate: strings.TrimSuffix(pair[0], "x") + "{id}", Name: "secret"}}
					rule.Hook, rule.When = "resources/templates/list.post", "true"
				}
				s := newServer(t, cat, rule)
				for _, route := range []string{"direct", "mux"} {
					r := newRequest("direct", s)
					r.Route = route
					uri := func(u string) string {
						if route == "mux" {
							return "a+" + u
						}
						return u
					}
					e := &Engine{}
					if o := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: uri(pair[0])}); o.Forward != nil {
						t.Fatalf("control forwarded: %s", outcomeText(o))
					}
					if o := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: uri(pair[1])}); o.Forward != nil {
						t.Errorf("%s alias forwarded: %s", route, asJSON(o.Forward.Params))
					}
				}
			})
		}
	}
}

// The URI checks never panic, and a slug put in front of a URI comes off
// again with the URI unchanged.
func FuzzURIPrefixRoundTrip(f *testing.F) {
	for _, s := range []string{"file:///a", "git+ssh://x", "skill://a/../private/SKILL.md", "file:///a%2Fb", "file:///a?q=/../x", "urn:example:a"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, u string) {
		if len(u) > 4096 {
			t.Skip()
		}
		_ = nonCanonical(u)
		_ = canonicalURI(u)
		if strings.IndexByte(u, ':') <= 0 {
			return
		}
		slug, rest, ok := SplitURI(PrefixURI("docs", u))
		if !ok || slug != "docs" || rest != u {
			t.Fatalf("prefix round trip lost identity: %q => %q %q %v", u, slug, rest, ok)
		}
	})
}

// Two resources a server lists under spellings of one URI cannot be told
// apart once a client asks in canonical form. A rule may hide one and not
// the other, so both are hidden, whatever order the server lists them in.
func TestTwoSpellingsOfOneURIAreBothHidden(t *testing.T) {
	ctx := context.Background()
	for _, order := range [][2]string{{"https://WIKI.example/doc", "https://wiki.example/doc"}, {"https://wiki.example/doc", "https://WIKI.example/doc"}} {
		names := map[string]string{"https://WIKI.example/doc": "open", "https://wiki.example/doc": "secret"}
		cat := Catalog{Resources: []*mcp.Resource{{URI: order[0], Name: names[order[0]]}, {URI: order[1], Name: names[order[1]]}}}
		s := newServer(t, cat, Rule{Hook: "resources/list.post", When: "resource.?name.orValue('') == 'secret'", Then: map[string]string{KeyDecision: "'deny'"}})
		e, r := &Engine{}, newRequest("direct", s)
		listed := e.ListResources(ctx, r)
		if got := asJSON(listed.Result); strings.Contains(got, "wiki.example/doc") {
			t.Errorf("%v: listed %s", order, got)
		}
		if !strings.Contains(strings.Join(listed.Issues, "\n"), "collision") {
			t.Errorf("%v: no issue records the collision: %v", order, listed.Issues)
		}
		if o := e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: "https://wiki.example/doc"}); o.Forward != nil {
			t.Errorf("%v: forwarded as %s", order, asJSON(o.Forward.Params))
		}
	}
}

// A denied template is matched in canonical form. A template the server
// writes with an upper-case host still covers the URIs a client can send.
func TestADeniedTemplateWithAnOddSpellingStillDenies(t *testing.T) {
	ctx := context.Background()
	cat := Catalog{Templates: []*mcp.ResourceTemplate{{URITemplate: "https://WIKI.example/secret/{id}", Name: "secret"}}}
	for what, rule := range map[string]Rule{
		"denied by name":   {Hook: "resources/templates/list.post", When: "template.name == 'secret'", Then: map[string]string{KeyDecision: "'deny'"}},
		"denied by prefix": {Hook: "resources/templates/list.post", When: "template.uriTemplate.startsWith('https://wiki.example/secret/')", Then: map[string]string{KeyDecision: "'deny'"}},
	} {
		s := newServer(t, cat, rule)
		e, r := &Engine{}, newRequest("direct", s)
		if o := e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: "https://wiki.example/secret/1"}); o.Forward != nil {
			t.Errorf("%s: a URI the template covers was forwarded", what)
		}
		if got := asJSON(e.ListTemplates(ctx, r).Result); strings.Contains(got, "secret") {
			t.Errorf("%s: listed %s", what, got)
		}
	}
	s := newServer(t, cat)
	if got := asJSON((&Engine{}).ListTemplates(ctx, newRequest("direct", s)).Result); !strings.Contains(got, `"https://wiki.example/secret/{id}"`) {
		t.Errorf("a template is not listed in canonical form: %s", got)
	}
}

// A denied template covers every spelling of the URIs it spells. Its fixed
// text may be written oddly, carry an expression in its host or port, or
// end in a query or a slash; a client reads the canonical URI, and a server
// that folds the difference would hand over the same thing. The read is
// refused, and the URI is dropped from another read's reply.
func TestADeniedTemplateCoversEverySpellingOfWhatItSpells(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct{ name, template, uri string }{
		{"escaped letter", "https://wiki.example/%73ecret/{id}", "https://wiki.example/secret/1"},
		{"dot segment", "https://wiki.example/a/../secret/{id}", "https://wiki.example/secret/1"},
		{"doubled slash", "https://wiki.example//secret/{id}", "https://wiki.example/secret/1"},
		{"letter not escaped", "https://wiki.example/café/{id}", "https://wiki.example/caf%C3%A9/1"},
		{"escape in lower case", "https://wiki.example/a%2fb/{id}", "https://wiki.example/a%2Fb/1"},
		{"file with one slash", "file:/secret/{id}", "file:///secret/1"},
		{"expression in the host", "https://WIKI.{domain}/secret/{id}", "https://wiki.example/secret/1"},
		{"expression in the port", "https://WIKI.example:{port}/secret/{id}", "https://wiki.example:8443/secret/1"},
		{"port that may be the default", "https://wiki.example:{port}/secret/{id}", "https://wiki.example/secret/1"},
		{"climbs after an expression", "https://wiki.example/a/{id}/../../secret", "https://wiki.example/secret"},
		{"trailing slash taken away", "https://wiki.example/secret/{id}/", "https://wiki.example/secret/1"},
		{"query changed", "https://wiki.example/secret/{id}?view=private", "https://wiki.example/secret/1?view=raw"},
		{"query taken away", "https://wiki.example/secret/{id}?view=private", "https://wiki.example/secret/1"},
	} {
		for _, route := range []string{"direct", "mux"} {
			t.Run(tc.name+"/"+route, func(t *testing.T) {
				s := newServer(t, Catalog{Templates: []*mcp.ResourceTemplate{{Name: "private", URITemplate: tc.template}}},
					Rule{Hook: "resources/templates/list.post", When: "template.name == 'private'", Then: map[string]string{KeyDecision: "'deny'"}})
				e, r := &Engine{}, newRequest(route, s)
				at := func(u string) string {
					if route == "mux" {
						return PrefixURI(s.Slug, u)
					}
					return u
				}
				if o := e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: at(tc.uri)}); o.Forward != nil {
					t.Errorf("%s was forwarded under the denied %s", tc.uri, tc.template)
				}
				p := &mcp.ReadResourceParams{URI: at("https://docs.example/public")}
				first := e.ReadResource(ctx, r, p)
				if first.Forward == nil {
					t.Fatalf("a read the template does not cover was refused: %s", outcomeText(first))
				}
				raw, _ := json.Marshal(map[string]any{"contents": []any{map[string]any{"uri": tc.uri, "text": "SECRET"}}})
				if got := asJSON(e.UpstreamReadReply(ctx, r, first.Call, p, raw).Result); strings.Contains(got, "SECRET") {
					t.Errorf("another read's reply carried %s: %s", tc.uri, got)
				}
			})
		}
	}
}

// The deny of an odd template errs wide, but not without bound: it covers
// the canonical fixed text before the first expression, or the host's
// fixed start when the expression is in the host. It says so in an issue.
func TestAnOddDeniedTemplateDeniesWideButNotEverything(t *testing.T) {
	ctx := context.Background()
	for template, outside := range map[string]string{
		"https://wiki.example/%73ecret/{id}":  "https://wiki.example/public/1",
		"https://WIKI.{domain}/secret/{id}":   "https://docs.example/secret/1",
		"https://wiki.example:{port}/s/{id}":  "https://docs.example/s/1",
		"https://wiki.example/a/{id}/../../b": "https://docs.example/b",
	} {
		s := newServer(t, Catalog{Templates: []*mcp.ResourceTemplate{{Name: "private", URITemplate: template}}},
			Rule{Hook: "resources/templates/list.post", When: "template.name == 'private'", Then: map[string]string{KeyDecision: "'deny'"}})
		e, r := &Engine{}, newRequest("direct", s)
		if o := e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: outside}); o.Forward == nil {
			t.Errorf("%s: %s, outside its reach, was refused: %s", template, outside, outcomeText(o))
		}
		if got := fmt.Sprint(e.ListResources(ctx, r).Issues); !strings.Contains(got, "the deny covers everything under") {
			t.Errorf("%s: no issue says how far the deny reaches: %s", template, got)
		}
	}
	for template, want := range map[string]string{
		"https://wiki.example/%73ecret/{id}":       "https://wiki.example/secret/",
		"https://wiki.example/se%63ret-{id}":       "https://wiki.example/",
		"file:/secret/{id}":                        "file:///secret/",
		"https://WIKI.{domain}/x":                  "https://wiki.",
		"https://WIKI.example:{port}/x":            "https://wiki.example",
		"https://wiki.example/a/{id}/../../b":      "https://wiki.example",
		"https://wiki.example//s/{id}?view=%7a{q}": "https://wiki.example/s/",
	} {
		if got := denyPrefix(canonicalTemplate(template)); got != want {
			t.Errorf("denyPrefix(%s) = %s, want %s", template, got, want)
		}
	}
}

// An allowed template whose fixed text is not canonical is not listed, as a
// resource is not: no URI a client may send could be matched against it.
// Forms the gateway does not parse, and an expression in the host of a
// scheme whose host is the server's own text, are listed as before.
func TestAnOddTemplateIsNotListed(t *testing.T) {
	ctx := context.Background()
	s := newServer(t, Catalog{Templates: []*mcp.ResourceTemplate{
		{Name: "odd", URITemplate: "https://wiki.example/%73ecret/{id}"},
		{Name: "host", URITemplate: "https://{tenant}.wiki.example/{id}"},
		{Name: "plain", URITemplate: "https://wiki.example/doc/{id}?view=raw"},
		{Name: "level3", URITemplate: "https://wiki.example{/path}"},
		{Name: "opaque", URITemplate: "confluence://{space}/{page}"},
	}})
	o := (&Engine{}).ListTemplates(ctx, newRequest("direct", s))
	got := asJSON(o.Result)
	for _, gone := range []string{"%73ecret", "{tenant}"} {
		if strings.Contains(got, gone) {
			t.Errorf("an odd template was listed: %s", got)
		}
	}
	for _, kept := range []string{"doc/{id}?view=raw", "{/path}", "confluence://{space}/{page}"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%s is not listed: %s", kept, got)
		}
	}
	if issues := fmt.Sprint(o.Issues); strings.Count(issues, "is not listed: its spelling is not canonical") != 2 {
		t.Errorf("issues: %s", issues)
	}
}

// A denied template covers what it spells, not what a server makes of a
// value. A server that expands id=".." in file:///public/{id}/secret
// builds a path that folds to file:///secret, but no client can send the
// unfolded path, and file:///secret is not a URI the template spells. It is
// an ordinary read, refused only if a rule denies file:///secret.
func TestADeniedTemplateCoversWhatItSpellsNotWhatAServerMakesOfAValue(t *testing.T) {
	ctx := context.Background()
	for _, template := range []string{"file:///public/{id}/secret", "file:///public/{+path}"} {
		s := newServer(t, Catalog{Templates: []*mcp.ResourceTemplate{{Name: "private", URITemplate: template}}},
			Rule{Hook: "resources/templates/list.post", When: "template.name == 'private'", Then: map[string]string{KeyDecision: "'deny'"}})
		e, r := &Engine{}, newRequest("direct", s)
		if o := e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: "file:///secret"}); o.Forward == nil {
			t.Errorf("%s: file:///secret was refused: %s", template, outcomeText(o))
		}
		if o := e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: "file:///public/../secret"}); o.Forward != nil {
			t.Errorf("%s: a dot segment was forwarded", template)
		}
	}
}

// resource.upstream.uri is the server's own spelling on the list hook too,
// so a rule can be written against exactly what the server sent.
func TestUpstreamURIIsTheServersSpellingOnTheListHook(t *testing.T) {
	ctx := context.Background()
	cat := Catalog{Resources: []*mcp.Resource{{URI: "https://WIKI.example/private/x", Name: "x"}}}
	s := newServer(t, cat,
		Rule{Hook: "resources/list.post", When: "resource.upstream.uri == 'https://WIKI.example/private/x'", Then: map[string]string{KeyDecision: "'deny'"}})
	e, r := &Engine{}, newRequest("direct", s)
	if got := asJSON(e.ListResources(ctx, r).Result); strings.Contains(got, "/private/") {
		t.Errorf("a deny on upstream.uri did not hide the resource: %s", got)
	}
	if o := e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: "https://wiki.example/private/x"}); o.Forward != nil {
		t.Error("the hidden resource was read")
	}
}

// An escaped dot is a dot to a URL parser, so it is folded before the path
// is cleaned, and the canonical form is stable.
func TestEscapedDotSegmentsAreFolded(t *testing.T) {
	for odd, want := range map[string]string{
		"https://h/a/%2e%2e/secret": "https://h/secret",
		"https://h/a/%2E%2E/b/":     "https://h/b/",
		"https://h/.%2e/x":          "https://h/x",
		"file:///a/%2e/b":           "file:///a/b",
	} {
		got := canonicalURI(odd)
		if got != want || canonicalURI(got) != got || nonCanonical(got) {
			t.Errorf("canonicalURI(%s) = %s, want %s, stable and accepted", odd, got, want)
		}
	}
}

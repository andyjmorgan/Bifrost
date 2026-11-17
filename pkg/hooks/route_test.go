package hooks

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Which server a URI belongs to on the mux, and what happens when that cannot
// be told.

// Every valid form of URI is forwarded as it was written, on both routes: a
// urn, a data URI, a custom scheme, a scheme with a "+", escapes that must
// stay escaped, and a query that holds another URL. A spelling that is not
// canonical is refused. On the mux a URI with no slug in front goes to the
// one server that lists it, and is refused as ambiguous when two do.
func TestValidURIFormsAreForwardedAndTheMuxFallbackDoesNotGuess(t *testing.T) {
	e, s := &Engine{}, newServer(t, Catalog{})
	for _, route := range []string{"direct", "mux"} {
		r := newRequest("direct", s)
		r.Route = route
		for _, uri := range []string{"urn:example:a", "data:text/plain,a%20b", "confluence://ENG/home", "git+ssh://host/repo", "file:///a%2Fb", "file:///a%25b", "web://fetch?url=https://example.com/a//b"} {
			p := uri
			if route == "mux" {
				p = "a+" + p
			}
			o := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: p})
			if o.Forward == nil || o.Forward.Params.(*mcp.ReadResourceParams).URI != uri {
				t.Errorf("valid URI %s on %s: %s", uri, route, outcomeText(o))
			}
		}
		for _, uri := range []string{"file:///a%", "file:///a%0", "file:///a%GG", "file:///a%2f", "file:///a%41", "file:///a/../b", "file:///a//b", "file:/a", "https://host:443/a"} {
			if route == "mux" {
				uri = "a+" + uri
			}
			if o := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: uri}); o.Forward != nil {
				t.Errorf("noncanonical URI forwarded: %s", uri)
			}
		}
	}
	a := newServer(t, Catalog{Resources: []*mcp.Resource{{URI: "docs://h/x", Name: "x"}}})
	b := newServer(t, a.Catalog)
	b.Slug = "b"
	r := newRequest("direct", a)
	r.Route, r.Servers = "mux", []*Server{a, b}
	if o := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: "docs://h/x"}); o.Forward != nil || o.Fallback == nil || o.Fallback.Outcome != "ambiguous" {
		t.Errorf("fallback ambiguity guessed: %s", outcomeText(o))
	}
	r.Servers = []*Server{a}
	if o := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: "docs://h/x"}); o.Forward == nil || o.Forward.Slug != "a" {
		t.Errorf("unique exact fallback failed: %s", outcomeText(o))
	}
}

// On the mux git+ssh://host/repo can be server git and ssh://host/repo, or a
// URI of the scheme git+ssh that other servers list. When two servers list it
// the gateway reports the ambiguity and does not fall back on the slug
// reading.
func TestTwoOwnersOfANativeURIAreAmbiguous(t *testing.T) {
	e, r, p := unlistedRead(t)
	r.Route, r.Servers[0].Slug = "mux", "git"
	p.URI = "git+ssh://host/repo"
	for _, slug := range []string{"b", "c"} {
		r.Servers = append(r.Servers, &Server{Slug: slug, Catalog: Catalog{Resources: []*mcp.Resource{{Name: "repo", URI: p.URI}}}})
	}
	o := e.ReadResource(context.Background(), r, p)
	if o.Forward != nil || o.Error == nil || !strings.Contains(o.Error.Message, "Ambiguous URI") {
		t.Fatalf("two native-URI owners must yield ambiguity, not a guessed prefix: %+v", o)
	}
}

// On the mux a URI that the named server does not list can be read two ways:
// a+docs://h/x is server a and docs://h/x, or a URI of the scheme a+docs that
// another server owns, as git+ssh://host/repo is. While another server's
// catalog is unavailable the gateway cannot tell, and it does not guess. This
// is by design; the scenario "native scheme fallback must retain unavailable
// catalog refusal" holds the engine to it as well. A URI the named server
// lists is not held up.
func TestAnOutageHoldsUpOnlyWhatCannotBeTold(t *testing.T) {
	a, b := newServer(t, Catalog{Resources: []*mcp.Resource{{URI: "docs://h/listed", Name: "l"}}}), newServer(t, Catalog{})
	b.Slug, b.Unavailable = "b", true
	r := newRequest("direct", a)
	r.Route, r.Servers = "mux", []*Server{a, b}
	if o := (&Engine{}).ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: "a+docs://h/listed"}); o.Forward == nil || o.Forward.Slug != "a" {
		t.Errorf("a listed read on a was held up by b: %s", outcomeText(o))
	}
	o := (&Engine{}).ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: "a+docs://h/unlisted"})
	if o.Forward != nil || !strings.Contains(outcomeText(o), "b unavailable") {
		t.Errorf("an unlisted read with b unavailable: %s", outcomeText(o))
	}
}

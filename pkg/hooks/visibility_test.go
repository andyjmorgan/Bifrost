package hooks

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// What a list rule hides is hidden from a read too: a hidden resource and its
// variants, a denied template, and a URI the server does not list.

// A hidden resource also covers the same URI with a trailing slash or a
// query, when the server does not list that variant itself. The variant is
// not readable, and an entry for it in another read's result is not passed
// on.
func TestAHiddenResourceCoversItsUnlistedVariants(t *testing.T) {
	for _, tc := range []struct {
		name, hidden, alias string
	}{
		{"authority-root", "https://wiki.example", "https://wiki.example/"},
		{"unlisted-query", "docs://h/private", "docs://h/private?view=raw"},
		{"unlisted-slash", "docs://h/private", "docs://h/private/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cat := Catalog{Resources: []*mcp.Resource{{URI: tc.hidden, Name: "secret"}}}
			s := newServer(t, cat, Rule{Hook: "resources/list.post", When: "resource.?name.orValue('') == 'secret'", Then: map[string]string{KeyDecision: "'deny'"}})
			e, r := &Engine{}, newRequest("direct", s)
			if o := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: tc.hidden}); o.Forward != nil {
				t.Fatal("control forwarded")
			}
			if o := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: tc.alias}); o.Forward != nil {
				t.Errorf("hidden alias forwarded: %s", outcomeText(o))
			}
			o := readReply(t, s, "docs://h/public", `{"contents":[{"uri":`+fmt.Sprintf("%q", tc.alias)+`,"text":"SECRET"}]}`)
			if strings.Contains(asJSON(o.Result), "SECRET") {
				t.Errorf("hidden alias returned: %s", outcomeText(o))
			}
		})
	}
}

// A hidden resource covers the same URI with a query or a trailing slash only
// when the server does not list that URI itself. A resource the server lists
// is judged by the rules on its own: here the alias has another name and no
// rule hides it, so it is listed and readable, and the listing and the read
// agree. This is by design.
func TestAListedAliasIsJudgedOnItsOwn(t *testing.T) {
	for _, alias := range []string{"docs://h/private?view=raw", "docs://h/private/"} {
		cat := Catalog{Resources: []*mcp.Resource{{URI: "docs://h/private", Name: "secret"}, {URI: alias, Name: "alias"}}}
		s := newServer(t, cat, Rule{Hook: "resources/list.post", When: "resource.?name.orValue('') == 'secret'", Then: map[string]string{KeyDecision: "'deny'"}})
		e, r := &Engine{}, newRequest("direct", s)
		listed := asJSON(e.ListResources(context.Background(), r).Result)
		if strings.Contains(listed, `"docs://h/private"`) || !strings.Contains(listed, alias) {
			t.Errorf("listing: %s", listed)
		}
		if o := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: alias}); o.Forward == nil {
			t.Errorf("%s is listed and was not readable", alias)
		}
		// Hiding the alias is one more condition in the rule.
		s = newServer(t, cat, Rule{Hook: "resources/list.post", When: "resource.uri.startsWith('docs://h/private')", Then: map[string]string{KeyDecision: "'deny'"}})
		if o := (&Engine{}).ReadResource(context.Background(), newRequest("direct", s), &mcp.ReadResourceParams{URI: alias}); o.Forward != nil {
			t.Errorf("%s passed a prefix deny", alias)
		}
	}
}

// A denied template that starts with an expression, in a form the gateway
// cannot parse, still denies: a URI the template can produce is not
// forwarded.
func TestADeniedTemplateThatStartsWithAnExpressionStillDenies(t *testing.T) {
	e, r, p := unlistedRead(t, Rule{Hook: "resources/templates/list.post", When: "true", Then: map[string]string{"decision": "'deny'"}})
	// RFC 6570 expansion: base="file:///secret.txt", q="x".
	r.Servers[0].Catalog.Templates = []*mcp.ResourceTemplate{{Name: "any", URITemplate: "{+base}{?q}"}}
	p.URI = "file:///secret.txt?q=x"
	listed := e.ListTemplates(context.Background(), r)
	if listed.Error != nil || len(listed.Result.(*mcp.ListResourceTemplatesResult).ResourceTemplates) != 0 {
		t.Fatalf("setup: template was not hidden: %+v", listed)
	}
	o := e.ReadResource(context.Background(), r, p)
	if o.Forward != nil || o.Error == nil {
		t.Fatalf("URI produced by denied unsupported template forwarded: %+v", o)
	}
}

// On a read of a URI the server does not list, only the list rules that deny
// are run: there is no listing for a rule to change. A deny that reads a mark
// an earlier rule set, with its own default, does not see the mark there. A
// deny written against the URI itself covers both. This is by design, and the
// docs say so.
func TestAnUnlistedReadRunsOnlyTheListRulesThatDeny(t *testing.T) {
	ctx := context.Background()
	cat := Catalog{Resources: []*mcp.Resource{{URI: "file:///private/listed", Name: "x"}}}
	s := newServer(t, cat,
		Rule{Hook: "resources/list.post", When: "resource.uri.startsWith('file:///private/')", Then: map[string]string{"resource._meta.private": "true"}},
		Rule{Hook: "resources/list.post", When: "resource.?_meta.?private.orValue(false)", Then: map[string]string{KeyDecision: "'deny'"}},
	)
	if o := (&Engine{}).ReadResource(ctx, newRequest("direct", s), &mcp.ReadResourceParams{URI: "file:///private/listed"}); o.Forward != nil {
		t.Fatal("control: the listed resource was forwarded")
	}
	if o := (&Engine{}).ReadResource(ctx, newRequest("direct", s), &mcp.ReadResourceParams{URI: "file:///private/unlisted"}); o.Forward == nil {
		t.Error("the mark was seen on an unlisted URI; the docs say it is not")
	}
	s = newServer(t, cat, Rule{Hook: "resources/list.post", When: "resource.uri.startsWith('file:///private/')", Then: map[string]string{KeyDecision: "'deny'"}})
	for _, uri := range []string{"file:///private/listed", "file:///private/unlisted"} {
		if o := (&Engine{}).ReadResource(ctx, newRequest("direct", s), &mcp.ReadResourceParams{URI: uri}); o.Forward != nil {
			t.Errorf("%s passed a deny written against the URI", uri)
		}
	}
}

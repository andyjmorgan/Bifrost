package hooks

import (
	"context"
	"regexp"
	"strings"
	"sync"
)

// ---- URI templates ----------------------------------------------------------------

// uriTemplate is a compiled resource template. Supported forms are literal
// text plus simple {var} (one or more characters, no "/", "?" or "#") and
// reserved {+var} (may span "/"). Per RFC 6570 either may expand to an
// empty string. Every literal is matched exactly and the
// whole URI must match. Any other form is unsupported: it can never grant
// access, but a deny on it still applies to URIs under its literal prefix.
type uriTemplate struct {
	raw         string
	re          *regexp.Regexp // nil when unsupported
	prefix      string         // literal text before the first expression
	unsupported string         // why, when unsupported
}

var (
	tplMu    sync.Mutex
	tplCache = map[string]*uriTemplate{}
	exprRe   = regexp.MustCompile(`\{([^{}]*)\}`)
	varRe    = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

func compileTemplate(raw string) *uriTemplate {
	tplMu.Lock()
	defer tplMu.Unlock()
	if t, ok := tplCache[raw]; ok {
		return t
	}
	t := &uriTemplate{raw: raw, prefix: templatePrefix(raw)}
	var b strings.Builder
	b.WriteString("^")
	last := 0
	for _, m := range exprRe.FindAllStringSubmatchIndex(raw, -1) {
		b.WriteString(regexp.QuoteMeta(raw[last:m[0]]))
		expr := raw[m[2]:m[3]]
		switch {
		case strings.HasPrefix(expr, "+") && varRe.MatchString(expr[1:]):
			b.WriteString(`(.*)`)
		case varRe.MatchString(expr):
			b.WriteString(`([^/?#]*)`)
		default:
			t.unsupported = "expression {" + expr + "}"
		}
		last = m[1]
	}
	rest := raw[last:]
	if strings.ContainsAny(rest, "{}") {
		t.unsupported = "unbalanced braces"
	}
	b.WriteString(regexp.QuoteMeta(rest))
	b.WriteString("$")
	if t.unsupported == "" {
		t.re = regexp.MustCompile(b.String())
	}
	tplCache[raw] = t
	return t
}

// matches reports whether uri can be produced by a supported template.
func (t *uriTemplate) matches(uri string) bool { return t.re != nil && t.re.MatchString(uri) }

// mayCover reports whether a template could produce uri: an exact match for
// supported templates, the literal prefix for unsupported ones. Used only
// to apply denies, never to grant.
func (t *uriTemplate) mayCover(uri string) bool {
	if t.re != nil {
		return t.re.MatchString(uri)
	}
	return t.prefix != "" && strings.HasPrefix(uri, t.prefix)
}

// ---- resource visibility --------------------------------------------------------------

// resView is one server's resource visibility for one request. Listings,
// reads, directory children, returned content items and fallback
// candidates all use it, so they cannot disagree.
type resView struct {
	visibleRes   map[string]map[string]any // normalized URI -> exposed resource
	listedRes    map[string]bool           // normalized URI is in the upstream listing
	okTemplates  []*uriTemplate
	badTemplates []*uriTemplate // denied, failed modify or collided
	deniedRoots  []string
	skills       []Skill
	hits         []Hit
	issues       []string
}

func (e *Engine) resourceView(ctx context.Context, r Request, s *Server) *resView {
	v := &resView{visibleRes: map[string]map[string]any{}, listedRes: map[string]bool{}}
	c := r.context(s)
	var hits []Hit
	v.skills, v.deniedRoots, hits = e.skillView(ctx, r, s)
	v.hits = append(v.hits, hits...)
	for _, x := range s.Catalog.Resources {
		v.listedRes[normalizeURI(x.URI)] = true
	}
	ls, issues, hits := s.Rules.listItems(ctx, KindResource, "resources/filter", "resources/modify", "uri", resourceItems(s.Catalog.Resources), c, e.Limits.Workers)
	v.hits = append(v.hits, hits...)
	v.issues = append(v.issues, issues...)
	for _, l := range ls {
		v.visibleRes[normalizeURI(l.upstreamID)] = l.value
	}
	ts, issues, hits := s.Rules.listItems(ctx, KindTemplate, "templates/filter", "templates/modify", "uriTemplate", templateItems(s.Catalog.Templates), c, e.Limits.Workers)
	v.hits = append(v.hits, hits...)
	v.issues = append(v.issues, issues...)
	ok := map[string]bool{}
	for _, t := range ts {
		ok[t.upstreamID] = true
	}
	for _, t := range s.Catalog.Templates {
		ct := compileTemplate(t.URITemplate)
		if ok[t.URITemplate] {
			v.okTemplates = append(v.okTemplates, ct)
		} else {
			v.badTemplates = append(v.badTemplates, ct)
		}
	}
	return v
}

// check decides one URI. A read is allowed unless something denies it: a
// denied skill root, a denied template that could cover the URI, a listed
// resource that a filter hid, or a resources/filter rule. A URI the server
// never listed is allowed too, because servers hand out URIs they do not
// list, in tool results and through templates the gateway cannot parse.
// resources/filter runs on such a URI with only `uri` to read, so a rule
// that needs `name` or `mimeType` fails there and the read is refused.
// It returns the item rules see for the URI.
func (e *Engine) checkURI(ctx context.Context, r Request, s *Server, v *resView, uri string) (map[string]any, bool, []Hit) {
	if nonCanonical(uri) {
		return nil, false, nil
	}
	if deniedUnder(uri, v.deniedRoots) {
		return nil, false, nil
	}
	n := normalizeURI(uri)
	for _, t := range v.badTemplates {
		if t.mayCover(uri) {
			return nil, false, nil
		}
	}
	if v.listedRes[n] {
		item, visible := v.visibleRes[n]
		if !visible {
			return nil, false, nil // hidden by a filter, a failed modify or a collision
		}
		return item, true, nil
	}
	probe := map[string]any{"uri": uri}
	it := &item{kind: KindResource, id: uri, view: NewView(probe), label: uri, bare: true}
	o := s.Rules.run(ctx, "resources/filter", uri, it.resolve, r.context(s).vars(), func(string, any) error { return nil })
	if o.denied {
		return nil, false, o.hits
	}
	return probe, true, o.hits
}

// claims reports whether this server gives the gateway a reason to think a
// URI is its own: it lists it, a template could produce it, or a skill
// holds it. Fallback resolution uses it to route a URI that carries no slug.
// It is about routing, not permission: on a route that already names the
// server, a URI needs no claim to be read.
func (v *resView) claims(uri string) bool {
	if v.listedRes[normalizeURI(uri)] {
		return true
	}
	for _, t := range v.okTemplates {
		if t.mayCover(uri) {
			return true
		}
	}
	for _, sk := range v.skills {
		if underRoot(uri, sk.Root()) {
			return true
		}
	}
	return false
}

// owned describes this upstream's URIs for slugUris: visible exact
// resources, supported templates and skill roots.
func (v *resView) owned() map[string]any {
	var exact, patterns, roots []any
	for u := range v.visibleRes {
		exact = append(exact, u)
	}
	for _, t := range v.okTemplates {
		if t.re != nil { // unsupported templates never grant or rewrite
			patterns = append(patterns, t.re.String())
		}
	}
	for _, sk := range v.skills {
		roots = append(roots, sk.Root())
	}
	return map[string]any{"exact": exact, "patterns": patterns, "roots": roots}
}

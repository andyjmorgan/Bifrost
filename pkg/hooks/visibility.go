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

// mayCover reports whether a template could produce uri, for routing: an
// exact match for a template the gateway can parse, the literal prefix for
// one it cannot. A template with no literal prefix claims nothing.
func (t *uriTemplate) mayCover(uri string) bool {
	if t.re != nil {
		return t.re.MatchString(uri)
	}
	return t.prefix != "" && strings.HasPrefix(uri, t.prefix)
}

// denies reports whether a denied template covers uri. It errs on the side
// of the deny: a template the gateway cannot parse covers everything under
// its literal prefix, and when it starts with an expression, so that there
// is no literal prefix, it covers every URI. A deny is never silently lost.
func (t *uriTemplate) denies(uri string) bool {
	if t.re != nil {
		return t.re.MatchString(uri)
	}
	return strings.HasPrefix(uri, t.prefix)
}

// ---- resource visibility --------------------------------------------------------------

// manifest is a skill's list of files. ok is false for a dynamic skill,
// which has none.
type manifest struct {
	files []SkillResource
	ok    bool
}

// manifestOf returns a visible skill's manifest.
func (v *resView) manifestOf(sk Skill) ([]SkillResource, bool) {
	m := v.manifests[sk.URI]
	return m.files, m.ok
}

// resView is one server's resource visibility for one request. Listings,
// reads, directory children, returned content items and fallback
// candidates all use it, so they cannot disagree.
type resView struct {
	visibleRes   map[string]map[string]any // normalized URI -> exposed resource
	listedRes    map[string]bool           // normalized URI is in the upstream listing
	hiddenBare   map[string]bool           // a listed resource that is not visible, without its query or trailing slash
	originalRes  map[string]map[string]any // normalized URI -> the resource as the upstream sent it
	okTemplates  []*uriTemplate
	badTemplates []*uriTemplate // denied, failed a change or collided
	deniedRoots  []string
	skills       []Skill
	manifests    map[string]manifest   // each visible skill's manifest, read once, by the skill's URI
	plans        map[string]*skillPlan // each skill's rewrite plan, made once, by the skill's URI
	hits         []Hit
	issues       []string
}

// resourceView returns a server's resource visibility for this request,
// working it out the first time it is asked for.
func (e *Engine) resourceView(ctx context.Context, r Request, s *Server) *resView {
	m, _ := ctx.Value(memoKey{}).(*memo)
	if m == nil {
		return e.buildResourceView(ctx, r, s)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.views[s]; ok {
		return v
	}
	v := e.buildResourceView(ctx, r, s)
	m.views[s] = v
	return v
}

func (e *Engine) buildResourceView(ctx context.Context, r Request, s *Server) *resView {
	v := &resView{visibleRes: map[string]map[string]any{}, listedRes: map[string]bool{}, originalRes: map[string]map[string]any{}, hiddenBare: map[string]bool{},
		manifests: map[string]manifest{}, plans: map[string]*skillPlan{}}
	c := r.context(s)
	var hits []Hit
	v.skills, v.deniedRoots, hits = e.skillView(ctx, r, s)
	v.hits = append(v.hits, hits...)
	for _, sk := range v.skills {
		files, ok := sk.manifest()
		v.manifests[sk.URI] = manifest{files: files, ok: ok}
	}
	for _, x := range s.Catalog.Resources {
		v.listedRes[normalizeURI(x.URI)] = true
		v.originalRes[normalizeURI(x.URI)] = toMap(x)
	}
	ls, issues, hits := s.Rules.listItems(ctx, KindResource, "resources/list.post", "uri", resourceItems(s.Catalog.Resources), c, e.Limits.Workers)
	v.hits = append(v.hits, hits...)
	v.issues = append(v.issues, issues...)
	for _, l := range ls {
		v.visibleRes[normalizeURI(l.upstreamID)] = l.value
	}
	for n := range v.listedRes {
		if _, visible := v.visibleRes[n]; !visible {
			v.hiddenBare[bare(n)] = true
		}
	}
	ts, issues, hits := s.Rules.listItems(ctx, KindTemplate, "resources/templates/list.post", "uriTemplate", templateItems(s.Catalog.Templates), c, e.Limits.Workers)
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
// resource that a rule hid, or a resources/list.post rule. A URI the server
// never listed is allowed too, because servers hand out URIs they do not
// list, in tool results and through templates the gateway cannot parse.
// Only the deny rules of resources/list.post run on such a URI, with only
// `uri` to read and nothing changed, so a deny rule that needs `name` or
// `mimeType` fails there and the read is refused. Rules that change a
// resource are skipped: there is no listed resource to change.
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
		if t.denies(uri) || t.denies(bare(uri)) {
			return nil, false, nil
		}
	}
	if v.listedRes[n] {
		item, visible := v.visibleRes[n]
		if !visible {
			return nil, false, nil // hidden by a rule, a failed change or a collision
		}
		return item, true, nil
	}
	// A hidden resource stays hidden with a query or a trailing slash
	// added, changed or taken away: a server that ignores either would hand
	// over the same thing. A URI the server lists itself was judged by the
	// rules on its own, above.
	if v.hiddenBare[normalizeURI(bare(uri))] {
		return nil, false, nil
	}
	probe := map[string]any{"uri": uri}
	it := &item{kind: KindResource, id: uri, view: NewView(probe), label: uri, bare: true}
	o := s.Rules.runIf(ctx, "resources/list.post", uri, it.resolve, r.context(s).roots("resources/list", nil), func(string, any) error { return nil },
		func(c *compiledRule) bool { return c.denies }, nil)
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

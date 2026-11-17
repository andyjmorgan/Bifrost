package hooks

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

var timeNow = time.Now

// slugRe is the slug grammar: a letter, then letters and digits. No "+",
// ".", "-" or "_", so "<slug>+<scheme>" splits at the first "+" and
// "<slug>__<name>" at the first "__".
var slugRe = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

// ValidSlug reports whether s matches the slug grammar.
func ValidSlug(s string) bool { return slugRe.MatchString(s) }

// PrefixURI writes a URI in the mux form <slug>+<uri>.
func PrefixURI(slug, uri string) string {
	if uri == "" {
		return uri
	}
	return slug + "+" + uri
}

// SplitURI splits a mux URI into slug and upstream URI. ok is false when the
// URI carries no slug prefix: no "+" before the first ":", or the part
// before "+" is not a valid slug.
func SplitURI(u string) (slug, rest string, ok bool) {
	colon := strings.IndexByte(u, ':')
	if colon < 0 {
		return "", u, false
	}
	plus := strings.IndexByte(u[:colon], '+')
	if plus <= 0 {
		return "", u, false
	}
	slug = u[:plus]
	if !ValidSlug(slug) {
		return "", u, false
	}
	return slug, u[plus+1:], true
}

// prefixResultURIs adds the slug prefix to every structured URI in a result:
// resource_link and embedded resource content blocks, read contents and
// directory children. Text is never touched.
func prefixResultURIs(m map[string]any, slug string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	for _, key := range []string{"content", "contents", "resources", "messages"} {
		if l, ok := m[key].([]any); ok {
			nl := make([]any, len(l))
			for i, x := range l {
				nl[i] = prefixBlock(x, slug)
			}
			out[key] = nl
		}
	}
	return out
}

func prefixBlock(x any, slug string) any {
	b, ok := x.(map[string]any)
	if !ok {
		return x
	}
	cp := make(map[string]any, len(b))
	for k, v := range b {
		cp[k] = v
	}
	if u, ok := cp["uri"].(string); ok && u != "" {
		cp["uri"] = PrefixURI(slug, u)
	}
	if r, ok := cp["resource"].(map[string]any); ok {
		cp["resource"] = prefixBlock(r, slug)
	}
	if c, ok := cp["content"].(map[string]any); ok { // prompt message content
		cp["content"] = prefixBlock(c, slug)
	}
	return cp
}

// ---- URI normalization ---------------------------------------------------------------

// normalizeURI puts a URI in one form for comparing it with listings and
// folders: the scheme in lower case, and "." and ".." segments and
// duplicate slashes resolved in the path. It never decodes: a percent
// escape such as %2F stays as written, so file:///a%2Fb is not file:///a/b.
// The gateway compares what the client sent with what the server listed; it
// does not translate between them. The query and fragment are left alone.
func normalizeURI(u string) string {
	scheme, rest, ok := strings.Cut(u, "://")
	if !ok {
		return u
	}
	host, p := rest, ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		host, p = rest[:i], rest[i:]
	}
	tail := ""
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p, tail = p[:i], p[i:]
	}
	if p != "" {
		trailing := strings.HasSuffix(p, "/") && len(p) > 1
		p = path.Clean(p)
		if trailing && p != "/" {
			p += "/"
		}
	}
	return strings.ToLower(scheme) + "://" + host + p + tail
}

// nonCanonical reports a URI that a server could read as another URI. A
// rule reads a URI as text, and a deny written against file:///secrets/
// matches only that spelling. A server that reads URIs with a URL parser
// takes FILE:///secrets/key, file:/secrets/key, file://localhost/secrets/key
// and file:///secrets/./key for the same file. The gateway does not try to
// guess what a server folds together: it refuses every spelling but one.
//
// The one spelling:
//   - no spaces, control characters or backslashes
//   - the scheme in lower case
//   - "//" after the scheme when the path starts with a slash, and always
//     for the schemes a URL parser treats as hierarchical
//   - for those schemes, the host in lower case, with no user, no trailing
//     dot, no empty or default port, and for file no "localhost"
//   - in the path: no "." or ".." segments, no empty segments, percent
//     escapes in upper-case hex, and none for a character that needs none
//   - no fragment: a fragment is the client's, and never names a resource
//
// The query is left alone. It often carries another URL, such as
// web://fetch?url=https://example.com/a, and that is not a path.
func nonCanonical(u string) bool {
	for i := 0; i < len(u); i++ {
		if c := u[i]; c <= ' ' || c == 0x7f || c == '\\' || c == '#' {
			return true
		}
	}
	i := strings.IndexAny(u, ":/?")
	if i <= 0 || u[i] != ':' {
		return false // no scheme: not a URI a server resolves; left to the lookup
	}
	scheme, rest := u[:i], u[i+1:]
	if scheme != strings.ToLower(scheme) {
		return true
	}
	if !strings.HasPrefix(rest, "//") {
		// file:/x and file:x are file:///x to a URL parser. A scheme with
		// no hierarchy, such as urn: or mailto:, has nothing to fold.
		return strings.HasPrefix(rest, "/") || hierarchical[scheme]
	}
	rest = rest[2:]
	if q := strings.IndexByte(rest, '?'); q >= 0 {
		rest = rest[:q]
	}
	host, p := rest, ""
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		host, p = rest[:j], rest[j:]
	}
	// Only for the schemes a URL parser knows does it fold the host. For
	// any other scheme, such as confluence://ENG/home, what follows "//" is
	// the server's own text and is compared as written.
	if hierarchical[scheme] {
		if host != strings.ToLower(host) || strings.ContainsRune(host, '@') || strings.HasSuffix(host, ".") {
			return true
		}
		if scheme == "file" && host == "localhost" {
			return true
		}
		if j := strings.LastIndexByte(host, ':'); j >= 0 && !strings.HasSuffix(host, "]") {
			if port := host[j+1:]; port == "" || port == defaultPort[scheme] {
				return true
			}
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	if strings.Contains(p, "//") {
		return true
	}
	for j := 0; j < len(p); j++ {
		if p[j] != '%' {
			continue
		}
		if j+2 >= len(p) {
			return true // a "%" with no two hex digits after it
		}
		hex := p[j+1 : j+3]
		b, err := hexByte(hex)
		if err != nil || isUnreserved(b) || hex != strings.ToUpper(hex) {
			return true
		}
	}
	return false
}

// hierarchical are the schemes a URL parser gives a "//" to when it is
// missing, and defaultPort the port it drops.
var (
	hierarchical = map[string]bool{"file": true, "http": true, "https": true, "ws": true, "wss": true, "ftp": true}
	defaultPort  = map[string]string{"http": "80", "ws": "80", "https": "443", "wss": "443", "ftp": "21"}
)

// bare is a URI without its query and without a trailing slash: the
// resource it names when a server ignores both. A listed resource a rule
// hid, and a template a rule denied, also cover the URIs whose bare form
// they cover.
func bare(u string) string {
	if q := strings.IndexByte(u, '?'); q >= 0 {
		u = u[:q]
	}
	if _, rest, ok := strings.Cut(u, "://"); ok && strings.Count(rest, "/") > 1 {
		u = strings.TrimSuffix(u, "/")
	}
	return u
}

func hexByte(s string) (byte, error) {
	var b byte
	for _, c := range []byte(s) {
		b <<= 4
		switch {
		case c >= '0' && c <= '9':
			b |= c - '0'
		case c >= 'a' && c <= 'f':
			b |= c - 'a' + 10
		case c >= 'A' && c <= 'F':
			b |= c - 'A' + 10
		default:
			return 0, fmt.Errorf("bad hex")
		}
	}
	return b, nil
}

func isUnreserved(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '-' || b == '.' || b == '_' || b == '~'
}

// underRoot reports whether uri equals root or sits beneath it, after
// normalization.
func underRoot(uri, root string) bool {
	u, r := normalizeURI(uri), normalizeURI(root)
	return u == r || strings.HasPrefix(u, strings.TrimSuffix(r, "/")+"/")
}

// ---- slugUris -------------------------------------------------------------------------

// muxContext is what slugUris needs, passed as the __mux variable: the
// route, the slug, and this upstream's owned URIs (exact URIs, supported
// template patterns and skill roots).
func muxContext(route, slug string, owned map[string]any) map[string]any {
	return map[string]any{"route": route, "slug": slug, "owned": owned}
}

var uriInText = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'<>()\[\]` + "`" + `]+`)

// rewriteOwnedURIs rewrites URIs in text that belong to this upstream into
// the mux form, using the same ownership a read uses. On 1:1 it returns the
// text unchanged.
func rewriteOwnedURIs(text string, mux any) string {
	m, _ := mux.(map[string]any)
	route, _ := m["route"].(string)
	slug, _ := m["slug"].(string)
	if (route != "mux" && route != "search") || slug == "" {
		return text
	}
	owned, _ := m["owned"].(map[string]any)
	strs := func(k string) []string {
		var out []string
		if l, ok := owned[k].([]any); ok {
			for _, x := range l {
				if s, ok := x.(string); ok {
					out = append(out, s)
				}
			}
		}
		return out
	}
	exact := map[string]bool{}
	for _, u := range strs("exact") {
		exact[u] = true
	}
	var pats []*regexp.Regexp
	for _, p := range strs("patterns") {
		if re, err := regexp.Compile(p); err == nil {
			pats = append(pats, re)
		}
	}
	roots := strs("roots")
	return uriInText.ReplaceAllStringFunc(text, func(u string) string {
		if _, _, already := SplitURI(u); already {
			return u
		}
		trimmed := strings.TrimRight(u, ".,;:!?")
		tail := u[len(trimmed):]
		owns := exact[normalizeURI(trimmed)]
		for _, re := range pats {
			if !owns && re.MatchString(trimmed) {
				owns = true
			}
		}
		if !owns && deniedUnder(trimmed, roots) {
			owns = true
		}
		if owns {
			return PrefixURI(slug, trimmed) + tail
		}
		return u
	})
}

// ---- fallback resolution ------------------------------------------------------------

// Candidate is one server that could own an unprefixed URI.
type Candidate struct {
	Slug  string
	URI   string // prefixed
	Exact bool
}

// AmbiguousError lists the candidates for an ambiguous URI.
func ambiguousError(uri string, cands []Candidate) *RPCError {
	var uris []string
	for _, c := range cands {
		uris = append(uris, c.URI)
	}
	sort.Strings(uris)
	return &RPCError{Code: CodeInvalidParams,
		Message: fmt.Sprintf("Ambiguous URI %s: matches %s. Retry with one of these.", uri, strings.Join(uris, ", ")),
		Data:    map[string]any{"candidates": uris}}
}

// templatePrefix is the literal part of a URI template before its first
// expression.
func templatePrefix(t string) string {
	if i := strings.IndexByte(t, '{'); i >= 0 {
		return t[:i]
	}
	return t
}

// matchTemplate reports whether uri can be produced by the template. It
// compares the literal prefix and, when the template has a literal suffix
// after its last expression, the suffix.
func matchTemplate(tpl, uri string) bool {
	pre := templatePrefix(tpl)
	if pre == "" || !strings.HasPrefix(uri, pre) {
		return false
	}
	if i := strings.LastIndexByte(tpl, '}'); i >= 0 && i+1 < len(tpl) {
		return strings.HasSuffix(uri, tpl[i+1:])
	}
	return true
}

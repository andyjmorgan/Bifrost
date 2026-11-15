package hooks

import (
	"fmt"
	"net/url"
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

// normalizeURI canonicalizes a URI for policy checks: percent-decoding of
// unreserved characters, "." and ".." segments resolved, duplicate slashes
// collapsed. Policy decisions (folder denies, listing matches) compare
// normalized forms so an encoded or dotted path cannot slip past a deny.
func normalizeURI(u string) string {
	scheme, rest, ok := strings.Cut(u, "://")
	if !ok {
		return u
	}
	host, p := rest, ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		host, p = rest[:i], rest[i:]
	}
	if dec, err := url.PathUnescape(p); err == nil {
		p = dec
	}
	if dec, err := url.PathUnescape(host); err == nil {
		host = dec
	}
	if p != "" {
		trailing := strings.HasSuffix(p, "/") && len(p) > 1
		p = path.Clean(p)
		if trailing && p != "/" {
			p += "/"
		}
	}
	return strings.ToLower(scheme) + "://" + host + p
}

// nonCanonical reports URIs whose path a careless upstream could resolve
// differently from the gateway: dot segments, empty segments, or
// percent-encoded unreserved characters. The gateway refuses them rather
// than guess.
func nonCanonical(u string) bool {
	_, rest, ok := strings.Cut(u, "://")
	if !ok {
		return false
	}
	for _, seg := range strings.Split(rest, "/")[1:] {
		if seg == "." || seg == ".." {
			return true
		}
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 && strings.Contains(rest[i:], "//") {
		return true
	}
	for i := 0; i+2 < len(u); i++ {
		if u[i] == '%' {
			if b, err := hexByte(u[i+1 : i+3]); err == nil && isUnreserved(b) {
				return true
			}
		}
	}
	return false
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

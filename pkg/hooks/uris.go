package hooks

import (
	"fmt"
	"net/netip"
	"path"
	"regexp"
	"strconv"
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
// and file:///secrets/./key for the same file.
//
// The gateway does not list the spellings it refuses: every review found
// one more. It says what a URI must look like, and refuses the rest.
//
//   - Before the query, every character is one a URI may hold as it is:
//     letters, digits and any of -._~!$&'()*+,;=:@/%[]. Anything else,
//     such as a brace or a letter that is not ASCII, must be
//     percent-encoded. There is no fragment.
//   - A percent escape is "%" and two upper-case hex digits, and never for
//     a letter, a digit or - . _ ~, which need none.
//   - The scheme is in lower case.
//   - "//" follows the scheme when the path starts with a slash, and always
//     for the schemes a URL parser treats as hierarchical.
//   - For those schemes the host is a name in lower case, a dotted IPv4
//     address or an IPv6 address in its shortest form, with no user, and a
//     port that is not empty, not the default and has no leading zero.
//     file has no "localhost".
//   - For any other scheme, such as confluence://ENG/home, what follows
//     "//" is the server's own text and is compared as written; only a port
//     is checked.
//   - The path has no "." or ".." segments and no empty segments.
func nonCanonical(u string) bool {
	// Nowhere, the query included: a space, a control character, a
	// backslash, which a URL parser drops or turns, or a fragment.
	for i := 0; i < len(u); i++ {
		if c := u[i]; c <= ' ' || c == 0x7f || c == '\\' || c == '#' {
			return true
		}
	}
	// The query is the server's to read and is not checked further. It
	// often carries another URL, such as web://fetch?url=https://x/a/../b.
	// A hidden resource and a denied template are matched without it.
	if q := strings.IndexByte(u, '?'); q >= 0 {
		u = u[:q]
	}
	for i := 0; i < len(u); i++ {
		if !uriChar[u[i]] {
			return true
		}
		if u[i] == '%' {
			if i+2 >= len(u) {
				return true
			}
			hex := u[i+1 : i+3]
			b, err := hexByte(hex)
			if err != nil || isUnreserved(b) || hex != strings.ToUpper(hex) {
				return true
			}
		}
	}
	i := strings.IndexAny(u, ":/?")
	if i <= 0 || u[i] != ':' {
		return false // no scheme: not a URI a server resolves; left to the lookup
	}
	scheme, rest := u[:i], u[i+1:]
	if !validScheme(scheme) {
		return true
	}
	if !strings.HasPrefix(rest, "//") {
		// file:/x and file:x are file:///x to a URL parser. A scheme with
		// no hierarchy, such as urn: or mailto:, has nothing to fold.
		return strings.HasPrefix(rest, "/") || hierarchical[scheme]
	}
	rest = rest[2:]
	authority, p := rest, ""
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		authority, p = rest[:j], rest[j:]
	}
	if !validAuthority(scheme, authority) {
		return true
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return strings.Contains(p, "//")
}

// uriChar marks the characters a URI may hold as they are.
var uriChar = func() (t [256]bool) {
	for c := 'a'; c <= 'z'; c++ {
		t[c], t[c-'a'+'A'] = true, true
	}
	for c := '0'; c <= '9'; c++ {
		t[c] = true
	}
	for _, c := range "-._~!$&'()*+,;=:@/?%[]" {
		t[c] = true
	}
	return t
}()

func validScheme(s string) bool {
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return s != ""
}

// validAuthority checks what follows "//": a host and perhaps a port.
func validAuthority(scheme, a string) bool {
	host, port, hasPort := a, "", false
	if strings.HasPrefix(a, "[") {
		end := strings.IndexByte(a, ']')
		if end < 0 {
			return false
		}
		host = a[:end+1]
		if tail := a[end+1:]; tail != "" {
			if tail[0] != ':' {
				return false
			}
			port, hasPort = tail[1:], true
		}
	} else if j := strings.LastIndexByte(a, ':'); j >= 0 && (hierarchical[scheme] || a[j+1:] == "" || isDigits(a[j+1:])) {
		// For another scheme, text after a colon that is not a number is
		// the server's own and no parser reads it as a port.
		host, port, hasPort = a[:j], a[j+1:], true
	}
	if hasPort {
		// A URL parser reads a port as a number: 0443 is 443, and an empty
		// one is none.
		n, err := strconv.Atoi(port)
		if err != nil || n > 65535 || port != strconv.Itoa(n) || port == defaultPort[scheme] {
			return false
		}
	}
	if !hierarchical[scheme] {
		return true
	}
	switch {
	case host == "":
		return scheme == "file" && !hasPort
	case scheme == "file" && host == "localhost":
		return false
	case strings.HasPrefix(host, "["):
		ip, err := netip.ParseAddr(host[1 : len(host)-1])
		return err == nil && ip.Is6() && !ip.Is4In6() && ip.Zone() == "" && ip.String() == host[1:len(host)-1]
	}
	// A name: labels of lower-case letters, digits and hyphens. That leaves
	// out a user ("@"), an escape ("%") and anything not ASCII, so a name
	// in another script comes as punycode.
	labels := strings.Split(host, ".")
	for _, l := range labels {
		if l == "" {
			return false // a leading, trailing or doubled dot
		}
		for _, c := range l {
			lower, digit := c >= 'a' && c <= 'z', c >= '0' && c <= '9'
			if !lower && !digit && c != '-' {
				return false
			}
		}
	}
	// A URL parser reads a host that ends in a number as an IPv4 address,
	// in any of several notations: 2130706433, 0x7f.1 and 127.1 are all
	// 127.0.0.1. Only the dotted form of four is accepted.
	if last := labels[len(labels)-1]; isDigits(last) || strings.HasPrefix(last, "0x") {
		ip, err := netip.ParseAddr(host)
		return err == nil && ip.Is4() && ip.String() == host
	}
	return true
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
	// https://wiki.example/ and https://wiki.example are one URI to a URL
	// parser, so the slash after the host goes too.
	if strings.HasSuffix(u, "/") && !strings.HasSuffix(u, "//") {
		u = u[:len(u)-1]
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

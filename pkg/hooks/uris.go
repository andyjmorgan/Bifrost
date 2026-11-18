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

// ---- canonical form -----------------------------------------------------------------

// canonicalURI gives a URI in the one spelling the gateway accepts, where
// it can work that out. It is what URIs are compared by.
//
// A client's URI has passed nonCanonical, so it comes back as it is. A
// server's own URIs have passed nothing. A server may list
// https://WIKI.example/private, and a URL parser reads that and
// https://wiki.example/private as one resource. Indexing the listing by
// canonical spelling keeps the two together: the rules that hide one hide
// the other.
//
// It folds what a URL parser folds and nothing else: the case of the
// scheme, for the schemes a parser knows the case of the host, a default
// port and leading zeros in a port, file's "localhost" and a missing "//",
// dot segments and doubled slashes, the form of percent escapes, and
// characters that must be escaped. It drops a fragment. It never decodes a
// reserved escape: file:///a%2Fb is not file:///a/b. A URI it cannot put in
// canonical form, such as one with a user or an IPv4 address written as one
// number, comes back in a form nonCanonical still refuses.
func canonicalURI(u string) string {
	if f := strings.IndexByte(u, '#'); f >= 0 {
		u = u[:f]
	}
	i := strings.IndexAny(u, ":/?")
	if i <= 0 || u[i] != ':' {
		return u
	}
	scheme, rest := strings.ToLower(u[:i]), u[i+1:]
	query := ""
	if q := strings.IndexByte(rest, '?'); q >= 0 {
		rest, query = rest[:q], rest[q:]
	}
	if !strings.HasPrefix(rest, "//") {
		switch {
		case scheme == "file" || (!hierarchical[scheme] && strings.HasPrefix(rest, "/")):
			rest = "///" + strings.TrimLeft(rest, "/") // file:/x and docs:/x have no host
		case hierarchical[scheme]:
			rest = "//" + strings.TrimLeft(rest, "/") // http:/host/x
		default:
			return scheme + ":" + escapes(rest) + query // urn:, mailto:, data:
		}
	}
	authority, p := rest[2:], ""
	if j := strings.IndexByte(authority, '/'); j >= 0 {
		authority, p = authority[:j], authority[j:]
	}
	// Escapes first: %2e%2e is ".." to a URL parser, so it must be a dot
	// segment by the time the path is cleaned.
	p = escapes(p)
	if p != "" {
		trailing := strings.HasSuffix(p, "/") && len(p) > 1
		p = path.Clean(p)
		if trailing && p != "/" {
			p += "/"
		}
	}
	return scheme + "://" + canonicalAuthority(scheme, authority) + p + query
}

// canonicalID is how an item is named to rules and in a listing: a
// resource's URI and a template's text in canonical form, anything else as
// it is. A rule written against https://wiki.example/ then also matches a
// resource the server lists as https://WIKI.example/…, and a client can ask
// for it in the one spelling the gateway accepts.
func canonicalID(kind ItemKind, id string) string {
	switch kind {
	case KindResource:
		return canonicalURI(id)
	case KindTemplate:
		return canonicalTemplate(id)
	}
	return id
}

// canonicalTemplate folds a URI template's scheme and, when it has no
// expression in it, its host and port, as canonicalURI does for a URI. A
// denied template is matched against URIs in canonical form, so its own
// fixed text must be in that form too, or https://WIKI.example/secret/{id}
// would not cover https://wiki.example/secret/1.
func canonicalTemplate(t string) string {
	i := strings.IndexAny(t, ":/?{")
	if i <= 0 || t[i] != ':' {
		return t
	}
	scheme, rest := strings.ToLower(t[:i]), t[i+1:]
	if !strings.HasPrefix(rest, "//") {
		return scheme + ":" + rest
	}
	a := rest[2:]
	end := strings.IndexAny(a, "/?{")
	if end >= 0 && a[end] == '{' {
		return scheme + ":" + rest // an expression in the host: nothing more to fold
	}
	authority, tail := a, ""
	if end >= 0 {
		authority, tail = a[:end], a[end:]
	}
	return scheme + "://" + canonicalAuthority(scheme, authority) + tail
}

// oddTemplate reports a template whose fixed text a URL parser could read
// as other text: a spelling that is not canonical, or, for a scheme whose
// host a parser folds, an expression in the host or port. The gateway
// cannot match a URI against such a template by text. Each expression
// stands in as a plain value of its kind.
func oddTemplate(t string) bool {
	if authorityExpr(t) {
		return true
	}
	return nonCanonical(exprRe.ReplaceAllStringFunc(t, func(e string) string {
		if len(e) < 3 {
			return "x"
		}
		switch e[1] {
		case '/':
			return "/x"
		case '.':
			return ".x"
		case '?':
			return "?x=x"
		case '&', ';':
			return string(e[1]) + "x=x"
		case '#':
			return ""
		}
		return "x"
	}))
}

// authorityExpr reports an expression in the host or port of a template
// whose scheme has a host a URL parser folds. An expression that starts the
// path or the query is not in the authority.
func authorityExpr(t string) bool {
	i := strings.IndexAny(t, ":/?{")
	if i <= 0 || t[i] != ':' || !hierarchical[strings.ToLower(t[:i])] || !strings.HasPrefix(t[i+1:], "//") {
		return false
	}
	a := t[i+3:]
	end := strings.IndexAny(a, "/?#{")
	if end < 0 || a[end] != '{' {
		return false
	}
	return end+1 >= len(a) || !strings.ContainsRune("/?&#", rune(a[end+1]))
}

// denyPrefix is how far the deny of an odd template reaches: everything
// under the canonical form of its fixed text, up to the last "/" before its
// first expression. When an expression is in the host or port, it is
// everything under the fixed start of the host. When fixed text after an
// expression climbs with "..", it is the whole host. It errs wide: a deny
// is never lost to a spelling.
func denyPrefix(t string) string {
	i := strings.IndexAny(t, ":/?{")
	if i <= 0 || t[i] != ':' {
		return ""
	}
	scheme := strings.ToLower(t[:i])
	if authorityExpr(t) {
		host := t[i+3:]
		host = host[:strings.IndexByte(host, '{')]
		if k := strings.LastIndexByte(host, '@'); k >= 0 {
			host = host[k+1:]
		}
		if j := strings.IndexByte(host, ':'); j >= 0 && !strings.HasPrefix(host, "[") {
			host = host[:j]
		}
		return scheme + "://" + strings.ToLower(host)
	}
	fixed := templatePrefix(t)
	if q := strings.IndexByte(fixed, '?'); q >= 0 {
		fixed = fixed[:q]
	}
	c := canonicalURI(fixed)
	hostEnd := len(c)
	if rest := c[len(scheme)+1:]; strings.HasPrefix(rest, "//") {
		if j := strings.IndexByte(rest[2:], '/'); j >= 0 {
			hostEnd = len(scheme) + 3 + j
		}
	} else {
		hostEnd = len(scheme) + 1
	}
	if climbs(t[len(templatePrefix(t)):]) {
		return c[:hostEnd]
	}
	if j := strings.LastIndexByte(c, '/'); j >= hostEnd {
		return c[:j+1]
	}
	return c
}

// climbs reports a ".." segment in a template's text after its first
// expression, before its query.
func climbs(rest string) bool {
	if q := strings.IndexByte(rest, '?'); q >= 0 {
		rest = rest[:q]
	}
	for _, seg := range strings.Split(escapes(exprRe.ReplaceAllString(rest, "/")), "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// bareTemplate is a template without its fixed query and trailing slash,
// as bare is for a URI. A query inside an expression is not fixed text.
func bareTemplate(t string) string {
	depth := 0
	for i := 0; i < len(t); i++ {
		switch t[i] {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case '?':
			if depth == 0 {
				t = t[:i]
				i = len(t)
			}
		}
	}
	if strings.HasSuffix(t, "/") && !strings.HasSuffix(t, "//") {
		t = t[:len(t)-1]
	}
	return t
}

// canonicalAuthority folds a host and port the way a URL parser does.
func canonicalAuthority(scheme, a string) string {
	host, port := a, ""
	if end := strings.IndexByte(a, ']'); strings.HasPrefix(a, "[") && end > 0 {
		host, port = a[:end+1], strings.TrimPrefix(a[end+1:], ":")
	} else if j := strings.LastIndexByte(a, ':'); j >= 0 && (a[j+1:] == "" || isDigits(a[j+1:])) {
		host, port = a[:j], a[j+1:]
	}
	if n, err := strconv.Atoi(port); err == nil {
		port = strconv.Itoa(n)
	}
	if port == defaultPort[scheme] {
		port = ""
	}
	if hierarchical[scheme] {
		host = strings.ToLower(host)
		if scheme == "file" && host == "localhost" {
			host = ""
		}
		if strings.HasPrefix(host, "[") {
			if ip, err := netip.ParseAddr(host[1 : len(host)-1]); err == nil && ip.Zone() == "" {
				host = "[" + ip.String() + "]"
			}
		}
	}
	if port != "" {
		return host + ":" + port
	}
	return host
}

// escapes writes percent escapes one way: upper-case hex, none for a
// character that needs none, and one for each byte a URI may not hold as it
// is. An escape of a reserved character stays an escape.
func escapes(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '%' && i+2 < len(s):
			if v, err := hexByte(s[i+1 : i+3]); err == nil {
				if isUnreserved(v) {
					b.WriteByte(v)
				} else {
					b.WriteString("%" + strings.ToUpper(s[i+1:i+3]))
				}
				i += 2
				continue
			}
			b.WriteByte(c) // a "%" that starts no escape: left for nonCanonical to refuse
		case uriChar[c]:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
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

// underRoot reports whether uri equals root or sits beneath it, both in
// canonical form.
func underRoot(uri, root string) bool {
	u, r := canonicalURI(uri), canonicalURI(root)
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
	if route != "mux" || slug == "" {
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
		owns := exact[canonicalURI(trimmed)]
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

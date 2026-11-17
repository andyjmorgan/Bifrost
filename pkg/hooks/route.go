package hooks

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Routing a resource URI on the mux: by its slug prefix, or, for a URI with
// none, by the one server that claims it.

// route finds the server for a URI. On the mux the slug prefix decides; a
// URI without one goes through fallback resolution. On 1:1 the URI is used
// as is. Non-canonical URIs are refused.
func (e *Engine) route(ctx context.Context, r Request, uri, method string) (*Server, string, Outcome, *RPCError) {
	var o Outcome
	if nonCanonical(uri) {
		return nil, "", o, &RPCError{Code: CodeInvalidParams, Message: "URI is not in canonical form: " + uri}
	}
	if !r.mux() {
		if len(r.Servers) != 1 {
			return nil, "", o, &RPCError{Code: CodeInvalidParams, Message: "not found: " + uri}
		}
		s := r.Servers[0]
		if !e.config(ctx, r, s, "resources", &o) {
			return nil, "", o, &RPCError{Code: CodeInvalidParams, Message: "not found: " + uri}
		}
		return s, uri, o, nil
	}
	if slug, rest, ok := SplitURI(uri); ok {
		s := r.server(slug)
		if s != nil && nonCanonical(rest) {
			return nil, "", o, &RPCError{Code: CodeInvalidParams, Message: "URI is not in canonical form: " + uri}
		}
		// A native scheme can contain "+", so a URI with no slug prefix can
		// look as if it had one: git+ssh://host/repo reads as slug "git".
		// When the named server does not claim the rest, try the whole URI
		// as an unprefixed one before settling on the prefixed reading.
		if s == nil || !e.resourceView(ctx, r, s).claims(rest) {
			var fo Outcome
			fs, fu, out, ferr := e.fallback(ctx, r, uri, method, &fo)
			if ferr == nil {
				return fs, fu, out, nil
			}
			// Only "nobody claims it" falls through to the prefixed reading.
			// Two claimants, or a server whose catalog is unavailable, is an
			// answer in its own right and must reach the client as one: the
			// unavailable server may be the one that owns git+ssh://…, and
			// sending ssh://… to the server named git would be a guess.
			if out.Fallback != nil && out.Fallback.Outcome != "not_found" {
				return nil, "", out, ferr
			}
		}
		if s == nil || !e.config(ctx, r, s, "resources", &o) {
			return nil, "", o, &RPCError{Code: CodeInvalidParams, Message: "not found: " + uri}
		}
		return s, rest, o, nil
	}
	return e.fallback(ctx, r, uri, method, &o)
}

// fallback resolves an unprefixed URI to one of the caller's servers, or
// reports it unknown or ambiguous. A server is a candidate only when it
// claims the URI (it lists it, or a template could produce it) and nothing
// denies the read. It never guesses: a URI no server claims is not found on
// the mux, and needs its slug prefix.
func (e *Engine) fallback(ctx context.Context, r Request, uri, method string, o *Outcome) (*Server, string, Outcome, *RPCError) {
	var exact, tmpl []Candidate
	var unavailable []string
	n := canonicalURI(uri)
	for _, s := range sortedServers(r.Servers) {
		if !e.config(ctx, r, s, "resources", o) {
			continue
		}
		if s.Unavailable {
			unavailable = append(unavailable, s.Slug)
			continue
		}
		v := e.resourceView(ctx, r, s)
		o.Hits = append(o.Hits, v.hits...)
		if _, ok, _ := e.checkURI(ctx, r, s, v, uri); !ok || !v.claims(uri) {
			continue
		}
		isExact := v.listedRes[n]
		for _, sk := range v.skills {
			if canonicalURI(sk.URI) == n {
				isExact = true
			}
			if ms, ok := v.manifestOf(sk); ok {
				for _, m := range ms {
					if canonicalURI(m.URI) == n {
						isExact = true
					}
				}
			}
		}
		c := Candidate{Slug: s.Slug, URI: PrefixURI(s.Slug, uri), Exact: isExact}
		if isExact {
			exact = append(exact, c)
		} else {
			tmpl = append(tmpl, c)
		}
	}
	ev := &FallbackEvent{URI: uri, Method: method}
	pick := func(c []Candidate, viaTemplate bool) (*Server, string, Outcome, *RPCError) {
		ev.Candidates, ev.Template = len(c), viaTemplate
		o.Fallback = ev
		if len(unavailable) > 0 {
			ev.Outcome = "unavailable"
			return nil, "", *o, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("cannot resolve %s: %s unavailable, and may be the server this URI belongs to; retry when it is back", uri, strings.Join(unavailable, ", "))}
		}
		if len(c) > 1 {
			ev.Outcome = "ambiguous"
			return nil, "", *o, ambiguousError(uri, c)
		}
		ev.Outcome, ev.Slug = "resolved", c[0].Slug
		return r.server(c[0].Slug), uri, *o, nil
	}
	if len(exact) > 0 {
		return pick(exact, false)
	}
	if len(tmpl) > 0 {
		return pick(tmpl, true)
	}
	if len(unavailable) > 0 {
		return pick(nil, false)
	}
	ev.Outcome = "not_found"
	o.Fallback = ev
	return nil, "", *o, &RPCError{Code: CodeInvalidParams, Message: "not found: " + uri}
}

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

// Package slug derives the immutable per-org identifier of an entitlement,
// the prefix clients see on its tool names.
//
// The slug is the name lowercased and reduced to [a-z0-9-] (runs of other
// characters become one hyphen), cut to MaxLen. When it collides with an existing slug the tail is overwritten
// with an incrementing counter: atlassian, atlassia1, ..., atlassia9,
// atlassi10, atlassi11, and so on. Renaming the server never changes it.
package slug

import (
	"errors"
	"strconv"
	"strings"
	"unicode"
)

// MaxLen is the fixed upper bound on slug length.
const MaxLen = 24

// ErrExhausted is returned when no free slug can be found for a base.
var ErrExhausted = errors.New("slug: no free slug for name")

// Base normalises a display name into slug characters. An empty result
// becomes "server".
func Base(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r > unicode.MaxASCII {
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if b.Len() > 0 && !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = "server"
	}
	if len(s) > MaxLen {
		s = strings.TrimRight(s[:MaxLen], "-")
	}
	return s
}

// Candidates yields base, then base with its tail replaced by 1, 2, ... until
// the counter can no longer fit, calling fn for each. It stops when fn
// returns true (a free slug was accepted).
func Candidates(base string, fn func(candidate string) bool) bool {
	if fn(base) {
		return true
	}
	for n := 1; ; n++ {
		suffix := strconv.Itoa(n)
		if len(suffix) >= MaxLen {
			return false
		}
		keep := min(len(base), MaxLen-len(suffix))
		if fn(strings.TrimRight(base[:keep], "-") + suffix) {
			return true
		}
		if n > 999_999 {
			return false
		}
	}
}

// Valid reports whether s is a well-formed slug.
func Valid(s string) bool {
	if s == "" || len(s) > MaxLen {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

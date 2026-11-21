package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

// KeyPrefix marks the gateway's own input requests so they never collide
// with an upstream's.
const KeyPrefix = "bifrost."

// StateTTL is how long a challenge stays valid.
const StateTTL = 5 * time.Minute

// State is what the gateway keeps between the rounds of one request: a
// question it asked, or the state a server sent with a question of its own.
// It stays in the gateway's store. The client holds only the key, which
// works once.
type State struct {
	Challenge     string            `json:"c"`             // id of this challenge
	Method        string            `json:"m"`             // tools/call, prompts/get or resources/read
	Caller        string            `json:"u"`             // caller user id
	Server        string            `json:"s"`             // server id
	Target        string            `json:"t"`             // upstream tool or prompt name, or resource URI
	ArgsHash      string            `json:"h"`             // hash of the effective arguments before call rules
	Generation    string            `json:"g"`             // policy generation
	Expires       int64             `json:"x"`             // unix seconds
	Pending       []string          `json:"p,omitempty"`   // gateway keys asked in this round
	Asks          map[string]string `json:"q,omitempty"`   // what each pending key asks: see binding
	Confirmed     []string          `json:"k,omitempty"`   // the bindings the user said yes to in earlier rounds
	Upstream      string            `json:"up,omitempty"`  // the upstream's own opaque requestState, if it sent one
	UpstreamRound bool              `json:"ur,omitempty"`  // the upstream asked for input; forward its answers on retry
	UpPending     []string          `json:"upp,omitempty"` // upstream keys asked in this round
}

// binding is what a confirmation covers: the question, as the user was
// shown it, and the arguments as they stood when it was asked, after the
// rules before it had made their changes. An answer counts only for the
// same question about the same arguments.
func binding(marker, args map[string]any) string {
	key, _ := marker[ElicitKey].(string)
	msg, _ := marker["message"].(string)
	sum := sha256.Sum256([]byte(canonical([]any{key, msg, args})))
	return hex.EncodeToString(sum[:16])
}

// argsHash hashes arguments canonically: keys sorted at every level, numbers
// in their JSON form, so two encodings of the same value hash alike.
func argsHash(args map[string]any) string {
	sum := sha256.Sum256([]byte(canonical(args)))
	return hex.EncodeToString(sum[:16])
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

func canonical(v any) string {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			b.Write(kb)
			b.WriteByte(':')
			b.WriteString(canonical(t[k]))
		}
		b.WriteByte('}')
		return b.String()
	case []any:
		var b strings.Builder
		b.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(canonical(x))
		}
		b.WriteByte(']')
		return b.String()
	}
	switch t := v.(type) {
	case json.Number:
		return canonNumber(t.String())
	case float64:
		return canonNumber(strconv.FormatFloat(t, 'g', -1, 64))
	case int64:
		return canonNumber(strconv.FormatInt(t, 10))
	case int:
		return canonNumber(strconv.Itoa(t))
	case uint64:
		return canonNumber(strconv.FormatUint(t, 10))
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// canonNumber writes a JSON number one way, so 1, 1.0 and 1e0 hash alike,
// and exactly, so two numbers that differ in any digit hash apart. It works
// on the digits and never through a float, which would round. The form is
// the digits with no leading or trailing zeros, "e", and the power of ten.
func canonNumber(text string) string {
	t := text
	neg := strings.HasPrefix(t, "-")
	t = strings.TrimPrefix(strings.TrimPrefix(t, "-"), "+")
	mant, exp := t, ""
	if i := strings.IndexAny(t, "eE"); i >= 0 {
		mant, exp = t[:i], t[i+1:]
	}
	whole, frac, _ := strings.Cut(mant, ".")
	e := 0
	if exp != "" {
		n, err := strconv.Atoi(exp)
		if err != nil || n > 1<<30 || n < -(1<<30) {
			return text // not a number this can read: compared as written
		}
		e = n
	}
	if !isDigits(whole+frac) || (exp == "" && strings.ContainsAny(t, "eE")) {
		return text
	}
	digits := strings.TrimLeft(whole+frac, "0")
	e -= len(frac)
	if trimmed := strings.TrimRight(digits, "0"); trimmed != digits {
		e += len(digits) - len(trimmed)
		digits = trimmed
	}
	if digits == "" {
		return "0"
	}
	if neg {
		digits = "-" + digits
	}
	return digits + "e" + strconv.Itoa(e)
}

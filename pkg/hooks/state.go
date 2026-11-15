package hooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// parallel runs fn(i) for i in [0,n) on up to workers goroutines. fn must
// only write to its own slot; that is what makes listing safe to spread.
func parallel(n, workers int, fn func(i int)) {
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers > n {
		workers = n
	}
	if workers <= 1 {
		for i := 0; i < n; i++ {
			fn(i)
		}
		return
	}
	var wg sync.WaitGroup
	next := make(chan int)
	var once sync.Once
	var caught *enginePanic
	// A panic on a worker goroutine would end the process, and no caller
	// can recover it. Each item recovers its own, the first one is kept, and
	// it is raised again on the caller's goroutine once every worker is done.
	guarded := func(i int) {
		defer func() {
			if p := recover(); p != nil {
				ep := capture(p)
				once.Do(func() { caught = ep })
			}
		}()
		fn(i)
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				guarded(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		next <- i
	}
	close(next)
	wg.Wait()
	if caught != nil {
		panic(caught)
	}
}

// ---- elicitation state -------------------------------------------------------------

// KeyPrefix marks the gateway's own input requests so they never collide
// with an upstream's.
const KeyPrefix = "bifrost."

// StateTTL is how long a challenge stays valid.
const StateTTL = 5 * time.Minute

// State is the gateway's part of a requestState. It is signed, so a client
// can read it but not change it.
type State struct {
	Challenge     string                    `json:"c"`             // id of this challenge
	Method        string                    `json:"m"`             // tools/call, prompts/get or resources/read
	Caller        string                    `json:"u"`             // caller user id
	Entitlement   string                    `json:"e"`             // entitlement slug
	Target        string                    `json:"t"`             // upstream tool or prompt name, or resource URI
	ArgsHash      string                    `json:"h"`             // hash of the effective arguments before call rules
	Generation    string                    `json:"g"`             // policy generation
	Expires       int64                     `json:"x"`             // unix seconds
	Pending       []string                  `json:"p,omitempty"`   // gateway keys asked in this round
	Answers       map[string]map[string]any `json:"a,omitempty"`   // gateway answers from earlier rounds
	Upstream      string                    `json:"up,omitempty"`  // the upstream's own opaque requestState, if it sent one
	UpstreamRound bool                      `json:"ur,omitempty"`  // the upstream asked for input; forward its answers on retry
	UpPending     []string                  `json:"upp,omitempty"` // upstream keys asked in this round
}

// ErrState is any problem with a presented requestState.
var ErrState = errors.New("invalid request state")

// Signer signs and verifies request state.
type Signer struct{ Key []byte }

// Seal encodes and signs a state.
func (s Signer) Seal(st State) string {
	b, _ := json.Marshal(st)
	p := base64.RawURLEncoding.EncodeToString(b)
	m := hmac.New(sha256.New, s.Key)
	m.Write([]byte(p))
	return p + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Open verifies and decodes a state.
func (s Signer) Open(tok string) (State, error) {
	var st State
	p, sig, ok := strings.Cut(tok, ".")
	if !ok {
		return st, ErrState
	}
	m := hmac.New(sha256.New, s.Key)
	m.Write([]byte(p))
	want := m.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(want, got) {
		return st, ErrState
	}
	b, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil || json.Unmarshal(b, &st) != nil {
		return st, ErrState
	}
	return st, nil
}

// argsHash hashes arguments canonically: keys sorted at every level, numbers
// in their JSON form, so two encodings of the same value hash alike.
func argsHash(args map[string]any) string {
	sum := sha256.Sum256([]byte(canonical(args)))
	return hex.EncodeToString(sum[:16])
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
	b, _ := json.Marshal(v)
	return string(b)
}

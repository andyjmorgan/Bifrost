package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"runtime"
	"sort"
	"strconv"
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

// State is what the gateway keeps between the rounds of one request: a
// question it asked, or the state a server sent with a question of its own.
// It stays in the gateway's store. The client holds only the key, which
// works once.
type State struct {
	Challenge     string            `json:"c"`             // id of this challenge
	Method        string            `json:"m"`             // tools/call, prompts/get or resources/read
	Caller        string            `json:"u"`             // caller user id
	Entitlement   string            `json:"e"`             // entitlement slug
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

// asked returns the check a pre hook's rules use to learn whether a
// question has been confirmed on this request. The engine decides that, not
// the rule: a rule only says when to ask and what. confirmed holds what the
// user has said yes to, each as a binding, so a yes counts only for the
// question it answered. The same key asked about other arguments, or with
// other words, is a new question and is asked.
func asked(confirmed map[string]bool, args map[string]any) func(map[string]any) bool {
	return func(marker map[string]any) bool { return confirmed[binding(marker, args)] }
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
	// A number is written one way, so 1, 1.0 and 1e0 hash alike. A whole
	// number keeps every digit.
	var f float64
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return strconv.FormatInt(i, 10)
		}
		if isDigits(strings.TrimPrefix(t.String(), "-")) {
			return t.String() // a whole number too big for 64 bits: every digit counts
		}
		x, err := t.Float64()
		if err != nil {
			return t.String()
		}
		f = x
	case float64:
		f = t
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	case uint64:
		return strconv.FormatUint(t, 10)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
	if f == math.Trunc(f) && math.Abs(f) < 1<<53 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

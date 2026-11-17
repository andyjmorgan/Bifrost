package hooks

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Context is the request context every hook sees. All of it is read-only to
// rules.
type Context struct {
	Server      map[string]any  `json:"server,omitempty"`
	Entitlement map[string]any  `json:"entitlement,omitempty"`
	Caller      map[string]any  `json:"caller,omitempty"`
	Client      map[string]any  `json:"client,omitempty"`
	Route       string          `json:"route,omitempty"` // direct | mux
	Flags       map[string]bool `json:"flags,omitempty"` // the request's switches, such as search and cli
	Now         time.Time       `json:"now,omitempty"`
}

func (c Context) vars() map[string]any {
	orEmpty := func(m map[string]any) map[string]any {
		if m == nil {
			return map[string]any{}
		}
		return m
	}
	client := orEmpty(c.Client)
	if _, ok := client["capabilities"].(map[string]any); !ok {
		// Always present, so has(client.capabilities.x) never errors. The
		// caller's map is copied, never modified.
		cp := make(map[string]any, len(client)+1)
		for k, v := range client {
			cp[k] = v
		}
		cp["capabilities"] = map[string]any{}
		client = cp
	}
	return map[string]any{
		"server": orEmpty(c.Server), "entitlement": orEmpty(c.Entitlement), "caller": orEmpty(c.Caller),
		"client": client, "route": ruleRoute(c.Route), "flags": flagVars(c.Flags), "now": timeOrZero(c.Now),
	}
}

// ruleRoute is the route as rules see it. Search is a flag, not a route, so
// the older value "search" reads as "mux".
func ruleRoute(route string) string {
	if route == "search" {
		return "mux"
	}
	return route
}

// Hit records what one rule did, for audit and for scenario output.
type Hit struct {
	Hook string `json:"hook"`
	Rule string `json:"rule"`
	// Server is the slug of the server whose rule this is, when the rules
	// were run with a server in their context.
	Server string   `json:"server,omitempty"`
	Item   string   `json:"item,omitempty"`
	Mode   string   `json:"mode"`             // enforce | audit
	Effect string   `json:"effect"`           // deny | elicit | set | error, prefixed "would " in audit mode
	Fields []string `json:"fields,omitempty"` // fields set
	Reason string   `json:"reason,omitempty"`
	Error  string   `json:"error,omitempty"`
	// Panic marks an error that was a panic, not an ordinary rule error.
	Panic bool `json:"panic,omitempty"`
	rule  *FaultRule
}

// outcome is the result of running one hook over one item.
type outcome struct {
	denied bool
	reason string
	elicit map[string]any // the elicit() marker, when a rule asked for input
	hits   []Hit
	err    error // an enforce rule failed to evaluate (counts as deny)
}

// setter applies one field a rule set. It returns an error to fail the rule.
type setter func(key string, val any) error

// run evaluates a hook's rules over one item, top to bottom. Changes chain:
// each rule's activation reads the current state. The first deny, elicit or
// evaluation error stops enforce rules; audit rules still run so the log is
// complete, but never change anything.
func (s *Set) run(ctx context.Context, hook, itemLabel string, resolve func(string) (any, bool), vars map[string]any, set setter) outcome {
	return s.runIf(ctx, hook, itemLabel, resolve, vars, set, nil)
}

// runIf is run over the rules keep accepts; nil accepts every rule.
func (s *Set) runIf(ctx context.Context, hook, itemLabel string, resolve func(string) (any, bool), vars map[string]any, set setter, keep func(*compiledRule) bool) outcome {
	var o outcome
	if s == nil {
		return o
	}
	act := &activation{vars: vars, item: resolve}
	// A panic in the engine's own code while a rule is in play travels up
	// with that rule attached, so the fault record can show its text.
	var cur *compiledRule
	defer func() {
		if p := recover(); p != nil {
			ep := capture(p)
			if ep.rule == nil {
				ep.rule = faultRule(hook, itemLabel, cur)
			}
			panic(ep)
		}
	}()
	stopped := false
	server := ""
	if sv, ok := vars["server"].(map[string]any); ok {
		server, _ = sv["slug"].(string)
	}
	bounded := newRunBudget(budgetFrom(ctx))
	defer bounded.close()
	for _, r := range s.byHook[hook] {
		if stopped && !r.audit {
			continue
		}
		if keep != nil && !keep(r) {
			continue
		}
		cur = r
		mode := ModeEnforce
		if r.audit {
			mode = ModeAudit
		}
		hit := Hit{Hook: hook, Rule: r.label, Server: server, Item: itemLabel, Mode: mode}
		ectx, done, berr := bounded.start(ctx, r.audit)
		if berr != nil {
			hit.Effect, hit.Error = "error", berr.Error()
			if r.audit {
				hit.Effect = "would error"
				o.hits = append(o.hits, hit)
				continue
			}
			o.hits = append(o.hits, hit)
			o.denied, o.err, o.reason = true, berr, ""
			stopped = true
			continue
		}
		var t0 time.Time
		if r.stat != nil {
			t0 = time.Now()
		}
		match, err := evalBool(ectx, r, act)
		if r.stat != nil {
			r.stat.observeWhen(time.Since(t0), match)
		}
		if err != nil || !match {
			done()
		}
		if err != nil {
			hit.Effect, hit.Error = "error", err.Error()
			if celPanic(err) {
				hit.Panic, hit.rule = true, faultRule(hook, itemLabel, r)
			}
			if r.audit {
				hit.Effect = "would error"
				o.hits = append(o.hits, hit)
				continue
			}
			o.hits = append(o.hits, hit)
			o.denied, o.err, o.reason = true, err, ""
			stopped = true
			continue
		}
		if !match {
			continue
		}
		// Evaluate every `then` value first (pure), then apply.
		vals := make([]any, len(r.then))
		failed := false
		if r.stat != nil {
			t0 = time.Now()
		}
		for i, t := range r.then {
			v, _, err := t.prog.ContextEval(ectx, act)
			if err != nil {
				hit.Effect, hit.Error = "error", fmt.Sprintf("%s: %v", t.key, err)
				if celPanic(err) {
					hit.Panic, hit.rule = true, faultRule(hook, itemLabel, r)
				}
				failed = true
				break
			}
			nv := native(v)
			if err := checkType(t.spec.Type, nv); err != nil {
				hit.Effect, hit.Error = "error", fmt.Sprintf("%s: %v", t.key, err)
				failed = true
				break
			}
			vals[i] = nv
		}
		if r.stat != nil {
			r.stat.observeThen(time.Since(t0))
		}
		done()
		if failed {
			if r.audit {
				hit.Effect = "would error"
				o.hits = append(o.hits, hit)
				continue
			}
			o.hits = append(o.hits, hit)
			o.denied, o.err, o.reason = true, fmt.Errorf("%s", hit.Error), ""
			stopped = true
			continue
		}
		var deny, elicit bool
		var reason string
		var marker map[string]any
		for i, t := range r.then {
			switch t.key {
			case "decision":
				deny = true
			case "reason":
				reason, _ = vals[i].(string)
			case "result":
				m, _ := vals[i].(map[string]any)
				if _, ok := m[ElicitKey]; !ok {
					hit.Effect, hit.Error = "error", "result must be built with elicit()"
					failed = true
				}
				elicit, marker = true, m
			}
		}
		hit.Reason = reason
		switch {
		case failed:
		case deny:
			hit.Effect = "deny"
		case elicit:
			hit.Effect = "elicit"
		default:
			hit.Effect = "set"
		}
		for _, t := range r.then {
			if t.key != "decision" && t.key != "reason" && t.key != "result" {
				hit.Fields = append(hit.Fields, t.key)
			}
		}
		if r.audit {
			hit.Effect = "would " + hit.Effect
			o.hits = append(o.hits, hit)
			continue
		}
		if failed {
			o.hits = append(o.hits, hit)
			o.denied, o.err, o.reason = true, fmt.Errorf("%s", hit.Error), ""
			stopped = true
			continue
		}
		if deny {
			o.hits = append(o.hits, hit)
			o.denied, o.reason = true, reason
			stopped = true
			continue
		}
		// Patches apply before an elicit stops evaluation, so the challenge
		// binds the arguments as patched so far.
		for i, t := range r.then {
			switch t.key {
			case "decision", "reason", "result":
				continue
			}
			if err := set(t.key, vals[i]); err != nil {
				hit.Effect, hit.Error = "error", fmt.Sprintf("%s: %v", t.key, err)
				o.hits = append(o.hits, hit)
				o.denied, o.err, o.reason = true, err, ""
				stopped = true
				break
			}
		}
		if stopped {
			continue
		}
		o.hits = append(o.hits, hit)
		if elicit {
			o.elicit = marker
			stopped = true
		}
	}
	cur = nil
	return o
}

func evalBool(ctx context.Context, r *compiledRule, act *activation) (bool, error) {
	v, _, err := r.when.ContextEval(ctx, act)
	if err != nil {
		return false, err
	}
	b, ok := native(v).(bool)
	if !ok {
		return false, fmt.Errorf("when did not produce a boolean")
	}
	return b, nil
}

func checkType(t ValueType, v any) error {
	ok := true
	switch t {
	case TText:
		_, ok = v.(string)
	case TBool:
		_, ok = v.(bool)
	case TNumber:
		switch v.(type) {
		case float64, int64, uint64:
		default:
			ok = false
		}
	case TList:
		_, ok = v.([]any)
	case TMap:
		_, ok = v.(map[string]any)
	case TDuration:
		switch d := v.(type) {
		case time.Duration:
			ok = d > 0
			if !ok {
				return fmt.Errorf("duration must be positive")
			}
		default:
			ok = false
		}
	case TDecision:
		ok = v == "deny"
	}
	if !ok {
		return fmt.Errorf("expected %s, got %T", t, v)
	}
	return nil
}

// sortedKeys returns map keys a-z.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func joinPath(p []string) string {
	esc := make([]string, len(p))
	for i, s := range p {
		esc[i] = strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), ".", `\.`)
	}
	return strings.Join(esc, ".")
}

// ---- budgets --------------------------------------------------------------------------

// budget accounts rule time per request. Enforce rules and audit rules draw
// on separate allowances, so audit work can never spend the enforcement
// budget (and so never change an outcome). Each evaluation runs with the
// time left in its own allowance and is charged what it used. Parent
// cancellation still stops everything.
type budget struct {
	mu                        sync.Mutex
	enforceUsed, enforceLimit time.Duration
	auditUsed, auditLimit     time.Duration
}

type budgetKey struct{}

func withBudget(ctx context.Context, enforce, audit time.Duration) context.Context {
	return context.WithValue(ctx, budgetKey{}, &budget{enforceLimit: enforce, auditLimit: audit})
}

func budgetFrom(ctx context.Context) *budget {
	b, _ := ctx.Value(budgetKey{}).(*budget)
	return b
}

var errBudget = errors.New("rule evaluation exceeded its time budget")
var errAuditBudget = errors.New("audit allowance exhausted")

// start returns a context bounded by the remaining allowance and a done
// func that charges the elapsed time. A nil budget means no limit.
func (b *budget) start(ctx context.Context, audit bool) (context.Context, func(), error) {
	if ctx.Err() != nil {
		return ctx, func() {}, ctx.Err()
	}
	if b == nil {
		return ctx, func() {}, nil
	}
	b.mu.Lock()
	var left time.Duration
	if audit {
		left = b.auditLimit - b.auditUsed
	} else {
		left = b.enforceLimit - b.enforceUsed
	}
	b.mu.Unlock()
	if left <= 0 {
		if audit {
			return ctx, func() {}, errAuditBudget
		}
		return ctx, func() {}, errBudget
	}
	ectx, cancel := context.WithTimeout(ctx, left)
	t0 := time.Now()
	once := sync.Once{}
	return ectx, func() {
		once.Do(func() {
			cancel()
			d := time.Since(t0)
			b.mu.Lock()
			if audit {
				b.auditUsed += d
			} else {
				b.enforceUsed += d
			}
			b.mu.Unlock()
		})
	}, nil
}

func (b *budget) enforceExhausted() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.enforceUsed >= b.enforceLimit
}

// runBudget creates the bounded evaluation context once per run (one item
// through one hook) and per mode, instead of once per rule, and charges
// each rule's elapsed time to the request budget.
type runBudget struct {
	b       *budget
	ctxs    [2]context.Context
	cancels [2]context.CancelFunc
}

func newRunBudget(b *budget) *runBudget { return &runBudget{b: b} }

func (rb *runBudget) start(ctx context.Context, audit bool) (context.Context, func(), error) {
	if ctx.Err() != nil {
		return ctx, func() {}, ctx.Err()
	}
	if rb.b == nil {
		return ctx, func() {}, nil
	}
	i := 0
	if audit {
		i = 1
	}
	rb.b.mu.Lock()
	var left time.Duration
	if audit {
		left = rb.b.auditLimit - rb.b.auditUsed
	} else {
		left = rb.b.enforceLimit - rb.b.enforceUsed
	}
	rb.b.mu.Unlock()
	if left <= 0 {
		if audit {
			return ctx, func() {}, errAuditBudget
		}
		return ctx, func() {}, errBudget
	}
	_ = i
	// A fresh deadline per rule: reusing one across other-mode work would
	// let audit time expire enforcement (V2-F03).
	rctx, cancel := context.WithTimeout(ctx, left)
	t0 := time.Now()
	charged := false
	return rctx, func() {
		if charged {
			return
		}
		charged = true
		cancel()
		d := time.Since(t0)
		rb.b.mu.Lock()
		if audit {
			rb.b.auditUsed += d
		} else {
			rb.b.enforceUsed += d
		}
		rb.b.mu.Unlock()
	}, nil
}

func (rb *runBudget) close() {
	for _, c := range rb.cancels {
		if c != nil {
			c()
		}
	}
}

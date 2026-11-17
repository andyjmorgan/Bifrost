package hooks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Context is what every hook is told about the request and where it is
// served. All of it is read-only to rules.
type Context struct {
	Server      map[string]any `json:"server,omitempty"`
	Entitlement map[string]any `json:"entitlement,omitempty"`
	Caller      map[string]any `json:"caller,omitempty"`
	// Client is the client as it describes itself on this request:
	// clientInfo and capabilities.
	Client map[string]any `json:"client,omitempty"`
	// Headers are the request's HTTP headers the gateway lets rules see, by
	// lower-case name. A gateway passes only what is on its allow-list.
	Headers map[string]string `json:"headers,omitempty"`
	Route   string            `json:"route,omitempty"` // direct | mux
	Flags   map[string]bool   `json:"flags,omitempty"` // the request's switches, such as search and cli
	Now     time.Time         `json:"now,omitempty"`
}

// roots builds the branches every hook has: server, entitlement and
// request. method and params are MCP's part of the request; params may be
// nil. A caller adds what its hook has beyond these: the response, a
// progress notification, an elicitation.
func (c Context) roots(method string, params map[string]any) map[string]any {
	orEmpty := func(m map[string]any) map[string]any {
		if m == nil {
			return map[string]any{}
		}
		return m
	}
	client := map[string]any{"capabilities": map[string]any{}}
	for k, v := range c.Client {
		// capabilities is always a map, so has(request.client.capabilities.x)
		// never fails. The caller's map is copied, never modified.
		if _, isMap := v.(map[string]any); k == "capabilities" && !isMap {
			continue
		}
		client[k] = v
	}
	headers := make(map[string]any, len(c.Headers))
	for k, v := range c.Headers {
		headers[strings.ToLower(k)] = v
	}
	return map[string]any{
		"server": orEmpty(c.Server), "entitlement": orEmpty(c.Entitlement),
		"request": map[string]any{
			"method": method, "params": orEmpty(params),
			"caller": orEmpty(c.Caller), "client": client, "headers": headers,
			"route": c.Route, "flags": flagVars(c.Flags), "time": timeOrZero(c.Now),
		},
	}
}

// requestOf is the request branch of a set of roots.
func requestOf(roots map[string]any) map[string]any { return roots["request"].(map[string]any) }

// setParam sets one key of request.params: the arguments after a rule
// changed them, so the next rule reads them as changed.
func setParam(roots map[string]any, key string, val any) {
	req := requestOf(roots)
	params := req["params"].(map[string]any)
	next := make(map[string]any, len(params)+1)
	for k, v := range params {
		next[k] = v
	}
	next[key] = val
	req["params"] = next
}

// subject builds the item branch from the item's fields: the ones that are
// there, the gateway's view of its parameters, and upstream. Nothing is
// filled in for a field the item does not have.
func subject(kind ItemKind, resolve func(string) (any, bool)) map[string]any {
	out := make(map[string]any, len(itemFields[kind])+2)
	for _, f := range itemFields[kind] {
		if v, ok := resolve(f); ok {
			out[f] = v
		}
	}
	for _, f := range []string{"params", "upstream"} {
		if v, ok := resolve(f); ok {
			out[f] = v
		}
	}
	return out
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
	return s.runIf(ctx, hook, itemLabel, resolve, vars, set, nil, nil)
}

// runAsking is run for a hook whose rules can ask the user to confirm.
// answered says whether a question has been confirmed on this request.
func (s *Set) runAsking(ctx context.Context, hook, itemLabel string, resolve func(string) (any, bool), vars map[string]any, answered func(map[string]any) bool, set setter) outcome {
	return s.runIf(ctx, hook, itemLabel, resolve, vars, set, nil, answered)
}

// runIf is run over the rules keep accepts; nil accepts every rule.
func (s *Set) runIf(ctx context.Context, hook, itemLabel string, resolve func(string) (any, bool), vars map[string]any, set setter, keep func(*compiledRule) bool, answered func(map[string]any) bool) outcome {
	var o outcome
	if s == nil {
		return o
	}
	spec := Hooks[hook]
	act := &activation{vars: vars, kind: spec.Item, item: resolve}
	strip := spec.writeRoot()
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
	bounded := budgetFrom(ctx)
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
			terr := checkType(t.spec.Type, nv)
			if m, isMap := nv.(map[string]any); terr == nil && isMap && t.spec.Keys != nil {
				for k := range m {
					if !slices.Contains(t.spec.Keys, k) {
						terr = fmt.Errorf("%s has no %q; it has %s", t.key, k, strings.Join(t.spec.Keys, ", "))
					}
				}
			}
			if err := terr; err != nil {
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
			case KeyDecision:
				deny = true
			case KeyReason:
				reason, _ = vals[i].(string)
			case KeyConfirm:
				m, _ := vals[i].(map[string]any)
				if _, ok := m[ElicitKey]; !ok {
					hit.Effect, hit.Error = "error", "confirm must be built with elicit()"
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
			if t.key != KeyDecision && t.key != KeyReason && t.key != KeyConfirm {
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
		// A rule's changes apply before its question is asked.
		for i, t := range r.then {
			switch t.key {
			case KeyDecision, KeyReason, KeyConfirm:
				continue
			}
			// The setter takes the path inside the branch this hook changes.
			if err := set(strings.TrimPrefix(t.key, strip), vals[i]); err != nil {
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
		if elicit {
			// The question is asked once the rule's own changes are made, so
			// it covers the arguments as they stand now.
			if answered != nil && answered(marker) {
				hit.Effect = "confirmed"
			} else {
				o.elicit = marker
				stopped = true
			}
		}
		o.hits = append(o.hits, hit)
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

// joinPath writes path segments as one key, the way splitPath reads them:
// a dot or a backslash inside a segment is escaped.
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

// memo holds what one request works out once and then asks for again. A
// server's resource view runs its list rules over the whole catalog, and a
// read asks for it when the URI is routed and again when its pre hook runs.
type memo struct {
	mu    sync.Mutex
	views map[*Server]*resView
}

type memoKey struct{}

func withMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, memoKey{}, &memo{views: map[*Server]*resView{}})
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

func (b *budget) enforceExhausted() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.enforceUsed >= b.enforceLimit
}

// start bounds one rule's evaluation by what is left of the request's
// allowance for its mode, enforce or audit. The func it returns charges the
// time the rule took. A nil budget has no limit.
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
	// A fresh deadline per rule: reusing one across work in the other mode
	// would let time spent on audit rules run out an enforce rule.
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
		b.mu.Lock()
		if audit {
			b.auditUsed += d
		} else {
			b.enforceUsed += d
		}
		b.mu.Unlock()
	}, nil
}

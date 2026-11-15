package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
)

// Modes.
const (
	ModeEnforce = "enforce"
	ModeAudit   = "audit"
)

// Rule is one rule as stored: a hook, a CEL condition and the fields it sets.
type Rule struct {
	ID      string            `json:"id,omitempty"`
	Name    string            `json:"name,omitempty"`
	Enabled *bool             `json:"enabled,omitempty"` // nil means enabled
	Mode    string            `json:"mode,omitempty"`    // enforce (default) or audit
	Hook    string            `json:"hook"`
	When    string            `json:"when"`
	Then    map[string]string `json:"then,omitempty"`
}

func (r Rule) enabled() bool { return r.Enabled == nil || *r.Enabled }

func (r Rule) label(i int) string {
	switch {
	case r.Name != "":
		return r.Name
	case r.ID != "":
		return r.ID
	}
	return fmt.Sprintf("rule %d", i+1)
}

// Problem names one reason a rule does not compile.
type Problem struct {
	Rule    int    `json:"rule"` // 0-based index
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// Problems is every problem in a rule list. A non-empty list fails the whole
// entitlement closed.
type Problems []Problem

func (ps Problems) Error() string {
	var b strings.Builder
	for i, p := range ps {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "rule %d", p.Rule+1)
		if p.Field != "" {
			fmt.Fprintf(&b, " (%s)", p.Field)
		}
		b.WriteString(": " + p.Message)
	}
	return b.String()
}

// Parse decodes a stored rule list. Unknown JSON keys are rejected so that
// rules written for another shape fail loudly instead of silently.
func Parse(raw json.RawMessage) ([]Rule, error) {
	if t := jsonTrim(string(raw)); t == "" || t == "null" {
		if t == "" && len(raw) > 0 {
			return nil, fmt.Errorf("rules: not JSON")
		}
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var rs []Rule
	if err := dec.Decode(&rs); err != nil {
		return nil, fmt.Errorf("rules: %w", err)
	}
	if jsonTrim(string(raw[dec.InputOffset():])) != "" {
		return nil, fmt.Errorf("rules: unexpected data after the rule list")
	}
	return rs, nil
}

type compiledThen struct {
	key  string
	spec *Field
	prog cel.Program
	vars []string
}

type compiledRule struct {
	idx     int
	rule    Rule
	label   string
	audit   bool
	when    cel.Program
	then    []compiledThen
	vars    map[string]bool
	dynamic bool
	stat    *RuleStat // set while profiling
}

// Set is a compiled rule list, grouped by hook in rule order.
type Set struct {
	byHook   map[string][]*compiledRule
	opts     Options
	prof     *Profile // nil unless profiling
	Warnings Problems // problems in disabled rules: reported, never blocking
	// Fingerprint identifies the compiled rule list. Elicitation state binds
	// it, so any rule change invalidates outstanding challenges even when
	// the gateway forgets to bump its generation.
	Fingerprint string
}

// Options bound evaluation.
type Options struct {
	// CostLimit caps the CEL cost of one evaluation; exceeding it is an
	// evaluation error, which denies. Zero means DefaultCostLimit.
	CostLimit uint64
}

// DefaultCostLimit is generous for real rules and stops runaway ones.
const DefaultCostLimit = 1_000_000

// Rules returns the compiled rules of a hook, in order.
func (s *Set) Rules(hook string) []Rule {
	if s == nil {
		return nil
	}
	var out []Rule
	for _, c := range s.byHook[hook] {
		out = append(out, c.rule)
	}
	return out
}

// Has reports whether a hook has any enabled rule.
func (s *Set) Has(hook string) bool { return s != nil && len(s.byHook[hook]) > 0 }

// Dynamic reports, per rule label, whether the rule depends on the request.
func (s *Set) Dynamic() map[string]bool {
	out := map[string]bool{}
	if s == nil {
		return out
	}
	for _, rs := range s.byHook {
		for _, c := range rs {
			out[c.label] = c.dynamic
		}
	}
	return out
}

// Compile checks every rule and returns the evaluable set, or every problem.
func Compile(rs []Rule, opts ...Options) (*Set, Problems) {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.CostLimit == 0 {
		o.CostLimit = DefaultCostLimit
	}
	s := &Set{byHook: map[string][]*compiledRule{}, opts: o}
	var probs Problems
	for i, r := range rs {
		c, ps := compileRule(i, r, o)
		if !r.enabled() {
			// A disabled rule that does not compile is ignored, but reported
			// so nobody enables it without seeing the error.
			s.Warnings = append(s.Warnings, ps...)
			continue
		}
		probs = append(probs, ps...)
		if c != nil {
			s.byHook[r.Hook] = append(s.byHook[r.Hook], c)
		}
	}
	if len(probs) > 0 {
		return nil, probs
	}
	b, _ := json.Marshal(rs)
	sum := sha256.Sum256(b)
	s.Fingerprint = hex.EncodeToString(sum[:8])
	return s, nil
}

func compileRule(i int, r Rule, o Options) (*compiledRule, Problems) {
	var probs Problems
	bad := func(field, msg string, a ...any) {
		probs = append(probs, Problem{Rule: i, Field: field, Message: fmt.Sprintf(msg, a...)})
	}
	spec, ok := Hooks[r.Hook]
	if !ok {
		bad("hook", "unknown hook %q", r.Hook)
		return nil, probs
	}
	switch r.Mode {
	case "", ModeEnforce, ModeAudit:
	default:
		bad("mode", "mode must be enforce or audit")
	}
	if strings.TrimSpace(r.When) == "" {
		bad("when", "when is required")
		return nil, probs
	}
	env, err := envFor(spec)
	if err != nil {
		bad("hook", "%v", err)
		return nil, probs
	}
	c := &compiledRule{idx: i, rule: r, label: r.label(i), audit: r.Mode == ModeAudit, vars: map[string]bool{}}
	prog, vars, err := compileExpr(env, r.When, cel.BoolType, o)
	if err != nil {
		bad("when", "%v", err)
	} else {
		c.when = prog
		for _, v := range vars {
			c.vars[v] = true
		}
	}
	if len(r.Then) == 0 && spec.Phase != PhaseFilter {
		bad("then", "then is required")
	}
	if len(r.Then) == 0 && spec.Phase == PhaseFilter {
		bad("then", "a filter rule must set decision: \"deny\"")
	}
	keys := make([]string, 0, len(r.Then))
	for k := range r.Then {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f := spec.field(k)
		if f == nil {
			bad(k, "%s cannot set %q", r.Hook, k)
			continue
		}
		if k == "limit" {
			bad(k, "rate limits are not enforced yet; remove limit until the counter store exists")
			continue
		}
		expr := r.Then[k]
		prog, vars, err := compileExpr(env, expr, nil, o)
		if err != nil {
			bad(k, "%v", err)
			continue
		}
		if f.Type == TDecision {
			if v, ok := constString(env, expr); !ok || v != "deny" {
				bad(k, "decision must be the literal \"deny\"; there is no allow decision")
				continue
			}
		}
		if spec.Phase == PhaseModify {
			if parts := splitPath(k); len(parts) == 3 && parts[0] == "params" && (parts[2] == "pin" || parts[2] == "default") {
				for _, v := range vars {
					if DynamicVars[v] {
						bad(k, "pin and default values must be static; set a value that depends on %s with args.%s in tools/call", v, parts[1])
						break
					}
				}
			}
		}
		c.then = append(c.then, compiledThen{key: k, spec: f, prog: prog, vars: vars})
		for _, v := range vars {
			c.vars[v] = true
		}
	}
	for v := range c.vars {
		if DynamicVars[v] {
			c.dynamic = true
		}
	}
	if spec.Phase == PhaseFilter && !hasKey(r.Then, "decision") {
		bad("then", "a filter rule must set decision: \"deny\"")
	}
	if len(probs) > 0 {
		return nil, probs
	}
	return c, nil
}

func hasKey(m map[string]string, k string) bool { _, ok := m[k]; return ok }

// constString evaluates an expression that needs no variables.
func constString(env *cel.Env, expr string) (string, bool) {
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return "", false
	}
	prg, err := env.Program(ast)
	if err != nil {
		return "", false
	}
	v, _, err := prg.Eval(map[string]any{})
	if err != nil {
		return "", false
	}
	s, ok := v.(types.String)
	return string(s), ok
}

// jsonTrim trims JSON whitespace only (space, tab, newline, carriage
// return); other Unicode space is not JSON whitespace.
func jsonTrim(s string) string { return strings.Trim(s, " \t\n\r") }

// Package rules evaluates an entitlement's ordered CEL rules. They run after
// the allow lists: each enabled rule whose kinds cover the capability and
// whose `when` is true sets the outcome to its action, and the last match
// wins, so a wildcard allow can re-enable what a list left out and a later
// deny can cut again. A rule that does not compile fails the whole
// entitlement closed.
package rules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/ext"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Actions.
const (
	Allow = "allow"
	Deny  = "deny"
)

// Rule is one ordered rule as stored on the entitlement.
type Rule struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Enabled *bool    `json:"enabled,omitempty"` // nil = enabled
	Action  string   `json:"action"`            // allow | deny
	Kinds   []string `json:"kinds,omitempty"`   // tool, prompt, resource, template; empty = any
	When    string   `json:"when"`
}

func (r Rule) enabled() bool { return r.Enabled == nil || *r.Enabled }

// Problem names a rule that does not compile.
type Problem struct {
	Rule    int    `json:"rule"` // 0-based index
	Message string `json:"message"`
}

// Problems is every problem in a rule list.
type Problems []Problem

func (ps Problems) Error() string {
	var b strings.Builder
	for i, p := range ps {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "rule %d: %s", p.Rule+1, p.Message)
	}
	return b.String()
}

// Vars lists the variables rules see, for the editor's reference.
var Vars = []string{"capability", "tool", "prompt", "resource", "template", "server", "caller", "client", "route"}

var env = func() *cel.Env {
	e, err := cel.NewEnv(
		cel.Variable("capability", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("tool", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("prompt", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("resource", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("template", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("server", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("caller", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("client", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("route", cel.StringType),
		ext.Strings(),
		cel.Function("regexReplace",
			cel.MemberOverload("string_regexReplace_string_string", []*cel.Type{cel.StringType, cel.StringType, cel.StringType}, cel.StringType,
				cel.FunctionBinding(func(args ...ref.Val) ref.Val {
					text, _ := args[0].Value().(string)
					pattern, _ := args[1].Value().(string)
					repl, _ := args[2].Value().(string)
					re, err := compileRegex(pattern)
					if err != nil {
						return types.NewErr("regexReplace: %v", err)
					}
					return types.String(re.ReplaceAllString(text, repl))
				})),
		),
	)
	if err != nil {
		panic(err)
	}
	return e
}()

type compiled struct {
	rule Rule
	prog cel.Program
}

// Compiled is a rule list ready to evaluate.
type Compiled struct {
	rules []compiled
}

// Empty reports whether no rule is enabled.
func (c *Compiled) Empty() bool { return c == nil || len(c.rules) == 0 }

// Parse decodes a stored rule list.
func Parse(raw json.RawMessage) ([]Rule, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var rs []Rule
	if err := json.Unmarshal(raw, &rs); err != nil {
		return nil, fmt.Errorf("rules: %w", err)
	}
	return rs, nil
}

// Compile checks every rule and returns the evaluable list, or Problems.
func Compile(rs []Rule) (*Compiled, Problems) {
	var problems Problems
	c := &Compiled{}
	for i, r := range rs {
		if r.Action != Allow && r.Action != Deny {
			problems = append(problems, Problem{Rule: i, Message: "action must be allow or deny"})
			continue
		}
		for _, k := range r.Kinds {
			switch k {
			case "tool", "prompt", "resource", "template":
			default:
				problems = append(problems, Problem{Rule: i, Message: "unknown kind " + k})
			}
		}
		if strings.TrimSpace(r.When) == "" {
			problems = append(problems, Problem{Rule: i, Message: "when is required"})
			continue
		}
		prog, err := compile(r.When)
		if err != nil {
			problems = append(problems, Problem{Rule: i, Message: err.Error()})
			continue
		}
		if r.enabled() {
			c.rules = append(c.rules, compiled{rule: r, prog: prog})
		}
	}
	if len(problems) > 0 {
		return nil, problems
	}
	return c, nil
}

func compile(expr string) (cel.Program, error) {
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, errors.New(firstLine(iss.Err().Error()))
	}
	if out := ast.OutputType(); !out.IsExactType(cel.BoolType) && !out.IsExactType(cel.DynType) {
		return nil, fmt.Errorf("expression must be a boolean, got %s", out)
	}
	return env.Program(ast, cel.InterruptCheckFrequency(64))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(strings.TrimSpace(s), "ERROR: <input>:")
	if i := strings.Index(s, " expecting {"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

var regexCache sync.Map

func compileRegex(pattern string) (*regexp.Regexp, error) {
	if v, ok := regexCache.Load(pattern); ok {
		return v.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	regexCache.Store(pattern, re)
	return re, nil
}

// Subject is one capability in its context.
type Subject struct {
	Kind   string // tool | prompt | resource | template
	Name   string
	Def    any            // the definition (*mcp.Tool, or the raw item map), may be nil
	Server map[string]any // slug, name, description, url, instructions
	Caller map[string]any // userId, email, name, role, type, keyName
	Client map[string]any // name, version, capabilities
	Route  string
}

// Hit records a rule that matched.
type Hit struct {
	Rule   string `json:"rule"`
	Action string `json:"action"`
	Error  string `json:"error,omitempty"`
}

// Decide runs the rules over one capability, starting from the allow-list
// outcome. The last matching rule wins. An evaluation error counts as a
// deny for that rule (fail closed) and is reported in the hits.
func (c *Compiled) Decide(ctx context.Context, base bool, s Subject) (bool, []Hit) {
	if c.Empty() {
		return base, nil
	}
	act := activation(s)
	out := base
	var hits []Hit
	for _, r := range c.rules {
		if len(r.rule.Kinds) > 0 && !contains(r.rule.Kinds, s.Kind) {
			continue
		}
		v, _, err := r.prog.ContextEval(ctx, act)
		if err != nil {
			out = false
			hits = append(hits, Hit{Rule: r.rule.Name, Action: Deny, Error: err.Error()})
			continue
		}
		if b, ok := v.Value().(bool); ok && b {
			out = r.rule.Action == Allow
			hits = append(hits, Hit{Rule: r.rule.Name, Action: r.rule.Action})
		}
	}
	return out, hits
}

func activation(s Subject) map[string]any {
	cap := ToMap(s.Def)
	cap["kind"] = s.Kind
	if _, ok := cap["name"]; !ok {
		cap["name"] = s.Name
	}
	empty := map[string]any{}
	act := map[string]any{
		"capability": cap, "tool": empty, "prompt": empty, "resource": empty, "template": empty,
		"server": orEmpty(s.Server), "caller": orEmpty(s.Caller), "client": orEmpty(s.Client), "route": s.Route,
	}
	if s.Kind != "" {
		act[s.Kind] = cap
	}
	return act
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// ToMap is the JSON view of a definition, which is what rules address. For
// tools it adds `meta`, `annotations` with the spec defaults filled in, and
// `annotationsRaw` as the upstream sent them.
func ToMap(v any) map[string]any {
	m := map[string]any{}
	if v == nil {
		return m
	}
	if mm, ok := v.(map[string]any); ok {
		for k, val := range mm {
			m[k] = val
		}
	} else {
		b, err := json.Marshal(v)
		if err != nil {
			return m
		}
		_ = json.Unmarshal(b, &m)
	}
	if meta, ok := m["_meta"]; ok {
		m["meta"] = meta
	} else {
		m["meta"] = map[string]any{}
	}
	for _, k := range []string{"title", "description"} {
		if _, ok := m[k]; !ok {
			m[k] = ""
		}
	}
	if t, ok := v.(*mcp.Tool); ok {
		raw, _ := m["annotations"].(map[string]any)
		if raw == nil {
			raw = map[string]any{}
		}
		m["annotationsRaw"] = raw
		ann := map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": true, "title": ""}
		if t.Annotations != nil {
			ann["readOnlyHint"] = t.Annotations.ReadOnlyHint
			ann["idempotentHint"] = t.Annotations.IdempotentHint
			ann["title"] = t.Annotations.Title
			if t.Annotations.DestructiveHint != nil {
				ann["destructiveHint"] = *t.Annotations.DestructiveHint
			}
			if t.Annotations.OpenWorldHint != nil {
				ann["openWorldHint"] = *t.Annotations.OpenWorldHint
			}
		}
		m["annotations"] = ann
		if _, ok := m["outputSchema"]; !ok {
			m["outputSchema"] = map[string]any{}
		}
		if _, ok := m["inputSchema"]; !ok {
			m["inputSchema"] = map[string]any{}
		}
	}
	return m
}

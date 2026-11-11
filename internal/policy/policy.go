// Package policy shapes what a route exposes: ordered CEL rules that hide,
// show, annotate, rename or guard tools before a client sees or calls them.
//
// A policy is a list of rules. Every tool starts visible (hidden when the
// policy is inverted, which makes the policy an allow-list); rules run in
// order, and each rule whose `when` expression is true applies its `then`
// assignments to the tool before the next rule looks at it. The same engine
// runs at tools/list time (what the client sees) and tools/call time (whether
// the call is allowed, needs confirmation, and which upstream name it maps to).
package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/ext"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Rule is one step of a policy. When is a CEL boolean over `tool`, `server`
// and `route`; Then maps a field (see Fields) to a CEL expression of that
// field's type.
type Rule struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Enabled *bool             `json:"enabled,omitempty"` // nil means enabled
	When    string            `json:"when"`
	Then    map[string]string `json:"then"`
}

func (r Rule) enabled() bool { return r.Enabled == nil || *r.Enabled }

// Policy is the stored object. Invert hides every tool before the rules run.
type Policy struct {
	ID          uuid.UUID `json:"id"`
	OwnerID     string    `json:"-"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Invert      bool      `json:"invert"`
	Rules       []Rule    `json:"rules"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Server is the context a rule sees as `server`.
type Server struct {
	Slug            string `json:"slug"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	Instructions    string `json:"instructions"`
	ProtocolVersion string `json:"protocolVersion"`
	URL             string `json:"url"`
}

// Kind is the type a `then` field takes.
type Kind string

const (
	KindBool   Kind = "bool"
	KindString Kind = "string"
)

// Field describes something a rule may assign.
type Field struct {
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	Doc  string `json:"doc"`
}

// Fields a rule may set, in display order.
var Fields = []Field{
	{"visible", KindBool, "Whether the tool is listed and callable on this route."},
	{"confirm", KindBool, "Ask the client to confirm (elicitation) before each call."},
	{"confirmMessage", KindString, "The question shown when confirming; defaults to naming the tool."},
	{"name", KindString, "The tool name the client sees (the mux prefix is added afterwards)."},
	{"title", KindString, "Display title."},
	{"description", KindString, "Description shown to the model."},
	{"annotations.readOnlyHint", KindBool, "The tool does not modify its environment."},
	{"annotations.destructiveHint", KindBool, "The tool may perform destructive updates."},
	{"annotations.idempotentHint", KindBool, "Repeating a call with the same arguments has no further effect."},
	{"annotations.openWorldHint", KindBool, "The tool interacts with an open world of external entities."},
}

var fieldKinds = func() map[string]Kind {
	m := map[string]Kind{}
	for _, f := range Fields {
		m[f.Name] = f.Kind
	}
	return m
}()

// Problem is a compile error located to a rule and field ("" for `when`).
type Problem struct {
	Rule    int    `json:"rule"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

func (p Problem) Error() string {
	if p.Field != "" {
		return fmt.Sprintf("rule %d, %s: %s", p.Rule+1, p.Field, p.Message)
	}
	return fmt.Sprintf("rule %d: %s", p.Rule+1, p.Message)
}

// Problems is every compile error in a policy.
type Problems []Problem

func (ps Problems) Error() string {
	var b strings.Builder
	for i, p := range ps {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(p.Error())
	}
	return b.String()
}

var env = func() *cel.Env {
	e, err := cel.NewEnv(
		cel.Variable("tool", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("server", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("route", cel.StringType),
		ext.Strings(),
	)
	if err != nil {
		panic(err)
	}
	return e
}()

type compiledRule struct {
	name string
	when cel.Program
	then []assignment
}

type assignment struct {
	field string
	kind  Kind
	prog  cel.Program
}

// Compiled is a policy ready to evaluate.
type Compiled struct {
	ID     uuid.UUID
	Name   string
	Invert bool
	rules  []compiledRule
}

// Compile checks every expression and returns the evaluable policy, or
// Problems naming each failing rule and field.
func Compile(p *Policy) (*Compiled, error) {
	c := &Compiled{ID: p.ID, Name: p.Name, Invert: p.Invert}
	var problems Problems
	for i, r := range p.Rules {
		if !r.enabled() {
			continue
		}
		cr := compiledRule{name: r.Name}
		if cr.name == "" {
			cr.name = fmt.Sprintf("rule %d", i+1)
		}
		if strings.TrimSpace(r.When) == "" {
			problems = append(problems, Problem{Rule: i, Message: "when is required (use true to always match)"})
		} else if prog, err := compile(r.When, KindBool); err != nil {
			problems = append(problems, Problem{Rule: i, Message: err.Error()})
		} else {
			cr.when = prog
		}
		if len(r.Then) == 0 {
			problems = append(problems, Problem{Rule: i, Message: "then must set at least one field"})
		}
		fields := make([]string, 0, len(r.Then))
		for f := range r.Then {
			fields = append(fields, f)
		}
		sort.Strings(fields) // deterministic application order within a rule
		for _, f := range fields {
			kind, ok := fieldKinds[f]
			if !ok {
				problems = append(problems, Problem{Rule: i, Field: f, Message: "unknown field"})
				continue
			}
			prog, err := compile(r.Then[f], kind)
			if err != nil {
				problems = append(problems, Problem{Rule: i, Field: f, Message: err.Error()})
				continue
			}
			cr.then = append(cr.then, assignment{field: f, kind: kind, prog: prog})
		}
		c.rules = append(c.rules, cr)
	}
	if len(problems) > 0 {
		return nil, problems
	}
	return c, nil
}

func compile(expr string, want Kind) (cel.Program, error) {
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, errors.New(firstLine(iss.Err().Error()))
	}
	out := ast.OutputType()
	switch want {
	case KindBool:
		if !out.IsExactType(cel.BoolType) && !out.IsExactType(cel.DynType) {
			return nil, fmt.Errorf("expression must be a boolean, got %s", out)
		}
	case KindString:
		if !out.IsExactType(cel.StringType) && !out.IsExactType(cel.DynType) {
			return nil, fmt.Errorf("expression must be a string, got %s", out)
		}
	}
	return env.Program(ast, cel.InterruptCheckFrequency(64))
}

// firstLine trims a cel-go issue to what a rule author needs: one line,
// without the "ERROR: <input>:" prefix or the parser's expected-token dump.
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

// Hit records one rule that matched a tool.
type Hit struct {
	Policy string   `json:"policy"`
	Rule   string   `json:"rule"`
	Set    []string `json:"set,omitempty"`
	Error  string   `json:"error,omitempty"`
}

// Decision is the outcome for one tool after every bound policy ran.
type Decision struct {
	Tool           *mcp.Tool // the shaped copy the client sees (name without mux prefix)
	Upstream       string    // the name the upstream knows the tool by
	Visible        bool
	Confirm        bool
	ConfirmMessage string
	Hits           []Hit
}

// Apply runs the policies in order over the tools of one server.
func Apply(ctx context.Context, policies []*Compiled, tools []*mcp.Tool, srv Server, route string) []Decision {
	out := make([]Decision, 0, len(tools))
	srvMap := toMap(srv)
	for _, t := range tools {
		d := apply(ctx, policies, t, srvMap, route)
		out = append(out, d)
	}
	return out
}

func apply(ctx context.Context, policies []*Compiled, t *mcp.Tool, srv map[string]any, route string) Decision {
	cp := *t
	if t.Annotations != nil {
		a := *t.Annotations
		cp.Annotations = &a
	}
	d := Decision{Tool: &cp, Upstream: t.Name, Visible: true}
	act := map[string]any{"tool": toMap(&cp), "server": srv, "route": route}
	tool := act["tool"].(map[string]any)
	slug, _ := srv["slug"].(string)
	refresh := func() {
		tool = toMap(&cp)
		// Rules see the name the way the mux shows it, slug__name, on every
		// route, because that is what people see in clients. The bare
		// upstream name is baseName; the `name` assignment sets that part.
		tool["baseName"] = cp.Name
		tool["name"] = slug + "__" + cp.Name
		tool["visible"] = d.Visible
		tool["confirm"] = d.Confirm
		act["tool"] = tool
	}
	refresh()
	for _, p := range policies {
		if p.Invert {
			d.Visible = false
			tool["visible"] = false
		}
		for _, r := range p.rules {
			hit := Hit{Policy: p.Name, Rule: r.name}
			ok, err := evalBool(ctx, r.when, act)
			if err != nil {
				hit.Error = err.Error()
				d.Hits = append(d.Hits, hit)
				continue
			}
			if !ok {
				continue
			}
			for _, a := range r.then {
				v, _, err := a.prog.ContextEval(ctx, act)
				if err != nil {
					hit.Error = a.field + ": " + err.Error()
					continue
				}
				if err := assign(&d, &cp, a, v.Value()); err != nil {
					hit.Error = a.field + ": " + err.Error()
					continue
				}
				hit.Set = append(hit.Set, a.field)
			}
			d.Hits = append(d.Hits, hit)
			refresh()
		}
	}
	return d
}

func evalBool(ctx context.Context, prog cel.Program, act map[string]any) (bool, error) {
	v, _, err := prog.ContextEval(ctx, act)
	if err != nil {
		return false, err
	}
	b, ok := v.Value().(bool)
	if !ok {
		return false, fmt.Errorf("when did not evaluate to a boolean")
	}
	return b, nil
}

func assign(d *Decision, t *mcp.Tool, a assignment, v any) error {
	switch a.kind {
	case KindBool:
		b, ok := v.(bool)
		if !ok {
			return fmt.Errorf("expected a boolean, got %T", v)
		}
		switch a.field {
		case "visible":
			d.Visible = b
		case "confirm":
			d.Confirm = b
		default:
			if t.Annotations == nil {
				t.Annotations = &mcp.ToolAnnotations{}
			}
			switch a.field {
			case "annotations.readOnlyHint":
				t.Annotations.ReadOnlyHint = b
			case "annotations.destructiveHint":
				t.Annotations.DestructiveHint = &b
			case "annotations.idempotentHint":
				t.Annotations.IdempotentHint = b
			case "annotations.openWorldHint":
				t.Annotations.OpenWorldHint = &b
			}
		}
	case KindString:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("expected a string, got %T", v)
		}
		switch a.field {
		case "name":
			s = strings.TrimSpace(s)
			if s == "" {
				return errors.New("name cannot be empty")
			}
			t.Name = s
		case "title":
			t.Title = s
		case "description":
			t.Description = s
		case "confirmMessage":
			d.ConfirmMessage = s
		}
	}
	return nil
}

// toMap is the JSON view of v, which is what CEL rules address. For tools it
// adds `meta` (the `_meta` object), `annotations` with the spec defaults
// filled in, and `annotationsRaw` as the upstream sent them.
func toMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
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
		if meta, ok := m["_meta"]; ok {
			m["meta"] = meta
		} else {
			m["meta"] = map[string]any{}
		}
		if _, ok := m["title"]; !ok {
			m["title"] = ""
		}
		if _, ok := m["description"]; !ok {
			m["description"] = ""
		}
		if _, ok := m["outputSchema"]; !ok {
			m["outputSchema"] = map[string]any{}
		}
		if _, ok := m["icons"]; !ok {
			m["icons"] = []any{}
		}
	}
	return m
}

// Cache compiles each policy once per (id, updatedAt).
type Cache struct {
	m sync.Map // key string -> *Compiled
}

// Get compiles p or returns the compiled form from an earlier call.
func (c *Cache) Get(p *Policy) (*Compiled, error) {
	key := p.ID.String() + "|" + p.UpdatedAt.UTC().Format(time.RFC3339Nano)
	if v, ok := c.m.Load(key); ok {
		return v.(*Compiled), nil
	}
	cp, err := Compile(p)
	if err != nil {
		return nil, err
	}
	c.m.Store(key, cp)
	return cp, nil
}

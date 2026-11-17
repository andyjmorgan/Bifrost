package hooks

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	"cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
	"cel.dev/cel-go/ext"
	"cel.dev/cel-go/interpreter"
)

// muxVar carries what slugUris needs; the slugUris(text) macro expands to
// @slugUris(text, @mux) so the helper stays a pure function of its inputs.
// Both names start with "@", which a rule cannot write: CEL's own grammar
// does not let an identifier begin with it.
const (
	muxVar      = "@mux"
	slugUrisFun = "@slugUris"
)

var (
	envMu   sync.Mutex
	envs    = map[string]*cel.Env{}
	envErrs = map[string]error{}
)

// envFor builds (once) the CEL environment of a hook: its variables, the
// enabled extensions and the helpers registered for that hook only.
func envFor(spec *HookSpec) (*cel.Env, error) {
	envMu.Lock()
	defer envMu.Unlock()
	if e, ok := envs[spec.Name]; ok {
		return e, envErrs[spec.Name]
	}
	opts := []cel.EnvOption{
		cel.CustomTypeAdapter(celAdapter),
		cel.OptionalTypes(),
		ext.Strings(), ext.Lists(), ext.Sets(), ext.TwoVarComprehensions(), ext.Regex(), ext.Bindings(), ext.Encoders(), ext.Math(),
		cel.CrossTypeNumericComparisons(true),
	}
	for _, root := range spec.Roots() {
		opts = append(opts, cel.Variable(root, cel.DynType))
	}
	for _, h := range spec.Helpers {
		opts = append(opts, helpers[h]...)
	}
	e, err := cel.NewEnv(opts...)
	envs[spec.Name], envErrs[spec.Name] = e, err
	return e, err
}

// compiled is one expression, ready to run, with what compiling it found.
type compiled struct {
	prg cel.Program
	// roots are the tree's branches the expression reads, which say whether
	// the rule depends on the request.
	roots []string
	// unguarded are the optional values it reads with a plain "."; tested
	// are the values it asks about with has().
	unguarded, tested []string
	// out is the type the expression gives, when the checker can tell.
	out *cel.Type
}

// fits reports whether a value of the type the checker found can be set on
// a field. A type the checker cannot tell fits: it is checked when the rule
// runs. Null fits everything: setting a key to null removes it.
func fits(want ValueType, out *cel.Type) bool {
	if out == nil {
		return true
	}
	switch out.Kind() {
	case types.DynKind, types.AnyKind, types.TypeParamKind, types.OpaqueKind, types.NullTypeKind, types.ErrorKind:
		return true
	}
	switch want {
	case TText:
		return out.Kind() == types.StringKind
	case TBool:
		return out.Kind() == types.BoolKind
	case TNumber:
		return out.Kind() == types.IntKind || out.Kind() == types.UintKind || out.Kind() == types.DoubleKind
	case TList:
		return out.Kind() == types.ListKind
	case TMap:
		return out.Kind() == types.MapKind
	case TDuration:
		return out.Kind() == types.DurationKind
	}
	return true
}

// compileExpr compiles one expression against a hook: it checks every path
// the expression reads against the hook's tree, and its output type.
func compileExpr(env *cel.Env, spec *HookSpec, expr string, want *cel.Type, o Options) (c compiled, err error) {
	a, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return c, errors.New(firstLine(iss.Err().Error()))
	}
	if c.unguarded, c.tested, err = checkPaths(a, spec); err != nil {
		return c, err
	}
	if want != nil {
		out := a.OutputType()
		if !out.IsExactType(want) && !out.IsExactType(cel.DynType) {
			return c, fmt.Errorf("expression must be %s, got %s", want, out)
		}
	}
	c.prg, err = env.Program(a, cel.CostLimit(o.CostLimit), cel.InterruptCheckFrequency(64), cel.EvalOptions(cel.OptOptimize),
		cel.CostTrackerOptions(helperCosts...))
	if err != nil {
		return c, err
	}
	c.roots, c.out = referenced(a), a.OutputType()
	return c, nil
}

func referenced(a *cel.Ast) []string {
	seen := map[string]bool{}
	var out []string
	native := a.NativeRep()
	for _, r := range native.ReferenceMap() {
		if r.Name != "" && !seen[r.Name] {
			seen[r.Name] = true
			out = append(out, r.Name)
		}
	}
	// Variables used only under has() or in comprehensions still appear as
	// identifiers; walk the expression for completeness.
	ast.PostOrderVisit(native.Expr(), ast.NewExprVisitor(func(e ast.Expr) {
		if e.Kind() == ast.IdentKind && !seen[e.AsIdent()] {
			seen[e.AsIdent()] = true
			out = append(out, e.AsIdent())
		}
	}))
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(strings.TrimSpace(s), "ERROR: <input>:")
	return strings.TrimSpace(s)
}

// ---- helpers ------------------------------------------------------------------------

var helpers = map[string][]cel.EnvOption{
	"drop": {cel.Function("drop",
		cel.Overload("drop_dyn_list", []*cel.Type{cel.DynType, cel.ListType(cel.StringType)}, cel.DynType,
			cel.BinaryBinding(func(v, paths ref.Val) ref.Val {
				ps, err := stringList(paths)
				if err != nil {
					return types.NewErr("drop: %v", err)
				}
				out := native(v)
				if n := countNodes(out, MaxHelperNodes+1); n > MaxHelperNodes {
					return types.NewErr("drop: input has more than %d nodes", MaxHelperNodes)
				}
				for _, p := range ps {
					out = dropPath(out, splitPath(p))
				}
				return celAdapter.NativeToValue(out)
			})),
		cel.FunctionDocs("drop(value, paths) removes the fields at each path. A path is dot-separated; * matches every key of a map or every element of a list.",
			`drop(response.result.structuredContent, ["issues.*.self", "meta"])`))},
	"dropSchema": {cel.Function("dropSchema",
		cel.Overload("dropSchema_dyn_list", []*cel.Type{cel.DynType, cel.ListType(cel.StringType)}, cel.DynType,
			cel.BinaryBinding(func(v, paths ref.Val) ref.Val {
				ps, err := stringList(paths)
				if err != nil {
					return types.NewErr("dropSchema: %v", err)
				}
				out := native(v)
				if n := countNodes(out, MaxHelperNodes+1); n > MaxHelperNodes {
					return types.NewErr("dropSchema: input has more than %d nodes", MaxHelperNodes)
				}
				for _, p := range ps {
					var err error
					if out, err = dropSchemaPath(out, splitPath(p)); err != nil {
						return types.NewErr("dropSchema: %v", err)
					}
				}
				return celAdapter.NativeToValue(out)
			})),
		cel.FunctionDocs("dropSchema(schema, paths) removes from a JSON Schema the properties drop() removes from matching values, including from required lists.",
			`dropSchema(tool.outputSchema, ["issues.*.self"])`))},
	"elicit": {cel.Function("elicit",
		cel.Overload("elicit_string_string", []*cel.Type{cel.StringType, cel.StringType}, cel.DynType,
			cel.BinaryBinding(func(k, m ref.Val) ref.Val { return elicitMarker(k, m, nil) })),
		cel.Overload("elicit_string_string_string", []*cel.Type{cel.StringType, cel.StringType, cel.StringType}, cel.DynType,
			cel.FunctionBinding(func(args ...ref.Val) ref.Val { return elicitMarker(args[0], args[1], args[2]) })),
		cel.Overload("elicit_string_string_map", []*cel.Type{cel.StringType, cel.StringType, cel.MapType(cel.StringType, cel.DynType)}, cel.DynType,
			cel.FunctionBinding(func(args ...ref.Val) ref.Val { return elicitTemplate(args[0], args[1], args[2], nil) })),
		cel.Overload("elicit_string_string_map_string", []*cel.Type{cel.StringType, cel.StringType, cel.MapType(cel.StringType, cel.DynType), cel.StringType}, cel.DynType,
			cel.FunctionBinding(func(args ...ref.Val) ref.Val { return elicitTemplate(args[0], args[1], args[2], args[3]) })),
		cel.FunctionDocs("elicit(key, message, error?) or elicit(key, template, values, error?) asks the user to confirm. With values, each {name} in the template is replaced by the value quoted, truncated to 80 characters and with control characters escaped, so argument text cannot pose as the gateway's own wording. The gateway returns input_required when the client supports elicitation, and an error result with the optional message otherwise.",
			`elicit("confirm", "Delete page {id}?", {"id": request.params.arguments.id})`))},
	"slugUris": {
		cel.Variable(muxVar, cel.DynType),
		cel.Macros(cel.GlobalMacro("slugUris", 1, func(eh cel.MacroExprFactory, _ ast.Expr, args []ast.Expr) (ast.Expr, *common.Error) {
			return eh.NewCall(slugUrisFun, args[0], eh.NewIdent(muxVar)), nil
		})),
		cel.Function(slugUrisFun,
			cel.Overload("slugUris_string_dyn", []*cel.Type{cel.StringType, cel.DynType}, cel.StringType,
				cel.BinaryBinding(func(text, mux ref.Val) ref.Val {
					s, _ := text.Value().(string)
					return types.String(rewriteOwnedURIs(s, native(mux)))
				}))),
	},
}

// ElicitKey marks an elicit() result inside a rule's `result` value.
const ElicitKey = "$elicit"

func elicitMarker(k, m, e ref.Val) ref.Val {
	out := map[string]any{ElicitKey: k.Value(), "message": m.Value()}
	if e != nil {
		out["error"] = e.Value()
	}
	return celAdapter.NativeToValue(out)
}

func stringList(v ref.Val) ([]string, error) {
	l, ok := v.(traits.Lister)
	if !ok {
		return nil, errors.New("paths must be a list of strings")
	}
	var out []string
	it := l.Iterator()
	for it.HasNext() == types.True {
		s, ok := it.Next().Value().(string)
		if !ok {
			return nil, errors.New("paths must be strings")
		}
		out = append(out, s)
	}
	return out, nil
}

// native converts a CEL value to plain Go (map[string]any, []any, string,
// float64/int64, bool, nil, time.Time, time.Duration).
func native(v ref.Val) any {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case types.Null:
		return nil
	case types.String:
		return string(t)
	case types.Bool:
		return bool(t)
	case types.Int:
		return int64(t)
	case types.Uint:
		return uint64(t)
	case types.Double:
		return float64(t)
	case types.Bytes:
		return []byte(t)
	case types.Timestamp:
		return t.Time
	case types.Duration:
		return t.Duration
	case traits.Mapper:
		out := map[string]any{}
		it := t.Iterator()
		for it.HasNext() == types.True {
			k := it.Next()
			out[fmt.Sprint(k.Value())] = native(t.Get(k))
		}
		return out
	case traits.Lister:
		out := []any{}
		it := t.Iterator()
		for it.HasNext() == types.True {
			out = append(out, native(it.Next()))
		}
		return out
	}
	if x, err := v.ConvertToNative(reflect.TypeOf((*any)(nil)).Elem()); err == nil {
		return x
	}
	return v.Value()
}

// ---- activation -----------------------------------------------------------------------

// activation resolves the tree's branches. The item's branch is built when a
// rule reads it, from the item's current fields, so a rule sees what the
// rules before it changed; everything else comes from roots.
type activation struct {
	vars map[string]any
	kind ItemKind
	item func(name string) (any, bool)
}

func (a *activation) ResolveName(name string) (any, bool) {
	if a.item != nil && name == string(a.kind) {
		if a.kind != KindServer {
			return subject(a.kind, a.item), true
		}
		// The server's own hook changes the server branch: its fields as
		// rules have left them, over what every hook is told about it.
		base, _ := a.vars["server"].(map[string]any)
		out := make(map[string]any, len(base)+4)
		for k, v := range base {
			out[k] = v
		}
		for _, f := range []string{"title", "description", "instructions", "upstream"} {
			if v, ok := a.item(f); ok {
				out[f] = v
			} else {
				delete(out, f)
			}
		}
		return out, true
	}
	v, ok := a.vars[name]
	return v, ok
}

func (a *activation) Parent() interpreter.Activation { return nil }

var _ interpreter.Activation = (*activation)(nil)

// timeOrZero keeps `now` typed even when unset.
// timeOrZero gives rules the request's time, or the real time when the
// gateway did not set one. An unset clock must not read as 1970: a rule
// about a change freeze or office hours would then silently never match.
func timeOrZero(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t
}

// MaxHelperNodes bounds the input a single drop or dropSchema call may
// traverse. CEL's cost tracker also charges every call by its input size,
// so repeated calls add up against the rule's cost limit.
const MaxHelperNodes = 100000

// countNodes counts values in a JSON-like tree, stopping at limit.
func countNodes(v any, limit int) int {
	n := 0
	var walk func(any)
	walk = func(x any) {
		if n >= limit {
			return
		}
		n++
		switch t := x.(type) {
		case map[string]any:
			for _, c := range t {
				walk(c)
			}
		case []any:
			for _, c := range t {
				walk(c)
			}
		}
	}
	walk(v)
	return n
}

func refNodes(v ref.Val) uint64 {
	return uint64(countNodes(native(v), MaxHelperNodes+1))
}

// helperCosts charges drop and dropSchema to CEL's cost tracker in
// proportion to the nodes they visit and the paths they apply.
var helperCosts = []interpreter.CostTrackerOption{
	interpreter.OverloadCostTracker("drop_dyn_list", func(args []ref.Val, _ ref.Val) *uint64 {
		c := refNodes(args[0]) * uint64(max(1, listLen(args[1])))
		return &c
	}),
	interpreter.OverloadCostTracker("dropSchema_dyn_list", func(args []ref.Val, _ ref.Val) *uint64 {
		c := refNodes(args[0]) * uint64(max(1, listLen(args[1])))
		return &c
	}),
}

func listLen(v ref.Val) int {
	if l, ok := v.(traits.Lister); ok {
		if n, ok := l.Size().(types.Int); ok {
			return int(n)
		}
	}
	return 0
}

var placeholderRe = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

// elicitTemplate fills {name} placeholders with quoted, bounded values.
func elicitTemplate(k, tpl, vals, e ref.Val) ref.Val {
	values, _ := native(vals).(map[string]any)
	t, _ := tpl.Value().(string)
	msg := placeholderRe.ReplaceAllStringFunc(t, func(m string) string {
		name := m[1 : len(m)-1]
		v, ok := values[name]
		if !ok {
			return m
		}
		s := fmt.Sprint(v)
		if r := []rune(s); len(r) > 80 {
			s = string(r[:80]) + "…"
		}
		return strconv.Quote(s)
	})
	return elicitMarker(k, types.String(msg), e)
}

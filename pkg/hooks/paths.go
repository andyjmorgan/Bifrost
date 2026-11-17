package hooks

import (
	"fmt"
	"sort"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/ast"
)

// A rule reads the tree through member chains: tool.name, tool.?title,
// tool._meta['team/owner'], response.result.content[0].text. checkPaths
// walks every chain an expression holds against the hook's tree when the
// rule is saved, so a name the tree does not have is refused then, with the
// names it does have, and not found out when a request arrives.
//
// It follows what a macro binds. In response.result.content.exists(c,
// c.type == 'image') the variable c is one element of the list, so c.type
// is checked against a content block. A variable that happens to share a
// branch's name is the variable, not the branch.

// step is one move along a chain: into a key, or into an element of a list.
type step struct {
	key      string
	elem     bool
	optional bool // read with .? or [?…], or tested with has()
	tested   bool // the step has() asks about
	index    ast.Expr
}

// chainOf returns the identifier a member chain starts from and its steps.
func chainOf(e ast.Expr) (root string, steps []step, ok bool) {
	switch e.Kind() {
	case ast.IdentKind:
		return strings.TrimPrefix(e.AsIdent(), "."), nil, true // .request is request, named from the root
	case ast.SelectKind:
		sel := e.AsSelect()
		root, steps, ok = chainOf(sel.Operand())
		if !ok {
			return "", nil, false
		}
		return root, append(steps, step{key: sel.FieldName(), optional: sel.IsTestOnly(), tested: sel.IsTestOnly()}), true
	case ast.CallKind:
		c := e.AsCall()
		fn := c.FunctionName()
		if (fn != "_?._" && fn != "_[_]" && fn != "_[?_]") || len(c.Args()) != 2 {
			return "", nil, false
		}
		root, steps, ok = chainOf(c.Args()[0])
		if !ok {
			return "", nil, false
		}
		idx := c.Args()[1]
		if idx.Kind() == ast.LiteralKind {
			if s, isStr := idx.AsLiteral().Value().(string); isStr {
				return root, append(steps, step{key: s, optional: fn != "_[_]"}), true
			}
		}
		if fn == "_?._" {
			return "", nil, false
		}
		return root, append(steps, step{elem: true, optional: fn == "_[?_]", index: idx}), true
	}
	return "", nil, false
}

// bound is what a macro's variable stands for: a place in the tree and how
// to say where it is, or nothing when the engine cannot tell.
type bound struct {
	node *Node
	path string
}

type pathCheck struct {
	spec *HookSpec
	// negated is how many ! the walk is inside. !has(x) says x is absent,
	// so it does not guard a read of x.
	negated   int
	unguarded map[string]bool
	tested    map[string]bool
	err       error
}

// checkPaths refuses an expression that reads something the hook's tree
// does not have. It also returns the optional values the expression reads
// with a plain "." and no guard (reading one that is absent fails the rule,
// and a rule that fails denies), and the values it tests with has().
func checkPaths(a *cel.Ast, spec *HookSpec) (unguarded, tested []string, err error) {
	c := &pathCheck{spec: spec, unguarded: map[string]bool{}, tested: map[string]bool{}}
	c.walk(a.NativeRep().Expr(), map[string]bound{})
	if c.err != nil {
		return nil, nil, c.err
	}
	for p := range c.unguarded {
		if !c.tested[p] { // has(x) && x: the same expression guards it
			unguarded = append(unguarded, p)
		}
	}
	for p := range c.tested {
		tested = append(tested, p)
	}
	sort.Strings(unguarded)
	sort.Strings(tested)
	return unguarded, tested, nil
}

// walk visits an expression. scope holds the variables the macros around it
// have bound.
func (c *pathCheck) walk(e ast.Expr, scope map[string]bound) {
	if c.err != nil || e == nil {
		return
	}
	if _, steps, ok := chainOf(e); ok {
		if len(steps) > 0 {
			c.chain(e, scope)
		} else if e.Kind() == ast.IdentKind {
			c.whole(strings.TrimPrefix(e.AsIdent(), "."), nil, scope)
		}
		return
	}
	switch e.Kind() {
	case ast.SelectKind:
		c.walk(e.AsSelect().Operand(), scope)
	case ast.CallKind:
		call := e.AsCall()
		if call.IsMemberFunction() {
			c.walk(call.Target(), scope)
		}
		if call.FunctionName() == "!_" {
			c.negated++
			defer func() { c.negated-- }()
		}
		for _, a := range call.Args() {
			c.walk(a, scope)
		}
	case ast.ListKind:
		for _, x := range e.AsList().Elements() {
			c.walk(x, scope)
		}
	case ast.MapKind:
		for _, en := range e.AsMap().Entries() {
			me := en.AsMapEntry()
			c.walk(me.Key(), scope)
			c.walk(me.Value(), scope)
		}
	case ast.StructKind:
		for _, f := range e.AsStruct().Fields() {
			c.walk(f.AsStructField().Value(), scope)
		}
	case ast.ComprehensionKind:
		comp := e.AsComprehension()
		c.walk(comp.IterRange(), scope)
		c.walk(comp.AccuInit(), scope)
		inner := make(map[string]bound, len(scope)+3)
		for k, v := range scope {
			inner[k] = v
		}
		over := c.resolve(comp.IterRange(), scope)
		elem, key := bound{}, bound{}
		if over.node != nil {
			switch over.node.Type {
			case "list":
				elem = bound{node: over.node.Items, path: over.path + "[]"}
			case "map":
				elem = bound{node: over.node.Values, path: over.path + "[]"}
			}
		}
		if comp.HasIterVar2() {
			// Two variables: the index or key, then the value.
			inner[comp.IterVar()], inner[comp.IterVar2()] = key, elem
		} else if over.node != nil && over.node.Type == "map" {
			inner[comp.IterVar()] = key // a macro over a map walks its keys
		} else {
			inner[comp.IterVar()] = elem
		}
		// cel.bind(x, init, body) binds x to init; other macros keep their
		// running result there.
		inner[comp.AccuVar()] = c.resolve(comp.AccuInit(), scope)
		c.walk(comp.LoopCondition(), inner)
		c.walk(comp.LoopStep(), inner)
		c.walk(comp.Result(), inner)
	}
}

// resolve says where in the tree an expression points, when it is a chain
// the tree describes.
func (c *pathCheck) resolve(e ast.Expr, scope map[string]bound) bound {
	root, steps, ok := chainOf(e)
	if !ok {
		return bound{}
	}
	cur, known := c.start(root, scope)
	if !known {
		return bound{}
	}
	for _, st := range steps {
		if cur.node == nil {
			return bound{}
		}
		next, _, _ := stepInto(cur, st)
		cur = next
	}
	return cur
}

// start is the place a chain begins: a macro's variable, or a branch of the
// tree.
func (c *pathCheck) start(root string, scope map[string]bound) (bound, bool) {
	if b, ok := scope[root]; ok {
		return b, true // the variable, even when it has a branch's name
	}
	if n := c.spec.Tree().Keys[root]; n != nil && !n.WriteOnly {
		return bound{node: n, path: root}, true
	}
	return bound{}, false
}

// stepInto takes one step from a place. missing is set when the step names
// a key a closed map does not have.
func stepInto(cur bound, st step) (next bound, missing bool, known bool) {
	n := cur.node
	switch {
	case st.elem:
		if n.Type == "list" {
			return bound{node: n.Items, path: cur.path + "[]"}, false, true
		}
		return bound{node: n.Values, path: cur.path + "[]"}, false, true
	case n.Keys != nil && n.Keys[st.key] != nil:
		return bound{node: n.Keys[st.key], path: cur.path + "." + st.key}, false, true
	case n.Open || n.Follows != "" || n.Type == "any":
		return bound{node: n.Values, path: cur.path + "." + st.key}, false, true
	case n.Type == "map":
		return bound{}, true, true
	}
	return bound{}, false, false
}

// whole notes a use of a place as a whole. request.flags is read by name:
// used any other way, such as 'serach' in request.flags, a typo would be
// false for ever.
func (c *pathCheck) whole(root string, steps []step, scope map[string]bound) {
	if _, shadowed := scope[root]; shadowed {
		return
	}
	if root == "request" && len(steps) == 1 && steps[0].key == "flags" && c.err == nil {
		c.err = fmt.Errorf("read a flag by name, as request.flags.%s; the flags are %s", Flags[0], strings.Join(Flags, ", "))
	}
}

// chain checks one member chain, taken as a whole.
func (c *pathCheck) chain(e ast.Expr, scope map[string]bound) {
	root, steps, _ := chainOf(e)
	for _, st := range steps {
		if st.index != nil {
			c.walk(st.index, scope) // a computed index is an expression of its own
		}
	}
	cur, known := c.start(root, scope)
	if !known {
		return // a name CEL has already refused, or will
	}
	if _, isVar := scope[root]; !isVar {
		c.whole(root, steps, scope)
		if len(steps) == 2 && root == "request" && steps[0].key == "flags" && steps[1].elem && c.err == nil {
			c.whole(root, steps[:1], scope) // request.flags[k]: not by name
		}
	}
	guarded := false // past a .? step the rest of the chain is optional too
	for _, st := range steps {
		if cur.node == nil || c.err != nil {
			return // past what the tree describes
		}
		next, missing, ok := stepInto(cur, st)
		if missing {
			c.err = noSuchKey(c.spec, cur.path, st.key, cur.node)
			return
		}
		if !ok || next.node == nil {
			return
		}
		if next.node.WriteOnly {
			c.err = fmt.Errorf("%s can be set here but not read", next.path)
			return
		}
		if st.optional {
			guarded = true
		}
		if next.node.Optional && !guarded {
			c.unguarded[next.path] = true
		}
		if st.tested && c.negated%2 == 0 {
			c.tested[next.path] = true
		}
		cur = next
	}
}

func noSuchKey(spec *HookSpec, parent, key string, node *Node) error {
	if parent == "request" && key == "upstream" {
		return fmt.Errorf("request.upstream is not available on %s: nothing has been sent to the server yet; it is there on the post hooks", spec.Name)
	}
	var keys []string
	for k, c := range node.Keys {
		if !c.WriteOnly {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if parent == "request.flags" {
		return fmt.Errorf("unknown flag %q; the flags are %s", key, strings.Join(keys, ", "))
	}
	return fmt.Errorf("%s has no %q; it has %s", parent, key, strings.Join(keys, ", "))
}

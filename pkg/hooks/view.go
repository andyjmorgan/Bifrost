package hooks

import (
	"bytes"
	"fmt"
	"strings"
)

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

// splitPath splits a dot-separated path. A backslash escapes the next
// character, so a property literally named "a.b" is written `a\.b`. Paths
// are kept as segment lists everywhere, never re-joined, so property names
// can never collide with path syntax.
func splitPath(p string) []string {
	var out []string
	var b strings.Builder
	esc := false
	for _, r := range p {
		switch {
		case esc:
			b.WriteRune(r)
			esc = false
		case r == '\\':
			esc = true
		case r == '.':
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	return append(out, b.String())
}

// ---- copy-on-write view ------------------------------------------------------------

// tombstone marks a deleted property in an overlay.
type tombstone struct{}

// View is an item as rules see it: the cached upstream object plus an
// overlay of changes keyed by property path. The upstream object is never
// written. Reads of untouched fields return the upstream values themselves;
// a field with changes beneath it is rebuilt by copying only the maps on the
// paths to those changes.
type View struct {
	base map[string]any
	over []overlayEntry
}

type overlayEntry struct {
	path []string
	val  any
}

// NewView wraps an upstream object. base must not be modified afterwards.
func NewView(base map[string]any) *View {
	if base == nil {
		base = map[string]any{}
	}
	return &View{base: base}
}

// Upstream is the item exactly as the upstream sent it.
func (v *View) Upstream() map[string]any { return v.base }

// Changed reports whether any rule changed the item.
func (v *View) Changed() bool { return len(v.over) > 0 }

// Overlay is the list of changes, in the order they were made.
func (v *View) Overlay() []overlayEntry { return v.over }

// Set records a change at path. Setting a value replaces anything recorded
// beneath it, so a whole-object replacement never leaves stale child changes.
func (v *View) Set(path []string, val any) {
	kept := v.over[:0:0]
	for _, e := range v.over {
		if !hasPrefix(e.path, path) {
			kept = append(kept, e)
		}
	}
	v.over = append(kept, overlayEntry{path: append([]string(nil), path...), val: val})
}

// Delete records the removal of a property.
func (v *View) Delete(path []string) { v.Set(path, tombstone{}) }

// Get returns the current value of a top-level field.
func (v *View) Get(field string) (any, bool) {
	var touched []overlayEntry
	for _, e := range v.over {
		if e.path[0] == field {
			touched = append(touched, e)
		}
	}
	bv, ok := v.base[field]
	if len(touched) == 0 {
		return bv, ok
	}
	cur := bv
	exists := ok
	for _, e := range touched {
		if len(e.path) == 1 {
			if _, del := e.val.(tombstone); del {
				cur, exists = nil, false
			} else {
				cur, exists = e.val, true
			}
			continue
		}
		cur = setIn(cur, e.path[1:], e.val)
		exists = true
	}
	return cur, exists
}

// Materialize returns the current object. With no changes it is the
// upstream object itself; otherwise a new top-level map whose untouched
// fields still point at the upstream values.
func (v *View) Materialize() map[string]any {
	if !v.Changed() {
		return v.base
	}
	out := make(map[string]any, len(v.base)+2)
	for k, x := range v.base {
		out[k] = x
	}
	seen := map[string]bool{}
	for _, e := range v.over {
		f := e.path[0]
		if seen[f] {
			continue
		}
		seen[f] = true
		if x, ok := v.Get(f); ok {
			out[f] = x
		} else {
			delete(out, f)
		}
	}
	return out
}

func hasPrefix(p, prefix []string) bool {
	if len(p) < len(prefix) {
		return false
	}
	for i := range prefix {
		if p[i] != prefix[i] {
			return false
		}
	}
	return true
}

// setIn returns a copy of root with val at path, copying only the maps along
// the path. Missing intermediate maps are created. A tombstone deletes.
func setIn(root any, path []string, val any) any {
	m, _ := root.(map[string]any)
	cp := make(map[string]any, len(m)+1)
	for k, x := range m {
		cp[k] = x
	}
	if len(path) == 1 {
		if _, del := val.(tombstone); del {
			delete(cp, path[0])
		} else {
			cp[path[0]] = val
		}
		return cp
	}
	cp[path[0]] = setIn(cp[path[0]], path[1:], val)
	return cp
}

// ---- drop and dropSchema ------------------------------------------------------------

// dropPath returns v without the fields at path, copying only what changes.
// "*" matches every key of a map or every element of a list.
func dropPath(v any, path []string) any {
	if len(path) == 0 {
		return v
	}
	seg, rest := path[0], path[1:]
	switch t := v.(type) {
	case map[string]any:
		cp := make(map[string]any, len(t))
		for k, x := range t {
			cp[k] = x
		}
		if seg == "*" {
			for k, x := range cp {
				if len(rest) == 0 {
					delete(cp, k)
				} else {
					cp[k] = dropPath(x, rest)
				}
			}
			return cp
		}
		x, ok := cp[seg]
		if !ok {
			return t
		}
		if len(rest) == 0 {
			delete(cp, seg)
		} else {
			cp[seg] = dropPath(x, rest)
		}
		return cp
	case []any:
		if seg != "*" {
			return t
		}
		cp := make([]any, len(t))
		for i, x := range t {
			cp[i] = dropPath(x, rest)
		}
		if len(rest) == 0 {
			return []any{}
		}
		return cp
	}
	return v
}

// dropSchemaPath removes from a JSON Schema what dropPath removes from a
// matching value: a name descends into properties, "*" into items (arrays)
// or into additionalProperties and every property (objects); a trailing
// "*" removes every property. The schema is copied, never modified. A path
// that has to pass through "$ref" is an error rather than a silent no-op.
func dropSchemaPath(s any, path []string) (any, error) {
	m, ok := s.(map[string]any)
	if !ok || len(path) == 0 {
		return s, nil
	}
	if _, ref := m["$ref"]; ref {
		return nil, fmt.Errorf("the schema uses $ref at %q; dropSchema does not follow references", strings.Join(path, "."))
	}
	cp := make(map[string]any, len(m))
	for k, x := range m {
		cp[k] = x
	}
	seg, rest := path[0], path[1:]
	if seg == "*" {
		if len(rest) == 0 {
			if _, ok := cp["properties"]; ok {
				cp["properties"] = map[string]any{}
			}
			delete(cp, "required")
			delete(cp, "minItems")
			delete(cp, "minProperties")
			return cp, nil
		}
		if items, ok := cp["items"]; ok {
			n, err := dropSchemaPath(items, rest)
			if err != nil {
				return nil, err
			}
			cp["items"] = n
		}
		if ap, ok := cp["additionalProperties"].(map[string]any); ok {
			n, err := dropSchemaPath(ap, rest)
			if err != nil {
				return nil, err
			}
			cp["additionalProperties"] = n
		}
		if props, ok := cp["properties"].(map[string]any); ok {
			np := make(map[string]any, len(props))
			for k, x := range props {
				n, err := dropSchemaPath(x, rest)
				if err != nil {
					return nil, err
				}
				np[k] = n
			}
			cp["properties"] = np
		}
		return cp, nil
	}
	props, ok := cp["properties"].(map[string]any)
	if !ok {
		return cp, nil
	}
	child, ok := props[seg]
	if !ok {
		return cp, nil
	}
	np := make(map[string]any, len(props))
	for k, x := range props {
		np[k] = x
	}
	if len(rest) == 0 {
		delete(np, seg)
		cp["required"] = without(cp["required"], seg)
		if cp["required"] == nil {
			delete(cp, "required")
		}
	} else {
		n, err := dropSchemaPath(child, rest)
		if err != nil {
			return nil, err
		}
		np[seg] = n
	}
	cp["properties"] = np
	return cp, nil
}

func without(req any, name string) any {
	l, ok := req.([]any)
	if !ok {
		if ls, ok2 := req.([]string); ok2 {
			l = make([]any, len(ls))
			for i, s := range ls {
				l[i] = s
			}
		} else {
			return req
		}
	}
	out := make([]any, 0, len(l))
	for _, x := range l {
		if x != name {
			out = append(out, x)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

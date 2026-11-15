package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// item is a non-tool item under evaluation.
type item struct {
	kind  ItemKind
	id    string // identity: name, uri or uriTemplate
	view  *View
	args  *argState // prompts only
	label string
	// bare marks a resource the server does not list. The gateway knows its
	// URI and nothing else, so a rule that reads its name, MIME type or any
	// other field fails, and a failing rule refuses. A deny written against
	// a name can never be assumed not to apply to something with no name.
	bare bool
}

func toMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}

func (it *item) resolve(name string) (any, bool) {
	if name == "upstream" {
		return derived(it.kind, it.view.Upstream()), true
	}
	if it.bare && name != "uri" {
		return nil, false
	}
	for _, f := range ItemFields[it.kind] {
		if f != name {
			continue
		}
		if name == "meta" {
			if v, ok := it.view.Get("_meta"); ok {
				return v, true
			}
			return map[string]any{}, true
		}
		if it.kind == KindSkill && (name == "name" || name == "description") {
			fm, _ := it.view.Upstream()["frontmatter"].(map[string]any)
			if v, ok := fm[name]; ok {
				return v, true
			}
			return "", true
		}
		if it.kind == KindPrompt && name == "arguments" && it.args != nil {
			return it.args.current(), true
		}
		v, ok := it.view.Get(name)
		if !ok {
			return defaultFor(name), true
		}
		return v, true
	}
	return nil, false
}

func derived(kind ItemKind, m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+2)
	for k, v := range m {
		out[k] = v
	}
	if meta, ok := m["_meta"]; ok {
		out["meta"] = meta
	} else {
		out["meta"] = map[string]any{}
	}
	if kind == KindSkill {
		fm, _ := m["frontmatter"].(map[string]any)
		out["name"], out["description"] = fm["name"], fm["description"]
	}
	for _, f := range ItemFields[kind] {
		if _, ok := out[f]; !ok {
			out[f] = defaultFor(f)
		}
	}
	return out
}

func (it *item) set(key string, val any) error {
	parts := splitPath(key)
	switch parts[0] {
	case "meta":
		if val == nil {
			it.view.Delete(append([]string{"_meta"}, parts[1:]...))
		} else {
			it.view.Set(append([]string{"_meta"}, parts[1:]...), val)
		}
	case "arguments":
		return it.args.set(parts[1], parts[2], val)
	default:
		it.view.Set(parts, val)
	}
	return nil
}

// listed is one item after filter and modify.
type listed struct {
	upstreamID string
	exposedID  string
	value      map[string]any
	args       *argState
}

// listItems runs a filter hook then a modify hook over items, in parallel,
// then sorts a-z by exposed identity and drops identity collisions.
func (s *Set) listItems(ctx context.Context, kind ItemKind, filter, modify string, idKey string, items []map[string]any, c Context, workers int) ([]listed, []string, []Hit) {
	type res struct {
		l      listed
		hidden bool
		issue  string
		hits   []Hit
	}
	results := make([]res, len(items))
	parallel(len(items), workers, func(i int) {
		base := items[i]
		id, _ := base[idKey].(string)
		it := &item{kind: kind, id: id, view: NewView(base), label: id}
		if kind == KindPrompt {
			it.args = newArgState(base)
		}
		vars := c.vars()
		fo := s.run(ctx, filter, id, it.resolve, vars, func(string, any) error { return nil })
		results[i].hits = append(results[i].hits, fo.hits...)
		if fo.denied {
			results[i].hidden = true
			if fo.err != nil {
				results[i].issue = fmt.Sprintf("%s: %v", id, fo.err)
			}
			return
		}
		if modify != "" {
			mo := s.run(ctx, modify, id, it.resolve, vars, it.set)
			results[i].hits = append(results[i].hits, mo.hits...)
			if mo.denied {
				results[i].hidden = true
				results[i].issue = fmt.Sprintf("%s: %s", id, errText(mo))
				return
			}
		}
		v := it.view.Materialize()
		if it.args != nil && it.args.touched {
			v = copyMap(v)
			v["arguments"] = it.args.current()
		}
		eid, _ := v[idKey].(string)
		if eid == "" {
			results[i].hidden = true
			results[i].issue = fmt.Sprintf("%s: %s cannot be empty", id, idKey)
			return
		}
		results[i].l = listed{upstreamID: id, exposedID: eid, value: v, args: it.args}
	})
	var out []listed
	var issues []string
	var hits []Hit
	byID := map[string][]int{}
	for i, r := range results {
		hits = append(hits, r.hits...)
		if r.issue != "" {
			issues = append(issues, r.issue)
		}
		if !r.hidden {
			byID[r.l.exposedID] = append(byID[r.l.exposedID], i)
		}
	}
	for id, idx := range byID {
		if len(idx) > 1 {
			issues = append(issues, fmt.Sprintf("collision: %d items expose %q; all are hidden", len(idx), id))
			continue
		}
		out = append(out, results[idx[0]].l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].exposedID < out[j].exposedID })
	sort.Strings(issues)
	return out, issues, hits
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ---- prompt arguments ---------------------------------------------------------------

// argState tracks prompt argument renames so prompts/get can reverse them.
type argState struct {
	base    []map[string]any
	cur     []string          // current names
	up      map[string]string // current -> upstream
	desc    map[string]string // current -> description override
	touched bool
}

func newArgState(base map[string]any) *argState {
	as := &argState{up: map[string]string{}, desc: map[string]string{}}
	if l, ok := base["arguments"].([]any); ok {
		for _, x := range l {
			m, _ := x.(map[string]any)
			n, _ := m["name"].(string)
			as.base = append(as.base, m)
			as.cur = append(as.cur, n)
			as.up[n] = n
		}
	}
	return as
}

func (as *argState) set(name, op string, val any) error {
	if _, ok := as.up[name]; !ok {
		return fmt.Errorf("no argument %q", name)
	}
	as.touched = true
	switch op {
	case "name":
		nn := val.(string)
		if nn == name {
			return nil
		}
		if _, exists := as.up[nn]; exists || nn == "" {
			return fmt.Errorf("renaming argument %q to %q collides", name, nn)
		}
		as.up[nn] = as.up[name]
		delete(as.up, name)
		if d, ok := as.desc[name]; ok {
			as.desc[nn] = d
			delete(as.desc, name)
		}
		for i, c := range as.cur {
			if c == name {
				as.cur[i] = nn
			}
		}
	case "description":
		as.desc[name] = val.(string)
	}
	return nil
}

func (as *argState) current() []any {
	out := make([]any, 0, len(as.cur))
	for i, c := range as.cur {
		m := copyMap(as.base[i])
		m["name"] = c
		if d, ok := as.desc[c]; ok {
			m["description"] = d
		}
		out = append(out, m)
	}
	return out
}

// reverse maps client argument names back to upstream names. Names that
// are upstream names of renamed arguments are refused.
func (as *argState) reverse(in map[string]string) (map[string]string, error) {
	out := map[string]string{}
	renamedFrom := map[string]bool{}
	for c, u := range as.up {
		if c != u {
			renamedFrom[u] = true
		}
	}
	for k, v := range in {
		u, ok := as.up[k]
		switch {
		case ok:
			out[u] = v
		case renamedFrom[k]:
			return nil, &ArgError{Msg: fmt.Sprintf("argument %q is not available on this prompt", k)}
		default:
			out[k] = v
		}
	}
	return out, nil
}

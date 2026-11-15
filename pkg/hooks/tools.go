package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"
	"sort"
	"sync"
	"weak"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- prepared catalog -----------------------------------------------------------------

// preparedTool is the rule-facing form of one upstream tool, built once per
// *mcp.Tool and cached for the tool's lifetime. Its maps reference the
// tool's own values (schemas are not copied), so views over it share every
// untouched value with the cached catalog. Catalog objects must not be
// modified once handed to the engine.
//
// The cache value must never reference the *mcp.Tool itself, or the weak
// key could never be collected (V2-F01).
type preparedTool struct {
	base     map[string]any
	derived  map[string]any
	props    map[string]any
	upOrder  []string
	required map[string]bool
	strict   bool
	patterns []*regexp.Regexp
	composed string
	indirect string
}

var prepCache sync.Map // weak.Pointer[mcp.Tool] -> *preparedTool

func prepare(t *mcp.Tool) *preparedTool {
	key := weak.Make(t)
	if v, ok := prepCache.Load(key); ok {
		return v.(*preparedTool)
	}
	p := buildPrepared(t)
	if actual, loaded := prepCache.LoadOrStore(key, p); loaded {
		return actual.(*preparedTool)
	}
	runtime.AddCleanup(t, func(k weak.Pointer[mcp.Tool]) { prepCache.Delete(k) }, key)
	return p
}

// schemaValue returns a schema as plain JSON values, sharing the original
// when it already is a map or a boolean.
func schemaValue(s any) any {
	switch t := s.(type) {
	case nil:
		return nil
	case map[string]any:
		return t
	case bool:
		return t
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	return decodeExact(b)
}

func decodeExact(b []byte) any {
	var v any
	dec := json.NewDecoder(bytesReader(b))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return nil
	}
	return v
}

func annotationsMap(a *mcp.ToolAnnotations) map[string]any {
	if a == nil {
		return nil
	}
	m := map[string]any{"readOnlyHint": a.ReadOnlyHint, "idempotentHint": a.IdempotentHint}
	if a.Title != "" {
		m["title"] = a.Title
	}
	if a.DestructiveHint != nil {
		m["destructiveHint"] = *a.DestructiveHint
	}
	if a.OpenWorldHint != nil {
		m["openWorldHint"] = *a.OpenWorldHint
	}
	return m
}

func buildPrepared(t *mcp.Tool) *preparedTool {
	base := map[string]any{"name": t.Name}
	if t.Title != "" {
		base["title"] = t.Title
	}
	if t.Description != "" {
		base["description"] = t.Description
	}
	if s := schemaValue(t.InputSchema); s != nil {
		base["inputSchema"] = s
	}
	if s := schemaValue(t.OutputSchema); s != nil {
		base["outputSchema"] = s
	}
	if a := annotationsMap(t.Annotations); a != nil {
		base["annotations"] = a
	}
	if t.Meta != nil {
		base["_meta"] = map[string]any(t.Meta)
	}
	if len(t.Icons) > 0 {
		base["icons"] = schemaValue(t.Icons)
	}
	p := &preparedTool{base: base, required: map[string]bool{}}
	p.derived = toolDerived(base)
	schema, _ := base["inputSchema"].(map[string]any)
	p.props, _ = schema["properties"].(map[string]any)
	for k := range p.props {
		p.upOrder = append(p.upOrder, k)
	}
	sort.Strings(p.upOrder)
	for _, r := range toStrings(schema["required"]) {
		p.required[r] = true
	}
	if ap, ok := schema["additionalProperties"].(bool); ok && !ap {
		p.strict = true
	}
	if pp, ok := schema["patternProperties"].(map[string]any); ok {
		for pat := range pp {
			if re, err := regexp.Compile(pat); err == nil {
				p.patterns = append(p.patterns, re)
			} else {
				p.composed = "patternProperties"
			}
		}
	}
	for _, kw := range []string{"allOf", "anyOf", "oneOf", "if", "then", "else", "dependentSchemas", "unevaluatedProperties", "$ref", "not"} {
		if _, ok := schema[kw]; ok && p.composed == "" {
			p.composed = kw
		}
	}
	for _, kw := range branchKeywords {
		if _, ok := schema[kw]; ok && p.indirect == "" {
			p.indirect = kw
		}
	}
	return p
}

// toolDerived adds the rule-facing views of a tool definition: annotations
// with the spec defaults filled in, annotationsRaw as sent, meta for _meta,
// and empty values for missing fields.
func toolDerived(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+4)
	for k, v := range m {
		out[k] = v
	}
	raw, _ := m["annotations"].(map[string]any)
	if raw == nil {
		raw = map[string]any{}
	}
	ann := map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": true, "title": ""}
	for k, v := range raw {
		ann[k] = v
	}
	out["annotations"] = ann
	out["annotationsRaw"] = raw
	if meta, ok := m["_meta"].(map[string]any); ok {
		out["meta"] = meta
	} else {
		out["meta"] = map[string]any{}
	}
	for _, k := range []string{"title", "description", "name"} {
		if _, ok := out[k]; !ok {
			out[k] = ""
		}
	}
	for _, k := range []string{"inputSchema", "outputSchema"} {
		if _, ok := out[k]; !ok {
			out[k] = map[string]any{}
		}
	}
	if _, ok := out["icons"]; !ok {
		out["icons"] = []any{}
	}
	return out
}

func defaultFor(name string) any {
	switch name {
	case "title", "description", "name", "uri", "uriTemplate", "mimeType":
		return ""
	case "icons", "arguments", "resources":
		return []any{}
	case "size":
		return int64(0)
	}
	return map[string]any{}
}

// ---- parameters -----------------------------------------------------------------------

// param is one parameter: its origin (an upstream key, or "" when the
// gateway added it), current name, visibility and changes. They are
// independent, so a hidden added parameter stays known (and refused) and
// is still stripped before forwarding.
type param struct {
	up     string
	name   string
	hidden bool
	desc   *string
	pin    any
	hasPin bool
	def    any
	hasDef bool
	enum   []any
	min    any
	max    any
	add    map[string]any
}

func (p *param) touched() bool {
	return p.desc != nil || p.hasPin || p.hasDef || p.enum != nil || p.min != nil || p.max != nil
}

// toolState is one tool under modification.
type toolState struct {
	pt     *preparedTool
	view   *View
	params []*param
	pTouch bool
	memo   map[string]any
}

func newToolState(pt *preparedTool) *toolState {
	ts := &toolState{pt: pt, view: NewView(pt.base)}
	for _, k := range pt.upOrder {
		ts.params = append(ts.params, &param{up: k, name: k})
	}
	return ts
}

func (ts *toolState) visible(name string) *param {
	for _, p := range ts.params {
		if !p.hidden && p.name == name {
			return p
		}
	}
	return nil
}

func (ts *toolState) any(name string) *param {
	if p := ts.visible(name); p != nil {
		return p
	}
	for _, p := range ts.params {
		if p.name == name {
			return p
		}
	}
	return nil
}

func (ts *toolState) changed() { ts.memo = nil }

// propSchema is a parameter's exposed schema. Untouched parameters return
// the upstream value itself, so booleans stay booleans and pointers are
// shared. A changed false schema stays false (allOf: [false]).
func (ts *toolState) propSchema(p *param) any {
	var orig any
	if p.add != nil {
		orig = p.add
	} else {
		orig = ts.pt.props[p.up]
	}
	if !p.touched() {
		return orig
	}
	var cp map[string]any
	switch o := orig.(type) {
	case map[string]any:
		cp = make(map[string]any, len(o)+2)
		for k, v := range o {
			cp[k] = v
		}
	case bool:
		cp = map[string]any{}
		if !o {
			cp["allOf"] = []any{false}
		}
	default:
		cp = map[string]any{}
	}
	if p.desc != nil {
		cp["description"] = *p.desc
	}
	if p.hasPin {
		cp["const"] = p.pin
	}
	if p.hasDef {
		cp["default"] = p.def
	}
	if p.enum != nil {
		cp["enum"] = p.enum
	}
	if p.min != nil {
		cp["minimum"] = p.min
	}
	if p.max != nil {
		cp["maximum"] = p.max
	}
	return cp
}

// inputSchema is the exposed schema, sharing every untouched value.
func (ts *toolState) inputSchema() any {
	orig, _ := ts.view.Get("inputSchema")
	if !ts.pTouch {
		return orig
	}
	schema, _ := orig.(map[string]any)
	cp := make(map[string]any, len(schema)+1)
	for k, v := range schema {
		cp[k] = v
	}
	if cp["type"] == nil {
		cp["type"] = "object"
	}
	props := map[string]any{}
	var req []any
	for _, p := range ts.params {
		if p.hidden {
			continue
		}
		props[p.name] = ts.propSchema(p)
		if p.up != "" && ts.pt.required[p.up] && !p.hasDef && !p.hasPin {
			req = append(req, p.name)
		}
	}
	cp["properties"] = props
	if len(req) > 0 {
		cp["required"] = req
	} else {
		delete(cp, "required")
	}
	return cp
}

func (ts *toolState) paramsVar() map[string]any {
	out := map[string]any{}
	for _, p := range ts.params {
		if !p.hidden {
			out[p.name] = ts.propSchema(p)
		}
	}
	return out
}

var toolFieldSet = func() map[string]bool {
	m := map[string]bool{}
	for _, f := range ItemFields[KindTool] {
		m[f] = true
	}
	return m
}()

// resolve answers item-field lookups for tool hooks, memoized until the
// next change.
func (ts *toolState) resolve(name string) (any, bool) {
	if name == "upstream" {
		return ts.pt.derived, true
	}
	if !toolFieldSet[name] {
		return nil, false
	}
	if v, ok := ts.memo[name]; ok {
		return v, true
	}
	var v any
	switch name {
	case "params":
		v = ts.paramsVar()
	case "inputSchema":
		v = ts.inputSchema()
	case "annotations", "annotationsRaw", "meta":
		if !ts.view.Changed() {
			v = ts.pt.derived[name]
		} else {
			v = toolDerived(ts.view.Materialize())[name]
		}
	default:
		if !ts.view.Changed() {
			v = ts.pt.derived[name]
		} else if x, ok := ts.view.Get(name); ok {
			v = x
		} else {
			v = defaultFor(name)
		}
	}
	if ts.memo == nil {
		ts.memo = map[string]any{}
	}
	ts.memo[name] = v
	return v, true
}

func toStrings(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// branchKeywords can name properties outside "properties" and "required".
// Parameter changes on a schema using them are refused, not half-applied.
var branchKeywords = []string{"allOf", "anyOf", "oneOf", "not", "if", "then", "else", "dependentRequired", "dependentSchemas", "dependencies", "patternProperties", "propertyNames", "$ref", "unevaluatedProperties"}

// setParam applies one params.<p>.<op> field.
func (ts *toolState) setParam(name, op string, val any) error {
	if ts.pt.indirect != "" {
		return fmt.Errorf("the input schema uses %q, which can name parameters indirectly; parameter changes are not supported on this tool", ts.pt.indirect)
	}
	ts.pTouch = true
	ts.changed()
	if op == "add" {
		if ts.visible(name) != nil {
			return fmt.Errorf("parameter %q already exists", name)
		}
		schema, _ := val.(map[string]any)
		ts.params = append(ts.params, &param{name: name, add: schema})
		return nil
	}
	p := ts.visible(name)
	if p == nil && (op == "pin" || op == "default") {
		p = ts.any(name)
	}
	if p == nil {
		return fmt.Errorf("no parameter %q", name)
	}
	switch op {
	case "name":
		nn := val.(string)
		if nn == "" {
			return fmt.Errorf("parameter name cannot be empty")
		}
		if nn != p.name && ts.visible(nn) != nil {
			return fmt.Errorf("renaming %q to %q collides with an existing parameter", p.name, nn)
		}
		p.name = nn
	case "description":
		s := val.(string)
		p.desc = &s
	case "hide":
		p.hidden = val.(bool)
	case "pin":
		p.pin, p.hasPin = val, true
	case "default":
		p.def, p.hasDef = val, true
	case "enum":
		p.enum = val.([]any)
	case "minimum":
		p.min = val
	case "maximum":
		p.max = val
	}
	return nil
}

// set applies one tools/modify field.
func (ts *toolState) set(key string, val any) error {
	ts.changed()
	parts := splitPath(key)
	switch parts[0] {
	case "params":
		return ts.setParam(parts[1], parts[2], val)
	case "meta":
		path := append([]string{"_meta"}, parts[1:]...)
		if val == nil {
			ts.view.Delete(path)
		} else {
			ts.view.Set(path, val)
		}
	default:
		ts.view.Set(parts, val)
	}
	return nil
}

// ---- the transform ------------------------------------------------------------------

// ToolTransform records how one exposed tool maps back to its upstream tool.
type ToolTransform struct {
	Upstream    string            `json:"upstream"`
	Exposed     string            `json:"exposed"`
	Params      map[string]string `json:"params,omitempty"`
	Added       []string          `json:"added,omitempty"`
	Hidden      map[string]string `json:"hidden,omitempty"`
	Pins        map[string]any    `json:"pins,omitempty"`
	VisiblePins map[string]any    `json:"visiblePins,omitempty"`
	Defaults    map[string]any    `json:"defaults,omitempty"`
	HiddenDefs  map[string]any    `json:"hiddenDefaults,omitempty"`
	Enums       map[string][]any  `json:"enums,omitempty"`
	Minimum     map[string]any    `json:"minimum,omitempty"`
	Maximum     map[string]any    `json:"maximum,omitempty"`
	pt          *preparedTool
	upKeys      map[string]bool
	required    []string
}

// finish builds the exposed tool and its transform. The output copies the
// tool struct and replaces only changed fields.
func (ts *toolState) finish(orig *mcp.Tool, filledByCall map[string]bool) (*mcp.Tool, *ToolTransform, error) {
	pt := ts.pt
	tr := &ToolTransform{Upstream: orig.Name, Params: map[string]string{}, Hidden: map[string]string{}, Pins: map[string]any{}, VisiblePins: map[string]any{},
		Defaults: map[string]any{}, HiddenDefs: map[string]any{}, Enums: map[string][]any{}, Minimum: map[string]any{}, Maximum: map[string]any{}, pt: pt, upKeys: map[string]bool{}}
	for _, k := range pt.upOrder {
		tr.upKeys[k] = true
	}
	for _, p := range ts.params {
		if p.hasPin && p.up != "" {
			tr.Pins[p.up] = p.pin
		}
		if p.hidden {
			tr.Hidden[p.name] = p.up
			if p.up != "" && p.hasDef {
				tr.HiddenDefs[p.up] = p.def
			}
			if p.up != "" && pt.required[p.up] && !p.hasPin && !p.hasDef {
				if !filledByCall[p.name] && !filledByCall[p.up] {
					return nil, nil, fmt.Errorf("parameter %q is required upstream and hidden without a pin, a default or a tools/call rule that sets it", p.up)
				}
				tr.required = append(tr.required, p.up)
			}
			continue
		}
		if p.up == "" {
			tr.Added = append(tr.Added, p.name)
		} else {
			tr.Params[p.name] = p.up
		}
		if p.hasDef {
			tr.Defaults[p.name] = p.def
		}
		if p.hasPin {
			tr.VisiblePins[p.name] = p.pin
		}
		if p.enum != nil {
			tr.Enums[p.name] = p.enum
		}
		if p.min != nil {
			tr.Minimum[p.name] = p.min
		}
		if p.max != nil {
			tr.Maximum[p.name] = p.max
		}
	}
	if !ts.view.Changed() && !ts.pTouch {
		tr.Exposed = orig.Name
		return orig, tr, nil
	}
	out := *orig
	m := ts.view.Materialize()
	out.Name, _ = m["name"].(string)
	out.Title, _ = m["title"].(string)
	out.Description, _ = m["description"].(string)
	if out.Name == "" {
		return nil, nil, fmt.Errorf("tool name cannot be empty")
	}
	if ts.pTouch {
		out.InputSchema = ts.inputSchema()
	} else if s, ok := m["inputSchema"]; ok {
		out.InputSchema = s
	}
	if s, ok := m["outputSchema"]; ok {
		out.OutputSchema = s
	} else {
		out.OutputSchema = nil
	}
	if a, ok := m["annotations"].(map[string]any); ok {
		var ann mcp.ToolAnnotations
		b, _ := json.Marshal(a)
		if err := json.Unmarshal(b, &ann); err != nil {
			return nil, nil, fmt.Errorf("annotations: %v", err)
		}
		out.Annotations = &ann
	} else {
		out.Annotations = nil
	}
	if meta, ok := m["_meta"].(map[string]any); ok && len(meta) > 0 {
		out.Meta = mcp.Meta(meta)
	} else {
		out.Meta = nil
	}
	tr.Exposed = out.Name
	return &out, tr, nil
}

// ---- calls ----------------------------------------------------------------------------

// ArgError is an invalid argument; the gateway returns it as invalid params.
type ArgError struct{ Msg string }

func (e *ArgError) Error() string { return e.Msg }

func (tr *ToolTransform) exposed(key string) bool {
	if _, ok := tr.Params[key]; ok {
		return true
	}
	for _, a := range tr.Added {
		if a == key {
			return true
		}
	}
	return false
}

func (tr *ToolTransform) isHidden(k string) bool {
	_, ok := tr.Hidden[k]
	return ok
}

// effectiveArgs builds the arguments call rules see, under exposed names:
// client keys checked (hidden, renamed-away and, under
// additionalProperties: false, undefined keys refused), defaults filled,
// visible pins applied, constraints checked. issue is set when
// additionalProperties could not be enforced.
func (tr *ToolTransform) effectiveArgs(in map[string]any) (map[string]any, string, error) {
	out := map[string]any{}
	issue := ""
	for k, v := range in {
		switch {
		case tr.exposed(k):
		case tr.isHidden(k) || tr.upKeys[k]:
			return nil, "", &ArgError{Msg: fmt.Sprintf("parameter %q is not available on this tool", k)}
		case tr.pt != nil && tr.pt.strict:
			// additionalProperties is decided by properties and
			// patternProperties at its own level only; composition
			// keywords elsewhere cannot widen it (JSON Schema core).
			matched := false
			for _, re := range tr.pt.patterns {
				if re.MatchString(k) {
					matched = true
					break
				}
			}
			if !matched {
				return nil, "", &ArgError{Msg: fmt.Sprintf("parameter %q is not defined on this tool", k)}
			}
		}
		out[k] = v
	}
	for k, d := range tr.Defaults {
		if _, ok := out[k]; !ok {
			out[k] = d
		}
	}
	for k, p := range tr.VisiblePins {
		if v, ok := out[k]; ok && !jsonEqual(v, p) {
			return nil, "", &ArgError{Msg: fmt.Sprintf("parameter %q must be %s", k, mustJSON(p))}
		}
		out[k] = p
	}
	return out, issue, tr.checkConstraints(out)
}

func (tr *ToolTransform) checkConstraints(args map[string]any) error {
	for k, allowed := range tr.Enums {
		v, ok := args[k]
		if !ok {
			continue
		}
		found := false
		for _, a := range allowed {
			if jsonEqual(a, v) {
				found = true
				break
			}
		}
		if !found {
			return &ArgError{Msg: fmt.Sprintf("parameter %q must be one of %s", k, mustJSON(allowed))}
		}
	}
	for k, min := range tr.Minimum {
		if v, ok := args[k]; ok {
			if c, ok := compareNumbers(v, min); !ok || c < 0 {
				return &ArgError{Msg: fmt.Sprintf("parameter %q must be at least %v", k, min)}
			}
		}
	}
	for k, max := range tr.Maximum {
		if v, ok := args[k]; ok {
			if c, ok := compareNumbers(v, max); !ok || c > 0 {
				return &ArgError{Msg: fmt.Sprintf("parameter %q must be at most %v", k, max)}
			}
		}
	}
	return nil
}

// patchable reports whether a tools/call rule may set args.<key>.
func (tr *ToolTransform) patchable(key string) error {
	if tr.exposed(key) || tr.isHidden(key) {
		return nil
	}
	if tr.upKeys[key] {
		return fmt.Errorf("args.%s names an upstream parameter that is exposed under another name; patch it by its exposed name", key)
	}
	return nil
}

// upstreamArgs reverses the exposed arguments: keys mapped back, every
// added parameter removed (visible or hidden), hidden defaults filled, pins
// forced. Pins always win.
func (tr *ToolTransform) upstreamArgs(args map[string]any) map[string]any {
	out := map[string]any{}
	added := map[string]bool{}
	for _, k := range tr.Added {
		added[k] = true
	}
	for k, v := range args {
		switch {
		case added[k]:
		case tr.Params[k] != "":
			out[tr.Params[k]] = v
		case tr.isHidden(k):
			if u := tr.Hidden[k]; u != "" {
				out[u] = v
			}
		default:
			out[k] = v
		}
	}
	for u, d := range tr.HiddenDefs {
		if _, ok := out[u]; !ok {
			out[u] = d
		}
	}
	for u, p := range tr.Pins {
		out[u] = p
	}
	return out
}

// missingRequired lists hidden upstream-required keys absent from the
// final arguments.
func (tr *ToolTransform) missingRequired(up map[string]any) []string {
	var out []string
	for _, k := range tr.required {
		if _, ok := up[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ---- listing --------------------------------------------------------------------------

// ExposedTool is one tool after filter and modify.
type ExposedTool struct {
	Tool      *mcp.Tool
	Transform *ToolTransform
	Changed   bool
	Hits      []Hit
}

// ToolList is the result of listing one server's tools.
type ToolList struct {
	Tools  []ExposedTool
	Hidden []string
	Issues []string
	Hits   []Hit
}

// ListTools runs tools/filter then tools/modify over a catalog, one item
// per goroutine (each writes only its own slot), then reduces: collisions
// hide every tool involved, output is sorted by exposed name.
func (s *Set) ListTools(ctx context.Context, cat []*mcp.Tool, c Context, workers int) ToolList {
	return s.listTools(ctx, cat, c, workers, "")
}

// listTools with only != "" evaluates just the upstream tools of that name.
// Calls use it when no rule can change a tool's name.
func (s *Set) listTools(ctx context.Context, cat []*mcp.Tool, c Context, workers int, only string) ToolList {
	type res struct {
		et     ExposedTool
		hidden bool
		issue  string
		hits   []Hit
		skip   bool
	}
	filledByCall := s.callFilled("tools/call")
	results := make([]res, len(cat))
	vars := c.vars()
	prof := s.profile()
	parallel(len(cat), workers, func(i int) {
		t := cat[i]
		if only != "" && t.Name != only {
			results[i].skip = true
			return
		}
		ph := prof.phase()
		ts := newToolState(prepare(t))
		ph.mark("convert")
		fo := s.run(ctx, "tools/filter", t.Name, ts.resolve, vars, func(string, any) error { return nil })
		ph.mark("filter")
		results[i].hits = append(results[i].hits, fo.hits...)
		if fo.denied {
			results[i].hidden = true
			if fo.err != nil {
				results[i].issue = fmt.Sprintf("%s: %v", t.Name, fo.err)
			}
			return
		}
		mo := s.run(ctx, "tools/modify", t.Name, ts.resolve, vars, ts.set)
		ph.mark("modify")
		results[i].hits = append(results[i].hits, mo.hits...)
		if mo.denied {
			results[i].hidden = true
			results[i].issue = fmt.Sprintf("%s: %s", t.Name, errText(mo))
			return
		}
		out, tr, err := ts.finish(t, filledByCall)
		ph.mark("finish")
		if err != nil {
			results[i].hidden = true
			results[i].issue = fmt.Sprintf("%s: %v", t.Name, err)
			return
		}
		results[i].et = ExposedTool{Tool: out, Transform: tr, Changed: out != t}
	})
	red := prof.phase()
	defer red.mark("reduce")
	var tl ToolList
	byName := map[string][]int{}
	for i, r := range results {
		if r.skip {
			continue
		}
		tl.Hits = append(tl.Hits, r.hits...)
		if r.issue != "" {
			tl.Issues = append(tl.Issues, r.issue)
		}
		if r.hidden {
			tl.Hidden = append(tl.Hidden, cat[i].Name)
			continue
		}
		byName[r.et.Tool.Name] = append(byName[r.et.Tool.Name], i)
	}
	for name, idx := range byName {
		if len(idx) > 1 {
			var ups []string
			for _, i := range idx {
				ups = append(ups, cat[i].Name)
				tl.Hidden = append(tl.Hidden, cat[i].Name)
			}
			sort.Strings(ups)
			tl.Issues = append(tl.Issues, fmt.Sprintf("name collision: %v all expose %q; all are hidden", ups, name))
			continue
		}
		tl.Tools = append(tl.Tools, results[idx[0]].et)
	}
	sort.Slice(tl.Tools, func(i, j int) bool { return tl.Tools[i].Tool.Name < tl.Tools[j].Tool.Name })
	sort.Strings(tl.Hidden)
	sort.Strings(tl.Issues)
	return tl
}

// renames reports whether any enforce rule could change a tool's name.
func (s *Set) renames() bool {
	if s == nil {
		return false
	}
	for _, r := range s.byHook["tools/modify"] {
		if r.audit {
			continue
		}
		for _, t := range r.then {
			if t.key == "name" {
				return true
			}
		}
	}
	return false
}

func errText(o outcome) string {
	if o.err != nil {
		return o.err.Error()
	}
	return o.reason
}

// callFilled lists parameter keys an enforce rule of a call hook sets.
// Audit rules never supply anything.
func (s *Set) callFilled(hook string) map[string]bool {
	out := map[string]bool{}
	if s == nil {
		return out
	}
	for _, r := range s.byHook[hook] {
		if r.audit {
			continue
		}
		for _, t := range r.then {
			if p := splitPath(t.key); len(p) == 2 && p[0] == "args" {
				out[p[1]] = true
			}
		}
	}
	return out
}

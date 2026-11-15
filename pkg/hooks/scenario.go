package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Scenario is a self-contained test: servers with rules and catalogs, a
// request context, and steps with expectations. hookctl reads scenarios as
// JSON (one object, an array, or one object per line).
type Scenario struct {
	Name    string           `json:"name"`
	IDs     []string         `json:"ids,omitempty"`
	Route   string           `json:"route,omitempty"` // direct (default) | mux | search
	Caller  map[string]any   `json:"caller,omitempty"`
	Client  map[string]any   `json:"client,omitempty"`
	Now     string           `json:"now,omitempty"` // RFC 3339
	Limits  Limits           `json:"limits,omitempty"`
	Secret  string           `json:"secret,omitempty"`
	Servers []ScenarioServer `json:"servers"`
	Steps   []Step           `json:"steps"`
	// Dir resolves catalogRef paths; set by the loader.
	Dir string `json:"-"`
}

// ScenarioServer is one entitlement in a scenario.
type ScenarioServer struct {
	Slug        string          `json:"slug"`
	Entitlement map[string]any  `json:"entitlement,omitempty"`
	Info        ServerInfo      `json:"info,omitempty"`
	Generation  string          `json:"generation,omitempty"`
	Unavailable bool            `json:"unavailable,omitempty"`
	Rules       json.RawMessage `json:"rules,omitempty"`
	Catalog     Catalog         `json:"catalog,omitempty"`
	// CatalogRef loads tools from a file: "path#key" where key is a
	// top-level array of tools (or of {"tool": ...} wrappers).
	CatalogRef string `json:"catalogRef,omitempty"`
	// Limit caps how many tools CatalogRef loads (0 = all).
	Limit int `json:"limit,omitempty"`
	// MetaAllow, when set, reduces upstream _meta to these keys, as the
	// gateway does when it fetches a catalog.
	MetaAllow []string `json:"metaAllow,omitempty"`
}

// Step is one operation and what to expect from it.
type Step struct {
	Op       string          `json:"op"`
	Params   json.RawMessage `json:"params,omitempty"`
	Upstream json.RawMessage `json:"upstream,omitempty"` // the upstream's reply, when the step forwards
	Caller   map[string]any  `json:"caller,omitempty"`   // override for this step
	Client   map[string]any  `json:"client,omitempty"`   // override for this step
	Now      string          `json:"now,omitempty"`      // override for this step
	Slugs    []string        `json:"slugs,omitempty"`    // narrow the mux to these slugs (?slugs=)
	// Rules replaces a server's rules from this step on (slug -> rule list),
	// as a policy change between requests would.
	Rules map[string]json.RawMessage `json:"rules,omitempty"`
	// Generation replaces a server's policy generation from this step on.
	Generation map[string]string `json:"generation,omitempty"`
	Expect     []Expect          `json:"expect,omitempty"`
}

// Expect is one assertion on a step's output. Path addresses the step
// report's JSON: dot keys, [n] indexes, [*] collects every element.
type Expect struct {
	Path        string `json:"path"`
	Equals      any    `json:"equals,omitempty"`
	Contains    any    `json:"contains,omitempty"`    // string contains, or list contains element
	NotContains any    `json:"notContains,omitempty"` // negation of contains
	Exists      *bool  `json:"exists,omitempty"`
	Len         *int   `json:"len,omitempty"`
	Matches     string `json:"matches,omitempty"` // regexp on the string form
	hasEquals   bool
}

// UnmarshalJSON records whether equals was present (equals: null is valid).
func (e *Expect) UnmarshalJSON(b []byte) error {
	type plain Expect
	var p plain
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber() // expected numbers stay exact
	if err := dec.Decode(&p); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(b, &raw)
	_, p.hasEquals = raw["equals"]
	*e = Expect(p)
	return nil
}

// Report is a scenario's result.
type Report struct {
	Name     string         `json:"name"`
	IDs      []string       `json:"ids,omitempty"`
	Status   string         `json:"status"` // pass | fail | skip
	Reason   string         `json:"reason,omitempty"`
	Problems map[string]any `json:"problems,omitempty"` // per slug, rule compile problems
	Warnings map[string]any `json:"warnings,omitempty"` // per slug, problems in disabled rules
	Steps    []StepReport   `json:"steps"`
	Failures []string       `json:"failures,omitempty"`
	Elapsed  string         `json:"elapsed"`
}

// StepReport is one step's output.
type StepReport struct {
	Op      string   `json:"op"`
	Outcome Outcome  `json:"outcome"`
	Reply   *Outcome `json:"reply,omitempty"`
	Elapsed string   `json:"elapsed"`
}

// ErrSkip marks a scenario that cannot run here (a missing real fixture).
var ErrSkip = errors.New("skip")

// LoadScenarios reads a file of scenarios: an object, an array, or JSONL.
func LoadScenarios(path string) ([]*Scenario, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	ss, err := ParseScenarios(b)
	for _, s := range ss {
		s.Dir = filepath.Dir(path)
	}
	return ss, err
}

// ParseScenarios parses scenario JSON in any of the accepted shapes.
func ParseScenarios(b []byte) ([]*Scenario, error) {
	t := strings.TrimSpace(string(b))
	if strings.HasPrefix(t, "[") {
		var out []*Scenario
		return out, json.Unmarshal([]byte(t), &out)
	}
	dec := json.NewDecoder(strings.NewReader(t))
	var out []*Scenario
	for dec.More() {
		var s Scenario
		if err := dec.Decode(&s); err != nil {
			return out, err
		}
		out = append(out, &s)
	}
	return out, nil
}

// Run executes a scenario.
func (sc *Scenario) Run(ctx context.Context) Report {
	start := time.Now()
	rep := Report{Name: sc.Name, IDs: sc.IDs, Status: "pass", Problems: map[string]any{}}
	servers, err := sc.build(rep.Problems)
	for k, v := range rep.Problems {
		if slug, ok := strings.CutPrefix(k, "\x00warn:"); ok {
			if rep.Warnings == nil {
				rep.Warnings = map[string]any{}
			}
			rep.Warnings[slug] = v
			delete(rep.Problems, k)
		}
	}
	if errors.Is(err, ErrSkip) {
		rep.Status, rep.Reason = "skip", err.Error()
		rep.Elapsed = time.Since(start).String()
		return rep
	}
	if err != nil {
		rep.Status, rep.Reason = "fail", err.Error()
		rep.Elapsed = time.Since(start).String()
		return rep
	}
	secret := sc.Secret
	if secret == "" {
		secret = "scenario-secret"
	}
	eng := &Engine{Signer: Signer{Key: []byte(secret)}, Limits: sc.Limits}
	var outputs []any
	for i, st := range sc.Steps {
		req := Request{Route: sc.Route, Caller: sc.Caller, Client: sc.Client, Servers: servers}
		if req.Route == "" {
			req.Route = "direct"
		}
		req.Now = parseTime(sc.Now)
		if st.Caller != nil {
			req.Caller = st.Caller
		}
		if st.Client != nil {
			req.Client = st.Client
		}
		if st.Now != "" {
			req.Now = parseTime(st.Now)
		}
		for slug, raw := range st.Rules {
			if s := findServer(servers, slug); s != nil {
				rs, err := Parse(raw)
				if err != nil {
					rep.Failures = append(rep.Failures, fmt.Sprintf("step %d: rules for %s: %v", i, slug, err))
					continue
				}
				set, probs := Compile(rs, Options{CostLimit: sc.Limits.CostLimit})
				s.Rules, s.Broken = set, nil
				if probs != nil {
					s.Broken = probs
				}
			}
		}
		for slug, g := range st.Generation {
			if s := findServer(servers, slug); s != nil {
				s.Generation = g
			}
		}
		if len(st.Slugs) > 0 {
			var narrowed []*Server
			for _, want := range st.Slugs {
				if s := req.server(want); s != nil {
					narrowed = append(narrowed, s)
				}
			}
			req.Servers = narrowed
		}
		params, err := substitute(st.Params, outputs)
		if err != nil {
			rep.Failures = append(rep.Failures, fmt.Sprintf("step %d: %v", i, err))
			outputs = append(outputs, map[string]any{})
			continue
		}
		t0 := time.Now()
		sr := eng.step(ctx, req, st.Op, params, st.Upstream)
		sr.Elapsed = time.Since(t0).String()
		rep.Steps = append(rep.Steps, sr)
		var generic any
		b, _ := json.Marshal(sr)
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.UseNumber()
		_ = dec.Decode(&generic)
		outputs = append(outputs, generic)
		for _, ex := range st.Expect {
			if msg := ex.check(generic, map[string]any{"problems": rep.Problems, "warnings": rep.Warnings}); msg != "" {
				rep.Failures = append(rep.Failures, fmt.Sprintf("step %d (%s): %s", i, st.Op, msg))
			}
		}
	}
	if len(rep.Failures) > 0 {
		rep.Status = "fail"
	}
	rep.Elapsed = time.Since(start).String()
	return rep
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	}
	return t
}

func (sc *Scenario) build(problems map[string]any) ([]*Server, error) {
	var out []*Server
	for _, ss := range sc.Servers {
		// A scenario describes a server on the front door's protocol version
		// unless it says otherwise. The engine itself treats a missing
		// version as old; write "unknown" to test that.
		if ss.Info.ProtocolVersion == "" {
			ss.Info.ProtocolVersion = ProtocolCurrent
		}
		rs, err := Parse(ss.Rules)
		if err != nil {
			problems[ss.Slug] = err.Error()
			out = append(out, &Server{Slug: ss.Slug, Broken: err, Info: ss.Info})
			continue
		}
		set, probs := Compile(rs, Options{CostLimit: sc.Limits.CostLimit})
		s := &Server{Slug: ss.Slug, Entitlement: ss.Entitlement, Info: ss.Info, Rules: set, Generation: ss.Generation, Catalog: ss.Catalog, Unavailable: ss.Unavailable}
		if s.Entitlement == nil {
			s.Entitlement = map[string]any{"slug": ss.Slug}
		}
		if probs != nil {
			problems[ss.Slug] = probs
			s.Broken = probs
		}
		if set != nil && len(set.Warnings) > 0 {
			problems["\x00warn:"+ss.Slug] = set.Warnings
		}
		if ss.CatalogRef != "" {
			tools, err := loadToolRef(sc.Dir, ss.CatalogRef, ss.Limit)
			if err != nil {
				return nil, err
			}
			s.Catalog.Tools = append(s.Catalog.Tools, tools...)
		}
		if ss.MetaAllow != nil {
			s.Catalog = s.Catalog.StripMeta(ss.MetaAllow)
		}
		out = append(out, s)
	}
	return out, nil
}

func loadToolRef(dir, ref string, limit int) ([]*mcp.Tool, error) {
	file, key, _ := strings.Cut(ref, "#")
	p := filepath.Join(dir, file)
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: fixture %s not present", ErrSkip, file)
		}
		return nil, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return nil, err
	}
	if key == "" {
		key = "tools"
	}
	var items []json.RawMessage
	if err := json.Unmarshal(top[key], &items); err != nil {
		return nil, fmt.Errorf("%s#%s: %w", file, key, err)
	}
	var out []*mcp.Tool
	seen := map[string]int{}
	for _, it := range items {
		var w struct {
			Tool json.RawMessage `json:"tool"`
		}
		raw := it
		if json.Unmarshal(it, &w) == nil && len(w.Tool) > 0 {
			raw = w.Tool
		}
		var t mcp.Tool
		if err := json.Unmarshal(raw, &t); err != nil || t.Name == "" {
			continue
		}
		// Real corpora repeat names across servers; keep names unique.
		base := t.Name
		for n := 1; seen[t.Name] > 0; n++ {
			t.Name = fmt.Sprintf("%s_%d", base, n)
		}
		seen[t.Name]++
		out = append(out, &t)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// step dispatches one operation.
func (e *Engine) step(ctx context.Context, r Request, op string, params, upstream json.RawMessage) StepReport {
	sr := StepReport{Op: op}
	decode := func(v any) bool {
		if len(params) == 0 {
			return true
		}
		if err := json.Unmarshal(params, v); err != nil {
			sr.Outcome = errOutcome(CodeInvalidParams, "scenario params: "+err.Error())
			return false
		}
		return true
	}
	reply := func(o Outcome, f func(*CallContext) Outcome) {
		sr.Outcome = o
		if o.Forward != nil && len(upstream) > 0 {
			rp := f(o.Call)
			sr.Reply = &rp
		}
	}
	switch op {
	case "discover":
		sr.Outcome = e.Discover(ctx, r, "test")
	case "connect":
		var p struct {
			Slug string `json:"slug"`
		}
		if decode(&p) {
			s := r.server(p.Slug)
			if s == nil && len(r.Servers) == 1 {
				s = r.Servers[0]
			}
			if s == nil {
				sr.Outcome = errOutcome(CodeInvalidParams, "unknown slug")
				break
			}
			ci, hits := e.Connect(ctx, r, s, "test")
			sr.Outcome = Outcome{Result: ci, Hits: hits}
		}
	case "config":
		var p struct {
			Slug string `json:"slug"`
		}
		if decode(&p) {
			s := r.server(p.Slug)
			if s == nil && len(r.Servers) == 1 {
				s = r.Servers[0]
			}
			if s == nil {
				sr.Outcome = errOutcome(CodeInvalidParams, "unknown slug")
				break
			}
			sw, hits := e.Config(ctx, r, s)
			sr.Outcome = Outcome{Result: sw, Hits: hits}
		}
	case "tools/list":
		sr.Outcome = e.ListTools(ctx, r)
	case "tools/call":
		var p mcp.CallToolParamsRaw
		if decode(&p) {
			if r.Route == "mux" && p.Name == InstructionsTool {
				var a struct {
					Slug string `json:"slug"`
				}
				_ = json.Unmarshal(p.Arguments, &a)
				sr.Outcome = e.ServerInstructions(ctx, r, a.Slug)
				break
			}
			reply(e.CallTool(ctx, r, &p), func(c *CallContext) Outcome { return e.UpstreamToolReply(ctx, r, c, &p, upstream) })
		}
	case "prompts/list":
		sr.Outcome = e.ListPrompts(ctx, r)
	case "prompts/get":
		var p mcp.GetPromptParams
		if decode(&p) {
			reply(e.GetPrompt(ctx, r, &p), func(c *CallContext) Outcome { return e.UpstreamPromptReply(ctx, r, c, &p, upstream) })
		}
	case "resources/list":
		sr.Outcome = e.ListResources(ctx, r)
	case "resources/templates/list":
		sr.Outcome = e.ListTemplates(ctx, r)
	case "resources/read", "resources/directory/read":
		var p mcp.ReadResourceParams
		if decode(&p) {
			if op == "resources/read" {
				reply(e.ReadResource(ctx, r, &p), func(c *CallContext) Outcome { return e.UpstreamReadReply(ctx, r, c, &p, upstream) })
			} else {
				reply(e.ReadDirectory(ctx, r, &p), func(c *CallContext) Outcome { return e.UpstreamReadReply(ctx, r, c, &p, upstream) })
			}
		}
	case "skills/list":
		sr.Outcome = e.ListSkills(ctx, r)
	case "skills/get":
		var p struct {
			URI string `json:"uri"`
		}
		if decode(&p) {
			sr.Outcome = e.GetSkill(ctx, r, p.URI)
		}
	case "tool_search":
		var p struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if decode(&p) {
			sr.Outcome = e.ToolSearch(ctx, r, p.Query, p.Limit)
		}
	case "list_tools":
		sr.Outcome = e.ListToolsYAML(ctx, r)
	case "get_tool_definition":
		var p struct {
			Names []string `json:"names"`
		}
		if decode(&p) {
			sr.Outcome = e.ToolDefinitions(ctx, r, p.Names)
		}
	case "execute_tool":
		var p struct {
			Name           string               `json:"name"`
			Arguments      json.RawMessage      `json:"arguments"`
			RequestState   string               `json:"requestState"`
			InputResponses mcp.InputResponseMap `json:"inputResponses"`
		}
		if decode(&p) {
			call := &mcp.CallToolParamsRaw{Name: p.Name, Arguments: p.Arguments, RequestState: p.RequestState, InputResponses: p.InputResponses}
			reply(e.CallTool(ctx, r, call), func(c *CallContext) Outcome { return e.UpstreamToolReply(ctx, r, c, call, upstream) })
		}
	case "get_server_instructions":
		var p struct {
			Slug string `json:"slug"`
		}
		if decode(&p) {
			sr.Outcome = e.ServerInstructions(ctx, r, p.Slug)
		}
	default:
		sr.Outcome = errOutcome(CodeInvalidParams, "unknown op "+op)
	}
	return sr
}

// ---- substitution and assertions ----------------------------------------------------

var refRe = regexp.MustCompile(`"\$\{steps\.(\d+)\.([^}]+)\}"`)

// substitute replaces "${steps.N.path}" string values with the value at path
// in step N's report.
func substitute(raw json.RawMessage, outputs []any) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	var ferr error
	out := refRe.ReplaceAllStringFunc(string(raw), func(m string) string {
		g := refRe.FindStringSubmatch(m)
		n, _ := strconv.Atoi(g[1])
		if n >= len(outputs) {
			ferr = fmt.Errorf("reference to step %d, which has not run", n)
			return m
		}
		vals := lookup(outputs[n], g[2])
		if len(vals) == 0 {
			ferr = fmt.Errorf("reference steps.%d.%s found nothing", n, g[2])
			return m
		}
		b, _ := json.Marshal(vals[0])
		return string(b)
	})
	return json.RawMessage(out), ferr
}

// lookup resolves a path against generic JSON, returning every match.
func lookup(v any, path string) []any {
	cur := []any{v}
	for _, seg := range tokenize(path) {
		var next []any
		for _, c := range cur {
			switch {
			case seg == "[*]":
				if l, ok := c.([]any); ok {
					next = append(next, l...)
				}
			case strings.HasPrefix(seg, "["):
				i, _ := strconv.Atoi(strings.Trim(seg, "[]"))
				if l, ok := c.([]any); ok && i >= 0 && i < len(l) {
					next = append(next, l[i])
				}
			default:
				if m, ok := c.(map[string]any); ok {
					if x, ok := m[seg]; ok {
						next = append(next, x)
					}
				}
			}
		}
		cur = next
	}
	return cur
}

func tokenize(path string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	esc := false
	for _, r := range path {
		switch {
		case esc:
			b.WriteRune(r)
			esc = false
		case r == '\\':
			esc = true
		case r == '.':
			flush()
		case r == '[':
			flush()
			b.WriteRune(r)
		case r == ']':
			b.WriteRune(r)
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return out
}

func (ex Expect) check(out any, extra map[string]any) string {
	root := out
	if strings.HasPrefix(ex.Path, "problems") || strings.HasPrefix(ex.Path, "warnings") {
		b, _ := json.Marshal(extra)
		var g any
		_ = json.Unmarshal(b, &g)
		root = g
	}
	vals := lookup(root, ex.Path)
	collected := strings.Contains(ex.Path, "[*]")
	var v any
	if collected {
		v = vals
	} else if len(vals) > 0 {
		v = vals[0]
	}
	exists := len(vals) > 0
	if collected {
		exists = len(vals) > 0
	}
	if ex.Exists != nil && *ex.Exists != exists {
		return fmt.Sprintf("%s: exists=%v, want %v (value %s)", ex.Path, exists, *ex.Exists, short(v))
	}
	if ex.hasEquals && !jsonEqual(normJSON(v), normJSON(ex.Equals)) {
		return fmt.Sprintf("%s = %s, want %s", ex.Path, short(v), short(ex.Equals))
	}
	if ex.Contains != nil && !contains(v, ex.Contains) {
		return fmt.Sprintf("%s = %s, want it to contain %s", ex.Path, short(v), short(ex.Contains))
	}
	if ex.NotContains != nil && !exists {
		return fmt.Sprintf("%s is missing; notContains needs a present value", ex.Path)
	}
	if ex.NotContains != nil && contains(v, ex.NotContains) {
		return fmt.Sprintf("%s = %s, want it not to contain %s", ex.Path, short(v), short(ex.NotContains))
	}
	if ex.Len != nil {
		n := -1
		switch t := v.(type) {
		case []any:
			n = len(t)
		case map[string]any:
			n = len(t)
		case string:
			n = len(t)
		}
		if !exists {
			return fmt.Sprintf("%s is missing; len needs a present array, object or string", ex.Path)
		}
		if n < 0 {
			return fmt.Sprintf("%s = %s is not an array, object or string", ex.Path, short(v))
		}
		if n != *ex.Len {
			return fmt.Sprintf("%s has length %d, want %d", ex.Path, n, *ex.Len)
		}
	}
	if ex.Matches != "" {
		re, err := regexp.Compile(ex.Matches)
		if err != nil {
			return "bad matches regexp: " + err.Error()
		}
		s := fmt.Sprint(v)
		if sv, ok := v.(string); ok {
			s = sv
		} else {
			b, _ := json.Marshal(v)
			s = string(b)
		}
		if !re.MatchString(s) {
			return fmt.Sprintf("%s = %s, want match /%s/", ex.Path, short(v), ex.Matches)
		}
	}
	return ""
}

func normJSON(v any) any {
	b, _ := json.Marshal(v)
	return decodeExact(b)
}

func contains(v, want any) bool {
	switch t := v.(type) {
	case string:
		ws, ok := want.(string)
		return ok && strings.Contains(t, ws)
	case []any:
		for _, x := range t {
			if jsonEqual(normJSON(x), normJSON(want)) {
				return true
			}
			if s, ok := x.(string); ok {
				if ws, ok := want.(string); ok && s == ws {
					return true
				}
			}
		}
		return false
	case map[string]any:
		ws, ok := want.(string)
		if ok {
			_, has := t[ws]
			return has
		}
	}
	return reflect.DeepEqual(v, want)
}

func short(v any) string {
	b, _ := json.Marshal(v)
	s := string(b)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func findServer(ss []*Server, slug string) *Server {
	for _, s := range ss {
		if s.Slug == slug {
			return s
		}
	}
	return nil
}

// ---- exports for the end-to-end harness ----------------------------------------------
//
// pkg/hooks/harness replays every scenario over real HTTP. These wrappers
// give it the runner's own building, substitution and assertion code, so an
// end-to-end run means exactly what a Run means.

// Built is a scenario's servers as Run builds them, with the compile
// problems and warnings that `problems.*` and `warnings.*` expectations read.
type Built struct {
	Servers  []*Server
	Problems map[string]any
	Warnings map[string]any
}

// Build compiles the scenario's servers exactly as Run does. The error wraps
// ErrSkip when a real fixture is not present.
func (sc *Scenario) Build() (Built, error) {
	b := Built{Problems: map[string]any{}}
	servers, err := sc.build(b.Problems)
	for k, v := range b.Problems {
		if slug, ok := strings.CutPrefix(k, "\x00warn:"); ok {
			if b.Warnings == nil {
				b.Warnings = map[string]any{}
			}
			b.Warnings[slug] = v
			delete(b.Problems, k)
		}
	}
	b.Servers = servers
	return b, err
}

// NewEngine returns the engine Run uses for this scenario.
func (sc *Scenario) NewEngine() *Engine {
	secret := sc.Secret
	if secret == "" {
		secret = "scenario-secret"
	}
	return &Engine{Signer: Signer{Key: []byte(secret)}, Limits: sc.Limits}
}

// StepTime is the clock Run gives a step.
func (sc *Scenario) StepTime(st Step) time.Time {
	if st.Now != "" {
		return parseTime(st.Now)
	}
	return parseTime(sc.Now)
}

// SubstituteRefs resolves "${steps.N.path}" references against earlier step
// outputs, as Run does.
func SubstituteRefs(raw json.RawMessage, outputs []any) (json.RawMessage, error) {
	return substitute(raw, outputs)
}

// Check evaluates the expectation against a step's output. extra holds
// "problems" and "warnings". It returns "" when the expectation holds.
func (ex Expect) Check(out any, extra map[string]any) string { return ex.check(out, extra) }

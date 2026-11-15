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
	"iter"
	"sort"
)

// Scenario is a self-contained test: servers with rules and catalogs, a
// request context, and steps with expectations. hookctl reads scenarios as
// JSON (one object, an array, or one object per line).
type Scenario struct {
	Name  string   `json:"name"`
	IDs   []string `json:"ids,omitempty"`
	Route string   `json:"route,omitempty"` // direct (default) or mux; "search" is the older spelling of mux with the search flag
	// Flags are the switches the request carries, such as {"search": true}
	// or {"cli": true}. They can be set for either route.
	Flags   map[string]bool  `json:"flags,omitempty"`
	Caller  map[string]any   `json:"caller,omitempty"`
	Client  map[string]any   `json:"client,omitempty"`
	Now     string           `json:"now,omitempty"` // RFC 3339
	Limits  Limits           `json:"limits,omitempty"`
	Secret  string           `json:"secret,omitempty"`
	Servers []ScenarioServer `json:"servers"`
	Steps   []Step           `json:"steps"`
	// Upstream says what the servers are. "" or "mock": servers described
	// by their catalogs, answering with each step's scripted reply.
	// "reference": the real reference servers, one run per server language;
	// the servers then carry only a slug and rules.
	Upstream string `json:"upstream,omitempty"`
	// Gateway sets transport options of the gateway in front of the engine.
	Gateway *GatewayOptions `json:"gateway,omitempty"`
	// Exclude lists the harnesses that must not run this case, each with
	// the reason. See Exclusion.
	Exclude []Exclusion `json:"exclude,omitempty"`
	// Only marks an outlier: a case that makes sense on one harness alone.
	// It runs on that harness and nowhere else, and is kept apart from the
	// common set.
	Only *OnlyHarness `json:"only,omitempty"`
	// Dir resolves catalogRef paths; set by the loader.
	Dir string `json:"-"`
}

// GatewayOptions are transport settings of the gateway a case runs behind.
// They mean nothing to the engine itself.
type GatewayOptions struct {
	// Token is the bearer token every request must carry.
	Token string `json:"token,omitempty"`
	// KeepAliveMs is how often an open stream gets a comment line.
	KeepAliveMs int `json:"keepAliveMs,omitempty"`
}

// OnlyHarness names the one harness an outlier case is for, and why it is
// specific to it.
type OnlyHarness struct {
	Harness string `json:"harness"`
	Reason  string `json:"reason"`
}

// Exclusion keeps one harness from running a case or a step, and says why.
// A case is never dropped from the set because a harness cannot run it; it
// stays, with an exclusion.
//
// Harness names: "engine" (this package's runner, no wire), the client
// harnesses "raw", "go-sdk", "python-sdk" and "typescript-sdk", and the
// server harnesses "server:go", "server:python" and "server:typescript".
// Harness may be one name or a list of names that share the reason.
type Exclusion struct {
	Harness HarnessNames `json:"harness"`
	Reason  string       `json:"reason"`
}

// HarnessNames is one harness name or a list of them.
type HarnessNames []string

// UnmarshalJSON accepts "name" or ["name", ...].
func (h *HarnessNames) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*h = HarnessNames{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return fmt.Errorf("exclude.harness must be a name or a list of names")
	}
	*h = many
	return nil
}

// Excluded returns the reason a harness is excluded by a list, or "".
func Excluded(list []Exclusion, harness string) string {
	for _, e := range list {
		for _, h := range e.Harness {
			if h == harness {
				if e.Reason == "" {
					return "(no reason given)"
				}
				return e.Reason
			}
		}
	}
	return ""
}

// MockReply scripts what a mock upstream does besides returning the step's
// `upstream` result.
type MockReply struct {
	// Stream answers with an SSE stream, not one JSON body.
	Stream bool `json:"stream,omitempty"`
	// DelayMs waits before the first byte; GapMs waits between events.
	DelayMs int `json:"delayMs,omitempty"`
	GapMs   int `json:"gapMs,omitempty"`
	// Progress events are sent before the result, under the token the
	// request carried. With no token they are not sent, unless
	// ForceProgress is set.
	Progress      []MockProgress `json:"progress,omitempty"`
	ForceProgress bool           `json:"forceProgress,omitempty"`
	// Notifications are sent after the progress events.
	Notifications []MockNotification `json:"notifications,omitempty"`
	// NoResponse ends the stream without a response.
	NoResponse bool `json:"noResponse,omitempty"`
	// Hang keeps the request open until the caller goes away.
	Hang bool `json:"hang,omitempty"`
	// Error answers with a JSON-RPC error instead of a result.
	Error *RPCError `json:"error,omitempty"`
	// Status, when not 200, answers with that HTTP status and Body.
	Status int    `json:"status,omitempty"`
	Body   string `json:"body,omitempty"`
	// RawBody replaces the whole JSON-RPC message with these bytes.
	RawBody string `json:"rawBody,omitempty"`
	// Then holds the results of the second and later requests a step
	// forwards, when a client makes several rounds of one request.
	Then []json.RawMessage `json:"then,omitempty"`
}

// MockProgress is one scripted progress event.
type MockProgress struct {
	Progress float64 `json:"progress"`
	Total    float64 `json:"total,omitempty"`
	Message  string  `json:"message,omitempty"`
}

// MockNotification is one scripted notification of any other kind.
type MockNotification struct {
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

// transport reports whether the script needs a real connection: things the
// engine never sees.
func (m *MockReply) transport() bool {
	return m != nil && (m.DelayMs > 0 || m.Hang || m.Status != 0 || m.Body != "" || m.RawBody != "")
}

// events reports whether the script holds stream events the engine handles.
func (m *MockReply) events() bool {
	return m != nil && (len(m.Progress) > 0 || len(m.Notifications) > 0 || m.NoResponse || m.Error != nil)
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

	// Mock scripts the mock upstream's reply beyond its result.
	Mock *MockReply `json:"mock,omitempty"`
	// Progress makes the client ask for progress.
	Progress bool `json:"progress,omitempty"`
	// Answers are the client's scripted answers to the questions it is
	// asked during this step, in order: {"action": ..., "content": ...}.
	// The client makes every round of the request itself.
	Answers []map[string]any `json:"answers,omitempty"`
	// Token is the bearer token the client sends, when the gateway wants one.
	Token *string `json:"token,omitempty"`
	// RawBody is an HTTP request body sent as it is, in place of op and
	// params. Only the raw client can send it.
	RawBody string `json:"rawBody,omitempty"`
	// Exclude lists the harnesses that must not make this step, each with
	// the reason. A client harness's excluded step is made by the raw
	// client in its place, so later steps still have what they need.
	Exclude []Exclusion `json:"exclude,omitempty"`
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
	// NotEquals is the negation of equals. Like every negative check it
	// needs a present value; use exists: false to assert absence.
	NotEquals any `json:"notEquals,omitempty"`
	// NotMatches is the negation of matches.
	NotMatches string `json:"notMatches,omitempty"`
	// Keys compares an object's keys, in any order.
	Keys []string `json:"keys,omitempty"`
	// Set compares a list's elements, in any order.
	Set []any `json:"set,omitempty"`
	// Has needs a list to hold an element that has every field given here
	// (or, for a value that is not an object, an equal element).
	Has any `json:"has,omitempty"`
	// HasText and LacksText look for text anywhere in the value's JSON form.
	HasText   []string `json:"hasText,omitempty"`
	LacksText []string `json:"lacksText,omitempty"`
	// Refused, on the path "client", needs the request to have been refused
	// (a JSON-RPC error, or a result with isError true), or not.
	Refused      *bool `json:"refused,omitempty"`
	hasEquals    bool
	hasNotEquals bool
	hasSet       bool
	hasKeys      bool
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
	_, p.hasNotEquals = raw["notEquals"]
	_, p.hasSet = raw["set"]
	_, p.hasKeys = raw["keys"]
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
	// Client is what a client would have received for the step: the reply
	// when the request was forwarded and answered, the outcome otherwise.
	Client *ClientView `json:"client,omitempty"`
	// Progress is the progress events relayed to the client, in order.
	Progress []any `json:"progress"`
	// Hits is every hit of the step, request side then reply side.
	Hits []Hit `json:"hits,omitempty"`
}

// ClientView is a step's final message as a client sees it.
type ClientView struct {
	Result any       `json:"result,omitempty"`
	Error  *RPCError `json:"error,omitempty"`
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
	if err := checkScenarioKeys(b); err != nil {
		return nil, err
	}
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

// Run executes a scenario on the engine harness: the engine alone, with no
// wire. A case that needs a wire must carry an exclusion for "engine"; it is
// then skipped with that reason. With HOOKS_RUN_EXCLUDED set, an excluded
// case is run anyway where it can be, and fails if it passes, so a stale
// exclusion is found.
func (sc *Scenario) Run(ctx context.Context) Report {
	excluded, needs := Excluded(sc.Exclude, "engine"), sc.NeedsWire()
	stop := func(status, reason string) Report {
		r := Report{Name: sc.Name, IDs: sc.IDs, Status: status, Reason: reason, Problems: map[string]any{}}
		if status == "fail" {
			r.Failures = []string{reason}
		}
		return r
	}
	if sc.Only != nil && sc.Only.Harness != "engine" {
		return stop("skip", "an outlier for the "+sc.Only.Harness+" harness: "+sc.Only.Reason)
	}
	switch {
	case needs != "" && excluded == "":
		return stop("fail", "this case needs a wire ("+needs+") and has no exclude entry for the engine harness")
	case excluded != "" && (needs != "" || os.Getenv("HOOKS_RUN_EXCLUDED") == ""):
		return stop("skip", "excluded for the engine harness: "+excluded)
	case excluded != "":
		if r := sc.run(ctx); r.Status == "pass" {
			return stop("fail", "excluded for the engine harness, but it passes; remove the exclusion: "+excluded)
		}
		return stop("skip", "excluded for the engine harness, and it does not pass: "+excluded)
	}
	return sc.run(ctx)
}

func (sc *Scenario) run(ctx context.Context) Report {
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
		req := Request{Route: sc.Route, Flags: sc.Flags, Caller: sc.Caller, Client: sc.Client, Servers: servers}
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
		if Excluded(st.Exclude, "engine") != "" && os.Getenv("HOOKS_RUN_EXCLUDED") == "" {
			outputs = append(outputs, map[string]any{})
			continue
		}
		t0 := time.Now()
		sr := eng.runStep(ctx, req, st, params)
		sr.Elapsed = time.Since(t0).String()
		sr.derive()
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

func first(vals []any) any {
	if len(vals) == 0 {
		return nil
	}
	return vals[0]
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
			case strings.HasPrefix(seg, "[") && strings.Contains(seg, "="):
				// [field=value]: the elements whose field has that value.
				field, want, _ := strings.Cut(strings.Trim(seg, "[]"), "=")
				if l, ok := c.([]any); ok {
					for _, x := range l {
						if m, ok := x.(map[string]any); ok {
							if got, has := m[field]; has && plainString(got) == want {
								next = append(next, x)
							}
						}
					}
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
	esc, inBracket := false, false
	for _, r := range path {
		switch {
		case esc:
			b.WriteRune(r)
			esc = false
		case inBracket && r != ']':
			b.WriteRune(r) // a selector's value may hold dots and slashes
		case r == '\\':
			esc = true
		case r == '.':
			flush()
		case r == '[':
			flush()
			b.WriteRune(r)
			inBracket = true
		case r == ']':
			b.WriteRune(r)
			flush()
			inBracket = false
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
		if vals == nil {
			vals = []any{} // nothing collected is the empty list, not null
		}
		v = vals
	} else if len(vals) > 0 {
		v = vals[0]
	}
	exists := len(vals) > 0
	if ex.Exists != nil && *ex.Exists != exists {
		return fmt.Sprintf("%s: exists=%v, want %v (value %s)", ex.Path, exists, *ex.Exists, short(v))
	}
	if collected && len(vals) == 0 && (ex.hasEquals || ex.hasSet || ex.Len != nil || ex.NotContains != nil) {
		// A [*] path that collects nothing looks the same whether its list
		// is empty or was never there. They are not the same: an error with
		// no result must not satisfy "the listing holds none of these". So
		// the list the path collects from has to be present.
		src := ex.Path[:strings.Index(ex.Path, "[*]")]
		from := lookup(root, src)
		if _, isList := first(from).([]any); !isList {
			return fmt.Sprintf("%s collects from %s, which is missing or not a list; an empty result needs a present, empty list", ex.Path, src)
		}
		exists = true // a present list with nothing to collect is a value: []
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
	return ex.checkMore(v, exists)
}

// checkMore holds the operators added for end-to-end cases.
func (ex Expect) checkMore(v any, exists bool) string {
	text := func() string {
		if s, ok := v.(string); ok {
			return s
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	// A negative check needs something to check. On a missing value it
	// would pass whatever happened, including an error that returned no
	// result at all. Use "exists": false to assert absence.
	missing := func(op string) string {
		return fmt.Sprintf("%s is missing; %s needs a present value (use \"exists\": false to assert absence)", ex.Path, op)
	}
	if ex.hasNotEquals {
		if !exists {
			return missing("notEquals")
		}
		if jsonEqual(normJSON(v), normJSON(ex.NotEquals)) {
			return fmt.Sprintf("%s = %s, want anything else", ex.Path, short(v))
		}
	}
	if ex.NotMatches != "" {
		re, err := regexp.Compile(ex.NotMatches)
		if err != nil {
			return "bad notMatches regexp: " + err.Error()
		}
		if !exists {
			return missing("notMatches")
		}
		if re.MatchString(text()) {
			return fmt.Sprintf("%s = %s, want no match of /%s/", ex.Path, short(v), ex.NotMatches)
		}
	}
	if ex.hasKeys {
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s = %s is not an object; keys needs one", ex.Path, short(v))
		}
		got := make([]string, 0, len(m))
		for k := range m {
			got = append(got, k)
		}
		want := append([]string(nil), ex.Keys...)
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			return fmt.Sprintf("%s has keys %s, want %s", ex.Path, short(got), short(want))
		}
	}
	if ex.hasSet {
		l, ok := v.([]any)
		if !ok && exists {
			return fmt.Sprintf("%s = %s is not a list; set needs one", ex.Path, short(v))
		}
		if !ok {
			// A missing value is not an empty list. Without this, "set": []
			// would pass against an error that returned no listing at all.
			return fmt.Sprintf("%s is missing; set needs a list, and an empty one must be present", ex.Path)
		}
		canon := func(items []any) []string {
			out := make([]string, 0, len(items))
			for _, x := range items {
				b, _ := json.Marshal(normJSON(x))
				out = append(out, string(b))
			}
			sort.Strings(out)
			return out
		}
		if strings.Join(canon(l), "\x00") != strings.Join(canon(ex.Set), "\x00") {
			return fmt.Sprintf("%s = %s, want the set %s", ex.Path, short(v), short(ex.Set))
		}
	}
	if ex.Has != nil {
		l, _ := v.([]any)
		found := false
		for _, x := range l {
			if subset(ex.Has, x) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Sprintf("%s = %s, want an element with %s", ex.Path, short(v), short(ex.Has))
		}
	}
	for _, part := range ex.HasText {
		if !strings.Contains(text(), part) {
			return fmt.Sprintf("%s = %s, want it to hold the text %q", ex.Path, short(v), part)
		}
	}
	for _, part := range ex.LacksText {
		if !exists {
			return missing("lacksText")
		}
		if strings.Contains(text(), part) {
			return fmt.Sprintf("%s = %s, want it not to hold the text %q", ex.Path, short(v), part)
		}
	}
	if ex.Refused != nil {
		// Refused means the gateway or the server said no: a JSON-RPC error
		// with a code, or a tool result marked as an error. A client that
		// merely failed, with no code, has not been refused; counting it
		// would let a crashed or timed-out client pass a refusal check.
		m, _ := v.(map[string]any)
		failed := false
		if em, ok := m["error"].(map[string]any); ok {
			code := fmt.Sprint(em["code"])
			failed = code != "" && code != "0" && code != "<nil>"
		}
		res, _ := m["result"].(map[string]any)
		isErr, _ := res["isError"].(bool)
		if got := failed || isErr; got != *ex.Refused {
			return fmt.Sprintf("%s: refused=%v, want %v (value %s)", ex.Path, got, *ex.Refused, short(v))
		}
	}
	return ""
}

// subset reports whether got has everything want has: every field of an
// object, recursively, and an equal value otherwise.
func subset(want, got any) bool {
	wm, ok := want.(map[string]any)
	if !ok {
		return jsonEqual(normJSON(got), normJSON(want))
	}
	gm, ok := got.(map[string]any)
	if !ok {
		return false
	}
	for k, w := range wm {
		g, has := gm[k]
		if !has || !subset(w, g) {
			return false
		}
	}
	return true
}

// plainString is a value's text for a [field=value] selector: a string as
// it is, anything else in its JSON form.
func plainString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
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

// ---- end-to-end additions to the format ----------------------------------------------

// NeedsWire reports why a case cannot run on the engine harness, or "". The
// engine harness is the engine alone: no client, no connection, no real
// server.
func (sc *Scenario) NeedsWire() string {
	switch {
	case sc.Upstream != "" && sc.Upstream != "mock":
		return "it runs against the " + sc.Upstream + " servers"
	case sc.Gateway != nil:
		return "it sets gateway transport options"
	}
	for i, st := range sc.Steps {
		if Excluded(st.Exclude, "engine") != "" {
			continue
		}
		at := fmt.Sprintf("step %d ", i)
		switch {
		case len(st.Answers) > 0:
			return at + "has a client answer questions"
		case st.Token != nil:
			return at + "sends a bearer token"
		case st.RawBody != "":
			return at + "sends a raw HTTP body"
		case st.Mock.transport():
			return at + "scripts the upstream's transport"
		}
		for _, ex := range st.Expect {
			for _, root := range []string{"wire", "request", "questions", "upstream", "gateway", "sdk"} {
				if ex.Path == root || strings.HasPrefix(ex.Path, root+".") || strings.HasPrefix(ex.Path, root+"[") {
					return at + "asserts on " + root + ", which only exists with a wire"
				}
			}
		}
	}
	return ""
}

// derive fills the views of a step that expectations share with the
// end-to-end harnesses.
func (sr *StepReport) derive() {
	src := sr.Outcome
	if sr.Reply != nil {
		src = *sr.Reply
	}
	if src.Result != nil || src.Error != nil {
		sr.Client = &ClientView{Result: src.Result, Error: src.Error}
	}
	if sr.Progress == nil {
		sr.Progress = []any{}
	}
	sr.Hits = append([]Hit(nil), sr.Outcome.Hits...)
	if sr.Reply != nil {
		sr.Hits = append(sr.Hits, sr.Reply.Hits...)
	}
}

// plainUpstreamReply is what a mock upstream answers when a step scripts
// events but no result.
func plainUpstreamReply(op string) json.RawMessage {
	switch op {
	case "tools/call", "execute_tool":
		return json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)
	case "prompts/get":
		return json.RawMessage(`{"messages":[]}`)
	}
	return json.RawMessage(`{"contents":[]}`)
}

// runStep runs one step. A step whose mock scripts stream events goes
// through the engine's stream entry points, as a gateway would drive it;
// every other step runs as before.
func (e *Engine) runStep(ctx context.Context, r Request, st Step, params json.RawMessage) StepReport {
	if !st.Mock.events() {
		return e.step(ctx, r, st.Op, params, st.Upstream)
	}
	sr := StepReport{Op: st.Op}
	bad := func(err error) StepReport {
		sr.Outcome = errOutcome(CodeInvalidParams, "scenario params: "+err.Error())
		return sr
	}
	// The upstream's events for a forwarded request: progress, the other
	// notifications, then the response, unless the script withholds it.
	events := func(call *CallContext) iter.Seq2[UpstreamEvent, error] {
		return func(yield func(UpstreamEvent, error) bool) {
			// A gateway hands the engine only the progress sent under a
			// token it issued, and it issues one only when the client
			// asked for progress. Whether those events are then relayed is
			// the engine's decision, from the server's progress switch:
			// this runner must not make that decision for it.
			if st.Progress {
				for _, p := range st.Mock.Progress {
					m := map[string]any{"progressToken": "scenario", "progress": p.Progress}
					if p.Total != 0 {
						m["total"] = p.Total
					}
					if p.Message != "" {
						m["message"] = p.Message
					}
					b, _ := json.Marshal(m)
					if !yield(UpstreamEvent{Method: ProgressMethod, Params: b}, nil) {
						return
					}
				}
			}
			for _, n := range st.Mock.Notifications {
				b, _ := json.Marshal(n.Params)
				if !yield(UpstreamEvent{Method: n.Method, Params: b}, nil) {
					return
				}
			}
			switch {
			case st.Mock.NoResponse:
			case st.Mock.Error != nil:
				yield(UpstreamEvent{Error: st.Mock.Error}, nil)
			case len(st.Upstream) > 0:
				yield(UpstreamEvent{Result: st.Upstream}, nil)
			default:
				yield(UpstreamEvent{Result: plainUpstreamReply(st.Op)}, nil)
			}
		}
	}
	pump := func(o Outcome, stream func(iter.Seq2[UpstreamEvent, error]) iter.Seq2[ReplyEvent, error]) {
		sr.Outcome = o
		if o.Forward == nil {
			return
		}
		for ev, err := range stream(events(o.Call)) {
			if err != nil {
				sr.Reply = &Outcome{Error: &RPCError{Code: -32603, Message: err.Error()}}
				return
			}
			if ev.Final != nil {
				sr.Reply = ev.Final
				return
			}
			var p any
			_ = json.Unmarshal(ev.Params, &p)
			sr.Progress = append(sr.Progress, p)
		}
	}
	switch st.Op {
	case "tools/call":
		var p mcp.CallToolParamsRaw
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return bad(err)
			}
		}
		pump(e.CallTool(ctx, r, &p), func(in iter.Seq2[UpstreamEvent, error]) iter.Seq2[ReplyEvent, error] {
			return e.ToolReplyStream(ctx, r, sr.Outcome.Call, &p, in)
		})
	case "prompts/get":
		var p mcp.GetPromptParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return bad(err)
			}
		}
		pump(e.GetPrompt(ctx, r, &p), func(in iter.Seq2[UpstreamEvent, error]) iter.Seq2[ReplyEvent, error] {
			return e.PromptReplyStream(ctx, r, sr.Outcome.Call, &p, in)
		})
	case "resources/read":
		var p mcp.ReadResourceParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return bad(err)
			}
		}
		pump(e.ReadResource(ctx, r, &p), func(in iter.Seq2[UpstreamEvent, error]) iter.Seq2[ReplyEvent, error] {
			return e.ReadReplyStream(ctx, r, sr.Outcome.Call, &p, in)
		})
	default:
		sr.Outcome = errOutcome(CodeInvalidParams, "a step with scripted events must be tools/call, prompts/get or resources/read, not "+st.Op)
	}
	return sr
}

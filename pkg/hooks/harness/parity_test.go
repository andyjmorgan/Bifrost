package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
)

// The parity bridge replays every engine-level scenario and doc check end
// to end, once per client: a real client over real HTTP, the test gateway
// with the scenario's rules, mock upstream servers scripted from the
// scenario's own catalogs and replies, and the scenario's own expectations
// checked by the scenario runner's own checker.
//
// Where each expectation is read from:
//
//   - outcome.forward.*: what the mock upstream actually received.
//   - outcome.result, outcome.error, reply.result, reply.error: what the
//     client saw.
//   - hits, issues, fallback and fault: the gateway's record of the engine's
//     outcome for that request. They never reach a client.
//   - problems and warnings: the gateway's record of the compile problems.
//
// A scenario added to testdata runs here with no new code. One that cannot
// run end to end must be listed in engineOnly (parity_lists_test.go), or the
// test fails.

var parityDirs = []string{"../testdata/scenarios", "../testdata/doc-checks"}

func TestScenarioParity(t *testing.T) {
	type loaded struct {
		file string
		scs  []*hooks.Scenario
		raws []rawScenario
	}
	var all []loaded
	for _, dir := range parityDirs {
		files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		sort.Strings(files)
		for _, f := range files {
			scs, err := hooks.LoadScenarios(f)
			if err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			raws, err := loadRawScenarios(f)
			if err != nil || len(raws) != len(scs) {
				t.Fatalf("%s: raw catalogs: %v (%d of %d)", f, err, len(raws), len(scs))
			}
			all = append(all, loaded{filepath.Base(filepath.Dir(f)) + "/" + filepath.Base(f), scs, raws})
		}
	}
	if len(all) == 0 {
		t.Fatal("no scenario files found")
	}
	if os.Getenv("HARNESS_PRINT_ENGINE_ONLY") != "" {
		// Maintenance aid: print the entries engineOnly should hold.
		for _, l := range all {
			for _, sc := range l.scs {
				if why := cannotRun(sc); why != "" {
					fmt.Printf("\t%q: %q,\n", l.file+": "+sc.Name, why)
				}
			}
		}
	}
	stats := newParityStats()
	seen := map[string]bool{}
	for _, c := range Clients {
		t.Run(c.Name(), func(t *testing.T) {
			for _, l := range all {
				names := map[string]int{}
				for i, sc := range l.scs {
					key := l.file + ": " + sc.Name
					if names[key]++; names[key] > 1 {
						key = fmt.Sprintf("%s #%d", key, names[key])
					}
					seen[key] = true
					t.Run(key, func(t *testing.T) {
						t.Parallel() // each scenario has its own gateway and upstreams
						runParity(t, c, key, sc, l.raws[i], stats)
					})
				}
			}
		})
	}
	for _, skipped := range SkippedClients {
		name, why, _ := strings.Cut(skipped, ": ")
		t.Run(name, func(t *testing.T) { t.Skipf("CLIENT NOT RUN: %s: %s", name, why) })
	}
	for key := range engineOnly {
		if !seen[key] {
			t.Errorf("engineOnly lists a scenario that does not exist: %s", key)
		}
	}
	for key := range knownDifferences {
		_, name, _ := strings.Cut(key, "|")
		if !seen[name] {
			t.Errorf("knownDifferences lists a scenario that does not exist: %s", key)
		}
	}
	sum := stats.summary()
	for _, skipped := range SkippedClients {
		sum += "  CLIENT NOT RUN: " + skipped + "\n"
	}
	t.Log(sum)
}

// ---- loading ----------------------------------------------------------------------------

// rawScenario keeps the catalogs as the file wrote them, so the mock
// upstream serves the very bytes the scenario states.
type rawScenario struct {
	Servers []struct {
		Catalog    map[string]json.RawMessage `json:"catalog"`
		CatalogRef string                     `json:"catalogRef"`
	} `json:"servers"`
}

func loadRawScenarios(path string) ([]rawScenario, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	t := bytes.TrimSpace(b)
	if bytes.HasPrefix(t, []byte("[")) {
		var out []rawScenario
		return out, json.Unmarshal(t, &out)
	}
	var out []rawScenario
	dec := json.NewDecoder(bytes.NewReader(t))
	for dec.More() {
		var s rawScenario
		if err := dec.Decode(&s); err != nil {
			return out, err
		}
		out = append(out, s)
	}
	return out, nil
}

// ---- the script the mock upstreams answer from --------------------------------------------

// script is the queue of upstream replies for the requests a step forwards.
// An empty entry, or an empty queue, means the plain default.
type script struct {
	mu sync.Mutex
	q  []json.RawMessage
}

func (s *script) set(q []json.RawMessage) {
	s.mu.Lock()
	s.q = q
	s.mu.Unlock()
}

func (s *script) next() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.q) == 0 {
		return nil
	}
	r := s.q[0]
	s.q = s.q[1:]
	return r
}

func plainReply(rec Recorded) string {
	switch rec.Method {
	case "tools/call":
		return `{"content":[{"type":"text","text":"ok"}]}`
	case "prompts/get":
		return `{"messages":[]}`
	}
	return `{"contents":[]}`
}

var forwardable = map[string]bool{"tools/call": true, "prompts/get": true, "resources/read": true, "resources/directory/read": true}

// ---- one scenario -------------------------------------------------------------------------

type parityServer struct {
	slug   string
	up     *Upstream
	modern bool
}

type parityRun struct {
	// oracle is the engine-level run of the same scenario. It is used only
	// to plan: to know which questions a round asks, so that a hand-built
	// answer to some other question is not mistaken for an SDK's own.
	oracle  hooks.Report
	t       *testing.T
	key     string
	sc      *hooks.Scenario
	client  Client
	g       *Gateway
	servers []parityServer
	live    []*hooks.Server
	script  *script
	outputs []any
	stats   *parityStats
	// failures are the messages of the expectations that did not hold.
	failures []string
}

func (p *parityRun) fail(format string, args ...any) {
	p.t.Helper()
	msg := fmt.Sprintf(format, args...)
	p.failures = append(p.failures, msg)
	p.t.Error(msg)
}

var searchOps = map[string]bool{"tool_search": true, "list_tools": true, "get_tool_definition": true, "execute_tool": true}

// cannotRun reports why a scenario has no end-to-end form, or "".
func cannotRun(sc *hooks.Scenario) string {
	for _, st := range sc.Steps {
		switch {
		case st.Op == "skills/list" || st.Op == "skills/get":
			return "deferred extension: skills"
		case searchOps[st.Op] && sc.Route != "search":
			return "a search-mode tool on a route that is not search mode, which no client can reach"
		}
		// On the wire a client's identity is what its _meta says. A scenario
		// that gives the engine one client and puts a different one in the
		// request's _meta describes a state no real request can be in.
		client := sc.Client
		if st.Client != nil {
			client = st.Client
		}
		if params, ok := stepParams(st); ok {
			if meta, ok := params["_meta"].(map[string]any); ok {
				identity := map[string]any{}
				for k, v := range client {
					if k != "capabilities" {
						identity[k] = v
					}
				}
				declared, has := meta[metaClient]
				if has && len(identity) > 0 && exactJSON(declared) != exactJSON(identity) {
					return "the scenario's client and the client declared in the request's _meta differ; on the wire _meta is the only declaration"
				}
				caps, hasCaps := client["capabilities"]
				declaredCaps, has := meta[metaCaps]
				if has && hasCaps && exactJSON(declaredCaps) != exactJSON(caps) {
					return "the scenario's client and the client declared in the request's _meta differ; on the wire _meta is the only declaration"
				}
			}
		}
	}
	return ""
}

func runParity(t *testing.T, c Client, key string, sc *hooks.Scenario, raw rawScenario, stats *parityStats) {
	why, listed := cannotRun(sc), engineOnly[key]
	switch {
	case why != "" && listed == "":
		t.Fatalf("this scenario cannot run end to end (%s) and is not in engineOnly", why)
	case why == "" && listed != "":
		t.Fatalf("engineOnly lists this scenario (%s), but it can run end to end", listed)
	case why != "":
		stats.engineOnly(c, listed)
		t.Skipf("engine-only: %s", listed)
	}
	if note := knownDifferences[c.Name()+"|"+key]; note != "" {
		stats.known(c)
		knownGap(t, "end-to-end result differs from the engine-level result: "+note)
	}

	built, err := sc.Build()
	if errors.Is(err, hooks.ErrSkip) {
		stats.noFixture(c)
		t.Skipf("fixture not present: %v", err)
	}
	if err != nil {
		t.Fatalf("building the scenario: %v", err)
	}
	g := NewGateway(t)
	g.TestHooks, g.DiscoverVersion, g.KeepAlive = true, "test", 0
	g.SetEngine(sc.NewEngine())
	g.Problems, g.Warnings = built.Problems, built.Warnings
	p := &parityRun{t: t, key: key, sc: sc, client: c, g: g, live: built.Servers, script: &script{}, stats: stats}
	if _, isRaw := c.(RawHTTP); !isRaw {
		p.oracle = sc.Run(context.Background())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for i, s := range built.Servers {
		version := s.Info.ProtocolVersion
		mockVersion := version
		if mockVersion == "" {
			mockVersion = VNone
		}
		u := NewUpstream(t, mockVersion)
		u.Name = s.Slug
		lists := map[string]json.RawMessage{}
		if i < len(raw.Servers) && raw.Servers[i].CatalogRef == "" {
			lists = raw.Servers[i].Catalog
		} else {
			lists["tools"], _ = json.Marshal(s.Catalog.Tools)
		}
		for _, k := range []string{"tools", "prompts", "resources", "resourceTemplates"} {
			if len(lists[k]) == 0 || string(lists[k]) == "null" {
				delete(lists, k)
			}
		}
		u.Lists(lists)
		u.Default(func(rec Recorded) Reply {
			if r := p.script.next(); len(r) > 0 {
				return Reply{RawResult: string(r)}
			}
			return Reply{RawResult: plainReply(rec)}
		})
		// The catalog travels: the gateway fetches it from the mock, and the
		// engine works on what arrived. It must be what the scenario stated.
		fetched, ferr := g.FetchCatalog(ctx, u.URL(), version)
		if ferr != nil {
			t.Fatalf("fetching the catalog of %s: %v", s.Slug, ferr)
		}
		if i < len(sc.Servers) && sc.Servers[i].MetaAllow != nil {
			fetched = fetched.StripMeta(sc.Servers[i].MetaAllow)
		}
		fetched.Skills = s.Catalog.Skills
		if got, want := exactJSON(fetched), exactJSON(s.Catalog); got != want {
			t.Fatalf("the catalog of %s changed between the upstream and the gateway:\n  arrived %s\n  stated  %s", s.Slug, clip(got), clip(want))
		}
		s.Catalog = fetched
		g.Register(s, u.URL(), version)
		u.Forget()
		p.servers = append(p.servers, parityServer{slug: s.Slug, up: u, modern: version == V2026})
	}

	plans := p.plan()
	for i := 0; i < len(sc.Steps); {
		n := 1
		for i+n < len(sc.Steps) && plans[i+n].via == "folded" {
			n++
		}
		p.unit(ctx, i, n, plans[i])
		i += n
	}
	stats.ran(c)
	if note := knownDifferences[c.Name()+"|"+key]; note != "" && !t.Failed() {
		t.Errorf("knownDifferences lists this scenario for %s, but it passes; remove the entry", c.Name())
		return
	}
	if t.Failed() && os.Getenv("HARNESS_PRINT_DIFFERENCES") != "" {
		// Maintenance aid: print the entry knownDifferences would hold.
		fmt.Printf("\t%q: %q,\n", c.Name()+"|"+key, strings.Join(p.failures, " ;; "))
	}
	if t.Failed() {
		// Is this a difference between the two runs, or does the scenario
		// fail at engine level as well?
		if rep := sc.Run(context.Background()); rep.Status == "pass" {
			t.Logf("the engine-level run of this scenario passes: the end-to-end result differs from it")
		} else {
			t.Logf("the engine-level run of this scenario also fails (%s): %v", rep.Status, rep.Failures)
		}
	}
}

func exactJSON(v any) string {
	b, _ := json.Marshal(v)
	var g any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	_ = dec.Decode(&g)
	b, _ = json.Marshal(g)
	return string(b)
}

func clip(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

func generic(v any) map[string]any {
	b, _ := json.Marshal(v)
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	_ = dec.Decode(&m)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

func decodeExact(raw json.RawMessage) (any, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	err := dec.Decode(&v)
	return v, err
}

// ---- planning: which client makes each step ------------------------------------------------

// stepPlan says how a step is made: by the client under test ("client"), as
// a later round of the previous step's own call ("folded"), or by the raw
// client because the client under test cannot express it ("raw").
type stepPlan struct {
	via string
	why string
}

var stateRef = regexp.MustCompile(`^\$\{steps\.(\d+)\.(outcome|reply)\.result\.requestState\}$`)

func stepParams(st hooks.Step) (map[string]any, bool) {
	if len(st.Params) == 0 {
		return nil, true
	}
	v, err := decodeExact(st.Params)
	if err != nil {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok
}

func hasOverrides(st hooks.Step) bool {
	return st.Caller != nil || st.Client != nil || st.Now != "" || len(st.Slugs) > 0 || len(st.Rules) > 0 || len(st.Generation) > 0
}

func (p *parityRun) plan() []stepPlan {
	steps := p.sc.Steps
	plans := make([]stepPlan, len(steps))
	_, isRaw := p.client.(RawHTTP)
	for i, st := range steps {
		if isRaw {
			plans[i] = stepPlan{via: "client"}
			continue
		}
		params, isObject := stepParams(st)
		_, hasState := params["requestState"]
		_, hasAnswers := params["inputResponses"]
		if isObject && (hasState || hasAnswers) {
			// A continuation. An SDK makes these itself, inside one call, so
			// it folds into the previous step when it is exactly what an
			// SDK would send next: the same request, the state the previous
			// round returned, and answers a handler could give.
			if why := p.foldable(i, params, plans); why != "" {
				plans[i] = stepPlan{via: "raw", why: "a hand-built continuation: " + why}
			} else {
				plans[i] = stepPlan{via: "folded"}
			}
			continue
		}
		req, err := p.request(st, st.Params)
		if err != nil {
			plans[i] = stepPlan{via: "raw", why: err.Error()}
			continue
		}
		if duplicateKeys(st.Params) {
			plans[i] = stepPlan{via: "raw", why: "a request with a repeated JSON key"}
			continue
		}
		if why := p.client.Can(req); why != "" {
			plans[i] = stepPlan{via: "raw", why: why}
			continue
		}
		plans[i] = stepPlan{via: "client"}
	}
	return plans
}

func (p *parityRun) foldable(i int, params map[string]any, plans []stepPlan) string {
	steps := p.sc.Steps
	if i == 0 {
		return "nothing came before it"
	}
	if plans[i-1].via == "raw" {
		return "the step it continues is itself raw-only"
	}
	head := i - 1
	for plans[head].via == "folded" {
		head--
	}
	st := steps[i]
	if hasOverrides(st) {
		return "the caller, client, clock, slugs or rules change between the rounds"
	}
	if st.Op != steps[head].Op {
		return "it continues with a different method"
	}
	ref, _ := params["requestState"].(string)
	m := stateRef.FindStringSubmatch(ref)
	if m == nil || m[1] != fmt.Sprint(i-1) {
		return "the request state is not the one the previous round returned"
	}
	rest := map[string]any{}
	for k, v := range params {
		if k != "requestState" && k != "inputResponses" {
			rest[k] = v
		}
	}
	headParams, _ := stepParams(steps[head])
	if headParams == nil {
		headParams = map[string]any{}
	}
	if exactJSON(rest) != exactJSON(headParams) {
		return "the request changes between the rounds"
	}
	answers, ok := params["inputResponses"].(map[string]any)
	if !ok || len(answers) == 0 {
		return "the answers are not a set of responses"
	}
	// An SDK answers the questions it was asked. A step that answers some
	// other key is hand-built.
	asked := p.askedBy(i - 1)
	if len(asked) != len(answers) {
		return "it does not answer exactly the questions the previous round asked"
	}
	for k := range answers {
		if !asked[k] {
			return "it does not answer exactly the questions the previous round asked"
		}
	}
	var first string
	for _, a := range answers {
		am, ok := a.(map[string]any)
		if !ok {
			return "an answer is not an object"
		}
		if _, ok := am["action"].(string); !ok {
			return "an answer has no action"
		}
		for k := range am {
			if k != "action" && k != "content" {
				return "an answer has a field a handler cannot return: " + k
			}
		}
		if c, has := am["content"]; has {
			if _, ok := c.(map[string]any); !ok {
				return "an answer's content is not an object"
			}
		}
		if first == "" {
			first = exactJSON(am)
		} else if exactJSON(am) != first {
			return "several different answers in one round, which a handler cannot tell apart"
		}
	}
	return ""
}

// askedBy returns the input request keys a step's result asks, according to
// the engine-level run.
func (p *parityRun) askedBy(i int) map[string]bool {
	out := map[string]bool{}
	if i < 0 || i >= len(p.oracle.Steps) {
		return out
	}
	sr := p.oracle.Steps[i]
	res := sr.Outcome.Result
	if sr.Reply != nil {
		res = sr.Reply.Result
	}
	reqs, _ := generic(res)["inputRequests"].(map[string]any)
	for k := range reqs {
		out[k] = true
	}
	return out
}

// duplicateKeys reports whether a JSON value repeats a key in any object.
// A typed SDK call cannot send that.
func duplicateKeys(raw json.RawMessage) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	type frame struct {
		object bool
		keys   map[string]bool
		isKey  bool
	}
	var stack []*frame
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		top := func() *frame {
			if len(stack) == 0 {
				return nil
			}
			return stack[len(stack)-1]
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{':
				if f := top(); f != nil && f.object {
					f.isKey = true
				}
				stack = append(stack, &frame{object: true, keys: map[string]bool{}, isKey: true})
			case '[':
				if f := top(); f != nil && f.object {
					f.isKey = true
				}
				stack = append(stack, &frame{})
			default:
				stack = stack[:len(stack)-1]
			}
			continue
		}
		f := top()
		if f == nil || !f.object {
			continue
		}
		if f.isKey {
			k, _ := tok.(string)
			if f.keys[k] {
				return true
			}
			f.keys[k] = true
			f.isKey = false
		} else {
			f.isKey = true
		}
	}
}

// ---- turning a step into a request --------------------------------------------------------

// request maps a scenario step to the wire request a client makes.
func (p *parityRun) request(st hooks.Step, params json.RawMessage) (ClientRequest, error) {
	req := ClientRequest{}
	var obj map[string]any
	if len(params) > 0 {
		v, err := decodeExact(params)
		if err != nil {
			return req, fmt.Errorf("params that are not JSON")
		}
		obj, _ = v.(map[string]any)
		req.RawParams = params // as written: duplicate keys and number spellings survive
	}
	switch st.Op {
	case "discover":
		req.Method = "server/discover"
	case "connect":
		req.Method = "harness/connect"
	case "config":
		req.Method = "harness/config"
	case "tool_search", "list_tools", "get_tool_definition", "get_server_instructions":
		req.Method = "tools/call"
		args := obj
		if args == nil {
			args = map[string]any{}
		}
		obj = map[string]any{"name": st.Op, "arguments": args}
		req.RawParams, _ = json.Marshal(obj)
	case "execute_tool":
		req.Method = "tools/call"
		wrapped := map[string]any{"name": "execute_tool"}
		args := map[string]any{}
		for k, v := range obj {
			if k == "requestState" || k == "inputResponses" || k == "_meta" {
				wrapped[k] = v
			} else {
				args[k] = v
			}
		}
		wrapped["arguments"] = args
		obj = wrapped
		req.RawParams, _ = json.Marshal(obj)
	default:
		req.Method = st.Op
	}
	req.Params = obj

	client := p.sc.Client
	if st.Client != nil {
		client = st.Client
	}
	info := map[string]any{}
	for k, v := range client {
		if k != "capabilities" {
			info[k] = v
		}
	}
	req.ClientInfo = info
	if caps, ok := client["capabilities"]; ok {
		req.Capabilities = caps
	}
	return req, nil
}

// endpoint is where a step's request goes, with the step's caller and clock.
func (p *parityRun) endpoint(st hooks.Step) string {
	route := p.sc.Route
	if route == "" {
		route = "direct"
	}
	var path string
	switch {
	case route == "direct" && len(p.live) == 1:
		path = "/direct/" + p.live[0].Slug
	case route == "mux" || route == "search":
		path = "/" + route
	default:
		path = "/route/" + route
	}
	caller := p.sc.Caller
	if st.Caller != nil {
		caller = st.Caller
	}
	cb, _ := json.Marshal(caller)
	q := "?now=" + queryEscape(p.sc.StepTime(st).Format(time.RFC3339Nano)) + "&caller=" + queryEscape(string(cb))
	if len(st.Slugs) > 0 {
		q += "&slugs=" + queryEscape(strings.Join(st.Slugs, ","))
	}
	return p.g.URL() + path + q
}

func queryEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// ---- running a unit: one step, plus the rounds an SDK folds into it --------------------------

// view is what the client saw of one round.
type view struct {
	observable bool
	result     any
	err        *ClientError
	failure    string
}

func (p *parityRun) unit(ctx context.Context, first, n int, plan stepPlan) {
	sc := p.sc
	head := sc.Steps[first]

	// Policy changes that take effect from this step on.
	for slug, raw := range head.Rules {
		for _, s := range p.live {
			if s.Slug != slug {
				continue
			}
			rs, err := hooks.Parse(raw)
			if err != nil {
				p.fail("step %d: rules for %s: %v", first, slug, err)
				continue
			}
			set, probs := hooks.Compile(rs, hooks.Options{CostLimit: sc.Limits.CostLimit})
			s.Rules, s.Broken = set, nil
			if probs != nil {
				s.Broken = probs
			}
		}
	}
	for slug, gen := range head.Generation {
		for _, s := range p.live {
			if s.Slug == slug {
				s.Generation = gen
			}
		}
	}

	params, err := hooks.SubstituteRefs(head.Params, p.outputs)
	if err != nil {
		p.fail("step %d: %v", first, err)
		for k := 0; k < n; k++ {
			p.outputs = append(p.outputs, map[string]any{})
		}
		return
	}
	req, err := p.request(head, params)
	if err != nil {
		p.fail("step %d: %v", first, err)
		for k := 0; k < n; k++ {
			p.outputs = append(p.outputs, map[string]any{})
		}
		return
	}

	client := p.client
	if plan.via == "raw" {
		client = RawHTTP{}
		p.stats.rawOnly(p.client, plan.why)
	} else {
		p.stats.viaClient(p.client, n)
	}
	// The upstream's scripted replies for each round, and the answers an SDK
	// handler gives between rounds.
	queue := make([]json.RawMessage, 0, n)
	for k := 0; k < n; k++ {
		queue = append(queue, sc.Steps[first+k].Upstream)
	}
	p.script.set(queue)
	if _, isRaw := client.(RawHTTP); !isRaw {
		req.Elicit = []map[string]any{}
		for k := 1; k < n; k++ {
			fp, _ := stepParams(sc.Steps[first+k])
			answers, _ := fp["inputResponses"].(map[string]any)
			keys := make([]string, 0, len(answers))
			for key := range answers {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				a, _ := answers[key].(map[string]any)
				req.Elicit = append(req.Elicit, a)
			}
		}
	}

	recMark := len(p.g.Records())
	seqMark := recordedSeq.Load()
	rep, err := client.Do(ctx, p.endpoint(head), req)
	if err == nil && (rep.NotConnected || rep.Refused != "") && n == 1 {
		// An SDK discovers the server before anything else. Where the
		// scenario's own limits refuse that, or the SDK declines to send the
		// request, the step's request is never made; only the raw client
		// can make it.
		p.stats.viaClient(p.client, -1)
		why := "the SDK could not connect under this scenario's limits"
		if rep.Refused != "" {
			why = rep.Refused
		}
		p.stats.rawOnly(p.client, why)
		client = RawHTTP{}
		req.Elicit = nil
		p.script.set(queue)
		recMark, seqMark = len(p.g.Records()), recordedSeq.Load()
		rep, err = client.Do(ctx, p.endpoint(head), req)
	}
	if err != nil {
		p.fail("step %d (%s) through %s: %v", first, head.Op, client.Name(), err)
		for k := 0; k < n; k++ {
			p.outputs = append(p.outputs, map[string]any{})
		}
		return
	}

	if os.Getenv("HARNESS_DEBUG") != "" {
		b, _ := json.Marshal(map[string]any{"request": req, "result": rep.Result, "error": rep.Error, "failure": rep.Failure, "elicitations": rep.Elicitations, "unanswered": rep.Unanswered, "sdk": rep.SDK})
		p.t.Logf("step %d through %s: %s", first, client.Name(), clip(string(b))+string(b[min(len(b), 400):min(len(b), 1500)]))
	}
	// The gateway's records for this unit, one per round.
	var rounds []Record
	for _, r := range p.g.Records()[recMark:] {
		if r.Method == req.Method {
			rounds = append(rounds, r)
		}
	}
	// What the upstreams received for this unit, in order.
	type observed struct {
		srv parityServer
		rec Recorded
	}
	var ups []observed
	for _, s := range p.servers {
		for _, r := range s.up.Requests("") {
			if r.Seq > seqMark && forwardable[r.Method] {
				ups = append(ups, observed{s, r})
			}
		}
	}
	sort.Slice(ups, func(a, b int) bool { return ups[a].rec.Seq < ups[b].rec.Seq })

	for k := 0; k < n; k++ {
		idx := first + k
		st := sc.Steps[idx]
		if k >= len(rounds) {
			p.fail("step %d (%s): round %d of the call never reached the gateway; the call ended after %d round(s)", idx, st.Op, k+1, len(rounds))
			p.outputs = append(p.outputs, map[string]any{})
			continue
		}
		rec := rounds[k]
		last := k == len(rounds)-1
		// What the client saw of this round. The raw client sees every
		// round. An SDK shows only how the whole call ended, and that end
		// belongs to the last round it made.
		v := view{}
		if last && !rep.Unanswered {
			v = view{observable: true, result: rep.Result, err: rep.Error, failure: rep.Failure}
		}

		out := generic(rec.Outcome)
		delete(out, "result")
		delete(out, "error")
		delete(out, "forward")
		step := map[string]any{"op": st.Op, "outcome": out}
		setSeen := func(target map[string]any, fromRecord hooks.Outcome) {
			switch {
			case !v.observable:
				// Not shown to an SDK. Keep what the gateway sent, so later
				// steps can refer to it; client-side expectations on this
				// round are not evaluated.
				r := generic(fromRecord)
				if x, ok := r["result"]; ok {
					target["result"] = unwrapSearch(st.Op, x)
				}
				if x, ok := r["error"]; ok {
					target["error"] = x
				}
			case v.failure != "":
				target["error"] = map[string]any{"code": json.Number("0"), "message": "client failure: " + v.failure}
			case v.err != nil:
				target["error"] = generic(v.err)
			default:
				target["result"] = unwrapSearch(st.Op, v.result)
			}
		}
		if rec.Outcome.Forward != nil {
			if len(ups) == 0 {
				p.fail("step %d (%s): the engine forwarded the request, but no upstream received it", idx, st.Op)
			} else {
				o := ups[0]
				ups = ups[1:]
				out["forward"] = observedForward(o.srv, o.rec, rec.Outcome.Forward)
			}
			if len(st.Upstream) > 0 {
				reply := map[string]any{}
				if rec.Reply != nil {
					reply = generic(*rec.Reply)
					delete(reply, "result")
					delete(reply, "error")
					delete(reply, "forward")
					setSeen(reply, *rec.Reply)
				}
				step["reply"] = reply
			}
		} else {
			setSeen(out, rec.Outcome)
		}
		if st.Op == "connect" {
			p.crossCheckConnect(idx, req, out, seqMark)
		}
		p.outputs = append(p.outputs, step)

		extra := map[string]any{"problems": p.g.Problems, "warnings": p.g.Warnings}
		for _, ex := range st.Expect {
			if clientSide(ex.Path) && !v.observable {
				p.stats.unobservable(p.client)
				continue
			}
			p.stats.checked(p.client)
			if msg := ex.Check(step, extra); msg != "" {
				p.fail("step %d (%s): %s", idx, st.Op, msg)
			}
		}
	}
	// An SDK retries by itself when a server answers input_required with
	// state and no questions. Those rounds are the SDK's own, not steps of
	// the scenario.
	extra := 0
	for _, r := range rounds[min(n, len(rounds)):] {
		extra++
		if r.Outcome.Forward != nil && len(ups) > 0 {
			ups = ups[1:]
		}
	}
	if extra > 0 {
		p.stats.selfRetried(p.client, extra)
	}
	if len(ups) > 0 {
		p.fail("step %d (%s): %d upstream request(s) the engine did not record as forwards", first, head.Op, len(ups))
	}
}

func clientSide(path string) bool {
	for _, pre := range []string{"outcome.result", "outcome.error", "reply.result", "reply.error"} {
		if strings.HasPrefix(path, pre) {
			return true
		}
	}
	return false
}

// unwrapSearch undoes the tool-result wrapping the gateway puts around
// search mode's own tools, so the scenario's expectations read the same
// value the engine returned.
func unwrapSearch(op string, result any) any {
	m, ok := result.(map[string]any)
	if !ok {
		return result
	}
	switch op {
	case "tool_search", "get_tool_definition":
		if sc, ok := m["structuredContent"]; ok {
			return sc
		}
	case "list_tools":
		if content, ok := m["content"].([]any); ok && len(content) == 1 {
			if block, ok := content[0].(map[string]any); ok {
				if text, ok := block["text"].(string); ok {
					return text
				}
			}
		}
	}
	return result
}

// observedForward is outcome.forward as the upstream saw it: which server
// was reached, with what method and params, and what it was told about its
// client. The gateway's own transport additions are taken out of _meta.
func observedForward(s parityServer, rec Recorded, engine *hooks.Forward) map[string]any {
	params := rec.Exact()
	meta, _ := params["_meta"].(map[string]any)
	var info, caps any
	if s.modern {
		info, caps = meta[metaClient], meta[metaCaps]
	} else {
		for _, r := range s.up.Requests("initialize") {
			if r.Seq < rec.Seq {
				m := r.Exact()
				info, caps = m["clientInfo"], m["capabilities"]
			}
		}
	}
	if meta != nil {
		delete(meta, metaVersion)
		delete(meta, metaClient)
		delete(meta, metaCaps)
		if tok, ok := meta["progressToken"].(string); ok && strings.HasPrefix(tok, "gw-") {
			delete(meta, "progressToken")
		}
		if len(meta) == 0 {
			delete(params, "_meta")
		}
	}
	connect := map[string]any{"clientInfo": info, "capabilities": caps}
	if engine.Connect != nil {
		connect["forwardMeta"] = engine.Connect.ForwardMeta
	}
	f := map[string]any{"slug": s.slug, "method": rec.Method, "params": params, "connect": connect}
	if engine.Timeout != "" {
		f["timeout"] = engine.Timeout
	}
	return f
}

// crossCheckConnect compares the server/connect decision with what the
// upstream was actually told when the gateway reached it with that identity.
func (p *parityRun) crossCheckConnect(idx int, req ClientRequest, out map[string]any, seqMark int64) {
	res, ok := out["result"].(map[string]any)
	if !ok {
		return
	}
	slug, _ := req.Params["slug"].(string)
	for _, s := range p.servers {
		if s.slug != slug && !(slug == "" && len(p.servers) == 1) {
			continue
		}
		method := "initialize"
		if s.modern {
			method = "server/discover"
		}
		var told map[string]any
		for _, r := range s.up.Requests(method) {
			if r.Seq > seqMark {
				m := r.Exact()
				if s.modern {
					meta, _ := m["_meta"].(map[string]any)
					told = map[string]any{"clientInfo": meta[metaClient], "capabilities": meta[metaCaps]}
				} else {
					told = map[string]any{"clientInfo": m["clientInfo"], "capabilities": m["capabilities"]}
				}
			}
		}
		if told == nil {
			p.t.Errorf("step %d (connect): the upstream %s was never reached", idx, s.slug)
			return
		}
		for _, k := range []string{"clientInfo", "capabilities"} {
			if exactJSON(told[k]) != exactJSON(res[k]) {
				p.t.Errorf("step %d (connect): the upstream was told %s = %s, the engine decided %s", idx, k, exactJSON(told[k]), exactJSON(res[k]))
			}
		}
	}
}

// ---- the summary --------------------------------------------------------------------------

type clientStats struct {
	ran, engineOnly, known, noFixture int
	stepsViaClient, checked           int
	unobservable                      int
	selfRetries                       int
	rawOnly                           map[string]int
	engineOnlyWhy                     map[string]int
}

type parityStats struct {
	mu sync.Mutex
	by map[string]*clientStats
}

func newParityStats() *parityStats { return &parityStats{by: map[string]*clientStats{}} }

func (s *parityStats) of(c Client) *clientStats {
	cs := s.by[c.Name()]
	if cs == nil {
		cs = &clientStats{rawOnly: map[string]int{}, engineOnlyWhy: map[string]int{}}
		s.by[c.Name()] = cs
	}
	return cs
}

func (s *parityStats) with(c Client, f func(*clientStats)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s.of(c))
}

func (s *parityStats) ran(c Client)       { s.with(c, func(x *clientStats) { x.ran++ }) }
func (s *parityStats) known(c Client)     { s.with(c, func(x *clientStats) { x.known++ }) }
func (s *parityStats) noFixture(c Client) { s.with(c, func(x *clientStats) { x.noFixture++ }) }
func (s *parityStats) checked(c Client)   { s.with(c, func(x *clientStats) { x.checked++ }) }
func (s *parityStats) unobservable(c Client) {
	s.with(c, func(x *clientStats) { x.unobservable++ })
}
func (s *parityStats) viaClient(c Client, n int) {
	s.with(c, func(x *clientStats) { x.stepsViaClient += n })
}
func (s *parityStats) selfRetried(c Client, n int) {
	s.with(c, func(x *clientStats) { x.selfRetries += n })
}
func (s *parityStats) engineOnly(c Client, why string) {
	s.with(c, func(x *clientStats) { x.engineOnly++; x.engineOnlyWhy[why]++ })
}
func (s *parityStats) rawOnly(c Client, why string) {
	// Group by the reason's first clause, so the summary stays short.
	why, _, _ = strings.Cut(why, ":")
	s.with(c, func(x *clientStats) { x.rawOnly[why]++ })
}

func (s *parityStats) summary() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	b.WriteString("parity summary\n")
	names := make([]string, 0, len(s.by))
	for n := range s.by {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		x := s.by[n]
		fmt.Fprintf(&b, "  %s: %d scenarios ran end to end; %d engine-only; %d fixture not present; %d listed as known differences\n", n, x.ran, x.engineOnly, x.noFixture, x.known)
		fmt.Fprintf(&b, "    steps made through this client: %d; expectations checked: %d; client-side expectations on rounds an SDK does not show: %d; rounds the SDK retried by itself: %d\n", x.stepsViaClient, x.checked, x.unobservable, x.selfRetries)
		for _, group := range []struct {
			title string
			m     map[string]int
		}{{"engine-only scenarios", x.engineOnlyWhy}, {"steps only the raw client can make", x.rawOnly}} {
			keys := make([]string, 0, len(group.m))
			for k := range group.m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(&b, "    %s: %d × %s\n", group.title, group.m[k], k)
			}
		}
	}
	return b.String()
}

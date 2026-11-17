package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness/servers"
)

// The golden runner. Every case is JSON under pkg/hooks/testdata (the format
// is written down in its README.md), and this is the one runner that takes
// the set over a wire: once per client harness in Clients, against the mock
// upstream or each reference server, as the case says.
//
// The runner decides nothing about what to skip. A case or a step runs on
// every harness unless it carries an `exclude` entry naming that harness
// with a reason, or the case is an outlier (`only`) for another harness. A
// case that cannot run on a harness and has no entry fails.
//
// Where each root of an expectation is read from:
//
//   - client, progress, questions: what the client saw.
//   - outcome.result, outcome.error, reply.result, reply.error: the same
//     final message, under the names the engine harness uses.
//   - outcome.forward, upstream: what the mock upstream actually received.
//   - hits, outcome.hits, issues, fallback, fault, problems, warnings: the
//     gateway's record of the engine's outcome. They never reach a client.
//   - wire: status, headers and SSE frames, for the raw client.
//
// Modes: HOOKS_RUN_EXCLUDED=1 runs excluded cells and steps anyway and fails
// on any that pass, to find stale exclusions. HARNESS_SUGGEST_EXCLUDES=1
// prints, for every case or step that needs an exclusion and has none, a
// line a maintainer can turn into one. HARNESS_DEBUG=1 logs what each client
// returned.

var goldenDirs = []string{"../testdata/scenarios", "../testdata/doc-checks", "../testdata/e2e"}

// goldenCase is one case of the set.
type goldenCase struct {
	key   string // "<folder>/<file>: <name>"
	file  string
	index int // position in its file
	sc    *hooks.Scenario
	raw   rawScenario
}

func loadGolden(t *testing.T) []*goldenCase {
	var out []*goldenCase
	seen := map[string]int{}
	for _, dir := range goldenDirs {
		var files []string
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(path, ".json") {
				files = append(files, path)
			}
			return nil
		})
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
			rel, _ := filepath.Rel("../testdata", f)
			for i, sc := range scs {
				key := filepath.ToSlash(rel) + ": " + sc.Name
				if seen[key]++; seen[key] > 1 {
					t.Fatalf("%s: two cases are named %q; names must be unique within a file", f, sc.Name)
				}
				out = append(out, &goldenCase{key: key, file: f, index: i, sc: sc, raw: raws[i]})
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no case files found")
	}
	return out
}

// serverColumns are the upstream columns of the summary, in order.
var serverColumns = append([]string{"mock"}, servers.Kinds...)

// columnsOf is the upstream columns a case runs against.
func columnsOf(sc *hooks.Scenario) []string {
	if sc.Upstream == "reference" {
		return servers.Kinds
	}
	return []string{"mock"}
}

func requiredBy(env string, kinds ...string) bool {
	for _, k := range strings.Split(os.Getenv(env), ",") {
		k = strings.TrimSpace(k)
		for _, want := range kinds {
			if k == "all" || (k != "" && k == want) {
				return true
			}
		}
	}
	return false
}

// TestGolden runs every case on every client harness and upstream, and logs
// the summary.
func TestGolden(t *testing.T) {
	start := time.Now()
	cases := loadGolden(t)
	res := newGoldenResults()

	// Client harnesses whose runtime is missing: by name, never silently.
	for _, skipped := range SkippedClients {
		name, why, _ := strings.Cut(skipped, ": ")
		if requiredBy("MATRIX_CLIENTS_REQUIRE", name, strings.TrimSuffix(name, "-sdk")) {
			t.Fatalf("the %s client harness is required by MATRIX_CLIENTS_REQUIRE but unavailable: %s", name, why)
		}
		msg := fmt.Sprintf("CLIENT HARNESS NOT RUN: %s: %s", name, why)
		fmt.Fprintf(os.Stderr, "\n*** %s ***\n\n", msg)
		res.note(msg)
		res.missingClients = append(res.missingClients, name)
	}
	// Reference servers start once and serve every case; they keep no state.
	refs := map[string]*servers.Server{}
	needRefs := false
	for _, gc := range cases {
		needRefs = needRefs || gc.sc.Upstream == "reference"
	}
	if needRefs {
		for _, kind := range servers.Kinds {
			if err := servers.Available(kind); err != nil {
				if requiredBy(servers.RequireEnv, kind) {
					t.Fatalf("the %s reference server is required by %s but unavailable: %v", kind, servers.RequireEnv, err)
				}
				msg := fmt.Sprintf("SERVER HARNESS NOT RUN: server:%s: %v", kind, err)
				fmt.Fprintf(os.Stderr, "\n*** %s ***\n\n", msg)
				res.note(msg)
				continue
			}
			refs[kind] = servers.Start(t, kind)
		}
	}

	// The engine harness: the same cases with no wire, as hookctl runs them.
	t.Run("engine", func(t *testing.T) {
		for _, gc := range cases {
			r := gc.sc.Run(context.Background())
			state := cellPass
			switch {
			case r.Status == "fail":
				state = cellFail
				t.Errorf("%s: %s %v", gc.key, r.Reason, r.Failures)
			case r.Status == "skip" && gc.sc.Only != nil:
				state = cellOther
			case r.Status == "skip" && hooks.Excluded(gc.sc.Exclude, "engine") != "":
				state = cellExcluded
				res.excluded("engine", hooks.Excluded(gc.sc.Exclude, "engine"))
			case r.Status == "skip":
				state = cellNotRun
				res.note(fmt.Sprintf("ENGINE HARNESS DID NOT RUN %s: %s", gc.key, r.Reason))
			}
			res.set(gc.key, "engine", state)
		}
	})

	t.Run("cells", func(t *testing.T) {
		for _, c := range Clients {
			t.Run(c.Name(), func(t *testing.T) {
				for _, gc := range cases {
					for _, server := range columnsOf(gc.sc) {
						t.Run(server+"/"+gc.key, func(t *testing.T) {
							t.Parallel() // each cell has its own gateway and mocks
							runCell(t, c, server, gc, refs, res)
						})
					}
				}
			})
		}
	})
	t.Log(res.table(cases, time.Since(start)))
}

// ---- one cell: a case on one client harness and one upstream ---------------------------------

type goldenServer struct {
	slug   string
	up     *Upstream // nil for a reference server
	modern bool
}

type goldenRun struct {
	t      *testing.T
	gc     *goldenCase
	sc     *hooks.Scenario
	client Client
	server string // "mock", or a reference server's language
	res    *goldenResults

	g       *Gateway
	servers []goldenServer
	live    []*hooks.Server
	script  *script
	outputs []any
	vars    map[string]string

	// probing is HOOKS_RUN_EXCLUDED on an excluded cell: failures are
	// expected, and passing is the error.
	probing  bool
	suggest  bool
	failures []string
	stood    int // steps made by the raw client in this harness's place
}

// fail records a failure. In probing mode it is only kept, because there a
// failure is what an exclusion predicts.
func (p *goldenRun) fail(format string, args ...any) {
	p.t.Helper()
	msg := fmt.Sprintf(format, args...)
	p.failures = append(p.failures, msg)
	if !p.probing && os.Getenv("HOOKS_RUN_EXCLUDED") == "" {
		p.t.Error(msg)
	}
}

// suggestion prints what a maintainer needs to write a missing exclusion.
func (p *goldenRun) suggestion(step int, harness, kind, detail string) {
	b, _ := json.Marshal(detail)
	fmt.Printf("SUGGEST\t%s\t%d\t%d\t%s\t%s\t%s\n", p.gc.file, p.gc.index, step, harness, kind, b)
}

var searchOps = map[string]bool{"tool_search": true, "list_tools": true, "get_tool_definition": true, "execute_tool": true}

// cannotRun reports why a case has no form over a wire, or "". It never
// skips anything: it is how the runner notices a case that needs an
// exclusion and has none.
func cannotRun(sc *hooks.Scenario) string {
	for _, st := range sc.Steps {
		switch {
		case st.Op == "skills/list" || st.Op == "skills/get":
			return "the harness gateway does not serve the Skills extension, which is deferred, so the " + st.Op + " step has no endpoint to reach"
		case searchOps[st.Op] && sc.Route != "search" && !sc.Flags[hooks.FlagSearch]:
			route := sc.Route
			if route == "" {
				route = "direct"
			}
			return "the case invokes search mode's " + st.Op + " on the " + route + " route, which the engine harness can do by calling it directly; over a wire that route has no such tool"
		}
		// On the wire a client's identity is what its _meta says. A case
		// that gives the engine one client and puts a different one in the
		// request's _meta describes a state no real request can be in.
		client := sc.Client
		if st.Client != nil {
			client = st.Client
		}
		if params, ok := stepParams(st); ok {
			if meta, ok := params["_meta"].(map[string]any); ok {
				identity, _ := client["clientInfo"].(map[string]any)
				differs := false
				if declared, has := meta[metaClient]; has && len(identity) > 0 && exactJSON(declared) != exactJSON(identity) {
					differs = true
				}
				if caps, hasCaps := client["capabilities"]; hasCaps {
					if declared, has := meta[metaCaps]; has && exactJSON(declared) != exactJSON(caps) {
						differs = true
					}
				}
				if differs {
					return "the case gives the engine one client and puts a different one in the request's _meta; over a wire the request's _meta is the client's only declaration, so the two cannot differ"
				}
			}
		}
	}
	return ""
}

func runCell(t *testing.T, c Client, server string, gc *goldenCase, refs map[string]*servers.Server, res *goldenResults) {
	sc := gc.sc
	col := c.Name() + "/" + server
	state := ""
	p := &goldenRun{t: t, gc: gc, sc: sc, client: c, server: server, res: res, script: &script{},
		suggest: os.Getenv("HARNESS_SUGGEST_EXCLUDES") != ""}
	defer func() {
		switch {
		case state != "":
		case t.Failed():
			state = cellFail
		case p.stood > 0:
			state = cellPart
		default:
			state = cellPass
		}
		res.set(gc.key, col, state)
	}()
	runExcluded := os.Getenv("HOOKS_RUN_EXCLUDED") != ""

	// An outlier belongs to one harness.
	if sc.Only != nil && sc.Only.Harness != c.Name() {
		state = cellOther
		t.Skipf("an outlier for the %s harness: %s", sc.Only.Harness, sc.Only.Reason)
	}
	// A reference server whose runtime is missing.
	if server != "mock" && refs[server] == nil {
		state = cellNotRun
		t.Skipf("SERVER HARNESS NOT RUN: server:%s", server)
	}
	// Case-level exclusions, for the client harness or the server harness.
	harness, reason := c.Name(), hooks.Excluded(sc.Exclude, c.Name())
	if reason == "" && server != "mock" {
		harness, reason = "server:"+server, hooks.Excluded(sc.Exclude, "server:"+server)
	}
	if reason != "" {
		res.excluded(harness, reason)
		if !runExcluded {
			state = cellExcluded
			t.Skipf("excluded for %s: %s", harness, reason)
		}
		p.probing = true
	}
	if why := cannotRun(sc); why != "" && reason == "" {
		if p.suggest {
			p.suggestion(-1, c.Name(), "cannot", why)
			state = cellExcluded
			t.Skip("suggested an exclusion")
		}
		t.Fatalf("this case cannot run on the %s harness (%s) and has no exclude entry for it", c.Name(), why)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	g := NewGateway(t)
	g.TestHooks, g.DiscoverVersion, g.KeepAlive = true, "test", 0
	p.g = g
	p.vars = map[string]string{"client.name": c.Identity(), "client.harness": c.Name(), "server.language": server}
	if sc.Gateway != nil {
		g.Token = sc.Gateway.Token
		g.KeepAlive = time.Duration(sc.Gateway.KeepAliveMs) * time.Millisecond
	}
	g.SetEngine(sc.NewEngine())
	if server == "mock" {
		if !p.setupMock(ctx) {
			state = cellNotRun
			return
		}
	} else {
		p.setupReference(refs)
	}

	for i, st := range sc.Steps {
		p.step(ctx, i, st, runExcluded)
	}

	if p.probing {
		if len(p.failures) == 0 {
			t.Errorf("this case is excluded for %s (%s), but it passes; remove the exclusion", harness, reason)
			return
		}
		state = cellExcluded
		t.Logf("excluded for %s, and it does not pass: %s", harness, p.failures[0])
		return
	}
	if t.Failed() && server == "mock" {
		// Is this a difference between the harnesses, or does the case fail
		// on the engine harness as well?
		if rep := sc.Run(context.Background()); rep.Status == "fail" {
			t.Logf("the engine harness fails this case as well: %v", rep.Failures)
		} else {
			t.Logf("the engine harness reports %q for this case: the result over the wire differs from it", rep.Status)
		}
	}
}

// setupMock builds the mock upstreams from the case's own catalogs and
// registers them. It reports false when a real fixture is not present.
func (p *goldenRun) setupMock(ctx context.Context) bool {
	t, sc, g := p.t, p.sc, p.g
	built, err := sc.Build()
	if errors.Is(err, hooks.ErrSkip) {
		p.res.note(fmt.Sprintf("FIXTURE NOT PRESENT: %s: %v", p.gc.key, err))
		t.Skipf("fixture not present: %v", err)
		return false
	}
	if err != nil {
		t.Fatalf("building the case: %v", err)
	}
	g.Problems, g.Warnings = built.Problems, built.Warnings
	p.live = built.Servers
	for i, s := range built.Servers {
		version := s.Info.ProtocolVersion
		mockVersion := version
		if mockVersion == "" {
			mockVersion = VNone
		}
		u := NewUpstream(t, mockVersion)
		u.Name = s.Slug
		lists := map[string]json.RawMessage{}
		if i < len(p.gc.raw.Servers) && p.gc.raw.Servers[i].CatalogRef == "" {
			for k, v := range p.gc.raw.Servers[i].Catalog {
				lists[k] = v
			}
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
			if r, ok := p.script.next(); ok {
				return r
			}
			return Reply{RawResult: plainReply(rec.Method)}
		})
		// The catalog travels: the gateway fetches it from the mock, and the
		// engine works on what arrived. It must be what the case stated.
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
		p.servers = append(p.servers, goldenServer{slug: s.Slug, up: u, modern: version == V2026})
	}
	return true
}

// setupReference registers the reference servers: the case's first server is
// the column's language, and each further server is the next language that
// is running, so the servers of a mux case always differ.
func (p *goldenRun) setupReference(refs map[string]*servers.Server) {
	t, sc, g := p.t, p.sc, p.g
	kinds := []string{p.server}
	for i, k := range servers.Kinds {
		if k != p.server {
			continue
		}
		for j := 1; j < len(servers.Kinds); j++ {
			if next := servers.Kinds[(i+j)%len(servers.Kinds)]; refs[next] != nil {
				kinds = append(kinds, next)
			}
		}
	}
	for i, ss := range sc.Servers {
		if i >= len(kinds) {
			t.Skipf("SERVER HARNESS NOT RUN: this case needs %d servers in different languages and only %d are running", len(sc.Servers), len(kinds))
		}
		var rules []hooks.Rule
		if len(ss.Rules) > 0 {
			rs, err := hooks.Parse(ss.Rules)
			if err != nil {
				t.Fatalf("rules of %s: %v", ss.Slug, err)
			}
			rules = rs
		}
		g.MetaAllow = ss.MetaAllow
		s := g.AddRemote(t, ss.Slug, refs[kinds[i]].URL, V2026, rules...)
		if ss.Entitlement != nil {
			s.Entitlement = ss.Entitlement
		}
		if ss.Generation != "" {
			s.Generation = ss.Generation
		}
		p.live = append(p.live, s)
		p.servers = append(p.servers, goldenServer{slug: ss.Slug, modern: true})
	}
}

// ---- the mock upstream's script -------------------------------------------------------------

// script is the queue of replies for the requests a step forwards. An empty
// queue means the plain default.
type script struct {
	mu sync.Mutex
	q  []Reply
}

func (s *script) set(q []Reply) {
	s.mu.Lock()
	s.q = q
	s.mu.Unlock()
}

func (s *script) next() (Reply, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.q) == 0 {
		return Reply{}, false
	}
	r := s.q[0]
	s.q = s.q[1:]
	return r, true
}

func plainReply(method string) string {
	switch method {
	case "tools/call":
		return `{"content":[{"type":"text","text":"ok"}]}`
	case "prompts/get":
		return `{"messages":[]}`
	}
	return `{"contents":[]}`
}

// scriptFor turns a step's `upstream` and `mock` into the mock's replies.
func scriptFor(st hooks.Step, method string) []Reply {
	first := Reply{RawResult: string(st.Upstream)}
	if len(st.Upstream) == 0 {
		first.RawResult = plainReply(method)
	}
	m := st.Mock
	if m == nil {
		return []Reply{first}
	}
	first.Stream = m.Stream || len(m.Progress) > 0 || len(m.Notifications) > 0 || m.NoResponse
	first.Delay = time.Duration(m.DelayMs) * time.Millisecond
	first.Gap = time.Duration(m.GapMs) * time.Millisecond
	for _, pr := range m.Progress {
		first.Progress = append(first.Progress, Progress{Progress: pr.Progress, Total: pr.Total, Message: pr.Message})
	}
	first.ForceProgress = m.ForceProgress
	for _, n := range m.Notifications {
		first.Notes = append(first.Notes, Note{Method: n.Method, Params: n.Params})
	}
	first.NoFinal, first.Hang = m.NoResponse, m.Hang
	if m.Error != nil {
		first.Error = &RPCErr{Code: int(m.Error.Code), Message: m.Error.Message}
	}
	first.Status, first.Body, first.RawBody = m.Status, m.Body, m.RawBody
	out := []Reply{first}
	for _, r := range m.Then {
		out = append(out, Reply{RawResult: string(r)})
	}
	return out
}

var forwardable = map[string]bool{"tools/call": true, "prompts/get": true, "resources/read": true, "resources/directory/read": true}

// ---- one step ------------------------------------------------------------------------------

// step runs one step on this cell's harnesses, honouring its exclusions.
func (p *goldenRun) step(ctx context.Context, i int, st hooks.Step, runExcluded bool) {

	skip := func() { p.outputs = append(p.outputs, map[string]any{}) }
	raw := RawHTTP{}
	_, clientIsRaw := p.client.(RawHTTP)

	// Policy changes take effect from this step on, whoever makes it.
	p.applyPolicy(i, st)

	// The server harness: an excluded step is not made at all.
	if p.server != "mock" {
		if why := hooks.Excluded(st.Exclude, "server:"+p.server); why != "" {
			p.res.excluded("server:"+p.server, why)
			p.stood++
			if !runExcluded {
				skip()
				return
			}
		}
	}
	// The client harness: an excluded step is made by the raw client in its
	// place, so the steps after it still have what they need. The raw
	// harness has no stand-in; its excluded step is not made.
	if why := hooks.Excluded(st.Exclude, p.client.Name()); why != "" {
		p.res.excluded(p.client.Name(), why)
		p.stood++
		if runExcluded {
			// Try it on the harness itself. Passing means the exclusion is
			// stale; not passing is what the exclusion says.
			out, cannot, fails := p.attempt(ctx, i, st, p.client, true)
			if cannot == "" && len(fails) == 0 {
				p.t.Errorf("step %d (%s) is excluded for %s (%s), but it passes there; remove the exclusion", i, st.Op, p.client.Name(), why)
				p.outputs = append(p.outputs, out)
				return
			}
		}
		if clientIsRaw {
			skip()
			return
		}
		out, _, fails := p.attempt(ctx, i, st, raw, false)
		for _, f := range fails {
			p.fail("step %d (%s), made by the raw client in place of %s: %s", i, st.Op, p.client.Name(), f)
		}
		p.outputs = append(p.outputs, out)
		return
	}

	out, cannot, fails := p.attempt(ctx, i, st, p.client, true)
	if cannot != "" {
		// The harness cannot make this step, and nothing excludes it.
		if p.suggest {
			p.suggestion(i, p.client.Name(), "cannot", cannot)
		} else {
			p.fail("step %d (%s) cannot run on the %s harness (%s) and has no exclude entry for it", i, st.Op, p.client.Name(), cannot)
		}
		// Carry on with a stand-in, so one missing entry is one failure.
		if clientIsRaw {
			skip()
			return
		}
		out, _, _ = p.attempt(ctx, i, st, raw, false)
		p.outputs = append(p.outputs, out)
		return
	}
	if len(fails) > 0 && p.suggest {
		p.suggestion(i, p.client.Name(), "differs", strings.Join(fails, " ;; "))
	} else {
		for _, f := range fails {
			p.fail("step %d (%s): %s", i, st.Op, f)
		}
	}
	p.outputs = append(p.outputs, out)
}

func (p *goldenRun) applyPolicy(i int, st hooks.Step) {
	for slug, raw := range st.Rules {
		for _, s := range p.live {
			if s.Slug != slug {
				continue
			}
			rs, err := hooks.Parse(raw)
			if err != nil {
				p.fail("step %d: rules for %s: %v", i, slug, err)
				continue
			}
			set, probs := hooks.Compile(rs, hooks.Options{CostLimit: p.sc.Limits.CostLimit})
			s.Rules, s.Broken = set, nil
			if probs != nil {
				s.Broken = probs
			}
		}
	}
	for slug, gen := range st.Generation {
		for _, s := range p.live {
			if s.Slug == slug {
				s.Generation = gen
			}
		}
	}
}

func hasWireExpectation(st hooks.Step) bool {
	for _, ex := range st.Expect {
		if ex.Path == "wire" || strings.HasPrefix(ex.Path, "wire.") {
			return true
		}
	}
	return false
}

// attempt makes one step through one client and checks its expectations. It
// returns the step's output, the reason the client could not make the step
// at all (in which case nothing was checked), and the expectations that did
// not hold. own says the client is the cell's own, not a stand-in.
func (p *goldenRun) attempt(ctx context.Context, i int, st hooks.Step, via Client, own bool) (out map[string]any, cannot string, fails []string) {
	out = map[string]any{}
	_, viaRaw := via.(RawHTTP)
	params, err := hooks.SubstituteRefs(st.Params, p.outputs)
	if err != nil {
		return out, "", []string{err.Error()}
	}
	req, err := p.request(st, params)
	if err != nil {
		return out, "", []string{err.Error()}
	}
	req.Progress, req.Body = st.Progress, st.RawBody
	switch {
	case st.Token != nil:
		req.Token = *st.Token
	case p.sc.Gateway != nil:
		req.Token = p.sc.Gateway.Token
	}
	if !viaRaw {
		if why := via.Can(req); why != "" {
			return out, why, nil
		}
		if duplicateKeys(st.Params) {
			return out, "a request with a repeated JSON key", nil
		}
		if hasWireExpectation(st) {
			return out, "expectations on the wire", nil
		}
	}
	// An SDK always answers through its handler; with nothing scripted the
	// handler stops the call at the first question. The raw client only
	// makes the rounds itself when the step scripts answers.
	req.Elicit = st.Answers
	if !viaRaw && req.Elicit == nil {
		req.Elicit = []map[string]any{}
	}
	p.script.set(scriptFor(st, req.Method))

	recMark, seqMark := len(p.g.Records()), recordedSeq.Load()
	cancelled := 0
	for _, s := range p.servers {
		if s.up != nil {
			cancelled += s.up.Cancelled()
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	rep, err := via.Do(callCtx, p.endpoint(st), req)
	cancel()
	if err != nil {
		return out, "", []string{fmt.Sprintf("through %s: %v", via.Name(), err)}
	}
	if os.Getenv("HARNESS_DEBUG") != "" {
		b, _ := json.Marshal(map[string]any{"request": req, "result": rep.Result, "error": rep.Error, "failure": rep.Failure, "questions": rep.Elicitations, "unanswered": rep.Unanswered, "sdk": rep.SDK})
		p.t.Logf("step %d through %s: %s", i, via.Name(), clip(string(b)))
	}
	if !viaRaw {
		switch {
		case rep.Refused != "":
			return out, "refused: " + rep.Refused, nil
		case rep.Unanswered && len(st.Answers) == 0:
			return out, "unanswered", nil
		}
	}

	// The gateway's records for this step, one per round the client made.
	var rounds []Record
	for _, r := range p.g.Records()[recMark:] {
		if r.Method == req.Method {
			rounds = append(rounds, r)
		}
	}
	// What the mock upstreams received for this step, in order.
	var ups []map[string]any
	if p.server == "mock" {
		type seen struct {
			srv goldenServer
			rec Recorded
		}
		var all []seen
		for _, s := range p.servers {
			for _, r := range s.up.Requests("") {
				if r.Seq > seqMark && forwardable[r.Method] {
					all = append(all, seen{s, r})
				}
			}
		}
		sort.Slice(all, func(a, b int) bool { return all[a].rec.Seq < all[b].rec.Seq })
		fwd := 0
		for _, r := range rounds {
			if r.Outcome.Forward == nil {
				continue
			}
			if fwd < len(all) {
				ups = append(ups, observedForward(all[fwd].srv, all[fwd].rec, r.Outcome.Forward))
				// The engine's decision on the client's _meta is checked
				// against what the upstream received, on every forward.
				if why := checkForwardedMeta(generic(r.Meta), all[fwd].rec, r.Outcome.Forward); why != "" {
					fails = append(fails, why)
				}
			}
			fwd++
		}
		if fwd != len(all) {
			fails = append(fails, fmt.Sprintf("the engine recorded %d forward(s), and the upstreams received %d request(s)", fwd, len(all)))
		}
	}

	// The step as expectations see it.
	seenResult, seenError := any(nil), any(nil)
	switch {
	case rep.Failure != "":
		seenError = map[string]any{"code": json.Number("0"), "message": "client failure: " + rep.Failure}
	case rep.Error != nil:
		seenError = generic(rep.Error)
	default:
		seenResult = unwrapSearch(st.Op, rep.Result)
	}
	put := func(target map[string]any) {
		if seenResult != nil {
			target["result"] = seenResult
		}
		if seenError != nil {
			target["error"] = seenError
		}
	}
	outcome := map[string]any{}
	out["op"], out["outcome"] = st.Op, outcome
	var hits []any
	if len(rounds) > 0 {
		last := rounds[len(rounds)-1]
		for k, v := range generic(last.Outcome) {
			if k != "result" && k != "error" && k != "forward" {
				outcome[k] = v
			}
		}
		if last.Outcome.Forward != nil {
			if p.server == "mock" {
				if len(ups) > 0 {
					outcome["forward"] = ups[len(ups)-1]
				}
			} else {
				outcome["forward"] = generic(last.Outcome.Forward)
			}
			if len(st.Upstream) > 0 || st.Mock != nil || p.server != "mock" {
				reply := map[string]any{}
				if last.Reply != nil {
					for k, v := range generic(*last.Reply) {
						if k != "result" && k != "error" && k != "forward" {
							reply[k] = v
						}
					}
				}
				put(reply)
				out["reply"] = reply
			}
		} else {
			put(outcome)
		}
		for _, r := range rounds {
			for _, h := range r.Hits() {
				hits = append(hits, generic(h))
			}
		}
	} else {
		put(outcome)
	}
	client := map[string]any{}
	put(client)
	if rep.Failure != "" {
		client["failure"] = rep.Failure
	}
	out["client"] = client
	out["hits"] = hits
	out["progress"] = anyMaps(rep.Progress)
	out["questions"] = anyMaps(rep.Elicitations)
	out["gateway"] = map[string]any{"rounds": json.Number(fmt.Sprint(len(rounds)))}
	out["sdk"] = rep.SDK
	if len(rounds) > 0 {
		// The client's request as the gateway received it, first round.
		out["request"] = map[string]any{"headers": rounds[0].Headers, "meta": rounds[0].Meta}
	}
	if p.server == "mock" {
		count := func() (n int) {
			for _, s := range p.servers {
				n += s.up.Cancelled()
			}
			return n
		}
		now := count()
		// The server learns of a cancel after the client has its answer. A
		// step that checks the count gives it a moment to arrive.
		watches := false
		for _, ex := range st.Expect {
			watches = watches || ex.Path == "upstream.cancelled"
		}
		for wait := 0; watches && now == cancelled && wait < 50; wait++ {
			time.Sleep(10 * time.Millisecond)
			now = count()
		}
		reqs := make([]any, 0, len(ups))
		for _, u := range ups {
			reqs = append(reqs, u)
		}
		out["upstream"] = map[string]any{"requests": reqs, "cancelled": json.Number(fmt.Sprint(now - cancelled))}
	}
	if rep.Wire != nil {
		out["wire"] = wireView(rep.Wire)
	}
	if st.Op == "connect" {
		p.crossCheckConnect(i, req, outcome, seqMark, &fails)
	}
	out = generic(out) // one plain JSON value, numbers as written

	extra := map[string]any{"problems": p.g.Problems, "warnings": p.g.Warnings}
	for _, ex := range st.Expect {
		p.res.checked(p.client.Name())
		if msg := p.withVars(ex).Check(out, extra); msg != "" {
			fails = append(fails, msg)
		}
	}
	return out, "", fails
}

func anyMaps(in []map[string]any) []any {
	out := make([]any, 0, len(in))
	for _, m := range in {
		out = append(out, m)
	}
	return out
}

// wireView is the raw HTTP reply for expectations: status, headers with
// lower-case names, and the SSE frames in order.
func wireView(w *RawReply) map[string]any {
	headers := map[string]any{}
	for k, v := range w.Header {
		headers[strings.ToLower(k)] = strings.Join(v, ", ")
	}
	frames := []any{}
	messages := []any{}
	for _, f := range w.Frames {
		if f.Comment {
			frames = append(frames, map[string]any{"comment": true})
			continue
		}
		frames = append(frames, map[string]any{"message": f.Msg})
		messages = append(messages, f.Msg)
	}
	return map[string]any{
		"status": w.Status, "stream": w.Stream(), "headers": headers,
		"frames": frames, "messages": messages, "comments": w.Comments(), "body": string(w.Body),
	}
}

// withVars fills ${client.name}, ${client.harness} and ${server.language} in
// an expectation's expected values.
func (p *goldenRun) withVars(ex hooks.Expect) hooks.Expect {
	b, _ := json.Marshal(ex)
	if !bytes.Contains(b, []byte("${")) {
		return ex
	}
	s := string(b)
	for k, v := range p.vars {
		enc, _ := json.Marshal(v)
		s = strings.ReplaceAll(s, "${"+k+"}", string(enc[1:len(enc)-1]))
	}
	var out hooks.Expect
	if json.Unmarshal([]byte(s), &out) != nil {
		return ex
	}
	return out
}

// ---- turning a step into a request --------------------------------------------------------

// request maps a step to the request a client makes.
func (p *goldenRun) request(st hooks.Step, params json.RawMessage) (ClientRequest, error) {
	req := ClientRequest{}
	var obj map[string]any
	if len(params) > 0 {
		v, err := decodeExact(params)
		if err != nil {
			return req, fmt.Errorf("params that are not JSON")
		}
		obj, _ = v.(map[string]any)
		req.RawParams = params // as written: repeated keys and number spellings survive
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
	// A mock case states its client exactly: no identity given means none
	// declared, as on the engine harness. A reference case leaves the
	// identity to each client unless it states one.
	info, _ := client["clientInfo"].(map[string]any)
	if info == nil {
		info = map[string]any{}
	}
	if len(info) > 0 || p.server == "mock" {
		req.ClientInfo = info
	}
	if caps, ok := client["capabilities"]; ok {
		req.Capabilities = caps
	}
	return req, nil
}

// endpoint is where a step's request goes, with the step's caller and clock.
func (p *goldenRun) endpoint(st hooks.Step) string {
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
	// A case's flags go on the query string, as a client sets them.
	for _, name := range hooks.Flags {
		if p.sc.Flags[name] {
			q += "&" + name + "=true"
		}
	}
	return p.g.URL() + path + q
}

// observedForward is outcome.forward as the upstream saw it: which server
// was reached, with what method and params, and what it was told about its
// client. The gateway's own transport additions are taken out of _meta.
func observedForward(s goldenServer, rec Recorded, engine *hooks.Forward) (f map[string]any) {
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
			defer func() { f["progressToken"] = "gateway's own" }()
		} else if tok, ok := meta["progressToken"]; ok {
			defer func() { f["progressToken"] = tok }()
		}
		if len(meta) == 0 {
			delete(params, "_meta")
		}
	}
	connect := map[string]any{"clientInfo": info, "capabilities": caps}
	if engine.Connect != nil {
		connect["forwardMeta"] = engine.Connect.ForwardMeta
	}
	f = map[string]any{"slug": s.slug, "method": rec.Method, "params": params, "connect": connect}
	if engine.Timeout != "" {
		f["timeout"] = engine.Timeout
	}
	return f
}

// checkForwardedMeta compares the engine's decision on the client's _meta
// with what the upstream was sent. The decision alone (forwardMeta true or
// false) is the engine's intention; this is the proof that it was carried
// out. With the decision off, nothing of the client's may arrive. With it
// on, every key of the client's must arrive, unchanged. The protocol's own
// keys and the progress token are the gateway's and are left out of both.
func checkForwardedMeta(client any, rec Recorded, engine *hooks.Forward) string {
	own := func(v any) map[string]any {
		out := map[string]any{}
		m, _ := v.(map[string]any)
		for k, x := range m {
			if strings.HasPrefix(k, "io.modelcontextprotocol/") || k == "progressToken" {
				continue
			}
			out[k] = x
		}
		return out
	}
	got := own(rec.Exact()["_meta"])
	want := map[string]any{}
	decided := engine.Connect != nil && engine.Connect.ForwardMeta
	if decided {
		want = own(client)
	}
	if exactJSON(got) != exactJSON(want) {
		return fmt.Sprintf("the engine decided forwardMeta=%v for %s, but the upstream received _meta %s, want %s", decided, rec.Method, clip(exactJSON(got)), clip(exactJSON(want)))
	}
	return ""
}

// crossCheckConnect compares the server.session decision with what the
// upstream was actually told when the gateway reached it with that identity.
func (p *goldenRun) crossCheckConnect(idx int, req ClientRequest, out map[string]any, seqMark int64, fails *[]string) {
	res, ok := out["result"].(map[string]any)
	if !ok || p.server != "mock" {
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
			*fails = append(*fails, fmt.Sprintf("connect: the upstream %s was never reached", s.slug))
			return
		}
		for _, k := range []string{"clientInfo", "capabilities"} {
			if exactJSON(told[k]) != exactJSON(res[k]) {
				*fails = append(*fails, fmt.Sprintf("connect: the upstream was told %s = %s, the engine decided %s", k, exactJSON(told[k]), exactJSON(res[k])))
			}
		}
	}
}

// ---- the summary ----------------------------------------------------------------------------

// Cell states.
const (
	cellPass     = "ok"
	cellPart     = "ok*" // passed, with one or more steps excluded for this harness
	cellExcluded = "EX"  // the whole case is excluded for this harness
	cellOther    = "n/a" // an outlier for another harness
	cellFail     = "FAIL"
	cellNotRun   = "-" // a runtime or a fixture is missing
)

type goldenResults struct {
	mu             sync.Mutex
	cells          map[string]map[string]string // case key -> "client/server" -> state
	reasons        map[string]map[string]int    // harness -> reason -> count
	checks         map[string]int               // client harness -> expectations checked
	notes          []string
	missingClients []string
}

func newGoldenResults() *goldenResults {
	return &goldenResults{cells: map[string]map[string]string{}, reasons: map[string]map[string]int{}, checks: map[string]int{}}
}

func (r *goldenResults) set(key, col, state string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cells[key] == nil {
		r.cells[key] = map[string]string{}
	}
	r.cells[key][col] = state
}

func (r *goldenResults) excluded(harness, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reasons[harness] == nil {
		r.reasons[harness] = map[string]int{}
	}
	r.reasons[harness][reason]++
}

func (r *goldenResults) checked(client string) {
	r.mu.Lock()
	r.checks[client]++
	r.mu.Unlock()
}

func (r *goldenResults) note(s string) {
	r.mu.Lock()
	r.notes = append(r.notes, s)
	r.mu.Unlock()
}

var shortServer = map[string]string{"mock": "mock", "go": "go", "python": "py", "typescript": "ts"}

// table is the one summary: cases down, client and upstream across.
func (r *goldenResults) table(cases []*goldenCase, took time.Duration) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var clients []string
	for _, c := range Clients {
		clients = append(clients, c.Name())
	}
	clients = append(clients, r.missingClients...)
	var cols []string
	for _, c := range clients {
		for _, s := range serverColumns {
			cols = append(cols, c+"/"+s)
		}
	}
	const nameWidth = 64
	var b strings.Builder
	fmt.Fprintf(&b, "\nGOLDEN SET: %d cases, %d client harnesses, upstreams mock and %s, %s\n", len(cases), len(clients), strings.Join(servers.Kinds, ", "), took.Round(time.Second))
	b.WriteString("Cells: ok passed; ok* passed with some steps excluded for that harness; EX case excluded; n/a outlier for another harness; - not run; . not this case's upstream\n\n")
	fmt.Fprintf(&b, "%-*s %-6s", nameWidth+1, "", "")
	for _, c := range clients {
		fmt.Fprintf(&b, " %-*s", len(serverColumns)*5-1, c)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "%-*s %-6s", nameWidth+1, "case", "engine")
	for range clients {
		for _, s := range serverColumns {
			fmt.Fprintf(&b, " %-4s", shortServer[s])
		}
	}
	b.WriteString("\n")
	counts := map[string]map[string]int{} // client -> state -> cells
	total := map[string]int{}
	tiers := map[string]int{}
	for _, gc := range cases {
		tiers[tierOf(gc.sc)]++
		name := gc.key
		if len(name) > nameWidth {
			name = name[:nameWidth-1] + "…"
		}
		fmt.Fprintf(&b, "%-*s", nameWidth+1, name)
		est := r.cells[gc.key]["engine"]
		if est == "" {
			est = cellNotRun
		}
		if counts["engine"] == nil {
			counts["engine"] = map[string]int{}
		}
		counts["engine"][est]++
		total[est]++
		fmt.Fprintf(&b, " %-6s", est)
		for _, col := range cols {
			client, server, _ := strings.Cut(col, "/")
			applies := false
			for _, s := range columnsOf(gc.sc) {
				applies = applies || s == server
			}
			st := r.cells[gc.key][col]
			switch {
			case !applies:
				st = "."
			case st == "":
				st = cellNotRun
			}
			if applies {
				if counts[client] == nil {
					counts[client] = map[string]int{}
				}
				counts[client][st]++
				total[st]++
			}
			fmt.Fprintf(&b, " %-4s", st)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nCells by harness:\n")
	states := []string{cellPass, cellPart, cellExcluded, cellOther, cellFail, cellNotRun}
	fmt.Fprintf(&b, "  %-16s", "")
	for _, s := range states {
		fmt.Fprintf(&b, " %6s", s)
	}
	b.WriteString("   expectations checked\n")
	for _, c := range append([]string{"engine"}, clients...) {
		fmt.Fprintf(&b, "  %-16s", c)
		for _, s := range states {
			fmt.Fprintf(&b, " %6d", counts[c][s])
		}
		if c == "engine" {
			b.WriteString("\n")
			continue
		}
		fmt.Fprintf(&b, "   %d\n", r.checks[c])
	}
	// The same cells by upstream: the mock, and each reference server.
	b.WriteString("\nCells by upstream (all client harnesses):\n")
	fmt.Fprintf(&b, "  %-16s", "")
	for _, s := range states {
		fmt.Fprintf(&b, " %6s", s)
	}
	b.WriteString("\n")
	for _, server := range serverColumns {
		by := map[string]int{}
		for _, gc := range cases {
			for _, c := range clients {
				applies := false
				for _, col := range columnsOf(gc.sc) {
					applies = applies || col == server
				}
				if !applies {
					continue
				}
				st := r.cells[gc.key][c+"/"+server]
				if st == "" {
					st = cellNotRun
				}
				by[st]++
			}
		}
		name := server
		if server != "mock" {
			name = "server:" + server
		}
		fmt.Fprintf(&b, "  %-16s", name)
		for _, s := range states {
			fmt.Fprintf(&b, " %6d", by[s])
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "  %-16s", "all")
	for _, s := range states {
		fmt.Fprintf(&b, " %6d", total[s])
	}
	b.WriteString("\n\nCases by tier:\n")
	for _, k := range []string{tierAll, tierEngine, tierSome, tierOutlier} {
		fmt.Fprintf(&b, "  %-62s %d\n", k, tiers[k])
	}
	b.WriteString("\nExclusions applied, by harness (cases and steps), each reason once with its count:\n")
	harnesses := make([]string, 0, len(r.reasons))
	for h := range r.reasons {
		harnesses = append(harnesses, h)
	}
	sort.Strings(harnesses)
	for _, h := range harnesses {
		n := 0
		for _, c := range r.reasons[h] {
			n += c
		}
		fmt.Fprintf(&b, "  %s: %d\n", h, n)
		reasons := make([]string, 0, len(r.reasons[h]))
		for reason := range r.reasons[h] {
			reasons = append(reasons, reason)
		}
		sort.Slice(reasons, func(i, j int) bool {
			if r.reasons[h][reasons[i]] != r.reasons[h][reasons[j]] {
				return r.reasons[h][reasons[i]] > r.reasons[h][reasons[j]]
			}
			return reasons[i] < reasons[j]
		})
		for _, reason := range reasons {
			fmt.Fprintf(&b, "    %4d × %s\n", r.reasons[h][reason], reason)
		}
	}
	var outliers []string
	for _, gc := range cases {
		if gc.sc.Only != nil {
			outliers = append(outliers, fmt.Sprintf("  %s [%s]: %s", gc.key, gc.sc.Only.Harness, gc.sc.Only.Reason))
		}
	}
	if len(outliers) > 0 {
		b.WriteString("\nOutliers:\n" + strings.Join(outliers, "\n") + "\n")
	}
	if len(r.notes) > 0 {
		sort.Strings(r.notes)
		b.WriteString("\nNotes:\n  " + strings.Join(r.notes, "\n  ") + "\n")
	}
	return b.String()
}

// The tiers of the set.
const (
	tierAll     = "common set, run whole by every harness"
	tierEngine  = "common set, excluded for the engine harness only"
	tierSome    = "common set, with an exclusion for a client or server harness"
	tierOutlier = "outliers, for one harness"
)

func tierOf(sc *hooks.Scenario) string {
	if sc.Only != nil {
		return tierOutlier
	}
	engine, other := false, false
	note := func(list []hooks.Exclusion) {
		for _, e := range list {
			for _, h := range e.Harness {
				if h == "engine" {
					engine = true
				} else {
					other = true
				}
			}
		}
	}
	note(sc.Exclude)
	for _, st := range sc.Steps {
		note(st.Exclude)
	}
	switch {
	case other:
		return tierSome
	case engine:
		return tierEngine
	}
	return tierAll
}

// ---- helpers ------------------------------------------------------------------------------

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

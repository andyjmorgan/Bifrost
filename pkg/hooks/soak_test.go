package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestSoak is a long-running burn-in. It only runs when HOOKS_SOAK is set
// to a duration (for example HOOKS_SOAK=6h). Progress is written every
// minute as JSON lines to HOOKS_SOAK_LOG (default soak.log).
//
// Workers run a random mix of real operations; the test fails on a panic,
// a scenario failure, a listing that is not byte-identical to the first
// listing for the same input, a change to the shared catalog, or sustained
// live-heap or goroutine growth.
func TestSoak(t *testing.T) {
	durStr := os.Getenv("HOOKS_SOAK")
	if durStr == "" {
		t.Skip("set HOOKS_SOAK=<duration> to run the burn-in")
	}
	dur, err := time.ParseDuration(durStr)
	if err != nil {
		t.Fatal(err)
	}
	logPath := os.Getenv("HOOKS_SOAK_LOG")
	if logPath == "" {
		logPath = "soak.log"
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer logf.Close()
	var logMu sync.Mutex
	logLine := func(m map[string]any) {
		m["time"] = time.Now().UTC().Format(time.RFC3339)
		b, _ := json.Marshal(m)
		logMu.Lock()
		logf.Write(append(b, '\n'))
		logMu.Unlock()
	}

	baseTools := realTools(t)
	calls := soakCalls(t)
	// Snapshot scenario files once: the suite op must test the binary under
	// test against the expectations it was built with, not files edited on
	// disk while a long run is in progress. Bytes are parsed fresh per op so
	// workers never share scenario objects.
	scenarioFiles, _ := filepath.Glob("testdata/scenarios/*.json")
	scenarioBytes := make([][]byte, len(scenarioFiles))
	for i, f := range scenarioFiles {
		if scenarioBytes[i], err = os.ReadFile(f); err != nil {
			t.Fatal(err)
		}
	}
	set, probs := Compile(realisticRules())
	if probs != nil {
		t.Fatal(probs)
	}
	before, _ := json.Marshal(baseTools)
	e := &Engine{Signer: Signer{Key: []byte("soak")}, Limits: Limits{Budget: 30 * time.Second}}

	// catalog holds the current catalog; refreshes swap in fresh tool
	// objects (same content), exercising the prepared-tool cache cleanup.
	var catalog atomic.Pointer[[]*mcp.Tool]
	cloneCatalog := func() []*mcp.Tool {
		out := make([]*mcp.Tool, len(baseTools))
		for i, tt := range baseTools {
			c := *tt
			out[i] = &c
		}
		return out
	}
	first := cloneCatalog()
	catalog.Store(&first)

	var golden sync.Map // role -> listing bytes
	var ops, fails atomic.Int64
	var failMu sync.Mutex
	var failures []string
	fail := func(msg string) {
		fails.Add(1)
		failMu.Lock()
		if len(failures) < 50 {
			failures = append(failures, msg)
		}
		failMu.Unlock()
		logLine(map[string]any{"event": "failure", "detail": msg})
	}

	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()
	var opCounts [7]atomic.Int64
	workers := runtime.GOMAXPROCS(0)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w) + time.Now().UnixNano()))
			for ctx.Err() == nil {
				func() {
					defer func() {
						if p := recover(); p != nil {
							fail(fmt.Sprintf("panic: %v\n%s", p, debug.Stack()))
						}
					}()
					cat := *catalog.Load()
					role := "member"
					if rng.Intn(2) == 0 {
						role = "admin"
					}
					s := &Server{Slug: "dd", Generation: "1", Rules: set, Catalog: Catalog{Tools: cat}}
					r := Request{Route: "mux", Servers: []*Server{s}, Caller: map[string]any{"userId": "u", "role": role}, Client: map[string]any{"capabilities": map[string]any{"elicitation": map[string]any{}}}}
					switch op := rng.Intn(100); {
					case op < 25: // full listing, byte-identical per role
						opCounts[0].Add(1)
						out := e.ListTools(ctx, r)
						if out.Error != nil {
							if ctx.Err() == nil {
								fail("listing error: " + out.Error.Message)
							}
							return
						}
						b, _ := json.Marshal(out.Result)
						if g, loaded := golden.LoadOrStore(role, b); loaded && string(g.([]byte)) != string(b) {
							fail("listing bytes changed for role " + role)
						}
					case op < 70: // a real call
						opCounts[1].Add(1)
						c := calls[rng.Intn(len(calls))]
						r.Route = "direct"
						s.Catalog.Tools = soakCatalogFor(cat, c.Tool)
						out := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: c.Tool, Arguments: c.Arguments})
						if out.Forward == nil && out.Result == nil && out.Error == nil {
							fail("empty call outcome for " + c.Tool)
						}
					case op < 80: // elicitation round trip
						opCounts[2].Add(1)
						soakElicit(ctx, e, fail)
					case op < 90: // mux fallback reads
						opCounts[3].Add(1)
						soakFallback(ctx, e, fail)
					case op < 95: // catalog refresh
						opCounts[4].Add(1)
						fresh := cloneCatalog()
						catalog.Store(&fresh)
					case op < 99: // search mode
						opCounts[5].Add(1)
						r.Route = "search"
						e.ToolSearch(ctx, r, "search issues", 5)
					default: // the whole scenario suite
						opCounts[6].Add(1)
						for i, f := range scenarioFiles {
							ss, err := ParseScenarios(scenarioBytes[i])
							if err != nil {
								fail(err.Error())
								continue
							}
							for _, sc := range ss {
								sc.Dir = filepath.Dir(f)
								if rep := sc.Run(context.Background()); rep.Status == "fail" {
									fail(fmt.Sprintf("scenario failed: %s: %s %v", sc.Name, rep.Reason, rep.Failures))
								}
							}
						}
					}
					ops.Add(1)
				}()
			}
		}(w)
	}

	// Monitor: live heap after GC and goroutines, every minute.
	type sample struct {
		heap uint64
		gor  int
	}
	var samples []sample
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	start := time.Now()
	var lastOps int64
monitor:
	for {
		select {
		case <-ctx.Done():
			break monitor
		case <-tick.C:
			runtime.GC()
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			cur := ops.Load()
			counts := map[string]int64{}
			for i, n := range []string{"list", "call", "elicit", "fallback", "refresh", "search", "suite"} {
				counts[n] = opCounts[i].Load()
			}
			samples = append(samples, sample{heap: ms.HeapAlloc, gor: runtime.NumGoroutine()})
			prepN := 0
			prepCache.Range(func(_, _ any) bool { prepN++; return true })
			logLine(map[string]any{"event": "tick", "elapsed": time.Since(start).Round(time.Second).String(), "ops": cur, "opsPerSec": float64(cur-lastOps) / 60,
				"failures": fails.Load(), "heapMiB": float64(ms.HeapAlloc) / (1 << 20), "goroutines": runtime.NumGoroutine(), "numGC": ms.NumGC,
				"pauseTotalMs": float64(ms.PauseTotalNs) / 1e6, "prepCache": prepN, "byOp": counts})
			lastOps = cur
		}
	}
	wg.Wait()

	after, _ := json.Marshal(baseTools)
	if string(before) != string(after) {
		fail("the shared catalog was modified")
	}
	// Leak check: compare the average of the last quarter of samples with
	// the average of the second quarter (after warm-up).
	if n := len(samples); n >= 8 {
		avg := func(ss []sample) (float64, float64) {
			var h, g float64
			for _, s := range ss {
				h += float64(s.heap)
				g += float64(s.gor)
			}
			return h / float64(len(ss)), g / float64(len(ss))
		}
		h1, g1 := avg(samples[n/4 : n/2])
		h2, g2 := avg(samples[3*n/4:])
		logLine(map[string]any{"event": "leakcheck", "heapEarlyMiB": h1 / (1 << 20), "heapLateMiB": h2 / (1 << 20), "goroutinesEarly": g1, "goroutinesLate": g2})
		if h2 > h1*1.5 && h2-h1 > 64<<20 {
			fail(fmt.Sprintf("live heap grew from %.0f MiB to %.0f MiB", h1/(1<<20), h2/(1<<20)))
		}
		if g2 > g1*1.5 && g2-g1 > 50 {
			fail(fmt.Sprintf("goroutines grew from %.0f to %.0f", g1, g2))
		}
	}
	logLine(map[string]any{"event": "done", "ops": ops.Load(), "failures": fails.Load()})
	if fails.Load() > 0 {
		t.Fatalf("%d failures; first: %v", fails.Load(), failures)
	}
}

type soakCall struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

func soakCalls(t *testing.T) []soakCall {
	b, err := os.ReadFile("../../internal/e2e/testdata/real/toolcalls.json")
	if err != nil {
		t.Skip("real call examples not present")
	}
	var f struct {
		Calls []soakCall `json:"calls"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f.Calls
}

// soakCatalogFor returns the catalog plus a permissive definition for a
// harness tool the corpus does not define.
func soakCatalogFor(cat []*mcp.Tool, name string) []*mcp.Tool {
	for _, t := range cat {
		if t.Name == name {
			return cat
		}
	}
	return append(append([]*mcp.Tool(nil), cat...), &mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}})
}

var soakElicitSet = func() *Set {
	s, _ := Compile([]Rule{{Hook: "tools/call", When: "!has(input.ok)", Then: map[string]string{"result": "elicit('ok', 'Go {id}?', {'id': string(args.id)})"}}})
	return s
}()

func soakElicit(ctx context.Context, e *Engine, fail func(string)) {
	s := &Server{Slug: "a", Generation: "1", Rules: soakElicitSet, Catalog: Catalog{Tools: []*mcp.Tool{{Name: "t", InputSchema: map[string]any{"type": "object"}}}}}
	r := Request{Route: "direct", Servers: []*Server{s}, Caller: map[string]any{"userId": "u"}, Client: map[string]any{"capabilities": map[string]any{"elicitation": map[string]any{}}}}
	first := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"id":1}`)})
	ir, ok := first.Result.(InputRequired)
	if !ok {
		if ctx.Err() == nil {
			fail(fmt.Sprintf("elicit: no input_required: %+v", first.Error))
		}
		return
	}
	var resp mcp.InputResponseMap
	_ = json.Unmarshal([]byte(`{"bifrost.ok":{"action":"accept","content":{"confirm":true}}}`), &resp)
	second := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"id":1}`), RequestState: ir.RequestState, InputResponses: resp})
	if second.Forward == nil && ctx.Err() == nil {
		fail(fmt.Sprintf("elicit: accepted retry not forwarded: %+v", second.Error))
	}
	replay := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"id":2}`), RequestState: ir.RequestState, InputResponses: resp})
	if replay.Forward != nil {
		fail("elicit: state accepted with changed arguments")
	}
}

func soakFallback(ctx context.Context, e *Engine, fail func(string)) {
	mk := func(slug string, uris ...string) *Server {
		var rs []*mcp.Resource
		for _, u := range uris {
			rs = append(rs, &mcp.Resource{URI: u, Name: u})
		}
		return &Server{Slug: slug, Catalog: Catalog{Resources: rs}}
	}
	r := Request{Route: "mux", Servers: []*Server{mk("a", "file:///one", "file:///both"), mk("b", "file:///both")}, Caller: map[string]any{"userId": "u"}}
	if out := e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: "file:///one"}); out.Forward == nil || out.Forward.Slug != "a" {
		if ctx.Err() == nil {
			fail("fallback: unique URI not resolved to a")
		}
	}
	if out := e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: "file:///both"}); out.Error == nil || out.Fallback == nil || out.Fallback.Outcome != "ambiguous" {
		if ctx.Err() == nil {
			fail("fallback: ambiguous URI not reported")
		}
	}
}

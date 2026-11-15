package matrix

import (
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness"
	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness/clients"
	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness/servers"
)

// The client kinds, in table order.
var clientKinds = []string{"raw", "go", "python", "typescript"}

// clientsRequireEnv lists client kinds that must be available ("all", or a
// comma-separated list), the counterpart of servers.RequireEnv.
const clientsRequireEnv = "MATRIX_CLIENTS_REQUIRE"

// Cell states.
const (
	stPass  = "pass"
	stGap   = "known difference"
	stNE    = "not expressible"
	stFail  = "FAIL"
	stSkip  = "skipped"
	stNoRun = "not run"
)

// tcase is one row of the matrix.
type tcase struct {
	id    string
	title string
	// rules are the rules of the server under test, slug "ref" on the direct
	// route, or slug "alpha" on the mux and search routes.
	rules []hooks.Rule
	// route is "direct" (the default), "mux" or "search". The mux and search
	// routes add a second server, slug "beta", in another language.
	route string
	// rulesB are the rules of the second server.
	rulesB []hooks.Rule
	// caps are the client capabilities declared on connect.
	caps map[string]any
	// rawOnly says why the case cannot be expressed through a high-level SDK
	// client. Such a case runs with the raw client only.
	rawOnly string
	// known lists known client or server differences, keyed
	// "<client>/<server>" with "*" for any, each with a one-line reason.
	known map[string]string
	run   func(e *env)
}

func (c tcase) knownGap(client, server string) string {
	for k, why := range c.known {
		if ok, _ := path.Match(k, client+"/"+server); ok {
			return why
		}
	}
	return ""
}

// results collects the state of every cell.
type results struct {
	mu    sync.Mutex
	cells map[string]map[string]string // case id -> "client/server" -> state
	notes []string
}

func (r *results) set(caseID, combo, state string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cells[caseID] == nil {
		r.cells[caseID] = map[string]string{}
	}
	r.cells[caseID][combo] = state
}

func (r *results) note(s string) {
	r.mu.Lock()
	r.notes = append(r.notes, s)
	r.mu.Unlock()
}

func required(env, kind string) bool {
	for _, k := range strings.Split(os.Getenv(env), ",") {
		if k = strings.TrimSpace(k); k == "all" || k == kind {
			return true
		}
	}
	return false
}

func newDriver(kind string) (clients.Driver, error) {
	switch kind {
	case "raw":
		return &rawDriver{}, nil
	case "go":
		return clients.NewGo()
	case "python":
		return clients.NewPython()
	case "typescript":
		return clients.NewTypeScript()
	}
	return nil, fmt.Errorf("unknown client kind %q", kind)
}

// partner is the second server of the mux and search cases: the next kind
// in order that is running, so the two always differ in language.
func partner(kind string, running map[string]*servers.Server) string {
	for i, k := range servers.Kinds {
		if k != kind {
			continue
		}
		for j := 1; j < len(servers.Kinds); j++ {
			p := servers.Kinds[(i+j)%len(servers.Kinds)]
			if running[p] != nil {
				return p
			}
		}
	}
	return ""
}

// TestMatrix runs every case on every client and server, and logs the table.
func TestMatrix(t *testing.T) {
	start := time.Now()
	res := &results{cells: map[string]map[string]string{}}

	// Servers start once and serve every case. They are stateless, so the
	// three server columns share them.
	running := map[string]*servers.Server{}
	for _, kind := range servers.Kinds {
		if err := servers.Available(kind); err != nil {
			if required(servers.RequireEnv, kind) {
				t.Fatalf("%s reference server is required by %s but unavailable: %v", kind, servers.RequireEnv, err)
			}
			msg := fmt.Sprintf("SKIPPING %s reference server: %v", kind, err)
			fmt.Fprintf(os.Stderr, "\n*** %s ***\n\n", msg)
			t.Log(msg)
			res.note(msg)
			continue
		}
		running[kind] = servers.Start(t, kind)
	}
	// Clients: check each is installed. Each server column gets its own
	// driver processes, so the columns can run in parallel.
	usable := map[string]bool{}
	for _, kind := range clientKinds {
		d, err := newDriver(kind)
		if err != nil {
			if required(clientsRequireEnv, kind) {
				t.Fatalf("%s client is required by %s but unavailable: %v", kind, clientsRequireEnv, err)
			}
			if !errors.Is(err, clients.ErrNotInstalled) {
				t.Fatalf("%s client: %v", kind, err)
			}
			msg := fmt.Sprintf("SKIPPING %s client: %v", kind, err)
			fmt.Fprintf(os.Stderr, "\n*** %s ***\n\n", msg)
			t.Log(msg)
			res.note(msg)
			continue
		}
		_ = d.Close()
		usable[kind] = true
	}
	if len(running) == 0 {
		t.Fatal("no reference server can run here; run pkg/hooks/harness/servers/setup.sh")
	}

	t.Run("cells", func(t *testing.T) {
		for _, server := range servers.Kinds {
			srv := running[server]
			t.Run(server, func(t *testing.T) {
				if srv == nil {
					for _, c := range cases {
						for _, client := range clientKinds {
							res.set(c.id, client+"/"+server, stSkip)
						}
					}
					t.Skipf("SKIPPING %s server column: its runtime is not installed", server)
				}
				t.Parallel()
				drivers := map[string]clients.Driver{}
				for _, kind := range clientKinds {
					if usable[kind] {
						d, _ := newDriver(kind)
						drivers[kind] = d
					}
				}
				t.Cleanup(func() {
					for _, d := range drivers {
						_ = d.Close()
					}
				})
				for _, c := range cases {
					runCase(t, c, server, running, drivers, res)
				}
			})
		}
	})

	logTable(t, res, time.Since(start))
}

func runCase(t *testing.T, c tcase, server string, running map[string]*servers.Server, drivers map[string]clients.Driver, res *results) {
	t.Run(c.id, func(t *testing.T) {
		g := harness.NewGateway(t)
		endpoint := ""
		switch c.route {
		case "", "direct":
			g.AddRemote(t, "ref", running[server].URL, harness.V2026, c.rules...)
			endpoint = g.Direct("ref")
		case "mux", "search":
			p := partner(server, running)
			if p == "" {
				for _, client := range clientKinds {
					res.set(c.id, client+"/"+server, stSkip)
				}
				msg := fmt.Sprintf("SKIPPING %s on %s: the %s route needs a second server in another language, and none is running", c.id, server, c.route)
				fmt.Fprintf(os.Stderr, "\n*** %s ***\n\n", msg)
				t.Skip(msg)
			}
			g.AddRemote(t, "alpha", running[server].URL, harness.V2026, c.rules...)
			g.AddRemote(t, "beta", running[p].URL, harness.V2026, c.rulesB...)
			endpoint = g.Mux()
			if c.route == "search" {
				endpoint = g.Search()
			}
		default:
			t.Fatalf("case %s: unknown route %q", c.id, c.route)
		}
		for _, client := range clientKinds {
			combo := client + "/" + server
			t.Run(client, func(t *testing.T) {
				state := stNoRun
				defer func() {
					switch {
					case state != stNoRun:
					case t.Failed():
						state = stFail
					case t.Skipped():
						state = stSkip
					default:
						state = stPass
					}
					res.set(c.id, combo, state)
				}()
				d := drivers[client]
				if d == nil {
					state = stSkip
					t.Skipf("SKIPPING %s client: not installed", client)
				}
				if c.rawOnly != "" && client != "raw" {
					state = stNE
					t.Skipf("not expressible through an SDK client: %s", c.rawOnly)
				}
				if why := c.knownGap(client, server); why != "" && os.Getenv("HOOKS_KNOWN_GAPS") == "" {
					state = stGap
					t.Skipf("known difference (%s client, %s server): %s (set HOOKS_KNOWN_GAPS=1 to run)", client, server, why)
				}
				e := &env{t: t, client: client, server: server, d: d, g: g, endpoint: endpoint}
				if r, ok := d.(*rawDriver); ok {
					e.raw = r
				}
				e.connect(c.caps)
				c.run(e)
			})
		}
	})
}

// short names for the table's columns.
var shortName = map[string]string{"raw": "raw", "go": "go", "python": "py", "typescript": "ts"}

func logTable(t *testing.T, res *results, took time.Duration) {
	res.mu.Lock()
	defer res.mu.Unlock()
	glyph := map[string]string{stPass: "ok", stGap: "KD", stNE: "NE", stFail: "FAIL", stSkip: "skip", stNoRun: "-"}
	var combos []string
	for _, c := range clientKinds {
		for _, s := range servers.Kinds {
			combos = append(combos, c+"/"+s)
		}
	}
	idw := len("case")
	for _, c := range cases {
		idw = max(idw, len(c.id))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nRULES ENGINE MATRIX: %d cases x %d combinations (client/server), %s\n\n", len(cases), len(combos), took.Round(time.Second))
	fmt.Fprintf(&b, "%-*s", idw+2, "case")
	for _, combo := range combos {
		cl, sv, _ := strings.Cut(combo, "/")
		fmt.Fprintf(&b, " %-6s", shortName[cl]+"/"+shortName[sv])
	}
	b.WriteString("\n")
	counts := map[string]int{}
	for _, c := range cases {
		fmt.Fprintf(&b, "%-*s", idw+2, c.id)
		for _, combo := range combos {
			st := res.cells[c.id][combo]
			if st == "" {
				st = stNoRun
			}
			counts[st]++
			fmt.Fprintf(&b, " %-6s", glyph[st])
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "  %-5s %-17s %d\n", glyph[k], k, counts[k])
	}
	fmt.Fprintf(&b, "  total cells             %d\n", len(cases)*len(combos))
	var gaps []string
	for _, c := range cases {
		for k, why := range c.known {
			gaps = append(gaps, fmt.Sprintf("  %s [%s]: %s", c.id, k, why))
		}
	}
	if len(gaps) > 0 {
		sort.Strings(gaps)
		b.WriteString("\nKnown differences:\n" + strings.Join(gaps, "\n") + "\n")
	}
	if len(res.notes) > 0 {
		b.WriteString("\nNotes:\n  " + strings.Join(res.notes, "\n  ") + "\n")
	}
	t.Log(b.String())
}

package hooks

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Profile records per-rule evaluation cost and the cost of the work around
// rules. It is opt-in (Set.EnableProfile) and safe under parallel listing:
// every counter is atomic.
type Profile struct {
	mu     sync.Mutex
	rules  []*RuleStat
	phases map[string]*phaseStat
}

// RuleStat is one rule's cost.
type RuleStat struct {
	Hook    string
	Rule    string
	evals   atomic.Uint64
	matches atomic.Uint64
	whenNs  atomic.Uint64
	thenNs  atomic.Uint64
	thens   atomic.Uint64
	maxNs   atomic.Uint64
}

func (r *RuleStat) observeWhen(d time.Duration, match bool) {
	r.evals.Add(1)
	if match {
		r.matches.Add(1)
	}
	ns := uint64(d.Nanoseconds())
	r.whenNs.Add(ns)
	for {
		m := r.maxNs.Load()
		if ns <= m || r.maxNs.CompareAndSwap(m, ns) {
			break
		}
	}
}

func (r *RuleStat) observeThen(d time.Duration) {
	r.thens.Add(1)
	r.thenNs.Add(uint64(d.Nanoseconds()))
}

type phaseStat struct {
	n  atomic.Uint64
	ns atomic.Uint64
}

// EnableProfile starts profiling this set and returns the profile.
func (s *Set) EnableProfile() *Profile {
	p := &Profile{phases: map[string]*phaseStat{}}
	for _, rs := range s.byHook {
		for _, r := range rs {
			r.stat = &RuleStat{Hook: r.rule.Hook, Rule: r.label}
			p.rules = append(p.rules, r.stat)
		}
	}
	s.prof = p
	return p
}

// phaseTimer times consecutive phases of one item's work.
type phaseTimer struct {
	p    *Profile
	last time.Time
}

func (p *Profile) phase() *phaseTimer {
	if p == nil {
		return nil
	}
	return &phaseTimer{p: p, last: time.Now()}
}

func (t *phaseTimer) mark(name string) {
	if t == nil {
		return
	}
	now := time.Now()
	t.p.add(name, now.Sub(t.last))
	t.last = now
}

func (p *Profile) add(name string, d time.Duration) {
	p.mu.Lock()
	ph, ok := p.phases[name]
	if !ok {
		ph = &phaseStat{}
		p.phases[name] = ph
	}
	p.mu.Unlock()
	ph.n.Add(1)
	ph.ns.Add(uint64(d.Nanoseconds()))
}

// RuleReport is one line of the report.
type RuleReport struct {
	Hook    string  `json:"hook"`
	Rule    string  `json:"rule"`
	Evals   uint64  `json:"evals"`
	Matches uint64  `json:"matches"`
	WhenAvg float64 `json:"whenAvgNs"`
	WhenMax uint64  `json:"whenMaxNs"`
	ThenAvg float64 `json:"thenAvgNs"`
	TotalMs float64 `json:"totalMs"`
}

// Rules reports every rule, slowest total first.
func (p *Profile) Rules() []RuleReport {
	var out []RuleReport
	for _, r := range p.rules {
		ev, th := r.evals.Load(), r.thens.Load()
		rr := RuleReport{Hook: r.Hook, Rule: r.Rule, Evals: ev, Matches: r.matches.Load(), WhenMax: r.maxNs.Load(),
			TotalMs: float64(r.whenNs.Load()+r.thenNs.Load()) / 1e6}
		if ev > 0 {
			rr.WhenAvg = float64(r.whenNs.Load()) / float64(ev)
		}
		if th > 0 {
			rr.ThenAvg = float64(r.thenNs.Load()) / float64(th)
		}
		out = append(out, rr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TotalMs > out[j].TotalMs })
	return out
}

// Phases reports total and per-item time for each phase.
func (p *Profile) Phases() map[string][2]float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string][2]float64{}
	for k, v := range p.phases {
		n := v.n.Load()
		avg := 0.0
		if n > 0 {
			avg = float64(v.ns.Load()) / float64(n)
		}
		out[k] = [2]float64{float64(v.ns.Load()) / 1e6, avg}
	}
	return out
}

// String renders a table.
func (p *Profile) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-14s %-70s %8s %8s %10s %10s %10s %9s\n", "hook", "rule", "evals", "matches", "when avg", "when max", "then avg", "total")
	for _, r := range p.Rules() {
		fmt.Fprintf(&b, "%-14s %-70s %8d %8d %8.0fns %8dns %8.0fns %7.2fms\n", r.Hook, trimTo(r.Rule, 70), r.Evals, r.Matches, r.WhenAvg, r.WhenMax, r.ThenAvg, r.TotalMs)
	}
	b.WriteString("\nphase        total      per item\n")
	ph := p.Phases()
	for _, k := range sortedKeys(ph) {
		fmt.Fprintf(&b, "%-10s %8.2fms %10.0fns\n", k, ph[k][0], ph[k][1])
	}
	return b.String()
}

func trimTo(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// profile returns the set's profile, nil-safe on a nil set.
func (s *Set) profile() *Profile {
	if s == nil {
		return nil
	}
	return s.prof
}

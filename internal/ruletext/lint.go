package ruletext

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Lint looks for rules that compile but probably do not do what the admin
// meant. Every finding is a warning. catalog is the names the server is
// known to offer, by kind (tool, prompt); a kind with no names is not
// checked, because the gateway may simply not have listed it yet.
func Lint(text map[string]string, catalog map[string][]string) []Problem {
	var out []Problem
	warn := func(stage string, line int, f string, a ...any) {
		out = append(out, Problem{Stage: stage, Line: line, Level: "warning", Message: fmt.Sprintf(f, a...)})
	}
	parsedBy := map[string][]parsed{}
	for i := range stages {
		s := &stages[i]
		parsedBy[s.ID], _ = parseStage(s.ID, text[s.ID])
	}
	// A name a list's post rule gives an item is a name later rules may use.
	renamed := map[string]map[string]bool{}
	for i := range stages {
		s := &stages[i]
		for _, p := range parsedBy[s.ID] {
			for _, a := range p.acts {
				if v, ok := unquote(a.expr); ok && a.kind == "set" && a.path == "name" {
					if renamed[s.Item] == nil {
						renamed[s.Item] = map[string]bool{}
					}
					renamed[s.Item][v] = true
				}
			}
		}
	}
	for i := range stages {
		s := &stages[i]
		stopped := 0 // the line of a rule that denies everything
		for _, p := range parsedBy[s.ID] {
			if stopped > 0 {
				warn(s.ID, p.line, "this rule never runs: line %d denies everything before it gets here", stopped)
				continue
			}
			switch cond := strings.TrimSpace(p.when); {
			case cond == "false":
				warn(s.ID, p.line, "this rule never applies: its condition is false")
			case cond == "true" && !p.audit && hasAction(p, "deny"):
				stopped = p.line
			}
			names := catalog[s.Item]
			if len(names) == 0 || (s.Item != "tool" && s.Item != "prompt") {
				continue
			}
			for _, ref := range nameRefs(p.when) {
				if contains(names, ref.name) || (!ref.upstream && renamed[s.Item][ref.name]) {
					continue
				}
				msg := fmt.Sprintf("this server has no %s named %q", s.Item, ref.name)
				if near := nearest(ref.name, names); near != "" {
					msg += fmt.Sprintf("; did you mean %q?", near)
				}
				warn(s.ID, p.line, "%s", msg)
			}
		}
	}
	return sorted(out)
}

func hasAction(p parsed, kind string) bool {
	for _, a := range p.acts {
		if a.kind == kind {
			return true
		}
	}
	return false
}

type nameRef struct {
	name     string
	upstream bool
}

var (
	nameEq = regexp.MustCompile(`(^|[^\w.])(upstream\.)?name\s*[=!]=\s*(?:'([^'\\]*)'|"([^"\\]*)")`)
	nameIn = regexp.MustCompile(`(^|[^\w.])(upstream\.)?name\s+in\s*\[([^\]]*)\]`)
	strLit = regexp.MustCompile(`'([^'\\]*)'|"([^"\\]*)"`)
)

// nameRefs finds the names a condition compares the item's name with:
// name == 'x', upstream.name != "x" and name in ['x', 'y'].
func nameRefs(when string) []nameRef {
	var out []nameRef
	for _, m := range nameEq.FindAllStringSubmatch(when, -1) {
		out = append(out, nameRef{name: m[3] + m[4], upstream: m[2] != ""})
	}
	for _, m := range nameIn.FindAllStringSubmatch(when, -1) {
		for _, l := range strLit.FindAllStringSubmatch(m[3], -1) {
			out = append(out, nameRef{name: l[1] + l[2], upstream: m[2] != ""})
		}
	}
	return out
}

// nearest returns the name closest to want, when one is within two edits.
func nearest(want string, names []string) string {
	best, bestD := "", 3
	sortedNames := append([]string(nil), names...)
	sort.Strings(sortedNames)
	for _, n := range sortedNames {
		if d := distance(want, n); d < bestD {
			best, bestD = n, d
		}
	}
	return best
}

func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

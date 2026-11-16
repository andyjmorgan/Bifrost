package ruletext

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
)

// Problem is something wrong with the text, placed on a line of a stage.
type Problem struct {
	Stage   string `json:"stage"`
	Line    int    `json:"line"` // 1-based; 0 is the stage as a whole
	Message string `json:"message"`
}

type action struct {
	kind string // deny, confirm, set
	line int
	path string // set only
	expr string
}

type parsed struct {
	line  int
	audit bool
	when  string
	acts  []action
}

// origin is where a compiled rule came from, to place the engine's problems.
type origin struct {
	stage string
	when  int
	lines map[string]int // then-field -> line
}

var (
	setRe  = regexp.MustCompile(`^set\s+([A-Za-z_][\w.*/\-]*)\s*=(.*)$`)
	wordRe = regexp.MustCompile(`^(audit\s+when|when|deny|confirm|set)(\s|$|["'])`)
)

// keyword reports the keyword a trimmed line starts with, and the rest.
func keyword(t string) (kw, rest string) {
	m := wordRe.FindStringSubmatch(t)
	if m == nil {
		return "", t
	}
	kw = m[1]
	if strings.HasPrefix(kw, "audit") {
		kw = "audit when"
	}
	rest = strings.TrimSpace(t[len(m[1]):])
	if kw == "set" && !setRe.MatchString(t) {
		return "", t // "set" as the start of an expression, not an action
	}
	return kw, rest
}

// splitInline splits "cond  action" on a when line. The action starts at
// the first deny, confirm or set that stands outside any string or bracket.
func splitInline(s string) (cond, act string) {
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'' || c == '"':
			i = skipString(s, i)
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
		case (c == ' ' || c == '\t') && depth == 0:
			j := i
			for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			if kw, _ := keyword(s[j:]); kw == "deny" || kw == "confirm" || kw == "set" {
				return strings.TrimSpace(s[:i]), s[j:]
			}
			i = j - 1
		}
	}
	return strings.TrimSpace(s), ""
}

// skipString returns the index of the closing quote of the string literal
// that opens at i, or the end of s when it does not close.
func skipString(s string, i int) int {
	q := s[i]
	if strings.HasPrefix(s[i:], strings.Repeat(string(q), 3)) {
		if end := strings.Index(s[i+3:], strings.Repeat(string(q), 3)); end >= 0 {
			return i + 3 + end + 2
		}
		return len(s)
	}
	raw := i > 0 && (s[i-1] == 'r' || s[i-1] == 'R')
	for j := i + 1; j < len(s); j++ {
		if s[j] == '\\' && !raw {
			j++
			continue
		}
		if s[j] == q {
			return j
		}
	}
	return len(s)
}

func parseStage(id, text string) (rules []parsed, probs []Problem) {
	bad := func(line int, f string, a ...any) {
		probs = append(probs, Problem{Stage: id, Line: line, Message: fmt.Sprintf(f, a...)})
	}
	var cur *parsed
	var open *string // the expression a continuation line adds to
	flush := func() {
		if cur != nil {
			rules = append(rules, *cur)
		}
		cur, open = nil, nil
	}
	addAction := func(line int, kw, rest, full string) {
		a := action{kind: kw, line: line}
		switch kw {
		case "set":
			m := setRe.FindStringSubmatch(full)
			a.path, a.expr = m[1], strings.TrimSpace(m[2])
			if strings.HasPrefix(a.expr, "=") {
				bad(line, "set takes one = : set %s = <value>", a.path)
				a.expr = strings.TrimSpace(strings.TrimLeft(a.expr, "="))
			}
		default:
			a.expr = rest
		}
		cur.acts = append(cur.acts, a)
		open = &cur.acts[len(cur.acts)-1].expr
	}
	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := i + 1
		t := strings.TrimSpace(raw)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		indented := raw[0] == ' ' || raw[0] == '\t'
		kw, rest := keyword(t)
		switch kw {
		case "when", "audit when":
			flush()
			cur = &parsed{line: line, audit: kw == "audit when"}
			cond, act := splitInline(rest)
			cur.when = cond
			open = &cur.when
			if act != "" {
				akw, arest := keyword(act)
				addAction(line, akw, arest, act)
			}
		case "deny", "confirm", "set":
			if cur == nil {
				bad(line, "%s needs a when line above it", kw)
				continue
			}
			addAction(line, kw, rest, t)
		default:
			if open == nil || !indented {
				bad(line, "expected a line that starts with when, set, deny or confirm")
				continue
			}
			*open = strings.TrimSpace(*open + " " + t)
		}
	}
	flush()
	return rules, probs
}

// build turns one stage's parsed rules into engine rules.
func build(s *Stage, ps []parsed) (rules []hooks.Rule, from []origin, probs []Problem) {
	bad := func(line int, f string, a ...any) {
		probs = append(probs, Problem{Stage: s.ID, Line: line, Message: fmt.Sprintf(f, a...)})
	}
	for _, p := range ps {
		if p.when == "" {
			bad(p.line, "when needs a condition; write when true for a rule that always applies")
			continue
		}
		if len(p.acts) == 0 {
			bad(p.line, "this rule does nothing: add %s", strings.Join(s.Actions, ", "))
			continue
		}
		then, lines := map[string]string{}, map[string]int{}
		when, ok, hides, changes := p.when, true, false, false
		for _, a := range p.acts {
			if !contains(s.Actions, a.kind) {
				bad(a.line, "%s is not available at this stage; it takes %s", a.kind, strings.Join(s.Actions, ", "))
				ok = false
				continue
			}
			put := func(k, v string) {
				if _, dup := then[k]; dup {
					bad(a.line, "this rule already sets %s", k)
					ok = false
				}
				then[k], lines[k] = v, a.line
			}
			switch a.kind {
			case "deny":
				hides = true
				put("decision", "'deny'")
				if a.expr != "" {
					put("reason", a.expr)
				}
			case "confirm":
				key, call, err := confirmCall(s.ID, a.expr)
				if err != nil {
					bad(a.line, "%v", err)
					ok = false
					continue
				}
				when = "(" + p.when + ") && !has(input." + key + ")"
				put("result", call)
			case "set":
				if a.expr == "" {
					bad(a.line, "set %s needs a value", a.path)
					ok = false
					continue
				}
				if a.path == "decision" || a.path == "reason" {
					hides = true
				} else {
					changes = true
				}
				put(a.path, a.expr)
			}
		}
		if !ok {
			continue
		}
		hook := s.setHook
		if hook == "" || (s.split() && hides) {
			hook = s.denyHook
		}
		if s.split() && hides && changes {
			bad(p.line, "a rule here either hides the item (deny) or changes it (set), not both; write two rules")
			continue
		}
		r := hooks.Rule{Name: fmt.Sprintf("%s:%d", s.ID, p.line), Hook: hook, When: when, Then: then}
		if p.audit {
			r.Mode = "audit"
		}
		rules = append(rules, r)
		from = append(from, origin{stage: s.ID, when: p.line, lines: lines})
	}
	return rules, from, probs
}

// confirmCall turns confirm "Delete {args.id}?" into the engine's elicit
// call. Each {expression} becomes a named value, which the engine quotes
// and truncates so argument text cannot pose as the gateway's own wording.
func confirmCall(stage, lit string) (key, call string, err error) {
	msg, ok := unquote(lit)
	if !ok || msg == "" {
		return "", "", fmt.Errorf(`confirm takes the question in quotes: confirm "Delete {args.id}?"`)
	}
	h := fnv.New32a()
	h.Write([]byte(stage + "\n" + msg))
	key = fmt.Sprintf("confirm_%08x", h.Sum32())
	var tmpl strings.Builder
	var vals []string
	for i := 0; i < len(msg); i++ {
		if msg[i] != '{' {
			tmpl.WriteByte(msg[i])
			continue
		}
		depth, j := 1, i+1
		for ; j < len(msg) && depth > 0; j++ {
			switch msg[j] {
			case '{':
				depth++
			case '}':
				depth--
			}
		}
		if depth != 0 {
			return "", "", fmt.Errorf("the question has a { with no closing }")
		}
		expr := strings.TrimSpace(msg[i+1 : j-1])
		if expr == "" {
			return "", "", fmt.Errorf("the question has an empty {}")
		}
		name := fmt.Sprintf("v%d", len(vals)+1)
		vals = append(vals, strconv.Quote(name)+": "+expr)
		tmpl.WriteString("{" + name + "}")
		i = j - 1
	}
	if len(vals) == 0 {
		return key, fmt.Sprintf("elicit(%s, %s)", strconv.Quote(key), strconv.Quote(msg)), nil
	}
	return key, fmt.Sprintf("elicit(%s, %s, {%s})", strconv.Quote(key), strconv.Quote(tmpl.String()), strings.Join(vals, ", ")), nil
}

// unquote reads a single- or double-quoted string literal.
func unquote(lit string) (string, bool) {
	lit = strings.TrimSpace(lit)
	if len(lit) < 2 || (lit[0] != '"' && lit[0] != '\'') || lit[len(lit)-1] != lit[0] {
		return "", false
	}
	var b strings.Builder
	body := lit[1 : len(lit)-1]
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c == lit[0] {
			return "", false // the literal ended early: something follows it
		}
		if c != '\\' || i+1 == len(body) {
			b.WriteByte(c)
			continue
		}
		i++
		switch body[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		default:
			b.WriteByte(body[i])
		}
	}
	return b.String(), true
}

// Compile turns the text of each stage into the engine's rules and checks
// them with the engine's own compiler. Problems come back placed on the
// line that caused them. The rules are usable only when there are none.
func Compile(text map[string]string) ([]hooks.Rule, []Problem) {
	var rules []hooks.Rule
	var from []origin
	var probs []Problem
	for id := range text {
		if _, ok := byID[id]; !ok && strings.TrimSpace(text[id]) != "" {
			probs = append(probs, Problem{Stage: id, Message: "there is no such stage"})
		}
	}
	for i := range stages {
		s := &stages[i]
		ps, pp := parseStage(s.ID, text[s.ID])
		rs, fr, bp := build(s, ps)
		probs = append(append(probs, pp...), bp...)
		rules, from = append(rules, rs...), append(from, fr...)
	}
	if len(probs) > 0 {
		return nil, sorted(probs)
	}
	if _, eps := hooks.Compile(rules); eps != nil {
		for _, ep := range eps {
			if ep.Rule < 0 || ep.Rule >= len(from) {
				probs = append(probs, Problem{Message: ep.Message})
				continue
			}
			o := from[ep.Rule]
			line, ok := o.lines[ep.Field]
			if !ok {
				line = o.when
			}
			msg := tidy(ep.Message)
			if ep.Field != "" && ep.Field != "when" && ok {
				msg = ep.Field + ": " + msg
			}
			probs = append(probs, Problem{Stage: o.stage, Line: line, Message: msg})
		}
		return nil, sorted(probs)
	}
	return rules, nil
}

var (
	exprPos   = regexp.MustCompile(`^\d+:\d+: `)
	container = regexp.MustCompile(` \(in container '[^']*'\)`)
)

// tidy drops what only makes sense beside the expression alone: the
// position inside it, and the CEL container.
func tidy(msg string) string {
	return container.ReplaceAllString(exprPos.ReplaceAllString(msg, ""), "")
}

func sorted(ps []Problem) []Problem {
	order := map[string]int{}
	for i, s := range stages {
		order[s.ID] = i
	}
	sort.SliceStable(ps, func(a, b int) bool {
		if ps[a].Stage != ps[b].Stage {
			return order[ps[a].Stage] < order[ps[b].Stage]
		}
		return ps[a].Line < ps[b].Line
	})
	return ps
}

// Render writes engine rules as text, by stage. It is how rules saved
// through the API as JSON are shown; it keeps every rule's meaning but not
// its name. A rule that is switched off is written as a comment.
func Render(rules []hooks.Rule) map[string]string {
	out := map[string]string{}
	for _, r := range rules {
		s, ok := byHook[r.Hook]
		if !ok {
			continue
		}
		var b strings.Builder
		head := "when "
		if r.Mode == "audit" {
			head = "audit when "
		}
		b.WriteString(head + oneExpr(r.When) + "\n")
		keys := make([]string, 0, len(r.Then))
		for k := range r.Then {
			if k != "decision" && k != "reason" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		if _, deny := r.Then["decision"]; deny {
			b.WriteString(strings.TrimRight("  deny "+oneExpr(r.Then["reason"]), " ") + "\n")
		} else if reason, ok := r.Then["reason"]; ok {
			b.WriteString("  set reason = " + reason + "\n")
		}
		for _, k := range keys {
			b.WriteString("  set " + k + " = " + oneExpr(r.Then[k]) + "\n")
		}
		block := b.String()
		if r.Enabled != nil && !*r.Enabled {
			block = "# Switched off:\n# " + strings.ReplaceAll(strings.TrimRight(block, "\n"), "\n", "\n# ") + "\n"
		}
		if out[s.ID] != "" {
			out[s.ID] += "\n"
		}
		out[s.ID] += block
	}
	return out
}

// oneExpr lays an expression out for the text: a line break in it becomes
// an indented continuation line.
func oneExpr(e string) string {
	return strings.ReplaceAll(strings.TrimSpace(e), "\n", "\n      ")
}

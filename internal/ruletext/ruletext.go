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

// Problem is something wrong with the text, placed on a line of a hook's rules.
type Problem struct {
	Hook    string `json:"hook"`
	Line    int    `json:"line"` // 1-based; 0 is the hook's rules as a whole
	Message string `json:"message"`
	// Level is "warning" for something that compiles but looks wrong. A
	// warning never stops a save. Empty means the rules cannot be used.
	Level string `json:"level,omitempty"`
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
	hook  string
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

func parseHook(id, text string) (rules []parsed, probs []Problem) {
	bad := func(line int, f string, a ...any) {
		probs = append(probs, Problem{Hook: id, Line: line, Message: fmt.Sprintf(f, a...)})
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
		// A // comment ends at the end of its line. Lines are joined into
		// one expression, so it is taken off here: left in, it would run to
		// the end of the expression and swallow the lines that continue it.
		if t = strings.TrimSpace(stripComment(t)); t == "" {
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

// build turns one hook's parsed rules into engine rules.
func build(s *Hook, ps []parsed) (rules []hooks.Rule, from []origin, probs []Problem) {
	bad := func(line int, f string, a ...any) {
		probs = append(probs, Problem{Hook: s.Name, Line: line, Message: fmt.Sprintf(f, a...)})
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
		when, ok := p.when, true
		for _, a := range p.acts {
			if !contains(s.Actions, a.kind) {
				bad(a.line, "%s is not available at this hook; it takes %s", a.kind, strings.Join(s.Actions, ", "))
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
				put(hooks.KeyDecision, "'deny'")
				if a.expr != "" {
					put(hooks.KeyReason, a.expr)
				}
			case "confirm":
				call, err := confirmCall(s.Name, a.expr)
				if err != nil {
					bad(a.line, "%v", err)
					ok = false
					continue
				}
				put(hooks.KeyConfirm, call)
			case "set":
				if a.expr == "" {
					bad(a.line, "set %s needs a value", a.path)
					ok = false
					continue
				}
				put(a.path, a.expr)
			}
		}
		if !ok {
			continue
		}
		r := hooks.Rule{Name: fmt.Sprintf("%s:%d", s.Name, p.line), Hook: s.Name, When: when, Then: then}
		if p.audit {
			r.Mode = "audit"
		}
		rules = append(rules, r)
		from = append(from, origin{hook: s.Name, when: p.line, lines: lines})
	}
	return rules, from, probs
}

// confirmCall turns confirm "Delete {tool.name}?" into the engine's elicit
// call. Each {expression} becomes a named value, which the engine quotes
// and truncates so argument text cannot pose as the gateway's own wording.
func confirmCall(hook, lit string) (call string, err error) {
	msg, ok := unquote(lit)
	if !ok || msg == "" {
		return "", fmt.Errorf(`confirm takes the question in quotes: confirm "Delete {tool.name}?"`)
	}
	h := fnv.New32a()
	h.Write([]byte(hook + "\n" + msg))
	key := fmt.Sprintf("confirm_%08x", h.Sum32())
	var tmpl strings.Builder
	var vals []string
	for i := 0; i < len(msg); i++ {
		if msg[i] != '{' {
			tmpl.WriteByte(msg[i])
			continue
		}
		// The expression runs to the matching }, and a brace inside a
		// string in it is text, not a bracket.
		depth, j := 1, i+1
		var quote byte
		for ; j < len(msg) && depth > 0; j++ {
			switch c := msg[j]; {
			case quote != 0:
				if c == '\\' {
					j++
				} else if c == quote {
					quote = 0
				}
			case c == '\'' || c == '"':
				quote = c
			case c == '{':
				depth++
			case c == '}':
				depth--
			}
		}
		if depth != 0 {
			return "", fmt.Errorf("the question has a { with no closing }")
		}
		expr := strings.TrimSpace(msg[i+1 : j-1])
		if expr == "" {
			return "", fmt.Errorf("the question has an empty {}")
		}
		name := fmt.Sprintf("v%d", len(vals)+1)
		vals = append(vals, strconv.Quote(name)+": "+expr)
		tmpl.WriteString("{" + name + "}")
		i = j - 1
	}
	if len(vals) == 0 {
		return fmt.Sprintf("elicit(%s, %s)", strconv.Quote(key), strconv.Quote(msg)), nil
	}
	return fmt.Sprintf("elicit(%s, %s, {%s})", strconv.Quote(key), strconv.Quote(tmpl.String()), strings.Join(vals, ", ")), nil
}

// stripComment removes a // comment from the end of a line. A // inside a
// string, such as 'https://example.com', is text.
func stripComment(line string) string {
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '\'' || line[i] == '"':
			i = skipString(line, i)
		case line[i] == '/' && i+1 < len(line) && line[i+1] == '/':
			return line[:i]
		}
	}
	return line
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

// Compile turns the text of each hook, by hook name, into the engine's rules and checks
// them with the engine's own compiler. Problems come back placed on the
// line that caused them. The rules are usable only when there are none.
func Compile(text map[string]string) ([]hooks.Rule, []Problem) {
	rules, _, probs, _ := compile(text)
	return rules, probs
}

// compile is Compile, with what Lint needs beside it: where each rule came
// from, and what the engine noted about rules that compile.
func compile(text map[string]string) (rules []hooks.Rule, from []origin, probs, notes []Problem) {
	for id := range text {
		if _, ok := byName[id]; !ok && strings.TrimSpace(text[id]) != "" {
			probs = append(probs, Problem{Hook: id, Message: "there is no such hook"})
		}
	}
	for i := range all {
		s := &all[i]
		ps, pp := parseHook(s.Name, text[s.Name])
		rs, fr, bp := build(s, ps)
		probs = append(append(probs, pp...), bp...)
		rules, from = append(rules, rs...), append(from, fr...)
	}
	if len(probs) > 0 {
		return nil, nil, sorted(probs), nil
	}
	place := func(ep hooks.Problem, level string) Problem {
		if ep.Rule < 0 || ep.Rule >= len(from) {
			return Problem{Message: ep.Message, Level: level}
		}
		o := from[ep.Rule]
		line, ok := o.lines[ep.Field]
		if !ok {
			line = o.when
		}
		msg := tidy(ep.Message)
		if ep.Field != "" && ep.Field != "when" && ok && level == "" {
			msg = ep.Field + ": " + msg
		}
		return Problem{Hook: o.hook, Line: line, Message: msg, Level: level}
	}
	set, eps := hooks.Compile(rules)
	if eps != nil {
		for _, ep := range eps {
			probs = append(probs, place(ep, ""))
		}
		return nil, nil, sorted(probs), nil
	}
	for _, n := range set.Notes {
		notes = append(notes, place(n, "warning"))
	}
	return rules, from, nil, notes
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
	for i, s := range all {
		order[s.Name] = i
	}
	sort.SliceStable(ps, func(a, b int) bool {
		if ps[a].Hook != ps[b].Hook {
			return order[ps[a].Hook] < order[ps[b].Hook]
		}
		return ps[a].Line < ps[b].Line
	})
	return ps
}

package hooks

import (
	"fmt"
	"sort"
	"strings"
)

// The gateway sits between clients and servers it does not control, and both
// send things it has never seen: a new content block type, a notification
// from an extension, a result type, a template form. The gateway is not
// opinionated about them: it passes on what is safe to pass and drops what
// the protocol says to drop, exactly as before.
//
// What it adds is a record. Every unknown thing is reported with its shape,
// so someone can look at what real servers send and decide whether the
// engine should learn it. That loop (see it, log it, investigate, teach the
// engine, add a test) is how the engine keeps up with servers in the wild.
//
// A record carries names and types, never values. That alone does not make
// it free of customer data, because a server can use data as a name: a map
// keyed by email address, a notification method with an id in it, a URI
// with a token in its query. So names are filtered too. A field name is
// shown only when it looks like a field name; any other is withheld and
// counted. A type or method name that does not look like one is replaced by
// its length. A URI target loses its query and fragment. What is left is
// meant to be safe for a general log, but it is still text a server chose:
// treat it as low-sensitivity, not as public.

// Unknown kinds.
const (
	UnknownContentBlock = "content_block" // a content block type in a tool result or prompt message
	UnknownNotification = "notification"  // a notification on a reply stream that is not relayed
	UnknownResultType   = "result_type"   // a resultType the engine does not handle
	UnknownInputRequest = "input_request" // an input request that is not an elicitation
	UnknownTemplateForm = "template_form" // a URI template expression the gateway cannot parse
)

// Unknown is one thing the engine did not recognise.
type Unknown struct {
	Kind   string `json:"kind"`             // one of the Unknown* kinds
	Name   string `json:"name"`             // the type, method or expression, as sent; "" when it had none
	Server string `json:"server,omitempty"` // the slug it came from
	Target string `json:"target,omitempty"` // the tool, prompt, URI or template involved
	// Shape maps each top-level field to its JSON type, such as "string",
	// "number", "object{3}" or "array[2]". It never holds a value. A field
	// whose name does not look like a field name is not shown: such fields
	// are counted under "(other fields)" with their types.
	Shape  map[string]string `json:"shape,omitempty"`
	Count  int               `json:"count"`  // how many times in this request
	Action string            `json:"action"` // what the gateway did: passed, dropped or refused
}

// Key identifies an unknown for aggregation: the same thing from the same
// server has the same key, whatever the request.
func (u Unknown) Key() string { return u.Server + "\x00" + u.Kind + "\x00" + u.Name }

// shapeOf describes a JSON value's top-level fields by type, never by
// value, and never by a name that might itself be data.
func shapeOf(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return map[string]string{"(value)": jsonType(v)}
	}
	out := map[string]string{}
	withheld := map[string]bool{}
	others := 0
	for _, k := range sortedKeys(m) {
		if !fieldName(k) || len(out) >= 32 { // a shape is a sketch, not a copy
			others++
			withheld[typeFamily(m[k])] = true
			continue
		}
		out[k] = jsonType(m[k])
	}
	if others > 0 {
		out["(other fields)"] = fmt.Sprintf("%d: %s", others, strings.Join(sortedKeys(withheld), ", "))
	}
	return out
}

// fieldName reports whether a key looks like a field name and not like
// data: short, starting with a letter or underscore, made of letters,
// digits, underscores and hyphens, with few digits. An email address, a
// path, a UUID or a long identifier does not pass.
func fieldName(k string) bool {
	if len(k) == 0 || len(k) > 32 {
		return false
	}
	digits := 0
	for i, c := range k {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_', c == '$' && i == 0:
		case c >= '0' && c <= '9':
			if i == 0 {
				return false
			}
			digits++
		case c == '-' && i > 0:
		default:
			return false
		}
	}
	return digits <= 3
}

// safeName keeps a type or method name a server sent only when it looks
// like one: letters, digits and the separators such names use, and not
// long. Anything else is described by its length alone.
func safeName(name string) string {
	if len(name) > 64 {
		return fmt.Sprintf("(unusual name, %d characters)", len(name))
	}
	digits := 0
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_', c == '-', c == '.', c == '/':
		case c >= '0' && c <= '9':
			digits++
		default:
			return fmt.Sprintf("(unusual name, %d characters)", len(name))
		}
	}
	if digits > 6 {
		return fmt.Sprintf("(unusual name, %d characters)", len(name))
	}
	return name
}

// safeTarget bounds a target and removes a URI's query and fragment, which
// are where a URI carries data.
func safeTarget(target string) string {
	if i := strings.IndexAny(target, "?#"); i >= 0 {
		target = target[:i]
	}
	if len(target) > 256 {
		target = target[:256] + "…"
	}
	return target
}

// templateForm reduces a template expression to its operator and
// modifiers, such as "{/…*}" or "{?…}". The variable names are left out:
// they are server text and add nothing to the investigation.
func templateForm(expr string) string {
	i := strings.IndexByte(expr, '{')
	j := strings.LastIndexByte(expr, '}')
	if i < 0 || j <= i+1 {
		return "{…}"
	}
	body := expr[i+1 : j]
	op := ""
	if strings.ContainsRune("+#./;?&=,!@|", rune(body[0])) {
		op = body[:1]
	}
	mod := ""
	if strings.Contains(body, "*") {
		mod += "*"
	}
	if strings.Contains(body, ":") {
		mod += ":n"
	}
	return "{" + op + "…" + mod + "}"
}

// typeFamily is a JSON type without its size.
func typeFamily(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	}
	return "number"
}

func jsonType(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case map[string]any:
		return fmt.Sprintf("object{%d}", len(t))
	case []any:
		return fmt.Sprintf("array[%d]", len(t))
	}
	return "number"
}

// unknowns collects records within one request, merging repeats.
type unknowns struct {
	byKey map[string]*Unknown
	order []string
}

func (u *unknowns) add(kind, name, server, target, action string, sample any) {
	if u.byKey == nil {
		u.byKey = map[string]*Unknown{}
	}
	if kind == UnknownTemplateForm {
		name = templateForm(name)
	} else {
		name = safeName(name)
	}
	target = safeTarget(target)
	key := server + "\x00" + kind + "\x00" + name + "\x00" + target
	if got, ok := u.byKey[key]; ok {
		got.Count++
		return
	}
	rec := &Unknown{Kind: kind, Name: name, Server: server, Target: target, Count: 1, Action: action}
	if sample != nil {
		rec.Shape = shapeOf(sample)
	}
	u.byKey[key] = rec
	u.order = append(u.order, key)
}

func (u *unknowns) list() []Unknown {
	if len(u.order) == 0 {
		return nil
	}
	keys := append([]string(nil), u.order...)
	sort.Strings(keys)
	out := make([]Unknown, 0, len(keys))
	for _, k := range keys {
		out = append(out, *u.byKey[k])
	}
	return out
}

// knownContentBlocks are the content block types of the front door's
// protocol version.
var knownContentBlocks = map[string]bool{"text": true, "image": true, "audio": true, "resource_link": true, "resource": true}

// contentUnknowns reports content blocks of a type the protocol version
// does not define. They are passed on unchanged: content belongs to the
// server, and a client that cannot read a block can ignore it.
func contentUnknowns(u *unknowns, server, target string, blocks any) {
	list, _ := blocks.([]any)
	for _, b := range list {
		m, ok := b.(map[string]any)
		if !ok {
			u.add(UnknownContentBlock, "", server, target, "passed", b)
			continue
		}
		typ, _ := m["type"].(string)
		if !knownContentBlocks[typ] {
			u.add(UnknownContentBlock, typ, server, target, "passed", m)
		}
	}
}

// reportUnknowns hands each record to the gateway's callback, if set. A
// callback that panics is ignored.
func (e *Engine) reportUnknowns(list []Unknown) {
	if e == nil || e.OnUnknown == nil || len(list) == 0 {
		return
	}
	defer func() { _ = recover() }()
	for _, u := range list {
		e.OnUnknown(u)
	}
}

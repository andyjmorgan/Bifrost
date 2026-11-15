package hooks

import (
	"fmt"
	"sort"
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
// A record never carries values. It carries names and types only, so it is
// safe to log and to aggregate.

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
	// "number", "object{3}" or "array[2]". It never holds a value.
	Shape  map[string]string `json:"shape,omitempty"`
	Count  int               `json:"count"`  // how many times in this request
	Action string            `json:"action"` // what the gateway did: passed, dropped or refused
}

// Key identifies an unknown for aggregation: the same thing from the same
// server has the same key, whatever the request.
func (u Unknown) Key() string { return u.Server + "\x00" + u.Kind + "\x00" + u.Name }

// shapeOf describes a JSON value's top-level fields by type, never by value.
func shapeOf(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return map[string]string{"(value)": jsonType(v)}
	}
	out := make(map[string]string, len(m))
	for k, x := range m {
		if len(out) >= 32 { // a shape is a sketch, not a copy
			out["(more)"] = fmt.Sprintf("%d fields", len(m)-32)
			break
		}
		out[k] = jsonType(x)
	}
	return out
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

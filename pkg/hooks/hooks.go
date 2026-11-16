// Package hooks is the Bifrost rules engine: CEL rules attached to MCP
// surfaces ("hooks"), evaluated over upstream catalogs and requests. It is a
// standalone library: it knows nothing about Bifrost's storage, auth or
// transport. A caller hands it rules, upstream data and request context, and
// gets back what the client would see and what would be forwarded upstream.
//
// The design is "v3: Hooks" in the MCP Gateway policy engine note; that
// document, not this code, is the source of truth.
package hooks

import "strings"

// ItemKind is the kind of item a hook works on.
type ItemKind string

// Item kinds.
const (
	KindTool     ItemKind = "tool"
	KindPrompt   ItemKind = "prompt"
	KindResource ItemKind = "resource"
	KindTemplate ItemKind = "template"
	KindSkill    ItemKind = "skill"
	KindServer   ItemKind = "server"
	KindNone     ItemKind = ""
)

// Phase is what a hook does.
type Phase string

// Phases.
const (
	PhaseList   Phase = "list"   // hides an item or changes it, rule by rule in written order
	PhaseFilter Phase = "filter" // decides visibility only; deny is final
	PhaseModify Phase = "modify" // changes fields; no decisions
	PhaseCall   Phase = "call"   // decides, elicits, patches arguments
	PhaseElicit Phase = "elicit" // shapes a question the upstream asks the user
	PhaseResult Phase = "result" // shapes an upstream result
	PhaseEvent  Phase = "event"  // drops or reshapes one event on a reply stream
	PhaseServer Phase = "server" // server-level settings
)

// ValueType is the type a `then` field's expression must produce.
type ValueType int

// Value types.
const (
	TAny ValueType = iota
	TText
	TBool
	TNumber
	TList
	TMap
	TDuration
	TDecision // the literal "deny"
	TResult   // a full result, built with elicit()
)

func (t ValueType) String() string {
	return [...]string{"any", "string", "bool", "number", "list", "map", "duration", "decision", "result"}[t]
}

// Field is one settable field. Pattern segments are separated by "." and
// "*" matches exactly one segment, such as a parameter name.
type Field struct {
	Pattern string
	Type    ValueType
}

// HookSpec describes a hook: what it reads and what it may set.
type HookSpec struct {
	Name    string
	Item    ItemKind
	Phase   Phase
	Vars    []string // variables beyond the common ones and the item's fields
	Fields  []Field
	Helpers []string
}

// CommonVars are visible in every hook. None of them is writable.
var CommonVars = []string{"server", "entitlement", "caller", "client", "route", "flags", "now"}

// DynamicVars make a rule depend on the request rather than the catalog.
var DynamicVars = map[string]bool{"caller": true, "client": true, "route": true, "flags": true, "now": true, "args": true, "input": true, "result": true, "request": true, "progress": true}

// ItemFields are the top-level variables for each item kind. Every item hook
// also gets `upstream`, the item exactly as the upstream sent it.
var ItemFields = map[ItemKind][]string{
	KindTool:     {"name", "title", "description", "annotations", "annotationsRaw", "meta", "inputSchema", "outputSchema", "params", "icons"},
	KindPrompt:   {"name", "title", "description", "arguments", "meta", "icons"},
	KindResource: {"uri", "name", "title", "description", "mimeType", "annotations", "meta", "size", "icons"},
	KindTemplate: {"uriTemplate", "name", "title", "description", "mimeType", "annotations", "meta", "icons"},
	KindSkill:    {"uri", "frontmatter", "resources", "name", "description"},
	KindServer:   {"title", "description", "instructions"},
}

var (
	filterFields = []Field{{"decision", TDecision}, {"reason", TText}}
	callFields   = []Field{{"decision", TDecision}, {"reason", TText}, {"result", TResult}, {"args.*", TAny}, {"timeout", TDuration}, {"limit", TMap}}
	elicitFields = []Field{{"decision", TDecision}, {"reason", TText}, {"message", TText}}
	eventFields  = []Field{{"decision", TDecision}, {"reason", TText}, {"message", TText}} // deny drops the event; message reshapes it
	textModify   = []Field{{"title", TText}, {"description", TText}, {"annotations", TMap}, {"annotations.*", TAny}, {"meta.*", TAny}}
)

// Hooks is the registry. A rule naming any other hook fails validation.
//
// A hook is named after where it runs: the method, then pre (the request) or
// post (the reply), then, where a reply can be several things, which one
// came back (progress, elicit or result). The two server hooks that belong
// to no method are server.settings and server.session.
var Hooks = map[string]*HookSpec{
	"server.session": {Name: "server.session", Item: KindNone, Phase: PhaseServer,
		Fields: []Field{{"clientInfo", TMap}, {"capabilities.elicitation", TBool}, {"meta", TBool}}},
	"server.settings": {Name: "server.settings", Item: KindNone, Phase: PhaseServer,
		Fields: []Field{{"tools", TBool}, {"prompts", TBool}, {"resources", TBool}, {"tasks", TBool}, {"instructions", TBool}, {"serverInfo", TBool}, {"searchRoster", TBool}, {"progress", TBool}}},
	"server/discover.post": {Name: "server/discover.post", Item: KindServer, Phase: PhaseModify,
		Fields: []Field{{"title", TText}, {"description", TText}, {"instructions", TText}}},

	"tools/list.post": {Name: "tools/list.post", Item: KindTool, Phase: PhaseList, Helpers: []string{"dropSchema"},
		Fields: listFields(Field{"name", TText}, Field{"title", TText}, Field{"description", TText}, Field{"annotations.*", TAny}, Field{"meta.*", TAny},
			Field{"params.*.name", TText}, Field{"params.*.description", TText}, Field{"params.*.hide", TBool}, Field{"params.*.pin", TAny},
			Field{"params.*.default", TAny}, Field{"params.*.enum", TList}, Field{"params.*.minimum", TNumber}, Field{"params.*.maximum", TNumber},
			Field{"params.*.add", TMap}, Field{"outputSchema", TAny})},
	"tools/call.pre":         {Name: "tools/call.pre", Item: KindTool, Phase: PhaseCall, Vars: []string{"args", "input"}, Fields: callFields, Helpers: []string{"elicit"}},
	"tools/call.post.elicit": {Name: "tools/call.post.elicit", Item: KindTool, Phase: PhaseElicit, Vars: []string{"args", "request"}, Fields: elicitFields},
	"tools/call.post.result": {Name: "tools/call.post.result", Item: KindTool, Phase: PhaseResult, Vars: []string{"args", "result"}, Helpers: []string{"drop"},
		Fields: []Field{{"decision", TDecision}, {"reason", TText}, {"content", TList}, {"structuredContent", TAny}}},
	"tools/call.post.progress": {Name: "tools/call.post.progress", Item: KindTool, Phase: PhaseEvent, Vars: []string{"args", "progress"}, Fields: eventFields},

	"prompts/list.post": {Name: "prompts/list.post", Item: KindPrompt, Phase: PhaseList,
		Fields: listFields(Field{"name", TText}, Field{"title", TText}, Field{"description", TText}, Field{"arguments.*.name", TText}, Field{"arguments.*.description", TText}, Field{"meta.*", TAny})},
	"prompts/get.pre":           {Name: "prompts/get.pre", Item: KindPrompt, Phase: PhaseCall, Vars: []string{"args", "input"}, Fields: []Field{{"decision", TDecision}, {"reason", TText}, {"result", TResult}, {"args.*", TAny}}, Helpers: []string{"elicit"}},
	"prompts/get.post.elicit":   {Name: "prompts/get.post.elicit", Item: KindPrompt, Phase: PhaseElicit, Vars: []string{"args", "request"}, Fields: elicitFields},
	"prompts/get.post.progress": {Name: "prompts/get.post.progress", Item: KindPrompt, Phase: PhaseEvent, Vars: []string{"args", "progress"}, Fields: eventFields},

	"resources/list.post":           {Name: "resources/list.post", Item: KindResource, Phase: PhaseList, Fields: listFields(textModify...)},
	"resources/templates/list.post": {Name: "resources/templates/list.post", Item: KindTemplate, Phase: PhaseList, Fields: listFields(textModify...)},
	"resources/read.pre":            {Name: "resources/read.pre", Item: KindResource, Phase: PhaseCall, Vars: []string{"args", "input"}, Fields: []Field{{"decision", TDecision}, {"reason", TText}, {"result", TResult}}, Helpers: []string{"elicit"}},
	"resources/read.post.elicit":    {Name: "resources/read.post.elicit", Item: KindResource, Phase: PhaseElicit, Vars: []string{"args", "request"}, Fields: elicitFields},
	"resources/read.post.result": {Name: "resources/read.post.result", Item: KindResource, Phase: PhaseResult, Vars: []string{"args", "result"}, Helpers: []string{"slugUris"},
		Fields: []Field{{"decision", TDecision}, {"reason", TText}, {"text", TText}}},
	"resources/read.post.progress": {Name: "resources/read.post.progress", Item: KindResource, Phase: PhaseEvent, Vars: []string{"args", "progress"}, Fields: eventFields},

	"skills/list.post": {Name: "skills/list.post", Item: KindSkill, Phase: PhaseFilter, Fields: filterFields},
}

// listFields are what a list hook may set: a deny, and the item's fields.
func listFields(change ...Field) []Field {
	return append(append([]Field{}, filterFields...), change...)
}

// vars lists every variable a hook's rules may read.
func (h *HookSpec) vars() []string {
	out := append([]string{}, CommonVars...)
	if fs, ok := ItemFields[h.Item]; ok {
		out = append(out, fs...)
		out = append(out, "upstream")
	}
	return append(out, h.Vars...)
}

// field finds the spec for a `then` key, or nil.
func (h *HookSpec) field(key string) *Field {
	parts := splitPath(key)
	for i := range h.Fields {
		pp := strings.Split(h.Fields[i].Pattern, ".")
		if len(pp) != len(parts) {
			continue
		}
		ok := true
		for j := range pp {
			if parts[j] == "" || (pp[j] != "*" && pp[j] != parts[j]) {
				ok = false
				break
			}
		}
		if ok {
			return &h.Fields[i]
		}
	}
	return nil
}

func (h *HookSpec) hasHelper(name string) bool {
	for _, x := range h.Helpers {
		if x == name {
			return true
		}
	}
	return false
}

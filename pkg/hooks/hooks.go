// Package hooks is the Bifrost rules engine: CEL rules attached to MCP
// surfaces ("hooks"), evaluated over upstream catalogs and requests. It is a
// standalone library: it knows nothing about Bifrost's storage, auth or
// transport. A caller hands it rules, upstream data and request context, and
// gets back what the client would see and what would be forwarded upstream.
//
// The design is "v3: Hooks" in the MCP Gateway policy engine note; that
// document, not this code, is the source of truth.
package hooks

import (
	"strings"
	"sync"
)

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

// Field is one path a rule may set. Pattern segments are separated by "."
// and "*" stands for any one name, such as a parameter's.
type Field struct {
	Pattern string
	Type    ValueType
}

// HookSpec describes a hook. What its rules may read and set is its tree:
// see Tree.
type HookSpec struct {
	Name    string
	Item    ItemKind
	Phase   Phase
	Helpers []string
}

// The keys of a rule's `then` that are not paths in the tree: the two that
// make a deny, and the one that asks the user to confirm.
const (
	KeyDecision = "decision"
	KeyReason   = "reason"
	KeyConfirm  = "confirm"
)

// Hooks is the registry. A rule naming any other hook fails validation.
//
// A hook is named after where it runs: the method, then pre (the request) or
// post (the reply), then, where a reply can be several things, which one
// came back (progress, elicit or result). The two server hooks that belong
// to no method are server.settings and server.session.
var Hooks = map[string]*HookSpec{
	"server.session":       {Name: "server.session", Item: KindNone, Phase: PhaseServer},
	"server.settings":      {Name: "server.settings", Item: KindNone, Phase: PhaseServer},
	"server/discover.post": {Name: "server/discover.post", Item: KindServer, Phase: PhaseModify},

	"tools/list.post":          {Name: "tools/list.post", Item: KindTool, Phase: PhaseList, Helpers: []string{"dropSchema"}},
	"tools/call.pre":           {Name: "tools/call.pre", Item: KindTool, Phase: PhaseCall, Helpers: []string{"elicit"}},
	"tools/call.post.elicit":   {Name: "tools/call.post.elicit", Item: KindTool, Phase: PhaseElicit},
	"tools/call.post.result":   {Name: "tools/call.post.result", Item: KindTool, Phase: PhaseResult, Helpers: []string{"drop"}},
	"tools/call.post.progress": {Name: "tools/call.post.progress", Item: KindTool, Phase: PhaseEvent},

	"prompts/list.post":         {Name: "prompts/list.post", Item: KindPrompt, Phase: PhaseList},
	"prompts/get.pre":           {Name: "prompts/get.pre", Item: KindPrompt, Phase: PhaseCall, Helpers: []string{"elicit"}},
	"prompts/get.post.elicit":   {Name: "prompts/get.post.elicit", Item: KindPrompt, Phase: PhaseElicit},
	"prompts/get.post.progress": {Name: "prompts/get.post.progress", Item: KindPrompt, Phase: PhaseEvent},

	"resources/list.post":           {Name: "resources/list.post", Item: KindResource, Phase: PhaseList},
	"resources/templates/list.post": {Name: "resources/templates/list.post", Item: KindTemplate, Phase: PhaseList},
	"resources/read.pre":            {Name: "resources/read.pre", Item: KindResource, Phase: PhaseCall, Helpers: []string{"elicit"}},
	"resources/read.post.elicit":    {Name: "resources/read.post.elicit", Item: KindResource, Phase: PhaseElicit},
	"resources/read.post.result":    {Name: "resources/read.post.result", Item: KindResource, Phase: PhaseResult, Helpers: []string{"slugUris"}},
	"resources/read.post.progress":  {Name: "resources/read.post.progress", Item: KindResource, Phase: PhaseEvent},

	"skills/list.post": {Name: "skills/list.post", Item: KindSkill, Phase: PhaseFilter},
}

var (
	treeOnce sync.Once
	trees    map[string]*Node
)

func buildTrees() {
	trees = make(map[string]*Node, len(Hooks))
	for name, h := range Hooks {
		trees[name] = buildTree(h)
	}
}

// Denies reports whether a rule on this hook may deny.
func (h *HookSpec) Denies() bool { return h.Phase != PhaseModify && h.Phase != PhaseServer }

// Confirms reports whether a rule on this hook may ask the user to confirm.
func (h *HookSpec) Confirms() bool { return h.Phase == PhaseCall }

// field finds what a `then` key sets on this hook, or nil: decision, reason
// and confirm where the hook takes them, and any writable path of the tree.
func (h *HookSpec) field(key string) *Field {
	switch key {
	case KeyDecision:
		if h.Denies() {
			return &Field{Pattern: key, Type: TDecision}
		}
		return nil
	case KeyReason:
		if h.Denies() {
			return &Field{Pattern: key, Type: TText}
		}
		return nil
	case KeyConfirm:
		if h.Confirms() {
			return &Field{Pattern: key, Type: TResult}
		}
		return nil
	}
	parts := splitPath(key)
	for _, p := range parts {
		if p == "" || p == "*" {
			return nil
		}
	}
	// The key must start with the hook's branch exactly as it is written. A
	// backslash escapes a dot inside a name; an escape in the branch itself
	// would name the same node and then not be found by the setter.
	if len(parts) > 1 && !strings.HasPrefix(key, h.writeRoot()) {
		return nil
	}
	if n := h.Tree().writablePath(parts); n != nil {
		return &Field{Pattern: key, Type: valueType(n.Type)}
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

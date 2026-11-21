// Package hooks is a rules engine for an MCP gateway. A server's rules are
// CEL expressions that run at fixed points in a request, called hooks:
// when tools are listed, before a call is forwarded, when a result comes
// back, and so on. A rule can hide an item, refuse a request, ask the user to
// confirm, or change what its hook lets it change.
//
// The engine does no I/O and knows nothing of a gateway's storage, auth or
// transport. A gateway hands it a Request, the servers on the route with
// their catalogs and compiled rules, and the client's request. The Outcome
// says what to return to the client, or what to forward to the server. The
// gateway forwards it and hands the reply back to the matching Upstream…Reply
// or …ReplyStream method.
//
// A gateway must supply a StateStore shared by all its instances, a
// generation for each server that changes when its rules, selection or
// catalog do, and a principal for each request. docs/rules/handover.md says what
// else, and docs/rules/hooks describes the rule language.
package hooks

import (
	"sort"
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
	PhaseTask   Phase = "task"   // shapes or ends a task's status
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
	// Keys are the only keys a map set on this path may have, when the
	// path is a map with a fixed set of them, such as annotations.
	Keys []string
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
// came back (progress, elicit, result or task). The two server hooks that belong
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
	"tools/call.post.task":     {Name: "tools/call.post.task", Item: KindTool, Phase: PhaseTask},

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
	// The key must be the path exactly as it is written. A backslash is
	// there to escape a dot or a backslash inside a name, and for nothing
	// else: "conten\t" would name the same node as "content" here and then
	// not be found by the setter.
	if joinPath(parts) != key {
		return nil
	}
	if n := h.Tree().writablePath(parts); n != nil {
		f := &Field{Pattern: key, Type: valueType(n.Type)}
		if n.Type == "map" && !n.Open && n.Follows == "" {
			for k := range n.Keys {
				f.Keys = append(f.Keys, k)
			}
			sort.Strings(f.Keys)
		}
		return f
	}
	return nil
}

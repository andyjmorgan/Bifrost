package hooks

import "sort"

// The schema of a hook is its tree: what a rule there may read and set. It
// is built from the registry, so it cannot drift from what the engine
// compiles. `hookctl hooks` prints it; editors and docs read it.

// SetSchema is one path a hook's rules may set.
type SetSchema struct {
	Path string `json:"path"` // "*" stands for any one name
	Type string `json:"type"`
}

// HookSchema is everything a rule on one hook may read and set.
type HookSchema struct {
	Hook  string `json:"hook"`
	Item  string `json:"item,omitempty"` // tool, prompt, resource, template, skill or server
	Phase string `json:"phase"`
	// Tree is what a rule reads: the branches of the hook, to any depth.
	Tree *Node `json:"tree"`
	// Sets lists the paths a rule may set, for convenience: they are the
	// tree's writable nodes.
	Sets []SetSchema `json:"sets"`
	// Denies and Confirms say whether a rule here may deny, and may ask the
	// user to confirm.
	Denies   bool     `json:"denies,omitempty"`
	Confirms bool     `json:"confirms,omitempty"`
	Helpers  []string `json:"helpers,omitempty"`
}

// FlagDocs describes each flag. Every name in Flags has an entry.
var FlagDocs = map[string]string{
	FlagSearch: "The request is in search mode: the gateway offers its search tools in place of the full listing. Set with ?search=true, on either route.",
	FlagCLI:    "The request comes from the command line client, through the gateway's API. The gateway sets it, and a client cannot: the request does not come in through MCP, but the call sent upstream is the same. Such a request cannot be asked for confirmation.",
}

// Schema returns the schema of every hook, ordered by hook name.
func Schema() []HookSchema {
	names := make([]string, 0, len(Hooks))
	for n := range Hooks {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]HookSchema, 0, len(names))
	for _, n := range names {
		out = append(out, Hooks[n].Schema())
	}
	return out
}

// Schema returns what a rule on this hook may read and set.
func (h *HookSpec) Schema() HookSchema {
	s := HookSchema{Hook: h.Name, Item: string(h.Item), Phase: string(h.Phase), Tree: h.Tree(),
		Denies: h.Denies(), Confirms: h.Confirms(), Helpers: append([]string(nil), h.Helpers...), Sets: []SetSchema{}}
	for _, f := range h.Sets() {
		s.Sets = append(s.Sets, SetSchema{Path: f.Pattern, Type: f.Type.String()})
	}
	return s
}

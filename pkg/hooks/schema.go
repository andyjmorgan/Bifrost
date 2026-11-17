package hooks

import "sort"

// The schema of a hook is what its rules may read and what they may set. It
// is generated from the registry, so it cannot drift from what the engine
// compiles. `hookctl hooks` prints it; editors and docs can read it.

// VarSchema describes one variable a hook's rules can read.
type VarSchema struct {
	Name string `json:"name"`
	// Type is string, bool, number, timestamp, list, map, struct or any.
	Type string `json:"type"`
	// Values lists the values of an enumerated string.
	Values []string `json:"values,omitempty"`
	// Fields lists the fields of a struct, each with its type. A struct's
	// fields are fixed: reading any other is refused when the rule is saved.
	Fields map[string]string `json:"fields,omitempty"`
	// Keys lists the keys a map is known to hold, each with its type. It is
	// a hint for editors: a map may hold other keys, and reading one that is
	// not listed is not refused.
	Keys map[string]string `json:"keys,omitempty"`
	Doc  string            `json:"doc"`
}

// SetSchema describes one field a hook's rules can set.
type SetSchema struct {
	Field string `json:"field"` // a path; "*" stands for any one name
	Type  string `json:"type"`
}

// HookSchema is everything a rule on one hook may read and set.
type HookSchema struct {
	Hook      string      `json:"hook"`
	Item      string      `json:"item,omitempty"` // tool, prompt, resource, template, skill or server
	Phase     string      `json:"phase"`
	Variables []VarSchema `json:"variables"`
	Sets      []SetSchema `json:"sets"`
	Helpers   []string    `json:"helpers,omitempty"`
}

// FlagDocs describes each flag. Every name in Flags has an entry.
var FlagDocs = map[string]string{
	FlagSearch: "The request is in search mode: the gateway offers its search tools in place of the full listing. Set with ?search=true, on either route.",
	FlagCLI:    "The request comes from the command line client, through the gateway's API. The gateway sets it, and a client cannot: the request does not come in through MCP, but the call sent upstream is the same. Such a request cannot be asked for confirmation.",
}

// flagsSchema is the `flags` variable: a struct with one bool per flag.
func flagsSchema() VarSchema {
	fields := make(map[string]string, len(Flags))
	for _, f := range Flags {
		fields[f] = "bool"
	}
	return VarSchema{Name: "flags", Type: "struct", Fields: fields,
		Doc: "The request's switches, read by name as flags.<name>. Every flag is always present, false when the request did not set it. A flag can be on for either route."}
}

var commonSchema = map[string]VarSchema{
	"server": {Type: "map", Doc: "The upstream server: slug, name, title, description, instructions, protocolVersion, url.",
		Keys: map[string]string{"slug": "string", "name": "string", "title": "string", "description": "string", "instructions": "string", "protocolVersion": "string", "url": "string"}},
	"entitlement": {Type: "map", Doc: "The entitlement serving the request: id, slug, name.",
		Keys: map[string]string{"id": "string", "slug": "string", "name": "string"}},
	"caller": {Type: "map", Doc: "The caller's identity, as the gateway supplies it. Opaque to the engine."},
	"client": {Type: "map", Doc: "The client, from this request: name, version, capabilities. capabilities is always present.",
		Keys: map[string]string{"name": "string", "version": "string", "capabilities": "map"}},
	"route": {Type: "string", Values: []string{"direct", "mux"}, Doc: "How the client reached the gateway. The two values are exclusive."},
	"now":   {Type: "timestamp", Doc: "The time of the request."},
}

var hookVarSchema = map[string]VarSchema{
	"args":   {Type: "map", Doc: "The request's arguments."},
	"input":  {Type: "map", Doc: "Confirmed answers to the gateway's own confirmations, by key."},
	"result": {Type: "map", Doc: "The upstream's result."},
	"request": {Type: "map", Doc: "One input request from the upstream: key, method and its params.",
		Keys: map[string]string{"key": "string", "method": "string", "mode": "string", "message": "string", "requestedSchema": "map", "url": "string", "elicitationId": "string"}},
	"progress": {Type: "struct", Fields: map[string]string{"progress": "number", "total": "number", "message": "string"}, Doc: "One progress event. total is present only when the upstream sent it."},
	"upstream": {Type: "map", Doc: "The item exactly as the upstream sent it. Never changes."},
}

var itemFieldTypes = map[string]string{
	"name": "string", "title": "string", "description": "string", "instructions": "string", "uri": "string", "uriTemplate": "string", "mimeType": "string",
	"annotations": "map", "annotationsRaw": "map", "meta": "map", "params": "map", "frontmatter": "map",
	"inputSchema": "any", "outputSchema": "any", "resources": "any", "size": "number", "icons": "list", "arguments": "list",
}

// resultKeys are the keys of `result`, by the kind of item the hook works on.
var resultKeys = map[ItemKind]map[string]string{
	KindTool:     {"isError": "bool", "content": "list", "structuredContent": "any", "meta": "map", "text": "string"},
	KindResource: {"text": "string", "contents": "list"},
}

// notUpstream are item fields `upstream` does not carry: params is the
// gateway's view of the parameters, built from inputSchema.
var notUpstream = map[string]bool{"params": true}

// upstreamKeys are the keys of `upstream` for an item kind: the item's
// fields as the upstream sends them.
func upstreamKeys(kind ItemKind) map[string]string {
	out := map[string]string{}
	for _, f := range ItemFields[kind] {
		if notUpstream[f] {
			continue
		}
		t := itemFieldTypes[f]
		if t == "" {
			t = "any"
		}
		out[f] = t
	}
	return out
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
	s := HookSchema{Hook: h.Name, Item: string(h.Item), Phase: string(h.Phase), Helpers: append([]string(nil), h.Helpers...)}
	for _, v := range h.vars() {
		switch {
		case v == "flags":
			s.Variables = append(s.Variables, flagsSchema())
		case commonSchema[v].Type != "":
			c := commonSchema[v]
			c.Name = v
			s.Variables = append(s.Variables, c)
		case hookVarSchema[v].Type != "":
			c := hookVarSchema[v]
			c.Name = v
			switch v {
			case "result":
				c.Keys = resultKeys[h.Item]
			case "upstream":
				c.Keys = upstreamKeys(h.Item)
			}
			s.Variables = append(s.Variables, c)
		default:
			t := itemFieldTypes[v]
			if t == "" {
				t = "any"
			}
			s.Variables = append(s.Variables, VarSchema{Name: v, Type: t, Doc: "A field of the " + string(h.Item) + "."})
		}
	}
	for _, f := range h.Fields {
		s.Sets = append(s.Sets, SetSchema{Field: f.Pattern, Type: f.Type.String()})
	}
	return s
}

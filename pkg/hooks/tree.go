package hooks

import (
	"sort"
	"strings"
)

// What a rule can see on a hook is one tree. Its branches are the item the
// hook is about (tool, prompt, resource, template, skill), the request, what
// came back (response, progress, elicitation), and where it happened
// (server). Hooks differ only in which branches exist and which
// one branch a rule may change.
//
// The names in the tree are MCP's wherever MCP has one, spelled as MCP spells
// them: arguments, _meta, clientInfo, inputSchema, structuredContent. A
// branch that is a message looks like the message on the wire: a request is
// method and params, a response is result. The gateway's own additions
// (caller, headers, upstream, params) sit beside MCP's part, never inside it.
//
// Nothing in the tree is filled in. An optional field the server left out is
// absent, and a rule reads it with CEL's optional form: tool.?title.
//
// X.upstream is X's canonical form, in the server's own terms: the tool as
// the server declares it, the request as the server receives it, the
// response as the server produced it. Nothing can set it.
//
// The tree is also data: Node serialises to JSON, and JSONSchema turns a
// hook's tree into the schema of the document a rule reads.

// Node is one place in a hook's tree.
type Node struct {
	// Type is string, bool, number, timestamp, duration, list, map or any.
	Type string `json:"type"`
	Doc  string `json:"doc,omitempty"`
	// Optional marks a value that may be absent. Read it with .? or has().
	Optional bool `json:"optional,omitempty"`
	// Writable marks a value a rule on this hook may set.
	Writable bool `json:"writable,omitempty"`
	// WriteOnly marks a value a rule sets but cannot read: a setting the
	// gateway takes from the rule.
	WriteOnly bool `json:"writeOnly,omitempty"`
	// Enum lists the values of a string that has a fixed set.
	Enum []string `json:"enum,omitempty"`
	// Keys are the keys a map holds.
	Keys map[string]*Node `json:"keys,omitempty"`
	// Open marks a map that may hold keys beyond Keys. Values is the form of
	// each of those.
	Open   bool  `json:"open,omitempty"`
	Values *Node `json:"values,omitempty"`
	// Names says the open map's keys are names from the server's catalog:
	// "parameters" (a tool's parameters or a prompt's arguments), "output"
	// (the properties of a tool's output schema) or "meta" (the keys seen in
	// the item's _meta). It is a hint for editors.
	Names string `json:"names,omitempty"`
	// Follows says the value takes its form from one of the item's own JSON
	// Schemas: "inputSchema" for arguments, "outputSchema" for structured
	// content.
	Follows string `json:"follows,omitempty"`
	// Items is the form of each element of a list.
	Items *Node `json:"items,omitempty"`
}

// ---- building blocks ------------------------------------------------------------------

func leaf(t string) *Node                { return &Node{Type: t} }
func opt(n *Node) *Node                  { c := *n; c.Optional = true; return &c }
func doc(n *Node, d string) *Node        { c := *n; c.Doc = d; return &c }
func object(keys map[string]*Node) *Node { return &Node{Type: "map", Keys: keys} }
func listOf(items *Node) *Node           { return &Node{Type: "list", Items: items} }
func openMap(values *Node) *Node         { return &Node{Type: "map", Open: true, Values: values} }
func enum(values ...string) *Node        { return &Node{Type: "string", Enum: values} }

// writable returns a copy of n that a rule may set.
func writable(n *Node) *Node { c := *n; c.Writable = true; return &c }

// setting is a value a rule sets and cannot read.
func setting(t string) *Node { return &Node{Type: t, Writable: true, WriteOnly: true, Optional: true} }

var (
	nStr  = leaf("string")
	nBool = leaf("bool")
	nNum  = leaf("number")
	nAny  = leaf("any")

	// nMeta is MCP's _meta: an open map.
	nMeta = &Node{Type: "map", Open: true, Values: nAny, Names: "meta", Optional: true}

	// nProperty is one property of a JSON Schema: a tool's parameter.
	nProperty = &Node{Type: "map", Open: true, Values: nAny, Keys: map[string]*Node{
		"type": opt(nAny), "title": opt(nStr), "description": opt(nStr), "default": opt(nAny), "enum": opt(listOf(nAny)),
		"minimum": opt(nNum), "maximum": opt(nNum), "items": opt(nAny), "properties": opt(openMap(nAny)), "required": opt(listOf(nStr)),
	}}
	nIcons = listOf(object(map[string]*Node{"src": nStr, "mimeType": opt(nStr), "sizes": opt(listOf(nStr)), "theme": opt(nStr)}))

	nToolAnnotations = object(map[string]*Node{
		"title": opt(nStr), "readOnlyHint": opt(nBool), "destructiveHint": opt(nBool), "idempotentHint": opt(nBool), "openWorldHint": opt(nBool),
	})
	nAnnotations = object(map[string]*Node{"audience": opt(listOf(nStr)), "priority": opt(nNum), "lastModified": opt(nStr)})

	// nContent is one content block of a tool's result.
	nContent = &Node{Type: "map", Open: true, Values: nAny, Keys: map[string]*Node{
		"type": nStr, "text": opt(nStr), "data": opt(nStr), "mimeType": opt(nStr),
		"uri": opt(nStr), "name": opt(nStr), "title": opt(nStr), "description": opt(nStr), "size": opt(nNum),
		"resource":    opt(object(map[string]*Node{"uri": nStr, "mimeType": opt(nStr), "text": opt(nStr), "blob": opt(nStr), "_meta": nMeta})),
		"annotations": opt(nAnnotations), "_meta": nMeta,
	}}
	// nContents is one entry of a resource read.
	nContents = &Node{Type: "map", Open: true, Values: opt(nAny), Keys: map[string]*Node{"uri": nStr, "mimeType": opt(nStr), "text": opt(nStr), "blob": opt(nStr), "_meta": nMeta}}
)

func schemaNode(names string) *Node {
	return &Node{Type: "map", Open: true, Values: nAny, Keys: map[string]*Node{
		"type":       opt(nStr),
		"properties": opt(&Node{Type: "map", Open: true, Values: nProperty, Names: names}),
		"required":   opt(listOf(nStr)), "additionalProperties": opt(nAny),
	}}
}

// itemFields are the item's own fields, as MCP declares them, for each kind.
// The order is the order internal code walks them in.
var itemFields = map[ItemKind][]string{
	KindTool:     {"name", "title", "description", "inputSchema", "outputSchema", "annotations", "icons", "_meta"},
	KindPrompt:   {"name", "title", "description", "arguments", "icons", "_meta"},
	KindResource: {"uri", "name", "title", "description", "mimeType", "size", "annotations", "icons", "_meta"},
	KindTemplate: {"uriTemplate", "name", "title", "description", "mimeType", "annotations", "icons", "_meta"},
	KindSkill:    {"uri", "name", "description", "frontmatter", "resources"},
}

// definition is the item as MCP declares it: the form of X.upstream, and of
// X before anything is marked writable.
func definition(kind ItemKind) *Node {
	switch kind {
	case KindTool:
		return object(map[string]*Node{
			"name": nStr, "title": opt(nStr), "description": opt(nStr),
			"inputSchema": schemaNode("parameters"), "outputSchema": opt(schemaNode("output")),
			"annotations": opt(nToolAnnotations), "icons": opt(nIcons), "_meta": nMeta,
		})
	case KindPrompt:
		return object(map[string]*Node{
			"name": nStr, "title": opt(nStr), "description": opt(nStr),
			"arguments": opt(listOf(object(map[string]*Node{"name": nStr, "title": opt(nStr), "description": opt(nStr), "required": opt(nBool)}))),
			"icons":     opt(nIcons), "_meta": nMeta,
		})
	case KindResource:
		return object(map[string]*Node{
			"uri": nStr, "name": opt(nStr), "title": opt(nStr), "description": opt(nStr), "mimeType": opt(nStr), "size": opt(nNum),
			"annotations": opt(nAnnotations), "icons": opt(nIcons), "_meta": nMeta,
		})
	case KindTemplate:
		return object(map[string]*Node{
			"uriTemplate": nStr, "name": opt(nStr), "title": opt(nStr), "description": opt(nStr), "mimeType": opt(nStr),
			"annotations": opt(nAnnotations), "icons": opt(nIcons), "_meta": nMeta,
		})
	case KindSkill:
		return object(map[string]*Node{
			"uri": nStr, "name": opt(nStr), "description": opt(nStr),
			"frontmatter": opt(openMap(nAny)), "resources": opt(nAny),
		})
	}
	return object(nil)
}

// itemNode is the item on a hook: its definition, the gateway's view of its
// parameters where it has one, and upstream. On a list hook the fields in
// set are writable.
func itemNode(kind ItemKind, list bool) *Node {
	n := definition(kind)
	n.Doc = "The " + string(kind) + " as the gateway presents it."
	up := definition(kind)
	up.Doc = "The " + string(kind) + "'s canonical definition, as the server declares it. Nothing can set it."
	n.Keys["upstream"] = up
	mark := func(keys ...string) {
		if !list {
			return
		}
		for _, k := range keys {
			n.Keys[k] = writable(n.Keys[k])
		}
	}
	metaValues := func() {
		if list {
			m := *nMeta
			m.Values = writable(nAny)
			n.Keys["_meta"] = &m
		}
	}
	switch kind {
	case KindTool:
		param := &Node{Type: "map", Open: true, Values: nAny, Keys: map[string]*Node{}}
		for k, v := range nProperty.Keys {
			param.Keys[k] = v
		}
		if list {
			for k, t := range map[string]string{"name": "string", "description": "string", "hide": "bool", "pin": "any", "default": "any", "enum": "list", "minimum": "number", "maximum": "number", "add": "map"} {
				w := &Node{Type: t, Writable: true, Optional: true}
				if k == "name" || k == "hide" || k == "pin" || k == "add" {
					w.WriteOnly = true
				}
				param.Keys[k] = w
			}
		}
		n.Keys["params"] = &Node{Type: "map", Open: true, Values: param, Names: "parameters",
			Doc: "The gateway's view of the tool's parameters, by name. Setting one changes the schema the client sees and what the gateway enforces on a call."}
		mark("name", "title", "description", "outputSchema")
		if list {
			n.Keys["annotations"] = settable(nToolAnnotations, false)
		}
		metaValues()
	case KindPrompt:
		arg := object(map[string]*Node{"name": nStr, "title": opt(nStr), "description": opt(nStr), "required": opt(nBool)})
		if list {
			arg.Keys["name"], arg.Keys["description"] = writable(nStr), writable(opt(nStr))
		}
		n.Keys["params"] = &Node{Type: "map", Open: true, Values: arg, Names: "parameters",
			Doc: "The gateway's view of the prompt's arguments, by name."}
		mark("name", "title", "description")
		metaValues()
	case KindResource, KindTemplate:
		mark("title", "description")
		if list {
			n.Keys["annotations"] = settable(nAnnotations, true)
		}
		metaValues()
	}
	return n
}

// settable is a map of annotations a list rule may change: each key MCP
// names and, where whole is set, the map in one go. Another key cannot be
// set: the annotations are carried in MCP's own form, which has no place
// for it, so it would be lost on the way to the client.
func settable(annotations *Node, whole bool) *Node {
	a := clone(annotations)
	for k, c := range a.Keys {
		a.Keys[k] = writable(c)
	}
	a.Optional, a.Writable = true, whole
	return a
}

// requestParams is MCP's part of the request, by method.
func requestParams(method string, kind ItemKind, pre bool) *Node {
	p := object(map[string]*Node{"_meta": nMeta})
	switch method {
	case "tools/call":
		p.Keys["name"] = nStr
		p.Keys["arguments"] = &Node{Type: "map", Open: true, Values: opt(nAny), Follows: "inputSchema"}
	case "prompts/get":
		p.Keys["name"] = nStr
		p.Keys["arguments"] = &Node{Type: "map", Open: true, Values: opt(nStr), Names: "parameters"}
	case "resources/read":
		p.Keys["uri"] = nStr
	}
	if a := p.Keys["arguments"]; a != nil && pre {
		c := *a
		c.Values = writable(c.Values)
		p.Keys["arguments"] = &c
	}
	return p
}

// requestNode is the request branch. On a pre hook its arguments can be
// changed and there is no upstream yet; on a post hook nothing changes and
// upstream is the request as the server received it.
func requestNode(method string, kind ItemKind, pre, post bool) *Node {
	n := object(map[string]*Node{
		"method": doc(nStr, "The MCP method of the request, such as "+method+"."),
		"params": doc(requestParams(method, kind, pre), "MCP's part of the request."),
		"caller": doc(openMap(nAny), "Who sent the request. Each gateway defines these fields."),
		"client": object(map[string]*Node{
			"clientInfo":   opt(object(map[string]*Node{"name": opt(nStr), "version": opt(nStr), "title": opt(nStr)})),
			"capabilities": openMap(nAny),
		}),
		"headers": doc(openMap(opt(nStr)), "The request's HTTP headers that are on the gateway's allow-list, by lower-case name. The list starts empty."),
		"route":   doc(enum("direct", "mux"), "How the client reached the gateway."),
		"flags":   flagsNode(),
		"time":    doc(leaf("timestamp"), "When the request arrived."),
	})
	n.Doc = "The request."
	if post {
		up := object(map[string]*Node{"params": requestParams(method, kind, false)})
		up.Doc = "The request in the server's own terms: its name for the item and its names for the arguments."
		n.Keys["upstream"] = up
	}
	return n
}

func flagsNode() *Node {
	keys := map[string]*Node{}
	for _, f := range Flags {
		keys[f] = doc(nBool, FlagDocs[f])
	}
	return object(keys)
}

func serverNode(discover bool) *Node {
	n := object(map[string]*Node{
		"id": nStr, "slug": nStr, "url": opt(nStr), "name": opt(nStr), "title": opt(nStr), "description": opt(nStr),
		"instructions": opt(nStr), "protocolVersion": opt(nStr),
	})
	n.Doc = "The server as the gateway presents it."
	if discover {
		for _, k := range []string{"title", "description", "instructions"} {
			n.Keys[k] = writable(n.Keys[k])
		}
		up := object(map[string]*Node{"title": opt(nStr), "description": opt(nStr), "instructions": opt(nStr)})
		up.Doc = "What the server says about itself."
		n.Keys["upstream"] = up
	}
	return n
}

func withUpstream(n *Node, d string) *Node {
	up := clone(n)
	strip(up)
	up.Doc = d
	n.Keys["upstream"] = up
	return n
}

// clone copies a node and everything under it.
func clone(n *Node) *Node {
	if n == nil {
		return nil
	}
	c := *n
	if n.Keys != nil {
		c.Keys = make(map[string]*Node, len(n.Keys))
		for k, v := range n.Keys {
			c.Keys[k] = clone(v)
		}
	}
	c.Values, c.Items = clone(n.Values), clone(n.Items)
	return &c
}

// strip clears every writable mark under n.
func strip(n *Node) {
	if n == nil {
		return
	}
	n.Writable = false
	for _, v := range n.Keys {
		strip(v)
	}
	strip(n.Values)
	strip(n.Items)
}

// ---- the tree of each hook ------------------------------------------------------------

// hookMethod is the MCP method a hook belongs to.
func hookMethod(hook string) string {
	if i := strings.IndexByte(hook, '.'); i > 0 {
		return hook[:i]
	}
	return hook
}

// Tree is everything a rule on this hook can read and set.
func (h *HookSpec) Tree() *Node {
	treeOnce.Do(buildTrees)
	return trees[h.Name]
}

func buildTree(h *HookSpec) *Node {
	method := hookMethod(h.Name)
	root := object(map[string]*Node{})
	pre := h.Phase == PhaseCall
	post := h.Phase == PhaseElicit || h.Phase == PhaseResult || h.Phase == PhaseEvent || h.Phase == PhaseTask
	switch h.Name {
	case "server.settings", "server.session":
		method = "server/discover"
	}
	root.Keys["request"] = requestNode(method, h.Item, pre, post)
	root.Keys["server"] = serverNode(h.Name == "server/discover.post")
	if h.Item != KindNone && h.Item != KindServer {
		root.Keys[string(h.Item)] = itemNode(h.Item, h.Phase == PhaseList)
	}
	switch h.Phase {
	case PhaseCall:
		if h.Name == "tools/call.pre" {
			root.Keys["timeout"] = doc(setting("duration"), "How long the gateway waits for the server on this call.")
		}
	case PhaseEvent:
		p := object(map[string]*Node{"params": object(map[string]*Node{
			"progress": nNum, "total": opt(nNum), "message": writable(opt(nStr)), "progressToken": opt(nAny),
		})})
		p.Doc = "One progress notification from the server."
		root.Keys["progress"] = withUpstream(p, "The notification as the server sent it.")
	case PhaseElicit:
		e := object(map[string]*Node{
			"key": doc(nStr, "The key the server gave this question."), "method": nStr,
			"params": &Node{Type: "map", Open: true, Values: nAny, Keys: map[string]*Node{
				"mode": opt(nStr), "message": writable(opt(nStr)), "requestedSchema": opt(openMap(nAny)), "url": opt(nStr), "elicitationId": opt(nStr), "_meta": nMeta,
			}},
		})
		e.Doc = "A question the server asks the user in place of a result."
		up := object(map[string]*Node{"params": clone(e.Keys["params"])})
		strip(up)
		up.Doc = "The question as the server asked it."
		e.Keys["upstream"] = up
		root.Keys["elicitation"] = e
	case PhaseTask:
		t := object(map[string]*Node{
			"status":         doc(nStr, "working or input_required."),
			"statusMessage":  writable(opt(nStr)),
			"createdAt":      opt(nStr),
			"lastUpdatedAt":  opt(nStr),
			"ttlMs":          opt(nNum),
			"pollIntervalMs": opt(nNum),
		})
		t.Doc = "The task's status as the client will get it. Its id, questions and result are not here."
		root.Keys["task"] = withUpstream(t, "The status as the server sent it.")
	case PhaseResult:
		var result *Node
		if h.Item == KindResource {
			result = object(map[string]*Node{"contents": writable(listOf(nContents)), "_meta": nMeta, "resultType": opt(nStr)})
		} else {
			result = object(map[string]*Node{
				"content": writable(listOf(nContent)), "isError": opt(nBool), "_meta": nMeta, "resultType": opt(nStr),
				"structuredContent": writable(&Node{Type: "any", Optional: true, Follows: "outputSchema"}),
			})
		}
		r := object(map[string]*Node{"result": doc(result, "MCP's part of the response: the fields MCP names. A field MCP requires and the server left out is not there, and a rule that reads it fails.")})
		r.Doc = "The response the client will get."
		root.Keys["response"] = withUpstream(r, "The response as the server produced it.")
	}
	switch h.Name {
	case "server.settings":
		keys := map[string]*Node{}
		for _, k := range []string{"tools", "prompts", "resources", "tasks", "instructions", "serverInfo", "searchRoster", "progress"} {
			keys[k] = setting("bool")
		}
		root.Keys["settings"] = &Node{Type: "map", Keys: keys, WriteOnly: true, Doc: "Whole parts of this server, switched on or off."}
	case "server.session":
		root.Keys["session"] = &Node{Type: "map", WriteOnly: true, Doc: "What the gateway tells the server about the client when it opens a session.", Keys: map[string]*Node{
			"clientInfo":   setting("map"),
			"capabilities": {Type: "map", WriteOnly: true, Keys: map[string]*Node{"elicitation": setting("bool")}},
			"_meta":        setting("bool"),
		}}
	}
	return root
}

// Roots are the tree's top-level branches a rule can read on this hook.
func (h *HookSpec) Roots() []string {
	var out []string
	for k, n := range h.Tree().Keys {
		if !n.WriteOnly {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// writeRoot is the path of the one branch a rule on this hook may change,
// with a trailing dot. A setter is handed the path inside it.
func (h *HookSpec) writeRoot() string {
	switch h.Phase {
	case PhaseList, PhaseFilter:
		return string(h.Item) + "."
	case PhaseModify:
		return "server."
	case PhaseCall:
		return "request.params."
	case PhaseResult:
		return "response.result."
	case PhaseEvent:
		return "progress.params."
	case PhaseElicit:
		return "elicitation.params."
	case PhaseTask:
		return "task."
	}
	if h.Name == "server.settings" {
		return "settings."
	}
	return "session."
}

// dynamicRoots make a rule depend on the request rather than the catalog.
var dynamicRoots = map[string]bool{"request": true, "response": true, "progress": true, "elicitation": true, "task": true}

// ---- paths ----------------------------------------------------------------------------

// find walks a path from the root and returns the node it ends on. closed
// is the first step that names a key a closed map does not have, or "".
func (n *Node) find(path []string) (node *Node, closed string) {
	cur := n
	for i, p := range path {
		if cur == nil {
			return nil, ""
		}
		switch {
		case cur.Keys != nil && cur.Keys[p] != nil:
			cur = cur.Keys[p]
		case cur.Open || cur.Follows != "" || cur.Type == "any":
			if cur.Values == nil {
				return nil, ""
			}
			cur = cur.Values
		case cur.Type == "map":
			return nil, strings.Join(path[:i+1], ".")
		default:
			return nil, ""
		}
	}
	return cur, ""
}

// writablePath reports the node a `then` key sets, when a rule may set it.
func (n *Node) writablePath(path []string) *Node {
	node, _ := n.find(path)
	if node == nil || !node.Writable {
		return nil
	}
	return node
}

// Sets lists every path a rule on this hook may set, with "*" standing for
// any one name, sorted.
func (h *HookSpec) Sets() []Field {
	var out []Field
	var walk func(n *Node, path []string)
	walk = func(n *Node, path []string) {
		if n == nil {
			return
		}
		if n.Writable && len(path) > 0 {
			out = append(out, Field{Pattern: strings.Join(path, "."), Type: valueType(n.Type)})
		}
		keys := make([]string, 0, len(n.Keys))
		for k := range n.Keys {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if k == "upstream" {
				continue
			}
			walk(n.Keys[k], append(append([]string{}, path...), k))
		}
		if n.Values != nil && (n.Values.Writable || len(n.Values.Keys) > 0) {
			walk(n.Values, append(append([]string{}, path...), "*"))
		}
	}
	walk(h.Tree(), nil)
	sort.Slice(out, func(a, b int) bool { return out[a].Pattern < out[b].Pattern })
	return out
}

func valueType(t string) ValueType {
	switch t {
	case "string":
		return TText
	case "bool":
		return TBool
	case "number":
		return TNumber
	case "list":
		return TList
	case "map":
		return TMap
	case "duration":
		return TDuration
	}
	return TAny
}

// ---- JSON Schema ----------------------------------------------------------------------

// JSONSchema is the hook's tree as a JSON Schema (draft 2020-12): the schema
// of the document a rule on this hook reads. Values a rule only sets are
// left out. A timestamp is an RFC 3339 string.
func (h *HookSpec) JSONSchema() map[string]any {
	s := h.Tree().jsonSchema()
	s["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	s["title"] = h.Name
	return s
}

func (n *Node) jsonSchema() map[string]any {
	out := map[string]any{}
	if n.Doc != "" {
		out["description"] = n.Doc
	}
	switch n.Type {
	case "string":
		out["type"] = "string"
		if len(n.Enum) > 0 {
			out["enum"] = n.Enum
		}
	case "bool":
		out["type"] = "boolean"
	case "number":
		out["type"] = "number"
	case "timestamp":
		out["type"], out["format"] = "string", "date-time"
	case "duration":
		out["type"] = "string"
	case "list":
		out["type"] = "array"
		if n.Items != nil {
			out["items"] = n.Items.jsonSchema()
		}
	case "map":
		out["type"] = "object"
		props, required := map[string]any{}, []string{}
		for k, c := range n.Keys {
			if c.WriteOnly {
				continue
			}
			props[k] = c.jsonSchema()
			if !c.Optional {
				required = append(required, k)
			}
		}
		if len(props) > 0 {
			out["properties"] = props
		}
		if len(required) > 0 {
			sort.Strings(required)
			out["required"] = required
		}
		switch {
		case n.Open && n.Values != nil && n.Values.Type != "any":
			out["additionalProperties"] = n.Values.jsonSchema()
		case n.Open || n.Follows != "":
			out["additionalProperties"] = true
		default:
			out["additionalProperties"] = false
		}
	}
	return out
}

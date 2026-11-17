package hooks

// The shapes of what rules read. MCP fixes the form of a tool, a prompt, a
// resource, a call's result, a content block and an elicitation request, so
// the schema says it: an editor can complete upstream.annotations.readOnlyHint
// or result.content[0].text without guessing. A shape is a hint. Reading a
// key it does not list is not refused.

// Shape describes the form of a value a rule reads, as far as it is known.
type Shape struct {
	// Type is string, bool, number, list, map or any.
	Type string `json:"type"`
	// Keys are the keys a map is known to hold. It may hold others.
	Keys map[string]*Shape `json:"keys,omitempty"`
	// Items is the form of each element of a list.
	Items *Shape `json:"items,omitempty"`
	// Names says the map's keys are names from the server's catalog:
	// "parameters" (a tool's parameters or a prompt's arguments), "output"
	// (the properties of a tool's output schema) or "meta" (the keys of the
	// item's _meta). Values is the form of the value under each name.
	Names  string `json:"names,omitempty"`
	Values *Shape `json:"values,omitempty"`
	// Follows says the value takes its form from one of the item's own JSON
	// Schemas: "inputSchema" for arguments, "outputSchema" for structured
	// content.
	Follows string `json:"follows,omitempty"`
}

func obj(keys map[string]*Shape) *Shape { return &Shape{Type: "map", Keys: keys} }
func listOf(items *Shape) *Shape        { return &Shape{Type: "list", Items: items} }

var (
	shStr  = &Shape{Type: "string"}
	shBool = &Shape{Type: "bool"}
	shNum  = &Shape{Type: "number"}
	shAny  = &Shape{Type: "any"}
	shMap  = &Shape{Type: "map"}

	// shProperty is one property of a JSON Schema: a tool's parameter.
	shProperty = obj(map[string]*Shape{
		"type": shStr, "title": shStr, "description": shStr, "default": shAny, "enum": listOf(shAny),
		"minimum": shNum, "maximum": shNum, "items": shAny, "properties": shMap, "required": listOf(shStr),
	})
	shInputSchema = obj(map[string]*Shape{
		"type": shStr, "properties": {Type: "map", Names: "parameters", Values: shProperty},
		"required": listOf(shStr), "additionalProperties": shAny,
	})
	shOutputSchema = obj(map[string]*Shape{
		"type": shStr, "properties": {Type: "map", Names: "output", Values: shProperty},
		"required": listOf(shStr), "additionalProperties": shAny,
	})
	shToolAnnotations = obj(map[string]*Shape{
		"title": shStr, "readOnlyHint": shBool, "destructiveHint": shBool, "idempotentHint": shBool, "openWorldHint": shBool,
	})
	shAnnotations = obj(map[string]*Shape{"audience": listOf(shStr), "priority": shNum, "lastModified": shStr})
	shIcons       = listOf(obj(map[string]*Shape{"src": shStr, "mimeType": shStr, "sizes": listOf(shStr), "theme": shStr}))
	shMeta        = &Shape{Type: "map", Names: "meta", Values: shAny}

	// shContent is one content block of a tool's result.
	shContent = obj(map[string]*Shape{
		"type": shStr, "text": shStr, "data": shStr, "mimeType": shStr,
		"uri": shStr, "name": shStr, "title": shStr, "description": shStr, "size": shNum,
		"resource":    obj(map[string]*Shape{"uri": shStr, "mimeType": shStr, "text": shStr, "blob": shStr}),
		"annotations": shAnnotations,
	})

	shToolResult = obj(map[string]*Shape{
		"isError": shBool, "content": listOf(shContent), "structuredContent": {Type: "any", Follows: "outputSchema"},
		"meta": shMap, "text": shStr,
	})
	shReadResult = obj(map[string]*Shape{
		"text":     shStr,
		"contents": listOf(obj(map[string]*Shape{"uri": shStr, "mimeType": shStr, "text": shStr, "blob": shStr})),
	})
	shRequest = obj(map[string]*Shape{
		"key": shStr, "method": shStr, "mode": shStr, "message": shStr, "url": shStr, "elicitationId": shStr,
		"requestedSchema": obj(map[string]*Shape{"type": shStr, "properties": shMap, "required": listOf(shStr)}),
	})
	shServer      = obj(map[string]*Shape{"slug": shStr, "name": shStr, "title": shStr, "description": shStr, "instructions": shStr, "protocolVersion": shStr, "url": shStr})
	shEntitlement = obj(map[string]*Shape{"id": shStr, "slug": shStr, "name": shStr})
	shClient      = obj(map[string]*Shape{
		"name": shStr, "version": shStr, "title": shStr,
		"capabilities": obj(map[string]*Shape{"elicitation": shMap, "sampling": shMap, "roots": shMap}),
	})
)

// itemShape is the form of one field of an item, or nil when the field is a
// plain string, number or bool.
func itemShape(kind ItemKind, field string) *Shape {
	switch field {
	case "annotations", "annotationsRaw":
		if kind == KindTool {
			return shToolAnnotations
		}
		return shAnnotations
	case "meta":
		return shMeta
	case "icons":
		return shIcons
	case "inputSchema":
		return shInputSchema
	case "outputSchema":
		return shOutputSchema
	case "params":
		return &Shape{Type: "map", Names: "parameters", Values: shProperty}
	case "arguments":
		return listOf(obj(map[string]*Shape{"name": shStr, "title": shStr, "description": shStr, "required": shBool}))
	case "frontmatter":
		return obj(map[string]*Shape{"name": shStr, "description": shStr})
	}
	return nil
}

// upstreamShape is the form of `upstream`: the item's own fields. params is
// left out: it is the gateway's view of the parameters, built from
// inputSchema, and the upstream's item does not carry it.
func upstreamShape(kind ItemKind) *Shape {
	keys := map[string]*Shape{}
	for _, f := range ItemFields[kind] {
		if f == "params" {
			continue
		}
		if s := itemShape(kind, f); s != nil {
			keys[f] = s
			continue
		}
		t := itemFieldTypes[f]
		if t == "" {
			t = "any"
		}
		keys[f] = &Shape{Type: t}
	}
	return obj(keys)
}

// argsShape is the form of `args`: a tool's follow its input schema, a
// prompt's are strings by argument name, a read's is its URI.
func argsShape(kind ItemKind) *Shape {
	switch kind {
	case KindTool:
		return &Shape{Type: "map", Follows: "inputSchema"}
	case KindPrompt:
		return &Shape{Type: "map", Names: "parameters", Values: shStr}
	case KindResource:
		return obj(map[string]*Shape{"uri": shStr})
	}
	return nil
}

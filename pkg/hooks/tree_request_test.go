package hooks

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func treeServer(t *testing.T, rules ...Rule) *Server {
	t.Helper()
	return &Server{Slug: "a", Generation: "1", Info: ServerInfo{Name: "Docs", ProtocolVersion: ProtocolCurrent}, Rules: mustCompile(t, rules),
		Catalog: Catalog{
			Tools:     []*mcp.Tool{{Name: "t", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}}},
			Prompts:   []*mcp.Prompt{{Name: "p", Arguments: []*mcp.PromptArgument{{Name: "tone"}}}},
			Resources: []*mcp.Resource{{URI: "docs://a", Name: "a"}},
		}}
}

func treeRequest(s *Server) Request {
	return Request{Route: "direct", Servers: []*Server{s}, Principal: "p",
		Caller:  map[string]any{"userId": "u", "role": "member"},
		Client:  map[string]any{"clientInfo": map[string]any{"name": "claude-code", "version": "2.1"}, "capabilities": map[string]any{"elicitation": map[string]any{}}},
		Headers: map[string]string{"X-Team": "sre"}}
}

func outcomeText(o Outcome) string {
	b, _ := json.Marshal(struct {
		Result any       `json:"result"`
		Error  *RPCError `json:"error"`
	}{o.Result, o.Error})
	return string(b)
}

// The request branch holds MCP's part under params, as each method spells
// it, beside what the gateway adds: the caller, the client in MCP's shape,
// the allow-listed headers by lower-case name, the route, the flags.
func TestTheRequestOnEachMethod(t *testing.T) {
	said := func(hook, more string) Rule {
		return Rule{Hook: hook, When: "true", Then: map[string]string{KeyDecision: "'deny'", KeyReason: "request.method + ' ' + " + more +
			" + ' trace=' + request.params._meta.trace + ' client=' + request.client.clientInfo.name + '/' + request.client.clientInfo.version" +
			" + ' team=' + request.headers['x-team'] + ' route=' + request.route + ' search=' + string(request.flags.search) + ' role=' + request.caller.role"}}
	}
	s := treeServer(t,
		said("tools/call.pre", "request.params.name + ' q=' + request.params.arguments.q"),
		said("prompts/get.pre", "request.params.name + ' tone=' + request.params.arguments.tone"),
		said("resources/read.pre", "request.params.uri"))
	e := &Engine{}
	r := treeRequest(s)
	meta := mcp.Meta{"trace": "abc"}
	rest := " trace=abc client=claude-code/2.1 team=sre route=direct search=false role=member"

	call := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Meta: meta, Name: "t", Arguments: json.RawMessage(`{"q":"x"}`)})
	if got := outcomeText(call); !strings.Contains(got, "tools/call t q=x"+rest) {
		t.Errorf("tools/call.pre read %s", got)
	}
	get := e.GetPrompt(context.Background(), r, &mcp.GetPromptParams{Meta: meta, Name: "p", Arguments: map[string]string{"tone": "warm"}})
	if got := outcomeText(get); !strings.Contains(got, "prompts/get p tone=warm"+rest) {
		t.Errorf("prompts/get.pre read %s", got)
	}
	read := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{Meta: meta, URI: "docs://a"})
	if got := outcomeText(read); !strings.Contains(got, "resources/read docs://a"+rest) {
		t.Errorf("resources/read.pre read %s", got)
	}
}

// A rule sees no header the gateway did not pass. A plain read of one that
// is not there fails the rule, which refuses the call; the optional form
// gives the rule's own default.
func TestHeadersAreOnlyWhatTheGatewayPasses(t *testing.T) {
	e := &Engine{}
	plain := treeServer(t, Rule{Hook: "tools/call.pre", When: "request.headers['authorization'] != ''", Then: map[string]string{KeyDecision: "'deny'", KeyReason: "'saw a credential'"}})
	r := treeRequest(plain)
	out := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`)})
	if got := outcomeText(out); out.Forward != nil || strings.Contains(got, "saw a credential") || !strings.Contains(got, "refused by policy") {
		t.Errorf("a read of a header that was not passed: forward=%v %s", out.Forward != nil, got)
	}
	guarded := treeServer(t, Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{
		"request.params.arguments.q": "request.headers[?'authorization'].orValue('none') + '/' + request.headers[?'x-team'].orValue('none')"}})
	out = e.CallTool(context.Background(), treeRequest(guarded), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`)})
	if out.Forward == nil {
		t.Fatalf("not forwarded: %s", outcomeText(out))
	}
	if q := out.Forward.Params.(*mcp.CallToolParams).Arguments.(map[string]any)["q"]; q != "none/sre" {
		t.Errorf("q = %v, want none/sre", q)
	}
}

// The names in the tree are MCP's: every key of an MCP object in the tree is
// a field of the same object in the SDK that implements the 2026-07-28
// schema. What the gateway adds (params, upstream) is listed, so nothing
// else can slip in under an invented name.
func TestTreeNamesAreMCPs(t *testing.T) {
	tags := func(v any) map[string]bool {
		out := map[string]bool{}
		var walk func(rt reflect.Type)
		walk = func(rt reflect.Type) {
			for i := 0; i < rt.NumField(); i++ {
				f := rt.Field(i)
				tag := strings.Split(f.Tag.Get("json"), ",")[0]
				if f.Anonymous && f.Type.Kind() == reflect.Struct {
					walk(f.Type)
				}
				if tag != "" && tag != "-" {
					out[tag] = true
				}
			}
		}
		walk(reflect.TypeOf(v))
		return out
	}
	check := func(what string, n *Node, sdk map[string]bool, ours ...string) {
		t.Helper()
		if n == nil {
			t.Fatalf("%s is not in the tree", what)
		}
		gateway := map[string]bool{}
		for _, k := range ours {
			gateway[k] = true
		}
		var keys []string
		for k := range n.Keys {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !sdk[k] && !gateway[k] {
				t.Errorf("%s.%s is not a field MCP defines", what, k)
			}
		}
	}
	tool := Hooks["tools/list.post"].Tree().Keys["tool"]
	check("tool", tool, tags(mcp.Tool{}), "params", "upstream")
	check("tool.upstream", tool.Keys["upstream"], tags(mcp.Tool{}))
	check("tool.annotations", tool.Keys["annotations"], tags(mcp.ToolAnnotations{}))
	check("tool.icons[]", tool.Keys["icons"].Items, tags(mcp.Icon{}))
	prompt := Hooks["prompts/list.post"].Tree().Keys["prompt"]
	check("prompt", prompt, tags(mcp.Prompt{}), "params", "upstream")
	check("prompt.arguments[]", prompt.Keys["arguments"].Items, tags(mcp.PromptArgument{}))
	resource := Hooks["resources/list.post"].Tree().Keys["resource"]
	check("resource", resource, tags(mcp.Resource{}), "upstream")
	check("resource.annotations", resource.Keys["annotations"], tags(mcp.Annotations{}))
	check("template", Hooks["resources/templates/list.post"].Tree().Keys["template"], tags(mcp.ResourceTemplate{}), "upstream")

	callReq := Hooks["tools/call.post.result"].Tree().Keys["request"]
	check("tools/call request.params", callReq.Keys["params"], tags(mcp.CallToolParams{}))
	check("tools/call request.upstream.params", callReq.Keys["upstream"].Keys["params"], tags(mcp.CallToolParams{}))
	check("request.client.clientInfo", callReq.Keys["client"].Keys["clientInfo"], tags(mcp.Implementation{}))
	check("prompts/get request.params", Hooks["prompts/get.pre"].Tree().Keys["request"].Keys["params"], tags(mcp.GetPromptParams{}))
	check("resources/read request.params", Hooks["resources/read.pre"].Tree().Keys["request"].Keys["params"], tags(mcp.ReadResourceParams{}))

	result := Hooks["tools/call.post.result"].Tree().Keys["response"].Keys["result"]
	check("tools/call response.result", result, tags(mcp.CallToolResult{}), "resultType")
	contents := Hooks["resources/read.post.result"].Tree().Keys["response"].Keys["result"]
	check("resources/read response.result", contents, tags(mcp.ReadResourceResult{}), "resultType")
	check("resources/read response.result.contents[]", contents.Keys["contents"].Items, tags(mcp.ResourceContents{}))
	check("progress.params", Hooks["tools/call.post.progress"].Tree().Keys["progress"].Keys["params"], tags(mcp.ProgressNotificationParams{}))
	check("elicitation.params", Hooks["tools/call.post.elicit"].Tree().Keys["elicitation"].Keys["params"], tags(mcp.ElicitParams{}))
}

// The document a rule reads on a hook validates against the JSON Schema the
// same hook's tree gives. The document is built by the functions the engine
// itself uses, on a real call.
func TestTheDocumentARuleReadsMatchesItsSchema(t *testing.T) {
	s := treeServer(t, Rule{Hook: "tools/list.post", When: "true", Then: map[string]string{"tool.name": "'renamed'", "tool.title": "'A tool'"}})
	e := &Engine{}
	r := treeRequest(s)
	out := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Meta: mcp.Meta{"trace": "abc"}, Name: "renamed", Arguments: json.RawMessage(`{"q":"x"}`)})
	if out.Forward == nil || out.Call == nil {
		t.Fatalf("not forwarded: %s", outcomeText(out))
	}
	validate := func(hook string, doc map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(Hooks[hook].JSONSchema())
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("%s: the schema does not parse: %v", hook, err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatalf("%s: the schema does not resolve: %v", hook, err)
		}
		b, _ := json.Marshal(doc)
		var instance any
		_ = json.Unmarshal(b, &instance)
		if err := resolved.Validate(instance); err != nil {
			t.Errorf("%s: the document does not match its schema: %v\n%s", hook, err, b)
		}
	}
	item := subject(KindTool, out.Call.resolve)
	if item["name"] != "renamed" || item["upstream"].(map[string]any)["name"] != "t" {
		t.Fatalf("the tool branch is %v", item)
	}

	post := out.Call.roots(r)
	post["tool"] = item
	served := map[string]any{"content": []any{map[string]any{"type": "text", "text": "hi"}}}
	post["response"] = map[string]any{"result": served, "upstream": map[string]any{"result": served}}
	validate("tools/call.post.result", post)

	pre := r.context(s).roots("tools/call", out.Call.Params)
	pre["tool"] = item
	validate("tools/call.pre", pre)

	list := r.context(s).roots("tools/list", nil)
	list["tool"] = item
	validate("tools/list.post", list)

	progress := out.Call.roots(r)
	progress["tool"] = item
	sent := map[string]any{"progress": 1, "message": "working"}
	progress["progress"] = map[string]any{"params": sent, "upstream": map[string]any{"params": sent}}
	validate("tools/call.post.progress", progress)

	// A document with a field the tree does not have is refused by the
	// schema, so the schema is not just "any object".
	pre["tool"].(map[string]any)["invented"] = true
	raw, _ := json.Marshal(Hooks["tools/call.pre"].JSONSchema())
	var schema jsonschema.Schema
	_ = json.Unmarshal(raw, &schema)
	resolved, _ := schema.Resolve(nil)
	b, _ := json.Marshal(pre)
	var instance any
	_ = json.Unmarshal(b, &instance)
	if err := resolved.Validate(instance); err == nil {
		t.Error("a tool with an invented field validated")
	}
}

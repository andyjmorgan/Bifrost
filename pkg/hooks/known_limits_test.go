package hooks

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Known limits. Each test in this file asserts behaviour the engine does not
// have yet, and fails today. They are skipped unless HOOKS_KNOWN_GAPS is
// set:
//
//	HOOKS_KNOWN_GAPS=1 go test ./pkg/hooks/
//
// With it set, the tests in this file fail and every other test passes. When
// a limit is lifted, its test loses the knownGap call and moves to the file
// for its topic.

// knownGap skips a test unless HOOKS_KNOWN_GAPS is set, so the suite stays
// green while the limit stands.
func knownGap(t *testing.T) {
	t.Helper()
	if os.Getenv("HOOKS_KNOWN_GAPS") == "" {
		t.Skip("known gap; set HOOKS_KNOWN_GAPS=1 to run")
	}
}

// Known limit: a confirmation covers the arguments as they stood when the
// question was asked. A rule after the question runs on the answer round and
// not on the question round, so what it sets was never part of what was
// confirmed and can differ between the two rounds. The same is true of _meta
// that is forwarded to the server.
func TestAConfirmationCoversWhatARuleSetsAfterTheQuestion(t *testing.T) {
	knownGap(t)
	ctx := context.Background()
	cat := Catalog{Tools: []*mcp.Tool{{Name: "deploy", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"env": map[string]any{"type": "string"}}}}}}
	s := newServer(t, cat,
		Rule{Hook: "tools/call.pre", When: "true", Then: confirmThen("sure", "Deploy?")},
		Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{"request.params.arguments.env": "request.params.?_meta.?env.orValue('staging')"}})
	e, r := &Engine{}, newRequest("direct", s)
	k := requestState(e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "deploy", Arguments: json.RawMessage(`{}`), Meta: mcp.Meta{"env": "staging"}}))
	o := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "deploy", Arguments: json.RawMessage(`{}`), Meta: mcp.Meta{"env": "production"}, RequestState: k, InputResponses: yesTo("sure")})
	if o.Forward != nil && strings.Contains(asJSON(o.Forward.Params), `"env":"production"`) {
		t.Errorf("asked with _meta env=staging, answered with env=production, forwarded %s", asJSON(o.Forward.Params))
	}
	s = newServer(t, oneTool(),
		Rule{Hook: "tools/call.pre", When: "true", Then: confirmThen("sure", "Really?")},
		Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{"request.params.arguments.q": "request.headers[?'x-target'].orValue('safe')"}})
	r = newRequest("direct", s)
	r.Headers = map[string]string{"x-target": "safe"}
	k = requestState(e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`)}))
	r.Headers = map[string]string{"x-target": "prod"}
	o = e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`), RequestState: k, InputResponses: yesTo("sure")})
	if o.Forward != nil && strings.Contains(asJSON(o.Forward.Params), `"q":"prod"`) {
		t.Errorf("asked with header x-target=safe, answered with prod, forwarded %s", asJSON(o.Forward.Params))
	}
	s = newServer(t, oneTool(),
		Rule{Hook: "server.session", When: "true", Then: map[string]string{"session._meta": "true"}},
		Rule{Hook: "tools/call.pre", When: "true", Then: confirmThen("sure", "Really?")})
	r = newRequest("direct", s)
	k = requestState(e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`), Meta: mcp.Meta{"target": "safe"}}))
	o = e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`), Meta: mcp.Meta{"target": "prod"}, RequestState: k, InputResponses: yesTo("sure")})
	if o.Forward != nil && strings.Contains(asJSON(o.Forward.Params), `"target":"prod"`) {
		t.Errorf("asked with _meta target=safe, answered with prod, and the server was sent %s", asJSON(o.Forward.Params))
	}
}

// Known limit: the path check does not see every wrong name. The docs say
// every path is checked when the rule is saved and a wrong name never waits
// for a request. These wrong names, reached through a macro's index or key
// variable, an index, a ternary, a literal, an optional or a computed key,
// are saved. They fail when a request arrives, which denies.
func TestAWrongPathBehindAComputedStepIsRefusedWhenSaved(t *testing.T) {
	knownGap(t)
	for _, x := range [][2]string{
		{"tools/call.post.result", "response.result.content.exists(i, c, i.type == 'text')"}, // i is the index
		{"tools/list.post", "tool.params.exists(n, p, n.type == 'string')"},                  // n is the name
		{"tools/call.post.result", "response.result.content.filter(c, c.type == 'text')[0].nosuch == 1"},
		{"tools/call.post.result", "cel.bind(r, true ? response : response, r.nosuch == 1)"},
		{"tools/call.post.result", "(true ? response : response).nosuch == 1"},
		{"tools/call.post.result", "[response][0].nosuch == 1"},
		{"tools/call.post.result", "{'a': response}.a.nosuch == 1"},
		{"tools/call.post.result", "optional.of(response).value().nosuch == 1"},
		{"tools/call.post.result", "response['res' + 'ult'].nosuch == 1"},
	} {
		if probs, _ := saveRule(x[0], x[1], nil); probs == nil {
			t.Errorf("%s: saved: %s", x[0], x[1])
		}
	}
}

// Known limit: a constant whose form is wrong below its type is still saved:
// a duration that is not positive, a list whose elements have the wrong form,
// a value an any-typed path refuses at request time. Each fails closed when a
// request arrives.
func TestAConstantOfTheWrongFormIsRefusedWhenSaved(t *testing.T) {
	knownGap(t)
	for _, x := range [][3]string{
		{"tools/call.pre", "timeout", "duration('0s')"},
		{"tools/call.pre", "timeout", "duration('-5s')"},
		{"tools/call.post.result", "response.result.content", "[1]"},
		{"tools/call.post.result", "response.result.content", "['a']"},
		{"tools/call.post.result", "response.result.structuredContent", "'a string'"},
		{"tools/call.post.result", "response.result.structuredContent", "[1]"},
		{"server.session", "session.clientInfo", "{1: 2}"},
	} {
		if probs, _ := saveRule(x[0], "true", map[string]string{x[1]: x[2]}); probs == nil {
			t.Errorf("%s: saved: %s = %s", x[0], x[1], x[2])
		}
	}
}

// Known limit: the catalog is held in the SDK's types, which drop a zero. A
// priority of 0 and a `required` of false, which the server sent, are absent
// to rules and gone from the wire. A rule that hides low-priority resources
// with its own default lets the lowest one through.
func TestAZeroTheServerSentIsSeenByRules(t *testing.T) {
	knownGap(t)
	ctx := context.Background()
	var low mcp.Resource
	if err := json.Unmarshal([]byte(`{"uri":"docs://low","name":"low","annotations":{"priority":0}}`), &low); err != nil {
		t.Fatal(err)
	}
	var p mcp.Prompt
	if err := json.Unmarshal([]byte(`{"name":"p","arguments":[{"name":"tone","required":false}]}`), &p); err != nil {
		t.Fatal(err)
	}
	s := newServer(t, Catalog{Resources: []*mcp.Resource{&low}, Prompts: []*mcp.Prompt{&p}},
		Rule{Hook: "resources/list.post", When: "resource.?annotations.?priority.orValue(1.0) < 0.5", Then: map[string]string{KeyDecision: "'deny'"}},
		Rule{Hook: "prompts/list.post", When: "true", Then: map[string]string{"prompt.description": "prompt.arguments.map(a, has(a.required) ? 'sent' : 'absent').join(',')"}},
	)
	if got := asJSON((&Engine{}).ListResources(ctx, newRequest("direct", s)).Result); strings.Contains(got, "docs://low") {
		t.Errorf("a resource the server gave priority 0 read as having no priority and was listed: %s", got)
	}
	if got := asJSON((&Engine{}).ListPrompts(ctx, newRequest("direct", s)).Result); !strings.Contains(got, `"description":"sent"`) {
		t.Errorf("an argument the server marked required: false read as %s", got)
	}
}

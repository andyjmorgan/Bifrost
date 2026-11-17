package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// What may go back to the client in a reply.

// A list a rule sets is checked beyond being a list. A result rule cannot
// send content blocks that are not objects, and a read rule cannot send an
// entry whose text is a number.
func TestARuleCannotSendMalformedContent(t *testing.T) {
	s := newServer(t, oneTool(), Rule{Hook: "tools/call.post.result", When: "true", Then: map[string]string{"response.result.content": "[1, 'two', {'no': 'type'}]"}})
	if o := toolReply(t, s, `{"content":[]}`); strings.Contains(outcomeText(o), `"content":[1,"two"`) {
		t.Errorf("the client got %s", outcomeText(o))
	}
	s = newServer(t, Catalog{Resources: []*mcp.Resource{{URI: "docs://a", Name: "a"}}}, Rule{Hook: "resources/read.post.result", When: "true", Then: map[string]string{"response.result.contents": "[{'uri': 'docs://a', 'text': 5, 'blob': [1]}]"}})
	if o := readReply(t, s, "docs://a", `{"contents":[{"uri":"docs://a","text":"A"}]}`); strings.Contains(outcomeText(o), `"text":5`) {
		t.Errorf("the client got %s", outcomeText(o))
	}
}

// Tasks are off for every server. A settings rule cannot switch them on, and
// a task handle a server returns on a tool call, a prompt or a read does not
// reach the client.
func TestATaskHandleNeverReachesTheClient(t *testing.T) {
	ctx := context.Background()
	s := newServer(t, Catalog{Tools: anyArgsTool.Tools, Prompts: []*mcp.Prompt{{Name: "p"}}}, Rule{Hook: "server.settings", When: "true", Then: map[string]string{"settings.tasks": "true"}})
	e, r := &Engine{}, newRequest("direct", s)
	if sw, _ := e.Config(ctx, r, s); sw["tasks"] {
		t.Error("tasks switch turned on")
	}
	tp := &mcp.CallToolParamsRaw{Name: "t"}
	tc := e.CallTool(ctx, r, tp)
	pp := &mcp.GetPromptParams{Name: "p"}
	pc := e.GetPrompt(ctx, r, pp)
	rp := &mcp.ReadResourceParams{URI: "docs://a"}
	rc := e.ReadResource(ctx, r, rp)
	if tc.Call == nil || pc.Call == nil || rc.Call == nil {
		t.Fatal("task controls did not forward")
	}
	raw := json.RawMessage(`{"resultType":"task","task":{"taskId":"secret-task"}}`)
	for _, o := range []Outcome{e.UpstreamToolReply(ctx, r, tc.Call, tp, raw), e.UpstreamPromptReply(ctx, r, pc.Call, pp, raw), e.UpstreamReadReply(ctx, r, rc.Call, rp, raw)} {
		if strings.Contains(asJSON(o.Result), "secret-task") {
			t.Errorf("task handle escaped: %s", outcomeText(o))
		}
	}
}

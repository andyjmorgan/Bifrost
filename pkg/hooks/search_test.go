package hooks

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The search entry points: tool search, tool definitions and execute.

// A list rule that fails hides the tool, and that holds on every search
// entry point: the search, the definitions and the execute call.
func TestAToolWhoseListRuleFailedIsNotReachableThroughSearch(t *testing.T) {
	ctx := context.Background()
	s := newServer(t, oneTool(), Rule{Hook: "tools/list.post", When: "tool.title == 'x'", Then: map[string]string{KeyDecision: "'deny'"}})
	r := newRequest("direct", s)
	r.Flags = map[string]bool{FlagSearch: true}
	e := &Engine{}
	if o := e.ToolSearch(ctx, r, "t", 10); strings.Contains(asJSON(o.Result), `"name":"t"`) {
		t.Errorf("failed tool surfaced in search: %s", outcomeText(o))
	}
	if o := e.ToolDefinitions(ctx, r, []string{"t"}); strings.Contains(asJSON(o.Result), `"name":"t"`) {
		t.Errorf("failed tool surfaced in definitions: %s", outcomeText(o))
	}
	if o := e.ExecuteTool(ctx, r, "t", nil, nil); o.Forward != nil {
		t.Error("failed tool executed through search")
	}
}

// ExecuteTool, the search route's way to call a tool, carries a confirmation
// through: the request state and the answer passed on the second call forward
// the call.
func TestExecuteToolCarriesAConfirmationToItsAnswer(t *testing.T) {
	e, r := entitledRequest(t, Rule{Hook: "tools/call.pre", When: "true", Then: confirmThen("ok", "Continue?")})
	r.Route = "search"
	first := e.ExecuteTool(context.Background(), r, "a__t", nil, nil)
	ir, ok := first.Result.(InputRequired)
	if !ok {
		t.Fatalf("initial confirmation: %+v", first)
	}
	out := e.ExecuteTool(context.Background(), r, "a__t", nil, &mcp.CallToolParamsRaw{RequestState: ir.RequestState, InputResponses: yesTo("ok")})
	if out.Forward == nil || out.Error != nil {
		t.Fatalf("ExecuteTool API lost continuation: %+v", out)
	}
}

package hooks

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Rules see the real time when the gateway does not set one.
func TestNowDefaultsToTheRealClock(t *testing.T) {
	rules := []Rule{{Hook: "tools/call.pre", When: "request.time < timestamp('2020-01-01T00:00:00Z')", Then: map[string]string{"decision": "'deny'", "reason": "'the clock reads before 2020'"}}}
	e := &Engine{}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: mustCompile(t, rules), Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
	p := &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"abc"}`)}
	if out := e.CallTool(context.Background(), newRequest("direct", s), p); out.Forward == nil {
		b, _ := json.Marshal(out.Result)
		t.Errorf("an unset clock read as the distant past: %s", b)
	}
	r := newRequest("direct", s)
	r.Now = time.Date(2019, 6, 1, 0, 0, 0, 0, time.UTC)
	if out := e.CallTool(context.Background(), r, p); out.Forward != nil {
		t.Error("the request's own time was ignored")
	}
}

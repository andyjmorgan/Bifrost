package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
)

// rule builds a rule from a hook, a condition and then-pairs.
func rule(hook, when string, then ...string) hooks.Rule {
	r := hooks.Rule{Hook: hook, When: when, Then: map[string]string{}}
	for i := 0; i+1 < len(then); i += 2 {
		r.Then[then[i]] = then[i+1]
	}
	return r
}

func bg(t testing.TB) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// call calls a tool through an SDK session.
func call(t testing.TB, cs *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, error) {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	return cs.CallTool(bg(t), &mcp.CallToolParams{Name: name, Arguments: args})
}

// mustCall calls a tool and fails the test on a protocol error.
func mustCall(t testing.TB, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := call(t, cs, name, args)
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

// refused reports whether a call was refused: a protocol error or a tool
// result marked as an error.
func refused(res *mcp.CallToolResult, err error) bool {
	return err != nil || (res != nil && res.IsError)
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func eq(t testing.TB, what string, got, want any) {
	t.Helper()
	if g, w := asJSON(got), asJSON(want); g != w {
		t.Errorf("%s:\n  got  %s\n  want %s", what, g, w)
	}
}

func has(t testing.TB, what, text, part string) {
	t.Helper()
	if !strings.Contains(text, part) {
		t.Errorf("%s: %q does not contain %q", what, text, part)
	}
}

func lacks(t testing.TB, what, text, part string) {
	t.Helper()
	if strings.Contains(text, part) {
		t.Errorf("%s: %q contains %q", what, text, part)
	}
}

const plainTool = `{"name":"t","inputSchema":{"type":"object","properties":{}}}`

// eventually waits for a condition. The SDK client hands notifications to
// their handlers on its own goroutine, so a call can return before the last
// progress handler has run.
func eventually(t testing.TB, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Errorf("timed out waiting for: %s", what)
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

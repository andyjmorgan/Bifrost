package gateway_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness"
)

// legacyPost sends one JSON-RPC message the way a pre-2026-07-28 client does:
// no Mcp-Method header, and after the handshake the negotiated version in
// Mcp-Protocol-Version. It returns the status and the JSON-RPC reply, if any.
func legacyPost(t *testing.T, url, version, body string) (int, map[string]any, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if version != "" {
		req.Header.Set("Mcp-Protocol-Version", version)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	text := string(raw)
	// A reply is either one JSON object or a single-event stream.
	payload := text
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "data:") {
			payload = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	var msg map[string]any
	_ = json.Unmarshal([]byte(payload), &msg)
	return res.StatusCode, msg, text
}

const legacyInit = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"old-client","version":"1"}}}`

// By default the front door is 2026-07-28 only. The library still answers an
// older client's initialize, and refuses the request that follows it. This is
// the behaviour older clients hit; the test pins it so the default is a choice.
func TestAnOlderClientIsRefusedAfterItsHandshakeByDefault(t *testing.T) {
	r := newRig(t)
	u := harness.NewUpstream(t, harness.V2026)
	u.Tool(toolT, harness.Static(harness.Reply{Result: harness.Text("ok")}))
	r.serve("a", u)

	if status, _, body := legacyPost(t, r.direct("a"), "", legacyInit); status != http.StatusOK {
		t.Fatalf("initialize: %d %s", status, body)
	}
	status, _, body := legacyPost(t, r.direct("a"), "2025-11-25", `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if status != http.StatusBadRequest || !strings.Contains(body, "Unsupported protocol version") {
		t.Fatalf("an older client should be refused by default, got %d %q", status, body)
	}
}

// With older versions allowed, a client that speaks 2025-11-25 completes its
// handshake, lists and calls, and the server's rules apply to it exactly as
// they do to a current client.
func TestAnOlderClientListsAndCallsThroughTheRulesWhenAllowed(t *testing.T) {
	r := newRig(t)
	r.gw.SetLegacyProtocolVersions([]string{"2025-11-25", "2025-06-18", " ", "2026-07-28"})
	u := harness.NewUpstream(t, harness.V2026)
	u.Tool(toolT, func(rec harness.Recorded) harness.Reply {
		return harness.Reply{Result: harness.Text("served " + rec.Map()["name"].(string))}
	})
	u.Tool(`{"name":"secret","inputSchema":{"type":"object"}}`, harness.Static(harness.Reply{Result: harness.Text("leak")}))
	r.serve("a", u,
		deny("tools/list.post", "tool.name == 'secret'"),
		hooks.Rule{Hook: "tools/list.post", When: "tool.upstream.name == 't'", Then: map[string]string{"tool.name": "'renamed'"}})

	status, init, body := legacyPost(t, r.direct("a"), "", legacyInit)
	if status != http.StatusOK {
		t.Fatalf("initialize: %d %s", status, body)
	}
	negotiated, _ := init["result"].(map[string]any)["protocolVersion"].(string)
	if negotiated != "2025-11-25" {
		t.Fatalf("negotiated %q, want the client's own 2025-11-25", negotiated)
	}
	if status, _, body := legacyPost(t, r.direct("a"), negotiated, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); status != http.StatusAccepted && status != http.StatusOK {
		t.Fatalf("initialized notification: %d %q", status, body)
	}

	status, list, body := legacyPost(t, r.direct("a"), negotiated, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if status != http.StatusOK {
		t.Fatalf("tools/list: %d %s", status, body)
	}
	var got []string
	for _, tool := range list["result"].(map[string]any)["tools"].([]any) {
		got = append(got, tool.(map[string]any)["name"].(string))
	}
	if !same(got, "renamed") {
		t.Fatalf("an older client listed %v; the rules must hide secret and rename t", got)
	}

	status, call, body := legacyPost(t, r.direct("a"), negotiated, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"renamed","arguments":{}}}`)
	if status != http.StatusOK {
		t.Fatalf("tools/call: %d %s", status, body)
	}
	content := call["result"].(map[string]any)["content"].([]any)
	if text := content[0].(map[string]any)["text"]; text != "served t" {
		t.Fatalf("call returned %v (%s)", text, body)
	}
	status, denied, body := legacyPost(t, r.direct("a"), negotiated, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"secret","arguments":{}}}`)
	if status != http.StatusOK || denied["error"] == nil {
		t.Fatalf("a tool hidden by a rule must not be callable by an older client: %d %s", status, body)
	}

	// A version that was not allowed is still refused.
	if status, _, _ := legacyPost(t, r.direct("a"), "2024-11-05", `{"jsonrpc":"2.0","id":5,"method":"tools/list"}`); status != http.StatusBadRequest {
		t.Fatalf("2024-11-05 was not allowed and got %d", status)
	}
	// And a current client is unaffected.
	cur := harness.Raw(t, r.direct("a"), harness.RawRequest{Method: "tools/list"})
	if got := names(t, cur, "tools", "name"); !same(got, "renamed") {
		t.Fatalf("a current client listed %v", got)
	}
}

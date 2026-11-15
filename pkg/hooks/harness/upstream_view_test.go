package harness

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const telemetryTool = `{"name":"search_logs","inputSchema":{"type":"object","properties":{
  "query":{"type":"string"},
  "telemetry":{"type":"object","additionalProperties":false,"properties":{"intent":{"type":"string"}},"required":["intent"]}},
  "required":["telemetry"]}}`

// Datadog's required telemetry parameter: hidden from the client, pinned for
// the server.
func TestHiddenPinnedParameter(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(telemetryTool, nil)
	g := NewGateway(t)
	g.AddServer(t, "dd", u, rule("tools/modify", "'telemetry' in params",
		"params.telemetry.hide", "true",
		"params.telemetry.pin", "{'intent': 'mind your own business'}"))
	cs := Connect(t, g.Direct("dd"), ClientOptions{})

	lt, err := cs.ListTools(bg(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	schema := asJSON(lt.Tools[0].InputSchema)
	lacks(t, "the client's schema", schema, "telemetry")
	has(t, "the client's schema", schema, "query")

	res := mustCall(t, cs, "search_logs", map[string]any{"query": "errors"})
	if res.IsError {
		t.Fatalf("the call was refused: %s", TextOf(res))
	}
	eq(t, "upstream arguments", u.Last(t, "tools/call").Args(),
		map[string]any{"query": "errors", "telemetry": map[string]any{"intent": "mind your own business"}})

	// A client that sends the hidden parameter itself never gets its value
	// through.
	u.Forget()
	_, _ = call(t, cs, "search_logs", map[string]any{"query": "errors", "telemetry": map[string]any{"intent": "leak"}})
	for _, r := range u.Requests("tools/call") {
		lacks(t, "upstream arguments", asJSON(r.Args()), "leak")
	}
}

// Renames are reversed on the way in: the server sees its own names.
func TestRenamesAreReversed(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(`{"name":"confluence_search","inputSchema":{"type":"object","properties":{"cql":{"type":"string"},"limit":{"type":"integer"}}}}`, nil)
	g := NewGateway(t)
	g.AddServer(t, "wiki", u,
		rule("tools/modify", "true", "name", "regex.replace(name, '^confluence_', '')"),
		rule("tools/modify", "name == 'search'", "params.cql.name", "'query'"))
	cs := Connect(t, g.Direct("wiki"), ClientOptions{})

	lt, err := cs.ListTools(bg(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "tools", toolNames(lt.Tools), []string{"search"})
	schema := asJSON(lt.Tools[0].InputSchema)
	has(t, "the client's schema", schema, `"query"`)
	lacks(t, "the client's schema", schema, `"cql"`)

	res := mustCall(t, cs, "search", map[string]any{"query": "space = ENG", "limit": 5})
	if res.IsError {
		t.Fatalf("the call was refused: %s", TextOf(res))
	}
	up := u.Last(t, "tools/call")
	eq(t, "upstream tool name", up.Map()["name"], "confluence_search")
	eq(t, "upstream arguments", up.Args(), map[string]any{"cql": "space = ENG", "limit": 5})

	if _, err := call(t, cs, "confluence_search", nil); err == nil {
		t.Error("the upstream name still works after a rename")
	}
}

// Upstream _meta is stripped from the catalog, MCP Apps' ui key included,
// and the client's request _meta is not passed to the server.
func TestMetaIsStripped(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(`{"name":"chart","inputSchema":{"type":"object","properties":{}},
	  "_meta":{"ui":{"resourceUri":"ui://apps/dataviz"},"vendor/internal":"x"}}`, nil)
	g := NewGateway(t)
	g.AddServer(t, "dd", u)
	cs := Connect(t, g.Direct("dd"), ClientOptions{})

	lt, err := cs.ListTools(bg(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	listed := asJSON(lt.Tools[0])
	lacks(t, "the listed tool", listed, "ui://apps/dataviz")
	lacks(t, "the listed tool", listed, "vendor/internal")

	p := &mcp.CallToolParams{Name: "chart", Arguments: map[string]any{}, Meta: mcp.Meta{"trace": "abc"}}
	p.SetProgressToken("client-token")
	if _, err := cs.CallTool(bg(t), p); err != nil {
		t.Fatal(err)
	}
	meta := u.Last(t, "tools/call").Meta()
	if _, ok := meta["trace"]; ok {
		t.Errorf("the client's _meta reached the server: %v", meta)
	}
	info, _ := meta[metaClient].(map[string]any)
	eq(t, "the server sees the gateway as its client", info["name"], "bifrost")
	lacks(t, "the server's view of the request", asJSON(meta), "harness-client")
	lacks(t, "the server's view of the request", asJSON(meta), "client-token")
}

// With meta forwarding on, the client's keys pass, but still never its
// progress token: the server gets the gateway's own.
func TestMetaForwardingKeepsTheGatewayToken(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, nil)
	g := NewGateway(t)
	g.AddServer(t, "a", u, rule("server/connect", "true", "meta", "true"))
	cs := Connect(t, g.Direct("a"), ClientOptions{})

	p := &mcp.CallToolParams{Name: "t", Arguments: map[string]any{}, Meta: mcp.Meta{"trace": "abc"}}
	p.SetProgressToken("client-token")
	if _, err := cs.CallTool(bg(t), p); err != nil {
		t.Fatal(err)
	}
	up := u.Last(t, "tools/call")
	eq(t, "forwarded _meta key", up.Meta()["trace"], "abc")
	tok, _ := up.ProgressToken().(string)
	if !strings.HasPrefix(tok, "gw-") {
		t.Errorf("the server's progress token should be the gateway's own, got %v", up.ProgressToken())
	}
}

// The server is told the gateway's protocol version and identity on every
// request, in the 2026-07-28 form.
func TestUpstreamSeesPerRequestIdentity(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, nil)
	g := NewGateway(t)
	g.AddServer(t, "a", u)
	cs := Connect(t, g.Direct("a"), ClientOptions{})
	mustCall(t, cs, "t", nil)
	up := u.Last(t, "tools/call")
	eq(t, "protocol version in _meta", up.Meta()[metaVersion], V2026)
	eq(t, "protocol version header", up.Header.Get("Mcp-Protocol-Version"), V2026)
	if n := len(u.Requests("initialize")); n != 0 {
		t.Errorf("a 2026-07-28 server got %d initialize requests", n)
	}
}

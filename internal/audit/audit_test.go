package audit

import (
	"context"
	"net/http"
	"testing"

	"github.com/andyjmorgan/Bifrost/internal/store"
)

func TestSniff(t *testing.T) {
	cases := []struct {
		body string
		want RPCInfo
	}{
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"roll","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`,
			RPCInfo{Method: "tools/call", ID: "1", ToolName: "roll", ProtocolVersion: "2026-07-28"}},
		{`{"jsonrpc":"2.0","method":"notifications/initialized"}`, RPCInfo{Method: "notifications/initialized", IsNotification: true}},
		{`{"jsonrpc":"2.0","id":"abc","method":"initialize","params":{"protocolVersion":"2025-03-26"}}`, RPCInfo{Method: "initialize", ID: "abc", ProtocolVersion: "2025-03-26"}},
		{`[{"jsonrpc":"2.0","id":7,"method":"ping"}]`, RPCInfo{Method: "ping", ID: "7"}},
		{`not json`, RPCInfo{}},
	}
	for _, c := range cases {
		if got := Sniff([]byte(c.body)); got != c.want {
			t.Errorf("Sniff(%s) = %+v, want %+v", c.body, got, c.want)
		}
	}
}

func TestRedact(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer x")
	h.Set("X-Api-Key", "k")
	h.Set("X-Custom-Token", "t")
	h.Set("Content-Type", "application/json")
	got := Redact(h, "X-Custom-Token")
	for _, k := range []string{"Authorization", "X-Api-Key", "X-Custom-Token"} {
		if got[k] != "[redacted]" {
			t.Errorf("%s not redacted: %q", k, got[k])
		}
	}
	if got["Content-Type"] != "application/json" {
		t.Errorf("content-type mangled: %q", got["Content-Type"])
	}
}

func TestOutcome(t *testing.T) {
	if outcomeFor(401, nil) != "denied" || outcomeFor(500, nil) != "error" || outcomeFor(200, []byte(`{"result":{}}`)) != "success" {
		t.Fatal("status mapping wrong")
	}
	if outcomeFor(200, []byte(`event: message
data: {"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"nope"}}`)) != "error" {
		t.Fatal("jsonrpc error in SSE not detected")
	}
}

// A request keeps at most MaxRuleHits hits, and a plain set goes first when
// there are more: a deny or an audit rule's verdict is what an operator
// came to read.
func TestRuleHitsAreCappedKeepingWhatMatters(t *testing.T) {
	ctx := context.WithValue(context.Background(), keyRules, &rules{})
	if RuleHits(ctx) != nil {
		t.Fatal("no hits must be nil, so nothing is stored")
	}
	for i := 0; i < MaxRuleHits+20; i++ {
		AddRuleHits(ctx, store.RuleHit{Rule: "set", Effect: "set"})
	}
	AddRuleHits(ctx, store.RuleHit{Rule: "deny", Effect: "deny"}, store.RuleHit{Rule: "audit", Effect: "would deny"})
	got := RuleHits(ctx)
	if len(got.Hits) != MaxRuleHits || got.Dropped != 22 {
		t.Fatalf("kept %d, dropped %d", len(got.Hits), got.Dropped)
	}
	if got.Hits[0].Rule != "deny" || got.Hits[1].Rule != "audit" {
		t.Fatalf("the first hits are %q and %q, want the deny and the audit rule", got.Hits[0].Rule, got.Hits[1].Rule)
	}
}

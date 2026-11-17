package hooks

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Where confirmation state is kept: the in-memory store, and a store the
// gateway supplies.

// The in-memory store hands a challenge out once and forgets it after its
// lifetime.
func TestMemoryStatesTakeOnceAndExpire(t *testing.T) {
	m := &MemoryStates{}
	m.Put("c_one", State{Target: "t"}, time.Minute)
	if st, ok := m.Take("c_one"); !ok || st.Target != "t" {
		t.Fatalf("first take: %+v %v", st, ok)
	}
	if _, ok := m.Take("c_one"); ok {
		t.Error("a challenge was handed out twice")
	}
	if _, ok := m.Take("c_never"); ok {
		t.Error("an unknown key was found")
	}
	m.Put("c_old", State{}, -time.Second)
	if _, ok := m.Take("c_old"); ok {
		t.Error("an expired challenge was handed out")
	}
	// Unanswered challenges are swept, so they cannot pile up.
	m.Put("c_stale", State{}, -time.Second)
	m.swept = time.Now().Add(-2 * time.Minute)
	m.Put("c_new", State{}, time.Minute)
	if n := m.Len(); n != 1 {
		t.Errorf("%d challenges held after a sweep, want 1", n)
	}
}

// countingStates is a store that counts its calls and takes no lifetime but
// StateTTL.
type countingStates struct {
	MemoryStates
	puts, takes atomic.Int64
}

func (c *countingStates) Put(k string, st State, ttl time.Duration) {
	c.puts.Add(1)
	if ttl != StateTTL {
		panic("unexpected lifetime")
	}
	c.MemoryStates.Put(k, st, ttl)
}

func (c *countingStates) Take(k string) (State, bool) {
	c.takes.Add(1)
	return c.MemoryStates.Take(k)
}

// The gateway can supply its own store. The engine uses it for every
// confirmation and never falls back to memory.
func TestConfirmationsUseTheConfiguredStore(t *testing.T) {
	store := &countingStates{}
	e := &Engine{States: store}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: confirmEveryCallSet, Catalog: Catalog{Tools: []*mcp.Tool{{Name: "t", InputSchema: map[string]any{"type": "object"}}}}}
	r := newRequest("direct", s)
	ctx := context.Background()
	first := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"id":1}`)})
	ir := first.Result.(InputRequired)
	var no mcp.InputResponseMap
	_ = json.Unmarshal([]byte(`{"bifrost.ok":{"action":"decline"}}`), &no)
	out := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"id":1}`), RequestState: ir.RequestState, InputResponses: no})
	if out.Forward != nil {
		t.Fatal("a declined confirmation was forwarded")
	}
	found := false
	for _, h := range out.Hits {
		if h.Rule == "(confirmation)" && h.Effect == "deny" {
			found = true
		}
	}
	if !found {
		t.Errorf("the decline left no audit hit: %+v", out.Hits)
	}
	if store.puts.Load() != 1 || store.takes.Load() != 1 || e.mem != nil {
		t.Errorf("puts %d, takes %d, fallback store used: %v", store.puts.Load(), store.takes.Load(), e.mem != nil)
	}
}

package harness

import (
	"testing"
)

// The SDK client caches listings and reads for the ttlMs the gateway
// states. A deny added later is not visible in a cached listing, but it
// still stops the call, because rules run on every request.
func TestDenyAppliesAtCallTimeDespiteACachedListing(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Tool(plainTool, nil)
	g := NewGateway(t)
	g.AddServer(t, "a", u)
	cs := Connect(t, g.Direct("a"), ClientOptions{})

	lt, err := cs.ListTools(bg(t), nil)
	if err != nil || len(lt.Tools) != 1 {
		t.Fatalf("first listing: %v %v", err, lt)
	}
	mustCall(t, cs, "t", nil)

	g.SetRules(t, "a", rule("tools/filter", "true", "decision", "'deny'"))

	again, err := cs.ListTools(bg(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("the same session lists %d tools after the deny (its cache holds the first listing for ttlMs)", len(again.Tools))
	fresh, err := Connect(t, g.Direct("a"), ClientOptions{}).ListTools(bg(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "tools a new session sees after the deny", len(fresh.Tools), 0)

	u.Forget()
	res, err := call(t, cs, "t", nil)
	if !refused(res, err) {
		t.Error("a call to a tool denied after the listing went through")
	}
	eq(t, "calls that reached the server", len(u.Requests("tools/call")), 0)
}

// Reads: how many reach the gateway when one session reads the same URI
// twice. This records what the SDK client does with the ttlMs on a read
// result; it asserts only that a deny is honoured for a session without the
// content cached.
func TestReadCachingInTheSDKClient(t *testing.T) {
	u := NewUpstream(t, V2026)
	u.Resource("docs://a", nil)
	g := NewGateway(t)
	g.AddServer(t, "a", u)
	cs := Connect(t, g.Direct("a"), ClientOptions{})
	if _, err := read(t, cs, "docs://a"); err != nil {
		t.Fatal(err)
	}
	if _, err := read(t, cs, "docs://a"); err != nil {
		t.Fatal(err)
	}
	n := len(u.Requests("resources/read"))
	t.Logf("two reads of one URI on one session reached the server %d time(s)", n)

	g.SetRules(t, "a", rule("resources/filter", "true", "decision", "'deny'"))
	_, cachedErr := read(t, cs, "docs://a")
	t.Logf("after a deny, the same session's read returns error=%v", cachedErr)
	if _, err := read(t, Connect(t, g.Direct("a"), ClientOptions{}), "docs://a"); err == nil {
		t.Error("a new session read a denied resource")
	}
}

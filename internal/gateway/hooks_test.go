package gateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/internal/audit"
	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/cache"
	"github.com/andyjmorgan/Bifrost/internal/crypto"
	"github.com/andyjmorgan/Bifrost/internal/gateway"
	"github.com/andyjmorgan/Bifrost/internal/store"
	"github.com/andyjmorgan/Bifrost/internal/upstream"
	"github.com/andyjmorgan/Bifrost/pkg/hooks"
	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness"
	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness/clients"
)

// These tests run the real Bifrost front door (the Go SDK server and the
// gateway's middleware) over HTTP, with the rules engine behind it, against
// mock upstream servers. The store is in memory. They prove the wiring: that
// a request reaches the engine with the right context, that a forward
// reaches the upstream as the engine wrote it, and that the reply comes back
// through the engine. What each rule does is proven by the engine's own
// golden set.

// ---- an in-memory store -------------------------------------------------------------

type memStore struct {
	mu      sync.Mutex
	servers []*store.Server
	creds   map[uuid.UUID]*store.Credential
	cache   map[string]memEntry
}

type memEntry struct {
	value   json.RawMessage
	expires time.Time
}

func newMemStore() *memStore {
	return &memStore{creds: map[uuid.UUID]*store.Credential{}, cache: map[string]memEntry{}}
}

func (m *memStore) ListServers(context.Context, string) ([]*store.Server, error) {
	return m.servers, nil
}

func (m *memStore) bundle(id uuid.UUID, ns []string) store.CacheBundle {
	b := store.CacheBundle{}
	for _, n := range ns {
		if e, ok := m.cache[n+"|"+id.String()]; ok && time.Now().Before(e.expires) {
			b[n] = e.value
		}
	}
	return b
}

func (m *memStore) GetExposureBySlug(_ context.Context, _, _, _, slug string) (*store.Server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.servers {
		if s.Slug == slug {
			c := *s
			return &c, nil
		}
	}
	return nil, store.ErrNotFound
}

func (m *memStore) ListExposuresWithCache(_ context.Context, _, _, _ string, ns ...string) ([]*store.Server, map[uuid.UUID]store.CacheBundle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*store.Server
	bundles := map[uuid.UUID]store.CacheBundle{}
	for _, s := range m.servers {
		c := *s
		out = append(out, &c)
		bundles[s.ID] = m.bundle(s.ID, ns)
	}
	return out, bundles, nil
}

func (m *memStore) GetExposureBySlugWithCache(ctx context.Context, org, user, role, slug string, ns ...string) (*store.Server, store.CacheBundle, error) {
	s, err := m.GetExposureBySlug(ctx, org, user, role, slug)
	if err != nil {
		return nil, nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return s, m.bundle(s.ID, ns), nil
}

func (m *memStore) ObserveCapabilities(context.Context, uuid.UUID, string, map[string]store.Seen) error {
	return nil
}

func (m *memStore) CacheGet(_ context.Context, ns, key string) (json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.cache[ns+"|"+key]; ok && time.Now().Before(e.expires) {
		return e.value, nil
	}
	return nil, store.ErrNotFound
}

func (m *memStore) CacheSet(_ context.Context, ns, key string, _ *uuid.UUID, _ time.Time, value json.RawMessage, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cache[ns+"|"+key] = memEntry{value: value, expires: time.Now().Add(ttl)}
	return nil
}

func (m *memStore) CacheTake(_ context.Context, ns, key string) (json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.cache[ns+"|"+key]
	if !ok || time.Now().After(e.expires) {
		return nil, store.ErrNotFound
	}
	delete(m.cache, ns+"|"+key)
	return e.value, nil
}

// count is how many live entries a cache namespace holds.
func (m *memStore) count(ns string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k := range m.cache {
		if strings.HasPrefix(k, ns+"|") {
			n++
		}
	}
	return n
}

func (m *memStore) GetCredential(_ context.Context, id uuid.UUID) (*store.Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.creds[id]; ok {
		return c, nil
	}
	return nil, store.ErrNotFound
}

func (m *memStore) UpdateOAuthTokens(context.Context, uuid.UUID, []byte, string, *time.Time, bool, []string) error {
	return nil
}

func (m *memStore) SetCredentialState(context.Context, uuid.UUID, store.CredentialState, string) error {
	return nil
}

func (m *memStore) InsertAuditEvent(context.Context, *store.AuditEvent, *store.AuditPayload) error {
	return nil
}

// ---- the rig ------------------------------------------------------------------------

type rig struct {
	t     *testing.T
	store *memStore
	srv   *httptest.Server
	gw    *gateway.Gateway
}

// entitle registers an entitlement on an upstream, with its rules.
func (r *rig) entitle(slug string, u *harness.Upstream, rules ...hooks.Rule) *store.Server {
	r.t.Helper()
	pol, _ := json.Marshal(rules)
	if len(rules) == 0 {
		pol = []byte("[]")
	}
	s := &store.Server{ID: uuid.New(), OrgID: "org", Name: "Server " + slug, Description: "the " + slug + " server", URL: u.URL(), Enabled: true,
		Slug: slug, EntitlementID: uuid.New(), EntitlementName: "Readers of " + slug, Policies: pol, Generation: 1, UpdatedAt: time.Now()}
	r.store.mu.Lock()
	r.store.servers = append(r.store.servers, s)
	r.store.mu.Unlock()
	return s
}

func (r *rig) direct(slug string) string { return r.srv.URL + "/" + slug + "/mcp" }
func (r *rig) mux() string               { return r.srv.URL + "/mcp" }

func newRig(t *testing.T) *rig {
	t.Helper()
	ms := newMemStore()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rec := audit.NewRecorder(ctx, ms, log)
	sealer, err := crypto.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	conn := upstream.New(ms, sealer, nil, rec, log, 10*time.Second, time.Minute, "test")
	conn.OnInit = func(srv *store.Server, ir *mcp.InitializeResult) {
		b, _ := json.Marshal(gateway.InitFrom(ir))
		_ = ms.CacheSet(ctx, cache.NSInit, srv.ID.String(), nil, time.Time{}, b, time.Minute)
	}
	gw := gateway.New(ms, conn, rec, nil, time.Minute, log, "test")
	caller := &auth.Caller{OrgID: "org", UserID: "u1", Role: auth.RoleMember, Subject: "u1@example.com", Email: "u1@example.com", Name: "User One", Type: auth.ActorOIDC}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithCaller(req.Context(), caller)))
		})
	})
	router.Handle("/mcp", gw.MuxHandler())
	router.Handle("/{slug}/mcp", gw.DirectHandler())
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return &rig{t: t, store: ms, srv: srv, gw: gw}
}

// ---- helpers ------------------------------------------------------------------------

var elicits = map[string]any{"elicitation": map[string]any{}}

func call(name string, args map[string]any) harness.RawRequest {
	if args == nil {
		args = map[string]any{}
	}
	return harness.RawRequest{Method: "tools/call", Params: map[string]any{"name": name, "arguments": args}}
}

func names(t *testing.T, rep *harness.RawReply, list, key string) []string {
	t.Helper()
	res := rep.Result()
	if res == nil {
		t.Fatalf("no result: %v", rep.Final())
	}
	items, _ := res[list].([]any)
	out := []string{}
	for _, it := range items {
		m, _ := it.(map[string]any)
		s, _ := m[key].(string)
		out = append(out, s)
	}
	return out
}

func same(a []string, b ...string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		seen[x]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func text(rep *harness.RawReply) string {
	res := rep.Result()
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	m, _ := content[0].(map[string]any)
	s, _ := m["text"].(string)
	return s
}

func code(rep *harness.RawReply) float64 {
	var c float64
	_, _ = fmt.Sscan(fmt.Sprint(rep.Error()["code"]), &c)
	return c
}

func dump(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

const (
	toolT    = `{"name":"t","description":"orig","inputSchema":{"type":"object","properties":{"q":{"type":"string"}}}}`
	toolKeep = `{"name":"keep","description":"orig","inputSchema":{"type":"object","properties":{}}}`
)

func deny(hook, when string) hooks.Rule {
	return hooks.Rule{Hook: hook, When: when, Then: map[string]string{"decision": "'deny'"}}
}

// ---- tools on the direct route --------------------------------------------------------

func TestDirectListAndCallGoThroughTheRules(t *testing.T) {
	r := newRig(t)
	u := harness.NewUpstream(t, harness.V2026)
	u.Tool(toolT, func(rec harness.Recorded) harness.Reply {
		return harness.Reply{Result: harness.Text("served " + rec.Map()["name"].(string))}
	})
	u.Tool(toolKeep, harness.Static(harness.Reply{Result: harness.Text("kept")}))
	u.Tool(`{"name":"secret","inputSchema":{"type":"object"}}`, harness.Static(harness.Reply{Result: harness.Text("leak")}))
	r.entitle("a", u,
		deny("tools/filter", "name == 'secret'"),
		hooks.Rule{Hook: "tools/modify", When: "upstream.name == 't'", Then: map[string]string{"name": "'renamed'", "description": "'changed'"}})

	list := harness.Raw(t, r.direct("a"), harness.RawRequest{Method: "tools/list"})
	if got := names(t, list, "tools", "name"); !same(got, "renamed", "keep") {
		t.Fatalf("listed %v, want renamed and keep", got)
	}
	if rt := list.Result()["resultType"]; rt != "complete" {
		t.Fatalf("resultType = %v", rt)
	}
	if got := text(harness.Raw(t, r.direct("a"), call("renamed", nil))); got != "served t" {
		t.Fatalf("the renamed tool returned %q; the server must see its own name", got)
	}
	for _, name := range []string{"secret", "t"} {
		rep := harness.Raw(t, r.direct("a"), call(name, nil))
		if code(rep) != -32602 {
			t.Fatalf("calling %s: want unknown tool, got %v", name, rep.Final())
		}
	}
	if n := len(u.Requests("tools/call")); n != 1 {
		t.Fatalf("the upstream saw %d calls, want 1", n)
	}
}

func TestSelectionCutsBeforeTheRules(t *testing.T) {
	r := newRig(t)
	u := harness.NewUpstream(t, harness.V2026)
	u.Tool(toolT, harness.Static(harness.Reply{Result: harness.Text("ok")}))
	u.Tool(toolKeep, harness.Static(harness.Reply{Result: harness.Text("kept")}))
	// A rule that would rename everything: it must never see a tool the
	// entitlement's allow list left out.
	s := r.entitle("a", u, hooks.Rule{Hook: "tools/modify", When: "true", Then: map[string]string{"description": "'seen by rules'"}})
	r.store.mu.Lock()
	s.Selection = store.Selection{store.SelTools: {"keep"}}
	r.store.mu.Unlock()
	if got := names(t, harness.Raw(t, r.direct("a"), harness.RawRequest{Method: "tools/list"}), "tools", "name"); !same(got, "keep") {
		t.Fatalf("listed %v, want only keep", got)
	}
	if rep := harness.Raw(t, r.direct("a"), call("t", nil)); code(rep) != -32602 {
		t.Fatalf("a tool outside the selection was callable: %v", rep.Final())
	}
}

// ---- the mux ---------------------------------------------------------------------------

func TestMuxPrefixesRoutesAndKeepsTheGatewayTools(t *testing.T) {
	r := newRig(t)
	a, b := harness.NewUpstream(t, harness.V2026), harness.NewUpstream(t, harness.V2026)
	a.Tool(toolT, harness.Static(harness.Reply{Result: harness.Text("from a")}))
	b.Tool(toolT, harness.Static(harness.Reply{Result: harness.Text("from b")}))
	r.entitle("a", a, deny("tools/call", "has(args.q) && args.q == 'no'"))
	r.entitle("b", b)

	got := names(t, harness.Raw(t, r.mux(), harness.RawRequest{Method: "tools/list"}), "tools", "name")
	if !same(got, "a__t", "b__t", hooks.InstructionsTool, gateway.RefreshTool) {
		t.Fatalf("mux listed %v", got)
	}
	if got := text(harness.Raw(t, r.mux(), call("b__t", nil))); got != "from b" {
		t.Fatalf("b__t returned %q", got)
	}
	// Models do not always keep a slug's case.
	if got := text(harness.Raw(t, r.mux(), call("A__t", nil))); got != "from a" {
		t.Fatalf("A__t returned %q", got)
	}
	// Server a's rule applies to a only.
	if rep := harness.Raw(t, r.mux(), call("a__t", map[string]any{"q": "no"})); rep.Result()["isError"] != true {
		t.Fatalf("a's deny did not apply: %v", rep.Final())
	}
	if got := text(harness.Raw(t, r.mux(), call("b__t", map[string]any{"q": "no"}))); got != "from b" {
		t.Fatalf("a's rule reached b: %q", got)
	}
	// The gateway's own tools still work.
	if rep := harness.Raw(t, r.mux(), call(gateway.RefreshTool, nil)); rep.Result() == nil {
		t.Fatalf("refresh_services failed: %v", rep.Final())
	}
	if rep := harness.Raw(t, r.mux(), call(hooks.InstructionsTool, map[string]any{"slug": "a"})); rep.Result() == nil {
		t.Fatalf("get_server_instructions failed: %v", rep.Final())
	}
}

func TestMuxReportsAServerItCannotReach(t *testing.T) {
	r := newRig(t)
	a := harness.NewUpstream(t, harness.V2026)
	a.Tool(toolT, harness.Static(harness.Reply{Result: harness.Text("from a")}))
	r.entitle("a", a)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusBadGateway) }))
	t.Cleanup(dead.Close)
	s := &store.Server{ID: uuid.New(), OrgID: "org", Name: "Dead", URL: dead.URL, Enabled: true, Slug: "dead", EntitlementID: uuid.New(), Policies: []byte("[]"), Generation: 1}
	r.store.servers = append(r.store.servers, s)

	rep := harness.Raw(t, r.mux(), harness.RawRequest{Method: "tools/list"})
	if got := names(t, rep, "tools", "name"); !same(got, "a__t", hooks.InstructionsTool, gateway.RefreshTool) {
		t.Fatalf("listed %v", got)
	}
	meta, _ := rep.Result()["_meta"].(map[string]any)
	down, _ := meta[gateway.MetaKeyUnavailable].([]any)
	if len(down) != 1 || !strings.Contains(dump(down), `"server":"dead"`) {
		t.Fatalf("the unreachable server was not reported: %v", rep.Result()["_meta"])
	}
}

// ---- replies ---------------------------------------------------------------------------

func TestResultRulesShapeTheReply(t *testing.T) {
	r := newRig(t)
	u := harness.NewUpstream(t, harness.V2026)
	u.Tool(toolT, harness.Static(harness.Reply{RawResult: `{"content":[{"type":"text","text":"hi"}],"structuredContent":{"keep":1,"internal":2}}`}))
	r.entitle("a", u, hooks.Rule{Hook: "tools/result", When: "has(result.structuredContent)", Then: map[string]string{"structuredContent": "drop(result.structuredContent, ['internal'])"}})
	res := harness.Raw(t, r.direct("a"), call("t", nil)).Result()
	if got := dump(res["structuredContent"]); got != `{"keep":1}` {
		t.Fatalf("structuredContent = %s", got)
	}
	if text(harness.Raw(t, r.direct("a"), call("t", nil))) != "hi" {
		t.Fatalf("the content was lost: %v", res)
	}
}

// The gateway talks to servers through the SDK client. A reply the client
// library cannot read, such as a content block of a type it does not know,
// cannot be passed on. The call must still end, with a JSON-RPC error, and
// must not hang or pass anything of the reply through.
func TestAReplyTheClientLibraryCannotReadEndsTheCallWithAnError(t *testing.T) {
	r := newRig(t)
	u := harness.NewUpstream(t, harness.V2026)
	u.Tool(toolT, harness.Static(harness.Reply{RawResult: `{"content":[{"type":"text","text":"hi"},{"type":"hologram","depth":3}]}`}))
	u.Tool(toolKeep, harness.Static(harness.Reply{Result: harness.Text("kept")}))
	r.entitle("a", u)
	rep := harness.Raw(t, r.direct("a"), call("t", nil))
	if code(rep) != float64(hooks.CodeUpstreamLost) {
		t.Fatalf("want a JSON-RPC error that ends the call, got %v", rep.Final())
	}
	if d := dump(rep.Final()); strings.Contains(d, "hologram") || strings.Contains(d, `"hi"`) {
		t.Fatalf("part of the unreadable reply reached the client: %s", d)
	}
	// The gateway keeps serving.
	if got := text(harness.Raw(t, r.direct("a"), call("keep", nil))); got != "kept" {
		t.Fatalf("the next call returned %q", got)
	}
}

func TestProgressIsRelayedUnderTheClientsTokenAndShapedByRules(t *testing.T) {
	r := newRig(t)
	u := harness.NewUpstream(t, harness.V2026)
	u.Tool(toolT, harness.Static(harness.Reply{Stream: true, Result: harness.Text("done"),
		Progress: []harness.Progress{{Progress: 1, Total: 3, Message: "step 1"}, {Progress: 2, Total: 3, Message: "reading /srv/secret"}, {Progress: 3, Total: 3, Message: "step 3"}}}))
	r.entitle("a", u,
		deny("tools/progress", "progress.message.contains('/srv')"),
		hooks.Rule{Hook: "tools/progress", When: "progress.message.startsWith('step')", Then: map[string]string{"message": "'working: ' + progress.message"}})

	req := call("t", nil)
	req.ProgressToken = "client-token"
	rep := harness.Raw(t, r.direct("a"), req)
	var got []string
	for _, m := range rep.Messages() {
		if m["method"] == "notifications/progress" {
			p, _ := m["params"].(map[string]any)
			if p["progressToken"] != "client-token" {
				t.Fatalf("progress under token %v, want the client's own", p["progressToken"])
			}
			got = append(got, p["message"].(string))
		}
	}
	if strings.Join(got, "|") != "working: step 1|working: step 3" {
		t.Fatalf("progress relayed as %v", got)
	}
	if text(rep) != "done" {
		t.Fatalf("result = %v", rep.Final())
	}
	if tok := u.Last(t, "tools/call").ProgressToken(); tok == nil || tok == "client-token" {
		t.Fatalf("the server was handed token %v; it must get the gateway's own", tok)
	}
	// A client that asks for none gets none, and the server is given no token.
	rep = harness.Raw(t, r.direct("a"), call("t", nil))
	for _, m := range rep.Messages() {
		if m["method"] == "notifications/progress" {
			t.Fatalf("progress nobody asked for was relayed: %v", m)
		}
	}
	if tok := u.Last(t, "tools/call").ProgressToken(); tok != nil {
		t.Fatalf("the server was handed a token nobody asked for: %v", tok)
	}
}

func TestALostReplyTellsTheClientNothingAboutTheServer(t *testing.T) {
	r := newRig(t)
	u := harness.NewUpstream(t, harness.V2026)
	u.Tool(toolT, harness.Static(harness.Reply{Status: 502, Body: "bad gateway body"}))
	u.Tool(toolKeep, harness.Static(harness.Reply{Stream: true, NoFinal: true}))
	r.entitle("a", u)
	for _, name := range []string{"t", "keep"} {
		rep := harness.Raw(t, r.direct("a"), call(name, nil))
		if code(rep) != float64(hooks.CodeUpstreamLost) {
			t.Fatalf("%s: want the lost-reply error, got %v", name, rep.Final())
		}
		for _, leak := range []string{"bad gateway body", "127.0.0.1", "http://", "502"} {
			if strings.Contains(dump(rep.Final()), leak) {
				t.Fatalf("%s: the error names %q: %v", name, leak, rep.Final())
			}
		}
	}
}

// ---- confirmations -----------------------------------------------------------------------

func TestConfirmationRoundTripUsesTheSharedStoreOnce(t *testing.T) {
	r := newRig(t)
	u := harness.NewUpstream(t, harness.V2026)
	u.Tool(`{"name":"delete","inputSchema":{"type":"object","properties":{"id":{"type":"string"}}}}`, func(rec harness.Recorded) harness.Reply {
		return harness.Reply{Result: harness.Text("deleted " + rec.Args()["id"].(string))}
	})
	r.entitle("a", u, hooks.Rule{Hook: "tools/call", When: "!has(input.ok)", Then: map[string]string{"result": "elicit('ok', 'Delete {id}?', {'id': args.id})"}})

	ask := call("delete", map[string]any{"id": "42"})
	ask.Capabilities = elicits
	first := harness.Raw(t, r.direct("a"), ask).Result()
	if first["resultType"] != "input_required" {
		t.Fatalf("want a question, got %v", first)
	}
	state, _ := first["requestState"].(string)
	if !strings.HasPrefix(state, "c_") {
		t.Fatalf("requestState = %q, want an opaque store key", state)
	}
	if len(u.Requests("tools/call")) != 0 {
		t.Fatalf("the call reached the server before it was confirmed")
	}
	if n := r.store.count("confirm"); n != 1 {
		t.Fatalf("%d pending confirmations in the shared store, want 1", n)
	}
	var key string
	for k := range first["inputRequests"].(map[string]any) {
		key = k
	}
	answer := func(content map[string]any) *harness.RawReply {
		req := ask
		req.Params = map[string]any{"name": "delete", "arguments": map[string]any{"id": "42"}, "requestState": state,
			"inputResponses": map[string]any{key: map[string]any{"action": "accept", "content": content}}}
		return harness.Raw(t, r.direct("a"), req)
	}
	if got := text(answer(map[string]any{"confirm": true})); got != "deleted 42" {
		t.Fatalf("the confirmed call returned %q", got)
	}
	if n := r.store.count("confirm"); n != 0 {
		t.Fatalf("the confirmation was not removed when it was used")
	}
	// The same answer again finds nothing: a confirmation works once.
	replay := answer(map[string]any{"confirm": true})
	if replay.Error() == nil && replay.Result()["isError"] != true && replay.Result()["resultType"] != "input_required" {
		t.Fatalf("a used confirmation was accepted again: %v", replay.Final())
	}
	if n := len(u.Requests("tools/call")); n != 1 {
		t.Fatalf("the server saw %d calls, want 1", n)
	}
}

func TestAClientThatCannotBeAskedIsRefusedNotAsked(t *testing.T) {
	r := newRig(t)
	u := harness.NewUpstream(t, harness.V2026)
	u.Tool(toolT, harness.Static(harness.Reply{Result: harness.Text("ran")}))
	r.entitle("a", u, hooks.Rule{Hook: "tools/call", When: "!has(input.ok)", Then: map[string]string{"result": "elicit('ok', 'Sure?')"}})
	rep := harness.Raw(t, r.direct("a"), call("t", nil)) // declares no elicitation
	if res := rep.Result(); res != nil && res["resultType"] == "input_required" {
		t.Fatalf("a client with no elicitation was asked: %v", res)
	}
	if rep.Error() == nil && rep.Result()["isError"] != true {
		t.Fatalf("the call was not refused: %v", rep.Final())
	}
	if len(u.Requests("tools/call")) != 0 {
		t.Fatalf("the call reached the server")
	}
}

// ---- what the server is told ------------------------------------------------------------

func TestTheServerSeesTheIdentityAndMetaTheRulesDecide(t *testing.T) {
	r := newRig(t)
	u := harness.NewUpstream(t, harness.V2026)
	u.Tool(toolT, harness.Static(harness.Reply{Result: harness.Text("ok")}))
	s := r.entitle("a", u)
	req := call("t", nil)
	req.ExactMeta = true
	req.Meta = map[string]any{"trace": "abc",
		"io.modelcontextprotocol/protocolVersion":    gateway.ProtocolVersion,
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "raw-client", "version": "1"},
		"io.modelcontextprotocol/clientCapabilities": elicits}
	harness.Raw(t, r.direct("a"), req)
	got := u.Last(t, "tools/call")
	if _, ok := got.Meta()["trace"]; ok {
		t.Fatalf("the client's _meta reached the server with no rule allowing it: %v", got.Meta())
	}
	if caps := got.Capabilities(); len(caps) != 0 {
		t.Fatalf("the server was told capabilities %v by default", caps)
	}

	pol, _ := json.Marshal([]hooks.Rule{{Hook: "server/connect", When: "true", Then: map[string]string{"meta": "true", "capabilities.elicitation": "true", "clientInfo": "{'name': 'acme', 'version': '7'}"}}})
	r.store.mu.Lock()
	s.Policies, s.Generation = pol, 2
	r.store.mu.Unlock()
	// The rule turns forwarding on, but the gateway's input list bounds what
	// can be forwarded. With the key not on the list, it still does not go.
	harness.Raw(t, r.direct("a"), req)
	if _, ok := u.Last(t, "tools/call").Meta()["trace"]; ok {
		t.Fatalf("a _meta key that is not on the input list was forwarded: %v", u.Last(t, "tools/call").Meta())
	}
	r.gw.SetMetaAllow([]string{"trace"}, nil)
	harness.Raw(t, r.direct("a"), req)
	got = u.Last(t, "tools/call")
	if got.Meta()["trace"] != "abc" {
		t.Fatalf("server/connect meta with the key on the input list did not forward it: %v", got.Meta())
	}
	if _, ok := got.Capabilities()["elicitation"]; !ok {
		t.Fatalf("the server was not told elicitation: %v", got.Capabilities())
	}
	if ci, _ := got.Meta()["io.modelcontextprotocol/clientInfo"].(map[string]any); ci["name"] != "acme" {
		t.Fatalf("clientInfo = %v", got.Meta()["io.modelcontextprotocol/clientInfo"])
	}
}

func TestAnOlderServerIsCalledThroughItsHandshakeWithNoProgress(t *testing.T) {
	r := newRig(t)
	u := harness.NewUpstream(t, harness.V2025)
	u.Tool(toolT, harness.Static(harness.Reply{Result: harness.Text("old ok"), ForceProgress: true, Progress: []harness.Progress{{Progress: 1, Message: "unasked"}}}))
	r.entitle("a", u, hooks.Rule{Hook: "tools/result", When: "true", Then: map[string]string{"content": "[{'type': 'text', 'text': 'shaped ' + result.content[0].text}]"}})
	req := call("t", nil)
	req.ProgressToken = "client-token"
	rep := harness.Raw(t, r.direct("a"), req)
	if got := text(rep); got != "shaped old ok" {
		t.Fatalf("result = %q (%v)", got, rep.Final())
	}
	for _, m := range rep.Messages() {
		if m["method"] == "notifications/progress" {
			t.Fatalf("progress from an older server was relayed: %v", m)
		}
	}
	if len(u.Requests("initialize")) == 0 {
		t.Fatalf("the older server was not reached through its handshake")
	}
}

// ---- rules that cannot be used ------------------------------------------------------------

func TestRulesThatDoNotLoadFailTheEntitlementClosed(t *testing.T) {
	for name, policies := range map[string]string{
		"the retired format":   `[{"id":"r1","action":"deny","kinds":["tool"],"when":"tool.name == 'x'"}]`,
		"a rule that is wrong": `[{"hook":"tools/filter","when":"nope ==","then":{"decision":"'deny'"}}]`,
		"an unknown hook":      `[{"hook":"tools/nope","when":"true","then":{"decision":"'deny'"}}]`,
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			u := harness.NewUpstream(t, harness.V2026)
			u.Tool(toolT, harness.Static(harness.Reply{Result: harness.Text("ran")}))
			s := r.entitle("a", u)
			s.Policies = []byte(policies)
			if got := names(t, harness.Raw(t, r.direct("a"), harness.RawRequest{Method: "tools/list"}), "tools", "name"); len(got) != 0 {
				t.Fatalf("an entitlement with unusable rules listed %v", got)
			}
			if rep := harness.Raw(t, r.direct("a"), call("t", nil)); rep.Error() == nil {
				t.Fatalf("an entitlement with unusable rules ran a call: %v", rep.Final())
			}
			if len(u.Requests("tools/call")) != 0 {
				t.Fatalf("the call reached the server")
			}
		})
	}
}

// ---- prompts, resources and discovery -------------------------------------------------------

func TestPromptsAndResourcesGoThroughTheRules(t *testing.T) {
	r := newRig(t)
	u := harness.NewUpstream(t, harness.V2026)
	u.Prompt(`{"name":"greet"}`, harness.Static(harness.Reply{RawResult: `{"messages":[{"role":"user","content":{"type":"text","text":"hello"}}]}`}))
	u.Prompt(`{"name":"secret"}`, harness.Static(harness.Reply{RawResult: `{"messages":[]}`}))
	u.Resource("docs://a", harness.Static(harness.Reply{Result: harness.Contents("docs://a", "body with token=abc")}))
	u.Resource("docs://hidden", harness.Static(harness.Reply{Result: harness.Contents("docs://hidden", "x")}))
	u.Resource("other://b", harness.Static(harness.Reply{Result: harness.Contents("other://b", "x")}))
	s := r.entitle("a", u,
		deny("prompts/filter", "name == 'secret'"),
		deny("resources/filter", "uri == 'docs://hidden'"),
		hooks.Rule{Hook: "resources/result", When: "result.text.contains('token=')", Then: map[string]string{"text": "result.text.replace('token=abc', 'token=***')"}})
	s.Selection = store.Selection{store.SelResources: {"docs://**"}, store.SelTemplates: {}}

	if got := names(t, harness.Raw(t, r.direct("a"), harness.RawRequest{Method: "prompts/list"}), "prompts", "name"); !same(got, "greet") {
		t.Fatalf("prompts listed %v", got)
	}
	get := harness.Raw(t, r.direct("a"), harness.RawRequest{Method: "prompts/get", Params: map[string]any{"name": "greet"}})
	if !strings.Contains(dump(get.Result()), "hello") {
		t.Fatalf("prompts/get = %v", get.Final())
	}
	if rep := harness.Raw(t, r.direct("a"), harness.RawRequest{Method: "prompts/get", Params: map[string]any{"name": "secret"}}); rep.Error() == nil {
		t.Fatalf("a filtered prompt was served: %v", rep.Final())
	}
	if got := names(t, harness.Raw(t, r.direct("a"), harness.RawRequest{Method: "resources/list"}), "resources", "uri"); !same(got, "docs://a") {
		t.Fatalf("resources listed %v, want docs://a (one hidden by a rule, one outside the selection)", got)
	}
	read := func(uri string) *harness.RawReply {
		return harness.Raw(t, r.direct("a"), harness.RawRequest{Method: "resources/read", Params: map[string]any{"uri": uri}})
	}
	if got := dump(read("docs://a").Result()); !strings.Contains(got, "token=***") || strings.Contains(got, "token=abc") {
		t.Fatalf("the read was not shaped: %s", got)
	}
	if rep := read("docs://hidden"); rep.Error() == nil {
		t.Fatalf("a resource a rule denies was read: %v", rep.Final())
	}
	if rep := read("other://b"); rep.Error() == nil {
		t.Fatalf("a resource outside the entitlement's selection was read: %v", rep.Final())
	}
	if n := len(u.Requests("resources/read")); n != 1 {
		t.Fatalf("the server saw %d reads, want 1", n)
	}
}

func TestDiscoveryIsShapedByTheRules(t *testing.T) {
	r := newRig(t)
	u := harness.NewUpstream(t, harness.V2026)
	u.Instructions = "upstream instructions"
	u.Tool(toolT, harness.Static(harness.Reply{Result: harness.Text("ok")}))
	u.Prompt(`{"name":"greet"}`, harness.Static(harness.Reply{RawResult: `{"messages":[]}`}))
	r.entitle("a", u,
		hooks.Rule{Hook: "server/config", When: "true", Then: map[string]string{"prompts": "false"}},
		hooks.Rule{Hook: "server/modify", When: "true", Then: map[string]string{"instructions": "'gateway instructions'"}})
	res := harness.Raw(t, r.direct("a"), harness.RawRequest{Method: "server/discover"}).Result()
	if res == nil {
		t.Fatal("no discover result")
	}
	caps, _ := res["capabilities"].(map[string]any)
	if _, ok := caps["prompts"]; ok {
		t.Fatalf("prompts are advertised though a rule switched them off: %v", caps)
	}
	if _, ok := caps["tools"]; !ok {
		t.Fatalf("tools are not advertised: %v", caps)
	}
	if res["instructions"] != "gateway instructions" {
		t.Fatalf("instructions = %v", res["instructions"])
	}
}

// ---- search mode -------------------------------------------------------------------------

func TestSearchModeSearchesAndExecutesWhatTheRulesExpose(t *testing.T) {
	r := newRig(t)
	u := harness.NewUpstream(t, harness.V2026)
	u.Tool(`{"name":"zebra_read","description":"zebra quartz reader","inputSchema":{"type":"object"}}`, harness.Static(harness.Reply{Result: harness.Text("read ok")}))
	u.Tool(`{"name":"zebra_admin","description":"zebra quartz admin","inputSchema":{"type":"object"}}`, harness.Static(harness.Reply{Result: harness.Text("admin ran")}))
	r.entitle("a", u, deny("tools/filter", "flags.search && name == 'zebra_admin'"))
	search := r.mux() + "?search=true"

	list := harness.Raw(t, search, harness.RawRequest{Method: "tools/list"})
	if got := names(t, list, "tools", "name"); !same(got, gateway.SearchTool, gateway.ListTool, gateway.DefineTool, gateway.ExecuteTool, gateway.RefreshTool) {
		t.Fatalf("search mode listed %v", got)
	}
	if d := dump(list.Result()); !strings.Contains(d, "- a: Server a") || !strings.Contains(d, "(1 tool)") {
		t.Fatalf("the roster does not describe what is exposed: %s", d)
	}
	found := harness.Raw(t, search, call(gateway.SearchTool, map[string]any{"query": "zebra quartz"}))
	if d := dump(found.Result()); !strings.Contains(d, "a__zebra_read") || strings.Contains(d, "zebra_admin") {
		t.Fatalf("search results: %s", d)
	}
	if got := text(harness.Raw(t, search, call(gateway.ExecuteTool, map[string]any{"name": "a__zebra_read", "arguments": map[string]any{}}))); got != "read ok" {
		t.Fatalf("execute_tool returned %q", got)
	}
	if rep := harness.Raw(t, search, call(gateway.ExecuteTool, map[string]any{"name": "a__zebra_admin", "arguments": map[string]any{}})); rep.Error() == nil && rep.Result()["isError"] != true {
		t.Fatalf("a tool hidden in search mode was executed: %v", rep.Final())
	}
	// The same tool is there on the plain mux: the rule reads the flag.
	if got := text(harness.Raw(t, r.mux(), call("a__zebra_admin", nil))); got != "admin ran" {
		t.Fatalf("outside search mode the tool returned %q", got)
	}
}

// ---- credentials -------------------------------------------------------------------------

func TestACredentialTheOperatorMustFixIsSaidPlainly(t *testing.T) {
	r := newRig(t)
	u := harness.NewUpstream(t, harness.V2026)
	u.Tool(toolT, harness.Static(harness.Reply{Result: harness.Text("ok")}))
	s := r.entitle("a", u)
	harness.Raw(t, r.direct("a"), harness.RawRequest{Method: "tools/list"}) // warm the catalog
	r.store.mu.Lock()
	r.store.creds[s.ID] = &store.Credential{ServerID: s.ID, Kind: store.AuthOAuth, State: store.StateBroken, StateReason: "invalid_grant"}
	r.store.mu.Unlock()
	rep := harness.Raw(t, r.direct("a"), call("t", nil))
	if code(rep) != -32011 {
		t.Fatalf("want the broken-credential error, got %v", rep.Final())
	}
}

// ---- the Go SDK client, end to end ---------------------------------------------------------

func TestGoSDKClientThroughTheGateway(t *testing.T) {
	r := newRig(t)
	u := harness.NewUpstream(t, harness.V2026)
	u.Tool(toolT, harness.Static(harness.Reply{Stream: true, Result: harness.Text("done"), Progress: []harness.Progress{{Progress: 1, Total: 1, Message: "step 1"}}}))
	u.Tool(`{"name":"delete","inputSchema":{"type":"object","properties":{"id":{"type":"string"}}}}`, harness.Static(harness.Reply{Result: harness.Text("deleted")}))
	r.entitle("a", u,
		hooks.Rule{Hook: "tools/modify", When: "upstream.name == 't'", Then: map[string]string{"name": "'work'"}},
		hooks.Rule{Hook: "tools/call", When: "name == 'delete' && !has(input.ok)", Then: map[string]string{"result": "elicit('ok', 'Delete?')"}})

	var (
		mu       sync.Mutex
		progress []string
		asked    []string
	)
	client := mcp.NewClient(&mcp.Implementation{Name: "sdk-test", Version: "1"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			mu.Lock()
			progress = append(progress, req.Params.Message)
			mu.Unlock()
		},
		ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			mu.Lock()
			asked = append(asked, req.Params.Message)
			mu.Unlock()
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, nil
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: r.direct("a"), DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()
	if v := sess.InitializeResult().ProtocolVersion; v != gateway.ProtocolVersion {
		t.Fatalf("negotiated %s", v)
	}
	lt, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var got []string
	for _, tl := range lt.Tools {
		got = append(got, tl.Name)
	}
	if !same(got, "work", "delete") {
		t.Fatalf("the SDK client listed %v", got)
	}
	params := &mcp.CallToolParams{Name: "work", Arguments: map[string]any{}}
	params.SetProgressToken("sdk-token")
	res, err := sess.CallTool(ctx, params)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); !ok || tc.Text != "done" {
		t.Fatalf("call result = %v", dump(res))
	}
	mu.Lock()
	if strings.Join(progress, "|") != "step 1" {
		t.Fatalf("the SDK client received progress %v", progress)
	}
	mu.Unlock()
	res, err = sess.CallTool(ctx, &mcp.CallToolParams{Name: "delete", Arguments: map[string]any{"id": "1"}})
	if err != nil {
		t.Fatalf("confirmed call: %v", err)
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); !ok || tc.Text != "deleted" {
		t.Fatalf("confirmed call result = %v", dump(res))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 1 || asked[0] != "Delete?" {
		t.Fatalf("the SDK client was asked %v", asked)
	}
}

// ---- the official client SDKs, end to end ------------------------------------------------

// Each official client SDK, as its own library makes requests, through the
// real front door: listing, a call with progress, a confirmation the SDK's
// own handler answers, a refusal, a prompt and a read.
func TestOfficialSDKClientsThroughTheGateway(t *testing.T) {
	type maker struct {
		name string
		make func() (clients.Driver, error)
	}
	for _, mk := range []maker{{"go", clients.NewGo}, {"python", clients.NewPython}, {"typescript", clients.NewTypeScript}} {
		t.Run(mk.name, func(t *testing.T) {
			d, err := mk.make()
			if err != nil {
				if errors.Is(err, clients.ErrNotInstalled) && os.Getenv("MATRIX_CLIENTS_REQUIRE") == "" {
					t.Skipf("%s SDK client: %v", mk.name, err)
				}
				t.Fatalf("%s SDK client: %v", mk.name, err)
			}
			defer d.Close()

			r := newRig(t)
			u := harness.NewUpstream(t, harness.V2026)
			u.Tool(toolT, harness.Static(harness.Reply{Stream: true, Result: harness.Text("done"), Progress: []harness.Progress{{Progress: 1, Total: 1, Message: "step 1"}}}))
			u.Tool(`{"name":"delete","inputSchema":{"type":"object","properties":{"id":{"type":"string"}}}}`, harness.Static(harness.Reply{Result: harness.Text("deleted")}))
			u.Tool(`{"name":"secret","inputSchema":{"type":"object","properties":{}}}`, harness.Static(harness.Reply{Result: harness.Text("leak")}))
			u.Prompt(`{"name":"greet"}`, harness.Static(harness.Reply{RawResult: `{"messages":[{"role":"user","content":{"type":"text","text":"hello"}}]}`}))
			u.Resource("docs://a", harness.Static(harness.Reply{Result: harness.Contents("docs://a", "body token=abc")}))
			r.entitle("a", u,
				deny("tools/filter", "name == 'secret'"),
				hooks.Rule{Hook: "tools/modify", When: "upstream.name == 't'", Then: map[string]string{"name": "'work'"}},
				hooks.Rule{Hook: "tools/progress", When: "true", Then: map[string]string{"message": "'working: ' + progress.message"}},
				hooks.Rule{Hook: "tools/call", When: "name == 'delete' && !has(input.ok)", Then: map[string]string{"result": "elicit('ok', 'Delete?')"}},
				hooks.Rule{Hook: "resources/result", When: "true", Then: map[string]string{"text": "result.text.replace('token=abc', 'token=***')"}})

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			conn, err := d.Connect(ctx, clients.ConnectOptions{URL: r.direct("a"), Capabilities: elicits})
			if err != nil || conn.Error != nil {
				t.Fatalf("connect: %v %v", err, conn.Error)
			}
			if v := conn.SDK.ProtocolVersion; v != gateway.ProtocolVersion {
				t.Fatalf("%s %s negotiated %q", conn.SDK.Name, conn.SDK.Version, v)
			}
			do := func(o clients.CallOptions) clients.Reply {
				t.Helper()
				rep, err := d.Call(ctx, o)
				if err != nil {
					t.Fatalf("%s: the driver failed: %v", o.Method, err)
				}
				return rep
			}
			list := do(clients.CallOptions{Method: "tools/list"})
			var got []string
			for _, it := range list.Result["tools"].([]any) {
				got = append(got, it.(map[string]any)["name"].(string))
			}
			if !same(got, "work", "delete") {
				t.Fatalf("listed %v", got)
			}
			work := do(clients.CallOptions{Method: "tools/call", Params: map[string]any{"name": "work", "arguments": map[string]any{}}, Progress: true})
			if work.Error != nil || !strings.Contains(dump(work.Result), `"done"`) {
				t.Fatalf("call: %v %s", work.Error, dump(work.Result))
			}
			if len(work.Progress) != 1 || work.Progress[0]["message"] != "working: step 1" {
				t.Fatalf("progress: %s", dump(work.Progress))
			}
			del := do(clients.CallOptions{Method: "tools/call", Params: map[string]any{"name": "delete", "arguments": map[string]any{"id": "1"}},
				Elicit: []clients.Answer{{Action: "accept", Content: map[string]any{"confirm": true}}}})
			if del.Error != nil || !strings.Contains(dump(del.Result), `"deleted"`) {
				t.Fatalf("confirmed call: %v %s", del.Error, dump(del.Result))
			}
			if len(del.Elicitations) != 1 || !strings.Contains(dump(del.Elicitations), "Delete?") {
				t.Fatalf("the SDK was asked: %s", dump(del.Elicitations))
			}
			declined := do(clients.CallOptions{Method: "tools/call", Params: map[string]any{"name": "delete", "arguments": map[string]any{"id": "2"}},
				Elicit: []clients.Answer{{Action: "decline"}}})
			if declined.Error == nil && declined.Result["isError"] != true {
				t.Fatalf("a declined confirmation ran the call: %s", dump(declined.Result))
			}
			hidden := do(clients.CallOptions{Method: "tools/call", Params: map[string]any{"name": "secret", "arguments": map[string]any{}}})
			if hidden.Error == nil {
				t.Fatalf("a filtered tool was callable: %s", dump(hidden.Result))
			}
			if hidden.Error.Kind == "rpc" && hidden.Error.RPCCode() != -32602 {
				t.Fatalf("a filtered tool: %v", hidden.Error)
			}
			prompt := do(clients.CallOptions{Method: "prompts/get", Params: map[string]any{"name": "greet"}})
			if prompt.Error != nil || !strings.Contains(dump(prompt.Result), "hello") {
				t.Fatalf("prompts/get: %v %s", prompt.Error, dump(prompt.Result))
			}
			read := do(clients.CallOptions{Method: "resources/read", Params: map[string]any{"uri": "docs://a"}})
			if read.Error != nil || !strings.Contains(dump(read.Result), "token=***") {
				t.Fatalf("resources/read: %v %s", read.Error, dump(read.Result))
			}
			if n := len(u.Requests("tools/call")); n != 2 {
				t.Fatalf("the server saw %d calls, want 2 (work, and the confirmed delete)", n)
			}
		})
	}
}

// A question the server itself asks reaches the client, and the client's
// answer reaches the server.
func TestAServersOwnQuestionMakesTheRoundTrip(t *testing.T) {
	r := newRig(t)
	u := harness.NewUpstream(t, harness.V2026)
	u.Tool(toolT, func(rec harness.Recorded) harness.Reply {
		if _, answered := rec.Map()["inputResponses"]; answered {
			return harness.Reply{Result: harness.Text("answered " + dump(rec.Map()["inputResponses"]))}
		}
		return harness.Reply{RawResult: `{"resultType":"input_required","requestState":"S1","inputRequests":{"p":{"method":"elicitation/create","params":{"mode":"form","message":"Which project?","requestedSchema":{"type":"object","properties":{"project":{"type":"string"}}}}}}}`}
	})
	r.entitle("a", u,
		hooks.Rule{Hook: "server/connect", When: "true", Then: map[string]string{"capabilities.elicitation": "true"}},
		hooks.Rule{Hook: "tools/input", When: "true", Then: map[string]string{"message": "'Gateway asks: ' + request.message"}})
	ask := call("t", nil)
	ask.Capabilities = elicits
	first := harness.Raw(t, r.direct("a"), ask).Result()
	if first["resultType"] != "input_required" {
		t.Fatalf("the server's question did not reach the client: %v", first)
	}
	if !strings.Contains(dump(first["inputRequests"]), "Gateway asks: Which project?") {
		t.Fatalf("the question was not reworded: %s", dump(first["inputRequests"]))
	}
	state, _ := first["requestState"].(string)
	if state == "" || state == "S1" {
		t.Fatalf("the server's own state reached the client unwrapped: %q", state)
	}
	var key string
	for k := range first["inputRequests"].(map[string]any) {
		key = k
	}
	ask.Params = map[string]any{"name": "t", "arguments": map[string]any{}, "requestState": state,
		"inputResponses": map[string]any{key: map[string]any{"action": "accept", "content": map[string]any{"project": "apollo"}}}}
	second := harness.Raw(t, r.direct("a"), ask)
	if got := text(second); !strings.Contains(got, "apollo") {
		t.Fatalf("the answer did not reach the server: %v", second.Final())
	}
	if got := u.Last(t, "tools/call").Map()["requestState"]; got != "S1" {
		t.Fatalf("the server got requestState %v back, want its own S1", got)
	}
}

// A server's _meta passes only where its key is on the gateway's output list:
// on the items of its catalog and on its results alike. With no list,
// nothing of it passes, including the name and version an SDK server stamps
// on every result.
func TestUpstreamMetaPassesOnlyThroughTheAllowlist(t *testing.T) {
	const tool = `{"name":"t","inputSchema":{"type":"object"},"_meta":{"ui":{"resourceUri":"ui://app"},"vendor/tier":"gold"}}`
	const result = `{"content":[{"type":"text","text":"ok"}],"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"upstream","version":"9"},"vendor/trace":"t-1"}}`
	check := func(t *testing.T, allow []string, wantTool, wantResult string) {
		t.Helper()
		r := newRig(t)
		r.gw.SetMetaAllow(nil, allow)
		u := harness.NewUpstream(t, harness.V2026)
		u.Tool(tool, harness.Static(harness.Reply{RawResult: result}))
		r.entitle("a", u)
		list := harness.Raw(t, r.direct("a"), harness.RawRequest{Method: "tools/list"}).Result()
		item := list["tools"].([]any)[0].(map[string]any)
		if got := dump(item["_meta"]); got != wantTool {
			t.Fatalf("the listed tool carries _meta %s, want %s", got, wantTool)
		}
		res := harness.Raw(t, r.direct("a"), call("t", nil)).Result()
		if got := dump(res["_meta"]); got != wantResult {
			t.Fatalf("the result carries _meta %s, want %s", got, wantResult)
		}
		if text(harness.Raw(t, r.direct("a"), call("t", nil))) != "ok" {
			t.Fatalf("the result's content was lost: %v", res)
		}
	}
	// Every result names the gateway, never the server behind it.
	const us = `"io.modelcontextprotocol/serverInfo":{"name":"bifrost","version":"test"}`
	t.Run("no list: nothing of the server's passes", func(t *testing.T) { check(t, nil, "null", "{"+us+"}") })
	t.Run("a list: only its keys pass", func(t *testing.T) {
		check(t, []string{"vendor/tier", "vendor/trace"}, `{"vendor/tier":"gold"}`, "{"+us+`,"vendor/trace":"t-1"}`)
	})
	t.Run("the server's identity cannot be put on the list", func(t *testing.T) {
		check(t, []string{"io.modelcontextprotocol/serverInfo"}, "null", "{"+us+"}")
	})
}

// Rules written as text are stored with the text beside the rules the
// engine runs. Both shapes of stored rules read back, and the retired
// format is still refused inside the new shape.
func TestStoredRulesKeepTheirText(t *testing.T) {
	rules := []hooks.Rule{{Hook: "tools/filter", When: "true", Then: map[string]string{"decision": "'deny'"}}}
	bare, _ := json.Marshal(rules)
	withText, _ := json.Marshal(gateway.StoredRules{Text: map[string]string{"tools/list.post": "when true deny"}, Rules: rules})
	for name, raw := range map[string][]byte{"a bare list": bare, "text and rules": withText} {
		rs, text, err := gateway.ParseStored(raw)
		if err != nil || len(rs) != 1 || rs[0].Hook != "tools/filter" {
			t.Fatalf("%s: rules %+v, err %v", name, rs, err)
		}
		if name == "text and rules" && text["tools/list.post"] != "when true deny" {
			t.Fatalf("the text was lost: %v", text)
		}
	}
	if _, _, err := gateway.ParseStored([]byte(`{"text":{},"rules":[{"allow":["x"]}]}`)); err == nil {
		t.Fatal("rules in the retired format were accepted")
	}
	if rs, text, err := gateway.ParseStored([]byte(`{"text":{"tools/call.pre":"# nothing yet"},"rules":[]}`)); err != nil || len(rs) != 0 || len(text) != 1 {
		t.Fatalf("text with no rules: %v %v %v", rs, text, err)
	}
}

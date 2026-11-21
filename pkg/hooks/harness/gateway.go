package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
)

// Meta keys of the 2026-07-28 per-request triple.
const (
	metaVersion = "io.modelcontextprotocol/protocolVersion"
	metaClient  = "io.modelcontextprotocol/clientInfo"
	metaCaps    = "io.modelcontextprotocol/clientCapabilities"
)

// Record is what the engine decided for one client request.
type Record struct {
	Route   string
	Method  string
	Outcome hooks.Outcome  // the request side
	Reply   *hooks.Outcome // the reply side, when the request was forwarded
	// Headers and Meta are the client's request as the gateway received it:
	// the HTTP headers, with lower-case names, and the request's _meta.
	Headers map[string]string
	Meta    map[string]any
	// Aborted is set when the client's stream was closed without a final
	// message, because the upstream's stream ended without one.
	Aborted bool
}

// Hits returns the hits of both sides.
func (r Record) Hits() []hooks.Hit {
	out := append([]hooks.Hit(nil), r.Outcome.Hits...)
	if r.Reply != nil {
		out = append(out, r.Reply.Hits...)
	}
	return out
}

// Issues returns the issues of both sides.
func (r Record) Issues() []string {
	out := append([]string(nil), r.Outcome.Issues...)
	if r.Reply != nil {
		out = append(out, r.Reply.Issues...)
	}
	return out
}

// JSON is the record's outcomes as JSON, for assertions on fields this
// package does not name.
func (r Record) JSON() string {
	b, _ := json.Marshal(map[string]any{"outcome": r.Outcome, "reply": r.Reply})
	return string(b)
}

type backend struct {
	slug    string
	url     string
	version string // negotiated with the upstream: V2026, an older one, or ""
	server  *hooks.Server
}

// Gateway is a thin test front door over the rules engine. It is not a
// production gateway.
type Gateway struct {
	Engine *hooks.Engine
	// Token, when set, is the bearer token every request must carry.
	Token string
	// Caller is the caller identity handed to the engine.
	Caller map[string]any
	// KeepAlive is how often an open stream gets an SSE comment line.
	KeepAlive time.Duration
	// MetaAllow lists the upstream _meta keys kept when a catalog is
	// fetched. The default keeps none.
	MetaAllow []string
	// TestHooks turns on what the scenario bridge needs and no real gateway
	// would offer: a per-request caller and clock in the query string
	// (?caller=<json>&now=<RFC 3339>), the /route/<name> endpoint, which runs
	// every registered server under any route name, and the harness/connect
	// and harness/config methods, which show server.session and
	// server.settings for one server.
	TestHooks bool
	// DiscoverVersion is the gateway version server/discover reports.
	DiscoverVersion string
	// Problems and Warnings are the rule compile problems of the registered
	// servers, by slug. They never reach a client.
	Problems map[string]any
	Warnings map[string]any

	srv *httptest.Server
	hc  *http.Client
	ad  adapter

	mu       sync.RWMutex
	backends map[string]*backend
	order    []string // slugs in registration order
	records  []Record
	notes    []string

	escaped atomic.Int64
	seq     atomic.Int64
}

// NewGateway starts a gateway. It stops when the test ends.
func NewGateway(t testing.TB) *Gateway {
	e := &hooks.Engine{Version: "harness"}
	g := &Gateway{
		Engine:    e,
		Caller:    map[string]any{"userId": "u1", "email": "u1@example.com"},
		KeepAlive: 15 * time.Second,
		hc:        &http.Client{},
		ad:        adapter{e: e},
		backends:  map[string]*backend{},
	}
	g.srv = httptest.NewServer(g)
	t.Cleanup(func() {
		g.srv.CloseClientConnections()
		g.srv.Close()
	})
	return g
}

// SetEngine replaces the engine, for a test that needs its own limits,
// signing key or state store.
func (g *Gateway) SetEngine(e *hooks.Engine) {
	g.Engine = e
	g.ad.e = e
}

// URL is the gateway's base address.
func (g *Gateway) URL() string { return g.srv.URL }

// Register adds a server whose engine record the test built itself. version
// is what the upstream speaks: V2026, or anything else for the older
// handshake.
func (g *Gateway) Register(s *hooks.Server, url, version string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.backends[s.Slug]; !ok {
		g.order = append(g.order, s.Slug)
	}
	g.backends[s.Slug] = &backend{slug: s.Slug, url: url, version: version, server: s}
}

// FetchCatalog reads an upstream's listings the way AddServer does, without
// registering anything.
func (g *Gateway) FetchCatalog(ctx context.Context, url, version string) (hooks.Catalog, error) {
	cat := map[string]json.RawMessage{}
	for method, key := range map[string]string{"tools/list": "tools", "prompts/list": "prompts", "resources/list": "resources", "resources/templates/list": "resourceTemplates"} {
		params := map[string]any{}
		if version == V2026 {
			params["_meta"] = g.triple(nil)
		}
		res, err := g.plain(ctx, url, version, method, params)
		if err != nil {
			return hooks.Catalog{}, fmt.Errorf("%s: %w", method, err)
		}
		var m map[string]json.RawMessage
		_ = json.Unmarshal(res, &m)
		if raw, ok := m[key]; ok {
			cat[key] = raw
		}
	}
	raw, _ := json.Marshal(cat)
	var catalog hooks.Catalog
	err := json.Unmarshal(raw, &catalog)
	return catalog, err
}

// Direct is the 1:1 endpoint for a slug.
func (g *Gateway) Direct(slug string) string { return g.srv.URL + "/direct/" + slug }

// Mux is the mux endpoint.
func (g *Gateway) Mux() string { return g.srv.URL + "/mux" }

// AddServer connects to an upstream, fetches its catalog and registers it
// under a slug with the given rules.
func (g *Gateway) AddServer(t testing.TB, slug string, u *Upstream, rules ...hooks.Rule) *hooks.Server {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b := &backend{slug: slug, url: u.URL()}
	info := hooks.ServerInfo{URL: u.URL()}

	// Probe: 2026-07-28 discovery first, then the older handshake.
	if res, err := g.plain(ctx, b.url, V2026, "server/discover", map[string]any{"_meta": g.triple(nil)}); err == nil {
		var d struct {
			SupportedVersions []string       `json:"supportedVersions"`
			Instructions      string         `json:"instructions"`
			Meta              map[string]any `json:"_meta"`
		}
		_ = json.Unmarshal(res, &d)
		for _, v := range d.SupportedVersions {
			if v == V2026 {
				b.version = V2026
			}
		}
		info.Instructions = d.Instructions
		if si, ok := d.Meta["io.modelcontextprotocol/serverInfo"].(map[string]any); ok {
			info.Name, _ = si["name"].(string)
			info.Version, _ = si["version"].(string)
		}
	}
	if b.version == "" {
		res, err := g.initialize(ctx, b.url, nil)
		if err != nil {
			t.Fatalf("harness: upstream %s answers neither discovery nor initialize: %v", slug, err)
		}
		var d struct {
			ProtocolVersion string `json:"protocolVersion"`
			Instructions    string `json:"instructions"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		}
		_ = json.Unmarshal(res, &d)
		b.version = d.ProtocolVersion
		info.Instructions, info.Name, info.Version = d.Instructions, d.ServerInfo.Name, d.ServerInfo.Version
	}
	info.ProtocolVersion = b.version

	// Fetch the catalog and reduce _meta to the allowlist, before any rule.
	cat := map[string]json.RawMessage{}
	for method, key := range map[string]string{"tools/list": "tools", "prompts/list": "prompts", "resources/list": "resources", "resources/templates/list": "resourceTemplates"} {
		params := map[string]any{}
		if b.version == V2026 {
			params["_meta"] = g.triple(nil)
		}
		res, err := g.plain(ctx, b.url, b.version, method, params)
		if err != nil {
			t.Fatalf("harness: %s on upstream %s: %v", method, slug, err)
		}
		var m map[string]json.RawMessage
		_ = json.Unmarshal(res, &m)
		if raw, ok := m[key]; ok {
			cat[key] = raw
		}
	}
	raw, _ := json.Marshal(cat)
	var catalog hooks.Catalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatalf("harness: catalog of upstream %s: %v", slug, err)
	}
	catalog = catalog.StripMeta(g.MetaAllow)

	b.server = &hooks.Server{Slug: slug, Info: info, Generation: "1", Catalog: catalog}
	b.server.Rules = compile(t, rules)
	g.mu.Lock()
	if _, ok := g.backends[slug]; !ok {
		g.order = append(g.order, slug)
	}
	g.backends[slug] = b
	g.mu.Unlock()
	u.Forget() // tests assert on what follows registration
	return b.server
}

func compile(t testing.TB, rules []hooks.Rule) *hooks.Set {
	t.Helper()
	set, probs := hooks.Compile(rules)
	if probs != nil {
		t.Fatalf("harness: rules do not compile: %v", probs)
	}
	return set
}

// SetRules replaces a server's rules, as a policy change would.
func (g *Gateway) SetRules(t testing.TB, slug string, rules ...hooks.Rule) {
	t.Helper()
	set := compile(t, rules)
	g.mu.Lock()
	defer g.mu.Unlock()
	b := g.backends[slug]
	cp := *b.server
	cp.Rules = set
	cp.Generation += "+"
	b.server = &cp
}

// Edit changes a server's engine record in place, for fault injection.
func (g *Gateway) Edit(slug string, f func(*hooks.Server)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	b := g.backends[slug]
	cp := *b.server
	f(&cp)
	b.server = &cp
}

// Records returns what the engine decided for each request so far.
func (g *Gateway) Records() []Record {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return append([]Record(nil), g.records...)
}

// LastRecord returns the most recent record for a method.
func (g *Gateway) LastRecord(t testing.TB, method string) Record {
	t.Helper()
	rs := g.Records()
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i].Method == method {
			return rs[i]
		}
	}
	t.Fatalf("gateway recorded no %s request", method)
	return Record{}
}

// Switches returns server.settings for a server as the engine sees it on the
// 1:1 route.
func (g *Gateway) Switches(slug string) map[string]bool {
	b := g.backend(slug)
	r := hooks.Request{Route: "direct", Caller: g.Caller, Client: map[string]any{"capabilities": map[string]any{}}, Now: time.Now(), Servers: []*hooks.Server{b.server}}
	sw, _ := g.ad.switches(context.Background(), r, b.server)
	return sw
}

// Notifications returns the methods of the notifications clients posted.
func (g *Gateway) Notifications() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return append([]string(nil), g.notes...)
}

// EscapedPanics counts panics that left the engine and were caught by the
// gateway's last-resort handler.
func (g *Gateway) EscapedPanics() int { return int(g.escaped.Load()) }

func (g *Gateway) record(r Record) {
	g.mu.Lock()
	g.records = append(g.records, r)
	g.mu.Unlock()
}

func (g *Gateway) backend(slug string) *backend {
	g.mu.RLock()
	defer g.mu.RUnlock()
	b := g.backends[slug]
	if b == nil {
		return nil
	}
	cp := *b
	return &cp
}

// ---- the front door -------------------------------------------------------------------

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// ServeHTTP is the 2026-07-28 Streamable HTTP front door. Anything refused
// here gets a plain HTTP status. Every accepted request is answered with an
// SSE stream.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route, servers, ok := g.route(r)
	if !ok {
		http.Error(w, "unknown route", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if g.Token != "" && r.Header.Get("Authorization") != "Bearer "+g.Token {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		http.Error(w, "unreadable body", http.StatusBadRequest)
		return
	}
	var msg rpcMessage
	if err := json.Unmarshal(body, &msg); err != nil || msg.JSONRPC != "2.0" {
		http.Error(w, "malformed JSON-RPC message", http.StatusBadRequest)
		return
	}
	if msg.Method == "" || len(msg.ID) == 0 || string(msg.ID) == "null" {
		g.mu.Lock()
		g.notes = append(g.notes, msg.Method)
		g.mu.Unlock()
		w.WriteHeader(http.StatusAccepted) // a notification or a response
		return
	}

	st := openStream(w, g.KeepAlive)
	defer st.close()
	defer func() {
		// Last resort. The engine should fail closed by itself; whatever
		// still escapes must not take the gateway down.
		if p := recover(); p != nil {
			g.escaped.Add(1)
			st.final(msg.ID, hooks.Outcome{Error: &hooks.RPCError{Code: -32603, Message: "The request could not be completed."}})
		}
	}()
	ov := g.overrides(r)
	ov.headers = map[string]string{}
	for k, v := range r.Header {
		ov.headers[strings.ToLower(k)] = strings.Join(v, ", ")
	}
	g.handle(r.Context(), st, route, servers, msg, ov)
}

// overrides is the per-request caller and clock of the test hooks.
type overrides struct {
	headers   map[string]string
	caller    map[string]any
	hasCaller bool
	now       time.Time
	// flags are the request's switches, read from the query string as a
	// real gateway would: ?search=true, ?cli=true. Not a test hook.
	flags map[string]bool
}

func (g *Gateway) overrides(r *http.Request) overrides {
	var o overrides
	q := r.URL.Query()
	for _, name := range hooks.Flags {
		// A client may set only the flags meant for clients. The others,
		// such as cli, are the gateway's to set; here only a test can.
		if !hooks.ClientMaySet(name) && !g.TestHooks {
			continue
		}
		if q.Get(name) == "true" {
			if o.flags == nil {
				o.flags = map[string]bool{}
			}
			o.flags[name] = true
		}
	}
	if !g.TestHooks {
		return o
	}
	if q.Has("caller") {
		o.hasCaller = true
		dec := json.NewDecoder(strings.NewReader(q.Get("caller")))
		dec.UseNumber()
		_ = dec.Decode(&o.caller)
	}
	if t, err := time.Parse(time.RFC3339Nano, q.Get("now")); err == nil {
		o.now = t
	}
	return o
}

func (g *Gateway) route(r *http.Request) (string, []*hooks.Server, bool) {
	path := r.URL.Path
	g.mu.RLock()
	defer g.mu.RUnlock()
	// Every server on the route, in registration order, narrowed by ?slugs=.
	all := func() []*hooks.Server {
		var out []*hooks.Server
		if want := r.URL.Query().Get("slugs"); want != "" {
			for _, slug := range strings.Split(want, ",") {
				if b := g.backends[slug]; b != nil {
					out = append(out, b.server)
				}
			}
			return out
		}
		for _, slug := range g.order {
			out = append(out, g.backends[slug].server)
		}
		return out
	}
	switch {
	case path == "/mux":
		return "mux", all(), true
	case strings.HasPrefix(path, "/direct/"):
		if b := g.backends[strings.TrimPrefix(path, "/direct/")]; b != nil {
			return "direct", []*hooks.Server{b.server}, true
		}
	case g.TestHooks && strings.HasPrefix(path, "/route/"):
		return strings.TrimPrefix(path, "/route/"), all(), true
	}
	return "", nil, false
}

// clientOf builds the engine's view of the client from the request's _meta:
// the declared identity, plus the declared capabilities when there are any.
func clientOf(meta map[string]any) map[string]any {
	c := map[string]any{}
	if info, ok := meta[metaClient].(map[string]any); ok {
		c["clientInfo"] = info
	}
	if caps, ok := meta[metaCaps]; ok {
		c["capabilities"] = caps
	}
	return c
}

func (g *Gateway) handle(ctx context.Context, st *stream, route string, servers []*hooks.Server, msg rpcMessage, ov overrides) {
	var env struct {
		Meta map[string]any `json:"_meta"`
		Name string         `json:"name"`
	}
	dec := json.NewDecoder(bytes.NewReader(msg.Params))
	dec.UseNumber()
	_ = dec.Decode(&env)
	req := hooks.Request{Route: route, Flags: ov.flags, Caller: g.Caller, Client: clientOf(env.Meta), Now: time.Now(), Servers: servers}
	// Search mode is a flag on either route.
	searching := ov.flags[hooks.FlagSearch]
	if ov.hasCaller {
		req.Caller = ov.caller
	}
	if !ov.now.IsZero() {
		req.Now = ov.now
	}
	rec := Record{Route: route, Method: msg.Method, Headers: ov.headers, Meta: env.Meta}

	own := func(out hooks.Outcome) {
		rec.Outcome = out
		g.record(rec)
		st.final(msg.ID, out)
	}

	if msg.Method == "server/discover" {
		version := g.DiscoverVersion
		if version == "" {
			version = "harness"
		}
		own(g.ad.discover(ctx, req, version))
		return
	}
	if g.TestHooks && (msg.Method == "harness/connect" || msg.Method == "harness/config") {
		own(g.serverHook(ctx, req, msg))
		return
	}
	if searching && msg.Method == "tools/list" {
		own(hooks.Outcome{Result: map[string]any{"tools": searchTools(), "ttlMs": 0, "cacheScope": "private", "resultType": "complete"}})
		return
	}
	if out, ok := g.ad.list(ctx, req, msg.Method); ok {
		own(out)
		return
	}

	var b begun
	switch {
	case msg.Method == "tools/call" && route == "mux" && !searching && env.Name == hooks.InstructionsTool:
		var p mcp.CallToolParamsRaw
		_ = json.Unmarshal(msg.Params, &p)
		own(g.ad.instructions(ctx, req, p.Arguments))
		return
	case msg.Method == "tools/call" && searching:
		var p mcp.CallToolParamsRaw
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			own(hooks.Outcome{Error: invalid(err)})
			return
		}
		sb, ok := g.ad.search(ctx, req, &p)
		if !ok {
			own(hooks.Outcome{Error: &hooks.RPCError{Code: hooks.CodeUnknownTool, Message: "unknown tool " + p.Name}})
			return
		}
		b = sb
	default:
		fb, rerr := g.ad.begin(ctx, req, msg.Method, msg.Params)
		if rerr != nil {
			own(hooks.Outcome{Error: rerr})
			return
		}
		b = fb
	}
	rec.Outcome = b.Outcome
	if b.Outcome.Forward == nil {
		g.record(rec)
		st.final(msg.ID, b.Outcome)
		return
	}
	g.forward(ctx, st, req, msg, env.Meta, b, rec)
}

// serverHook answers the two test-hook methods. harness/config returns the
// server.settings switches. harness/connect returns the server.session
// decision and also reaches the upstream with that identity, so a test can
// see what the server is told.
func (g *Gateway) serverHook(ctx context.Context, req hooks.Request, msg rpcMessage) hooks.Outcome {
	var p struct {
		Slug string `json:"slug"`
	}
	_ = json.Unmarshal(msg.Params, &p)
	var s *hooks.Server
	for _, x := range req.Servers {
		if x.Slug == p.Slug {
			s = x
		}
	}
	if s == nil && len(req.Servers) == 1 {
		s = req.Servers[0]
	}
	if s == nil {
		return hooks.Outcome{Error: &hooks.RPCError{Code: hooks.CodeInvalidParams, Message: "unknown slug"}}
	}
	if msg.Method == "harness/config" {
		sw, hits := g.ad.switches(ctx, req, s)
		return hooks.Outcome{Result: sw, Hits: hits}
	}
	version := g.DiscoverVersion
	if version == "" {
		version = "harness"
	}
	ci, hits := g.ad.connect(ctx, req, s, version)
	if be := g.backend(s.Slug); be != nil {
		if be.version == V2026 {
			_, _ = g.plain(ctx, be.url, V2026, "server/discover", map[string]any{"_meta": g.triple(&ci)})
		} else {
			_, _ = g.initialize(ctx, be.url, &ci)
		}
	}
	return hooks.Outcome{Result: ci, Hits: hits}
}

var (
	errNoResponse = errors.New("the upstream's stream ended without a response")
	errMalformed  = errors.New("the upstream sent a malformed reply")
)

// forward sends the engine's forward upstream and pumps the reply back.
func (g *Gateway) forward(ctx context.Context, st *stream, req hooks.Request, msg rpcMessage, meta map[string]any, b begun, rec Record) {
	fail := func(text string) {
		out := hooks.Outcome{Error: &hooks.RPCError{Code: -32603, Message: text}}
		rec.Reply = &out
		g.record(rec)
		st.final(msg.ID, out)
	}
	fwd := b.Outcome.Forward
	be := g.backend(fwd.Slug)
	if be == nil {
		fail("unknown upstream")
		return
	}
	if fwd.Timeout != "" {
		if d, err := time.ParseDuration(fwd.Timeout); err == nil {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, d)
			defer cancel()
		}
	}

	// Progress is relayed only when the client asked for it and the
	// server's switch allows it. The upstream gets the gateway's own token.
	clientToken := meta["progressToken"]
	var upToken any
	if clientToken != nil {
		if sw, _ := g.ad.switches(ctx, req, be.server); sw["progress"] {
			upToken = fmt.Sprintf("gw-%d", g.seq.Add(1))
		}
	}

	events, err := g.send(ctx, be, fwd, upToken)
	if err != nil {
		// The call failed before any reply: a timeout, a refused
		// connection, an HTTP error. It is handed to the engine as a
		// stream that fails at once, so the client gets the engine's one
		// answer for a lost reply and the cause goes to the record. The
		// transport's own error text names the upstream's address and must
		// not reach the client.
		cause := err
		events = func(yield func(upEvent, error) bool) { yield(upEvent{}, cause) }
	}
	for ev, err := range g.ad.replies(ctx, b, events) {
		switch {
		case errors.Is(err, errNoResponse):
			// The spec says the request is lost and the client re-issues
			// it. Close the client's stream without a final message.
			rec.Aborted = true
			g.record(rec)
			st.abort()
			return
		case err != nil:
			fail(err.Error())
			return
		case ev.Outcome != nil:
			rec.Reply = ev.Outcome
			g.record(rec)
			st.final(msg.ID, *ev.Outcome)
			return
		}
		// A progress event: only under the token the gateway handed out,
		// and rewritten to the client's token.
		var p map[string]any
		if json.Unmarshal(ev.Params, &p) != nil || upToken == nil || fmt.Sprint(p["progressToken"]) != fmt.Sprint(upToken) {
			continue
		}
		p["progressToken"] = clientToken
		st.notify(ev.Method, p)
	}
	rec.Aborted = true
	g.record(rec)
	st.abort()
}

// ---- upstream side --------------------------------------------------------------------

// triple is the per-request _meta a 2026-07-28 upstream expects.
func (g *Gateway) triple(ci *hooks.ConnectInfo) map[string]any {
	m := map[string]any{metaVersion: V2026, metaClient: map[string]any{"name": "bifrost", "version": "harness"}, metaCaps: map[string]any{}}
	if ci != nil {
		if ci.ClientInfo != nil {
			m[metaClient] = ci.ClientInfo
		}
		if ci.Capabilities != nil {
			m[metaCaps] = ci.Capabilities
		}
	}
	return m
}

func (g *Gateway) post(ctx context.Context, url, version, method string, params any, notification bool) (*http.Response, error) {
	env := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if !notification {
		env["id"] = g.seq.Add(1)
	}
	body, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json, text/event-stream")
	if version != "" {
		hr.Header.Set("Mcp-Protocol-Version", version)
	}
	hr.Header.Set("Mcp-Method", method)
	if name := mcpName(version, method, params); name != "" {
		hr.Header.Set("Mcp-Name", name) // see remote.go
	}
	return g.hc.Do(hr)
}

// plain makes one request and returns its result, ignoring notifications.
func (g *Gateway) plain(ctx context.Context, url, version, method string, params any) (json.RawMessage, error) {
	resp, err := g.post(ctx, url, version, method, params, false)
	if err != nil {
		return nil, err
	}
	for ev, err := range readEvents(resp) {
		if err != nil {
			return nil, err
		}
		if !ev.final() {
			continue
		}
		if ev.Error != nil {
			return nil, ev.Error
		}
		return ev.Result, nil
	}
	return nil, errNoResponse
}

// initialize runs the pre-2026 handshake, declaring the given capabilities.
func (g *Gateway) initialize(ctx context.Context, url string, ci *hooks.ConnectInfo) (json.RawMessage, error) {
	params := map[string]any{"protocolVersion": V2025, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "bifrost", "version": "harness"}}
	if ci != nil {
		if ci.Capabilities != nil {
			params["capabilities"] = ci.Capabilities
		}
		if ci.ClientInfo != nil {
			params["clientInfo"] = ci.ClientInfo
		}
	}
	res, err := g.plain(ctx, url, "", "initialize", params)
	if err != nil {
		return nil, err
	}
	if resp, err := g.post(ctx, url, "", "notifications/initialized", map[string]any{}, true); err == nil {
		resp.Body.Close()
	}
	return res, nil
}

// send forwards one request upstream and returns the reply as events.
func (g *Gateway) send(ctx context.Context, be *backend, fwd *hooks.Forward, upToken any) (iter.Seq2[upEvent, error], error) {
	raw, err := json.Marshal(fwd.Params)
	if err != nil {
		return nil, err
	}
	params := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // numbers go upstream exactly as the engine wrote them
	if err := dec.Decode(&params); err != nil {
		return nil, err
	}
	meta, _ := params["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	delete(meta, "progressToken") // never the client's own token
	if be.version == V2026 {
		for k, v := range g.triple(fwd.Connect) {
			meta[k] = v
		}
	} else {
		// An older server learns capabilities in a handshake. The gateway
		// keeps no sessions, so it shakes hands for each forwarded request.
		if _, err := g.initialize(ctx, be.url, fwd.Connect); err != nil {
			return nil, err
		}
	}
	if upToken != nil {
		meta["progressToken"] = upToken
	}
	if len(meta) > 0 {
		params["_meta"] = meta
	} else {
		delete(params, "_meta")
	}
	resp, err := g.post(ctx, be.url, be.version, fwd.Method, params, false)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		return nil, fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}
	return readEvents(resp), nil
}

// readEvents reads an upstream reply, one JSON body or an SSE stream, as a
// sequence of events. It closes the body when the sequence ends.
func readEvents(resp *http.Response) iter.Seq2[upEvent, error] {
	return func(yield func(upEvent, error) bool) {
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			yield(upEvent{}, fmt.Errorf("upstream returned HTTP %d", resp.StatusCode))
			return
		}
		ct := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "text/event-stream") {
			body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
			if err != nil {
				yield(upEvent{}, err)
				return
			}
			ev, ok := parseEvent(body)
			if !ok {
				yield(upEvent{}, errMalformed)
				return
			}
			yield(ev, nil)
			return
		}
		rd := bufio.NewReaderSize(resp.Body, 64<<10)
		var data []byte
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					yield(upEvent{}, errNoResponse)
				} else {
					yield(upEvent{}, err)
				}
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case strings.HasPrefix(line, ":"): // a comment: keep-alive
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")...)
			case line == "" && len(data) > 0:
				ev, ok := parseEvent(data)
				data = nil
				if !ok {
					yield(upEvent{}, errMalformed)
					return
				}
				if !yield(ev, nil) || ev.final() {
					return
				}
			}
		}
	}
}

// parseEvent decodes one JSON-RPC message from an upstream. ok is false for
// anything that is not a notification or a response.
func parseEvent(b []byte) (upEvent, bool) {
	var m struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int64  `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return upEvent{}, false
	}
	hasID := len(m.ID) > 0 && string(m.ID) != "null"
	switch {
	case m.Method != "" && !hasID:
		return upEvent{Method: m.Method, Params: m.Params}, true
	case m.Method != "":
		// A request from the server. 2026-07-28 forbids these; treat it as a
		// notification nobody relays.
		return upEvent{Method: "(server request) " + m.Method}, true
	case m.Error != nil:
		return upEvent{Error: &hooks.RPCError{Code: m.Error.Code, Message: m.Error.Message}}, true
	case len(m.Result) > 0:
		return upEvent{Result: m.Result}, true
	}
	return upEvent{}, false
}

// ---- the client's stream --------------------------------------------------------------

// stream is the SSE reply to one client request.
type stream struct {
	w    http.ResponseWriter
	fl   http.Flusher
	mu   sync.Mutex
	done bool
	stop chan struct{}
	wg   sync.WaitGroup
}

func openStream(w http.ResponseWriter, keepAlive time.Duration) *stream {
	s := &stream{w: w, stop: make(chan struct{})}
	s.fl, _ = w.(http.Flusher)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s.flush()
	if keepAlive > 0 {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			t := time.NewTicker(keepAlive)
			defer t.Stop()
			for {
				select {
				case <-s.stop:
					return
				case <-t.C:
					s.mu.Lock()
					if !s.done {
						_, _ = io.WriteString(s.w, ":\n\n")
						s.flush()
					}
					s.mu.Unlock()
				}
			}
		}()
	}
	return s
}

func (s *stream) flush() {
	if s.fl != nil {
		s.fl.Flush()
	}
}

func (s *stream) data(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	_, _ = fmt.Fprintf(s.w, "data: %s\n\n", b)
	s.flush()
}

func (s *stream) notify(method string, params any) {
	s.data(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// final writes the last message of the stream. Later writes are ignored.
func (s *stream) final(id json.RawMessage, out hooks.Outcome) {
	env := map[string]any{"jsonrpc": "2.0", "id": id}
	switch {
	case out.Error != nil:
		env["error"] = out.Error
	case out.Result != nil:
		if _, err := json.Marshal(out.Result); err != nil {
			env["error"] = &hooks.RPCError{Code: -32603, Message: "the result could not be encoded"}
		} else {
			env["result"] = out.Result
		}
	default:
		env["error"] = &hooks.RPCError{Code: -32603, Message: "the request produced no outcome"}
	}
	s.data(env)
	s.mu.Lock()
	s.done = true
	s.mu.Unlock()
}

// abort ends the stream without a final message.
func (s *stream) abort() {
	s.mu.Lock()
	s.done = true
	s.mu.Unlock()
}

func (s *stream) close() {
	close(s.stop)
	s.wg.Wait()
}

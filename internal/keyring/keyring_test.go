package keyring

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/keyring/store"
	"github.com/andyjmorgan/Bifrost/internal/keyring/store/memstore"
)

const (
	testUIBase     = "https://bifrost.test"
	testLegacyHost = "vault.test"
	// testKek is 32 zero bytes: a fixture, not a key anyone uses.
	testKek = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
)

// testAccessKey is assembled at run time so no key-shaped literal sits in the source.
var testAccessKey = strings.Join([]string{"dwv", "mount", "fixture"}, "_")

func testConfig() *Config {
	return &Config{
		ActiveKekID:          "local:v1",
		Keks:                 map[string]string{"local:v1": testKek},
		OAuthRedirectBaseURL: "https://" + testLegacyHost,
		UIBaseURL:            testUIBase,
		LegacyHosts:          []string{testLegacyHost, "10.0.0.1:30820", ":30999"},
		TrustedProxies:       []string{"127.0.0.1/32"},
		AuditChannelCap:      1000,
	}
}

// newTestKeyring builds the whole module on the in-memory store, holding one access key with every
// scope. The handler under test is the one the gateway serves: Wrap around a stand-in for its router.
func newTestKeyring(t *testing.T, cfg *Config) (*Keyring, http.Handler) {
	t.Helper()
	ms := memstore.New()
	hash := sha256.Sum256([]byte(testAccessKey))
	if err := ms.InsertAccessKey(context.Background(), &store.AccessKey{
		UserID: uuid.New(), Name: "fixture", KeyHash: hash[:], KeyPrefix: testAccessKey[:9],
		Scopes: []string{"vault:read", "vault:readwrite", "vault:audit"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	k, err := build(context.Background(), cfg, ms, nil)
	if err != nil {
		t.Fatal(err)
	}
	return k, k.Wrap(gatewayStub())
}

// gatewayStub stands in for the gateway's router. It carries the three routes the gateway and the
// keyring both define, one route only the gateway has, and the SPA fallback, and marks every
// response so a test can tell which side answered.
func gatewayStub() http.Handler {
	r := chi.NewRouter()
	mark := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Served-By", "gateway")
		_, _ = io.WriteString(w, "gateway "+r.URL.Path)
	}
	for _, p := range []string{"/healthz", "/api/v1/api-keys", "/api/v1/audit", "/api/v1/me", "/api/v1/config", "/api/v1/servers"} {
		r.Get(p, mark)
	}
	r.Post("/mcp", mark)
	r.NotFound(mark)
	return r
}

type call struct {
	method, host, path string
	key                bool
	body               any
}

func do(t *testing.T, h http.Handler, c call) *httptest.ResponseRecorder {
	t.Helper()
	if c.method == "" {
		c.method = http.MethodGet
	}
	if c.host == "" {
		c.host = "bifrost.test"
	}
	var body io.Reader
	if c.body != nil {
		b, err := json.Marshal(c.body)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(b)
	}
	req := httptest.NewRequest(c.method, "http://"+c.host+c.path, body)
	if c.key {
		req.Header.Set("Authorization", "Bearer "+testAccessKey)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func byGateway(rec *httptest.ResponseRecorder) bool {
	return rec.Header().Get("X-Served-By") == "gateway"
}

func TestDisabledByDefault(t *testing.T) {
	for _, name := range []string{"CRYPTO_KEKS", "CRYPTO_ACTIVE_KEK_ID"} {
		t.Setenv(envPrefix+name, "")
		t.Setenv(legacyEnvPrefix+name, "")
	}
	// Other keyring settings do not switch it on; only the KEKs do.
	t.Setenv(envPrefix+"LEGACY_HOSTS", testLegacyHost)
	host := HostConfig{DSN: "postgres://unused", PublicBaseURL: testUIBase}

	cfg, err := LoadConfig(host)
	if err != nil || cfg != nil {
		t.Fatalf("LoadConfig = %v, %v; want nil, nil", cfg, err)
	}
	// FromEnv returns before it would open a database.
	k, err := FromEnv(context.Background(), host, nil)
	if err != nil || k != nil {
		t.Fatalf("FromEnv = %v, %v; want nil, nil", k, err)
	}

	// The disabled module leaves the gateway's router as the handler, whatever the path or host.
	h := k.Wrap(gatewayStub())
	for _, c := range []call{
		{path: "/keyring/api/config"},
		{path: "/keyring/api/v1/api-keys"},
		{path: "/api/v1/api-keys", host: testLegacyHost},
		{path: "/", host: testLegacyHost},
	} {
		if rec := do(t, h, c); !byGateway(rec) {
			t.Errorf("%s on %q was not served by the gateway (status %d)", c.path, c.host, rec.Code)
		}
	}
	if k.LegacyHosts() != nil {
		t.Error("a disabled keyring has no legacy hosts")
	}
	k.Start()
	k.Shutdown()
}

func TestPrefixMount(t *testing.T) {
	_, h := newTestKeyring(t, testConfig())

	rec := do(t, h, call{path: "/keyring/api/config"})
	var appConfig map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &appConfig); err != nil || rec.Code != 200 {
		t.Fatalf("config: %d %s", rec.Code, rec.Body)
	}
	if _, ok := appConfig["cliClientId"]; !ok {
		t.Fatalf("not the keyring's config document: %s", rec.Body)
	}
	if rec := do(t, h, call{path: "/keyring/healthz"}); rec.Code != 200 || byGateway(rec) {
		t.Fatalf("healthz: %d", rec.Code)
	}

	// The same path means different things with and without the prefix.
	if rec := do(t, h, call{path: "/keyring/api/v1/api-keys", key: true}); rec.Code != 200 || byGateway(rec) || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("keyring api-keys: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, call{path: "/api/v1/api-keys", key: true}); !byGateway(rec) {
		t.Fatalf("the gateway's own api-keys was not left to the gateway: %d", rec.Code)
	}

	// The keyring's authentication guards the mount.
	if rec := do(t, h, call{path: "/keyring/api/v1/api-keys"}); rec.Code != http.StatusUnauthorized || byGateway(rec) {
		t.Fatalf("no credential: %d", rec.Code)
	}
	// An unknown API path under the prefix is the keyring's 404, not the gateway's SPA.
	if rec := do(t, h, call{path: "/keyring/api/v1/servers", key: true}); rec.Code != http.StatusNotFound || byGateway(rec) {
		t.Fatalf("unknown keyring route: %d", rec.Code)
	}
	// The prefix is on every host, the legacy ones included.
	if rec := do(t, h, call{path: "/keyring/api/v1/me", host: testLegacyHost, key: true}); rec.Code != 200 || byGateway(rec) {
		t.Fatalf("prefix on the legacy host: %d", rec.Code)
	}

	// Only the API is taken: other paths under /keyring, and look-alikes, stay with the gateway.
	for _, p := range []string{"/keyring", "/keyring/", "/keyring/secrets", "/keyring/mcp", "/keyringx/api/config", "/keyring/apiary"} {
		if rec := do(t, h, call{path: p}); !byGateway(rec) {
			t.Errorf("%s was taken from the gateway (status %d)", p, rec.Code)
		}
	}
}

func TestLegacyHostWinsOverGatewayRoutes(t *testing.T) {
	_, h := newTestKeyring(t, testConfig())

	// The three paths both sides define, at Vault's original addresses.
	for _, p := range []string{"/api/v1/api-keys", "/api/v1/audit", "/api/v1/me"} {
		legacy := do(t, h, call{path: p, host: testLegacyHost, key: true})
		if legacy.Code != 200 || byGateway(legacy) {
			t.Errorf("%s on the legacy host: status %d, gateway=%v", p, legacy.Code, byGateway(legacy))
		}
		if own := do(t, h, call{path: p, key: true}); !byGateway(own) {
			t.Errorf("%s on the gateway's host was not the gateway's", p)
		}
	}
	// The keyring's shapes, not the gateway's.
	var me map[string]any
	if err := json.Unmarshal(do(t, h, call{path: "/api/v1/me", host: testLegacyHost, key: true}).Body.Bytes(), &me); err != nil || me["userId"] == nil {
		t.Fatalf("me is not the keyring's: %v %v", me, err)
	}

	// Nothing under /api falls through to the gateway on a legacy host, routed or not.
	if rec := do(t, h, call{path: "/api/v1/servers", host: testLegacyHost, key: true}); rec.Code != http.StatusNotFound || byGateway(rec) {
		t.Fatalf("gateway-only route reached through the legacy host: %d", rec.Code)
	}
	if rec := do(t, h, call{path: "/api/v1/config", host: testLegacyHost}); byGateway(rec) {
		t.Fatal("the gateway's config was served on the legacy host")
	}
	if rec := do(t, h, call{path: "/healthz", host: testLegacyHost}); rec.Code != 200 || byGateway(rec) {
		t.Fatalf("healthz: %d", rec.Code)
	}
	// The gateway's front door is not exposed on Vault's host.
	if rec := do(t, h, call{method: http.MethodPost, path: "/mcp", host: testLegacyHost}); rec.Code != http.StatusNotFound || byGateway(rec) {
		t.Fatalf("POST /mcp on the legacy host: %d", rec.Code)
	}

	hosts := []struct {
		host   string
		legacy bool
	}{
		{"vault.test", true},
		{"VAULT.test", true},
		{"vault.test:8080", true}, // a bare name matches any port
		{"10.0.0.1:30820", true},  // host:port matches that port only
		{"10.0.0.1:8080", false},
		{"10.0.0.1", false},
		{"10.0.0.7:30999", true}, // a bare :port matches any host on it, as a NodePort is reached
		{"vault-lan:30999", true},
		{"10.0.0.7:3099", false},
		{"bifrost.test", false},
		{"notvault.test", false},
	}
	for _, c := range hosts {
		rec := do(t, h, call{path: "/api/v1/api-keys", host: c.host, key: true})
		if byGateway(rec) == c.legacy {
			t.Errorf("host %q: legacy=%v, want %v", c.host, !byGateway(rec), c.legacy)
		}
	}
}

func TestLegacyHostBrowserRedirect(t *testing.T) {
	_, h := newTestKeyring(t, testConfig())

	pages := map[string]string{
		"/":                               "/secrets",
		"/?code=abc&state=def":            "/secrets",
		"/credentials":                    "/secrets",
		"/credentials?tab=oauth":          "/secrets?tab=oauth",
		"/providers":                      "/providers",
		"/providers/":                     "/providers",
		"/oauthconnect":                   "/connect",
		"/oauthconnect?connected=github":  "/connect?connected=github",
		"/profile":                        "/profile",
		"/access-keys":                    "/secrets",
		"/audit":                          "/secrets",
		"/assets/index-abc123.js":         "/secrets",
		"/no/such/page?next=//evil.test/": "/secrets",
	}
	for from, to := range pages {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := do(t, h, call{method: method, path: from, host: testLegacyHost})
			if rec.Code != http.StatusFound || rec.Header().Get("Location") != testUIBase+to {
				t.Errorf("%s %s: %d to %q, want 302 to %q", method, from, rec.Code, rec.Header().Get("Location"), testUIBase+to)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("%s %s: the redirect may be cached", method, from)
			}
		}
	}

	// A write to a page path is nothing: not redirected, and not handed to the gateway.
	if rec := do(t, h, call{method: http.MethodPost, path: "/credentials", host: testLegacyHost}); rec.Code != http.StatusNotFound || byGateway(rec) {
		t.Fatalf("POST to a page: %d", rec.Code)
	}

	// API calls are served where they land, authenticated or not: a redirect would lose the
	// Authorization header on the way to another host.
	for _, c := range []call{
		{path: "/api/v1/api-keys", host: testLegacyHost, key: true},
		{path: "/api/v1/api-keys", host: testLegacyHost},
		{path: "/api/config", host: testLegacyHost},
		{path: "/api/v1/nothing-here", host: testLegacyHost, key: true},
		{method: http.MethodPost, path: "/api/v1/api-keys", host: testLegacyHost, key: true, body: map[string]any{"name": "n", "secret": "s"}},
	} {
		rec := do(t, h, c)
		if rec.Code/100 == 3 || rec.Header().Get("Location") != "" {
			t.Errorf("%s %s was redirected (%d to %q)", c.method, c.path, rec.Code, rec.Header().Get("Location"))
		}
	}
	// The same pages on the gateway's own host are the gateway's.
	if rec := do(t, h, call{path: "/credentials"}); !byGateway(rec) {
		t.Fatalf("a page on the gateway's host: %d", rec.Code)
	}
}

// fakeProvider is an OAuth provider's token endpoint that records the form it was sent.
type fakeProvider struct {
	*httptest.Server
	mu   sync.Mutex
	form url.Values
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	p := &fakeProvider{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		p.mu.Lock()
		p.form = r.PostForm
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *fakeProvider) sent(key string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.form.Get(key)
}

// connect registers a provider and its app, then starts a flow through the prefix mount, the way
// the gateway's UI does. It returns the authorize URL the browser would be sent to.
func connect(t *testing.T, h http.Handler, tokenEndpoint string) *url.URL {
	t.Helper()
	if rec := do(t, h, call{method: http.MethodPost, path: "/keyring/api/v1/manifests/oauth", key: true, body: map[string]any{
		"key": "acme", "authorizationEndpoint": "https://acme.test/authorize", "tokenEndpoint": tokenEndpoint,
		"defaultScopes": []string{"openid"},
	}}); rec.Code != 200 {
		t.Fatalf("add provider: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, call{method: http.MethodPost, path: "/keyring/api/v1/oauth/configs", key: true, body: map[string]any{
		"provider": "acme", "clientId": "cid", "clientSecret": "csec", "scopes": []string{"openid"},
	}}); rec.Code != 200 {
		t.Fatalf("add app: %d %s", rec.Code, rec.Body)
	}
	rec := do(t, h, call{path: "/keyring/api/v1/oauth/acme/connect", key: true})
	var out struct {
		AuthorizeURL string `json:"authorizeUrl"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.AuthorizeURL == "" {
		t.Fatalf("connect: %d %s", rec.Code, rec.Body)
	}
	u, err := url.Parse(out.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestOAuthBases pins the two addresses of an OAuth flow apart: the redirect URI the provider
// sees, which stays on the host registered with it, and the page the browser ends on, which is the
// gateway's UI.
func TestOAuthBases(t *testing.T) {
	provider := newFakeProvider(t)
	cfg := testConfig() // redirect base https://vault.test, UI base https://bifrost.test
	_, h := newTestKeyring(t, cfg)

	const wantRedirectURI = "https://" + testLegacyHost + "/api/oauth/callback"
	authorize := connect(t, h, provider.URL)
	if got := authorize.Query().Get("redirect_uri"); got != wantRedirectURI {
		t.Fatalf("authorize redirect_uri = %q, want %q", got, wantRedirectURI)
	}

	// The provider sends the browser back to the legacy host. The callback is served there, and
	// its own redirect goes to the gateway's UI.
	state := authorize.Query().Get("state")
	rec := do(t, h, call{path: "/api/oauth/callback?code=c0de&state=" + url.QueryEscape(state), host: testLegacyHost})
	if want := testUIBase + "/connect?connected=acme"; rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("callback: %d to %q, want 302 to %q", rec.Code, rec.Header().Get("Location"), want)
	}
	// The exchange repeats the redirect URI of the authorize step, as the provider requires.
	if got := provider.sent("redirect_uri"); got != wantRedirectURI {
		t.Fatalf("token exchange redirect_uri = %q, want %q", got, wantRedirectURI)
	}

	// A failed callback lands on the same page of the UI, with the reason.
	rec = do(t, h, call{path: "/api/oauth/callback?error=access_denied", host: testLegacyHost})
	if want := testUIBase + "/connect?oauth_error=access_denied"; rec.Header().Get("Location") != want {
		t.Fatalf("failed callback to %q, want %q", rec.Header().Get("Location"), want)
	}
	// The callback answers under the prefix too, for a provider app registered with that address.
	rec = do(t, h, call{path: "/keyring/api/oauth/callback"})
	if want := testUIBase + "/connect?oauth_error=missing_code"; rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("prefixed callback: %d to %q, want %q", rec.Code, rec.Header().Get("Location"), want)
	}
}

// TestOAuthBasesMoveIndependently changes each base on its own and checks the other holds still.
func TestOAuthBasesMoveIndependently(t *testing.T) {
	cases := []struct {
		name, redirectBase, uiBase string
	}{
		{"redirect on the gateway", testUIBase + MountPrefix, testUIBase},
		{"UI elsewhere", "https://" + testLegacyHost, "https://ui.test"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.OAuthRedirectBaseURL, cfg.UIBaseURL = c.redirectBase, c.uiBase
			_, h := newTestKeyring(t, cfg)

			authorize := connect(t, h, "https://acme.test/token")
			if got, want := authorize.Query().Get("redirect_uri"), c.redirectBase+"/api/oauth/callback"; got != want {
				t.Fatalf("redirect_uri = %q, want %q", got, want)
			}
			rec := do(t, h, call{path: "/keyring/api/oauth/callback?error=nope"})
			if got, want := rec.Header().Get("Location"), c.uiBase+"/connect?oauth_error=nope"; got != want {
				t.Fatalf("after callback = %q, want %q", got, want)
			}
		})
	}
}

func TestStartAndShutdown(t *testing.T) {
	k, h := newTestKeyring(t, testConfig())
	closed := false
	k.closeDB = func() { closed = true }
	k.Start()
	k.Start() // a second Start must not launch a second pair of workers
	if rec := do(t, h, call{path: "/keyring/api/v1/me", key: true}); rec.Code != 200 {
		t.Fatalf("me: %d", rec.Code)
	}
	k.Shutdown()
	if !closed {
		t.Fatal("Shutdown did not close the database")
	}
	// The audit sink is closed now; a late request must still be answered.
	if rec := do(t, h, call{path: "/keyring/api/v1/me", key: true}); rec.Code != 200 {
		t.Fatalf("me after shutdown: %d", rec.Code)
	}
	if got := k.LegacyHosts(); len(got) != 3 {
		t.Fatalf("LegacyHosts = %v", got)
	}
}

func TestBuildRejectsBadKek(t *testing.T) {
	cfg := testConfig()
	cfg.ActiveKekID = "local:v2" // not among the configured keys
	if _, err := build(context.Background(), cfg, memstore.New(), nil); err == nil {
		t.Fatal("expected an error for an active KEK that is not configured")
	}
	cfg = testConfig()
	cfg.OIDCAuthority, cfg.OIDCRequireHTTPS = "http://idp.test", true
	if _, err := build(context.Background(), cfg, memstore.New(), nil); err == nil {
		t.Fatal("expected an error for a plain-http authority")
	}
}

func TestOpenFailsOnBadDSN(t *testing.T) {
	cfg := testConfig()
	cfg.DSN = "not a dsn ://"
	if _, err := Open(context.Background(), cfg, nil); err == nil {
		t.Fatal("expected an error for an unusable DSN")
	}
}

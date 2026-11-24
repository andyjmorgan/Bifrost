package keyring

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	gwaudit "github.com/andyjmorgan/Bifrost/internal/audit"
	"github.com/andyjmorgan/Bifrost/internal/keyring/audit"
	"github.com/andyjmorgan/Bifrost/internal/keyring/contracts"
	"github.com/andyjmorgan/Bifrost/internal/keyring/crypto"
	"github.com/andyjmorgan/Bifrost/internal/keyring/manifests"
	"github.com/andyjmorgan/Bifrost/internal/keyring/service"
	"github.com/andyjmorgan/Bifrost/internal/keyring/store"
	"github.com/andyjmorgan/Bifrost/internal/keyring/store/memstore"
	"github.com/andyjmorgan/Bifrost/internal/keyringref"
	gwstore "github.com/andyjmorgan/Bifrost/internal/store"
	"github.com/andyjmorgan/Bifrost/internal/upstream"
)

// Fixture values, assembled at run time so nothing credential-shaped sits in the source.
var (
	fixtureAccess  = func(n int) string { return fmt.Sprintf("access-%d-fixture", n) }
	fixtureRefresh = func(n int) string { return fmt.Sprintf("refresh-%d-fixture", n) }
	aliceSecret    = strings.Join([]string{"alice", "stored", "value"}, "-")
	bobSecret      = strings.Join([]string{"bob", "stored", "value"}, "-")
)

var gatewayUse = keyringref.Use{ServerID: "4d0c2b8e-1f0a-4c57-9a52-6f6f8f2f0f10", Slug: "xmcp"}

// person is a keyring user: an id and the tenant their rows are written under.
type person struct {
	id, tenant uuid.UUID
}

func (p person) ctx() context.Context {
	return contracts.WithCaller(context.Background(), contracts.Caller{UserID: p.id, TenantID: p.tenant})
}

// sourceRig is the whole keyring on the in-memory store, with a stand-in OAuth provider.
type sourceRig struct {
	t      *testing.T
	k      *Keyring
	ms     *memstore.Mem
	cipher crypto.Cipher
	src    keyringref.Holder

	idp       *httptest.Server
	mu        sync.Mutex
	refreshes int
}

func newSourceRig(t *testing.T) *sourceRig {
	t.Helper()
	cfg := testConfig()
	ms := memstore.New()
	k, err := build(context.Background(), cfg, ms, nil)
	if err != nil {
		t.Fatal(err)
	}
	kek, err := crypto.NewLocalKekProvider(cfg.ActiveKekID, cfg.Keks)
	if err != nil {
		t.Fatal(err)
	}
	r := &sourceRig{t: t, k: k, ms: ms, cipher: crypto.NewEnvelopeCipher(kek), src: k.Source()}
	// The provider rotates on every refresh: refresh N is answered with access N+1.
	r.idp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil || req.Form.Get("grant_type") != "refresh_token" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		r.mu.Lock()
		r.refreshes++
		n := r.refreshes
		r.mu.Unlock()
		if req.Form.Get("refresh_token") != fixtureRefresh(n) {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"expires_in":3600}`, fixtureAccess(n+1), fixtureRefresh(n+1))
	}))
	t.Cleanup(r.idp.Close)
	return r
}

func (r *sourceRig) refreshCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refreshes
}

// secret stores a secret in p's keyring, through the service the HTTP API uses.
func (r *sourceRig) secret(p person, params service.CreateAPIKeyParams) {
	r.t.Helper()
	if _, err := r.k.source.secrets.Create(p.ctx(), params); err != nil {
		r.t.Fatal(err)
	}
}

// account connects an OAuth account in p's keyring: the provider, its app credentials and a token
// set that expires at expiry. withRefresh says whether a refresh token came with it.
func (r *sourceRig) account(p person, provider, account string, expiry time.Time, withRefresh bool) {
	r.t.Helper()
	ctx := p.ctx()
	if err := r.k.source.resolver.UpsertOAuth(ctx, manifests.Manifest{
		Key: provider, TokenEndpoint: r.idp.URL + "/token", AuthorizationEndpoint: r.idp.URL + "/auth", ScopeDelimiter: " ",
	}); err != nil {
		r.t.Fatal(err)
	}
	clientSecret := "app-secret-fixture"
	if _, err := service.NewOAuthConfigService(r.ms, r.cipher, r.k.auditLog, r.k.source.resolver).Upsert(ctx, provider, "app-id", &clientSecret, []string{"read"}, nil); err != nil {
		r.t.Fatal(err)
	}
	providerID, err := r.k.source.resolver.ResolveProviderID(ctx, provider, p.id)
	if err != nil || providerID == uuid.Nil {
		r.t.Fatalf("provider id: %v", err)
	}
	row := &store.OAuthToken{UserID: p.id, TenantID: p.tenant, ProviderID: providerID, ProviderKey: provider, Account: account, ExpiresAt: &expiry}
	if row.AccessTokenCipher, err = r.cipher.EncryptString(fixtureAccess(1)); err != nil {
		r.t.Fatal(err)
	}
	if withRefresh {
		if row.RefreshTokenCipher, err = r.cipher.EncryptString(fixtureRefresh(1)); err != nil {
			r.t.Fatal(err)
		}
	}
	if err := r.ms.InsertOAuthToken(ctx, row); err != nil {
		r.t.Fatal(err)
	}
}

// events drains what the keyring's audit trail has been handed so far.
func (r *sourceRig) events() []audit.Event {
	var out []audit.Event
	for {
		select {
		case e := <-r.k.auditLog.Reader():
			out = append(out, e)
		default:
			return out
		}
	}
}

func str(s string) *string { return &s }

func newPerson() person { return person{id: uuid.New(), tenant: uuid.New()} }

func secretRefOf(p person, name string) keyringref.Ref {
	return keyringref.Ref{Owner: p.id.String(), Kind: keyringref.KindSecret, Secret: name}
}

func accountRefOf(p person, provider, account string) keyringref.Ref {
	return keyringref.Ref{Owner: p.id.String(), Kind: keyringref.KindOAuth, Provider: provider, Account: account}
}

// fromGateway reports whether an audit event names the gateway and the server as its caller.
func fromGateway(e audit.Event) bool {
	return e.Transport == gatewayTransport && e.Method != nil && strings.Contains(*e.Method, gatewayUse.Slug) && strings.Contains(*e.Method, gatewayUse.ServerID)
}

func TestSourceIsNilWhenTheKeyringIsOff(t *testing.T) {
	var k *Keyring
	if src := k.Source(); src != nil {
		t.Fatalf("a disabled keyring handed out %T", src)
	}
}

func TestSourceSendsASecretAsItDescribesItself(t *testing.T) {
	r := newSourceRig(t)
	alice := newPerson()
	r.secret(alice, service.CreateAPIKeyParams{Name: "atlas", Secret: &aliceSecret, Kind: contracts.KindHeaderAPIKey, Header: str("X-Atlas-Key"), Prefix: str("Key ")})
	r.secret(alice, service.CreateAPIKeyParams{Name: "bare", Secret: &aliceSecret, Kind: contracts.KindHeaderAPIKey})
	r.secret(alice, service.CreateAPIKeyParams{Name: "legacy", Secret: &aliceSecret, Kind: contracts.KindHTTPBasic, Username: str("svc")})
	r.events()

	for name, want := range map[string]keyringref.Header{
		"atlas":  {Name: "X-Atlas-Key", Value: "Key " + aliceSecret},
		"bare":   {Name: "Authorization", Value: aliceSecret},
		"legacy": {Name: "Authorization", Value: "Basic " + base64.StdEncoding.EncodeToString([]byte("svc:"+aliceSecret))},
	} {
		ref := secretRefOf(alice, name)
		if err := r.src.Check(context.Background(), ref); err != nil {
			t.Fatalf("%s: check: %v", name, err)
		}
		if got := r.events(); len(got) != 0 {
			t.Fatalf("%s: a check left %d audit events", name, len(got))
		}
		got, err := r.src.SecretHeader(context.Background(), ref, gatewayUse)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != want {
			t.Fatalf("%s: the header is not what the secret describes", name)
		}
		// The read is in the keyring's audit trail, under its owner and tenant, and says the gateway
		// made it for which server.
		events := r.events()
		if len(events) != 1 {
			t.Fatalf("%s: %d audit events, want 1", name, len(events))
		}
		e := events[0]
		if e.Type != audit.EventTokenAccessed || e.Outcome != audit.OutcomeSuccess || e.UserID != alice.id || e.TenantID != alice.tenant {
			t.Fatalf("%s: audit event %+v", name, e)
		}
		if !fromGateway(e) || e.TargetName == nil || *e.TargetName != name {
			t.Fatalf("%s: the audit event does not name the gateway, the server and the secret: %+v", name, e)
		}
		if written, _ := json.Marshal(e); strings.Contains(string(written), aliceSecret) {
			t.Fatalf("%s: the audit event holds the secret", name)
		}
	}
}

func TestSourceRefusesASecretThatNamesNoHeader(t *testing.T) {
	r := newSourceRig(t)
	alice := newPerson()
	r.secret(alice, service.CreateAPIKeyParams{Name: "box", Secret: &aliceSecret, Kind: contracts.KindSSH, Username: str("root")})
	r.secret(alice, service.CreateAPIKeyParams{Name: "blob", Secret: &aliceSecret, Kind: contracts.KindOpaque})
	r.secret(alice, service.CreateAPIKeyParams{Name: "dsn", Secret: &aliceSecret, Kind: contracts.KindConnectionString})
	r.events()

	for _, name := range []string{"box", "blob", "dsn"} {
		ref := secretRefOf(alice, name)
		if err := r.src.Check(context.Background(), ref); !errors.Is(err, keyringref.ErrUnusable) {
			t.Fatalf("%s: check gave %v, want ErrUnusable", name, err)
		}
		if _, err := r.src.SecretHeader(context.Background(), ref, gatewayUse); !errors.Is(err, keyringref.ErrUnusable) {
			t.Fatalf("%s: got %v, want ErrUnusable", name, err)
		}
	}
	for _, e := range r.events() {
		if e.Outcome != audit.OutcomeFailure || !fromGateway(e) {
			t.Fatalf("a refused read was recorded as %+v", e)
		}
	}
}

// A reference resolves in its owner's keyring and nowhere else. Bob holding a credential of the
// same name does not make it Alice's, and Alice's reference never reads Bob's value.
func TestSourceStaysInTheOwnersKeyring(t *testing.T) {
	r := newSourceRig(t)
	alice, bob := newPerson(), newPerson()
	r.secret(bob, service.CreateAPIKeyParams{Name: "only-bob", Secret: &bobSecret, Kind: contracts.KindHeaderAPIKey, Header: str("X-Key")})
	r.secret(bob, service.CreateAPIKeyParams{Name: "both", Secret: &bobSecret, Kind: contracts.KindHeaderAPIKey, Header: str("X-Key")})
	r.secret(alice, service.CreateAPIKeyParams{Name: "both", Secret: &aliceSecret, Kind: contracts.KindHeaderAPIKey, Header: str("X-Key")})
	r.account(bob, "acme", "bob@example.com", time.Now().Add(time.Hour), true)
	r.events()
	ctx := context.Background()

	// Bob's secret, asked for as Alice.
	ref := secretRefOf(alice, "only-bob")
	if err := r.src.Check(ctx, ref); !errors.Is(err, keyringref.ErrNotFound) {
		t.Fatalf("check gave %v, want ErrNotFound", err)
	}
	if h, err := r.src.SecretHeader(ctx, ref, gatewayUse); !errors.Is(err, keyringref.ErrNotFound) || h != (keyringref.Header{}) {
		t.Fatalf("another user's secret was not refused: %v", err)
	}
	// Bob's OAuth account, asked for as Alice.
	acct := accountRefOf(alice, "acme", "bob@example.com")
	if err := r.src.Check(ctx, acct); !errors.Is(err, keyringref.ErrNotFound) {
		t.Fatalf("check gave %v, want ErrNotFound", err)
	}
	if tok, err := r.src.OAuthToken(ctx, acct, gatewayUse, ""); !errors.Is(err, keyringref.ErrNotFound) || tok.AccessToken != "" {
		t.Fatalf("another user's OAuth account was not refused: %v", err)
	}
	// The refusals are recorded under Alice, the user the reference claims, never under Bob.
	refused := r.events()
	if len(refused) != 2 {
		t.Fatalf("%d audit events for two refused reads", len(refused))
	}
	for _, e := range refused {
		if e.Outcome != audit.OutcomeFailure || e.UserID != alice.id || !fromGateway(e) {
			t.Fatalf("a refused read was recorded as %+v", e)
		}
	}

	// A shared name gives each owner their own value.
	for owner, want := range map[person]string{alice: aliceSecret, bob: bobSecret} {
		h, err := r.src.SecretHeader(ctx, secretRefOf(owner, "both"), gatewayUse)
		if err != nil || h.Value != want {
			t.Fatalf("a shared name crossed keyrings (err %v)", err)
		}
	}

	// An owner the keyring cannot know is not found either, rather than matching everyone.
	for _, owner := range []string{"", "dev", uuid.Nil.String()} {
		if err := r.src.Check(ctx, keyringref.Ref{Owner: owner, Kind: keyringref.KindSecret, Secret: "both"}); !errors.Is(err, keyringref.ErrNotFound) {
			t.Fatalf("owner %q: got %v, want ErrNotFound", owner, err)
		}
	}
}

func TestSourceHandsOutALiveOAuthToken(t *testing.T) {
	r := newSourceRig(t)
	alice := newPerson()
	r.account(alice, "acme", "alice", time.Now().Add(time.Hour), true)
	r.events()
	ctx := context.Background()
	ref := accountRefOf(alice, "acme", "alice")

	if err := r.src.Check(ctx, ref); err != nil {
		t.Fatal(err)
	}
	tok, err := r.src.OAuthToken(ctx, ref, gatewayUse, "")
	if err != nil || tok.AccessToken != fixtureAccess(1) || tok.ExpiresAt == nil {
		t.Fatalf("token: %v", err)
	}
	if r.refreshCount() != 0 {
		t.Fatal("a live token was refreshed")
	}
	events := r.events()
	if len(events) != 1 || events[0].Type != audit.EventTokenAccessed || events[0].UserID != alice.id || events[0].TenantID != alice.tenant || !fromGateway(events[0]) {
		t.Fatalf("audit: %+v", events)
	}

	// The server refused that token: the keyring refreshes it although its expiry is an hour away.
	tok, err = r.src.OAuthToken(ctx, ref, gatewayUse, fixtureAccess(1))
	if err != nil || tok.AccessToken != fixtureAccess(2) {
		t.Fatalf("after a rejection: %v", err)
	}
	if r.refreshCount() != 1 {
		t.Fatalf("%d refreshes, want 1", r.refreshCount())
	}
	var refreshed bool
	for _, e := range r.events() {
		if written, _ := json.Marshal(e); strings.Contains(string(written), fixtureAccess(2)) || strings.Contains(string(written), fixtureRefresh(2)) {
			t.Fatal("an audit event holds a token")
		}
		refreshed = refreshed || (e.Type == audit.EventTokenRefreshed && e.Outcome == audit.OutcomeSuccess && fromGateway(e))
	}
	if !refreshed {
		t.Fatal("the refresh is not in the audit trail as the gateway's")
	}

	// A second caller still holding the old token gets the new one without another rotation.
	tok, err = r.src.OAuthToken(ctx, ref, gatewayUse, fixtureAccess(1))
	if err != nil || tok.AccessToken != fixtureAccess(2) || r.refreshCount() != 1 {
		t.Fatalf("a token that was already replaced was refreshed again (%d refreshes, err %v)", r.refreshCount(), err)
	}

	// Close to expiry it refreshes by itself, as it does for the CLI.
	soon := time.Now().Add(10 * time.Second)
	rows, _ := r.ms.ListOAuthTokens(ctx, alice.id)
	rows[0].ExpiresAt = &soon
	if err := r.ms.UpdateOAuthToken(ctx, &rows[0]); err != nil {
		t.Fatal(err)
	}
	tok, err = r.src.OAuthToken(ctx, ref, gatewayUse, "")
	if err != nil || tok.AccessToken != fixtureAccess(3) || r.refreshCount() != 2 {
		t.Fatalf("a token about to expire was not refreshed (%d refreshes, err %v)", r.refreshCount(), err)
	}

	// Disconnected: the very next ask finds nothing.
	if ok, err := r.ms.DeleteOAuthToken(ctx, alice.id, rows[0].ID); err != nil || !ok {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.src.OAuthToken(ctx, ref, gatewayUse, ""); !errors.Is(err, keyringref.ErrNotFound) {
		t.Fatalf("a disconnected account still resolved: %v", err)
	}
}

func TestSourceCannotReplaceATokenWithoutARefreshToken(t *testing.T) {
	r := newSourceRig(t)
	alice := newPerson()
	r.account(alice, "acme", "alice", time.Now().Add(time.Hour), false)
	ref := accountRefOf(alice, "acme", "alice")

	if _, err := r.src.OAuthToken(context.Background(), ref, gatewayUse, fixtureAccess(1)); !errors.Is(err, keyringref.ErrNoRefresh) {
		t.Fatalf("got %v, want ErrNoRefresh", err)
	}
	if r.refreshCount() != 0 {
		t.Fatal("the provider was called without a refresh token")
	}
}

// ---- through the gateway's RoundTripper ------------------------------------------------------

// gatewayAudit keeps what the gateway's own audit trail was given.
type gatewayAudit struct {
	mu       sync.Mutex
	payloads []*gwstore.AuditPayload
}

func (a *gatewayAudit) InsertAuditEvent(_ context.Context, _ *gwstore.AuditEvent, p *gwstore.AuditPayload) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.payloads = append(a.payloads, p)
	return nil
}

func (a *gatewayAudit) written(t *testing.T, n int) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		a.mu.Lock()
		count := len(a.payloads)
		b, _ := json.Marshal(a.payloads)
		a.mu.Unlock()
		if count >= n {
			return string(b)
		}
		if time.Now().After(deadline) {
			t.Fatalf("gateway audit: %d payloads, want %d", count, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// oneCredential is the gateway's credential store, holding a single row.
type oneCredential struct{ cred gwstore.Credential }

func (s oneCredential) GetCredential(context.Context, uuid.UUID) (*gwstore.Credential, error) {
	c := s.cred
	return &c, nil
}

func (oneCredential) SetCredentialState(context.Context, uuid.UUID, gwstore.CredentialState, string) error {
	return nil
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// The real keyring behind the gateway's real credential middleware, against a stand-in MCP server
// that takes a provider token and offers no OAuth discovery of its own: the case of X.
func TestGatewayReachesAServerWithAKeyringCredential(t *testing.T) {
	r := newSourceRig(t)
	alice := newPerson()
	r.account(alice, "x", "alice", time.Now().Add(time.Hour), true)
	r.secret(alice, service.CreateAPIKeyParams{Name: "atlas", Secret: &aliceSecret, Kind: contracts.KindHeaderAPIKey, Header: str("X-Atlas-Key"), Prefix: str("Key ")})
	r.events()

	// The server accepts the second access token and the stored secret, and nothing else.
	var mu sync.Mutex
	var got []http.Header
	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		got = append(got, req.Header.Clone())
		mu.Unlock()
		if req.Header.Get("Authorization") == "Bearer "+fixtureAccess(2) || req.Header.Get("X-Atlas-Key") == "Key "+aliceSecret {
			_, _ = io.WriteString(w, `{"ok":true}`)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer mcpServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &lockedBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sink := &gatewayAudit{}
	connect := func(cred gwstore.Credential) (*http.Client, error) {
		srv := &gwstore.Server{ID: uuid.MustParse(gatewayUse.ServerID), Slug: gatewayUse.Slug, URL: mcpServer.URL, AuthKind: cred.Kind, Enabled: true}
		cred.ServerID, cred.State = srv.ID, gwstore.StateOK
		conn := upstream.New(oneCredential{cred: cred}, r.src, gwaudit.NewRecorder(ctx, sink, log), log, 5*time.Second, "test")
		return conn.HTTPClient(ctx, srv)
	}
	send := func(hc *http.Client) (int, error) {
		resp, err := hc.Post(mcpServer.URL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}

	// OAuth account: the first token is refused, the keyring refreshes it at the provider, and the
	// request goes through on the retry.
	hc, err := connect(gwstore.Credential{Kind: gwstore.AuthKeyringOAuth, KeyringOwner: alice.id.String(), KeyringProvider: "x", KeyringAccount: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if status, err := send(hc); err != nil || status != http.StatusOK {
		t.Fatalf("oauth account: status %d, err %v", status, err)
	}
	if r.refreshCount() != 1 {
		t.Fatalf("%d refreshes at the provider, want 1", r.refreshCount())
	}
	mu.Lock()
	if len(got) != 2 || got[0].Get("Authorization") != "Bearer "+fixtureAccess(1) || got[1].Get("Authorization") != "Bearer "+fixtureAccess(2) {
		t.Fatalf("the server saw %d requests, not the first token and then the refreshed one", len(got))
	}
	got = nil
	mu.Unlock()

	// Secret: sent in the header it names, behind its prefix.
	hc, err = connect(gwstore.Credential{Kind: gwstore.AuthKeyringSecret, KeyringOwner: alice.id.String(), KeyringSecretName: "atlas"})
	if err != nil {
		t.Fatal(err)
	}
	if status, err := send(hc); err != nil || status != http.StatusOK {
		t.Fatalf("secret: status %d, err %v", status, err)
	}

	// The keyring's audit trail has the gateway's reads; every one names the gateway and the server.
	events := r.events()
	kinds := map[audit.EventType]int{}
	for _, e := range events {
		if !fromGateway(e) || e.UserID != alice.id || e.TenantID != alice.tenant {
			t.Fatalf("keyring audit event %+v", e)
		}
		kinds[e.Type]++
	}
	if kinds[audit.EventTokenAccessed] != 3 || kinds[audit.EventTokenRefreshed] != 1 {
		t.Fatalf("keyring audit: %v, want three reads (two of the token, one of the secret) and one refresh", kinds)
	}

	// The same reference in Bob's hands reaches nothing: his keyring does not hold it.
	bob := newPerson()
	hc, err = connect(gwstore.Credential{Kind: gwstore.AuthKeyringSecret, KeyringOwner: bob.id.String(), KeyringSecretName: "atlas"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := send(hc); !errors.Is(err, keyringref.ErrNotFound) {
		t.Fatalf("another user's reference resolved: %v", err)
	}

	// Deleted in the keyring: the next request fails, and says why.
	rows, _ := r.ms.ListAPIKeys(ctx, alice.id)
	if ok, err := r.ms.DeleteAPIKey(ctx, alice.id, rows[0].ID); err != nil || !ok {
		t.Fatalf("delete: %v", err)
	}
	hc, err = connect(gwstore.Credential{Kind: gwstore.AuthKeyringSecret, KeyringOwner: alice.id.String(), KeyringSecretName: "atlas"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := send(hc); !errors.Is(err, keyringref.ErrNotFound) || !strings.Contains(err.Error(), "keyring credential unavailable") {
		t.Fatalf("a deleted secret: %v", err)
	}

	// Nothing the gateway wrote down holds a token or the secret.
	gatewayWrote := sink.written(t, 3)
	keyringWrote, _ := json.Marshal(events)
	for _, secret := range []string{fixtureAccess(1), fixtureAccess(2), fixtureRefresh(1), fixtureRefresh(2), aliceSecret} {
		for where, text := range map[string]string{"the gateway's log": logs.String(), "the gateway's audit trail": gatewayWrote, "the keyring's audit trail": string(keyringWrote)} {
			if strings.Contains(text, secret) {
				t.Errorf("%s holds a credential", where)
			}
		}
	}
	if !strings.Contains(gatewayWrote, `"X-Atlas-Key":"[redacted]"`) || !strings.Contains(gatewayWrote, `"Authorization":"[redacted]"`) {
		t.Errorf("the gateway's audit trail did not mask the credential headers: %s", gatewayWrote)
	}
}

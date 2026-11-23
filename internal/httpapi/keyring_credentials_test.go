package httpapi

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
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/internal/audit"
	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/cache"
	"github.com/andyjmorgan/Bifrost/internal/crypto"
	"github.com/andyjmorgan/Bifrost/internal/db"
	"github.com/andyjmorgan/Bifrost/internal/gateway"
	"github.com/andyjmorgan/Bifrost/internal/keyringref"
	"github.com/andyjmorgan/Bifrost/internal/oauthclient"
	"github.com/andyjmorgan/Bifrost/internal/store"
	"github.com/andyjmorgan/Bifrost/internal/upstream"
)

// These tests run the servers API on a real Postgres, because the point is
// what the API stores and returns. Set MCPGW_TEST_DSN to a throwaway
// database to run them; they are skipped otherwise.

// Two users, as the identity provider would name them.
const (
	alice = "5f0e7c1a-3b1d-4c55-8a40-1d2b3c4d5e61"
	bob   = "9a1b2c3d-4e5f-4a6b-8c7d-0e1f2a3b4c52"
)

// What the stub keyring hands out. Assembled at run time so nothing
// credential-shaped sits in the source.
var (
	stubToken  = strings.Join([]string{"stub", "access", "token"}, "-")
	stubSecret = strings.Join([]string{"stub", "secret", "value"}, "-")
)

const (
	stubHeader = "X-Stub-Credential"
	stubUser   = "stub-user"
)

// keyringStub is a keyring holding names per owner. It gives every OAuth
// account the same token and every secret the same header, so a test can
// look for those two values anywhere they should not be. What the gateway
// stores for a server is kept as it was given, in gateway.
type keyringStub struct {
	held    map[string][]keyringref.Ref // by owner
	gateway *gatewaySpace
}

// gatewaySpace is the stub's stand-in for the keyring space of
// keyringref.GatewayOwner: the credentials servers own themselves, by name.
type gatewaySpace struct {
	mu      sync.Mutex
	secrets map[string]keyringref.ServerSecret
	puts    []keyringref.Use
	forgot  []string
}

func (g *gatewaySpace) secret(name string) (keyringref.ServerSecret, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	s, ok := g.secrets[name]
	return s, ok
}

func (k keyringStub) Check(_ context.Context, ref keyringref.Ref) error {
	if ref.HeldByGateway() && k.gateway != nil {
		if _, ok := k.gateway.secret(ref.Secret); ok {
			return nil
		}
	}
	for _, h := range k.held[ref.Owner] {
		if h == ref {
			return nil
		}
	}
	return fmt.Errorf("keyring %s: %w", ref, keyringref.ErrNotFound)
}

func (k keyringStub) Describe(ctx context.Context, ref keyringref.Ref) (keyringref.Description, error) {
	if err := k.Check(ctx, ref); err != nil {
		return keyringref.Description{}, err
	}
	if ref.HeldByGateway() {
		s, _ := k.gateway.secret(ref.Secret)
		return keyringref.Description{Scheme: s.Scheme, Username: s.Username, HeaderName: s.HeaderName}, nil
	}
	return keyringref.Description{Scheme: keyringref.SchemeHeader, HeaderName: stubHeader}, nil
}

// The stub connects no servers; server_oauth_test.go runs those through the
// real keyring.
func (k keyringStub) BeginServerOAuth(context.Context, keyringref.Use, keyringref.ServerOAuth) (string, keyringref.Ref, error) {
	return "", keyringref.Ref{}, errors.New("the stub keyring connects no servers")
}

func (k keyringStub) OAuthState(context.Context, string) (keyringref.OAuthState, error) {
	return keyringref.OAuthState{}, keyringref.ErrNoState
}

func (k keyringStub) CompleteServerOAuth(context.Context, string, string) error {
	return keyringref.ErrNoState
}

func (k keyringStub) AbandonServerOAuth(context.Context, string, string) error {
	return keyringref.ErrNoState
}

func (k keyringStub) PutServerSecret(_ context.Context, use keyringref.Use, in keyringref.ServerSecret) (keyringref.Ref, error) {
	k.gateway.mu.Lock()
	defer k.gateway.mu.Unlock()
	name := keyringref.ServerCredentialName(use.Slug)
	k.gateway.secrets[name] = in
	k.gateway.puts = append(k.gateway.puts, use)
	return keyringref.Ref{Owner: keyringref.GatewayOwner, Kind: keyringref.KindSecret, Secret: name}, nil
}

func (k keyringStub) Forget(_ context.Context, ref keyringref.Ref, _ keyringref.Use) error {
	if !ref.HeldByGateway() {
		return nil
	}
	k.gateway.mu.Lock()
	defer k.gateway.mu.Unlock()
	delete(k.gateway.secrets, ref.Secret)
	k.gateway.forgot = append(k.gateway.forgot, ref.Secret)
	return nil
}

func (k keyringStub) OAuthToken(ctx context.Context, ref keyringref.Ref, _ keyringref.Use, _ string) (keyringref.Token, error) {
	if err := k.Check(ctx, ref); err != nil {
		return keyringref.Token{}, err
	}
	return keyringref.Token{AccessToken: stubToken}, nil
}

func (k keyringStub) SecretHeader(ctx context.Context, ref keyringref.Ref, _ keyringref.Use) (keyringref.Header, error) {
	if err := k.Check(ctx, ref); err != nil {
		return keyringref.Header{}, err
	}
	if ref.HeldByGateway() {
		s, _ := k.gateway.secret(ref.Secret)
		if s.Scheme == keyringref.SchemeBasic {
			return keyringref.Header{Name: "Authorization", Value: "Basic " + base64.StdEncoding.EncodeToString([]byte(s.Username+":"+s.Value))}, nil
		}
		return keyringref.Header{Name: s.HeaderName, Value: s.Value}, nil
	}
	return keyringref.Header{Name: stubHeader, Value: stubSecret}, nil
}

// aliceHolds is a keyring in which Alice has one OAuth account and one
// secret, and Bob has nothing.
func aliceHolds() keyringStub {
	return keyringStub{gateway: &gatewaySpace{secrets: map[string]keyringref.ServerSecret{}}, held: map[string][]keyringref.Ref{alice: {
		{Owner: alice, Kind: keyringref.KindOAuth, Provider: "x", Account: "alice"},
		{Owner: alice, Kind: keyringref.KindSecret, Secret: "atlas"},
	}}}
}

type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// apiRig is the gateway's API on a throwaway database, in dev mode: the
// X-Dev-User header says who is calling.
type apiRig struct {
	t      *testing.T
	h      http.Handler
	store  *store.Postgres
	keys   *auth.Keys
	logs   *logBuffer
	mcpURL string // a real MCP server that wants the stub credential
}

// newAPIRig builds the API with kr as its keyring; nil is the keyring off.
func newAPIRig(t *testing.T, kr keyringref.Holder) *apiRig {
	t.Helper()
	dsn := os.Getenv("MCPGW_TEST_DSN")
	if dsn == "" {
		t.Skip("set MCPGW_TEST_DSN to a throwaway Postgres to run the API tests")
	}
	ctx, cancel := context.WithCancel(context.Background())
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	t.Cleanup(cancel)

	logs := &logBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	st := store.NewPostgres(pool)
	sealer, err := crypto.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	keys := auth.NewKeys(st, sealer)
	rec := audit.NewRecorder(ctx, st, log)
	oauth := oauthclient.New("http://gateway.test", &http.Client{Timeout: 5 * time.Second})
	conn := upstream.New(st, sealer, oauth, kr, rec, log, 5*time.Second, time.Minute, "test")
	cw := cache.NewWriter(ctx, st, log)
	api := New(Deps{
		Store: st, Keys: keys, Authn: &auth.Authenticator{Keys: keys, Users: st, DevOwner: alice, PublicBase: "http://gateway.test"},
		Conn: conn, OAuth: oauth, Keyring: kr, Gateway: gateway.New(st, conn, rec, cw, time.Minute, log, "test"), Recorder: rec, Cache: cw,
		ToolsTTL: time.Minute, Log: log, PublicBase: "http://gateway.test",
	})

	// An MCP server with one tool, which answers only to the stub credential.
	server := mcp.NewServer(&mcp.Implementation{Name: "stub", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "hello", Description: "says hello"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "hello"}}}, nil, nil
	})
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, Logger: quiet})
	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		if r.Header.Get("Authorization") != "Bearer "+stubToken && r.Header.Get(stubHeader) != stubSecret && (user != stubUser || pass != stubSecret) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(mcpServer.Close)

	return &apiRig{t: t, h: api.Router(), store: st, keys: keys, logs: logs, mcpURL: mcpServer.URL}
}

// do calls the API as a user (or, when bearer is set, with that API key)
// and returns the status and the raw body.
func (r *apiRig) do(method, path, user, bearer string, body any) (int, string) {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			r.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, "/api/v1"+path, rd)
	if user != "" {
		req.Header.Set("X-Dev-User", user)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// server registers a server without a credential, as Alice, and removes it
// when the test ends.
func (r *apiRig) server() ServerDTO {
	r.t.Helper()
	status, body := r.do(http.MethodPost, "/servers", alice, "", CreateServerInput{
		Name: "stub " + uuid.NewString()[:8], Slug: "k" + uuid.NewString()[:8], URL: r.mcpURL, Auth: AuthInput{Kind: store.AuthNone},
	})
	if status != http.StatusCreated {
		r.t.Fatalf("create server: %d %s", status, body)
	}
	var out CreateServerResult
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		r.t.Fatal(err)
	}
	id := uuid.MustParse(out.Server.ID)
	r.t.Cleanup(func() { _ = r.store.DeleteServer(context.Background(), id) })
	return out.Server
}

func decodeServer(t *testing.T, body string) ServerDTO {
	t.Helper()
	var out CreateServerResult
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("%v in %s", err, body)
	}
	return out.Server
}

var (
	oauthInput  = AuthInput{Kind: store.AuthKeyringOAuth, KeyringProvider: "x", KeyringAccount: "alice"}
	secretInput = AuthInput{Kind: store.AuthKeyringSecret, KeyringSecretName: "atlas"}
)

// noCredentialIn fails the test when text holds what the keyring hands out.
func noCredentialIn(t *testing.T, where, text string) {
	t.Helper()
	for _, secret := range []string{stubToken, stubSecret} {
		if strings.Contains(text, secret) {
			t.Errorf("%s holds a keyring credential: %s", where, text)
		}
	}
}

func TestServerUsesAKeyringCredential(t *testing.T) {
	for name, in := range map[string]AuthInput{"oauth account": oauthInput, "secret": secretInput} {
		t.Run(name, func(t *testing.T) {
			r := newAPIRig(t, aliceHolds())
			s := r.server()

			status, body := r.do(http.MethodPut, "/servers/"+s.ID+"/credential", alice, "", in)
			if status != http.StatusOK {
				t.Fatalf("put credential: %d %s", status, body)
			}
			noCredentialIn(t, "the response to setting it", body)
			got := decodeServer(t, body)
			c := got.Credential
			if got.AuthKind != in.Kind || c == nil || c.Kind != in.Kind || c.State != store.StateOK || c.KeyringOwner != alice {
				t.Fatalf("the server does not show its keyring credential: %s", body)
			}
			if c.KeyringProvider != in.KeyringProvider || c.KeyringAccount != in.KeyringAccount || c.KeyringSecretName != in.KeyringSecretName {
				t.Fatalf("the reference came back changed: %+v", c)
			}

			// The gateway holds the reference and no secret.
			row, err := r.store.GetCredential(context.Background(), uuid.MustParse(s.ID))
			if err != nil {
				t.Fatal(err)
			}
			if len(row.SecretBlob) != 0 || row.KeyringOwner != alice {
				t.Fatalf("stored %+v", row)
			}

			// The server is reached with it: a ping initializes and lists
			// tools on an MCP server that refuses anything else.
			status, body = r.do(http.MethodPost, "/servers/"+s.ID+"/ping", bob, "", nil)
			var ping PingResult
			if err := json.Unmarshal([]byte(body), &ping); err != nil || status != http.StatusOK {
				t.Fatalf("ping: %d %s", status, body)
			}
			if !ping.OK || ping.ToolCount != 1 {
				t.Fatalf("the server was not reached with the keyring credential: %s", body)
			}
			noCredentialIn(t, "the ping result", body)

			// Nothing the API returns holds the credential: the server, the
			// listing, and the audited exchanges with their headers.
			for _, path := range []string{"/servers", "/servers/" + s.ID} {
				_, body := r.do(http.MethodGet, path, bob, "", nil)
				noCredentialIn(t, "GET "+path, body)
			}
			r.auditedWithoutTheCredential(s.ID)
			noCredentialIn(t, "the gateway's log", r.logs.String())
		})
	}
}

// auditedWithoutTheCredential reads every upstream exchange the gateway
// recorded for a server and checks that the credential header is masked.
func (r *apiRig) auditedWithoutTheCredential(serverID string) {
	r.t.Helper()
	var page PageDTO[AuditEventDTO]
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, body := r.do(http.MethodGet, "/audit?hop=upstream&serverId="+serverID, alice, "", nil)
		if err := json.Unmarshal([]byte(body), &page); err != nil {
			r.t.Fatal(err)
		}
		if len(page.Items) >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(page.Items) == 0 {
		r.t.Fatal("no upstream exchange was audited")
	}
	masked := 0
	for _, e := range page.Items {
		_, body := r.do(http.MethodGet, "/audit/"+e.ID, alice, "", nil)
		noCredentialIn(r.t, "an audited exchange", body)
		var detail AuditDetailDTO
		if err := json.Unmarshal([]byte(body), &detail); err != nil {
			r.t.Fatal(err)
		}
		if detail.Payload == nil {
			continue
		}
		for name, value := range detail.Payload.RequestHeaders {
			if (name == "Authorization" || name == stubHeader) && value == "[redacted]" {
				masked++
			}
		}
	}
	if masked == 0 {
		r.t.Fatal("no audited exchange shows the credential header as masked")
	}
}

// A reference is resolved in the keyring of the person setting it. Bob
// naming a credential that only Alice holds finds nothing.
func TestAnotherUsersKeyringCredentialIsRefused(t *testing.T) {
	r := newAPIRig(t, aliceHolds())
	s := r.server()

	for name, in := range map[string]AuthInput{"oauth account": oauthInput, "secret": secretInput} {
		status, body := r.do(http.MethodPut, "/servers/"+s.ID+"/credential", bob, "", in)
		if status != http.StatusBadRequest || !strings.Contains(body, keyringref.ErrNotFound.Error()) {
			t.Fatalf("%s: Bob set a credential from Alice's keyring: %d %s", name, status, body)
		}
		// The same on create, which must not leave a server behind.
		slug := "k" + uuid.NewString()[:8]
		status, body = r.do(http.MethodPost, "/servers", bob, "", CreateServerInput{Name: "bob's", Slug: slug, URL: r.mcpURL, Auth: in})
		if status != http.StatusBadRequest {
			t.Fatalf("%s: create: %d %s", name, status, body)
		}
		if _, body := r.do(http.MethodGet, "/servers", bob, "", nil); strings.Contains(body, slug) {
			t.Fatalf("%s: a refused create left a server", name)
		}
	}
	if got, _ := r.store.GetServer(context.Background(), uuid.MustParse(s.ID)); got.AuthKind != store.AuthNone {
		t.Fatalf("a refused change still switched the server to %s", got.AuthKind)
	}

	// A name nobody holds, and a reference with a part missing.
	for _, in := range []AuthInput{
		{Kind: store.AuthKeyringSecret, KeyringSecretName: "nope"},
		{Kind: store.AuthKeyringSecret},
		{Kind: store.AuthKeyringOAuth, KeyringProvider: "x"},
	} {
		if status, body := r.do(http.MethodPut, "/servers/"+s.ID+"/credential", alice, "", in); status != http.StatusBadRequest {
			t.Fatalf("%+v: %d %s", in, status, body)
		}
	}
}

// A gateway API key carries no right to read the keyring, so it cannot
// point a server at a keyring credential, even its own creator's.
func TestAnAPIKeyCannotSetAKeyringCredential(t *testing.T) {
	r := newAPIRig(t, aliceHolds())
	s := r.server()
	_, key, err := r.keys.Create(context.Background(), alice, "edit "+uuid.NewString()[:8], auth.AllScopes)
	if err != nil {
		t.Fatal(err)
	}

	status, body := r.do(http.MethodPut, "/servers/"+s.ID+"/credential", "", key, secretInput)
	if status != http.StatusForbidden {
		t.Fatalf("an API key set a keyring credential: %d %s", status, body)
	}
	// The same key still sets the gateway's own kinds.
	status, body = r.do(http.MethodPut, "/servers/"+s.ID+"/credential", "", key, AuthInput{Kind: store.AuthHeader, HeaderName: "X-Api-Key", HeaderValue: "v"})
	if status != http.StatusOK {
		t.Fatalf("an API key could not set a header credential: %d %s", status, body)
	}
}

// Every operator may edit every server, but a keyring credential is one
// person's: only they may send it to a new URL.
func TestOnlyTheOwnerRepointsAServerWithAKeyringCredential(t *testing.T) {
	r := newAPIRig(t, aliceHolds())
	s := r.server()
	if status, body := r.do(http.MethodPut, "/servers/"+s.ID+"/credential", alice, "", secretInput); status != http.StatusOK {
		t.Fatalf("put credential: %d %s", status, body)
	}
	elsewhere := UpdateServerInput{URL: ptr("http://127.0.0.1:1/mcp")}

	if status, body := r.do(http.MethodPatch, "/servers/"+s.ID, bob, "", elsewhere); status != http.StatusForbidden {
		t.Fatalf("Bob re-pointed a server that uses Alice's keyring: %d %s", status, body)
	}
	_, key, err := r.keys.Create(context.Background(), alice, "edit "+uuid.NewString()[:8], auth.AllScopes)
	if err != nil {
		t.Fatal(err)
	}
	if status, body := r.do(http.MethodPatch, "/servers/"+s.ID, "", key, elsewhere); status != http.StatusForbidden {
		t.Fatalf("an API key re-pointed a server that uses a keyring credential: %d %s", status, body)
	}
	if got, _ := r.store.GetServer(context.Background(), uuid.MustParse(s.ID)); got.URL != r.mcpURL {
		t.Fatalf("the URL changed to %s", got.URL)
	}

	// Bob may still rename it, and Alice may move it.
	if status, body := r.do(http.MethodPatch, "/servers/"+s.ID, bob, "", UpdateServerInput{Name: ptr("renamed")}); status != http.StatusOK {
		t.Fatalf("Bob could not rename the server: %d %s", status, body)
	}
	if status, body := r.do(http.MethodPatch, "/servers/"+s.ID, alice, "", elsewhere); status != http.StatusOK {
		t.Fatalf("Alice could not re-point her own server: %d %s", status, body)
	}
}

func ptr[T any](v T) *T { return &v }

// With the keyring off the API is what it was: the keyring kinds are
// refused, the other kinds work, and no response grows a keyring field.
func TestKeyringOffTheAPIIsAsBefore(t *testing.T) {
	r := newAPIRig(t, nil)
	s := r.server()

	for name, in := range map[string]AuthInput{"oauth account": oauthInput, "secret": secretInput} {
		status, body := r.do(http.MethodPut, "/servers/"+s.ID+"/credential", alice, "", in)
		if status != http.StatusBadRequest || !strings.Contains(body, keyringref.ErrOff.Error()) {
			t.Fatalf("%s: %d %s", name, status, body)
		}
		status, body = r.do(http.MethodPost, "/servers", alice, "", CreateServerInput{Name: "n", Slug: "k" + uuid.NewString()[:8], URL: r.mcpURL, Auth: in})
		if status != http.StatusBadRequest || !strings.Contains(body, keyringref.ErrOff.Error()) {
			t.Fatalf("%s: create: %d %s", name, status, body)
		}
	}

	status, body := r.do(http.MethodPut, "/servers/"+s.ID+"/credential", alice, "", AuthInput{Kind: store.AuthHeader, HeaderName: stubHeader, HeaderValue: stubSecret})
	if status != http.StatusOK {
		t.Fatalf("header credential: %d %s", status, body)
	}
	if strings.Contains(strings.ToLower(body), "keyring") {
		t.Fatalf("a response without a keyring credential names the keyring: %s", body)
	}
	noCredentialIn(t, "the response to setting a header credential", body)
	status, body = r.do(http.MethodPost, "/servers/"+s.ID+"/ping", alice, "", nil)
	var ping PingResult
	if err := json.Unmarshal([]byte(body), &ping); err != nil || status != http.StatusOK || !ping.OK {
		t.Fatalf("the gateway's own header credential stopped working: %d %s", status, body)
	}
	if _, body = r.do(http.MethodGet, "/servers/"+s.ID, alice, "", nil); strings.Contains(strings.ToLower(body), "keyring") {
		t.Fatalf("a server without a keyring credential names the keyring: %s", body)
	}
}

// A server that was given a keyring credential, on a gateway whose keyring
// has since been turned off, says so instead of calling out with nothing.
func TestKeyringOffAServerWithAKeyringCredentialFailsClearly(t *testing.T) {
	r := newAPIRig(t, nil)
	s := r.server()
	ctx := context.Background()
	id := uuid.MustParse(s.ID)
	row, err := r.store.GetServer(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	row.AuthKind = store.AuthKeyringOAuth
	if err := r.store.UpdateServer(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := r.store.UpsertCredential(ctx, &store.Credential{ServerID: id, Kind: store.AuthKeyringOAuth, State: store.StateOK, OAuthScopes: []string{},
		KeyringOwner: alice, KeyringProvider: "x", KeyringAccount: "alice"}); err != nil {
		t.Fatal(err)
	}

	status, body := r.do(http.MethodPost, "/servers/"+s.ID+"/ping", alice, "", nil)
	var ping PingResult
	if err := json.Unmarshal([]byte(body), &ping); err != nil || status != http.StatusOK {
		t.Fatalf("ping: %d %s", status, body)
	}
	if ping.OK || !strings.Contains(ping.Error, "keyring is not enabled") {
		t.Fatalf("the failure does not say the keyring is off: %s", body)
	}
	// The API still shows which credential the server is waiting for.
	_, body = r.do(http.MethodGet, "/servers/"+s.ID, alice, "", nil)
	var got ServerDTO
	if err := json.Unmarshal([]byte(body), &got); err != nil || got.Credential == nil || got.Credential.KeyringAccount != "alice" {
		t.Fatalf("the reference is not shown: %s", body)
	}
}

// The check constraint keeps a keyring credential from being stored
// without saying whose keyring it is in.
func TestAKeyringCredentialMustNameItsOwner(t *testing.T) {
	r := newAPIRig(t, nil)
	s := r.server()
	err := r.store.UpsertCredential(context.Background(), &store.Credential{ServerID: uuid.MustParse(s.ID), Kind: store.AuthKeyringSecret,
		State: store.StateOK, OAuthScopes: []string{}, KeyringSecretName: "atlas"})
	if err == nil {
		t.Fatal("a keyring credential was stored without an owner")
	}
}

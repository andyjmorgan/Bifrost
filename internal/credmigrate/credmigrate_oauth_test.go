package credmigrate_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/andyjmorgan/Bifrost/internal/audit"
	"github.com/andyjmorgan/Bifrost/internal/credmigrate"
	"github.com/andyjmorgan/Bifrost/internal/keyringref"
	"github.com/andyjmorgan/Bifrost/internal/store"
	"github.com/andyjmorgan/Bifrost/internal/upstream"
)

// authServer is an authorization server whose refresh tokens rotate, and a
// resource that answers only to the access token currently in force. Each
// client has one grant.
type authServer struct {
	srv *httptest.Server

	mu        sync.Mutex
	n         int
	grants    map[string]*grant // by client id
	refreshes int
	replays   int // refreshes made with a token that had been retired
}

type grant struct {
	secret, access, refresh string
}

func newAuthServer(t *testing.T) *authServer {
	t.Helper()
	a := &authServer{grants: map[string]*grant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		a.mu.Lock()
		defer a.mu.Unlock()
		a.refreshes++
		g := a.grants[r.Form.Get("client_id")]
		_, secret, _ := r.BasicAuth()
		secret, _ = url.QueryUnescape(secret)
		if g == nil || g.secret != secret || r.Form.Get("resource") != a.srv.URL+"/mcp" || r.Form.Get("refresh_token") != g.refresh {
			if g != nil && r.Form.Get("refresh_token") != g.refresh {
				a.replays++
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"refused by the stub"}`)
			return
		}
		g.access, g.refresh = a.mint("access"), a.mint("refresh")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": g.access, "refresh_token": g.refresh, "token_type": "Bearer", "expires_in": 3600})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, g := range a.grants {
			if r.Header.Get("Authorization") == "Bearer "+g.access {
				_, _ = io.WriteString(w, "ok")
				return
			}
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	a.srv = httptest.NewServer(mux)
	t.Cleanup(a.srv.Close)
	return a
}

func (a *authServer) mint(kind string) string {
	a.n++
	return fmt.Sprintf("%s-%d-%s", kind, a.n, uuid.NewString())
}

// issue makes a grant for a client and returns its first token pair.
func (a *authServer) issue(clientID, secret string) (access, refresh string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	g := &grant{secret: secret, access: a.mint("access"), refresh: a.mint("refresh")}
	a.grants[clientID] = g
	return g.access, g.refresh
}

func (a *authServer) current(clientID string) (access, refresh string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.grants[clientID].access, a.grants[clientID].refresh
}

func (a *authServer) counts() (refreshes, replays int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.refreshes, a.replays
}

// oauthServer registers a server whose OAuth connection an older gateway
// sealed itself, with a live grant at as. expiry is when its access token ends.
func (r *rig) oauthServer(name string, as *authServer, clientSecret string, expiry time.Time) (uuid.UUID, string) {
	r.t.Helper()
	clientID := "client-" + r.tag + "-" + name
	access, refresh := as.issue(clientID, clientSecret)
	kind := "cimd"
	if clientSecret != "" {
		kind = "dcr"
	}
	refreshed := time.Now().Add(-time.Hour)
	id := r.server(name, credRow{
		Kind: store.AuthOAuth, OAuthIssuer: as.srv.URL, OAuthAuthorizationEndpoint: as.srv.URL + "/authorize", OAuthTokenEndpoint: as.srv.URL + "/token",
		OAuthRegistrationKind: kind, OAuthClientID: clientID, OAuthResource: as.srv.URL + "/mcp", OAuthScopes: []string{"mcp:tools"}, OAuthTokenType: "Bearer",
		OAuthExpiresAt: &expiry, OAuthHasRefreshToken: true, LastRefreshedAt: &refreshed,
	}, &sealed{AccessToken: access, RefreshToken: refresh, ClientSecret: clientSecret})
	// The server's URL is the resource, so a request through the connector lands on the stub.
	if _, err := r.pool.Exec(context.Background(), `update servers set url=$2 where id=$1`, id, as.srv.URL+"/mcp"); err != nil {
		r.t.Fatal(err)
	}
	return id, clientID
}

// connector is the gateway's upstream connector over this rig's database and
// keyring. It sends what the keyring hands it and refreshes nothing itself.
func (r *rig) connector() *upstream.Connector {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r.t.Cleanup(cancel)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return upstream.New(r.st, r.kr.Source(), audit.NewRecorder(ctx, r.st, log), log, 10*time.Second, "test")
}

// call makes one request to the server through the connector, as a forwarded
// request would, and returns the status.
func (r *rig) call(conn *upstream.Connector, id uuid.UUID) (int, error) {
	r.t.Helper()
	ctx := context.Background()
	srv, err := r.st.GetServer(ctx, id)
	if err != nil {
		r.t.Fatal(err)
	}
	hc, err := conn.HTTPClient(ctx, srv)
	if err != nil {
		return 0, err
	}
	resp, err := hc.Post(srv.URL, "application/json", strings.NewReader("{}"))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func (r *rig) exported(slug string) keyringref.ServerConnection {
	r.t.Helper()
	c, err := r.kr.Migration().ExportOAuth(context.Background(),
		keyringref.Ref{Owner: keyringref.GatewayOwner, Kind: keyringref.KindOAuth, Provider: "mcp-" + slug, Account: keyringref.ServerAccount}, keyringref.Use{Slug: slug})
	if err != nil {
		r.t.Fatalf("export %s: %v", slug, err)
	}
	return c
}

// noTokens fails the test when a report holds anything the stub issued.
func noTokens(t *testing.T, as *authServer, report string) {
	t.Helper()
	as.mu.Lock()
	defer as.mu.Unlock()
	for _, g := range as.grants {
		for _, v := range []string{g.access, g.refresh, g.secret} {
			if v != "" && strings.Contains(report, v) {
				t.Fatalf("the report carries a token or client secret:\n%s", report)
			}
		}
	}
}

// await receives from ch, or fails the test instead of hanging when the
// other side never gets there.
func await[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

// OAuth connections of every state move into the keyring, work from there,
// and come back with the tokens the keyring refreshed in the meantime.
func TestOAuthConnectionsMoveAndComeBack(t *testing.T) {
	r := newRig(t)
	as := newAuthServer(t)
	clientSecret := "client-secret-" + uuid.NewString()
	okID, okClient := r.oauthServer("ok", as, "", time.Now().Add(time.Hour))
	dcrID, dcrClient := r.oauthServer("dcr", as, clientSecret, time.Now().Add(time.Hour))
	brokenID, _ := r.oauthServer("broken", as, "", time.Now().Add(-time.Hour))
	if err := r.st.SetCredentialState(context.Background(), brokenID, store.StateBroken, "invalid_grant: revoked"); err != nil {
		t.Fatal(err)
	}
	pendingID := r.server("pending", credRow{Kind: store.AuthOAuth, OAuthTokenEndpoint: as.srv.URL + "/token", OAuthRegistrationKind: "dcr",
		OAuthClientID: "client-pending", OAuthResource: as.srv.URL + "/mcp"}, &sealed{ClientSecret: clientSecret})
	if err := r.st.SetCredentialState(context.Background(), pendingID, store.StatePending, "awaiting consent"); err != nil {
		t.Fatal(err)
	}
	headerID := r.server("header", credRow{Kind: store.AuthHeader, HeaderName: "X-Api-Key"}, &sealed{HeaderValue: headerValue})
	conn := r.connector()

	// Dry run.
	report, sum, err := r.run(credmigrate.Options{Kind: credmigrate.KindOAuth, DryRun: true}, nil)
	if err != nil || sum.Moved != 4 || sum.Skipped != 1 || len(lines(report, "would move")) != 4 {
		t.Fatalf("dry run: %v %+v\n%s", err, sum, report)
	}
	noTokens(t, as, report)
	if r.cred(okID).Kind != store.AuthOAuth {
		t.Fatal("a dry run moved a row")
	}

	// The move.
	report, sum, err = r.run(credmigrate.Options{Kind: credmigrate.KindOAuth}, nil)
	if err != nil || sum.Moved != 4 || sum.Skipped != 1 {
		t.Fatalf("move: %v %+v\n%s", err, sum, report)
	}
	noTokens(t, as, report)
	for name, id := range map[string]uuid.UUID{"ok": okID, "dcr": dcrID, "broken": brokenID, "pending": pendingID} {
		c := r.cred(id)
		want := keyringref.Ref{Owner: keyringref.GatewayOwner, Kind: keyringref.KindOAuth, Provider: "mcp-" + r.slug(name), Account: keyringref.ServerAccount}
		if c.Kind != store.AuthKeyringOAuth || c.KeyringRef() != want || r.authKind(id) != store.AuthKeyringOAuth || r.migratedFrom(id) != "oauth" {
			t.Fatalf("%s is %s %+v after the move", name, c.Kind, c.KeyringRef())
		}
		if len(c.SecretBlob) != 0 || c.OAuthClientID != "" || c.OAuthTokenEndpoint != "" || c.OAuthExpiresAt != nil || c.OAuthHasRefreshToken {
			t.Fatalf("%s kept part of its connection in the gateway's store", name)
		}
	}
	if s := r.cred(brokenID); s.State != store.StateBroken || s.StateReason != "invalid_grant: revoked" {
		t.Fatalf("the broken connection is now %s (%q)", s.State, s.StateReason)
	}
	if s := r.cred(pendingID); s.State != store.StatePending {
		t.Fatalf("the pending connection is now %s", s.State)
	}
	if r.cred(headerID).Kind != store.AuthHeader {
		t.Fatal("an OAuth run moved a header credential")
	}
	// What the keyring holds is what the gateway held.
	access, refresh := as.current(dcrClient)
	if c := r.exported(r.slug("dcr")); c.AccessToken != access || c.RefreshToken != refresh || c.ClientSecret != clientSecret || c.ClientID != dcrClient ||
		c.RegistrationKind != "dcr" || c.Resource != as.srv.URL+"/mcp" || c.TokenEndpoint != as.srv.URL+"/token" || len(c.Scopes) != 1 || c.Broken != "" {
		t.Fatal("the registered client's connection did not arrive whole")
	}
	if c := r.exported(r.slug("broken")); c.Broken != "invalid_grant: revoked" || !c.Connected {
		t.Fatalf("the broken connection arrived as %q", c.Broken)
	}
	if c := r.exported(r.slug("pending")); c.Connected || c.ClientID != "client-pending" || c.ClientSecret != clientSecret {
		t.Fatal("the pending connection did not arrive as a registration without tokens")
	}

	// Both live servers answer through the keyring, with no refresh needed.
	for name, id := range map[string]uuid.UUID{"ok": okID, "dcr": dcrID} {
		if status, err := r.call(conn, id); err != nil || status != http.StatusOK {
			t.Fatalf("%s through the keyring: %d %v", name, status, err)
		}
	}
	if n, _ := as.counts(); n != 0 {
		t.Fatalf("%d refreshes so far, want none", n)
	}
	// The broken one says so and is not refreshed.
	if _, err := r.call(conn, brokenID); !errors.Is(err, upstream.ErrCredentialBroken) {
		t.Fatalf("the broken connection: %v", err)
	}
	// The keyring refreshes the registered client's token with its secret
	// and the resource, and the rotation is accepted: the copy was live.
	oldAccess, _ := as.current(dcrClient)
	ref := keyringref.Ref{Owner: keyringref.GatewayOwner, Kind: keyringref.KindOAuth, Provider: "mcp-" + r.slug("dcr"), Account: keyringref.ServerAccount}
	if _, err := r.kr.Source().OAuthToken(context.Background(), ref, keyringref.Use{Slug: r.slug("dcr")}, oldAccess); err != nil {
		t.Fatalf("a refresh from the keyring: %v", err)
	}
	if n, replays := as.counts(); n != 1 || replays != 0 {
		t.Fatalf("%d refreshes, %d with a retired token", n, replays)
	}

	// A second run has nothing to do.
	if report, sum, err = r.run(credmigrate.Options{Kind: credmigrate.KindOAuth}, nil); err != nil || sum.Moved != 0 || sum.Already != 4 {
		t.Fatalf("second run: %v %+v\n%s", err, sum, report)
	}

	// The rollback brings back the tokens as the keyring last refreshed them.
	report, sum, err = r.run(credmigrate.Options{Kind: credmigrate.KindOAuth, Rollback: true}, nil)
	if err != nil || sum.Moved != 4 {
		t.Fatalf("rollback: %v %+v\n%s", err, sum, report)
	}
	noTokens(t, as, report)
	access, refresh = as.current(dcrClient)
	c := r.cred(dcrID)
	if got := r.opened(dcrID); c.Kind != store.AuthOAuth || got.AccessToken != access || got.RefreshToken != refresh || got.ClientSecret != clientSecret ||
		c.OAuthClientID != dcrClient || c.OAuthRegistrationKind != "dcr" || c.OAuthResource != as.srv.URL+"/mcp" || !c.OAuthHasRefreshToken || r.migratedFrom(dcrID) != "" {
		t.Fatalf("the registered client's connection did not come back whole: %s", c.Kind)
	}
	if s := r.cred(brokenID); s.Kind != store.AuthOAuth || s.State != store.StateBroken || s.StateReason != "invalid_grant: revoked" {
		t.Fatalf("the broken connection came back %s/%s", s.Kind, s.State)
	}
	if s := r.cred(pendingID); s.Kind != store.AuthOAuth || s.State != store.StatePending || r.opened(pendingID).ClientSecret != clientSecret {
		t.Fatalf("the pending connection came back %s/%s", s.Kind, s.State)
	}
	if _, err := r.kr.Migration().ExportOAuth(context.Background(), ref, keyringref.Use{}); !errors.Is(err, keyringref.ErrNotFound) {
		t.Fatalf("the keyring still holds a connection after the rollback: %v", err)
	}
	// The other live server came back with the tokens its authorization
	// server still honours: the rollback rotated nothing and lost nothing.
	access, refresh = as.current(okClient)
	if got := r.opened(okID); r.cred(okID).Kind != store.AuthOAuth || got.AccessToken != access || got.RefreshToken != refresh {
		t.Fatal("the other connection did not come back with its live tokens")
	}
	if n, replays := as.counts(); n != 1 || replays != 0 {
		t.Fatalf("%d refreshes, %d with a retired token; want the keyring's one and none", n, replays)
	}
	// This test used to end with the gateway refreshing the restored row
	// itself and sending a request through it. The gateway no longer reads
	// its own store, so there is nothing of that left to exercise: a restored
	// row is what an older gateway uses, and this one refuses to send it.
	for name, id := range map[string]uuid.UUID{"ok": okID, "dcr": dcrID} {
		if _, err := r.call(conn, id); !errors.Is(err, upstream.ErrNotMigrated) {
			t.Fatalf("%s after the rollback: %v, want ErrNotMigrated", name, err)
		}
	}
	if n, _ := as.counts(); n != 1 {
		t.Fatalf("a request through a restored row reached the token endpoint (%d refreshes)", n)
	}
}

// Two tests stood here: a gateway refresh already in flight when the command
// starts (TestARefreshInFlightFinishesBeforeTheMove), and a request that
// needs a refresh while the command is moving its server
// (TestARefreshWaitsForTheMove). Both raced the gateway's own token refresh
// against the move. The gateway no longer refreshes a token itself, so that
// race cannot happen in this binary and there is nothing to run them
// against. The scenario was covered in the commit that introduced the
// refresh guard, where the tests failed without it; an older gateway that
// still refreshes is kept out by the session check
// (TestOAuthMoveRefusesBesideAnOlderGateway). The keyring's refresh still
// races a rollback, which the next test covers.

// slowExport makes the read out of the keyring take a while, and says when
// it has started.
type slowExport struct {
	credmigrate.Vault
	started chan struct{}
	hold    time.Duration
}

func (v *slowExport) ExportOAuth(ctx context.Context, ref keyringref.Ref, use keyringref.Use) (keyringref.ServerConnection, error) {
	c, err := v.Vault.ExportOAuth(ctx, ref, use)
	close(v.started)
	time.Sleep(v.hold)
	return c, err
}

// A rollback while the keyring is asked for a token that is due. The
// keyring's refresh waits for the rollback, finds the account gone and
// refreshes nothing, so the restored row holds tokens that are still live.
func TestAKeyringRefreshWaitsForTheRollback(t *testing.T) {
	r := newRig(t)
	as := newAuthServer(t)
	id, client := r.oauthServer("back", as, "", time.Now().Add(-time.Minute)) // due for a refresh
	if report, sum, err := r.run(credmigrate.Options{Kind: credmigrate.KindOAuth}, nil); err != nil || sum.Moved != 1 {
		t.Fatalf("move: %v %+v\n%s", err, sum, report)
	}
	v := &slowExport{Vault: r.kr.Migration(), started: make(chan struct{}), hold: 400 * time.Millisecond}
	restored := make(chan error, 1)
	go func() {
		_, _, err := r.run(credmigrate.Options{Kind: credmigrate.KindOAuth, Rollback: true}, v)
		restored <- err
	}()
	await(t, v.started, "the rollback to read the tokens out") // it holds the guard

	ref := keyringref.Ref{Owner: keyringref.GatewayOwner, Kind: keyringref.KindOAuth, Provider: "mcp-" + r.slug("back"), Account: keyringref.ServerAccount}
	started := time.Now()
	_, err := r.kr.Source().OAuthToken(context.Background(), ref, keyringref.Use{Slug: r.slug("back")}, "")
	if !errors.Is(err, keyringref.ErrNotFound) {
		t.Fatalf("the keyring's answer after the rollback took the account: %v", err)
	}
	if waited := time.Since(started); waited < 200*time.Millisecond {
		t.Fatalf("the keyring's refresh did not wait for the rollback (it took %s)", waited)
	}
	if err := await(t, restored, "the rollback to finish"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if n, _ := as.counts(); n != 0 {
		t.Fatalf("%d refreshes during the rollback, want none", n)
	}
	// The restored row is live: it seals the token pair the authorization
	// server currently honours, which nothing has rotated. (This used to be
	// shown by a request through the row, refreshed by the gateway itself;
	// the gateway no longer reads its own store.)
	access, refresh := as.current(client)
	if got := r.opened(id); r.cred(id).Kind != store.AuthOAuth || got.AccessToken != access || got.RefreshToken != refresh {
		t.Fatal("the restored row does not seal the tokens the authorization server honours")
	}
	if n, replays := as.counts(); n != 0 || replays != 0 {
		t.Fatalf("%d refreshes, %d with a retired token; want none of either", n, replays)
	}
}

// An OAuth run stops when one server fails and is finished by the next run.
func TestAnInterruptedOAuthRunResumes(t *testing.T) {
	r := newRig(t)
	as := newAuthServer(t)
	firstID, _ := r.oauthServer("a", as, "", time.Now().Add(time.Hour))
	secondID, secondClient := r.oauthServer("b", as, "", time.Now().Add(time.Hour))
	v := &failingOAuthVault{Vault: r.kr.Migration(), fail: r.slug("b")}

	report, sum, err := r.run(credmigrate.Options{Kind: credmigrate.KindOAuth}, v)
	if !errors.Is(err, credmigrate.ErrFailed) || sum.Moved != 1 || sum.Failed != 1 {
		t.Fatalf("first run: %v %+v\n%s", err, sum, report)
	}
	if r.cred(firstID).Kind != store.AuthKeyringOAuth || r.cred(secondID).Kind != store.AuthOAuth {
		t.Fatal("the servers are not one moved, one as it was")
	}
	// The one that failed half way still works from the gateway's store: the
	// keyring's half-written copy is not in use.
	access, _ := as.current(secondClient)
	if got := r.opened(secondID); got.AccessToken != access {
		t.Fatal("the server that failed lost its tokens")
	}
	v.fail = ""
	if report, sum, err = r.run(credmigrate.Options{Kind: credmigrate.KindOAuth}, v); err != nil || sum.Moved != 1 || sum.Already != 1 {
		t.Fatalf("resumed run: %v %+v\n%s", err, sum, report)
	}
	if c := r.exported(r.slug("b")); c.AccessToken != access {
		t.Fatal("the resumed server's tokens are not in the keyring")
	}
}

// failingOAuthVault writes the copy and then reports a failure, as a
// process killed between the copy and the switch would leave things.
type failingOAuthVault struct {
	credmigrate.Vault
	fail string
}

func (v *failingOAuthVault) ImportOAuth(ctx context.Context, owner, tenant uuid.UUID, use keyringref.Use, c keyringref.ServerConnection) (keyringref.Ref, error) {
	ref, err := v.Vault.ImportOAuth(ctx, owner, tenant, use, c)
	if use.Slug == v.fail {
		return keyringref.Ref{}, errors.New("stopped after the copy")
	}
	return ref, err
}

// The command will not move OAuth rows while a session that is not this
// version of Bifrost is connected: it could be a gateway that refreshes
// without waiting.
func TestOAuthMoveRefusesBesideAnOlderGateway(t *testing.T) {
	r := newRig(t)
	r.strictSessions = true
	as := newAuthServer(t)
	id, _ := r.oauthServer("guarded", as, "", time.Now().Add(time.Hour))

	// A session that names itself as nothing: what an older gateway looks like.
	older, err := pgx.Connect(context.Background(), dsnWithoutName(t))
	if err != nil {
		t.Fatal(err)
	}
	defer older.Close(context.Background())

	report, _, err := r.run(credmigrate.Options{Kind: credmigrate.KindOAuth}, nil)
	if err == nil || !strings.Contains(err.Error(), "refusing to move OAuth credentials") || !strings.Contains(report, "(unnamed)") {
		t.Fatalf("the command ran beside an unnamed session: %v\n%s", err, report)
	}
	if r.cred(id).Kind != store.AuthOAuth {
		t.Fatal("a refused run moved a row")
	}
	// A dry run only reports it; a static run is not concerned.
	if report, _, err = r.run(credmigrate.Options{Kind: credmigrate.KindOAuth, DryRun: true}, nil); err != nil || !strings.Contains(report, "(unnamed)") {
		t.Fatalf("dry run: %v\n%s", err, report)
	}
	if _, _, err = r.run(credmigrate.Options{Kind: credmigrate.KindStatic}, nil); err != nil {
		t.Fatalf("a static run was refused: %v", err)
	}
	// Told that the session is something else, it goes ahead.
	r.strictSessions = false
	if report, sum, err := r.run(credmigrate.Options{Kind: credmigrate.KindOAuth}, nil); err != nil || sum.Moved != 1 {
		t.Fatalf("with the sessions vouched for: %v %+v\n%s", err, sum, report)
	}
}

// dsnWithoutName is the test database as a client that does not name itself reaches it.
func dsnWithoutName(t *testing.T) string {
	t.Helper()
	return os.Getenv("MCPGW_TEST_DSN")
}

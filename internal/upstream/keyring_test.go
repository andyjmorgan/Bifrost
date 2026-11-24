package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/audit"
	"github.com/andyjmorgan/Bifrost/internal/keyringref"
	"github.com/andyjmorgan/Bifrost/internal/store"
)

// Values a test must never find in a log line or an audit payload. They are
// assembled at run time so nothing credential-shaped sits in the source.
var (
	tokenOne   = strings.Join([]string{"access", "one", "fixture"}, "-")
	tokenTwo   = strings.Join([]string{"access", "two", "fixture"}, "-")
	secretOne  = strings.Join([]string{"stored", "secret", "fixture"}, "-")
	testOwner  = "0b9f4a52-5d0e-4b0a-9a55-2f6f1f3c7a11"
	oauthRef   = keyringref.Ref{Owner: testOwner, Kind: keyringref.KindOAuth, Provider: "x", Account: "alice"}
	secretRef  = keyringref.Ref{Owner: testOwner, Kind: keyringref.KindSecret, Secret: "atlas-key"}
	everything = []string{tokenOne, tokenTwo, secretOne}
)

// ---- fakes ---------------------------------------------------------------------

// fakeKeyring is the keyring as the connector sees it. It hands out the
// current token and header, and remembers what it was asked.
type fakeKeyring struct {
	mu      sync.Mutex
	token   keyringref.Token
	onRetry *keyringref.Token // what a rejected token is replaced with; nil means it cannot be refreshed
	header  keyringref.Header
	err     error

	refs     []keyringref.Ref
	uses     []keyringref.Use
	rejected []string
}

func (f *fakeKeyring) Check(context.Context, keyringref.Ref) error { return f.err }

func (f *fakeKeyring) OAuthToken(_ context.Context, ref keyringref.Ref, use keyringref.Use, rejected string) (keyringref.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refs, f.uses, f.rejected = append(f.refs, ref), append(f.uses, use), append(f.rejected, rejected)
	if f.err != nil {
		return keyringref.Token{}, f.err
	}
	if rejected != "" {
		if f.onRetry == nil {
			return keyringref.Token{}, keyringref.ErrNoRefresh
		}
		f.token = *f.onRetry
	}
	return f.token, nil
}

func (f *fakeKeyring) SecretHeader(_ context.Context, ref keyringref.Ref, use keyringref.Use) (keyringref.Header, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refs, f.uses, f.rejected = append(f.refs, ref), append(f.uses, use), append(f.rejected, "")
	return f.header, f.err
}

func (f *fakeKeyring) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.rejected...)
}

func (f *fakeKeyring) set(fn func(*fakeKeyring)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// credStore holds the one credential a test's server has.
type credStore struct{ cred *store.Credential }

func (s credStore) GetCredential(context.Context, uuid.UUID) (*store.Credential, error) {
	if s.cred == nil {
		return nil, store.ErrNotFound
	}
	c := *s.cred
	return &c, nil
}

func (credStore) SetCredentialState(context.Context, uuid.UUID, store.CredentialState, string) error {
	return nil
}

// auditSink keeps what the gateway's own audit trail was given.
type auditSink struct {
	mu       sync.Mutex
	payloads []*store.AuditPayload
}

func (a *auditSink) InsertAuditEvent(_ context.Context, _ *store.AuditEvent, p *store.AuditPayload) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.payloads = append(a.payloads, p)
	return nil
}

// wait returns the payloads once n have been written.
func (a *auditSink) wait(t *testing.T, n int) []*store.AuditPayload {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		a.mu.Lock()
		got := append([]*store.AuditPayload(nil), a.payloads...)
		a.mu.Unlock()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("audit: %d payloads written, want %d", len(got), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// syncBuffer is a log destination a test can read while the code writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// seen is one request as the fake MCP server received it.
type seen struct {
	header http.Header
	body   string
}

// fakeUpstream stands in for an MCP server. answer decides the status of
// each request from its headers; the default is 200.
type fakeUpstream struct {
	*httptest.Server
	mu     sync.Mutex
	got    []seen
	answer func(http.Header) int
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	u := &fakeUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.got = append(u.got, seen{header: r.Header.Clone(), body: string(body)})
		answer := u.answer
		u.mu.Unlock()
		status := http.StatusOK
		if answer != nil {
			status = answer(r.Header)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"status":"answered"}`)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *fakeUpstream) requests() []seen {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]seen(nil), u.got...)
}

// rig is a connector over one server with one credential.
type rig struct {
	conn  *Connector
	srv   *store.Server
	up    *fakeUpstream
	audit *auditSink
	logs  *syncBuffer
}

func newKeyringRig(t *testing.T, kr keyringref.Keyring, cred *store.Credential) *rig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	logs := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sink := &auditSink{}
	up := newFakeUpstream(t)
	srv := &store.Server{ID: uuid.New(), Slug: "xmcp", Name: "X", URL: up.URL, AuthKind: cred.Kind, Enabled: true}
	cred.ServerID = srv.ID
	conn := New(credStore{cred: cred}, kr, audit.NewRecorder(ctx, sink, log), log, 5*time.Second, "test")
	return &rig{conn: conn, srv: srv, up: up, audit: sink, logs: logs}
}

// client is a new HTTP client for the server: what one forwarded request
// gets.
func (r *rig) client(t *testing.T) *http.Client {
	t.Helper()
	hc, err := r.conn.HTTPClient(context.Background(), r.srv)
	if err != nil {
		t.Fatal(err)
	}
	return hc
}

func post(t *testing.T, hc *http.Client, url, body string) *http.Response {
	t.Helper()
	resp, err := hc.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func drain(resp *http.Response) string {
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(b)
}

// noSecrets fails the test when anything the gateway wrote down (its log,
// its audit payloads) holds a token or a secret.
func (r *rig) noSecrets(t *testing.T, payloads []*store.AuditPayload) {
	t.Helper()
	written, _ := json.Marshal(payloads)
	for _, secret := range everything {
		if strings.Contains(r.logs.String(), secret) {
			t.Errorf("the log holds a credential: %s", r.logs.String())
		}
		if strings.Contains(string(written), secret) {
			t.Errorf("an audit payload holds a credential: %s", written)
		}
	}
}

func oauthCred() *store.Credential {
	return &store.Credential{Kind: store.AuthKeyringOAuth, State: store.StateOK, KeyringOwner: testOwner, KeyringProvider: "x", KeyringAccount: "alice"}
}

func secretCred() *store.Credential {
	return &store.Credential{Kind: store.AuthKeyringSecret, State: store.StateOK, KeyringOwner: testOwner, KeyringSecretName: "atlas-key"}
}

// ---- keyring OAuth account -----------------------------------------------------

func TestKeyringOAuthSendsTheAccountsToken(t *testing.T) {
	kr := &fakeKeyring{token: keyringref.Token{AccessToken: tokenOne}}
	r := newKeyringRig(t, kr, oauthCred())

	hc := r.client(t)
	for range 2 {
		if resp := post(t, hc, r.up.URL, `{"n":1}`); resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		} else {
			drain(resp)
		}
	}
	for i, req := range r.up.requests() {
		if got := req.header.Get("Authorization"); got != "Bearer "+tokenOne {
			t.Fatalf("request %d carried %q", i, got)
		}
	}
	// One client is one forwarded request: the keyring is asked once for it.
	if asked := kr.asked(); len(asked) != 1 || asked[0] != "" {
		t.Fatalf("the keyring was asked %q, want once and without a rejected token", asked)
	}
	if kr.refs[0] != oauthRef {
		t.Fatalf("asked for %+v, want %+v", kr.refs[0], oauthRef)
	}
	if use := kr.uses[0]; use.Slug != "xmcp" || use.ServerID != r.srv.ID.String() {
		t.Fatalf("the keyring was told %+v, want the server", use)
	}

	// The account's token changes in the keyring: the next forwarded request
	// sends the new one, with nothing carried over.
	kr.set(func(f *fakeKeyring) { f.token = keyringref.Token{AccessToken: tokenTwo} })
	drain(post(t, r.client(t), r.up.URL, `{"n":2}`))
	reqs := r.up.requests()
	if got := reqs[len(reqs)-1].header.Get("Authorization"); got != "Bearer "+tokenTwo {
		t.Fatalf("after the keyring changed, the request carried %q", got)
	}
	r.noSecrets(t, r.audit.wait(t, 3))
}

func TestKeyringOAuthTokenIsNotKeptPastItsExpiry(t *testing.T) {
	gone := time.Now().Add(-time.Second)
	kr := &fakeKeyring{token: keyringref.Token{AccessToken: tokenOne, ExpiresAt: &gone}}
	r := newKeyringRig(t, kr, oauthCred())

	hc := r.client(t)
	drain(post(t, hc, r.up.URL, `{}`))
	drain(post(t, hc, r.up.URL, `{}`))
	if asked := kr.asked(); len(asked) != 2 {
		t.Fatalf("an expired token was reused: the keyring was asked %d times, want 2", len(asked))
	}

	later := time.Now().Add(time.Hour)
	kr.set(func(f *fakeKeyring) { f.token.ExpiresAt = &later })
	hc = r.client(t)
	drain(post(t, hc, r.up.URL, `{}`))
	drain(post(t, hc, r.up.URL, `{}`))
	if asked := kr.asked(); len(asked) != 3 {
		t.Fatalf("a live token was fetched again within one client: asked %d times, want 3", len(asked))
	}
}

func TestKeyringOAuthRefreshesOnceAfterA401(t *testing.T) {
	kr := &fakeKeyring{token: keyringref.Token{AccessToken: tokenOne}, onRetry: &keyringref.Token{AccessToken: tokenTwo}}
	r := newKeyringRig(t, kr, oauthCred())
	r.up.answer = func(h http.Header) int {
		if h.Get("Authorization") == "Bearer "+tokenTwo {
			return http.StatusOK
		}
		return http.StatusUnauthorized
	}

	hc := r.client(t)
	resp := post(t, hc, r.up.URL, `{"call":"once"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d after the retry, want 200", resp.StatusCode)
	}
	drain(resp)
	reqs := r.up.requests()
	if len(reqs) != 2 {
		t.Fatalf("the server saw %d requests, want the first and one retry", len(reqs))
	}
	if reqs[0].header.Get("Authorization") != "Bearer "+tokenOne || reqs[1].header.Get("Authorization") != "Bearer "+tokenTwo {
		t.Fatal("the retry did not carry the refreshed token")
	}
	if reqs[1].body != `{"call":"once"}` {
		t.Fatalf("the retry sent body %q", reqs[1].body)
	}
	if asked := kr.asked(); len(asked) != 2 || asked[0] != "" || asked[1] != tokenOne {
		t.Fatalf("the keyring was asked %d times; the second must name the rejected token", len(asked))
	}

	// The same client keeps the refreshed token for its later requests.
	drain(post(t, hc, r.up.URL, `{}`))
	if asked := kr.asked(); len(asked) != 2 {
		t.Fatalf("the refreshed token was not kept: asked %d times", len(asked))
	}
	r.noSecrets(t, r.audit.wait(t, 3))
}

func TestKeyringOAuthRetriesOnlyOnce(t *testing.T) {
	kr := &fakeKeyring{token: keyringref.Token{AccessToken: tokenOne}, onRetry: &keyringref.Token{AccessToken: tokenTwo}}
	r := newKeyringRig(t, kr, oauthCred())
	r.up.answer = func(http.Header) int { return http.StatusUnauthorized }

	resp := post(t, r.client(t), r.up.URL, `{}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want the server's 401", resp.StatusCode)
	}
	drain(resp)
	if n := len(r.up.requests()); n != 2 {
		t.Fatalf("the server saw %d requests, want 2", n)
	}
	if asked := kr.asked(); len(asked) != 2 {
		t.Fatalf("the keyring was asked %d times, want 2", len(asked))
	}
}

func TestKeyringOAuthA401StandsWhenTheAccountCannotRefresh(t *testing.T) {
	kr := &fakeKeyring{token: keyringref.Token{AccessToken: tokenOne}} // onRetry nil: no refresh token
	r := newKeyringRig(t, kr, oauthCred())
	r.up.answer = func(http.Header) int { return http.StatusUnauthorized }

	resp := post(t, r.client(t), r.up.URL, `{}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want the server's 401", resp.StatusCode)
	}
	if body := drain(resp); !strings.Contains(body, "answered") {
		t.Fatalf("the server's answer was lost: %q", body)
	}
	if n := len(r.up.requests()); n != 1 {
		t.Fatalf("the server saw %d requests, want 1", n)
	}
	r.noSecrets(t, r.audit.wait(t, 1))
}

// ---- keyring secret ------------------------------------------------------------

func TestKeyringSecretIsSentAsItDescribesItself(t *testing.T) {
	for name, h := range map[string]keyringref.Header{
		"its own header and prefix": {Name: "X-Atlas-Credential", Value: "Key " + secretOne},
		"http basic":                {Name: "Authorization", Value: "Basic " + secretOne},
	} {
		t.Run(name, func(t *testing.T) {
			kr := &fakeKeyring{header: h}
			r := newKeyringRig(t, kr, secretCred())

			hc := r.client(t)
			drain(post(t, hc, r.up.URL, `{}`))
			drain(post(t, hc, r.up.URL, `{}`))
			for i, req := range r.up.requests() {
				if got := req.header.Get(h.Name); got != h.Value {
					t.Fatalf("request %d carried %q in %s", i, got, h.Name)
				}
			}
			if asked := kr.asked(); len(asked) != 1 {
				t.Fatalf("the keyring was asked %d times for one client, want 1", len(asked))
			}
			if kr.refs[0] != secretRef {
				t.Fatalf("asked for %+v, want %+v", kr.refs[0], secretRef)
			}

			// The secret chose its header, so the gateway's audit trail has
			// to mask a name it had never heard of.
			payloads := r.audit.wait(t, 2)
			for _, p := range payloads {
				if got := p.RequestHeaders[h.Name]; got != "[redacted]" {
					t.Fatalf("the audit payload kept %s as %q", h.Name, got)
				}
			}
			r.noSecrets(t, payloads)

			// A changed secret is what the next forwarded request sends.
			kr.set(func(f *fakeKeyring) { f.header.Value = "rotated" })
			drain(post(t, r.client(t), r.up.URL, `{}`))
			reqs := r.up.requests()
			if got := reqs[len(reqs)-1].header.Get(h.Name); got != "rotated" {
				t.Fatalf("after the secret changed, the request carried %q", got)
			}
		})
	}
}

// ---- failures ------------------------------------------------------------------

func TestKeyringCredentialFailsClearlyWhenItIsGone(t *testing.T) {
	for name, cred := range map[string]*store.Credential{"oauth account": oauthCred(), "secret": secretCred()} {
		t.Run(name, func(t *testing.T) {
			kr := &fakeKeyring{err: keyringref.ErrNotFound}
			r := newKeyringRig(t, kr, cred)

			_, err := r.client(t).Post(r.up.URL, "application/json", strings.NewReader(`{}`))
			if err == nil {
				t.Fatal("the request went out without a credential")
			}
			if !errors.Is(err, keyringref.ErrNotFound) || !strings.Contains(err.Error(), "keyring credential unavailable") {
				t.Fatalf("the error does not say what happened: %v", err)
			}
			if n := len(r.up.requests()); n != 0 {
				t.Fatalf("the server saw %d requests, want none", n)
			}
			if !strings.Contains(r.logs.String(), "keyring credential unavailable") {
				t.Fatal("the failure was not logged")
			}
		})
	}
}

func TestKeyringOffRefusesAKeyringCredential(t *testing.T) {
	for name, cred := range map[string]*store.Credential{"oauth account": oauthCred(), "secret": secretCred()} {
		t.Run(name, func(t *testing.T) {
			r := newKeyringRig(t, nil, cred)
			_, err := r.conn.HTTPClient(context.Background(), r.srv)
			if !errors.Is(err, ErrKeyringOff) {
				t.Fatalf("got %v, want ErrKeyringOff", err)
			}
			if _, err := r.conn.Open(context.Background(), r.srv); !errors.Is(err, ErrKeyringOff) {
				t.Fatalf("opening a session: got %v, want ErrKeyringOff", err)
			}
			if n := len(r.up.requests()); n != 0 {
				t.Fatalf("the server saw %d requests, want none", n)
			}
		})
	}
}

// A credential row the gateway sealed in its own store, which `gateway
// migrate-credentials` has not moved, is not sent and not guessed at: the
// server fails with ErrNotMigrated, with or without a keyring, and nothing
// goes upstream.
func TestACredentialNotMovedToTheKeyringIsRefused(t *testing.T) {
	for _, kind := range []store.AuthKind{store.AuthBasic, store.AuthHeader, store.AuthOAuth} {
		for name, kr := range map[string]keyringref.Keyring{"keyring on": &fakeKeyring{token: keyringref.Token{AccessToken: tokenOne}}, "keyring off": nil} {
			t.Run(string(kind)+", "+name, func(t *testing.T) {
				// The state does not matter: a pending or broken OAuth row of
				// the old kind is just as unmoved.
				for _, state := range []store.CredentialState{store.StateOK, store.StatePending, store.StateBroken} {
					r := newKeyringRig(t, kr, &store.Credential{Kind: kind, State: state})
					_, err := r.conn.HTTPClient(context.Background(), r.srv)
					if !errors.Is(err, ErrNotMigrated) {
						t.Fatalf("%s: got %v, want ErrNotMigrated", state, err)
					}
					if !strings.Contains(err.Error(), "gateway migrate-credentials") {
						t.Fatalf("the error does not name the command: %v", err)
					}
					if _, err := r.conn.Open(context.Background(), r.srv); !errors.Is(err, ErrNotMigrated) {
						t.Fatalf("%s: opening a session: got %v, want ErrNotMigrated", state, err)
					}
					if n := len(r.up.requests()); n != 0 {
						t.Fatalf("%s: the server saw %d requests, want none", state, n)
					}
					// The warning names the server and what its row still is.
					logs := r.logs.String()
					if !strings.Contains(logs, `"slug":"xmcp"`) || !strings.Contains(logs, `"kind":"`+string(kind)+`"`) || !strings.Contains(logs, `"level":"WARN"`) {
						t.Fatalf("the refusal was not logged with the slug and kind: %s", logs)
					}
				}
			})
		}
	}
}

// A transport that was handed such a row all the same sends nothing either.
func TestTheCredentialMiddlewareSendsNothingForAnUnmovedRow(t *testing.T) {
	for _, kind := range []store.AuthKind{store.AuthBasic, store.AuthHeader, store.AuthOAuth} {
		r := newKeyringRig(t, nil, &store.Credential{Kind: store.AuthNone, State: store.StateOK})
		ct := &credentialTransport{c: r.conn, srv: r.srv, cred: &store.Credential{ServerID: r.srv.ID, Kind: kind, State: store.StateOK}, next: http.DefaultTransport}
		req, err := http.NewRequest(http.MethodPost, r.up.URL, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if resp, err := ct.RoundTrip(req); !errors.Is(err, ErrNotMigrated) {
			if resp != nil {
				resp.Body.Close()
			}
			t.Fatalf("%s: got %v, want ErrNotMigrated", kind, err)
		}
		if n := len(r.up.requests()); n != 0 {
			t.Fatalf("%s: the server saw %d requests, want none", kind, n)
		}
	}
}

// A server without a credential is sent none, keyring or no keyring.
func TestAServerWithoutACredentialIsSentNone(t *testing.T) {
	none := newKeyringRig(t, nil, &store.Credential{Kind: store.AuthNone, State: store.StateOK})
	drain(post(t, none.client(t), none.up.URL, `{}`))
	if got := none.up.requests()[0].header.Get("Authorization"); got != "" {
		t.Fatalf("a server without a credential was sent %q", got)
	}
}

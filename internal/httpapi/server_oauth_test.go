package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/internal/keyring"
	"github.com/andyjmorgan/Bifrost/internal/keyring/store/memstore"
	"github.com/andyjmorgan/Bifrost/internal/keyringref"
	"github.com/andyjmorgan/Bifrost/internal/store"
)

var gatewayOwnerID = uuid.MustParse(keyringref.GatewayOwner)

// oauthUpstream is an MCP server that wants a bearer token, and the
// authorization server that issues it. Refresh tokens rotate: each refresh
// retires the one it was made with.
type oauthUpstream struct {
	t   *testing.T
	srv *httptest.Server
	dcr bool // no Client ID Metadata Documents: the client must register

	mu           sync.Mutex
	n            int
	codes        map[string]url.Values // code -> the authorization request
	clients      map[string]string     // registered client id -> secret
	access       string
	refresh      string
	expiresIn    int
	refreshes    int
	refuse       string // an OAuth error every refresh is answered with
	unavailable  bool   // the token endpoint answers 503
	registered   int
	lastResource string
}

func (u *oauthUpstream) mcpURL() string { return u.srv.URL + "/mcp" }

// mint makes a token-shaped value no source file contains.
func (u *oauthUpstream) mint(kind string) string {
	u.n++
	return fmt.Sprintf("%s-%d-%s", kind, u.n, uuid.NewString())
}

func newOAuthUpstream(t *testing.T, dcr bool) *oauthUpstream {
	t.Helper()
	u := &oauthUpstream{t: t, dcr: dcr, codes: map[string]url.Values{}, clients: map[string]string{}, expiresIn: 3600}
	server := mcp.NewServer(&mcp.Implementation{Name: "oauth-stub", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "hello", Description: "says hello"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "hello"}}}, nil, nil
	})
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	mux := http.NewServeMux()
	u.srv = httptest.NewServer(mux)
	t.Cleanup(u.srv.Close)
	base := u.srv.URL
	js := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		ok := u.access != "" && r.Header.Get("Authorization") == "Bearer "+u.access
		u.mu.Unlock()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+base+`/.well-known/oauth-protected-resource/mcp", scope="mcp:tools"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, _ *http.Request) {
		js(w, map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base + "/as"}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server/as", func(w http.ResponseWriter, _ *http.Request) {
		md := map[string]any{"issuer": base + "/as", "authorization_endpoint": base + "/as/authorize", "token_endpoint": base + "/as/token"}
		if u.dcr {
			md["registration_endpoint"] = base + "/as/register"
		} else {
			md["client_id_metadata_document_supported"] = true
		}
		js(w, md)
	})
	mux.HandleFunc("/as/register", func(w http.ResponseWriter, _ *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		id, secret := u.mint("client"), u.mint("client-secret")
		u.clients[id] = secret
		u.registered++
		w.WriteHeader(http.StatusCreated)
		js(w, map[string]any{"client_id": id, "client_secret": secret})
	})
	mux.HandleFunc("/as/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		u.mu.Lock()
		defer u.mu.Unlock()
		refuse := func(code string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": "refused by the stub"})
		}
		if u.unavailable {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		u.lastResource = r.Form.Get("resource")
		if u.lastResource != base+"/mcp" {
			refuse("invalid_target")
			return
		}
		if u.dcr {
			id, secret, ok := r.BasicAuth()
			id, _ = url.QueryUnescape(id)
			secret, _ = url.QueryUnescape(secret)
			if !ok || u.clients[id] == "" || u.clients[id] != secret {
				refuse("invalid_client")
				return
			}
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			req, ok := u.codes[r.Form.Get("code")]
			delete(u.codes, r.Form.Get("code"))
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if !ok || req.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(sum[:]) || req.Get("redirect_uri") != r.Form.Get("redirect_uri") || req.Get("client_id") != r.Form.Get("client_id") {
				refuse("invalid_grant")
				return
			}
		case "refresh_token":
			u.refreshes++
			if u.refuse != "" {
				refuse(u.refuse)
				return
			}
			if r.Form.Get("refresh_token") != u.refresh {
				refuse("invalid_grant")
				return
			}
		default:
			refuse("unsupported_grant_type")
			return
		}
		u.access, u.refresh = u.mint("access"), u.mint("refresh")
		js(w, map[string]any{"access_token": u.access, "refresh_token": u.refresh, "token_type": "Bearer", "expires_in": u.expiresIn, "scope": "mcp:tools"})
	})
	return u
}

// consent plays the browser: it takes the authorization URL the gateway
// returned, checks what the gateway asked for, and returns the callback
// query the authorization server would send the browser back with.
func (u *oauthUpstream) consent(authorizeURL string) url.Values {
	u.t.Helper()
	au, err := url.Parse(authorizeURL)
	if err != nil || !strings.HasPrefix(authorizeURL, u.srv.URL+"/as/authorize?") {
		u.t.Fatalf("the authorization URL is not the discovered one: %q", authorizeURL)
	}
	q := au.Query()
	if q.Get("resource") != u.mcpURL() || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("response_type") != "code" {
		u.t.Fatalf("the authorization request lacks the resource or PKCE: %v", q)
	}
	// The gateway's public identity must not move: every authorization
	// server already has these two on file.
	if q.Get("redirect_uri") != "http://gateway.test/oauth/callback" {
		u.t.Fatalf("redirect_uri is %q", q.Get("redirect_uri"))
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.dcr && q.Get("client_id") != "http://gateway.test/.well-known/mcp-client-metadata.json" {
		u.t.Fatalf("client_id is %q, want the gateway's client metadata document", q.Get("client_id"))
	}
	if u.dcr && u.clients[q.Get("client_id")] == "" {
		u.t.Fatalf("client_id %q was never registered", q.Get("client_id"))
	}
	code := u.mint("code")
	u.codes[code] = q
	return url.Values{"code": {code}, "state": {q.Get("state")}}
}

func (u *oauthUpstream) set(fn func()) {
	u.mu.Lock()
	defer u.mu.Unlock()
	fn()
}

func (u *oauthUpstream) count() (refreshes int, access string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.refreshes, u.access
}

// oauthRig is the gateway's API with the real keyring, on its in-memory
// store, behind it.
type oauthRig struct {
	*apiRig
	ms *memstore.Mem
	up *oauthUpstream
}

func newOAuthRig(t *testing.T, dcr bool) *oauthRig {
	t.Helper()
	ms := memstore.New()
	kr, err := keyring.New(context.Background(), &keyring.Config{
		ActiveKekID: "local:v1", Keks: map[string]string{"local:v1": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
		OAuthRedirectBaseURL: "http://gateway.test/keyring", UIBaseURL: "http://gateway.test", GatewayBaseURL: "http://gateway.test", AuditChannelCap: 10000,
	}, ms, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &oauthRig{apiRig: newAPIRig(t, kr.Source()), ms: ms, up: newOAuthUpstream(t, dcr)}
}

// create registers a server that wants OAuth and returns it with the
// authorization URL.
func (r *oauthRig) create() (ServerDTO, string) {
	r.t.Helper()
	slug := "o" + uuid.NewString()[:8]
	status, body := r.do(http.MethodPost, "/servers", alice, "", CreateServerInput{Name: "oauth " + slug, Slug: slug, URL: r.up.mcpURL(), Auth: AuthInput{Kind: store.AuthOAuth}})
	if status != http.StatusCreated {
		r.t.Fatalf("create: %d %s", status, body)
	}
	var out CreateServerResult
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		r.t.Fatal(err)
	}
	id := uuid.MustParse(out.Server.ID)
	r.t.Cleanup(func() { _ = r.store.DeleteServer(context.Background(), id) })
	return out.Server, out.AuthorizeURL
}

// callback sends the browser's return to the gateway's callback path and
// returns where it was redirected.
func (r *oauthRig) callback(q url.Values) string {
	r.t.Helper()
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth/callback?"+q.Encode(), nil))
	if rec.Code != http.StatusFound {
		r.t.Fatalf("callback: %d %s", rec.Code, rec.Body.String())
	}
	return rec.Header().Get("Location")
}

func (r *oauthRig) get(id string) ServerDTO {
	r.t.Helper()
	status, body := r.do(http.MethodGet, "/servers/"+id, alice, "", nil)
	var s ServerDTO
	if err := json.Unmarshal([]byte(body), &s); err != nil || status != http.StatusOK {
		r.t.Fatalf("get: %d %s", status, body)
	}
	r.noTokenIn("a server response", body)
	return s
}

func (r *oauthRig) ping(id string) PingResult {
	r.t.Helper()
	status, body := r.do(http.MethodPost, "/servers/"+id+"/ping", alice, "", nil)
	var p PingResult
	if err := json.Unmarshal([]byte(body), &p); err != nil || status != http.StatusOK {
		r.t.Fatalf("ping: %d %s", status, body)
	}
	return p
}

// noTokenIn fails the test when text holds a token the stub issued.
func (r *oauthRig) noTokenIn(where, text string) {
	r.t.Helper()
	r.up.mu.Lock()
	defer r.up.mu.Unlock()
	held := []string{r.up.access, r.up.refresh}
	for _, secret := range r.up.clients {
		held = append(held, secret)
	}
	for _, v := range held {
		if v != "" && strings.Contains(text, v) {
			r.t.Fatalf("%s carries a token or client secret", where)
		}
	}
}

// connect creates a server and takes it through consent.
func (r *oauthRig) connect() ServerDTO {
	r.t.Helper()
	s, authorizeURL := r.create()
	if to := r.callback(r.up.consent(authorizeURL)); to != "http://gateway.test/servers/"+s.ID+"?oauth=ok" {
		r.t.Fatalf("the callback sent the browser to %q", to)
	}
	return r.get(s.ID)
}

// A new server that wants OAuth is connected through the keyring: discovery
// from the server, the gateway's client metadata document as the client,
// PKCE, and the resource on every request. The gateway keeps a reference.
func TestAServerIsConnectedThroughTheKeyring(t *testing.T) {
	r := newOAuthRig(t, false)
	s, authorizeURL := r.create()

	if s.AuthKind != store.AuthOAuth || s.Credential == nil || s.Credential.State != store.StatePending || s.Credential.Store != "keyring" {
		t.Fatalf("a server awaiting consent is shown as %s/%+v", s.AuthKind, s.Credential)
	}
	row, err := r.store.GetCredential(context.Background(), uuid.MustParse(s.ID))
	if err != nil {
		t.Fatal(err)
	}
	want := keyringref.Ref{Owner: keyringref.GatewayOwner, Kind: keyringref.KindOAuth, Provider: keyringref.ServerCredentialName(s.Slug), Account: keyringref.ServerAccount}
	if row.Kind != store.AuthKeyringOAuth || row.KeyringRef() != want || r.oldStore(s.ID).any() {
		t.Fatalf("stored as %s %+v with something in the gateway's own columns, want only the reference %+v", row.Kind, row.KeyringRef(), want)
	}
	if ping := r.ping(s.ID); ping.OK || !strings.Contains(ping.Error, "pending authorization") {
		t.Fatalf("a server awaiting consent answered: %+v", ping)
	}

	if to := r.callback(r.up.consent(authorizeURL)); to != "http://gateway.test/servers/"+s.ID+"?oauth=ok" {
		t.Fatalf("the callback sent the browser to %q", to)
	}
	got := r.get(s.ID)
	c := got.Credential
	if got.AuthKind != store.AuthOAuth || c.State != store.StateOK || c.Store != "keyring" {
		t.Fatalf("after consent the server is %s/%s in %q", got.AuthKind, c.State, c.Store)
	}
	if c.OAuthIssuer != r.up.srv.URL+"/as" || c.OAuthResource != r.up.mcpURL() || c.OAuthRegistrationKind != "cimd" ||
		c.OAuthClientID != "http://gateway.test/.well-known/mcp-client-metadata.json" || !c.OAuthHasRefreshToken || c.OAuthExpiresAt == nil || len(c.OAuthScopes) != 1 {
		t.Fatalf("the connection is not described from the keyring: %+v", c)
	}
	if ping := r.ping(s.ID); !ping.OK || ping.ToolCount != 1 {
		t.Fatalf("the server does not answer with its keyring token: %+v", ping)
	}
	// The keyring holds the account under the gateway's own id, and the
	// consent state was used up.
	tokens, _ := r.ms.ListOAuthTokens(context.Background(), gatewayOwnerID)
	if len(tokens) != 1 || tokens[0].ProviderKey != want.Provider || tokens[0].Account != keyringref.ServerAccount {
		t.Fatalf("the keyring holds %d accounts for the gateway", len(tokens))
	}
	if again := r.callback(url.Values{"code": {"x"}, "state": {urlState(t, authorizeURL)}}); !strings.Contains(again, "oauth=error") {
		t.Fatalf("a replayed callback was accepted: %s", again)
	}
	r.noTokenIn("the gateway's logs", r.logs.String())

	// Deleting the server deletes the provider, its client and its tokens.
	if status, body := r.do(http.MethodDelete, "/servers/"+s.ID, alice, "", nil); status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", status, body)
	}
	tokens, _ = r.ms.ListOAuthTokens(context.Background(), gatewayOwnerID)
	manifests, _ := r.ms.ListOAuthManifests(context.Background(), gatewayOwnerID)
	configs, _ := r.ms.ListOAuthConfigs(context.Background(), gatewayOwnerID)
	if len(tokens)+len(manifests)+len(configs) != 0 {
		t.Fatalf("the connection outlived its server: %d tokens, %d providers, %d clients", len(tokens), len(manifests), len(configs))
	}
}

func urlState(t *testing.T, authorizeURL string) string {
	t.Helper()
	u, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

// An authorization server that takes no client metadata documents gets a
// dynamic registration, and its client secret is used at the token endpoint.
func TestAServerIsRegisteredDynamicallyWhenItMustBe(t *testing.T) {
	r := newOAuthRig(t, true)
	s := r.connect()
	if s.Credential.OAuthRegistrationKind != "dcr" || s.Credential.State != store.StateOK {
		t.Fatalf("registered as %q, state %s", s.Credential.OAuthRegistrationKind, s.Credential.State)
	}
	if r.up.registered != 1 {
		t.Fatalf("%d registrations, want 1", r.up.registered)
	}
	if !r.ping(s.ID).OK {
		t.Fatal("the server does not answer")
	}
	// A refresh authenticates the client too.
	r.expire()
	if ping := r.ping(s.ID); !ping.OK {
		t.Fatalf("after a refresh as a registered client: %+v", ping)
	}
	if n, _ := r.up.count(); n != 1 {
		t.Fatalf("%d refreshes, want 1", n)
	}
	_, body := r.do(http.MethodGet, "/servers", alice, "", nil)
	r.noTokenIn("the server list", body)
	r.noTokenIn("the gateway's logs", r.logs.String())
}

// expire makes the keyring's stored token due for a refresh.
func (r *oauthRig) expire() {
	r.t.Helper()
	ctx := context.Background()
	tokens, err := r.ms.ListOAuthTokens(ctx, gatewayOwnerID)
	if err != nil || len(tokens) != 1 {
		r.t.Fatalf("tokens: %d %v", len(tokens), err)
	}
	past := time.Now().Add(-time.Minute)
	tokens[0].ExpiresAt = &past
	if err := r.ms.UpdateOAuthToken(ctx, &tokens[0]); err != nil {
		r.t.Fatal(err)
	}
}

// The keyring refreshes a server's token with the resource it is bound to,
// replaces one the server refuses early, and rides out an authorization
// server that is briefly down.
func TestAServersTokenIsRefreshedByTheKeyring(t *testing.T) {
	r := newOAuthRig(t, false)
	s := r.connect()
	_, first := r.up.count()

	r.expire()
	if ping := r.ping(s.ID); !ping.OK {
		t.Fatalf("after expiry: %+v", ping)
	}
	n, second := r.up.count()
	if n != 1 || second == first || r.up.lastResource != r.up.mcpURL() {
		t.Fatalf("%d refreshes, resource %q", n, r.up.lastResource)
	}

	// The server stops honouring the token before its expiry.
	r.up.set(func() { r.up.access = r.up.mint("revoked-elsewhere") })
	if ping := r.ping(s.ID); !ping.OK {
		t.Fatalf("after a 401 before expiry: %+v", ping)
	}
	if n, _ := r.up.count(); n != 2 {
		t.Fatalf("%d refreshes after a rejected token, want 2", n)
	}

	// The token endpoint is down: this call fails, the credential is not
	// broken, and the next one works.
	r.expire()
	r.up.set(func() { r.up.unavailable = true })
	if ping := r.ping(s.ID); ping.OK {
		t.Fatal("a refresh against a dead token endpoint succeeded")
	}
	if got := r.get(s.ID); got.Credential.State != store.StateOK {
		t.Fatalf("a transient failure left the credential %s", got.Credential.State)
	}
	r.up.set(func() { r.up.unavailable = false })
	if ping := r.ping(s.ID); !ping.OK {
		t.Fatalf("after the token endpoint came back: %+v", ping)
	}
	r.noTokenIn("the gateway's logs", r.logs.String())
}

// A refresh the token endpoint definitively refuses breaks the credential:
// it says so, it is not tried again, and consent puts it right.
func TestARefusedRefreshBreaksTheCredential(t *testing.T) {
	r := newOAuthRig(t, false)
	s := r.connect()
	if !r.ping(s.ID).OK {
		t.Fatal("the server does not answer")
	}

	r.expire()
	r.up.set(func() { r.up.refuse = "invalid_grant" })
	ping := r.ping(s.ID)
	if ping.OK || !strings.Contains(ping.Error, "re-authorize") || !strings.Contains(ping.Error, "invalid_grant") {
		t.Fatalf("a refused refresh was reported as %+v", ping)
	}
	got := r.get(s.ID)
	if got.Credential.State != store.StateBroken || !strings.Contains(got.Credential.StateReason, "invalid_grant") || got.AuthKind != store.AuthOAuth {
		t.Fatalf("the credential is %s (%q)", got.Credential.State, got.Credential.StateReason)
	}
	// Broken is not retried: the refresh token is known to be dead.
	before, _ := r.up.count()
	if r.ping(s.ID).OK {
		t.Fatal("a broken credential answered")
	}
	if after, _ := r.up.count(); after != before {
		t.Fatalf("a broken credential was refreshed again (%d -> %d)", before, after)
	}

	// Re-authorizing from the server's page.
	r.up.set(func() { r.up.refuse = "" })
	status, body := r.do(http.MethodPost, "/servers/"+s.ID+"/oauth/start", alice, "", map[string]string{"returnTo": "http://gateway.test/secrets"})
	var started struct {
		AuthorizeURL string `json:"authorizeUrl"`
	}
	if err := json.Unmarshal([]byte(body), &started); err != nil || status != http.StatusOK || started.AuthorizeURL == "" {
		t.Fatalf("restart: %d %s", status, body)
	}
	if got := r.get(s.ID); got.Credential.State != store.StatePending {
		t.Fatalf("a restarted connection is %s", got.Credential.State)
	}
	if to := r.callback(r.up.consent(started.AuthorizeURL)); to != "http://gateway.test/secrets?oauth=ok" {
		t.Fatalf("the callback sent the browser to %q", to)
	}
	if got := r.get(s.ID); got.Credential.State != store.StateOK || got.Credential.LastError != "" {
		t.Fatalf("after consent the credential is %s with error %q", got.Credential.State, got.Credential.LastError)
	}
	if !r.ping(s.ID).OK {
		t.Fatal("the re-authorized server does not answer")
	}
}

// Consent that the authorization server refuses leaves the credential
// broken with the reason, and the state cannot be used afterwards.
func TestRefusedConsentBreaksTheCredential(t *testing.T) {
	r := newOAuthRig(t, false)
	s, authorizeURL := r.create()
	state := urlState(t, authorizeURL)

	to := r.callback(url.Values{"error": {"access_denied"}, "error_description": {"the user said no"}, "state": {state}})
	if to != "http://gateway.test/servers/"+s.ID+"?oauth=error&reason=access_denied" {
		t.Fatalf("the callback sent the browser to %q", to)
	}
	got := r.get(s.ID)
	if got.Credential.State != store.StateBroken || !strings.Contains(got.Credential.StateReason, "access_denied") {
		t.Fatalf("the credential is %s (%q)", got.Credential.State, got.Credential.StateReason)
	}
	q := r.up.consent(authorizeURL)
	if to := r.callback(q); !strings.Contains(to, "oauth=error") {
		t.Fatalf("a state that was refused was accepted later: %s", to)
	}
}

// Giving an OAuth server another credential removes its connection from
// the keyring, and a static credential replaced by OAuth is removed too.
func TestSwitchingBetweenOAuthAndAStaticCredential(t *testing.T) {
	r := newOAuthRig(t, false)
	s := r.connect()
	ctx := context.Background()

	if status, body := r.do(http.MethodPut, "/servers/"+s.ID+"/credential", alice, "", headerInput); status != http.StatusOK {
		t.Fatalf("header: %d %s", status, body)
	}
	if tokens, _ := r.ms.ListOAuthTokens(ctx, gatewayOwnerID); len(tokens) != 0 {
		t.Fatal("the OAuth connection outlived the change to a header credential")
	}
	if secrets, _ := r.ms.ListAPIKeys(ctx, gatewayOwnerID); len(secrets) != 1 {
		t.Fatalf("%d secrets held for the gateway, want the header credential", len(secrets))
	}

	status, body := r.do(http.MethodPut, "/servers/"+s.ID+"/credential", alice, "", AuthInput{Kind: store.AuthOAuth})
	var out CreateServerResult
	if err := json.Unmarshal([]byte(body), &out); err != nil || status != http.StatusOK || out.AuthorizeURL == "" {
		t.Fatalf("oauth: %d %s", status, body)
	}
	if secrets, _ := r.ms.ListAPIKeys(ctx, gatewayOwnerID); len(secrets) != 0 {
		t.Fatal("the header credential outlived the change to OAuth")
	}
	r.callback(r.up.consent(out.AuthorizeURL))
	if !r.ping(s.ID).OK {
		t.Fatal("the server does not answer after going back to OAuth")
	}
}

// The gateway's identity as an OAuth client does not move: every upstream
// authorization server has the callback path on file as the redirect URI,
// and fetches the client metadata document from its well-known path. Both
// are served whether the keyring is on or off, and the callback answers a
// state nobody started as unknown.
func TestTheGatewaysOAuthClientRoutesStay(t *testing.T) {
	const document = `{"client_id":"http://gateway.test/.well-known/mcp-client-metadata.json","client_name":"Bifrost","client_uri":"http://gateway.test",` +
		`"grant_types":["authorization_code","refresh_token"],"redirect_uris":["http://gateway.test/oauth/callback"],"response_types":["code"],"token_endpoint_auth_method":"none"}` + "\n"
	for name, kr := range map[string]keyringref.Holder{"keyring on": aliceHolds(), "keyring off": nil} {
		t.Run(name, func(t *testing.T) {
			r := newAPIRig(t, kr)

			rec := httptest.NewRecorder()
			r.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/mcp-client-metadata.json", nil))
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" || rec.Body.String() != document {
				t.Fatalf("the client metadata document changed: %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
			}

			for _, query := range []string{"state=nobody-started-this&code=x", "state=nobody-started-this&error=access_denied", "code=x", ""} {
				rec = httptest.NewRecorder()
				r.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/oauth/callback?"+query, nil))
				if to := rec.Header().Get("Location"); rec.Code != http.StatusFound || to != "http://gateway.test/servers?oauth=error&reason=unknown+or+expired+state" {
					t.Fatalf("callback?%s: %d to %q", query, rec.Code, to)
				}
			}
		})
	}
}

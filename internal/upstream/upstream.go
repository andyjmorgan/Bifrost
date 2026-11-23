// Package upstream opens MCP client sessions to registered servers. The
// credential for a server is applied by an http.RoundTripper middleware on
// the client, which also refreshes expiring OAuth tokens inline, retries
// once after a 401, and marks a credential broken when refresh fails.
//
// A server's credential is either the gateway's own (sealed in its store)
// or a reference into the keyring, fetched through keyringref.Keyring for
// each session (see keyring.go).
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/internal/audit"
	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/crypto"
	"github.com/andyjmorgan/Bifrost/internal/keyringref"
	"github.com/andyjmorgan/Bifrost/internal/oauthclient"
	"github.com/andyjmorgan/Bifrost/internal/store"
)

// Store is the slice of persistence the connector needs.
type Store interface {
	GetCredential(ctx context.Context, serverID uuid.UUID) (*store.Credential, error)
	UpdateOAuthTokens(ctx context.Context, serverID uuid.UUID, blob []byte, tokenType string, expiresAt *time.Time, hasRefresh bool, scopes []string) error
	SetCredentialState(ctx context.Context, serverID uuid.UUID, state store.CredentialState, reason string) error
}

// Errors surfaced to callers.
var (
	ErrCredentialBroken  = errors.New("upstream: credential is broken; re-authorize the server")
	ErrCredentialPending = errors.New("upstream: credential is pending authorization")
	// ErrKeyringOff is a server whose credential lives in the keyring on a
	// gateway that runs without one.
	ErrKeyringOff = errors.New("upstream: the server uses a keyring credential, and the keyring is not enabled on this gateway")
)

// Connector builds authenticated MCP sessions.
type Connector struct {
	store    Store
	sealer   *crypto.Sealer
	oauth    *oauthclient.Client
	keyring  keyringref.Keyring // nil when the keyring is off
	recorder *audit.Recorder
	log      *slog.Logger
	timeout  time.Duration
	leeway   time.Duration
	// OnInit is called after each successful connect with the initialize result.
	OnInit  func(srv *store.Server, ir *mcp.InitializeResult)
	version string
	locks   sync.Map // serverID -> *sync.Mutex guarding refresh
}

// New builds a connector. kr is the keyring that servers with a keyring
// credential are resolved through; nil means the keyring is off, and such
// a server then fails to connect with ErrKeyringOff.
func New(s Store, sealer *crypto.Sealer, oauth *oauthclient.Client, kr keyringref.Keyring, rec *audit.Recorder, log *slog.Logger, timeout, leeway time.Duration, version string) *Connector {
	return &Connector{store: s, sealer: sealer, oauth: oauth, keyring: kr, recorder: rec, log: log, timeout: timeout, leeway: leeway, version: version}
}

// Timeout is the per-call upstream budget.
func (c *Connector) Timeout() time.Duration { return c.timeout }

func baseTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		MaxIdleConns:          32,
		IdleConnTimeout:       60 * time.Second,
	}
}

// HTTPClient returns an http.Client for server with the credential and
// audit middlewares installed. It is what the MCP client transport uses,
// and callers may use it directly for raw probes.
func (c *Connector) HTTPClient(ctx context.Context, srv *store.Server) (*http.Client, error) {
	cred, err := c.store.GetCredential(ctx, srv.ID)
	if errors.Is(err, store.ErrNotFound) {
		cred = &store.Credential{ServerID: srv.ID, Kind: store.AuthNone, State: store.StateOK}
	} else if err != nil {
		return nil, err
	}
	// Fail fast with a clear reason instead of letting the dial wrap it. A
	// server's own connection held in the keyring has the same two states:
	// waiting for consent, and refused by its token endpoint.
	if cred.Kind == store.AuthOAuth || cred.Kind == store.AuthKeyringOAuth {
		switch cred.State {
		case store.StateBroken:
			return nil, fmt.Errorf("%w (%s)", ErrCredentialBroken, cred.StateReason)
		case store.StatePending:
			return nil, ErrCredentialPending
		}
	}
	if cred.Kind.FromKeyring() && c.keyring == nil {
		c.log.Warn("upstream: keyring credential on a gateway without a keyring", "slug", srv.Slug, "kind", cred.Kind)
		return nil, ErrKeyringOff
	}
	ct := &credentialTransport{c: c, srv: srv, cred: cred}
	var rt http.RoundTripper = c.recorder.Transport(baseTransport(), cred.HeaderName)
	ct.next = rt
	return &http.Client{Transport: sseErrors{next: ct}, Timeout: c.timeout}, nil
}

// sseErrors rewrites an error response that a server framed as an SSE
// event into the plain JSON body the client library reads. A server may
// refuse a request with an HTTP error status and a JSON-RPC error in an
// event, as servers built on the C# SDK do. The client library reads the
// body of an error status as JSON only, so without this it would drop the
// server's error and report the request as lost.
type sseErrors struct{ next http.RoundTripper }

// maxErrorBody bounds how much of an error response is read.
const maxErrorBody = 1 << 20

func (t sseErrors) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil || resp.StatusCode < 400 || !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return resp, err
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	resp.Body.Close()
	if rerr != nil {
		return nil, rerr
	}
	if msg := firstSSEData(body); msg != nil {
		body = msg
		resp.Header = resp.Header.Clone()
		resp.Header.Set("Content-Type", "application/json")
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Del("Content-Length")
	return resp, nil
}

// firstSSEData is the data of the first event in an SSE body, when it is
// a JSON object, or nil.
func firstSSEData(body []byte) []byte {
	var data []byte
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		switch {
		case len(line) == 0:
			if len(data) > 0 {
				return jsonObject(data)
			}
		case bytes.HasPrefix(line, []byte("data:")):
			d := bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" "))
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, d...)
		}
	}
	return jsonObject(data)
}

func jsonObject(b []byte) []byte {
	if !json.Valid(b) || len(bytes.TrimSpace(b)) == 0 || bytes.TrimSpace(b)[0] != '{' {
		return nil
	}
	return b
}

// Session is an open downstream connection.
type Session struct {
	*mcp.ClientSession
	Server *store.Server
	// Ctx is the detached context the session was opened with; use it for
	// calls on the session so no inbound protocol state leaks upstream.
	Ctx    context.Context
	cancel context.CancelFunc
}

// Close ends the session and releases its detached context.
func (s *Session) Close() error {
	err := s.ClientSession.Close()
	if s.cancel != nil {
		s.cancel()
	}
	return err
}

// Open connects to a server, negotiating whatever protocol version it speaks.
//
// The context is detached first: the go-sdk stores the inbound request's
// negotiated protocol version on the context, and the outgoing client would
// otherwise inherit it and speak 2026-07-28 to a server that may not.
func (c *Connector) Open(ctx context.Context, srv *store.Server) (*Session, error) {
	return c.OpenWith(ctx, srv, "")
}

// OpenWith is Open starting the handshake at protocolVersion (the version the
// server negotiated last time; "" means latest with the discover probe). If
// the server now rejects that version, it retries once with latest. On
// success OnInit, if set, is told what the server reported.
func (c *Connector) OpenWith(ctx context.Context, srv *store.Server, protocolVersion string) (*Session, error) {
	return c.OpenFor(ctx, srv, protocolVersion, SessionOptions{})
}

// SessionOptions is how the gateway presents itself to a server for one
// forwarded request. The rules decide all of it (server.session).
type SessionOptions struct {
	// Identity is the client name and version the server is told. Nil
	// means the gateway's own.
	Identity *mcp.Implementation
	// Capabilities is what the server is told the client can do. Nil means
	// the SDK's defaults, which is right for the gateway's own catalog
	// fetches; a forwarded request always states them.
	Capabilities *mcp.ClientCapabilities
	// OnProgress receives each progress notification of a call on this
	// session, in order, before the call returns.
	OnProgress func(*mcp.ProgressNotificationParams)
	// PassQuestions hands a server's own question back to the caller as the
	// call's result. Without it the client library would try to answer the
	// question itself, and the gateway has nobody to ask: the question is
	// the end client's to answer.
	PassQuestions bool
	// Capture, when set, keeps the raw result of one method's request on
	// this session.
	Capture *Capture
}

// OpenFor is OpenWith with the identity, capabilities and handlers of one
// forwarded request.
func (c *Connector) OpenFor(ctx context.Context, srv *store.Server, protocolVersion string, o SessionOptions) (*Session, error) {
	sess, err := c.open(ctx, srv, protocolVersion, o)
	if err != nil && protocolVersion != "" && strings.Contains(err.Error(), "unsupported protocol version") {
		c.log.Info("upstream: cached protocol version rejected; retrying with latest", "slug", srv.Slug, "version", protocolVersion)
		sess, err = c.open(ctx, srv, "", o)
	}
	if err == nil && c.OnInit != nil {
		if ir := sess.InitializeResult(); ir != nil {
			c.OnInit(srv, ir)
		}
	}
	return sess, err
}

func (c *Connector) open(ctx context.Context, srv *store.Server, protocolVersion string, o SessionOptions) (*Session, error) {
	dctx, cancel := detach(ctx, targetFor(ctx, srv))
	hc, err := c.HTTPClient(dctx, srv)
	if err != nil {
		cancel()
		return nil, err
	}
	if o.Capture != nil {
		hc.Transport = captureTransport{next: hc.Transport, c: o.Capture}
	}
	as := o.Identity
	if as == nil {
		as = &mcp.Implementation{Name: "bifrost", Version: c.version}
	}
	opts := &mcp.ClientOptions{Capabilities: o.Capabilities}
	if o.PassQuestions {
		opts.MultiRoundTrip = &mcp.MultiRoundTripOptions{Disabled: true}
	}
	if o.OnProgress != nil {
		opts.ProgressNotificationHandler = func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			if req != nil && req.Params != nil {
				o.OnProgress(req.Params)
			}
		}
	}
	client := mcp.NewClient(as, opts)
	cs, err := client.Connect(dctx, &mcp.StreamableClientTransport{
		Endpoint: srv.URL, HTTPClient: hc,
		// Connections are opened per call and the gateway never consumes
		// server-initiated messages, so the standalone GET stream is pure
		// overhead. Older go-sdk servers also hold the GET without flushing
		// headers, which would block Connect.
		DisableStandaloneSSE: true,
	}, &mcp.ClientSessionOptions{ProtocolVersion: protocolVersion})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("upstream %s: connect: %w", srv.Slug, err)
	}
	return &Session{ClientSession: cs, Server: srv, Ctx: dctx, cancel: cancel}, nil
}

// detach builds a context that follows ctx's cancellation and carries only
// the gateway's own values (caller, request id, audit target).
func detach(ctx context.Context, t audit.Target) (context.Context, context.CancelFunc) {
	nctx, cancel := context.WithCancel(context.Background())
	if dl, ok := ctx.Deadline(); ok {
		nctx, cancel = context.WithDeadline(nctx, dl)
	}
	// The watcher keeps its own handle: nctx is rebound below, and the
	// goroutine must not read a variable that is still being written.
	done := nctx.Done()
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-done:
		}
	}()
	if caller := auth.CallerFrom(ctx); caller != nil {
		nctx = auth.WithCaller(nctx, caller)
	}
	nctx = audit.Inherit(ctx, nctx)
	nctx = audit.WithTarget(nctx, t)
	return nctx, cancel
}

func targetFor(ctx context.Context, srv *store.Server) audit.Target {
	t, ok := audit.TargetFrom(ctx)
	if !ok {
		t.Route = "api"
	}
	t.ServerID = srv.ID
	t.Slug = srv.Slug
	return t
}

// ---- credential middleware --------------------------------------------------------

type credentialTransport struct {
	c    *Connector
	srv  *store.Server
	cred *store.Credential
	next http.RoundTripper
	// kr is what the keyring handed out for this client, when the
	// credential is a keyring reference.
	kr keyringMemo
}

func (t *credentialTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	cred := t.cred
	switch cred.Kind {
	case store.AuthNone:
		return t.next.RoundTrip(req)
	case store.AuthBasic:
		sec, err := t.c.openSecret(cred)
		if err != nil {
			return nil, err
		}
		req = cloneWithBody(req)
		req.SetBasicAuth(cred.BasicUsername, sec.Password)
		return t.next.RoundTrip(req)
	case store.AuthHeader:
		sec, err := t.c.openSecret(cred)
		if err != nil {
			return nil, err
		}
		req = cloneWithBody(req)
		req.Header.Set(cred.HeaderName, sec.HeaderValue)
		return t.next.RoundTrip(req)
	case store.AuthOAuth:
		switch cred.State {
		case store.StateBroken:
			return nil, ErrCredentialBroken
		case store.StatePending:
			return nil, ErrCredentialPending
		}
		sec, err := t.c.openSecret(cred)
		if err != nil {
			return nil, err
		}
		refreshed := false
		if t.c.needsRefresh(cred) && sec.RefreshToken != "" {
			if sec, err = t.c.refresh(ctx, t.srv, cred, sec); err != nil {
				return nil, err
			}
			refreshed = true
		}
		req = cloneWithBody(req)
		req.Header.Set("Authorization", bearer(cred, sec))
		resp, err := t.next.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusUnauthorized || refreshed || sec.RefreshToken == "" {
			return resp, nil
		}
		// The token was rejected before its expiry: refresh once and retry.
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16)) //nolint:errcheck
		resp.Body.Close()
		if sec, err = t.c.refresh(ctx, t.srv, cred, sec); err != nil {
			return nil, err
		}
		retry := cloneWithBody(req)
		retry.Header.Set("Authorization", bearer(cred, sec))
		return t.next.RoundTrip(retry)
	case store.AuthKeyringOAuth:
		return t.keyringOAuth(req)
	case store.AuthKeyringSecret:
		return t.keyringSecret(req)
	}
	return nil, fmt.Errorf("upstream: unknown credential kind %q", cred.Kind)
}

func bearer(cred *store.Credential, sec *store.Secret) string {
	typ := cred.OAuthTokenType
	if typ == "" || typ == "bearer" {
		typ = "Bearer"
	}
	return typ + " " + sec.AccessToken
}

// cloneWithBody makes the request replayable so a retry can resend it.
func cloneWithBody(req *http.Request) *http.Request {
	r := req.Clone(req.Context())
	if req.Body == nil || req.Body == http.NoBody {
		return r
	}
	if req.GetBody != nil {
		if b, err := req.GetBody(); err == nil {
			r.Body = b
			return r
		}
	}
	buf, _ := io.ReadAll(req.Body)
	req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(buf))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(buf)), nil }
	r.Body = io.NopCloser(bytes.NewReader(buf))
	r.GetBody = req.GetBody
	return r
}

func (c *Connector) openSecret(cred *store.Credential) (*store.Secret, error) {
	if len(cred.SecretBlob) == 0 {
		return &store.Secret{}, nil
	}
	pt, err := c.sealer.Open(cred.SecretBlob, cred.ServerID[:])
	if err != nil {
		return nil, fmt.Errorf("upstream: unseal: %w", err)
	}
	var s store.Secret
	if err := json.Unmarshal(pt, &s); err != nil {
		return nil, fmt.Errorf("upstream: secret: %w", err)
	}
	return &s, nil
}

// SealSecret encrypts a secret for storage against a server.
func (c *Connector) SealSecret(serverID uuid.UUID, s *store.Secret) ([]byte, error) {
	b, _ := json.Marshal(s)
	return c.sealer.Seal(b, serverID[:])
}

// OpenSecret exposes decryption for the API layer (never returned to clients).
func (c *Connector) OpenSecret(cred *store.Credential) (*store.Secret, error) {
	return c.openSecret(cred)
}

// needsRefresh is true inside the leeway window before expiry. Short-lived
// tokens (lifetime under twice the leeway) use half their lifetime instead,
// so a 5-minute token with a 10-minute leeway is not refreshed on every call.
func (c *Connector) needsRefresh(cred *store.Credential) bool {
	if cred.OAuthExpiresAt == nil {
		return false
	}
	leeway := c.leeway
	if cred.LastRefreshedAt != nil {
		if life := cred.OAuthExpiresAt.Sub(*cred.LastRefreshedAt); life > 0 && life < 2*leeway {
			leeway = life / 2
		}
	}
	return time.Now().Add(leeway).After(*cred.OAuthExpiresAt)
}

// NeedsRefresh reports whether a credential is due for a refresh.
func (c *Connector) NeedsRefresh(cred *store.Credential) bool { return c.needsRefresh(cred) }

// refresh serialises refreshes per server, re-reads the credential under
// the lock (another request may have refreshed already), and persists the
// new token set. A definitive token-endpoint rejection marks the credential broken.
func (c *Connector) refresh(ctx context.Context, srv *store.Server, cred *store.Credential, sec *store.Secret) (*store.Secret, error) {
	muI, _ := c.locks.LoadOrStore(srv.ID, &sync.Mutex{})
	mu := muI.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	if fresh, err := c.store.GetCredential(ctx, srv.ID); err == nil && fresh.LastRefreshedAt != nil &&
		(cred.LastRefreshedAt == nil || fresh.LastRefreshedAt.After(*cred.LastRefreshedAt)) {
		*cred = *fresh
		return c.openSecret(cred)
	}
	reg := &oauthclient.Registration{Kind: cred.OAuthRegistrationKind, ClientID: cred.OAuthClientID, ClientSecret: sec.ClientSecret}
	// The refresh outlives the caller: a cancelled request must not abort a
	// token rotation half-way (the old refresh token may already be spent).
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.timeout)
	defer cancel()
	ts, err := c.oauth.Refresh(rctx, cred.OAuthTokenEndpoint, reg, sec.RefreshToken, cred.OAuthResource, cred.OAuthScopes)
	if err != nil {
		// Only a definitive answer from the token endpoint (4xx such as
		// invalid_grant) breaks the credential. Network errors, timeouts and
		// 5xx are transient: fail this call and try again next time.
		var te *oauthclient.TokenError
		if !errors.As(err, &te) || te.Status >= 500 {
			c.log.Warn("token refresh failed (transient)", "slug", srv.Slug, "err", err)
			return nil, fmt.Errorf("upstream: token refresh failed: %w", err)
		}
		reason := te.Code + ": " + te.Description
		if sErr := c.store.SetCredentialState(rctx, srv.ID, store.StateBroken, reason); sErr != nil {
			c.log.Error("mark broken failed", "slug", srv.Slug, "err", sErr)
		}
		cred.State = store.StateBroken
		c.log.Warn("token refresh failed; credential marked broken", "slug", srv.Slug, "reason", reason)
		return nil, fmt.Errorf("%w: %s", ErrCredentialBroken, reason)
	}
	newSec := &store.Secret{AccessToken: ts.AccessToken, RefreshToken: ts.RefreshToken, ClientSecret: sec.ClientSecret}
	blob, err := c.SealSecret(srv.ID, newSec)
	if err != nil {
		return nil, err
	}
	if err := c.store.UpdateOAuthTokens(rctx, srv.ID, blob, ts.TokenType, ts.ExpiresAt, ts.RefreshToken != "", ts.Scope); err != nil {
		return nil, err
	}
	now := time.Now()
	cred.SecretBlob, cred.OAuthExpiresAt, cred.LastRefreshedAt, cred.State = blob, ts.ExpiresAt, &now, store.StateOK
	if ts.TokenType != "" {
		cred.OAuthTokenType = ts.TokenType
	}
	c.log.Info("token refreshed", "slug", srv.Slug, "expiresAt", ts.ExpiresAt)
	return newSec, nil
}

// Refresh is the worker entry point: refresh one credential now.
func (c *Connector) Refresh(ctx context.Context, srv *store.Server, cred *store.Credential) error {
	sec, err := c.openSecret(cred)
	if err != nil {
		return err
	}
	if sec.RefreshToken == "" {
		return c.store.SetCredentialState(ctx, srv.ID, store.StateBroken, "no refresh token")
	}
	_, err = c.refresh(ctx, srv, cred, sec)
	return err
}

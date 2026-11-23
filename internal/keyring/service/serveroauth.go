package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/andyjmorgan/Bifrost/internal/keyring/audit"
	"github.com/andyjmorgan/Bifrost/internal/keyring/contracts"
	"github.com/andyjmorgan/Bifrost/internal/keyring/manifests"
	"github.com/andyjmorgan/Bifrost/internal/keyring/store"
	"github.com/andyjmorgan/Bifrost/internal/oauthclient"
)

// This file is the keyring's MCP-style OAuth: an account whose provider is discovered from an MCP
// server, as the MCP authorization spec lays out, instead of written by a person.
//
//   - Discovery starts at the server: its WWW-Authenticate challenge, its protected resource
//     metadata (RFC 9728) and the authorization server metadata that names (RFC 8414).
//   - The client is registered by the flow: a Client ID Metadata Document where the authorization
//     server takes one, dynamic registration (RFC 7591) otherwise.
//   - PKCE, and the resource indicator (RFC 8707) on the authorization request, the code exchange
//     and every refresh.
//   - A refresh the token endpoint definitively refuses marks the account broken.
//
// The protocol itself is internal/oauthclient, the gateway's client, used here unchanged. What this
// file adds is where the result is kept: a provider, its app credentials and a token row, like any
// other account in the keyring.

// ServerAccount is the account name of an MCP server's connection. A server has one connection and
// no userinfo endpoint to name it by, so the name is fixed.
const ServerAccount = "default"

// serverStateTTL is how long a consent round trip may take.
const serverStateTTL = 15 * time.Minute

// maxBrokenReason bounds the reason stored with a broken account (the column is varchar(1024)).
const maxBrokenReason = 1000

// BrokenError is an account whose refresh the token endpoint has definitively refused. It stays
// broken, and is not refreshed or handed out, until it is authorized again.
type BrokenError struct{ Reason string }

func (e *BrokenError) Error() string {
	return "oauth account needs to be authorized again: " + e.Reason
}

// RefreshGuard is held, shared, for as long as a refresh talks to the token endpoint and stores the
// result. A credential migration holds it exclusively, so no token rotates while rows are moved.
type RefreshGuard interface {
	Shared(ctx context.Context) (release func(), err error)
}

// noGuard is the guard of a keyring that shares its tokens with nothing else.
type noGuard struct{}

func (noGuard) Shared(context.Context) (func(), error) { return func() {}, nil }

// TokenOption configures an OAuthTokenService beyond what every caller of NewOAuthTokenService sets.
type TokenOption func(*OAuthTokenService)

// WithServerOAuth gives the token service the client that refreshes the accounts of MCP servers.
// Without it such an account cannot be refreshed.
func WithServerOAuth(c *oauthclient.Client) TokenOption {
	return func(s *OAuthTokenService) { s.server = c }
}

// WithRefreshGuard makes every refresh hold g. Nil leaves refreshes unguarded.
func WithRefreshGuard(g RefreshGuard) TokenOption {
	return func(s *OAuthTokenService) {
		if g != nil {
			s.guard = g
		}
	}
}

// FlowOption configures an OAuthFlowService beyond what every caller of NewOAuthFlowService sets.
type FlowOption func(*OAuthFlowService)

// WithServerFlow gives the flow service the client that discovers, registers with and exchanges
// codes at the authorization servers of MCP servers. Without it BeginServer is refused.
func WithServerFlow(c *oauthclient.Client) FlowOption {
	return func(s *OAuthFlowService) { s.server = c }
}

// BeginServerParams describes the MCP server a connection is started for.
type BeginServerParams struct {
	// Provider is the key the server's provider is stored under.
	Provider string
	// Name is the server's name, for the provider's own.
	Name string
	// ServerURL is the MCP endpoint discovery starts from.
	ServerURL string
	// Scopes overrides the scopes discovery found. Empty takes the challenge's, then the protected
	// resource metadata's.
	Scopes []string
	// ReturnTo is where the browser goes once the callback has run.
	ReturnTo string
}

// StateInfo is what a pending consent round trip is for.
type StateInfo struct {
	Provider string
	Owner    uuid.UUID
	ReturnTo string
	// Server reports whether the provider was discovered from an MCP server.
	Server bool
}

// errNoServerOAuth is a keyring that was not given the MCP OAuth client.
var errNoServerOAuth = errors.New("this keyring cannot connect MCP servers: no MCP OAuth client is configured")

// BeginServer starts the connection of an MCP server for the ambient caller: it discovers the
// server's authorization server, registers a client with it, stores both as a provider with its app
// credentials, and returns the URL to send the browser to. Any account the provider already had is
// removed first: a new registration is a new client, and the old refresh token belongs to the old one.
func (s *OAuthFlowService) BeginServer(ctx context.Context, p BeginServerParams) (*BeginAuthResult, error) {
	ctx, span := startSpan(ctx, "oauthflow.begin_server")
	defer span.End()
	span.SetAttributes(attribute.String("oauth.provider", p.Provider))
	if s.server == nil {
		return nil, errNoServerOAuth
	}
	caller := contracts.CallerFrom(ctx)

	d, err := s.server.Discover(ctx, p.ServerURL)
	if err != nil {
		return nil, err
	}
	scopes := p.Scopes
	if len(scopes) == 0 {
		scopes = d.ChallengeScopes
	}
	if len(scopes) == 0 {
		scopes = d.ScopesSupported
	}
	reg, err := s.server.Register(ctx, d, scopes)
	if err != nil {
		return nil, err
	}

	existing, err := s.resolver.ResolveProviderID(ctx, p.Provider, caller.UserID)
	if err != nil {
		return nil, err
	}
	if err := s.resolver.UpsertOAuth(ctx, manifests.Manifest{
		Key: p.Provider, Name: p.Name, AuthorizationEndpoint: d.AuthorizationEndpoint, TokenEndpoint: d.TokenEndpoint,
		ScopeDelimiter: " ", DefaultScopes: scopes,
		MCP: &manifests.MCPProvider{
			ServerURL: p.ServerURL, Issuer: d.AuthorizationServer, Resource: d.Resource,
			RegistrationKind: reg.Kind, RegistrationEndpoint: d.RegistrationEndpoint, RedirectURI: s.server.RedirectURI,
		},
	}); err != nil {
		return nil, err
	}
	providerID, err := s.resolver.ResolveProviderID(ctx, p.Provider, caller.UserID)
	if err != nil {
		return nil, err
	}
	if err := s.putServerConfig(ctx, caller, providerID, p.Provider, reg, scopes); err != nil {
		return nil, err
	}
	if old, err := s.store.FindOAuthToken(ctx, caller.UserID, providerID, ServerAccount); err != nil {
		return nil, err
	} else if old != nil {
		if _, err := s.store.DeleteOAuthToken(ctx, caller.UserID, old.ID); err != nil {
			return nil, err
		}
		s.audit.Emit(ctx, audit.EmitParams{Type: audit.EventTokenRemoved, Outcome: audit.OutcomeSuccess,
			TargetKind: "oauth_token", TargetProvider: p.Provider, TargetAccount: ServerAccount, Detail: "authorization restarted"})
	}

	pkce, err := oauthclient.NewPKCE()
	if err != nil {
		return nil, err
	}
	state, err := oauthclient.NewState()
	if err != nil {
		return nil, err
	}
	if err := s.store.InsertOAuthState(ctx, &store.OAuthState{
		State: state, Provider: p.Provider, CodeVerifier: pkce.Verifier,
		OwnerUserID: caller.UserID, OwnerTenantID: caller.TenantID,
		RedirectURI: s.server.RedirectURI, ExpiresAt: time.Now().UTC().Add(serverStateTTL), ReturnTo: ptrIfNotEmpty(p.ReturnTo),
	}); err != nil {
		return nil, err
	}

	event := audit.EventCredentialCreated
	if existing != uuid.Nil {
		event = audit.EventCredentialChanged
	}
	s.audit.Emit(ctx, audit.EmitParams{Type: event, Outcome: audit.OutcomeSuccess,
		TargetKind: "provider", TargetProvider: p.Provider, TargetName: p.Provider, Detail: "MCP server registered (" + reg.Kind + ")"})
	return &BeginAuthResult{AuthorizeURL: s.server.AuthorizeURL(d, reg, scopes, state, pkce), State: state}, nil
}

// putServerConfig stores the client a registration produced as the provider's app credentials. A
// public client has no secret; the column is NOT NULL, so that is zero-length bytes.
func (s *OAuthFlowService) putServerConfig(ctx context.Context, caller contracts.Caller, providerID uuid.UUID, provider string, reg *oauthclient.Registration, scopes []string) error {
	clientID, err := s.cipher.EncryptString(reg.ClientID)
	if err != nil {
		return err
	}
	secret := []byte{}
	if reg.ClientSecret != "" {
		if secret, err = s.cipher.EncryptString(reg.ClientSecret); err != nil {
			return err
		}
	}
	scopesJSON := encodeScopes(scopes)
	redirect := s.server.RedirectURI
	row, err := s.store.GetOAuthConfigByProvider(ctx, caller.UserID, providerID)
	if err != nil {
		return err
	}
	if row == nil {
		return s.store.InsertOAuthConfig(ctx, &store.OAuthProviderConfig{
			UserID: caller.UserID, TenantID: caller.TenantID, ProviderID: providerID, ProviderKey: provider,
			ClientIDCipher: clientID, ClientSecretCipher: secret, ScopesJSON: &scopesJSON, RedirectURI: &redirect,
		})
	}
	row.ProviderKey, row.ClientIDCipher, row.ClientSecretCipher, row.ScopesJSON, row.RedirectURI = provider, clientID, secret, &scopesJSON, &redirect
	return s.store.UpdateOAuthConfig(ctx, row)
}

// StateInfo says what a pending round trip is for, without consuming it. It returns nil when the
// state is unknown or has expired.
func (s *OAuthFlowService) StateInfo(ctx context.Context, state string) (*StateInfo, error) {
	row, err := s.store.GetOAuthStateByState(ctx, state)
	if err != nil || row == nil || row.ExpiresAt.Before(time.Now().UTC()) {
		return nil, err
	}
	manifest, err := s.resolver.GetOAuth(ctx, row.Provider, row.OwnerUserID)
	if err != nil {
		return nil, err
	}
	return &StateInfo{Provider: row.Provider, Owner: row.OwnerUserID, ReturnTo: deref(row.ReturnTo), Server: manifest != nil && manifest.MCP != nil}, nil
}

// Abandon ends a round trip the authorization server refused, so its state cannot be used later.
func (s *OAuthFlowService) Abandon(ctx context.Context, state, reason string) error {
	row, err := s.store.GetOAuthStateByState(ctx, state)
	if err != nil || row == nil {
		return err
	}
	if _, err := s.store.DeleteOAuthState(ctx, row.ID); err != nil {
		return err
	}
	s.audit.Emit(ctx, audit.EmitParams{Type: audit.EventTokenAdded, Outcome: audit.OutcomeFailure,
		TargetKind: "oauth_token", TargetProvider: row.Provider, Detail: "authorization refused: " + clip(reason, 200),
		UserID: &row.OwnerUserID, TenantID: &row.OwnerTenantID})
	return nil
}

// completeServer finishes the round trip of an MCP server: the code is exchanged with the client
// the flow registered and the resource the token is for, and the token set is stored as the
// provider's one account.
func (s *OAuthFlowService) completeServer(ctx context.Context, stateRow *store.OAuthState, manifest *manifests.Manifest, config *store.OAuthProviderConfig, code string) (*CompleteAuthResult, error) {
	if s.server == nil {
		return nil, errNoServerOAuth
	}
	reg, err := serverRegistration(s.cipher, manifest, config)
	if err != nil {
		return nil, err
	}
	ts, err := s.server.Exchange(ctx, manifest.TokenEndpoint, reg, code, stateRow.CodeVerifier, manifest.MCP.Resource)
	if err != nil {
		// A token endpoint's error names a code and a description, never a token.
		return nil, OAuthAuthorizationError{"token exchange failed: " + exchangeFailure(err)}
	}
	scopes := ts.Scope
	if len(scopes) == 0 {
		scopes = decodeScopes(config.ScopesJSON)
	}
	if err := s.storeServerToken(ctx, stateRow.OwnerUserID, stateRow.OwnerTenantID, manifest.ID, manifest.Key, ts, scopes); err != nil {
		return nil, err
	}
	s.audit.Emit(ctx, audit.EmitParams{Type: audit.EventTokenAdded, Outcome: audit.OutcomeSuccess,
		TargetKind: "oauth_token", TargetProvider: manifest.Key, TargetAccount: ServerAccount,
		UserID: &stateRow.OwnerUserID, TenantID: &stateRow.OwnerTenantID})
	return &CompleteAuthResult{Provider: manifest.Key, Account: ServerAccount, Scopes: scopes, ExpiresAt: ts.ExpiresAt, ReturnTo: deref(stateRow.ReturnTo)}, nil
}

// storeServerToken writes a server's token set as the provider's one account, usable again.
func (s *OAuthFlowService) storeServerToken(ctx context.Context, owner, tenant, providerID uuid.UUID, provider string, ts *oauthclient.TokenSet, scopes []string) error {
	existing, err := s.store.FindOAuthToken(ctx, owner, providerID, ServerAccount)
	if err != nil {
		return err
	}
	if existing != nil {
		if err := sealServerToken(s.cipher, existing, ts, scopes); err != nil {
			return err
		}
		return s.store.UpdateOAuthToken(ctx, existing)
	}
	row := &store.OAuthToken{UserID: owner, TenantID: tenant, ProviderID: providerID, ProviderKey: provider, Account: ServerAccount}
	if err := sealServerToken(s.cipher, row, ts, scopes); err != nil {
		return err
	}
	return s.store.InsertOAuthToken(ctx, row)
}

// sealServerToken writes a token set into a row's sealed columns: refreshed now, and no longer
// broken. A token type the authorization server did not repeat is kept.
func sealServerToken(c interface {
	EncryptString(string) ([]byte, error)
}, row *store.OAuthToken, ts *oauthclient.TokenSet, scopes []string) error {
	access, err := c.EncryptString(ts.AccessToken)
	if err != nil {
		return err
	}
	refresh := []byte{}
	if ts.RefreshToken != "" {
		if refresh, err = c.EncryptString(ts.RefreshToken); err != nil {
			return err
		}
	}
	scopesJSON := encodeScopes(scopes)
	now := time.Now().UTC()
	row.AccessTokenCipher, row.RefreshTokenCipher, row.ScopesJSON = access, refresh, &scopesJSON
	row.ExpiresAt, row.LastRefreshedAt = ts.ExpiresAt, &now
	row.BrokenAt, row.BrokenReason = nil, nil
	if ts.TokenType != "" {
		row.TokenType = &ts.TokenType
	}
	return nil
}

// serverRegistration is the client an MCP provider's app credentials describe.
func serverRegistration(c interface {
	DecryptToString([]byte) (string, error)
}, manifest *manifests.Manifest, config *store.OAuthProviderConfig) (*oauthclient.Registration, error) {
	clientID, err := c.DecryptToString(config.ClientIDCipher)
	if err != nil {
		return nil, err
	}
	reg := &oauthclient.Registration{Kind: manifest.MCP.RegistrationKind, ClientID: clientID}
	if len(config.ClientSecretCipher) > 0 {
		if reg.ClientSecret, err = c.DecryptToString(config.ClientSecretCipher); err != nil {
			return nil, err
		}
	}
	return reg, nil
}

// refreshServer exchanges the refresh token of an MCP server's account, with the resource the
// token is bound to. The caller holds the refresh lock and the guard, and has re-read token.
//
// Only a definitive answer from the token endpoint (a 4xx such as invalid_grant) breaks the
// account. A network error, a timeout or a 5xx is transient: this call fails and the next tries
// again.
func (s *OAuthTokenService) refreshServer(ctx context.Context, manifest *manifests.Manifest, config *store.OAuthProviderConfig, token *store.OAuthToken) (*OAuthAccessToken, error) {
	fail := func(msg string) {
		s.audit.Emit(ctx, audit.EmitParams{Type: audit.EventTokenRefreshed, Outcome: audit.OutcomeFailure,
			TargetKind: "oauth_token", TargetProvider: token.ProviderKey, TargetAccount: token.Account, Detail: msg})
	}
	if s.server == nil {
		fail("no MCP OAuth client is configured")
		return nil, OAuthRefreshError{errNoServerOAuth.Error()}
	}
	refreshTok, err := s.cipher.DecryptToString(token.RefreshTokenCipher)
	if err != nil {
		return nil, err
	}
	reg, err := serverRegistration(s.cipher, manifest, config)
	if err != nil {
		return nil, err
	}
	scopes := decodeScopes(token.ScopesJSON)
	ts, err := s.server.Refresh(ctx, manifest.TokenEndpoint, reg, refreshTok, manifest.MCP.Resource, scopes)
	if err != nil {
		var te *oauthclient.TokenError
		if !errors.As(err, &te) || te.Status >= 500 {
			msg := fmt.Sprintf("refresh failed for %s: %s", manifest.Key, exchangeFailure(err))
			fail(msg)
			return nil, OAuthRefreshError{msg}
		}
		reason := clip(strings.TrimSuffix(te.Code+": "+te.Description, ": "), maxBrokenReason)
		now := time.Now().UTC()
		token.BrokenAt, token.BrokenReason = &now, &reason
		if uerr := s.store.UpdateOAuthToken(ctx, token); uerr != nil {
			return nil, uerr
		}
		fail("refresh refused; the account is broken until it is authorized again: " + clip(reason, 200))
		return nil, &BrokenError{Reason: reason}
	}

	if len(ts.Scope) > 0 {
		scopes = ts.Scope
	}
	if err := sealServerToken(s.cipher, token, ts, scopes); err != nil {
		return nil, err
	}
	if err := s.store.UpdateOAuthToken(ctx, token); err != nil {
		return nil, err
	}
	s.audit.Emit(ctx, audit.EmitParams{Type: audit.EventTokenRefreshed, Outcome: audit.OutcomeSuccess,
		TargetKind: "oauth_token", TargetProvider: token.ProviderKey, TargetAccount: token.Account})
	return &OAuthAccessToken{AccessToken: ts.AccessToken, ExpiresAt: ts.ExpiresAt, Scopes: scopes, TokenType: deref(token.TokenType)}, nil
}

// exchangeFailure is a token endpoint failure as an error message may carry it: the OAuth error
// code and description when the endpoint gave one, and otherwise the transport's error.
func exchangeFailure(err error) string {
	var te *oauthclient.TokenError
	if errors.As(err, &te) {
		return clip(strings.TrimSuffix(fmt.Sprintf("HTTP %d %s: %s", te.Status, te.Code, te.Description), ": "), 300)
	}
	return clip(err.Error(), 300)
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

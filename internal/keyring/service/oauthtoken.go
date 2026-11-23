package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/andyjmorgan/Bifrost/internal/keyring/audit"
	"github.com/andyjmorgan/Bifrost/internal/keyring/contracts"
	"github.com/andyjmorgan/Bifrost/internal/keyring/crypto"
	"github.com/andyjmorgan/Bifrost/internal/keyring/manifests"
	"github.com/andyjmorgan/Bifrost/internal/keyring/store"
	"github.com/andyjmorgan/Bifrost/internal/oauthclient"
)

// refreshWindow is how close to expiry a token is proactively refreshed.
const refreshWindow = 60 * time.Second

// ErrNotRefreshable is returned by ReplaceRejectedToken for an account that cannot be refreshed: it
// holds no refresh token, or its provider or the provider's app credentials are gone.
var ErrNotRefreshable = errors.New("oauth token cannot be refreshed: no refresh token or no provider app credentials")

// OAuthAccessToken is a live access token (auto-refreshed by the vault).
type OAuthAccessToken struct {
	AccessToken string
	ExpiresAt   *time.Time
	Scopes      []string
	// TokenType is the type the authorization server issued; empty reads as Bearer.
	TokenType string
}

// OAuthTokenSummary is the non-secret view of a connected account.
type OAuthTokenSummary struct {
	ID              uuid.UUID
	Provider        string
	Account         string
	ExpiresAt       *time.Time
	LastRefreshedAt *time.Time
	Scopes          []string
}

// OAuthTokenService reads connected tokens and refreshes them server-to-server on demand.
type OAuthTokenService struct {
	store        store.Store
	cipher       crypto.Cipher
	audit        *audit.Log
	resolver     *manifests.Resolver
	client       *http.Client
	refreshLocks *keyedMutex         // serialises refreshes per token id (avoids rotating-token clobber)
	server       *oauthclient.Client // refreshes the accounts of MCP servers; nil when not configured
	guard        RefreshGuard        // held for the length of every refresh
}

// NewOAuthTokenService builds the service.
func NewOAuthTokenService(s store.Store, c crypto.Cipher, a *audit.Log, r *manifests.Resolver, client *http.Client, opts ...TokenOption) *OAuthTokenService {
	if client == nil {
		client = http.DefaultClient
	}
	svc := &OAuthTokenService{store: s, cipher: c, audit: a, resolver: r, client: client, refreshLocks: newKeyedMutex(), guard: noGuard{}}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

// List returns the caller's connected tokens.
func (s *OAuthTokenService) List(ctx context.Context) ([]OAuthTokenSummary, error) {
	ctx, span := startSpan(ctx, "oauthtoken.list")
	defer span.End()
	rows, err := s.store.ListOAuthTokens(ctx, contracts.CallerFrom(ctx).UserID)
	if err != nil {
		return nil, err
	}
	out := make([]OAuthTokenSummary, len(rows))
	for i := range rows {
		out[i] = OAuthTokenSummary{
			ID: rows[i].ID, Provider: rows[i].ProviderKey, Account: rows[i].Account,
			ExpiresAt: rows[i].ExpiresAt, LastRefreshedAt: rows[i].LastRefreshedAt, Scopes: decodeScopes(rows[i].ScopesJSON),
		}
	}
	return out, nil
}

// Delete removes one of the caller's connected tokens.
func (s *OAuthTokenService) Delete(ctx context.Context, id uuid.UUID) (bool, error) {
	ctx, span := startSpan(ctx, "oauthtoken.delete")
	defer span.End()
	caller := contracts.CallerFrom(ctx)
	token, err := s.store.GetOAuthTokenByID(ctx, caller.UserID, id)
	if err != nil || token == nil {
		return false, err
	}
	ok, err := s.store.DeleteOAuthToken(ctx, caller.UserID, id)
	if err != nil || !ok {
		return ok, err
	}
	s.audit.Emit(ctx, audit.EmitParams{Type: audit.EventTokenRemoved, Outcome: audit.OutcomeSuccess,
		TargetKind: "oauth_token", TargetProvider: token.ProviderKey, TargetAccount: token.Account})
	return true, nil
}

// GetAccessToken returns a live access token for the provider (optionally a specific account),
// refreshing it server-to-server when within the refresh window. Returns nil when none is found.
func (s *OAuthTokenService) GetAccessToken(ctx context.Context, provider, account string) (*OAuthAccessToken, error) {
	return s.accessToken(ctx, provider, account, "")
}

// ReplaceRejectedToken returns an access token to use in place of rejected, one the provider's
// resource has just refused although its expiry said it was live. The token is refreshed whatever
// its expiry, unless the stored one already differs from rejected (a sibling refreshed it first), in
// which case that one is returned. Returns nil when no token is found, and ErrNotRefreshable when
// the account cannot be refreshed.
func (s *OAuthTokenService) ReplaceRejectedToken(ctx context.Context, provider, account, rejected string) (*OAuthAccessToken, error) {
	if rejected == "" {
		return nil, ValidationError{"the rejected access token is required."}
	}
	return s.accessToken(ctx, provider, account, rejected)
}

// accessToken is GetAccessToken when rejected is empty and ReplaceRejectedToken otherwise.
func (s *OAuthTokenService) accessToken(ctx context.Context, provider, account, rejected string) (*OAuthAccessToken, error) {
	ctx, span := startSpan(ctx, "oauthtoken.get_access_token")
	defer span.End()
	span.SetAttributes(attribute.String("oauth.provider", provider))

	caller := contracts.CallerFrom(ctx)
	providerID, err := s.resolver.ResolveProviderID(ctx, provider, caller.UserID)
	if err != nil {
		return nil, err
	}
	if providerID == uuid.Nil {
		s.audit.Emit(ctx, audit.EmitParams{Type: audit.EventTokenAccessed, Outcome: audit.OutcomeFailure,
			TargetKind: "oauth_token", TargetProvider: provider, TargetAccount: account, Detail: "unknown provider"})
		return nil, nil
	}

	token, err := s.store.FindOAuthToken(ctx, caller.UserID, providerID, account)
	if err != nil {
		return nil, err
	}
	if token == nil {
		s.audit.Emit(ctx, audit.EmitParams{Type: audit.EventTokenAccessed, Outcome: audit.OutcomeFailure,
			TargetKind: "oauth_token", TargetProvider: provider, TargetAccount: account, Detail: "no token for provider/account"})
		return nil, nil
	}

	s.audit.Emit(ctx, audit.EmitParams{Type: audit.EventTokenAccessed, Outcome: audit.OutcomeSuccess,
		TargetKind: "oauth_token", TargetProvider: token.ProviderKey, TargetAccount: token.Account})

	// A broken account is not handed out and not refreshed: its refresh token is known to be dead.
	if token.BrokenAt != nil {
		return nil, &BrokenError{Reason: deref(token.BrokenReason)}
	}

	scopes := decodeScopes(token.ScopesJSON)
	if rejected == "" && (token.ExpiresAt == nil || token.ExpiresAt.After(time.Now().Add(refreshWindow))) {
		access, err := s.cipher.DecryptToString(token.AccessTokenCipher)
		if err != nil {
			return nil, err
		}
		return &OAuthAccessToken{AccessToken: access, ExpiresAt: token.ExpiresAt, Scopes: scopes, TokenType: deref(token.TokenType)}, nil
	}

	manifest, err := s.resolver.GetOAuth(ctx, provider, token.UserID)
	if err != nil {
		return nil, err
	}
	config, err := s.store.GetOAuthConfigByProvider(ctx, token.UserID, providerID)
	if err != nil {
		return nil, err
	}
	if manifest == nil || config == nil || len(token.RefreshTokenCipher) == 0 {
		if rejected != "" {
			return nil, ErrNotRefreshable
		}
		access, err := s.cipher.DecryptToString(token.AccessTokenCipher)
		if err != nil {
			return nil, err
		}
		return &OAuthAccessToken{AccessToken: access, ExpiresAt: token.ExpiresAt, Scopes: scopes, TokenType: deref(token.TokenType)}, nil
	}

	return s.refresh(ctx, manifest, config, token, rejected)
}

// refresh exchanges the refresh token for a new access token. A non-empty rejected is an access
// token the caller knows to be dead: the stored token is then replaced however far off its expiry.
func (s *OAuthTokenService) refresh(ctx context.Context, manifest *manifests.Manifest, config *store.OAuthProviderConfig, token *store.OAuthToken, rejected string) (*OAuthAccessToken, error) {
	ctx, span := startSpan(ctx, "oauthtoken.refresh")
	defer span.End()

	// Serialise refreshes of the same token: a refresh_token may rotate, so two concurrent
	// refreshes would race and the loser would persist a now-invalid refresh token. Once the
	// lock is held, re-read the row — a sibling may have already refreshed it.
	unlock := s.refreshLocks.lock(token.ID)
	defer unlock()
	// The guard is what a credential migration holds while it moves rows between stores. Waiting
	// for it here, and reading the row again below, means a refresh never rotates a token that is
	// being copied, and never refreshes a row that has just been moved away.
	release, err := s.guard.Shared(ctx)
	if err != nil {
		return nil, OAuthRefreshError{"refresh held off: " + err.Error()}
	}
	defer release()
	if fresh, err := s.store.FindOAuthToken(ctx, token.UserID, token.ProviderID, token.Account); err == nil {
		if fresh == nil {
			return nil, nil // the account went away while this refresh waited
		}
		if fresh.BrokenAt != nil {
			return nil, &BrokenError{Reason: deref(fresh.BrokenReason)}
		}
		live := fresh.ExpiresAt != nil && fresh.ExpiresAt.After(time.Now().Add(refreshWindow))
		if live || rejected != "" {
			access, derr := s.cipher.DecryptToString(fresh.AccessTokenCipher)
			if derr != nil {
				return nil, derr
			}
			// A rejected token is replaced unless the stored one is already another: then a
			// sibling has refreshed it, and refreshing again would only spend a rotation.
			if (rejected == "" && live) || (rejected != "" && access != rejected) {
				return &OAuthAccessToken{AccessToken: access, ExpiresAt: fresh.ExpiresAt, Scopes: decodeScopes(fresh.ScopesJSON), TokenType: deref(fresh.TokenType)}, nil
			}
		}
		token = fresh
	}
	if len(token.RefreshTokenCipher) == 0 {
		return nil, ErrNotRefreshable
	}
	if manifest.MCP != nil {
		return s.refreshServer(ctx, manifest, config, token)
	}

	refreshTok, err := s.cipher.DecryptToString(token.RefreshTokenCipher)
	if err != nil {
		return nil, err
	}
	clientID, err := s.cipher.DecryptToString(config.ClientIDCipher)
	if err != nil {
		return nil, err
	}
	clientSecret, err := s.cipher.DecryptToString(config.ClientSecretCipher)
	if err != nil {
		return nil, err
	}

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshTok},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}

	emitFail := func(msg string) error {
		s.audit.Emit(ctx, audit.EmitParams{Type: audit.EventTokenRefreshed, Outcome: audit.OutcomeFailure,
			TargetKind: "oauth_token", TargetProvider: token.ProviderKey, TargetAccount: token.Account, Detail: msg})
		return OAuthRefreshError{msg}
	}

	status, body, err := postForm(ctx, s.client, manifest.TokenEndpoint, form, manifest.TokenEndpointAuthMethod)
	if err != nil {
		return nil, emitFail(fmt.Sprintf("refresh failed for %s: %v", manifest.Key, err))
	}
	if !httpOK(status) {
		// Status only — the raw provider body can carry token-like material.
		return nil, emitFail(fmt.Sprintf("refresh failed for %s: HTTP %d", manifest.Key, status))
	}

	var parsed tokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.AccessToken == "" {
		return nil, emitFail(fmt.Sprintf("refresh response for %s had no access_token", manifest.Key))
	}

	var expiresAt *time.Time
	if parsed.ExpiresIn > 0 {
		t := time.Now().UTC().Add(time.Duration(parsed.ExpiresIn) * time.Second)
		expiresAt = &t
	}

	accessBlob, err := s.cipher.EncryptString(parsed.AccessToken)
	if err != nil {
		return nil, err
	}
	token.AccessTokenCipher = accessBlob
	if parsed.RefreshToken != "" {
		newRefresh, err := s.cipher.EncryptString(parsed.RefreshToken)
		if err != nil {
			return nil, err
		}
		token.RefreshTokenCipher = newRefresh
	}
	token.ExpiresAt = expiresAt
	now := time.Now().UTC()
	token.LastRefreshedAt = &now
	if err := s.store.UpdateOAuthToken(ctx, token); err != nil {
		return nil, err
	}

	s.audit.Emit(ctx, audit.EmitParams{Type: audit.EventTokenRefreshed, Outcome: audit.OutcomeSuccess,
		TargetKind: "oauth_token", TargetProvider: token.ProviderKey, TargetAccount: token.Account})

	return &OAuthAccessToken{AccessToken: parsed.AccessToken, ExpiresAt: expiresAt, Scopes: decodeScopes(token.ScopesJSON)}, nil
}

// tokenResponse is the subset of an OAuth token endpoint JSON response we consume.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

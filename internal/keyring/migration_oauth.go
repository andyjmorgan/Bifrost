package keyring

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/keyring/audit"
	"github.com/andyjmorgan/Bifrost/internal/keyring/manifests"
	"github.com/andyjmorgan/Bifrost/internal/keyring/service"
	"github.com/andyjmorgan/Bifrost/internal/keyring/store"
	"github.com/andyjmorgan/Bifrost/internal/keyringref"
)

// maxBrokenReason bounds a broken account's reason to its column (varchar(1024)).
const maxBrokenReason = 1000

// ProviderTaken reports whether owner's keyring already has a provider under the key ref gives that
// is not a server's own connection: one somebody wrote, which a move must not overwrite.
func (m *Migration) ProviderTaken(ctx context.Context, ref keyringref.Ref) (bool, error) {
	owner, err := ownerID(ref)
	if err != nil {
		return false, err
	}
	manifest, err := m.s.resolver.GetOAuth(ctx, ref.Provider, owner)
	if err != nil {
		return false, err
	}
	return manifest != nil && manifest.MCP == nil, nil
}

// ImportOAuth writes a server's OAuth connection into owner's keyring as a provider discovered from
// the server, the client registered for it and, when the connection has tokens, its one account. It
// replaces what the provider held, so a move that stopped half way is simply repeated.
//
// The redirect URI recorded is the gateway's callback, which is what the client was registered with
// wherever the connection was kept before.
func (m *Migration) ImportOAuth(ctx context.Context, owner, tenant uuid.UUID, use keyringref.Use, c keyringref.ServerConnection) (keyringref.Ref, error) {
	if use.Slug == "" {
		return keyringref.Ref{}, errors.New("keyring: a server connection needs the server's slug")
	}
	if c.TokenEndpoint == "" || c.ClientID == "" {
		return keyringref.Ref{}, errors.New("keyring: a server connection needs a token endpoint and a client id")
	}
	s := m.s
	ref := keyringref.Ref{Owner: owner.String(), Kind: keyringref.KindOAuth, Provider: keyringref.ServerCredentialName(use.Slug), Account: keyringref.ServerAccount}
	ctx = asGateway(ctx, owner, tenant, use)

	if err := s.resolver.UpsertOAuth(ctx, manifests.Manifest{
		Key: ref.Provider, Name: c.Name, AuthorizationEndpoint: c.AuthorizationEndpoint, TokenEndpoint: c.TokenEndpoint,
		ScopeDelimiter: " ", DefaultScopes: c.Scopes,
		MCP: &manifests.MCPProvider{
			ServerURL: c.ServerURL, Issuer: c.Issuer, Resource: c.Resource,
			RegistrationKind: c.RegistrationKind, RegistrationEndpoint: c.RegistrationEndpoint, RedirectURI: s.redirectURI,
		},
	}); err != nil {
		return keyringref.Ref{}, fmt.Errorf("keyring %s: %w", ref, err)
	}
	providerID, err := s.resolver.ResolveProviderID(ctx, ref.Provider, owner)
	if err != nil || providerID == uuid.Nil {
		return keyringref.Ref{}, fmt.Errorf("keyring %s: the provider was not stored: %w", ref, errors.Join(err, keyringref.ErrNotFound))
	}

	// The client.
	clientID, err := s.cipher.EncryptString(c.ClientID)
	if err != nil {
		return keyringref.Ref{}, err
	}
	clientSecret := []byte{}
	if c.ClientSecret != "" {
		if clientSecret, err = s.cipher.EncryptString(c.ClientSecret); err != nil {
			return keyringref.Ref{}, err
		}
	}
	scopesJSON := service.EncodeScopes(c.Scopes)
	redirect := s.redirectURI
	config, err := s.store.GetOAuthConfigByProvider(ctx, owner, providerID)
	if err != nil {
		return keyringref.Ref{}, fmt.Errorf("keyring %s: %w", ref, err)
	}
	if config == nil {
		err = s.store.InsertOAuthConfig(ctx, &store.OAuthProviderConfig{
			UserID: owner, TenantID: tenant, ProviderID: providerID, ProviderKey: ref.Provider,
			ClientIDCipher: clientID, ClientSecretCipher: clientSecret, ScopesJSON: &scopesJSON, RedirectURI: &redirect,
		})
	} else {
		config.ProviderKey, config.ClientIDCipher, config.ClientSecretCipher, config.ScopesJSON, config.RedirectURI = ref.Provider, clientID, clientSecret, &scopesJSON, &redirect
		err = s.store.UpdateOAuthConfig(ctx, config)
	}
	if err != nil {
		return keyringref.Ref{}, fmt.Errorf("keyring %s: %w", ref, err)
	}
	s.audit.Emit(ctx, audit.EmitParams{Type: audit.EventCredentialCreated, Outcome: audit.OutcomeSuccess,
		TargetKind: "provider", TargetProvider: ref.Provider, TargetName: ref.Provider, Detail: "MCP server connection moved from the gateway's store (" + c.RegistrationKind + ")"})

	// The tokens, when there are any.
	existing, err := s.store.FindOAuthToken(ctx, owner, providerID, keyringref.ServerAccount)
	if err != nil {
		return keyringref.Ref{}, fmt.Errorf("keyring %s: %w", ref, err)
	}
	if !c.Connected {
		if existing != nil {
			if _, err := s.store.DeleteOAuthToken(ctx, owner, existing.ID); err != nil {
				return keyringref.Ref{}, fmt.Errorf("keyring %s: %w", ref, err)
			}
		}
		return ref, nil
	}
	row := existing
	if row == nil {
		row = &store.OAuthToken{UserID: owner, TenantID: tenant, ProviderID: providerID, ProviderKey: ref.Provider, Account: keyringref.ServerAccount}
	}
	if row.AccessTokenCipher, err = s.cipher.EncryptString(c.AccessToken); err != nil {
		return keyringref.Ref{}, err
	}
	row.RefreshTokenCipher = []byte{}
	if c.RefreshToken != "" {
		if row.RefreshTokenCipher, err = s.cipher.EncryptString(c.RefreshToken); err != nil {
			return keyringref.Ref{}, err
		}
	}
	row.ScopesJSON, row.ExpiresAt, row.LastRefreshedAt = &scopesJSON, c.ExpiresAt, c.LastRefreshedAt
	row.TokenType, row.BrokenAt, row.BrokenReason = nil, nil, nil
	if c.TokenType != "" {
		row.TokenType = &c.TokenType
	}
	if c.Broken != "" {
		now, reason := time.Now().UTC(), c.Broken
		if r := []rune(reason); len(r) > maxBrokenReason {
			reason = string(r[:maxBrokenReason])
		}
		row.BrokenAt, row.BrokenReason = &now, &reason
	}
	if existing == nil {
		err = s.store.InsertOAuthToken(ctx, row)
	} else {
		err = s.store.UpdateOAuthToken(ctx, row)
	}
	if err != nil {
		return keyringref.Ref{}, fmt.Errorf("keyring %s: %w", ref, err)
	}
	s.audit.Emit(ctx, audit.EmitParams{Type: audit.EventTokenAdded, Outcome: audit.OutcomeSuccess,
		TargetKind: "oauth_token", TargetProvider: ref.Provider, TargetAccount: keyringref.ServerAccount, Detail: "moved from the gateway's store"})
	return ref, nil
}

// ExportOAuth decrypts the server connection ref names, whole. The read is audited. Only a provider
// discovered from an MCP server is exported: an account of a provider somebody wrote is not a
// server's connection and has nowhere to go.
func (m *Migration) ExportOAuth(ctx context.Context, ref keyringref.Ref, use keyringref.Use) (keyringref.ServerConnection, error) {
	var c keyringref.ServerConnection
	s := m.s
	owner, err := ownerID(ref)
	if err != nil {
		return c, err
	}
	manifest, err := s.resolver.GetOAuth(ctx, ref.Provider, owner)
	if err != nil {
		return c, fmt.Errorf("keyring %s: %w", ref, err)
	}
	if manifest == nil {
		return c, fmt.Errorf("keyring %s: %w", ref, keyringref.ErrNotFound)
	}
	if manifest.MCP == nil {
		return c, fmt.Errorf("keyring %s is an account of a provider somebody wrote, not a server's own connection: %w", ref, keyringref.ErrUnusable)
	}
	config, err := s.store.GetOAuthConfigByProvider(ctx, owner, manifest.ID)
	if err != nil {
		return c, fmt.Errorf("keyring %s: %w", ref, err)
	}
	if config == nil {
		return c, fmt.Errorf("keyring %s has no registered client: %w", ref, keyringref.ErrNotFound)
	}
	c = keyringref.ServerConnection{
		ServerURL: manifest.MCP.ServerURL, Name: manifest.Name, Issuer: manifest.MCP.Issuer,
		AuthorizationEndpoint: manifest.AuthorizationEndpoint, TokenEndpoint: manifest.TokenEndpoint,
		RegistrationEndpoint: manifest.MCP.RegistrationEndpoint, RegistrationKind: manifest.MCP.RegistrationKind,
		Resource: manifest.MCP.Resource, Scopes: service.DecodeScopes(config.ScopesJSON),
	}
	if c.ClientID, err = s.cipher.DecryptToString(config.ClientIDCipher); err != nil {
		return c, fmt.Errorf("keyring %s: %w", ref, err)
	}
	if len(config.ClientSecretCipher) > 0 {
		if c.ClientSecret, err = s.cipher.DecryptToString(config.ClientSecretCipher); err != nil {
			return c, fmt.Errorf("keyring %s: %w", ref, err)
		}
	}
	token, err := s.store.FindOAuthToken(ctx, owner, manifest.ID, ref.Account)
	if err != nil {
		return c, fmt.Errorf("keyring %s: %w", ref, err)
	}
	if token == nil {
		return c, nil
	}
	c.Connected, c.ExpiresAt, c.LastRefreshedAt = true, token.ExpiresAt, token.LastRefreshedAt
	c.TokenType = deref(token.TokenType)
	if scopes := service.DecodeScopes(token.ScopesJSON); len(scopes) > 0 {
		c.Scopes = scopes
	}
	if c.AccessToken, err = s.cipher.DecryptToString(token.AccessTokenCipher); err != nil {
		return c, fmt.Errorf("keyring %s: %w", ref, err)
	}
	if len(token.RefreshTokenCipher) > 0 {
		if c.RefreshToken, err = s.cipher.DecryptToString(token.RefreshTokenCipher); err != nil {
			return c, fmt.Errorf("keyring %s: %w", ref, err)
		}
	}
	if token.BrokenAt != nil {
		c.Broken = deref(token.BrokenReason)
		if c.Broken == "" {
			c.Broken = "refresh refused"
		}
	}
	s.audit.Emit(asGateway(ctx, owner, token.TenantID, use), audit.EmitParams{Type: audit.EventTokenAccessed, Outcome: audit.OutcomeSuccess,
		TargetKind: "oauth_token", TargetProvider: ref.Provider, TargetAccount: token.Account, Detail: "read whole, to move it"})
	return c, nil
}

// RemoveOAuth deletes the server connection ref names from its owner's keyring: the provider, its
// client and its tokens. A provider somebody wrote is left alone.
func (m *Migration) RemoveOAuth(ctx context.Context, ref keyringref.Ref, use keyringref.Use) error {
	owner, err := ownerID(ref)
	if err != nil {
		return err
	}
	return m.s.forgetOAuth(ctx, owner, ref, use)
}

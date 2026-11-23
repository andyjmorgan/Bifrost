package service

import (
	"context"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/andyjmorgan/Bifrost/internal/keyring/audit"
	"github.com/andyjmorgan/Bifrost/internal/keyring/contracts"
	"github.com/andyjmorgan/Bifrost/internal/keyring/manifests"
	"github.com/andyjmorgan/Bifrost/internal/keyring/store"
)

// oauthManifestKind is the manifest kind of an OAuth provider, the only kind that owns credentials.
const oauthManifestKind = "oauth"

// ProviderService adds, edits and removes the caller's OAuth providers and records each change in
// the audit trail. A provider decides where an account's refresh token is sent, and removing one
// takes its app credentials and every connected account with it, so neither may pass unrecorded.
type ProviderService struct {
	store    store.Store
	resolver *manifests.Resolver
	audit    *audit.Log
}

// NewProviderService builds the service.
func NewProviderService(s store.Store, r *manifests.Resolver, a *audit.Log) *ProviderService {
	return &ProviderService{store: s, resolver: r, audit: a}
}

// Save adds or edits one of the caller's providers. An add is audited as CredentialCreated and an
// edit as CredentialChanged, both against the provider's key.
func (s *ProviderService) Save(ctx context.Context, m manifests.Manifest) error {
	ctx, span := startSpan(ctx, "provider.save")
	defer span.End()
	span.SetAttributes(attribute.String("oauth.provider", m.Key))

	existing, err := s.resolver.ResolveProviderID(ctx, m.Key, contracts.CallerFrom(ctx).UserID)
	if err != nil {
		return err
	}
	if err := s.resolver.UpsertOAuth(ctx, m); err != nil {
		return err
	}
	event := audit.EventCredentialCreated
	if existing != uuid.Nil {
		event = audit.EventCredentialChanged
	}
	s.audit.Emit(ctx, audit.EmitParams{Type: event, Outcome: audit.OutcomeSuccess,
		TargetKind: "provider", TargetProvider: m.Key, TargetName: m.Key})
	return nil
}

// Remove deletes one of the caller's providers together with its app credentials and its connected
// accounts. Every row that goes is audited: TokenRemoved for each account, CredentialRemoved for
// the app credentials and for the provider itself.
func (s *ProviderService) Remove(ctx context.Context, kind, key string) (bool, error) {
	ctx, span := startSpan(ctx, "provider.remove")
	defer span.End()
	span.SetAttributes(attribute.String("oauth.provider", key))

	caller := contracts.CallerFrom(ctx)
	// What the cascade is about to take is read first, so the trail can name it.
	var accounts []string
	hadConfig := false
	if kind == oauthManifestKind {
		providerID, err := s.resolver.ResolveProviderID(ctx, key, caller.UserID)
		if err != nil {
			return false, err
		}
		if providerID != uuid.Nil {
			tokens, err := s.store.ListOAuthTokens(ctx, caller.UserID)
			if err != nil {
				return false, err
			}
			for i := range tokens {
				if tokens[i].ProviderID == providerID {
					accounts = append(accounts, tokens[i].Account)
				}
			}
			config, err := s.store.GetOAuthConfigByProvider(ctx, caller.UserID, providerID)
			if err != nil {
				return false, err
			}
			hadConfig = config != nil
		}
	}

	ok, err := s.resolver.Delete(ctx, kind, key)
	if err != nil || !ok {
		return ok, err
	}
	for _, account := range accounts {
		s.audit.Emit(ctx, audit.EmitParams{Type: audit.EventTokenRemoved, Outcome: audit.OutcomeSuccess,
			TargetKind: "oauth_token", TargetProvider: key, TargetAccount: account, Detail: "provider removed"})
	}
	if hadConfig {
		s.audit.Emit(ctx, audit.EmitParams{Type: audit.EventCredentialRemoved, Outcome: audit.OutcomeSuccess,
			TargetKind: "provider_config", TargetProvider: key, TargetName: key, Detail: "provider removed"})
	}
	s.audit.Emit(ctx, audit.EmitParams{Type: audit.EventCredentialRemoved, Outcome: audit.OutcomeSuccess,
		TargetKind: "provider", TargetProvider: key, TargetName: key})
	return true, nil
}

package keyring

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/keyring/audit"
	"github.com/andyjmorgan/Bifrost/internal/keyring/contracts"
	"github.com/andyjmorgan/Bifrost/internal/keyring/manifests"
	"github.com/andyjmorgan/Bifrost/internal/keyring/service"
	"github.com/andyjmorgan/Bifrost/internal/keyring/store"
	"github.com/andyjmorgan/Bifrost/internal/keyringref"
)

// gatewayTransport is the transport an audit row carries when the gateway itself fetched the
// credential, in process, for one of its MCP servers. Rows from the HTTP API carry "http".
const gatewayTransport = "gateway"

// Source lends keyring credentials to the gateway for the MCP servers that reference one, and holds
// the credentials a server owns itself. It is the keyring's side of keyringref.Keyring, and the only
// way the gateway reaches into the keyring: it calls the same services as the HTTP API, in process,
// as the user the reference belongs to.
//
// Every lookup is scoped to the reference's owner, so a reference can only ever resolve inside the
// keyring of the person who set it, or inside the gateway's own space (keyringref.GatewayOwner) for
// a credential that belongs to a server. Every fetch and every change is recorded in the keyring's
// audit trail with the gateway and the server as the caller.
type Source struct {
	store    store.Store
	resolver *manifests.Resolver
	secrets  *service.APIKeyService
	tokens   *service.OAuthTokenService
	audit    *audit.Log
}

func newSource(st store.Store, resolver *manifests.Resolver, secrets *service.APIKeyService, tokens *service.OAuthTokenService, log *audit.Log) *Source {
	return &Source{store: st, resolver: resolver, secrets: secrets, tokens: tokens, audit: log}
}

// Source returns the gateway's door into the keyring, or nil when the keyring is off. The nil is an
// untyped interface value, so the gateway can test it directly.
func (k *Keyring) Source() keyringref.Holder {
	if k == nil {
		return nil
	}
	return k.source
}

// Check implements keyringref.Keyring. It reads the row, never the secret, and records nothing.
func (s *Source) Check(ctx context.Context, ref keyringref.Ref) error {
	owner, err := ownerID(ref)
	if err != nil {
		return err
	}
	switch ref.Kind {
	case keyringref.KindOAuth:
		_, err = s.findToken(ctx, owner, ref)
	case keyringref.KindSecret:
		_, err = s.findSecret(ctx, owner, ref)
	default:
		err = fmt.Errorf("keyring: unknown credential kind %q", ref.Kind)
	}
	return err
}

// OAuthToken implements keyringref.Keyring.
func (s *Source) OAuthToken(ctx context.Context, ref keyringref.Ref, use keyringref.Use, rejected string) (keyringref.Token, error) {
	owner, err := ownerID(ref)
	if err != nil {
		return keyringref.Token{}, err
	}
	row, err := s.findToken(ctx, owner, ref)
	if err != nil {
		s.refused(ctx, owner, use, err, audit.EmitParams{TargetKind: "oauth_token", TargetProvider: ref.Provider, TargetAccount: ref.Account})
		return keyringref.Token{}, err
	}
	ctx = asGateway(ctx, owner, row.TenantID, use)
	var tok *service.OAuthAccessToken
	if rejected == "" {
		tok, err = s.tokens.GetAccessToken(ctx, ref.Provider, ref.Account)
	} else {
		tok, err = s.tokens.ReplaceRejectedToken(ctx, ref.Provider, ref.Account, rejected)
	}
	switch {
	case errors.Is(err, service.ErrNotRefreshable):
		return keyringref.Token{}, fmt.Errorf("keyring %s: %w", ref, keyringref.ErrNoRefresh)
	case err != nil:
		return keyringref.Token{}, fmt.Errorf("keyring %s: %w", ref, err)
	case tok == nil: // removed between the two reads
		return keyringref.Token{}, fmt.Errorf("keyring %s: %w", ref, keyringref.ErrNotFound)
	}
	return keyringref.Token{AccessToken: tok.AccessToken, ExpiresAt: tok.ExpiresAt}, nil
}

// SecretHeader implements keyringref.Keyring.
func (s *Source) SecretHeader(ctx context.Context, ref keyringref.Ref, use keyringref.Use) (keyringref.Header, error) {
	owner, err := ownerID(ref)
	if err != nil {
		return keyringref.Header{}, err
	}
	row, err := s.findSecret(ctx, owner, ref)
	if err != nil {
		s.refused(ctx, owner, use, err, audit.EmitParams{TargetKind: "api_key", TargetName: ref.Secret})
		return keyringref.Header{}, err
	}
	sec, err := s.secrets.GetByName(asGateway(ctx, owner, row.TenantID, use), ref.Secret)
	if err != nil {
		return keyringref.Header{}, fmt.Errorf("keyring %s: %w", ref, err)
	}
	if sec == nil { // removed between the two reads
		return keyringref.Header{}, fmt.Errorf("keyring %s: %w", ref, keyringref.ErrNotFound)
	}
	name, value := service.AssembleHeader(sec.Kind, deref(sec.Header), deref(sec.Prefix), deref(sec.Username), sec.Secret)
	return keyringref.Header{Name: name, Value: value}, nil
}

// Describe implements keyringref.Keyring. It reads the row, never the secret, and records nothing.
func (s *Source) Describe(ctx context.Context, ref keyringref.Ref) (keyringref.Description, error) {
	owner, err := ownerID(ref)
	if err != nil {
		return keyringref.Description{}, err
	}
	if ref.Kind != keyringref.KindSecret {
		return keyringref.Description{}, fmt.Errorf("keyring: %s has no description: %w", ref, keyringref.ErrUnusable)
	}
	row, err := s.findSecret(ctx, owner, ref)
	if err != nil {
		return keyringref.Description{}, err
	}
	if contracts.CredentialKindFromWire(row.Kind) == contracts.KindHTTPBasic {
		return keyringref.Description{Scheme: keyringref.SchemeBasic, Username: deref(row.Username)}, nil
	}
	return keyringref.Description{Scheme: keyringref.SchemeHeader, HeaderName: service.HeaderName(deref(row.HeaderName))}, nil
}

// PutServerSecret implements keyringref.Keyring. The secret is written through the same service as
// one a person stores, so it is sealed and audited the same way; only its owner differs.
func (s *Source) PutServerSecret(ctx context.Context, use keyringref.Use, in keyringref.ServerSecret) (keyringref.Ref, error) {
	return s.putSecret(ctx, gatewayOwnerID, uuid.Nil, use, in)
}

// putSecret stores a server's static credential in owner's keyring under the server's name.
func (s *Source) putSecret(ctx context.Context, owner, tenant uuid.UUID, use keyringref.Use, in keyringref.ServerSecret) (keyringref.Ref, error) {
	if use.Slug == "" {
		return keyringref.Ref{}, errors.New("keyring: a server credential needs the server's slug")
	}
	ref := keyringref.Ref{Owner: owner.String(), Kind: keyringref.KindSecret, Secret: keyringref.ServerCredentialName(use.Slug)}
	params := service.CreateAPIKeyParams{Name: ref.Secret, Secret: &in.Value}
	about := "The credential of the MCP server " + use.Slug + ", held for the gateway."
	params.Description = &about
	if in.URL != "" {
		params.BaseURL = &in.URL
	}
	switch in.Scheme {
	case keyringref.SchemeBasic:
		if in.Username == "" || in.Value == "" {
			return keyringref.Ref{}, errors.New("keyring: a Basic credential needs a username and a password")
		}
		params.Kind, params.Username = contracts.KindHTTPBasic, &in.Username
	case keyringref.SchemeHeader:
		if in.HeaderName == "" || in.Value == "" || strings.ContainsAny(in.HeaderName, " :\r\n") {
			return keyringref.Ref{}, errors.New("keyring: a header credential needs a valid header name and a value")
		}
		params.Kind, params.Header = contracts.KindHeaderAPIKey, &in.HeaderName
	default:
		return keyringref.Ref{}, fmt.Errorf("keyring: unknown credential scheme %q", in.Scheme)
	}
	if _, err := s.secrets.Create(asGateway(ctx, owner, tenant, use), params); err != nil {
		return keyringref.Ref{}, fmt.Errorf("keyring %s: %w", ref, err)
	}
	return ref, nil
}

// Forget implements keyringref.Keyring.
func (s *Source) Forget(ctx context.Context, ref keyringref.Ref, use keyringref.Use) error {
	if !ref.HeldByGateway() {
		return nil // a person's credential outlives the server that borrowed it
	}
	if ref.Kind != keyringref.KindSecret {
		return fmt.Errorf("keyring: cannot forget %s", ref)
	}
	return s.forgetSecret(ctx, gatewayOwnerID, ref, use)
}

// forgetSecret deletes the stored secret ref names from owner's keyring, if it is there.
func (s *Source) forgetSecret(ctx context.Context, owner uuid.UUID, ref keyringref.Ref, use keyringref.Use) error {
	row, err := s.store.GetAPIKeyByName(ctx, owner, ref.Secret)
	if err != nil {
		return fmt.Errorf("keyring %s: %w", ref, err)
	}
	if row == nil {
		return nil
	}
	if _, err := s.secrets.Delete(asGateway(ctx, owner, row.TenantID, use), row.ID); err != nil {
		return fmt.Errorf("keyring %s: %w", ref, err)
	}
	return nil
}

// findToken reads the OAuth account ref names in owner's keyring.
func (s *Source) findToken(ctx context.Context, owner uuid.UUID, ref keyringref.Ref) (*store.OAuthToken, error) {
	if ref.Provider == "" || ref.Account == "" {
		return nil, fmt.Errorf("keyring: an OAuth account needs a provider and an account: %w", keyringref.ErrNotFound)
	}
	providerID, err := s.resolver.ResolveProviderID(ctx, ref.Provider, owner)
	if err != nil {
		return nil, fmt.Errorf("keyring %s: %w", ref, err)
	}
	if providerID == uuid.Nil {
		return nil, fmt.Errorf("keyring %s: %w", ref, keyringref.ErrNotFound)
	}
	row, err := s.store.FindOAuthToken(ctx, owner, providerID, ref.Account)
	if err != nil {
		return nil, fmt.Errorf("keyring %s: %w", ref, err)
	}
	if row == nil {
		return nil, fmt.Errorf("keyring %s: %w", ref, keyringref.ErrNotFound)
	}
	return row, nil
}

// findSecret reads the stored secret ref names in owner's keyring, without decrypting it, and
// refuses one that does not describe an HTTP header.
func (s *Source) findSecret(ctx context.Context, owner uuid.UUID, ref keyringref.Ref) (*store.APIKey, error) {
	if ref.Secret == "" {
		return nil, fmt.Errorf("keyring: a secret needs a name: %w", keyringref.ErrNotFound)
	}
	row, err := s.store.GetAPIKeyByName(ctx, owner, ref.Secret)
	if err != nil {
		return nil, fmt.Errorf("keyring %s: %w", ref, err)
	}
	if row == nil {
		return nil, fmt.Errorf("keyring %s: %w", ref, keyringref.ErrNotFound)
	}
	if why := notAHeader(row); why != "" {
		return nil, fmt.Errorf("keyring %s %s: %w", ref, why, keyringref.ErrUnusable)
	}
	return row, nil
}

// notAHeader says why a stored secret cannot authenticate an HTTP request, or "" when it can. A
// secret describes itself: http_basic is sent as Authorization: Basic with its username, and a
// secret with a header is sent in that header behind its prefix. Anything else (an SSH login, a
// connection string, an opaque value) says nothing about HTTP, and guessing would send it somewhere
// its owner never meant.
func notAHeader(row *store.APIKey) string {
	kind := contracts.CredentialKindFromWire(row.Kind)
	header := deref(row.HeaderName)
	if strings.ContainsAny(header, " :\r\n") {
		return "names an invalid header"
	}
	switch {
	case kind == contracts.KindHTTPBasic:
		if deref(row.Username) == "" {
			return "is HTTP Basic without a username"
		}
	case kind == contracts.KindHeaderAPIKey || header != "":
	default:
		return "is of kind " + string(kind) + ", which names no header"
	}
	return ""
}

// refused records a fetch that found nothing to hand out. The services record the ones that do.
func (s *Source) refused(ctx context.Context, owner uuid.UUID, use keyringref.Use, cause error, p audit.EmitParams) {
	p.Type, p.Outcome = audit.EventTokenAccessed, audit.OutcomeFailure
	p.Detail = "credential not found"
	if errors.Is(cause, keyringref.ErrUnusable) {
		p.Detail = "credential cannot be sent as an HTTP header"
	}
	s.audit.Emit(asGateway(ctx, owner, uuid.Nil, use), p)
}

// asGateway is the context a service is called with: the owner as the caller, so every row it
// touches is the owner's, and the gateway and the server as the audited source of the request.
// tenant is the credential row's own, so the audit row lands where its owner can read it.
func asGateway(ctx context.Context, owner, tenant uuid.UUID, use keyringref.Use) context.Context {
	method := "mcp-server " + use.Slug
	if use.ServerID != "" {
		method += " (" + use.ServerID + ")"
	}
	// A change to a credential the gateway holds is made by somebody: the row is the gateway's,
	// and the person is named beside the server.
	if use.Actor != "" {
		method += " by " + use.Actor
	}
	ctx = contracts.WithCaller(ctx, contracts.Caller{UserID: owner, TenantID: tenant})
	return audit.WithRequestInfo(ctx, audit.RequestInfo{Transport: gatewayTransport, Method: &method, Headers: map[string]string{}})
}

// gatewayOwnerID is keyringref.GatewayOwner as the keyring's stores take it.
var gatewayOwnerID = uuid.MustParse(keyringref.GatewayOwner)

// ownerID is the keyring user a reference belongs to. The keyring knows its users by the UUID
// subject of their identity provider token, which is the gateway's user id as well.
func ownerID(ref keyringref.Ref) (uuid.UUID, error) {
	id, err := uuid.Parse(ref.Owner)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, fmt.Errorf("keyring: user %q has no keyring: %w", ref.Owner, keyringref.ErrNotFound)
	}
	return id, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

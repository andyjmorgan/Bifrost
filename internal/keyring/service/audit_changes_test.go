package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/keyring/audit"
	"github.com/andyjmorgan/Bifrost/internal/keyring/contracts"
	"github.com/andyjmorgan/Bifrost/internal/keyring/manifests"
	"github.com/andyjmorgan/Bifrost/internal/keyring/store"
	"github.com/andyjmorgan/Bifrost/internal/keyring/store/memstore"
)

// changeRig is every service that can delete or change a credential, over one store and one audit
// sink, as one caller.
type changeRig struct {
	ms        *memstore.Mem
	log       *audit.Log
	ctx       context.Context
	user      uuid.UUID
	secrets   *APIKeyService
	keys      *AccessKeyService
	configs   *OAuthConfigService
	tokens    *OAuthTokenService
	providers *ProviderService
}

func newChangeRig(t *testing.T) *changeRig {
	t.Helper()
	cipher := testCipher(t)
	ms := memstore.New()
	loader, err := manifests.NewLoader()
	if err != nil {
		t.Fatal(err)
	}
	resolver := manifests.NewResolver(ms, loader)
	log := audit.NewLog(100, nil, nil)
	user := uuid.New()
	return &changeRig{
		ms: ms, log: log, user: user,
		ctx:       contracts.WithCaller(context.Background(), contracts.Caller{UserID: user, TenantID: uuid.New()}),
		secrets:   NewAPIKeyService(ms, cipher, log),
		keys:      NewAccessKeyService(ms, log),
		configs:   NewOAuthConfigService(ms, cipher, log, resolver),
		tokens:    NewOAuthTokenService(ms, cipher, log, resolver, nil),
		providers: NewProviderService(ms, resolver, log),
	}
}

// drain returns what has been emitted since the last call.
func (r *changeRig) drain() []audit.Event {
	var out []audit.Event
	for {
		select {
		case e := <-r.log.Reader():
			out = append(out, e)
		default:
			return out
		}
	}
}

// line is an event as "Type kind provider/account name detail", for comparing.
func line(e audit.Event) string {
	return strings.TrimSpace(fmt.Sprintf("%s %s %s/%s %s | %s", e.Type, deref(e.TargetKind), deref(e.TargetProvider), deref(e.TargetAccount), deref(e.TargetName), deref(e.Detail)))
}

func wantEvents(t *testing.T, r *changeRig, what string, want ...string) {
	t.Helper()
	got := r.drain()
	if len(got) != len(want) {
		lines := make([]string, len(got))
		for i, e := range got {
			lines[i] = line(e)
		}
		t.Fatalf("%s: %d events %q, want %d %q", what, len(got), lines, len(want), want)
	}
	for i, e := range got {
		if line(e) != want[i] {
			t.Errorf("%s: event %d = %q, want %q", what, i, line(e), want[i])
		}
		if e.UserID != r.user {
			t.Errorf("%s: event %d is recorded under %s, want the caller %s", what, i, e.UserID, r.user)
		}
		if e.Outcome != audit.OutcomeSuccess || e.CreatedAt.IsZero() {
			t.Errorf("%s: event %d lacks an outcome or a time", what, i)
		}
	}
}

// TestDeletesAndChangesAreAudited pins that nothing which removes or replaces a credential passes
// without a row saying who did it, to what and when, and that no row carries the credential.
func TestDeletesAndChangesAreAudited(t *testing.T) {
	r := newChangeRig(t)
	const first, second = "s3cret-value-one", "s3cret-value-two"

	// A stored secret: created, replaced, edited without a new value, deleted.
	created, err := r.secrets.Create(r.ctx, CreateAPIKeyParams{Name: "ci-token", Secret: strPtr(first), Kind: contracts.KindOpaque})
	if err != nil {
		t.Fatal(err)
	}
	wantEvents(t, r, "create secret", "CredentialCreated api_key / ci-token |")
	if _, err := r.secrets.Create(r.ctx, CreateAPIKeyParams{Name: "ci-token", Secret: strPtr(second), Kind: contracts.KindOpaque}); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, r, "replace secret", "CredentialChanged api_key / ci-token | secret replaced")
	if _, err := r.secrets.Create(r.ctx, CreateAPIKeyParams{Name: "ci-token", Description: strPtr("for CI"), Kind: contracts.KindOpaque}); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, r, "edit secret", "CredentialChanged api_key / ci-token | details edited; secret kept")
	if ok, err := r.secrets.Delete(r.ctx, created.ID); err != nil || !ok {
		t.Fatalf("delete secret: %v %v", ok, err)
	}
	wantEvents(t, r, "delete secret", "CredentialRemoved api_key / ci-token |")
	if ok, _ := r.secrets.Delete(r.ctx, created.ID); ok {
		t.Fatal("a second delete reported a row")
	}
	wantEvents(t, r, "delete nothing")

	// An access key: created, switched off and on, deleted.
	key, _, err := r.keys.Create(r.ctx, "laptop", nil, []string{"vault:read"})
	if err != nil {
		t.Fatal(err)
	}
	wantEvents(t, r, "create key", "CredentialCreated access_key / laptop |")
	if _, err := r.keys.SetEnabled(r.ctx, key.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.keys.SetEnabled(r.ctx, key.ID, true); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, r, "toggle key", "CredentialChanged access_key / laptop | disabled", "CredentialChanged access_key / laptop | enabled")
	if ok, err := r.keys.Delete(r.ctx, key.ID); err != nil || !ok {
		t.Fatalf("delete key: %v %v", ok, err)
	}
	wantEvents(t, r, "delete key", "CredentialRemoved access_key / laptop |")
	if ok, _ := r.keys.Delete(r.ctx, uuid.New()); ok {
		t.Fatal("deleting an unknown key reported a row")
	}
	wantEvents(t, r, "delete no key")

	// A provider: added, edited, given app credentials that are then replaced, and removed with a
	// connected account still on it.
	m := manifests.Manifest{Key: "acme", TokenEndpoint: "https://idp.example/token"}
	if err := r.providers.Save(r.ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := r.providers.Save(r.ctx, m); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, r, "save provider", "CredentialCreated provider acme/ acme |", "CredentialChanged provider acme/ acme |")
	if _, err := r.configs.Upsert(r.ctx, "acme", "client-id", strPtr(first), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.configs.Upsert(r.ctx, "acme", "client-id", strPtr(second), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.configs.Upsert(r.ctx, "acme", "client-id-2", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, r, "app credentials",
		"CredentialCreated provider_config acme/ acme |",
		"CredentialChanged provider_config acme/ acme | client secret replaced",
		"CredentialChanged provider_config acme/ acme | client id or scopes edited; client secret kept")

	providerID, err := manifests.NewResolver(r.ms, nil).ResolveProviderID(r.ctx, "acme", r.user)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ms.InsertOAuthToken(r.ctx, &store.OAuthToken{UserID: r.user, ProviderID: providerID, ProviderKey: "acme", Account: "ada@example.com"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.providers.Remove(r.ctx, "oauth", "acme"); err != nil || !ok {
		t.Fatalf("remove provider: %v %v", ok, err)
	}
	wantEvents(t, r, "remove provider",
		"TokenRemoved oauth_token acme/ada@example.com  | provider removed",
		"CredentialRemoved provider_config acme/ acme | provider removed",
		"CredentialRemoved provider acme/ acme |")
	if ok, _ := r.providers.Remove(r.ctx, "oauth", "acme"); ok {
		t.Fatal("removing a provider twice reported a row")
	}
	wantEvents(t, r, "remove no provider")

	// App credentials deleted on their own.
	if err := r.providers.Save(r.ctx, m); err != nil {
		t.Fatal(err)
	}
	id, err := r.configs.Upsert(r.ctx, "acme", "client-id", strPtr(first), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.drain()
	if ok, err := r.configs.Delete(r.ctx, id); err != nil || !ok {
		t.Fatalf("delete app credentials: %v %v", ok, err)
	}
	wantEvents(t, r, "delete app credentials", "CredentialRemoved provider_config acme/ acme |")
}

// TestChangeEventsCarryNoSecret replays the same changes and reads every field of every event for
// the values that were stored.
func TestChangeEventsCarryNoSecret(t *testing.T) {
	r := newChangeRig(t)
	const first, second = "s3cret-value-one", "s3cret-value-two"
	created, err := r.secrets.Create(r.ctx, CreateAPIKeyParams{Name: "ci-token", Secret: strPtr(first), Kind: contracts.KindOpaque})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.secrets.Create(r.ctx, CreateAPIKeyParams{Name: "ci-token", Secret: strPtr(second), Kind: contracts.KindOpaque}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.secrets.Delete(r.ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	key, minted, err := r.keys.Create(r.ctx, "laptop", nil, []string{"vault:read"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.keys.Delete(r.ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	events := r.drain()
	if len(events) != 5 {
		t.Fatalf("%d events, want 5", len(events))
	}
	for _, e := range events {
		all := fmt.Sprintf("%+v %s %s %s %s %s %s", e, deref(e.TargetKind), deref(e.TargetProvider), deref(e.TargetAccount), deref(e.TargetName), deref(e.Method), deref(e.Detail))
		for _, secret := range []string{first, second, minted, strings.TrimPrefix(minted, SecretPrefix)} {
			if strings.Contains(all, secret) {
				t.Fatalf("a %s event carries a secret value", e.Type)
			}
		}
	}
}

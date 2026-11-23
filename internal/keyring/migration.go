package keyring

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/keyring/contracts"
	"github.com/andyjmorgan/Bifrost/internal/keyring/service"
	"github.com/andyjmorgan/Bifrost/internal/keyringref"
)

// Migration is the keyring's side of `gateway migrate-credentials`: writing a server's credential
// into a named owner's keyring space, reading it back out, and removing it. It differs from Source
// in one way, which is why it is a separate type: the owner is whoever the command was told, where
// Source only ever writes to the gateway's own space. Nothing but the command uses it.
//
// Everything goes through the same services as the HTTP API, so what the command writes is sealed
// and audited like any other credential.
type Migration struct {
	s *Source
}

// Migration returns the migration door, or nil when the keyring is off.
func (k *Keyring) Migration() *Migration {
	if k == nil {
		return nil
	}
	return &Migration{s: k.source}
}

// OwnerTenant is the tenant the rows of owner are written under: that of a credential the owner
// already holds, so a moved credential lands beside the owner's others, or the nil tenant when the
// owner holds nothing yet. The gateway's own space has no tenant.
func (m *Migration) OwnerTenant(ctx context.Context, owner uuid.UUID) (uuid.UUID, error) {
	if owner == gatewayOwnerID {
		return uuid.Nil, nil
	}
	secrets, err := m.s.store.ListAPIKeys(ctx, owner)
	if err != nil {
		return uuid.Nil, err
	}
	if len(secrets) > 0 {
		return secrets[0].TenantID, nil
	}
	tokens, err := m.s.store.ListOAuthTokens(ctx, owner)
	if err != nil {
		return uuid.Nil, err
	}
	if len(tokens) > 0 {
		return tokens[0].TenantID, nil
	}
	return uuid.Nil, nil
}

// HasSecret reports whether owner's keyring holds a secret under the name ref gives.
func (m *Migration) HasSecret(ctx context.Context, ref keyringref.Ref) (bool, error) {
	owner, err := ownerID(ref)
	if err != nil {
		return false, err
	}
	row, err := m.s.store.GetAPIKeyByName(ctx, owner, ref.Secret)
	return row != nil, err
}

// ImportSecret stores a server's static credential in owner's keyring, under the server's name,
// replacing a secret of that name.
func (m *Migration) ImportSecret(ctx context.Context, owner, tenant uuid.UUID, use keyringref.Use, in keyringref.ServerSecret) (keyringref.Ref, error) {
	return m.s.putSecret(ctx, owner, tenant, use, in)
}

// ExportSecret decrypts the secret ref names and returns it as the static credential it describes.
// The read is audited like any other. A prefix the secret carries is folded into the value, which is
// what a header credential outside the keyring holds.
func (m *Migration) ExportSecret(ctx context.Context, ref keyringref.Ref, use keyringref.Use) (keyringref.ServerSecret, error) {
	owner, err := ownerID(ref)
	if err != nil {
		return keyringref.ServerSecret{}, err
	}
	row, err := m.s.findSecret(ctx, owner, ref)
	if err != nil {
		return keyringref.ServerSecret{}, err
	}
	sec, err := m.s.secrets.GetByName(asGateway(ctx, owner, row.TenantID, use), ref.Secret)
	if err != nil {
		return keyringref.ServerSecret{}, fmt.Errorf("keyring %s: %w", ref, err)
	}
	if sec == nil {
		return keyringref.ServerSecret{}, fmt.Errorf("keyring %s: %w", ref, keyringref.ErrNotFound)
	}
	out := keyringref.ServerSecret{URL: deref(sec.BaseURL)}
	if sec.Kind == contracts.KindHTTPBasic {
		out.Scheme, out.Username, out.Value = keyringref.SchemeBasic, deref(sec.Username), sec.Secret
		return out, nil
	}
	out.Scheme = keyringref.SchemeHeader
	out.HeaderName, out.Value = service.AssembleHeader(sec.Kind, deref(sec.Header), deref(sec.Prefix), "", sec.Secret)
	return out, nil
}

// RemoveSecret deletes the secret ref names from its owner's keyring, if it is there. Unlike
// Source.Forget it acts on any owner: the command removes what the command put there.
func (m *Migration) RemoveSecret(ctx context.Context, ref keyringref.Ref, use keyringref.Use) error {
	owner, err := ownerID(ref)
	if err != nil {
		return err
	}
	return m.s.forgetSecret(ctx, owner, ref, use)
}

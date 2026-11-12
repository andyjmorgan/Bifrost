package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/crypto"
	"github.com/andyjmorgan/Bifrost/internal/store"
)

// KeyPrefix marks a gateway API key.
const KeyPrefix = "mcpg_"

// ErrBadKey is returned for unknown or revoked keys.
var ErrBadKey = errors.New("auth: invalid api key")

// KeyStore is the slice of the store the key service needs.
type KeyStore interface {
	CreateAPIKey(ctx context.Context, k *store.APIKey) error
	GetAPIKeyByHash(ctx context.Context, hash []byte) (*store.APIKey, error)
	GetAPIKey(ctx context.Context, owner string, id uuid.UUID) (*store.APIKey, error)
}

// Keys issues and verifies API keys.
type Keys struct {
	store  KeyStore
	sealer *crypto.Sealer
}

// NewKeys builds the key service.
func NewKeys(s KeyStore, sealer *crypto.Sealer) *Keys { return &Keys{store: s, sealer: sealer} }

// Hash is the lookup digest of a secret.
func Hash(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

// Create mints a key in org that acts as createdBy. The plaintext is
// returned exactly once.
func (k *Keys) Create(ctx context.Context, org, createdBy, name string, scopes []string) (*store.APIKey, string, error) {
	for _, s := range scopes {
		if !containsString(AllScopes, s) {
			return nil, "", fmt.Errorf("auth: unknown scope %q", s)
		}
	}
	if len(scopes) == 0 {
		return nil, "", errors.New("auth: at least one scope is required")
	}
	raw := make([]byte, 30)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("auth: %w", err)
	}
	secret := KeyPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
	id := uuid.New()
	blob, err := k.sealer.Seal([]byte(secret), id[:])
	if err != nil {
		return nil, "", err
	}
	rec := &store.APIKey{
		ID: id, OrgID: org, CreatedBy: createdBy, Name: name, Prefix: secret[:len(KeyPrefix)+8],
		SecretHash: Hash(secret), SecretBlob: blob, Scopes: scopes, CreatedAt: time.Now(),
	}
	if err := k.store.CreateAPIKey(ctx, rec); err != nil {
		return nil, "", err
	}
	return rec, secret, nil
}

// Verify resolves a presented key to a caller.
func (k *Keys) Verify(ctx context.Context, secret string) (*Caller, error) {
	if !strings.HasPrefix(secret, KeyPrefix) {
		return nil, ErrBadKey
	}
	rec, err := k.store.GetAPIKeyByHash(ctx, Hash(secret))
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrBadKey
	}
	if err != nil {
		return nil, err
	}
	id := rec.ID
	return &Caller{
		OrgID: rec.OrgID, UserID: rec.CreatedBy, Subject: rec.Name, Type: ActorAPIKey, Scopes: rec.Scopes,
		APIKeyID: &id, APIKeyPrefix: rec.Prefix,
	}, nil
}

// Reveal decrypts an owner's key so the UI can drive end-to-end tests
// through the front door as that key.
func (k *Keys) Reveal(ctx context.Context, owner string, id uuid.UUID) (string, error) {
	rec, err := k.store.GetAPIKey(ctx, owner, id)
	if err != nil {
		return "", err
	}
	if rec.RevokedAt != nil {
		return "", ErrBadKey
	}
	pt, err := k.sealer.Open(rec.SecretBlob, rec.ID[:])
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

func containsString(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

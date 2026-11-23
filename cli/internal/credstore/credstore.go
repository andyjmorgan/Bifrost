// Package credstore resolves and persists the CLI's credential for a host.
//
// Resolution precedence (highest first):
//  1. VAULT_API_KEY environment variable — ephemeral, NEVER persisted.
//  2. OS keyring — macOS Keychain, Linux Secret Service, Windows Credential Manager.
//  3. A 0600 fallback credentials file (used only when no keyring is available).
//  4. The predecessor CLI's keyring entry, then its 0600 file — read-only. A login
//     found there is copied into this CLI's own store (2 or 3) and used from there on;
//     the predecessor's entry is never written or deleted.
//
// The keyring service and the file's directory are the identity's names: `bifrost`, or
// `dwvault` under that name, which has no predecessor and skips step 4.
//
// `auth login` writes the OAuth/API-key credential blob to the keyring (or the file
// fallback); the env variable is read-only and is never written to either store.
package credstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/zalando/go-keyring"

	"github.com/andyjmorgan/Bifrost/cli/internal/config"
	"github.com/andyjmorgan/Bifrost/cli/internal/identity"
)

const (
	envVar   = "VAULT_API_KEY"
	credFile = "credentials.json"
)

// Source identifies where a resolved key came from.
type Source string

const (
	// SourceEnv means the key came from the environment variable.
	SourceEnv Source = "env"
	// SourceKeyring means the key came from the OS keyring.
	SourceKeyring Source = "keyring"
	// SourceFile means the key came from the 0600 fallback file.
	SourceFile Source = "file"
)

// ErrNotFound is returned when no credential exists for the host.
var ErrNotFound = errors.New("no stored credential for host")

// CredentialType distinguishes an API-key credential from an OAuth one.
type CredentialType string

const (
	// TypeAPIKey is a static API-key credential.
	TypeAPIKey CredentialType = "api_key"
	// TypeOAuth is an OAuth credential (access/refresh tokens).
	TypeOAuth CredentialType = "oauth"
)

// Credential is a stored credential for a host: either an API key or an OAuth token set.
type Credential struct {
	Type             CredentialType `json:"type"`
	Secret           string         `json:"secret,omitempty"`
	Issuer           string         `json:"issuer,omitempty"`
	ClientID         string         `json:"clientId,omitempty"`
	Scopes           string         `json:"scopes,omitempty"`
	AccessToken      string         `json:"accessToken,omitempty"`
	RefreshToken     string         `json:"refreshToken,omitempty"`
	ExpiresAt        string         `json:"expiresAt,omitempty"`
	RefreshExpiresAt string         `json:"refreshExpiresAt,omitempty"`
	Account          string         `json:"account,omitempty"`
}

// Resolve returns the API key for host and where it came from, honouring precedence.
func Resolve(host string) (key string, src Source, err error) {
	c, src, err := ResolveCredential(host)
	if err != nil {
		return "", "", err
	}
	if c.Type != TypeAPIKey {
		return "", "", fmt.Errorf("stored credential for %s is %s, not api_key", host, c.Type)
	}
	return c.Secret, src, nil
}

// ResolveCredential returns the full stored credential for host and where it came from,
// honouring the env > keyring > file precedence, then the predecessor CLI's stores.
func ResolveCredential(host string) (*Credential, Source, error) {
	if v := os.Getenv(envVar); v != "" {
		return &Credential{Type: TypeAPIKey, Secret: v}, SourceEnv, nil
	}
	id := identity.Current()
	repairSessionBus()
	own, err := lookup(id.KeyringService, id.ConfigDir, host)
	if err != nil {
		return nil, "", err
	}
	if own.raw != "" {
		c, err := parseCredential(own.raw)
		return c, own.src, err
	}
	if c, src, err := adoptLegacy(id, host); !errors.Is(err, ErrNotFound) {
		return c, src, err
	}
	if own.keyringErr != nil {
		return nil, "", fmt.Errorf("read OS keyring (no file fallback available): %w", own.keyringErr)
	}
	return nil, "", ErrNotFound
}

// stored is the outcome of looking one host up under one set of storage names.
type stored struct {
	raw string // the credential blob; "" when neither store holds one
	src Source
	// keyringErr is a keyring failure other than not-found. It is reported only once the
	// file fallbacks have also come up empty, so a locked keyring never hides a usable file.
	keyringErr error
}

// lookup reads host's credential blob from the named keyring service, then from the 0600
// file in the named config dir. It never writes.
func lookup(service, dir, host string) (stored, error) {
	v, kerr := keyring.Get(service, host)
	if kerr == nil && v != "" {
		return stored{raw: v, src: SourceKeyring}, nil
	}
	if errors.Is(kerr, keyring.ErrNotFound) {
		kerr = nil
	}
	p, err := filePathIn(dir)
	if err != nil {
		return stored{}, err
	}
	m, err := fileLoadAt(p)
	if err != nil {
		return stored{}, err
	}
	return stored{raw: m[host], src: SourceFile, keyringErr: kerr}, nil
}

// adoptLegacy looks for a login the predecessor CLI stored for host and, when there is one,
// copies it into this CLI's own store so later runs no longer depend on the predecessor.
// The predecessor's keyring entry and files are only read. It returns ErrNotFound when
// there is no predecessor, the user logged out of host, or the predecessor has no login.
//
// A failed copy is not fatal: the credential is still returned, read in place, and the
// copy is attempted again on the next run.
func adoptLegacy(id identity.Identity, host string) (*Credential, Source, error) {
	legacy := id.Legacy
	if legacy == nil {
		return nil, "", ErrNotFound
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, "", err
	}
	if cfg.Hosts[host].LoggedOut {
		return nil, "", ErrNotFound
	}
	for _, legacyHost := range id.LegacyHosts(host) {
		found, err := lookup(legacy.KeyringService, legacy.ConfigDir, legacyHost)
		if err != nil {
			return nil, "", fmt.Errorf("read the %s login for %s: %w", legacy.Name, legacyHost, err)
		}
		if found.raw == "" {
			continue
		}
		c, err := parseCredential(found.raw)
		if err != nil {
			return nil, "", fmt.Errorf("read the %s login for %s: %w", legacy.Name, legacyHost, err)
		}
		// The blob is copied as stored rather than re-encoded, so nothing in it is lost.
		kind, err := storeRaw(host, found.raw)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "%s: using the %s login for %s in place; copying it to %s's own store failed: %v\n",
				id.Name, legacy.Name, legacyHost, id.Name, err)
			return c, found.src, nil
		}
		cfg.Hosts[host] = adoptedHost(legacyHost, c, kind)
		if err := config.Save(cfg); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "%s: copied the %s login for %s, but could not record it: %v\n",
				id.Name, legacy.Name, legacyHost, err)
		}
		_, _ = fmt.Fprintf(os.Stderr, "%s: found your %s login for %s and copied it to %s's own store (%s); the %s entry is unchanged\n",
			id.Name, legacy.Name, legacyHost, id.Name, kind, legacy.Name)
		return c, Source(kind), nil
	}
	return nil, "", ErrNotFound
}

// adoptedHost builds the non-secret host record for a copied login: the predecessor's own
// record when it kept one, otherwise what the credential itself says.
func adoptedHost(legacyHost string, c *Credential, kind config.StoreKind) config.Host {
	h := config.Host{Account: c.Account, Store: kind, Auth: config.AuthType(c.Type)}
	// The record is descriptive only, so an unreadable predecessor config costs nothing.
	if old, err := config.LoadLegacy(); err == nil {
		if rec, ok := old.Hosts[legacyHost]; ok {
			if rec.Account != "" {
				h.Account = rec.Account
			}
			if rec.Auth != "" {
				h.Auth = rec.Auth
			}
		}
	}
	return h
}

// Store persists key for host. It prefers the OS keyring; if that's unavailable it
// falls back to a 0600 file. The chosen store is returned so callers can record it.
func Store(host, key string) (config.StoreKind, error) {
	return StoreCredential(host, &Credential{Type: TypeAPIKey, Secret: key})
}

// StoreCredential persists c for host. It prefers the OS keyring; if that's unavailable
// it falls back to a 0600 file. The chosen store is returned so callers can record it.
func StoreCredential(host string, c *Credential) (config.StoreKind, error) {
	b, err := json.Marshal(c) //nolint:gosec // G117: serializing the credential for storage is the intended behavior
	if err != nil {
		return "", err
	}
	return storeRaw(host, string(b))
}

func storeRaw(host, key string) (config.StoreKind, error) {
	id := identity.Current()
	repairSessionBus()
	kerr := keyring.Set(id.KeyringService, host, key)
	if kerr == nil {
		_ = fileDelete(host) // drop any stale file copy once the keyring holds it
		return config.StoreKeyring, nil
	}
	// Surface the fallback rather than silently writing the secret to disk.
	_, _ = fmt.Fprintf(os.Stderr, "%s: OS keyring unavailable (%v); storing secret in 0600 file fallback\n", id.Name, kerr)
	if err := fileSet(host, key); err != nil {
		return "", fmt.Errorf("no OS keyring available and file fallback failed: %w", err)
	}
	return config.StoreFile, nil
}

func parseCredential(raw string) (*Credential, error) {
	if raw == "" {
		return nil, ErrNotFound
	}
	if raw[0] != '{' {
		return &Credential{Type: TypeAPIKey, Secret: raw}, nil
	}
	var c Credential
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, err
	}
	if c.Type == "" {
		return nil, fmt.Errorf("credential missing type")
	}
	return &c, nil
}

// Delete removes any stored credential for host from both of this CLI's stores. A
// predecessor's entry is left alone.
func Delete(host string) error {
	repairSessionBus()
	_ = keyring.Delete(identity.Current().KeyringService, host)
	return fileDelete(host)
}

// --- 0600 file fallback ---

// filePath is this CLI's own credentials file.
func filePath() (string, error) {
	return filePathIn(identity.Current().ConfigDir)
}

// filePathIn is the credentials file inside the named directory under the user config dir.
func filePathIn(dir string) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, dir, credFile), nil
}

func fileLoad() (map[string]string, string, error) {
	p, err := filePath()
	if err != nil {
		return nil, "", err
	}
	m, err := fileLoadAt(p)
	return m, p, err
}

// fileLoadAt reads the credentials file at p, returning an empty map when it is absent.
func fileLoadAt(p string) (map[string]string, error) {
	// Refuse a group/other-readable secrets file before reading, writing, or deleting.
	if err := checkPerms(p); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p) //nolint:gosec // G304: path is the program-controlled credentials file, not attacker-supplied
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return m, nil
}

func fileSet(host, key string) error {
	m, p, err := fileLoad()
	if err != nil {
		return err
	}
	m[host] = key
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return writeFileAtomic(p, b)
}

// writeFileAtomic writes b to a unique 0600 temp file in p's directory, then renames it
// over p. The unique temp name avoids a fixed-suffix race between concurrent invocations.
func writeFileAtomic(p string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(p), ".tmp-*") // CreateTemp makes the file 0600
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // no-op once the rename succeeds
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err //coverage:ignore Close error after a successful write not reproducible
	}
	return os.Rename(tmp, p)
}

func fileDelete(host string) error {
	m, p, err := fileLoad()
	if err != nil {
		return err
	}
	if _, ok := m[host]; !ok {
		return nil
	}
	delete(m, host)
	if len(m) == 0 {
		if rerr := os.Remove(p); rerr != nil && !os.IsNotExist(rerr) {
			return rerr
		}
		return nil
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(p, b, 0o600)
}

// checkPerms refuses to read a secrets file that group/other can access.
func checkPerms(p string) error {
	if runtime.GOOS == "windows" {
		return nil //coverage:ignore unreachable on the Linux test host
	}
	fi, err := os.Stat(p)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("insecure permissions %#o on %s (want 0600); refusing to read", fi.Mode().Perm(), p)
	}
	return nil
}

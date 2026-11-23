// Package config manages the CLI's host-keyed configuration file.
//
// The file holds only NON-SECRET metadata: which hosts you've logged into, the
// account a host's key belongs to, and where that host's secret is stored. The
// secret itself lives in the OS keyring or a separate 0600 credentials file — see
// package credstore. The file is $XDG_CONFIG_HOME/<name>/hosts.json (0600), where
// <name> is the identity's config dir: `bifrost`, or `dwvault` under that name.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/andyjmorgan/Bifrost/cli/internal/identity"
)

// StoreKind records where a host's secret is kept.
type StoreKind string

// AuthType records how a host authenticates (API key or OAuth).
type AuthType string

const (
	// StoreKeyring means the secret lives in the OS keyring.
	StoreKeyring StoreKind = "keyring"
	// StoreFile means the secret lives in a 0600 credentials file.
	StoreFile StoreKind = "file"

	// AuthAPIKey means the host authenticates with a static API key.
	AuthAPIKey AuthType = "api_key"
	// AuthOAuth means the host authenticates with an OAuth token.
	AuthOAuth AuthType = "oauth"
)

// Host is the non-secret record for one server host.
type Host struct {
	Account string    `json:"account,omitempty"` // identity the key belongs to (email/name/sub)
	Store   StoreKind `json:"store,omitempty"`   // where the secret is kept
	Auth    AuthType  `json:"auth,omitempty"`    // api_key or oauth
	// LoggedOut marks a host the user logged out of while a predecessor's login for it
	// still exists, so that login is not picked up again. Cleared by the next login.
	LoggedOut bool `json:"loggedOut,omitempty"`
}

// Config is the whole file.
type Config struct {
	Hosts map[string]Host `json:"hosts"`
}

const fileName = "hosts.json"

// createTemp is os.CreateTemp, indirected only so tests can exercise Save's atomic-write
// failure path (e.g. by handing back an already-closed file). Production never reassigns it.
var createTemp = os.CreateTemp

// Dir returns the CLI's config directory ($XDG_CONFIG_HOME/<name>), the home for
// hosts.json and other non-secret state files (e.g. the update-check cache).
func Dir() (string, error) {
	return dirNamed(identity.Current().ConfigDir)
}

func dirNamed(name string) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, name), nil
}

// Path returns the config file path ($XDG_CONFIG_HOME/<name>/hosts.json).
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fileName), nil
}

// Load reads the config, returning an empty (non-nil) Config if the file is absent.
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	return loadFile(p)
}

// LoadLegacy reads the predecessor CLI's config, which is never written. It returns an
// empty (non-nil) Config when this identity has no predecessor or the file is absent.
func LoadLegacy() (*Config, error) {
	legacy := identity.Current().Legacy
	if legacy == nil {
		return &Config{Hosts: map[string]Host{}}, nil
	}
	dir, err := dirNamed(legacy.ConfigDir)
	if err != nil {
		return nil, err
	}
	return loadFile(filepath.Join(dir, fileName))
}

func loadFile(p string) (*Config, error) {
	b, err := os.ReadFile(p) //nolint:gosec // G304: path is the program-controlled config dir, not attacker-supplied
	if os.IsNotExist(err) {
		return &Config{Hosts: map[string]Host{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	if c.Hosts == nil {
		c.Hosts = map[string]Host{}
	}
	return &c, nil
}

// Save writes the config atomically with 0600 perms (0700 dir).
func Save(c *Config) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err //coverage:ignore Config always marshals
	}
	// Unique temp file (0600) in the same dir, renamed over the target — avoids a
	// fixed-suffix race between concurrent invocations.
	f, err := createTemp(filepath.Dir(p), ".tmp-*")
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

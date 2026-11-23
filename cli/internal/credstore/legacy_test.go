package credstore

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/andyjmorgan/Bifrost/cli/internal/config"
	"github.com/andyjmorgan/Bifrost/cli/internal/identity"
)

const (
	legacyService = "dwvault"
	legacyDir     = "dwvault"

	// A dwvault OAuth login exactly as that CLI stored it. Field order and spacing are
	// deliberately not what json.Marshal would produce, so a copy that re-encoded the
	// blob instead of carrying it verbatim would show up as a difference.
	legacyOAuthBlob = `{"type":"oauth", "issuer":"https://auth.example/realms/Agents","clientId":"donkeywork-vault-cli",` +
		`"scopes":"openid profile email offline_access","accessToken":"legacy-access","refreshToken":"legacy-refresh",` +
		`"expiresAt":"2026-09-17T10:59:00Z","refreshExpiresAt":"offline","account":"user@example.com"}`
)

// legacyEnv gives a test a mock keyring and an empty config home, and returns that home.
func legacyEnv(t *testing.T) string {
	t.Helper()
	keyring.MockInit()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("VAULT_API_KEY", "")
	return home
}

// snapshot records every file under dir (bytes, mode, mtime) so a test can prove a tree was
// only read.
type snapshot map[string]fileState

type fileState struct {
	data  string
	mode  os.FileMode
	mtime time.Time
}

func snap(t *testing.T, dir string) snapshot {
	t.Helper()
	out := snapshot{}
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		st := fileState{mode: fi.Mode(), mtime: fi.ModTime()}
		if !fi.IsDir() {
			b, err := os.ReadFile(p) //nolint:gosec // G304,G122: test reads back its own temp tree
			if err != nil {
				return err
			}
			st.data = string(b)
		}
		out[p] = st
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}
	return out
}

func (s snapshot) assertUnchanged(t *testing.T, dir string) {
	t.Helper()
	now := snap(t, dir)
	if len(now) != len(s) {
		t.Fatalf("%s: %d entries, was %d", dir, len(now), len(s))
	}
	for p, was := range s {
		got, ok := now[p]
		if !ok {
			t.Fatalf("%s was removed", p)
		}
		if got.data != was.data || got.mode != was.mode || !got.mtime.Equal(was.mtime) {
			t.Fatalf("%s was modified", p)
		}
	}
}

// writeLegacyFiles lays down the dwvault config dir: its hosts.json and, when creds is not
// empty, its 0600 credentials file.
func writeLegacyFiles(t *testing.T, home, hosts, creds string) string {
	t.Helper()
	dir := filepath.Join(home, legacyDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if hosts != "" {
		if err := os.WriteFile(filepath.Join(dir, "hosts.json"), []byte(hosts), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if creds != "" {
		if err := os.WriteFile(filepath.Join(dir, credFile), []byte(creds), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The headline case: a machine logged in with dwvault against the Vault host runs bifrost
// with its default address. The login is found under the old names, used, copied to the
// new names, and the old entries are byte-for-byte as they were.
func TestAdoptLegacy_KeyringLoginForDefaultAddress(t *testing.T) {
	home := legacyEnv(t)
	legacyTree := writeLegacyFiles(t, home,
		`{"hosts":{"https://vault.donkeywork.dev":{"account":"owner@example.com","store":"file","auth":"oauth"}}}`, "")
	if err := keyring.Set(legacyService, identity.VaultAddr, legacyOAuthBlob); err != nil {
		t.Fatal(err)
	}
	before := snap(t, legacyTree)

	c, src, err := ResolveCredential(identity.BifrostAddr)
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if c.Type != TypeOAuth || c.RefreshToken != "legacy-refresh" || c.ClientID != "donkeywork-vault-cli" {
		t.Fatalf("credential = %+v", c)
	}
	if src != SourceKeyring {
		t.Fatalf("source = %q, want keyring (bifrost's own, after the copy)", src)
	}

	// The old entries are untouched.
	if got, err := keyring.Get(legacyService, identity.VaultAddr); err != nil || got != legacyOAuthBlob {
		t.Fatalf("dwvault keyring entry changed: err=%v same=%v", err, got == legacyOAuthBlob)
	}
	before.assertUnchanged(t, legacyTree)

	// The login now lives under bifrost's names, keyed by the address bifrost uses.
	if got, err := keyring.Get(service, identity.BifrostAddr); err != nil || got != legacyOAuthBlob {
		t.Fatalf("bifrost keyring entry: err=%v verbatim=%v", err, got == legacyOAuthBlob)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := config.Host{Account: "owner@example.com", Store: config.StoreKeyring, Auth: config.AuthOAuth}
	if cfg.Hosts[identity.BifrostAddr] != want {
		t.Fatalf("hosts.json record = %+v, want %+v", cfg.Hosts[identity.BifrostAddr], want)
	}

	// From here on bifrost stands alone: the dwvault entry can go away without effect.
	if err := keyring.Delete(legacyService, identity.VaultAddr); err != nil {
		t.Fatal(err)
	}
	if c, src, err := ResolveCredential(identity.BifrostAddr); err != nil || src != SourceKeyring || c.RefreshToken != "legacy-refresh" {
		t.Fatalf("after the dwvault entry is gone: c=%+v src=%q err=%v", c, src, err)
	}
}

// With no OS keyring (a headless box) the dwvault login is in its 0600 file, and the copy
// lands in bifrost's own 0600 file.
func TestAdoptLegacy_FileLogin(t *testing.T) {
	home := legacyEnv(t)
	keyring.MockInitWithError(errors.New("no keyring here"))
	t.Cleanup(keyring.MockInit)
	legacyTree := writeLegacyFiles(t, home, "", `{"https://vault.donkeywork.dev":"dwv_legacy_file"}`)
	before := snap(t, legacyTree)

	c, src, err := ResolveCredential(identity.BifrostAddr)
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if c.Type != TypeAPIKey || c.Secret != "dwv_legacy_file" || src != SourceFile {
		t.Fatalf("c=%+v src=%q", c, src)
	}
	before.assertUnchanged(t, legacyTree)

	own := filepath.Join(home, ownDir, credFile)
	fi, err := os.Stat(own)
	if err != nil {
		t.Fatalf("bifrost credentials file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("bifrost credentials file mode = %#o, want 0600", fi.Mode().Perm())
	}
	m, err := fileLoadAt(own)
	if err != nil || m[identity.BifrostAddr] != "dwv_legacy_file" {
		t.Fatalf("bifrost credentials file: m=%v err=%v", len(m), err)
	}
	// No hosts.json to copy from, so the record comes from the credential itself.
	cfg, _ := config.Load()
	want := config.Host{Store: config.StoreFile, Auth: config.AuthAPIKey}
	if cfg.Hosts[identity.BifrostAddr] != want {
		t.Fatalf("hosts.json record = %+v, want %+v", cfg.Hosts[identity.BifrostAddr], want)
	}
}

// A login for any other host (a self-hosted server, or --addr pointing at the Vault host)
// is found under the same key, and stays under that key.
func TestAdoptLegacy_SameHost(t *testing.T) {
	legacyEnv(t)
	const host = "https://vault.example"
	if err := keyring.Set(legacyService, host, "dwv_selfhosted"); err != nil {
		t.Fatal(err)
	}
	if k, _, err := Resolve(host); err != nil || k != "dwv_selfhosted" {
		t.Fatalf("Resolve: k=%q err=%v", k, err)
	}
	if got, err := keyring.Get(service, host); err != nil || got != "dwv_selfhosted" {
		t.Fatalf("bifrost entry for %s: %q, %v", host, got, err)
	}
	if got, err := keyring.Get(legacyService, host); err != nil || got != "dwv_selfhosted" {
		t.Fatalf("dwvault entry for %s changed: %q, %v", host, got, err)
	}
}

// The Vault-host alias applies to bifrost's default address only: a login for the Vault
// host is never handed to some other server.
func TestAdoptLegacy_NoAliasForOtherHosts(t *testing.T) {
	legacyEnv(t)
	if err := keyring.Set(legacyService, identity.VaultAddr, "dwv_vault_host"); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"https://bifrost.example/keyring", "https://bifrost.donkeywork.dev", "http://bifrost.donkeywork.dev/keyring"} {
		if _, _, err := ResolveCredential(host); !errors.Is(err, ErrNotFound) {
			t.Errorf("ResolveCredential(%s) = %v, want ErrNotFound", host, err)
		}
	}
}

// bifrost's own login always wins; the dwvault one is not read and nothing is copied.
func TestAdoptLegacy_OwnLoginWins(t *testing.T) {
	legacyEnv(t)
	if err := keyring.Set(legacyService, identity.VaultAddr, "dwv_old"); err != nil {
		t.Fatal(err)
	}
	if _, err := Store(identity.BifrostAddr, "dwv_new"); err != nil {
		t.Fatal(err)
	}
	if k, _, err := Resolve(identity.BifrostAddr); err != nil || k != "dwv_new" {
		t.Fatalf("Resolve: k=%q err=%v", k, err)
	}
	if got, _ := keyring.Get(legacyService, identity.VaultAddr); got != "dwv_old" {
		t.Fatalf("dwvault entry changed to %q", got)
	}
}

// VAULT_API_KEY still beats every store, and an env-only run copies nothing.
func TestAdoptLegacy_EnvKeySkipsStores(t *testing.T) {
	legacyEnv(t)
	if err := keyring.Set(legacyService, identity.VaultAddr, "dwv_old"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VAULT_API_KEY", "dwv_env")
	if k, src, err := Resolve(identity.BifrostAddr); err != nil || k != "dwv_env" || src != SourceEnv {
		t.Fatalf("Resolve: k=%q src=%q err=%v", k, src, err)
	}
	if _, err := keyring.Get(service, identity.BifrostAddr); err == nil {
		t.Fatal("an env-key run must not copy the dwvault login")
	}
}

// A host the user logged out of stays logged out, though the dwvault login is still there.
func TestAdoptLegacy_LoggedOutHostIsSkipped(t *testing.T) {
	legacyEnv(t)
	if err := keyring.Set(legacyService, identity.VaultAddr, "dwv_old"); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(&config.Config{Hosts: map[string]config.Host{identity.BifrostAddr: {LoggedOut: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveCredential(identity.BifrostAddr); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ResolveCredential = %v, want ErrNotFound", err)
	}
	if _, err := keyring.Get(service, identity.BifrostAddr); err == nil {
		t.Fatal("a logged-out host must not be copied")
	}
	if got, _ := keyring.Get(legacyService, identity.VaultAddr); got != "dwv_old" {
		t.Fatalf("dwvault entry changed to %q", got)
	}
}

// Delete removes bifrost's copy only.
func TestDelete_LeavesLegacyAlone(t *testing.T) {
	home := legacyEnv(t)
	legacyTree := writeLegacyFiles(t, home, "", `{"https://vault.donkeywork.dev":"dwv_file"}`)
	if err := keyring.Set(legacyService, identity.VaultAddr, "dwv_keyring"); err != nil {
		t.Fatal(err)
	}
	before := snap(t, legacyTree)
	if _, _, err := ResolveCredential(identity.BifrostAddr); err != nil {
		t.Fatal(err)
	}
	if err := Delete(identity.BifrostAddr); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := keyring.Get(service, identity.BifrostAddr); err == nil {
		t.Fatal("bifrost entry should be gone")
	}
	if got, _ := keyring.Get(legacyService, identity.VaultAddr); got != "dwv_keyring" {
		t.Fatalf("dwvault keyring entry changed to %q", got)
	}
	before.assertUnchanged(t, legacyTree)
}

// If the copy cannot be written the login still works, read in place.
func TestAdoptLegacy_CopyFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm-based unwritable dir not meaningful on Windows")
	}
	home := legacyEnv(t)
	keyring.MockInitWithError(errors.New("no keyring here"))
	t.Cleanup(keyring.MockInit)
	legacyTree := writeLegacyFiles(t, home, "", `{"https://vault.donkeywork.dev":"dwv_legacy_file"}`)
	own := filepath.Join(home, ownDir)
	if err := os.MkdirAll(own, 0o500); err != nil { //nolint:gosec // G301: test deliberately makes bifrost's dir read-only
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(own, 0o700) }) //nolint:gosec // G302: restoring dir perms in test cleanup
	before := snap(t, legacyTree)

	c, src, err := ResolveCredential(identity.BifrostAddr)
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if c.Secret != "dwv_legacy_file" || src != SourceFile {
		t.Fatalf("c=%+v src=%q", c, src)
	}
	before.assertUnchanged(t, legacyTree)
}

// If the credential is copied but hosts.json cannot be written, the login still resolves.
func TestAdoptLegacy_RecordFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm-based unwritable dir not meaningful on Windows")
	}
	home := legacyEnv(t)
	if err := keyring.Set(legacyService, identity.VaultAddr, "dwv_old"); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(home, ownDir)
	if err := os.MkdirAll(own, 0o500); err != nil { //nolint:gosec // G301: test deliberately makes bifrost's dir read-only
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(own, 0o700) }) //nolint:gosec // G302: restoring dir perms in test cleanup

	if k, src, err := Resolve(identity.BifrostAddr); err != nil || k != "dwv_old" || src != SourceKeyring {
		t.Fatalf("Resolve: k=%q src=%q err=%v", k, src, err)
	}
	if got, _ := keyring.Get(service, identity.BifrostAddr); got != "dwv_old" {
		t.Fatalf("bifrost entry = %q", got)
	}
}

// A dwvault login that cannot be read is reported, naming dwvault, not swallowed.
func TestAdoptLegacy_Unreadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm bits not meaningful on Windows")
	}
	t.Run("corrupt blob", func(t *testing.T) {
		legacyEnv(t)
		if err := keyring.Set(legacyService, identity.VaultAddr, "{invalid"); err != nil {
			t.Fatal(err)
		}
		_, _, err := ResolveCredential(identity.BifrostAddr)
		if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "dwvault login") {
			t.Fatalf("got %v, want a dwvault read error", err)
		}
	})
	t.Run("insecure file", func(t *testing.T) {
		home := legacyEnv(t)
		dir := writeLegacyFiles(t, home, "", `{"https://vault.donkeywork.dev":"dwv_x"}`)
		if err := os.Chmod(filepath.Join(dir, credFile), 0o644); err != nil { //nolint:gosec // G302: test deliberately loosens perms
			t.Fatal(err)
		}
		_, _, err := ResolveCredential(identity.BifrostAddr)
		if err == nil || !strings.Contains(err.Error(), "insecure permissions") {
			t.Fatalf("got %v, want the insecure-permissions refusal", err)
		}
	})
	t.Run("corrupt own hosts.json", func(t *testing.T) {
		home := legacyEnv(t)
		own := filepath.Join(home, ownDir)
		if err := os.MkdirAll(own, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(own, "hosts.json"), []byte("{bad"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ResolveCredential(identity.BifrostAddr); err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("got %v, want a config parse error", err)
		}
	})
}

// A keyring failure is still reported when no store, old or new, has a login.
func TestAdoptLegacy_KeyringFailureSurfaces(t *testing.T) {
	legacyEnv(t)
	keyringErr := errors.New("keyring locked")
	keyring.MockInitWithError(keyringErr)
	t.Cleanup(keyring.MockInit)
	if _, _, err := ResolveCredential(identity.BifrostAddr); !errors.Is(err, keyringErr) {
		t.Fatalf("got %v, want the keyring failure", err)
	}
}

// Run as dwvault, the CLI uses dwvault's own names and nothing else: it reads and writes
// the dwvault entries, and a bifrost login is invisible to it.
func TestDwvaultIdentity_UsesOldNamesOnly(t *testing.T) {
	home := legacyEnv(t)
	t.Cleanup(identity.SetForTesting(identity.Dwvault()))

	if err := keyring.Set(service, identity.VaultAddr, "dwv_bifrost_only"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveCredential(identity.VaultAddr); !errors.Is(err, ErrNotFound) {
		t.Fatalf("dwvault must not see a bifrost login: %v", err)
	}

	if err := keyring.Set(legacyService, identity.VaultAddr, "dwv_old"); err != nil {
		t.Fatal(err)
	}
	if k, src, err := Resolve(identity.VaultAddr); err != nil || k != "dwv_old" || src != SourceKeyring {
		t.Fatalf("Resolve: k=%q src=%q err=%v", k, src, err)
	}
	if _, err := os.Stat(filepath.Join(home, ownDir)); !os.IsNotExist(err) {
		t.Fatalf("dwvault created bifrost's config dir (stat err = %v)", err)
	}

	if store, err := Store(identity.VaultAddr, "dwv_replaced"); err != nil || store != config.StoreKeyring {
		t.Fatalf("Store: %q, %v", store, err)
	}
	if got, _ := keyring.Get(legacyService, identity.VaultAddr); got == "dwv_old" || got == "" {
		t.Fatalf("dwvault entry was not rewritten: %q", got)
	}
	if got, _ := keyring.Get(service, identity.VaultAddr); got != "dwv_bifrost_only" {
		t.Fatalf("dwvault touched the bifrost entry: %q", got)
	}

	p, err := filePath()
	if err != nil || p != filepath.Join(home, legacyDir, credFile) {
		t.Fatalf("filePath = %q, %v", p, err)
	}
	if err := Delete(identity.VaultAddr); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get(legacyService, identity.VaultAddr); err == nil {
		t.Fatal("dwvault logout should remove the dwvault entry")
	}
}

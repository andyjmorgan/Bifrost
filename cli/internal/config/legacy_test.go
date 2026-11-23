package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/andyjmorgan/Bifrost/cli/internal/identity"
)

const legacyHosts = `{"hosts":{"https://vault.donkeywork.dev":{"account":"owner@example.com","store":"file","auth":"oauth"}}}`

func writeLegacyHosts(t *testing.T, home, body string) string {
	t.Helper()
	dir := filepath.Join(home, "dwvault")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, fileName)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// bifrost reads dwvault's hosts.json without creating, changing or adopting it as its own.
func TestLoadLegacy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	p := writeLegacyHosts(t, home, legacyHosts)
	before, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}

	old, err := LoadLegacy()
	if err != nil {
		t.Fatalf("LoadLegacy: %v", err)
	}
	want := Host{Account: "owner@example.com", Store: StoreFile, Auth: AuthOAuth}
	if old.Hosts["https://vault.donkeywork.dev"] != want {
		t.Fatalf("legacy hosts = %+v", old.Hosts)
	}

	// bifrost's own config is separate, and still empty.
	own, err := Load()
	if err != nil || len(own.Hosts) != 0 {
		t.Fatalf("own config: hosts=%v err=%v", own.Hosts, err)
	}
	if err := Save(own); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, dirName, fileName)); err != nil {
		t.Fatalf("own hosts.json: %v", err)
	}

	after, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p) //nolint:gosec // G304: test reads back its own temp file
	if string(b) != legacyHosts || !after.ModTime().Equal(before.ModTime()) || after.Mode() != before.Mode() {
		t.Fatal("dwvault's hosts.json was modified")
	}
}

func TestLoadLegacy_Absent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old, err := LoadLegacy()
	if err != nil || old == nil || len(old.Hosts) != 0 {
		t.Fatalf("LoadLegacy with no dwvault dir: %+v, %v", old, err)
	}
}

func TestLoadLegacy_Malformed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	writeLegacyHosts(t, home, "{bad")
	if _, err := LoadLegacy(); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestLoadLegacy_NoConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if _, err := LoadLegacy(); err == nil {
		t.Fatal("expected an error with no config home")
	}
}

// As dwvault there is no predecessor: the config dir is dwvault's own, and LoadLegacy is empty
// even though that file exists.
func TestDwvaultIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	writeLegacyHosts(t, home, legacyHosts)
	t.Cleanup(identity.SetForTesting(identity.Dwvault()))

	dir, err := Dir()
	if err != nil || dir != filepath.Join(home, "dwvault") {
		t.Fatalf("Dir = %q, %v", dir, err)
	}
	own, err := Load()
	if err != nil || own.Hosts["https://vault.donkeywork.dev"].Account != "owner@example.com" {
		t.Fatalf("Load as dwvault: %+v, %v", own, err)
	}
	old, err := LoadLegacy()
	if err != nil || len(old.Hosts) != 0 {
		t.Fatalf("LoadLegacy as dwvault: %+v, %v", old, err)
	}
}

func TestLoggedOutRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(&Config{Hosts: map[string]Host{"https://h": {LoggedOut: true}}}); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil || !got.Hosts["https://h"].LoggedOut {
		t.Fatalf("Load: %+v, %v", got, err)
	}
}

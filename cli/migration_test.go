package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/andyjmorgan/Bifrost/cli/internal/config"
	"github.com/andyjmorgan/Bifrost/cli/internal/identity"
)

// withStdin feeds s to os.Stdin for the duration of the test.
func withStdin(t *testing.T, s string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(s); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old; _ = r.Close() })
}

// The whole journey of an existing dwvault user, through the real commands: bifrost is
// logged in from the first command with no login step; logging out sticks although the
// dwvault login is still there; logging in again works; and the dwvault entry is the same
// at the end as at the start.
func TestExistingDwvaultLogin_NoSecondLogin(t *testing.T) {
	resetClientGlobals(t)
	srv, _ := keyringServer(t, "")
	base := srv.URL
	if err := keyring.Set("dwvault", base, "dwv_test"); err != nil {
		t.Fatal(err)
	}

	// First command: it just works, and says once where the login came from.
	stdout, stderr, err := run(t, "--addr", base, "credentials", "list")
	if err != nil || !strings.Contains(stdout, "acme-api") {
		t.Fatalf("first list: err=%v stdout=%q", err, stdout)
	}
	if !strings.Contains(stderr, "found your dwvault login") || !strings.Contains(stderr, "the dwvault entry is unchanged") {
		t.Fatalf("first run did not say the login was carried over: %q", stderr)
	}
	if strings.Contains(stderr, "dwv_test") {
		t.Fatalf("the notice leaked the key: %q", stderr)
	}

	// Second command: served from bifrost's own store, silently.
	if _, stderr, err := run(t, "--addr", base, "credentials", "list"); err != nil || stderr != "" {
		t.Fatalf("second list: err=%v stderr=%q", err, stderr)
	}
	if _, stderr, err := run(t, "--addr", base, "auth", "status"); err != nil || !strings.Contains(stderr, "source:  keyring") {
		t.Fatalf("auth status: err=%v stderr=%q", err, stderr)
	}
	if _, _, err := run(t, "--addr", base, "auth", "login", "--api-key"); err == nil || !strings.Contains(err.Error(), "already logged in") {
		t.Fatalf("login over a carried-over login: %v", err)
	}

	// Logout removes bifrost's copy and stays logged out.
	if _, stderr, err := run(t, "--addr", base, "auth", "logout"); err != nil || !strings.Contains(stderr, "logged out of "+base) {
		t.Fatalf("logout: err=%v stderr=%q", err, stderr)
	}
	if _, _, err := run(t, "--addr", base, "credentials", "list"); err == nil || !strings.Contains(err.Error(), "run `bifrost auth login`") {
		t.Fatalf("list after logout: %v", err)
	}
	if _, _, err := run(t, "--addr", base, "auth", "status"); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("status after logout: %v", err)
	}
	cfg, err := config.Load()
	if err != nil || !cfg.Hosts[base].LoggedOut {
		t.Fatalf("hosts.json after logout: %+v, %v", cfg.Hosts, err)
	}

	// A new login replaces the marker.
	withStdin(t, "dwv_test\n")
	if _, stderr, err := run(t, "--addr", base, "auth", "login", "--api-key"); err != nil || !strings.Contains(stderr, "logged in to "+base) {
		t.Fatalf("login: err=%v stderr=%q", err, stderr)
	}
	cfg, err = config.Load()
	want := config.Host{Account: "owner@example.com", Store: config.StoreKeyring, Auth: config.AuthAPIKey}
	if err != nil || cfg.Hosts[base] != want {
		t.Fatalf("hosts.json after login: %+v, %v", cfg.Hosts[base], err)
	}
	if _, _, err := run(t, "--addr", base, "credentials", "list"); err != nil {
		t.Fatalf("list after login: %v", err)
	}

	// Throughout, the dwvault entry was only read.
	if got, err := keyring.Get("dwvault", base); err != nil || got != "dwv_test" {
		t.Fatalf("dwvault keyring entry changed: %q, %v", got, err)
	}
}

// Run as dwvault the same binary is the old CLI for storage too: it reads the dwvault
// login, never looks at bifrost's, and logging out removes the dwvault entry outright.
func TestAuth_AsDwvault(t *testing.T) {
	resetClientGlobals(t)
	t.Cleanup(identity.SetForTesting(identity.Dwvault()))
	home := os.Getenv("XDG_CONFIG_HOME")
	srv, _ := keyringServer(t, "")
	base := srv.URL

	if err := keyring.Set("bifrost", base, "dwv_test"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "--addr", base, "credentials", "list"); err == nil || !strings.Contains(err.Error(), "run `dwvault auth login`") {
		t.Fatalf("dwvault used a bifrost login, or gave the wrong advice: %v", err)
	}

	withStdin(t, "dwv_test\n")
	if _, stderr, err := run(t, "--addr", base, "auth", "login", "--api-key"); err != nil || !strings.Contains(stderr, "logged in to "+base) {
		t.Fatalf("login: err=%v stderr=%q", err, stderr)
	}
	if got, err := keyring.Get("dwvault", base); err != nil || !strings.Contains(got, "dwv_test") {
		t.Fatalf("dwvault login was not stored under the dwvault name: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "dwvault", "hosts.json")); err != nil { //nolint:gosec // G703: home is the test's own temp dir
		t.Fatalf("dwvault hosts.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "bifrost")); !os.IsNotExist(err) { //nolint:gosec // G703: home is the test's own temp dir
		t.Fatalf("dwvault wrote into bifrost's config dir (stat err = %v)", err)
	}
	if stdout, _, err := run(t, "--addr", base, "credentials", "list"); err != nil || !strings.Contains(stdout, "acme-api") {
		t.Fatalf("list: err=%v", err)
	}

	if _, stderr, err := run(t, "--addr", base, "auth", "logout"); err != nil || strings.Contains(stderr, "left in place") {
		t.Fatalf("logout: err=%v stderr=%q", err, stderr)
	}
	if _, err := keyring.Get("dwvault", base); err == nil {
		t.Fatal("dwvault logout left the dwvault entry behind")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Hosts[base]; ok {
		t.Fatalf("dwvault logout left a host record: %+v", cfg.Hosts[base])
	}
	if got, _ := keyring.Get("bifrost", base); got != "dwv_test" {
		t.Fatalf("dwvault touched the bifrost entry: %q", got)
	}
}

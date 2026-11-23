package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andyjmorgan/Bifrost/cli/internal/selfupdate"
)

// runMainEnv makes the test binary behave as the CLI itself. The end-to-end tests below
// start this binary under a chosen file name, so the identity really is taken from argv[0],
// the way a `dwvault` symlink selects it in production.
const runMainEnv = "BIFROST_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// installAs places this test binary in a fresh directory under the given command name:
// a symlink by default, a full copy when copyFile is set (for tests that replace it).
func installAs(t *testing.T, name string, copyFile bool) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if !copyFile {
		if err := os.Symlink(self, path); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		return path
	}
	src, err := os.Open(self) //nolint:gosec // G304: the test binary's own path
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o755) //nolint:gosec // G302,G304: an executable copy in the test's temp dir
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// execCLI runs the binary at path as the CLI with a clean, isolated environment. Only
// commands that never reach the OS keyring are run this way: the child has no mock.
func execCLI(t *testing.T, path string, env map[string]string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command(path, args...) //nolint:gosec // G204: path is this test binary under a test-chosen name
	cmd.Env = []string{
		runMainEnv + "=1",
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"PATH=" + os.Getenv("PATH"),
		// Under `go test -cover` the child is a coverage build too; without somewhere to
		// write its counters it would add a warning to the stderr these tests compare.
		"GOCOVERDIR=" + t.TempDir(),
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		exit = ee.ExitCode()
	default:
		t.Fatalf("run %s %v: %v", path, args, err)
	}
	return out.String(), errb.String(), exit
}

func TestInvocationName_Version(t *testing.T) {
	for _, name := range []string{"bifrost", "dwvault"} {
		t.Run(name, func(t *testing.T) {
			bin := installAs(t, name, false)
			stdout, stderr, exit := execCLI(t, bin, nil, "version")
			if exit != 0 || stdout != "" || stderr != name+" version dev\n" {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
			}
		})
	}
}

// Under its own name the CLI is bifrost throughout: the new default address, both address
// variables, and help that says bifrost.
func TestInvocationName_BifrostHelp(t *testing.T) {
	bin := installAs(t, "bifrost", false)
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"default", nil, `(default "https://bifrost.donkeywork.dev/keyring")`},
		{"VAULT_ADDR", map[string]string{"VAULT_ADDR": "https://vault.donkeywork.dev"}, `(default "https://vault.donkeywork.dev")`},
		{"BIFROST_ADDR first", map[string]string{"BIFROST_ADDR": "https://b.example/keyring", "VAULT_ADDR": "https://v.example"},
			`(default "https://b.example/keyring")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, exit := execCLI(t, bin, tc.env, "--help")
			if exit != 0 || stdout != "" {
				t.Fatalf("exit=%d stdout=%q", exit, stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("help lacks %q:\n%s", tc.want, stderr)
			}
			if !strings.Contains(stderr, "Bifrost credential CLI") || !strings.Contains(stderr, "bifrost [command]") {
				t.Fatalf("help is not bifrost's:\n%s", stderr)
			}
			if strings.Contains(stderr, "dwvault") {
				t.Fatalf("bifrost help mentions dwvault:\n%s", stderr)
			}
		})
	}
}

// Started as dwvault it is the old CLI: the old default address, VAULT_ADDR only, and help
// that says dwvault and never bifrost.
func TestInvocationName_DwvaultHelp(t *testing.T) {
	bin := installAs(t, "dwvault", false)
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"default", nil, `(default "https://vault.donkeywork.dev")`},
		{"VAULT_ADDR", map[string]string{"VAULT_ADDR": "https://v.example"}, `(default "https://v.example")`},
		{"BIFROST_ADDR ignored", map[string]string{"BIFROST_ADDR": "https://b.example/keyring"}, `(default "https://vault.donkeywork.dev")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, exit := execCLI(t, bin, tc.env, "--help")
			if exit != 0 || stdout != "" {
				t.Fatalf("exit=%d stdout=%q", exit, stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("help lacks %q:\n%s", tc.want, stderr)
			}
			if !strings.Contains(stderr, "DonkeyWork Vault credential CLI") || !strings.Contains(stderr, "dwvault [command]") {
				t.Fatalf("help is not dwvault's:\n%s", stderr)
			}
			if strings.Contains(strings.ToLower(stderr), "bifrost") {
				t.Fatalf("dwvault help mentions bifrost:\n%s", stderr)
			}
		})
	}
	for _, sub := range [][]string{{"credentials", "--help"}, {"auth", "login", "--help"}, {"skill", "--help"}, {"keys", "--help"}, {"oauth", "--help"}} {
		_, stderr, exit := execCLI(t, bin, nil, sub...)
		if exit != 0 || strings.Contains(strings.ToLower(stderr), "bifrost") || !strings.Contains(stderr, "dwvault") {
			t.Fatalf("dwvault %v: exit=%d\n%s", sub, exit, stderr)
		}
	}
}

// Each name sends its requests where that name's variables say, at the right path.
func TestInvocationName_Requests(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, addrVar, wantPath string
	}{
		{"bifrost", "/keyring", "BIFROST_ADDR", "/keyring/api/v1/api-keys"},
		{"bifrost", "", "VAULT_ADDR", "/api/v1/api-keys"},
		{"dwvault", "", "VAULT_ADDR", "/api/v1/api-keys"},
	} {
		t.Run(tc.name+" via "+tc.addrVar, func(t *testing.T) {
			srv, paths := keyringServer(t, tc.prefix)
			bin := installAs(t, tc.name, false)
			env := map[string]string{tc.addrVar: srv.URL + tc.prefix, "VAULT_API_KEY": "dwv_test"}
			stdout, stderr, exit := execCLI(t, bin, env, "credentials", "list")
			if exit != 0 || !strings.Contains(stdout, "acme-api") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
			}
			if got := paths(); len(got) != 1 || got[0] != tc.wantPath {
				t.Fatalf("request paths = %v, want [%s]", got, tc.wantPath)
			}

			stdout, stderr, exit = execCLI(t, bin, env, "credentials", "get", "acme-api")
			if exit != 0 || stdout != "s3cr3t-value\n" || stderr != "" {
				t.Fatalf("get: exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
			}
		})
	}
}

// An error names the command the user typed, so its advice can be followed as written.
func TestInvocationName_Errors(t *testing.T) {
	srv, _ := keyringServer(t, "")
	for _, name := range []string{"bifrost", "dwvault"} {
		bin := installAs(t, name, false)
		stdout, stderr, exit := execCLI(t, bin, map[string]string{"VAULT_ADDR": srv.URL, "VAULT_API_KEY": "dwv_bad"}, "credentials", "list")
		if exit != 1 || stdout != "" || !strings.HasPrefix(stderr, name+": ") {
			t.Fatalf("%s: exit=%d stdout=%q stderr=%q", name, exit, stdout, stderr)
		}
		if strings.Contains(stderr, "dwv_bad") {
			t.Fatalf("%s: the key leaked into the error: %q", name, stderr)
		}
	}
}

func fileSum(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: a file in the test's temp dir
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// The state Bifrost's repository is in today, end to end: public, with no CLI release.
// `update` says so, exits 0, prints nothing on stdout and leaves the binary alone.
func TestUpdateE2E_NoReleasePublished(t *testing.T) {
	api, requests := githubHost(t, selfupdate.DefaultRepo, map[string]map[string]string{
		"v1.0.0": {platformAsset("bifrost"): "a gateway release must never be installed as the CLI"},
	})
	bin := installAs(t, "bifrost", true)
	before := fileSum(t, bin)
	env := map[string]string{selfupdate.APIEnv: api}
	for _, args := range [][]string{{"update", "--force"}, {"update", "--force", "--check"}} {
		stdout, stderr, exit := execCLI(t, bin, env, args...)
		if exit != 0 || stdout != "" {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q", args, exit, stdout, stderr)
		}
		if !strings.Contains(stderr, "no release has been published yet at github.com/andyjmorgan/Bifrost/releases") ||
			!strings.Contains(stderr, "Nothing was changed") {
			t.Fatalf("%v: stderr=%q", args, stderr)
		}
	}
	if requests() != 2 {
		t.Fatalf("%d requests, want one listing per run and no download", requests())
	}
	if fileSum(t, bin) != before {
		t.Fatal("the binary was modified")
	}
}

// With a release published the same command fetches, verifies and replaces the binary,
// taking the highest cli-v tag and ignoring every other release in the repository. This is
// the control for the tests around it: the updater does work when there is something to do.
func TestUpdateE2E_ReleaseReplacesBinary(t *testing.T) {
	const next = "#!/bin/sh\necho next-bifrost\n"
	api, requests := githubHost(t, selfupdate.DefaultRepo, map[string]map[string]string{
		"cli-v9.9.9":  {platformAsset("bifrost"): next},
		"cli-v9.9.10": {platformAsset("bifrost"): next + "# 9.9.10\n"},
		"v99.0.0":     {platformAsset("bifrost"): "gateway"},
	})
	bin := installAs(t, "bifrost", true)
	env := map[string]string{selfupdate.APIEnv: api}

	// A build from source is left alone unless forced.
	_, stderr, exit := execCLI(t, bin, env, "update")
	if exit != 0 || !strings.Contains(stderr, "built from source") || requests() != 0 {
		t.Fatalf("unforced: exit=%d requests=%d stderr=%q", exit, requests(), stderr)
	}

	stdout, stderr, exit := execCLI(t, bin, env, "update", "--force")
	if exit != 0 || stdout != "" || !strings.Contains(stderr, "updated bifrost dev → v9.9.10") {
		t.Fatalf("forced: exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	got, err := os.ReadFile(bin) //nolint:gosec // G304: a file in the test's temp dir
	if err != nil || string(got) != next+"# 9.9.10\n" {
		t.Fatalf("the binary was not replaced with the cli-v9.9.10 release (err=%v)", err)
	}
}

// A release whose checksum does not match is refused and the binary is left as it was.
func TestUpdateE2E_ChecksumMismatchLeavesBinary(t *testing.T) {
	asset := platformAsset("bifrost")
	api, _ := githubHost(t, selfupdate.DefaultRepo, map[string]map[string]string{
		"cli-v9.9.9": {asset: "tampered", "SHA256SUMS": strings.Repeat("0", 64) + "  " + asset + "\n"},
	})
	bin := installAs(t, "bifrost", true)
	before := fileSum(t, bin)
	_, stderr, exit := execCLI(t, bin, map[string]string{selfupdate.APIEnv: api}, "update", "--force")
	if exit != 1 || !strings.Contains(stderr, "checksum mismatch") {
		t.Fatalf("exit=%d stderr=%q", exit, stderr)
	}
	if fileSum(t, bin) != before {
		t.Fatal("the binary was modified despite the checksum mismatch")
	}
}

// Pointed at a repository that does not exist, or is private, the command fails and says why.
func TestUpdateE2E_RepositoryNotFound(t *testing.T) {
	api, _ := githubHost(t, selfupdate.DefaultRepo, nil)
	bin := installAs(t, "bifrost", true)
	env := map[string]string{selfupdate.APIEnv: api, selfupdate.RepoEnv: "someone/private"}
	stdout, stderr, exit := execCLI(t, bin, env, "update", "--force")
	if exit != 1 || stdout != "" || !strings.Contains(stderr, "someone/private was not found, or is not public") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
}

// Started as dwvault the CLI never updates itself, even with a release published and --force
// given: no request is made and the file is untouched. A release is a bifrost binary, and it
// must not arrive under this name unasked.
func TestUpdateE2E_DwvaultNeverUpdates(t *testing.T) {
	const replaced = "#!/bin/sh\necho replaced\n"
	api, requests := githubHost(t, selfupdate.DefaultRepo, map[string]map[string]string{
		"cli-v9.9.9": {platformAsset("bifrost"): replaced, platformAsset("dwvault"): replaced},
		"v9.9.9":     {platformAsset("dwvault"): replaced},
	})
	for _, viaSymlink := range []bool{true, false} {
		// Either a symlink to a bifrost binary, as installed, or a binary itself named dwvault.
		bin := installAs(t, "dwvault", true)
		target := bin
		if viaSymlink {
			target = installAs(t, "bifrost", true)
			bin = filepath.Join(filepath.Dir(target), "dwvault")
			if err := os.Symlink(target, bin); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
		}
		before := fileSum(t, target)
		env := map[string]string{selfupdate.APIEnv: api, "DWVAULT_REPO": selfupdate.DefaultRepo}
		for _, args := range [][]string{{"update"}, {"update", "--force"}, {"update", "--check"}, {"--update"}, {"__update-check"}} {
			stdout, stderr, exit := execCLI(t, bin, env, args...)
			if exit != 0 || stdout != "" {
				t.Fatalf("%v: exit=%d stdout=%q stderr=%q", args, exit, stdout, stderr)
			}
			if args[0] != "__update-check" && !strings.Contains(stderr, "does not update itself") {
				t.Fatalf("%v: stderr=%q", args, stderr)
			}
		}
		if requests() != 0 {
			t.Fatalf("dwvault made %d request(s) for releases", requests())
		}
		if fileSum(t, target) != before {
			t.Fatal("the binary behind dwvault was modified")
		}
	}
}

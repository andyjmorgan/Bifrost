package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/andyjmorgan/Bifrost/cli/internal/identity"
	"github.com/andyjmorgan/Bifrost/cli/internal/selfupdate"
)

// githubHost stands in for GitHub. It lists the given releases (tag → asset name → body)
// for repo and serves their assets, generating each release's SHA256SUMS unless the release
// supplies its own. It returns the API root and a counter of requests received.
func githubHost(t *testing.T, repo string, releases map[string]map[string]string) (api string, requests func() int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	files := map[string]string{}
	for tag, assets := range releases {
		var sums strings.Builder
		for name, body := range assets {
			files["/dl/"+tag+"/"+name] = body
			sum := sha256.Sum256([]byte(body))
			sums.WriteString(hex.EncodeToString(sum[:]) + "  " + name + "\n")
		}
		if _, ok := assets["SHA256SUMS"]; !ok {
			files["/dl/"+tag+"/SHA256SUMS"] = sums.String()
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		if r.URL.Path == "/repos/"+repo+"/releases" {
			type asset struct {
				Name string `json:"name"`
				URL  string `json:"browser_download_url"`
			}
			type release struct {
				Tag    string  `json:"tag_name"`
				Assets []asset `json:"assets"`
			}
			list := []release{}
			for tag := range releases {
				rel := release{Tag: tag}
				for path := range files {
					if name, ok := strings.CutPrefix(path, "/dl/"+tag+"/"); ok {
						rel.Assets = append(rel.Assets, asset{Name: name, URL: "http://" + r.Host + path})
					}
				}
				list = append(list, rel)
			}
			_ = json.NewEncoder(w).Encode(list)
			return
		}
		body, ok := files[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"Not Found"}`)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

// platformAsset is the release asset the running platform downloads under the given name.
func platformAsset(name string) string {
	return name + "-" + runtime.GOOS + "-" + runtime.GOARCH
}

// updateEnv isolates the in-process update path: an empty config home, the given stand-in
// for GitHub, the default repository, and the given version.
func updateEnv(t *testing.T, api, ver string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv(selfupdate.APIEnv, api)
	t.Setenv(selfupdate.RepoEnv, "")
	old := version
	version = ver
	t.Cleanup(func() { version = old })
	return home
}

// The state Bifrost's repository is in today: public, with no CLI release. Every form of
// the command says so plainly, succeeds, and changes nothing.
func TestUpdate_NoReleasePublished(t *testing.T) {
	for name, releases := range map[string]map[string]map[string]string{
		"no releases at all":    {},
		"only gateway releases": {"v1.0.0": {platformAsset("bifrost"): "gateway"}, "gateway-v2.0.0": {}},
	} {
		api, _ := githubHost(t, selfupdate.DefaultRepo, releases)
		for _, tc := range []struct {
			name         string
			ver          string
			force, check bool
		}{
			{"release build, update", "v0.3.0", false, false},
			{"release build, update --check", "v0.3.0", false, true},
			{"release build, update --force", "v0.3.0", true, false},
			{"source build, update --force", "dev", true, false},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				updateEnv(t, api, tc.ver)
				var out bytes.Buffer
				if err := runUpdate(&out, tc.force, tc.check); err != nil {
					t.Fatalf("runUpdate: %v", err)
				}
				msg := out.String()
				for _, want := range []string{
					"bifrost: no release has been published yet at github.com/andyjmorgan/Bifrost/releases",
					"releases are tagged cli-vX.Y.Z",
					"Nothing was changed",
				} {
					if !strings.Contains(msg, want) {
						t.Fatalf("message lacks %q: %q", want, msg)
					}
				}
				if strings.Contains(msg, "downloading") || strings.Contains(msg, "updated") {
					t.Fatalf("message suggests something was installed: %q", msg)
				}
			})
		}
	}
}

// A build from source is left alone, without asking GitHub, unless forced.
func TestUpdate_SourceBuildIsNotUpdated(t *testing.T) {
	api, requests := githubHost(t, selfupdate.DefaultRepo, map[string]map[string]string{"cli-v9.9.9": {platformAsset("bifrost"): "bin"}})
	for _, ver := range []string{"dev", "dev-abc1234", "v0.3.0-2-gabc1234"} {
		updateEnv(t, api, ver)
		var out bytes.Buffer
		if err := runUpdate(&out, false, false); err != nil || !strings.Contains(out.String(), "built from source") {
			t.Fatalf("%s: err=%v out=%q", ver, err, out.String())
		}
	}
	if requests() != 0 {
		t.Fatalf("%d request(s) made for a build from source", requests())
	}
}

func TestUpdate_Check(t *testing.T) {
	api, requests := githubHost(t, selfupdate.DefaultRepo, map[string]map[string]string{
		"cli-v0.4.0": {platformAsset("bifrost"): "bin"},
		"cli-v0.3.5": {platformAsset("bifrost"): "old"},
		"v7.0.0":     {platformAsset("bifrost"): "a gateway release, never the CLI's"},
	})
	for _, tc := range []struct {
		name, ver string
		check     bool
		want      string
	}{
		{"newer available", "v0.3.0", true, "bifrost v0.4.0 is available (you have v0.3.0)"},
		{"already latest", "v0.4.0", true, "bifrost v0.4.0 is the latest release (you have v0.4.0)"},
		{"ahead of latest", "v0.5.0", true, "bifrost v0.4.0 is the latest release (you have v0.5.0)"},
		{"up to date, nothing to install", "v0.4.0", false, "bifrost v0.4.0 is up to date"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := updateEnv(t, api, tc.ver)
			before := requests()
			var out bytes.Buffer
			if err := runUpdate(&out, false, tc.check); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("got %q, want %q", out.String(), tc.want)
			}
			if requests() != before+1 {
				t.Fatalf("%d request(s), want only the release listing", requests()-before)
			}
			// The answer is cached for the passive notice.
			b, err := os.ReadFile(filepath.Join(home, "bifrost", "update.json")) //nolint:gosec // G304: a file in the test's temp dir
			if err != nil || !strings.Contains(string(b), `"v0.4.0"`) {
				t.Fatalf("update.json: %q, %v", b, err)
			}
		})
	}
}

func TestUpdate_Errors(t *testing.T) {
	api, _ := githubHost(t, selfupdate.DefaultRepo, nil)
	t.Run("repository not found or private", func(t *testing.T) {
		updateEnv(t, api, "v0.3.0")
		t.Setenv(selfupdate.RepoEnv, "someone/private")
		var out bytes.Buffer
		err := runUpdate(&out, false, false)
		if err == nil || !strings.Contains(err.Error(), "someone/private was not found, or is not public") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("plain-http API root", func(t *testing.T) {
		updateEnv(t, "http://api.example", "v0.3.0")
		var out bytes.Buffer
		if err := runUpdate(&out, false, false); err == nil || !strings.Contains(err.Error(), "must use https") {
			t.Fatalf("got %v", err)
		}
	})
}

// As dwvault nothing is fetched, whatever is published, configured or forced.
func TestUpdate_AsDwvault(t *testing.T) {
	api, requests := githubHost(t, selfupdate.DefaultRepo, map[string]map[string]string{
		"cli-v9.9.9": {platformAsset("bifrost"): "bin", platformAsset("dwvault"): "bin"},
	})
	home := updateEnv(t, api, "v0.2.21")
	t.Cleanup(identity.SetForTesting(identity.Dwvault()))

	for _, tc := range []struct{ force, check bool }{{false, false}, {true, false}, {false, true}} {
		var out bytes.Buffer
		if err := runUpdate(&out, tc.force, tc.check); err != nil {
			t.Fatalf("runUpdate: %v", err)
		}
		if !strings.Contains(out.String(), "dwvault is the compatibility name for bifrost and does not update itself") {
			t.Fatalf("got %q", out.String())
		}
	}
	if _, _, err := run(t, "__update-check"); err != nil {
		t.Fatalf("__update-check: %v", err)
	}
	if requests() != 0 {
		t.Fatalf("dwvault made %d request(s)", requests())
	}
	if _, err := os.Stat(filepath.Join(home, "dwvault")); !os.IsNotExist(err) { //nolint:gosec // G703: home is the test's own temp dir
		t.Fatalf("dwvault wrote update state (stat err = %v)", err)
	}

	// The command is still there for scripts that call it, just not advertised.
	sub, _, err := newRoot().Find([]string{"update"})
	if err != nil || sub.Name() != "update" || !sub.Hidden {
		t.Fatalf("update under dwvault: cmd=%v hidden=%v err=%v", sub.Name(), sub.Hidden, err)
	}
}

// The background check caches "nothing published" too, so it is not repeated on every command.
func TestUpdateCheckHidden(t *testing.T) {
	t.Run("no release", func(t *testing.T) {
		api, requests := githubHost(t, selfupdate.DefaultRepo, nil)
		updateEnv(t, api, "v0.3.0")
		if _, stderr, err := run(t, "__update-check"); err != nil || stderr != "" {
			t.Fatalf("err=%v stderr=%q", err, stderr)
		}
		st, err := selfupdate.LoadState()
		if err != nil || st.CheckedAt.IsZero() || st.LatestVersion != "" || requests() != 1 {
			t.Fatalf("state=%+v err=%v requests=%d", st, err, requests())
		}
	})
	t.Run("release", func(t *testing.T) {
		api, _ := githubHost(t, selfupdate.DefaultRepo, map[string]map[string]string{"cli-v0.4.0": {}})
		updateEnv(t, api, "v0.3.0")
		if _, _, err := run(t, "__update-check"); err != nil {
			t.Fatal(err)
		}
		if st, err := selfupdate.LoadState(); err != nil || st.LatestVersion != "v0.4.0" {
			t.Fatalf("state=%+v err=%v", st, err)
		}
	})
	t.Run("failure is silent", func(t *testing.T) {
		api, _ := githubHost(t, "another/repo", nil) // the default repository 404s here
		updateEnv(t, api, "v0.3.0")
		if _, stderr, err := run(t, "__update-check"); err != nil || stderr != "" {
			t.Fatalf("err=%v stderr=%q", err, stderr)
		}
		if st, _ := selfupdate.LoadState(); !st.CheckedAt.IsZero() {
			t.Fatalf("a failed check must not be cached: %+v", st)
		}
	})
}

func TestUpdateCheckDisabled(t *testing.T) {
	t.Setenv("BIFROST_NO_UPDATE_CHECK", "")
	t.Setenv("VAULT_NO_UPDATE_CHECK", "")
	if updateCheckDisabled(identity.Bifrost()) {
		t.Fatal("disabled with neither variable set")
	}
	t.Setenv("VAULT_NO_UPDATE_CHECK", "1")
	if !updateCheckDisabled(identity.Bifrost()) || !updateCheckDisabled(identity.Dwvault()) {
		t.Fatal("VAULT_NO_UPDATE_CHECK should disable the check under both names")
	}
	t.Setenv("VAULT_NO_UPDATE_CHECK", "")
	t.Setenv("BIFROST_NO_UPDATE_CHECK", "1")
	if !updateCheckDisabled(identity.Bifrost()) {
		t.Fatal("BIFROST_NO_UPDATE_CHECK should disable the check")
	}
	if updateCheckDisabled(identity.Dwvault()) {
		t.Fatal("dwvault reads only VAULT_NO_UPDATE_CHECK")
	}
}

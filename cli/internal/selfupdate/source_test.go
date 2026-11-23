package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/andyjmorgan/Bifrost/cli/internal/identity"
)

func TestRepo(t *testing.T) {
	t.Setenv(RepoEnv, "")
	if r, err := Repo(); err != nil || r != "andyjmorgan/Bifrost" {
		t.Fatalf("default Repo = %q, %v", r, err)
	}
	t.Setenv(RepoEnv, " me/fork ")
	if r, err := Repo(); err != nil || r != "me/fork" {
		t.Fatalf("override Repo = %q, %v", r, err)
	}
	for _, bad := range []string{"me", "me/fork/extra", "me/fork?x=1", "../etc/passwd", "me/ fork", "https://github.com/me/fork", "-me/fork"} {
		t.Setenv(RepoEnv, bad)
		if r, err := Repo(); err == nil {
			t.Errorf("Repo accepted %q as %q", bad, r)
		}
	}
}

func TestAPIBase(t *testing.T) {
	t.Setenv(APIEnv, "")
	if b, err := apiBase(); err != nil || b != "https://api.github.com" {
		t.Fatalf("default apiBase = %q, %v", b, err)
	}
	for raw, want := range map[string]string{
		"https://ghe.example/api/v3/": "https://ghe.example/api/v3",
		"http://127.0.0.1:8080":       "http://127.0.0.1:8080",
		"http://localhost:8080/":      "http://localhost:8080",
		"http://[::1]:8080":           "http://[::1]:8080",
	} {
		t.Setenv(APIEnv, raw)
		if b, err := apiBase(); err != nil || b != want {
			t.Errorf("apiBase(%q) = %q, %v; want %q", raw, b, err, want)
		}
	}
	for _, bad := range []string{"http://api.example", "ftp://api.example", "api.example", "https://", "https://%zz"} {
		t.Setenv(APIEnv, bad)
		if b, err := apiBase(); err == nil {
			t.Errorf("apiBase accepted %q as %q", bad, b)
		}
	}
}

// listing serves body as the release listing of repo and records each request.
func listing(t *testing.T, repo string, status int, body string) *[]*http.Request {
	t.Helper()
	var reqs []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, r)
		if r.URL.Path != "/repos/"+repo+"/releases" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(APIEnv, srv.URL)
	t.Setenv(RepoEnv, repo)
	return &reqs
}

func releasesJSON(t *testing.T, rels ...Release) string {
	t.Helper()
	b, err := json.Marshal(rels)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The newest CLI release is the highest cli-v version, wherever it sits in the listing and
// whatever else the repository has released.
func TestLatest_PicksHighestCLITag(t *testing.T) {
	reqs := listing(t, "me/fork", http.StatusOK, releasesJSON(t,
		Release{TagName: "v9.0.0"},                       // a gateway release: not ours
		Release{TagName: "gateway-v3.1.0"},               // nor this
		Release{TagName: "cli-v0.3.9"},                   //
		Release{TagName: "cli-v0.10.0"},                  // highest: numeric, not lexical
		Release{TagName: "cli-v0.4.0"},                   //
		Release{TagName: "cli-v1.0.0", Prerelease: true}, // not published
		Release{TagName: "cli-v2.0.0", Draft: true},      // not published
		Release{TagName: "cli-v0.11"},                    // not a version
		Release{TagName: "cli-0.12.0"},                   // missing the v
		Release{TagName: "cli-v0.13.0-rc1"},              // not a strict version
	))
	rel, err := Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.TagName != "cli-v0.10.0" || rel.Version() != "v0.10.0" {
		t.Fatalf("Latest = %q (version %q), want cli-v0.10.0", rel.TagName, rel.Version())
	}
	if len(*reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(*reqs))
	}
	r := (*reqs)[0]
	if r.URL.Path != "/repos/me/fork/releases" || r.URL.Query().Get("per_page") != "100" {
		t.Fatalf("request = %s", r.URL)
	}
	if r.Header.Get("Accept") != "application/vnd.github+json" {
		t.Fatalf("accept = %q", r.Header.Get("Accept"))
	}
	if r.Header.Get("Authorization") != "" {
		t.Fatal("the release check must not send credentials")
	}
}

// A repository with no CLI release yet, the state Bifrost's starts in, is ErrNoRelease:
// an answer, not a failure.
func TestLatest_NoRelease(t *testing.T) {
	for name, body := range map[string]string{
		"no releases at all":    "[]",
		"only other tags":       `[{"tag_name":"v1.0.0"},{"tag_name":"gateway-v2.0.0"}]`,
		"only a draft":          `[{"tag_name":"cli-v0.3.0","draft":true}]`,
		"only a prerelease":     `[{"tag_name":"cli-v0.3.0","prerelease":true}]`,
		"only malformed cli-v*": `[{"tag_name":"cli-vnext"},{"tag_name":"cli-v1"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			listing(t, "andyjmorgan/Bifrost", http.StatusOK, body)
			rel, err := Latest(context.Background())
			if !errors.Is(err, ErrNoRelease) || rel != nil {
				t.Fatalf("Latest = %+v, %v; want ErrNoRelease", rel, err)
			}
		})
	}
}

func TestLatest_Errors(t *testing.T) {
	t.Run("repository missing or private", func(t *testing.T) {
		listing(t, "me/fork", http.StatusNotFound, `{"message":"Not Found"}`)
		_, err := Latest(context.Background())
		if err == nil || errors.Is(err, ErrNoRelease) || !strings.Contains(err.Error(), "me/fork was not found, or is not public") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("rate limited", func(t *testing.T) {
		listing(t, "me/fork", http.StatusForbidden, `{"message":"API rate limit exceeded"}`)
		_, err := Latest(context.Background())
		if err == nil || errors.Is(err, ErrNoRelease) || !strings.Contains(err.Error(), "403") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		listing(t, "me/fork", http.StatusOK, "not json")
		if _, err := Latest(context.Background()); err == nil || errors.Is(err, ErrNoRelease) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("network error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		addr := srv.URL
		srv.Close()
		t.Setenv(APIEnv, addr)
		t.Setenv(RepoEnv, "")
		if _, err := Latest(context.Background()); err == nil {
			t.Fatal("expected network error")
		}
	})
	t.Run("bad repo", func(t *testing.T) {
		t.Setenv(RepoEnv, "not a repo")
		if _, err := Latest(context.Background()); err == nil || !strings.Contains(err.Error(), RepoEnv) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("bad api base", func(t *testing.T) {
		t.Setenv(RepoEnv, "")
		t.Setenv(APIEnv, "http://api.example")
		if _, err := Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "must use https") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("nil context", func(t *testing.T) {
		t.Setenv(RepoEnv, "")
		t.Setenv(APIEnv, "")
		if _, err := Latest(nil); err == nil { //nolint:staticcheck // SA1012: a nil context is the point: it makes request construction fail
			t.Fatal("expected request-build error")
		}
	})
}

// assetServer serves files by path and returns a release whose assets point at them.
func assetServer(t *testing.T, files map[string]string, names ...string) (*Release, *[]string) {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		body, ok := files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	rel := &Release{TagName: "cli-v9.9.9"}
	for _, n := range names {
		rel.Assets = append(rel.Assets, Asset{Name: n, BrowserDownloadURL: srv.URL + "/" + n})
	}
	return rel, &paths
}

func sumLine(bin, name string) string {
	sum := sha256.Sum256([]byte(bin))
	return hex.EncodeToString(sum[:]) + "  " + name + "\n"
}

func TestDownload_Success(t *testing.T) {
	const bin = "the-binary-bytes"
	rel, _ := assetServer(t, map[string]string{
		AssetName():     bin,
		sha256SumsAsset: sumLine(bin, AssetName()),
	}, AssetName(), sha256SumsAsset)
	got, err := Download(context.Background(), rel)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(got) != bin {
		t.Fatalf("got %q", got)
	}
}

func TestDownload_ChecksumMismatch(t *testing.T) {
	rel, _ := assetServer(t, map[string]string{
		AssetName():     "real",
		sha256SumsAsset: sumLine("different", AssetName()),
	}, AssetName(), sha256SumsAsset)
	_, err := Download(context.Background(), rel)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want checksum mismatch, got %v", err)
	}
}

func TestDownload_MissingBinaryAsset(t *testing.T) {
	rel, _ := assetServer(t, nil, sha256SumsAsset) // no platform binary
	_, err := Download(context.Background(), rel)
	if err == nil || !strings.Contains(err.Error(), "no asset") {
		t.Fatalf("want no-asset error, got %v", err)
	}
}

func TestDownload_MissingManifest(t *testing.T) {
	rel, _ := assetServer(t, nil, AssetName()) // no SHA256SUMS
	_, err := Download(context.Background(), rel)
	if err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("want manifest error, got %v", err)
	}
}

func TestDownload_ManifestFetchError(t *testing.T) {
	rel, _ := assetServer(t, nil, AssetName(), sha256SumsAsset) // both assets 404
	_, err := Download(context.Background(), rel)
	if err == nil || !strings.Contains(err.Error(), "fetch SHA256SUMS") {
		t.Fatalf("want manifest fetch error, got %v", err)
	}
}

func TestDownload_AssetNotInManifest(t *testing.T) {
	rel, paths := assetServer(t, map[string]string{
		AssetName():     "bin",
		sha256SumsAsset: strings.Repeat("a", 64) + "  some-other-file\n",
	}, AssetName(), sha256SumsAsset)
	_, err := Download(context.Background(), rel)
	if err == nil || !strings.Contains(err.Error(), "not listed") {
		t.Fatalf("want not-listed error, got %v", err)
	}
	// A binary that cannot be verified is never fetched.
	if len(*paths) != 1 {
		t.Fatalf("requested %v, want only the manifest", *paths)
	}
}

func TestDownload_BinaryFetchError(t *testing.T) {
	rel, _ := assetServer(t, map[string]string{
		sha256SumsAsset: sumLine("bin", AssetName()),
	}, AssetName(), sha256SumsAsset)
	_, err := Download(context.Background(), rel)
	if err == nil || !strings.Contains(err.Error(), "fetch "+AssetName()) {
		t.Fatalf("want binary fetch error, got %v", err)
	}
}

// An asset URL that is not https is refused before anything is fetched, for the binary and
// for the manifest alike.
func TestDownload_RefusesPlainHTTPAssets(t *testing.T) {
	for _, plain := range []string{AssetName(), sha256SumsAsset} {
		rel := &Release{TagName: "cli-v9.9.9", Assets: []Asset{
			{Name: AssetName(), BrowserDownloadURL: "https://dl.example/" + AssetName()},
			{Name: sha256SumsAsset, BrowserDownloadURL: "https://dl.example/" + sha256SumsAsset},
		}}
		for i := range rel.Assets {
			if rel.Assets[i].Name == plain {
				rel.Assets[i].BrowserDownloadURL = "http://dl.example/" + plain
			}
		}
		_, err := Download(context.Background(), rel)
		if err == nil || !strings.Contains(err.Error(), "must use https") {
			t.Errorf("plain-http %s: got %v", plain, err)
		}
	}
}

func TestAssetURL(t *testing.T) {
	rel := &Release{TagName: "cli-v1.0.0", Assets: []Asset{{Name: "a", BrowserDownloadURL: "https://x/a"}}}
	if got, err := assetURL(rel, "a"); err != nil || got != "https://x/a" {
		t.Fatalf("assetURL = %q, %v", got, err)
	}
	if got, err := assetURL(rel, "missing"); err != nil || got != "" {
		t.Fatalf("assetURL missing = %q, %v; want empty", got, err)
	}
}

func TestCLIVersion(t *testing.T) {
	for tag, want := range map[string]string{
		"cli-v0.3.0": "v0.3.0", "cli-v10.20.30": "v10.20.30",
		"v0.3.0": "", "cli-0.3.0": "", "cli-v0.3": "", "cli-v0.3.0-rc1": "", "gateway-v0.3.0": "", "": "",
	} {
		got, ok := cliVersion(tag)
		if got != want || ok != (want != "") {
			t.Errorf("cliVersion(%q) = %q, %v; want %q", tag, got, ok, want)
		}
	}
}

func TestIsRelease(t *testing.T) {
	for v, want := range map[string]bool{
		"v0.3.0": true, "0.3.0": true, "dev": false, "": false,
		"v0.3.0-4-gabc1234": false, "v0.3": false, "v0.3.0-dirty": false, "dev-abc1234": false,
	} {
		if got := IsRelease(v); got != want {
			t.Errorf("IsRelease(%q) = %v, want %v", v, got, want)
		}
	}
}

// The asset is named after the identity, so the binary can only ever fetch its own name.
func TestAssetName_FollowsIdentity(t *testing.T) {
	defer identity.SetForTesting(identity.Dwvault())()
	if got := AssetName(); !strings.HasPrefix(got, "dwvault-") {
		t.Fatalf("AssetName() = %q under dwvault", got)
	}
}

func TestCheckRedirect(t *testing.T) {
	req := func(raw string) *http.Request {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Request{URL: u}
	}
	if err := checkRedirect(req("https://cdn.example/a"), []*http.Request{req("https://dl.example/a")}); err != nil {
		t.Errorf("https to https: %v", err)
	}
	if err := checkRedirect(req("http://cdn.example/a"), []*http.Request{req("https://dl.example/a")}); err == nil {
		t.Error("https to http should be refused")
	}
	if err := checkRedirect(req("http://127.0.0.1/b"), []*http.Request{req("http://127.0.0.1/a")}); err != nil {
		t.Errorf("loopback http to http: %v", err)
	}
	via := make([]*http.Request, 10)
	for i := range via {
		via[i] = req("https://dl.example/a")
	}
	if err := checkRedirect(req("https://dl.example/b"), via); err == nil {
		t.Error("an eleventh hop should be refused")
	}
}

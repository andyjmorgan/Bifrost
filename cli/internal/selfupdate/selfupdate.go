// Package selfupdate upgrades the running binary in place from the project's GitHub
// releases, and maintains a small cache of the latest-seen version for the CLI's passive
// update notice.
//
// The CLI shares its repository with the gateway, so its releases carry their own tag
// scheme, cli-vX.Y.Z, and the repository-wide "latest release" is never consulted: the
// newest CLI release is the highest cli-v version among the published releases. Each
// release holds one binary per platform named "bifrost-<goos>-<goarch>" plus a
// "SHA256SUMS" manifest (see .github/workflows/cli-release.yml). An upgrade fetches the
// asset for the current runtime, verifies its sha256 against the manifest, and atomically
// renames it over the live executable — it never touches PATH or shell profiles
// (first-install placement is install.sh's job).
package selfupdate

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/andyjmorgan/Bifrost/cli/internal/config"
	"github.com/andyjmorgan/Bifrost/cli/internal/identity"
)

const (
	// DefaultRepo is the GitHub "owner/name" whose releases carry the CLI. Kept here as the
	// single source of truth so the self-updater and install.sh stay in lockstep.
	DefaultRepo = "andyjmorgan/Bifrost"
	// RepoEnv overrides DefaultRepo, for parity with install.sh (so `bifrost update` follows
	// the same source the binary was installed from).
	RepoEnv = "BIFROST_REPO"
	// APIEnv overrides the GitHub REST API root, for a GitHub Enterprise host or a mirror.
	APIEnv = "BIFROST_GITHUB_API"
	// TagPrefix marks the CLI's release tags (cli-vX.Y.Z), keeping them apart from any tag
	// the gateway in the same repository may use.
	TagPrefix = "cli-"

	defaultAPIBase  = "https://api.github.com"
	sha256SumsAsset = "SHA256SUMS"
)

// ErrNoRelease is returned by Latest when the repository has no published CLI release.
// It is an expected state, not a failure: callers say so and do nothing.
var ErrNoRelease = errors.New("no release has been published")

// createTemp is os.CreateTemp, indirected only so tests can exercise the atomic-write
// failure paths in Apply and SaveState (e.g. by handing back an already-closed file so
// the subsequent Write fails). Production never reassigns it.
var createTemp = os.CreateTemp

// maxDownload caps a single asset read so a misconfigured or hostile BIFROST_REPO can't
// exhaust memory. The binary is ~10 MB; 256 MB is comfortable headroom. A truncated
// binary fails the sha256 check; a truncated manifest fails the "not listed" lookup.
const maxDownload = 256 << 20

// maxListing caps the release listing read, for the same reason.
const maxListing = 16 << 20

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9._-]+$`)

// Repo returns the release repository: BIFROST_REPO when set, otherwise DefaultRepo. The
// value becomes part of a URL, so anything but a plain owner/name is refused.
func Repo() (string, error) {
	r := strings.TrimSpace(os.Getenv(RepoEnv))
	if r == "" {
		return DefaultRepo, nil
	}
	if !repoPattern.MatchString(r) {
		return "", fmt.Errorf("%s=%q is not a GitHub owner/name", RepoEnv, r)
	}
	return r, nil
}

// apiBase returns the GitHub REST API root: BIFROST_GITHUB_API when set, otherwise GitHub's.
func apiBase() (string, error) {
	raw := strings.TrimRight(strings.TrimSpace(os.Getenv(APIEnv)), "/")
	if raw == "" {
		return defaultAPIBase, nil
	}
	if err := requireTLS(raw); err != nil {
		return "", fmt.Errorf("%s: %w", APIEnv, err)
	}
	return raw, nil
}

// requireTLS accepts an https URL. The binary and the checksums that vouch for it are
// fetched from the same release, so TLS is what ties them to the host; plain http is
// accepted only for a loopback host, for local testing.
func requireTLS(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%q is not a URL: %w", raw, err)
	}
	switch {
	case u.Scheme == "https" && u.Host != "":
	case u.Scheme == "http" && isLoopback(u.Hostname()):
	default:
		return fmt.Errorf("%q must use https", raw)
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Release is the subset of the GitHub release payload we need.
type Release struct {
	TagName    string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

// Asset is one file attached to a release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Version is the release's version as the binary reports it: the tag without TagPrefix,
// e.g. "v0.3.0" for the tag "cli-v0.3.0".
func (r *Release) Version() string {
	return strings.TrimPrefix(r.TagName, TagPrefix)
}

// cliVersion extracts the version from a CLI release tag. Only a strict cli-vX.Y.Z is one;
// every other tag in the repository belongs to something else.
func cliVersion(tag string) (string, bool) {
	v, ok := strings.CutPrefix(tag, TagPrefix)
	if !ok || !strings.HasPrefix(v, "v") || !IsRelease(v) {
		return "", false
	}
	return v, true
}

// AssetName is the release asset name for the current platform, e.g. bifrost-linux-amd64.
func AssetName() string {
	return fmt.Sprintf("%s-%s-%s", identity.Current().Name, runtime.GOOS, runtime.GOARCH)
}

// Latest returns the newest published (non-draft, non-prerelease) CLI release: the one with
// the highest cli-vX.Y.Z tag. It returns ErrNoRelease when there is none. The caller
// controls the timeout via ctx.
//
// Only the first page of the listing is read. GitHub lists newest first, so that is the
// hundred most recent releases, which holds the newest CLI release unless the gateway has
// published a hundred of its own since.
func Latest(ctx context.Context) (*Release, error) {
	repo, err := Repo()
	if err != nil {
		return nil, err
	}
	api, err := apiBase()
	if err != nil {
		return nil, err
	}
	u := fmt.Sprintf("%s/repos/%s/releases?per_page=100", api, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("github releases: repository %s was not found, or is not public", repo)
	default:
		return nil, fmt.Errorf("github releases: %s", resp.Status)
	}
	var all []Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxListing)).Decode(&all); err != nil {
		return nil, fmt.Errorf("github releases: %w", err)
	}
	var best *Release
	for i := range all {
		r := &all[i]
		if r.Draft || r.Prerelease {
			continue
		}
		v, ok := cliVersion(r.TagName)
		if !ok {
			continue
		}
		if best == nil || Compare(v, best.Version()) > 0 {
			best = r
		}
	}
	if best == nil {
		return nil, ErrNoRelease
	}
	return best, nil
}

// IsRelease reports whether v is a strict MAJOR.MINOR.PATCH version, with or without the
// leading "v". Anything else ("dev", a git describe string) is a build from source.
func IsRelease(v string) bool {
	_, ok := parse(v)
	return ok
}

// Compare orders two strict vMAJOR.MINOR.PATCH versions: -1 if a<b, 0 if equal, +1 if a>b.
// A version that doesn't parse (e.g. "dev") is treated as the lowest possible, so it never
// looks newer than a real release.
func Compare(a, b string) int {
	am, aok := parse(a)
	bm, bok := parse(b)
	switch {
	case !aok && !bok:
		return 0
	case !aok:
		return -1
	case !bok:
		return 1
	}
	for i := 0; i < 3; i++ {
		if am[i] != bm[i] {
			if am[i] < bm[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// parse splits a strict "vMAJOR.MINOR.PATCH" tag into its three numeric components.
func parse(v string) ([3]int, bool) {
	var out [3]int
	s := strings.TrimPrefix(v, "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Download fetches this platform's binary from the release and verifies its sha256 against
// the release's SHA256SUMS manifest. It returns the verified binary bytes.
func Download(ctx context.Context, rel *Release) ([]byte, error) {
	name := AssetName()
	binURL, err := assetURL(rel, name)
	if err != nil {
		return nil, err
	}
	if binURL == "" {
		return nil, fmt.Errorf("release %s has no asset %q", rel.TagName, name)
	}
	sumsURL, err := assetURL(rel, sha256SumsAsset)
	if err != nil {
		return nil, err
	}
	if sumsURL == "" {
		return nil, fmt.Errorf("release %s has no %s manifest", rel.TagName, sha256SumsAsset)
	}

	sums, err := fetch(ctx, sumsURL)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", sha256SumsAsset, err)
	}
	want, err := sumFor(sums, name)
	if err != nil {
		return nil, err
	}

	bin, err := fetch(ctx, binURL)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", name, err)
	}
	got := sha256.Sum256(bin)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("checksum mismatch for %s (release may be corrupt)", name)
	}
	return bin, nil
}

// assetURL returns the download URL of the named asset, or "" if absent. A URL that is
// not https is refused rather than followed.
func assetURL(rel *Release, name string) (string, error) {
	for _, a := range rel.Assets {
		if a.Name == name {
			if err := requireTLS(a.BrowserDownloadURL); err != nil {
				return "", fmt.Errorf("release %s asset %s: %w", rel.TagName, name, err)
			}
			return a.BrowserDownloadURL, nil
		}
	}
	return "", nil
}

// sumFor extracts the hex sha256 for name from a "sha256  filename" manifest (the format
// produced by `sha256sum`).
func sumFor(manifest []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(manifest))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		// `sha256sum` prefixes the binary-mode filename with '*'.
		if strings.TrimPrefix(fields[1], "*") == name {
			sum := strings.ToLower(fields[0])
			if len(sum) != sha256.Size*2 {
				return "", fmt.Errorf("malformed checksum for %s in %s", name, sha256SumsAsset)
			}
			return sum, nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s not listed in %s", name, sha256SumsAsset)
}

// client follows GitHub's redirects (assets are served from a second host) but never lets
// one leave TLS.
var client = &http.Client{CheckRedirect: checkRedirect}

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return fmt.Errorf("refusing a redirect from https to %s", req.URL.Scheme)
	}
	return nil
}

// fetch GETs a URL and returns the full body.
func fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxDownload))
}

// executable resolves the running binary's real path. It's a var (not a direct
// os.Executable call) only so tests can point Apply at a temp file instead of the live
// test binary; production always uses os.Executable.
var executable = os.Executable

// Apply atomically replaces the running executable with bin. It resolves the executable
// through symlinks, writes a temp file alongside the real target (same dir ⇒ rename is
// atomic, not a cross-device copy), makes it executable, and renames it into place.
// Returns the resolved path that was replaced.
func Apply(bin []byte) (string, error) {
	exe, err := executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
	f, err := createTemp(dir, "."+identity.Current().Name+"-update-*")
	if err != nil {
		return "", fmt.Errorf("cannot stage update in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // no-op once the rename succeeds
	if _, err := f.Write(bin); err != nil {
		_ = f.Close()
		return "", err
	}
	// Flush to disk before the rename so a crash can't leave a truncated binary in place.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", err //coverage:ignore Sync syscall failure not reproducible on a normal temp file
	}
	if err := f.Close(); err != nil {
		return "", err //coverage:ignore Close error after a successful write/sync not reproducible
	}
	if err := os.Chmod(tmp, 0o755); err != nil { //nolint:gosec // G302: staged binary must be executable (0o755); the file is program-controlled
		return "", err //coverage:ignore Chmod syscall failure not reproducible on a normal temp file
	}
	if err := os.Rename(tmp, exe); err != nil {
		return "", fmt.Errorf("cannot replace %s: %w", exe, err)
	}
	return exe, nil
}

// State is the cached result of the last passive update check.
type State struct {
	CheckedAt     time.Time `json:"checked_at"`
	LatestVersion string    `json:"latest_version"`
}

const stateFile = "update.json"

func statePath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, stateFile), nil
}

// LoadState reads the update-check cache, returning a zero State if it's absent.
func LoadState() (State, error) {
	var s State
	p, err := statePath()
	if err != nil {
		return s, err
	}
	b, err := os.ReadFile(p) //nolint:gosec // G304: path is the program-controlled update-check cache, not attacker-supplied
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		// The cache is disposable: treat a corrupt file as empty so the next check
		// simply rewrites it, rather than erroring on the CLI's hot path.
		return State{}, nil
	}
	return s, nil
}

// SaveState writes the update-check cache atomically (0600 file, 0700 dir).
func SaveState(s State) error {
	p, err := statePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err //coverage:ignore State always marshals
	}
	f, err := createTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err //coverage:ignore Close error after a successful write not reproducible
	}
	return os.Rename(tmp, p)
}

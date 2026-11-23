package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/andyjmorgan/Bifrost/cli/internal/identity"
)

func TestDefaultAddr(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   identity.Identity
		env  map[string]string
		want string
	}{
		{"bifrost default", identity.Bifrost(), nil, "https://bifrost.donkeywork.dev/keyring"},
		{"bifrost BIFROST_ADDR", identity.Bifrost(), map[string]string{"BIFROST_ADDR": "https://b.example/keyring"}, "https://b.example/keyring"},
		{"bifrost VAULT_ADDR fallback", identity.Bifrost(), map[string]string{"VAULT_ADDR": "https://vault.donkeywork.dev"}, "https://vault.donkeywork.dev"},
		{"bifrost BIFROST_ADDR beats VAULT_ADDR", identity.Bifrost(),
			map[string]string{"BIFROST_ADDR": "https://b.example/keyring", "VAULT_ADDR": "https://v.example"}, "https://b.example/keyring"},
		{"bifrost blank BIFROST_ADDR falls through", identity.Bifrost(),
			map[string]string{"BIFROST_ADDR": "  ", "VAULT_ADDR": "https://v.example"}, "https://v.example"},
		{"dwvault default", identity.Dwvault(), nil, "https://vault.donkeywork.dev"},
		{"dwvault VAULT_ADDR", identity.Dwvault(), map[string]string{"VAULT_ADDR": "https://v.example"}, "https://v.example"},
		{"dwvault ignores BIFROST_ADDR", identity.Dwvault(), map[string]string{"BIFROST_ADDR": "https://b.example/keyring"}, "https://vault.donkeywork.dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := defaultAddr(tc.id, func(k string) string { return tc.env[k] })
			if got != tc.want {
				t.Fatalf("defaultAddr = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://bifrost.donkeywork.dev/keyring":   "https://bifrost.donkeywork.dev/keyring",
		"https://bifrost.donkeywork.dev/keyring/":  "https://bifrost.donkeywork.dev/keyring",
		"https://bifrost.donkeywork.dev/keyring//": "https://bifrost.donkeywork.dev/keyring",
		"https://vault.donkeywork.dev":             "https://vault.donkeywork.dev",
		"https://vault.donkeywork.dev/":            "https://vault.donkeywork.dev",
		"http://localhost:8080":                    "http://localhost:8080",
		"http://localhost:8080/keyring":            "http://localhost:8080/keyring",
		"localhost:8080":                           "http://localhost:8080",
		"localhost:8080/keyring/":                  "http://localhost:8080/keyring",
		"  https://vault.example  ":                "https://vault.example",
		"https://h.example/a/b":                    "https://h.example/a/b",
	} {
		if got := baseURL(in); got != want {
			t.Errorf("baseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// --addr wins over both variables; without it the variables and then the default apply.
func TestAddrFlagAndEnvPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name          string
		bifrost, vaul string
		args          []string
		want          string
	}{
		{name: "default", want: "https://bifrost.donkeywork.dev/keyring"},
		{name: "VAULT_ADDR", vaul: "https://v.example/", want: "https://v.example"},
		{name: "BIFROST_ADDR", bifrost: "https://b.example/keyring/", vaul: "https://v.example", want: "https://b.example/keyring"},
		{name: "flag", bifrost: "https://b.example/keyring", vaul: "https://v.example",
			args: []string{"--addr", "https://flag.example/prefix/"}, want: "https://flag.example/prefix"},
		{name: "flag bare host", args: []string{"--addr", "localhost:9"}, want: "http://localhost:9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetClientGlobals(t)
			t.Setenv("BIFROST_ADDR", tc.bifrost)
			t.Setenv("VAULT_ADDR", tc.vaul)
			root := newRoot()
			if err := root.ParseFlags(tc.args); err != nil {
				t.Fatal(err)
			}
			if got := httpBaseURL(); got != tc.want {
				t.Fatalf("httpBaseURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

// captureStd runs fn with os.Stdout and os.Stderr redirected, and returns what each got.
func captureStd(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = outW, errW
	var wg sync.WaitGroup
	var outB, errB []byte
	wg.Add(2)
	go func() { defer wg.Done(); outB, _ = io.ReadAll(outR) }()
	go func() { defer wg.Done(); errB, _ = io.ReadAll(errR) }()
	func() {
		defer func() {
			os.Stdout, os.Stderr = oldOut, oldErr
			_ = outW.Close()
			_ = errW.Close()
		}()
		fn()
	}()
	wg.Wait()
	return string(outB), string(errB)
}

// keyringServer is a stand-in for the keyring API mounted at prefix ("" for the root, as
// the Vault host serves it; "/keyring" as Bifrost does). It records every request path.
func keyringServer(t *testing.T, prefix string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	mux := http.NewServeMux()
	reply := func(v any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Api-Key") != "dwv_test" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
	}
	mux.HandleFunc(prefix+"/api/v1/me", reply(map[string]any{"userId": "u1", "email": "owner@example.com"}))
	mux.HandleFunc(prefix+"/api/v1/api-keys", reply([]map[string]any{{
		"id": "7b0c1c7e-4b55-4c0e-9d0a-1f0e3c6f8a11", "name": "acme-api", "kind": "header_api_key",
		"createdAt": "2026-01-02T03:04:05Z", "baseUrl": "https://api.acme.example", "description": "Acme API",
	}}))
	mux.HandleFunc(prefix+"/api/v1/api-keys/acme-api/reveal", reply(map[string]any{
		"secret": "s3cr3t-value", "header": "Authorization", "headerValue": "Bearer s3cr3t-value",
		"kind": "header_api_key", "prefix": "Bearer ", "scheme": "header",
	}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

// run executes the CLI in-process with args and returns its streams and error.
func run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	stdout, stderr = captureStd(t, func() {
		root := newRoot()
		root.SetArgs(args)
		err = root.Execute()
	})
	return stdout, stderr, err
}

// A base with a path prefix and one without both work, for the generated client
// (credentials list) and for the hand-written one (auth status), and a trailing slash on
// the base changes nothing.
func TestRequestsHonourThePathPrefix(t *testing.T) {
	for _, tc := range []struct{ name, prefix, suffix string }{
		{"root, as the Vault host serves it", "", ""},
		{"root with trailing slash", "", "/"},
		{"prefix, as Bifrost serves it", "/keyring", ""},
		{"prefix with trailing slash", "/keyring", "/"},
		{"nested prefix", "/a/keyring", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetClientGlobals(t)
			srv, paths := keyringServer(t, tc.prefix)
			t.Setenv("VAULT_API_KEY", "dwv_test")
			base := srv.URL + tc.prefix + tc.suffix

			stdout, _, err := run(t, "--addr", base, "credentials", "list")
			if err != nil {
				t.Fatalf("credentials list: %v", err)
			}
			if !strings.Contains(stdout, "acme-api") {
				t.Fatalf("list output = %q", stdout)
			}
			if _, stderr, err := run(t, "--addr", base, "auth", "status"); err != nil || !strings.Contains(stderr, "owner@example.com") {
				t.Fatalf("auth status: err=%v stderr=%q", err, stderr)
			}

			want := []string{tc.prefix + "/api/v1/api-keys", tc.prefix + "/api/v1/me"}
			got := paths()
			if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
				t.Fatalf("request paths = %v, want %v", got, want)
			}
		})
	}
}

// The same holds when the base comes from the environment instead of the flag.
func TestRequestsHonourThePathPrefix_FromEnv(t *testing.T) {
	resetClientGlobals(t)
	srv, paths := keyringServer(t, "/keyring")
	t.Setenv("VAULT_API_KEY", "dwv_test")
	t.Setenv("BIFROST_ADDR", srv.URL+"/keyring")
	t.Setenv("VAULT_ADDR", "http://127.0.0.1:1") // would fail if it were used
	if _, _, err := run(t, "credentials", "list"); err != nil {
		t.Fatalf("credentials list: %v", err)
	}
	if got := paths(); len(got) != 1 || got[0] != "/keyring/api/v1/api-keys" {
		t.Fatalf("request paths = %v", got)
	}
}

// Secrets go to stdout and nowhere else: `get` and `header` print exactly the value, and
// stderr never carries it, on success or on failure.
func TestSecretsReachStdoutOnly(t *testing.T) {
	resetClientGlobals(t)
	srv, _ := keyringServer(t, "/keyring")
	t.Setenv("VAULT_API_KEY", "dwv_test")
	base := srv.URL + "/keyring"

	stdout, stderr, err := run(t, "--addr", base, "credentials", "get", "acme-api")
	if err != nil || stdout != "s3cr3t-value\n" {
		t.Fatalf("get: stdout=%q err=%v", stdout, err)
	}
	if stderr != "" {
		t.Fatalf("get wrote to stderr: %q", stderr)
	}

	stdout, stderr, err = run(t, "--addr", base, "credentials", "header", "acme-api")
	if err != nil || stdout != "Authorization: Bearer s3cr3t-value\n" {
		t.Fatalf("header: stdout=%q err=%v", stdout, err)
	}
	if stderr != "" {
		t.Fatalf("header wrote to stderr: %q", stderr)
	}

	// The key that authenticates the CLI is a secret too, and never appears in an error.
	t.Setenv("VAULT_API_KEY", "dwv_wrong_key")
	stdout, stderr, err = run(t, "--addr", base, "credentials", "get", "acme-api")
	if err == nil {
		t.Fatal("expected a rejected key to fail")
	}
	if stdout != "" {
		t.Fatalf("a failed get wrote to stdout: %q", stdout)
	}
	for _, s := range []string{stderr, err.Error()} {
		if strings.Contains(s, "dwv_wrong_key") || strings.Contains(s, "s3cr3t-value") {
			t.Fatalf("a secret leaked into an error: %q", s)
		}
	}
}

// Pointed at an address where a web app answers instead of the API, which is what the
// default address is until the keyring is served there, the error says so and names the
// address rather than reporting "200 OK".
func TestWebPageInsteadOfAPI(t *testing.T) {
	resetClientGlobals(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<!doctype html><html><body>app</body></html>")
	}))
	t.Cleanup(srv.Close)
	t.Setenv("VAULT_API_KEY", "dwv_test")
	base := srv.URL + "/keyring"
	for _, args := range [][]string{{"credentials", "list"}, {"credentials", "get", "x"}, {"auth", "status"}} {
		stdout, _, err := run(t, append([]string{"--addr", base}, args...)...)
		if err == nil || !strings.Contains(err.Error(), "answered with a web page, not the API") || !strings.Contains(err.Error(), base) {
			t.Fatalf("%v: %v", args, err)
		}
		if stdout != "" || strings.Contains(err.Error(), "dwv_test") || strings.Contains(err.Error(), "doctype") {
			t.Fatalf("%v: stdout=%q err=%v", args, stdout, err)
		}
	}
}

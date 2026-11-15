// Package servers launches the reference MCP servers described in SPEC.md
// (one per official SDK: Go, Python and TypeScript) as subprocesses for tests.
//
// A test calls [Start] with a kind ("go", "python" or "typescript") and gets
// back a running server and its startup details; the process is stopped when
// the test ends. [Available] reports whether a kind can run here, so tests can
// skip loudly when a runtime or its dependencies are missing. Install the
// Python and TypeScript dependencies with pkg/hooks/harness/servers/setup.sh.
package servers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Kinds lists the server kinds, in a stable order.
var Kinds = []string{"go", "python", "typescript"}

const (
	modulePath = "github.com/andyjmorgan/Bifrost"
	// Dir is the directory of this package, relative to the repository root.
	Dir = "pkg/hooks/harness/servers"

	setupHint = Dir + "/setup.sh"

	startupTimeout = 60 * time.Second
	stopStepWait   = 3 * time.Second
)

// Startup is the first stdout line of a reference server.
type Startup struct {
	URL              string   `json:"url"`
	Language         string   `json:"language"`
	SDK              string   `json:"sdk"`
	SDKVersion       string   `json:"sdkVersion"`
	ProtocolVersions []string `json:"protocolVersions"`
	Unsupported      []string `json:"unsupported"`
}

// Server is a running reference server.
type Server struct {
	Startup

	// Kind is the kind passed to Start.
	Kind string

	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stderr  *lockedBuffer
	extra   *lockedBuffer // stdout after the startup line; should stay empty
	exited  chan struct{}
	waitErr error

	stopOnce sync.Once
	stopped  string
}

// Stderr returns everything the server has written to stderr so far.
func (s *Server) Stderr() string { return s.stderr.String() }

// ExtraStdout returns anything written to stdout after the startup line. The
// contract says this stays empty.
func (s *Server) ExtraStdout() string { return s.extra.String() }

// IsUnsupported reports whether the server lists name in its unsupported list.
func (s *Server) IsUnsupported(name string) bool {
	for _, u := range s.Unsupported {
		if u == name {
			return true
		}
	}
	return false
}

// ExitError returns the process's exit error once it has exited (see Exited).
func (s *Server) ExitError() error {
	select {
	case <-s.exited:
		return s.waitErr
	default:
		return nil
	}
}

// Exited is closed when the process has exited.
func (s *Server) Exited() <-chan struct{} { return s.exited }

// CloseStdin closes the server's stdin, which the contract says makes it exit.
func (s *Server) CloseStdin() error { return s.stdin.Close() }

// Signal sends sig to the server process.
func (s *Server) Signal(sig os.Signal) error { return s.cmd.Process.Signal(sig) }

// Stop stops the server: it closes stdin, then sends SIGTERM, then kills the
// process, waiting a few seconds after each step. It returns which step
// stopped the process ("stdin", "sigterm", "kill" or "already exited"). It is
// safe to call more than once; Start registers it as a test cleanup.
func (s *Server) Stop() string {
	s.stopOnce.Do(func() {
		select {
		case <-s.exited:
			s.stopped = "already exited"
			return
		default:
		}
		s.stdin.Close()
		if s.waitFor(stopStepWait) {
			s.stopped = "stdin"
			return
		}
		s.cmd.Process.Signal(syscall.SIGTERM)
		if s.waitFor(stopStepWait) {
			s.stopped = "sigterm"
			return
		}
		s.cmd.Process.Kill()
		<-s.exited
		s.stopped = "kill"
	})
	return s.stopped
}

func (s *Server) waitFor(d time.Duration) bool {
	select {
	case <-s.exited:
		return true
	case <-time.After(d):
		return false
	}
}

// Start launches the reference server of the given kind from the repository
// root, waits for its startup line and for its URL to answer, and stops it
// when the test ends. If the kind cannot run here (see [Available]) the test
// is skipped with a loud message.
func Start(t testing.TB, kind string) *Server {
	t.Helper()
	if err := Available(kind); err != nil {
		SkipLoudly(t, kind, err)
	}
	root, err := RepoRoot()
	if err != nil {
		t.Fatalf("servers: %v", err)
	}
	name, args, err := command(root, kind)
	if err != nil {
		t.Fatalf("servers: %v", err)
	}

	cmd := exec.Command(name, args...)
	cmd.Dir = root
	s := &Server{
		Kind:   kind,
		cmd:    cmd,
		stderr: &lockedBuffer{},
		extra:  &lockedBuffer{},
		exited: make(chan struct{}),
	}
	cmd.Stderr = s.stderr
	if s.stdin, err = cmd.StdinPipe(); err != nil {
		t.Fatalf("servers: stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("servers: stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("servers: start %s server: %v", kind, err)
	}
	go func() {
		s.waitErr = cmd.Wait()
		close(s.exited)
	}()
	t.Cleanup(func() {
		how := s.Stop()
		if t.Failed() {
			t.Logf("%s server stopped by %s; stderr:\n%s", kind, how, s.Stderr())
		}
	})

	line, err := readLine(stdout, s.exited, startupTimeout)
	if err != nil {
		t.Fatalf("servers: %s server startup line: %v\nstderr:\n%s", kind, err, s.Stderr())
	}
	go io.Copy(s.extra, stdout)
	if err := json.Unmarshal(line, &s.Startup); err != nil {
		t.Fatalf("servers: %s server startup line is not JSON: %v\nline: %s", kind, err, line)
	}
	if s.Language != kind {
		t.Fatalf("servers: started %s server, startup line says language %q", kind, s.Language)
	}
	if s.URL == "" {
		t.Fatalf("servers: %s server startup line has no url: %s", kind, line)
	}
	if err := waitAnswers(s.URL, s.exited, startupTimeout); err != nil {
		t.Fatalf("servers: %s server at %s: %v\nstderr:\n%s", kind, s.URL, err, s.Stderr())
	}
	return s
}

// RequireEnv names an environment variable listing server kinds that must be
// available ("all", or a comma-separated list such as "go,python"). A listed
// kind that is unavailable fails the test instead of skipping it, for CI.
const RequireEnv = "REFSERVERS_REQUIRE"

// SkipLoudly skips t because kind is unavailable, writing the reason to
// stderr as well (go test shows it with -v, or whenever the package fails).
// If RequireEnv lists kind, it fails the test instead.
func SkipLoudly(t testing.TB, kind string, err error) {
	t.Helper()
	if required(kind) {
		t.Fatalf("%s reference server is required by %s but unavailable: %v", kind, RequireEnv, err)
	}
	msg := fmt.Sprintf("SKIPPING %s reference server: %v", kind, err)
	fmt.Fprintf(os.Stderr, "\n*** %s ***\n\n", msg)
	t.Skip(msg)
}

func required(kind string) bool {
	for _, k := range strings.Split(os.Getenv(RequireEnv), ",") {
		if k = strings.TrimSpace(k); k == "all" || k == kind {
			return true
		}
	}
	return false
}

// Available reports whether the given kind can run here. The error names
// setup.sh when a runtime, SDK install or server file is missing.
func Available(kind string) error {
	root, err := RepoRoot()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, Dir, kind)
	switch kind {
	case "go":
		if _, err := exec.LookPath("go"); err != nil {
			return fmt.Errorf("go toolchain not found on PATH")
		}
		return needFile(filepath.Join(dir, "main.go"))
	case "python":
		if err := needFile(filepath.Join(dir, "server.py")); err != nil {
			return err
		}
		py := filepath.Join(dir, ".venv", "bin", "python")
		if err := needFile(py); err != nil {
			return err
		}
		return pythonDepsInstalled(dir)
	case "typescript":
		if _, err := exec.LookPath("node"); err != nil {
			return fmt.Errorf("node not found on PATH; install Node.js, then run %s", setupHint)
		}
		if err := needFile(filepath.Join(dir, "server.mjs")); err != nil {
			return err
		}
		return nodeDepsInstalled(dir)
	default:
		return fmt.Errorf("unknown server kind %q (want one of %v)", kind, Kinds)
	}
}

func needFile(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("%s is missing (run %s once the server exists)", path, setupHint)
	}
	return nil
}

// pythonDepsInstalled checks that every package pinned in requirements.txt has
// a .dist-info directory in the virtualenv.
func pythonDepsInstalled(dir string) error {
	reqs, err := os.ReadFile(filepath.Join(dir, "requirements.txt"))
	if err != nil {
		return fmt.Errorf("%s/requirements.txt is missing (%v); run %s once the Python server exists", dir, err, setupHint)
	}
	infos, _ := filepath.Glob(filepath.Join(dir, ".venv", "lib", "python*", "site-packages", "*.dist-info"))
	have := map[string]bool{}
	for _, info := range infos {
		base := strings.TrimSuffix(filepath.Base(info), ".dist-info")
		if i := strings.Index(base, "-"); i >= 0 {
			base = base[:i]
		}
		have[normalizePy(base)] = true
	}
	for _, line := range strings.Split(string(reqs), "\n") {
		name := requirementName(line)
		if name == "" {
			continue
		}
		if !have[normalizePy(name)] {
			return fmt.Errorf("python package %q is not installed in %s/.venv; run %s", name, dir, setupHint)
		}
	}
	return nil
}

var reqNameRE = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)`)

func requirementName(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
		return ""
	}
	m := reqNameRE.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return m[1]
}

var pyNormRE = regexp.MustCompile(`[-_.]+`)

func normalizePy(name string) string { return strings.ToLower(pyNormRE.ReplaceAllString(name, "_")) }

// nodeDepsInstalled checks that every dependency in package.json is present
// in node_modules.
func nodeDepsInstalled(dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return fmt.Errorf("%s/package.json is missing (%v); run %s once the TypeScript server exists", dir, err, setupHint)
	}
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return fmt.Errorf("%s/package.json: %v", dir, err)
	}
	for name := range pkg.Dependencies {
		if _, err := os.Stat(filepath.Join(dir, "node_modules", name, "package.json")); err != nil {
			return fmt.Errorf("node package %q is not installed in %s/node_modules; run %s", name, dir, setupHint)
		}
	}
	return nil
}

// command returns the launch command for kind, relative to the repository
// root. The Go server is built once per test binary instead of `go run`.
func command(root, kind string) (string, []string, error) {
	switch kind {
	case "go":
		bin, err := goBinary(root)
		if err != nil {
			return "", nil, err
		}
		return bin, []string{"--port", "0"}, nil
	case "python":
		return filepath.Join(root, Dir, "python", ".venv", "bin", "python"),
			[]string{Dir + "/python/server.py", "--port", "0"}, nil
	case "typescript":
		return "node", []string{Dir + "/typescript/server.mjs", "--port", "0"}, nil
	}
	return "", nil, fmt.Errorf("unknown server kind %q", kind)
}

var (
	goBuildOnce sync.Once
	goBuildPath string
	goBuildErr  error
)

// goBinary builds the Go server once per test binary. It builds into the
// gitignored go/.bin directory, writing a temporary file and renaming it into
// place so concurrent test binaries do not clobber each other.
func goBinary(root string) (string, error) {
	goBuildOnce.Do(func() {
		binDir := filepath.Join(root, Dir, "go", ".bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			goBuildErr = err
			return
		}
		final := filepath.Join(binDir, "refserver-go")
		tmp := final + "." + strconv.Itoa(os.Getpid()) + ".tmp"
		cmd := exec.Command("go", "build", "-o", tmp, "./"+Dir+"/go")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			os.Remove(tmp)
			goBuildErr = fmt.Errorf("building the Go server: %v\n%s", err, out)
			return
		}
		if err := os.Rename(tmp, final); err != nil {
			os.Remove(tmp)
			goBuildErr = err
			return
		}
		goBuildPath = final
	})
	return goBuildPath, goBuildErr
}

var (
	rootOnce sync.Once
	rootPath string
	rootErr  error
)

// RepoRoot finds the repository root by walking up from the working directory
// to the go.mod of module github.com/andyjmorgan/Bifrost.
func RepoRoot() (string, error) {
	rootOnce.Do(func() {
		dir, err := os.Getwd()
		if err != nil {
			rootErr = err
			return
		}
		for {
			data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
			if err == nil && modFileModule(data) == modulePath {
				rootPath = dir
				return
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				rootErr = fmt.Errorf("no go.mod for module %s above the working directory", modulePath)
				return
			}
			dir = parent
		}
	})
	return rootPath, rootErr
}

func modFileModule(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "module" {
			return strings.Trim(f[1], `"`)
		}
	}
	return ""
}

// readLine reads the first line of r, failing if the process exits first or
// the timeout passes.
func readLine(r io.Reader, exited <-chan struct{}, timeout time.Duration) ([]byte, error) {
	type result struct {
		line []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := bufio.NewReader(r).ReadBytes('\n')
		ch <- result{bytes.TrimSpace(line), err}
	}()
	select {
	case res := <-ch:
		if res.err != nil && len(res.line) == 0 {
			return nil, fmt.Errorf("reading stdout: %v", res.err)
		}
		return res.line, nil
	case <-exited:
		// The reader may still deliver a line written just before exit.
		select {
		case res := <-ch:
			if len(res.line) > 0 {
				return res.line, nil
			}
		case <-time.After(200 * time.Millisecond):
		}
		return nil, errors.New("process exited before writing its startup line")
	case <-time.After(timeout):
		return nil, fmt.Errorf("no startup line after %v", timeout)
	}
}

// waitAnswers polls url until the HTTP server answers with any status.
func waitAnswers(url string, exited <-chan struct{}, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return errors.New("process exited before its URL answered")
		default:
		}
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("URL did not answer within %v: %v", timeout, lastErr)
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

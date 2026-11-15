package clients

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// ConnectOptions is how a client connects.
type ConnectOptions struct {
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
	// Capabilities the client declares. An "elicitation" key registers the
	// SDK's elicitation handler, which answers from CallOptions.Elicit.
	Capabilities map[string]any `json:"capabilities,omitempty"`
	// Mode is the version negotiation: "" or "auto", "legacy", or a version
	// to pin. The Go SDK ignores it and always negotiates.
	Mode string `json:"mode,omitempty"`
	// Cache, when false, turns the SDK's response cache off where it can.
	Cache *bool `json:"cache,omitempty"`
	// ReadTimeoutSeconds bounds each request inside the SDK. Zero means 20.
	ReadTimeoutSeconds int `json:"readTimeoutSeconds,omitempty"`
}

// Answer is one scripted reply of the elicitation handler.
type Answer struct {
	Action  string         `json:"action"`
	Content map[string]any `json:"content,omitempty"`
}

// CallOptions is one request made through the SDK.
type CallOptions struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`
	// Progress asks the SDK for progress and collects what it delivers.
	Progress bool `json:"progress,omitempty"`
	// Elicit is what the elicitation handler answers, in order.
	Elicit []Answer `json:"elicit,omitempty"`
	// CancelAfterMs cancels the call through the SDK's own mechanism.
	CancelAfterMs int `json:"cancelAfterMs,omitempty"`
	// CacheMode is the SDK's per-call cache setting, where it has one:
	// "use", "refresh" or "bypass".
	CacheMode string `json:"cacheMode,omitempty"`
}

// SDK names the client library behind a driver and what it negotiated.
type SDK struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	Language        string `json:"language"`
	ProtocolVersion string `json:"protocolVersion,omitempty"`
}

// Error is a failure the SDK reported for a request.
type Error struct {
	// Kind is "rpc" for a JSON-RPC error from the server, "cancelled" when
	// the driver cancelled the call, "validation" when the SDK rejected the
	// server's reply, and "other" for transport and remaining failures.
	Kind    string `json:"kind"`
	Code    any    `json:"code,omitempty"`
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Data    any    `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s error %v: %s", e.Kind, e.Code, e.Message)
}

// RPCCode is the JSON-RPC code of an "rpc" error, or 0.
func (e *Error) RPCCode() int {
	if e == nil || e.Kind != "rpc" {
		return 0
	}
	switch c := e.Code.(type) {
	case float64:
		return int(c)
	case int:
		return c
	case int64:
		return int(c)
	}
	return 0
}

// Reply is what the SDK made of one request.
type Reply struct {
	Result       map[string]any   `json:"result,omitempty"`
	Error        *Error           `json:"error,omitempty"`
	Progress     []map[string]any `json:"progress,omitempty"`
	Elicitations []map[string]any `json:"elicitations,omitempty"`
	SDK          SDK              `json:"sdk"`
}

// Driver puts one client SDK behind the same three operations. Connect may
// be called again; it replaces the connection. A returned error means the
// driver itself failed; a failure the SDK reported is in Reply.Error.
type Driver interface {
	Name() string
	Connect(ctx context.Context, o ConnectOptions) (Reply, error)
	Call(ctx context.Context, o CallOptions) (Reply, error)
	Close() error
}

// ErrNotInstalled is returned by a constructor when the runtime or the SDK
// for that client is missing.
var ErrNotInstalled = errors.New("not installed, run clients/setup.sh")

// dir is this package's source folder, where the driver programs live.
func dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(file)
}

// NewPython is the official Python SDK (the `mcp` package).
func NewPython() (Driver, error) {
	py := filepath.Join(dir(), "python", ".venv", "bin", "python")
	if _, err := os.Stat(py); err != nil {
		return nil, fmt.Errorf("python client: no virtualenv at %s: %w", py, ErrNotInstalled)
	}
	if out, err := exec.Command(py, "-c", "import mcp").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("python client: the mcp package does not import (%s): %w", bytes.TrimSpace(out), ErrNotInstalled)
	}
	return &process{name: "python", cmd: []string{py, filepath.Join(dir(), "python", "driver.py")}, cwd: filepath.Join(dir(), "python")}, nil
}

// NewTypeScript is the official TypeScript SDK, @modelcontextprotocol/client.
func NewTypeScript() (Driver, error) { return newNode("typescript", "@modelcontextprotocol/client") }

// NewTypeScriptV1 is the earlier TypeScript SDK, @modelcontextprotocol/sdk
// 1.x, which predates 2026-07-28.
func NewTypeScriptV1() (Driver, error) {
	return newNode("typescript-v1", "@modelcontextprotocol/sdk", "--sdk=v1")
}

func newNode(name, pkg string, args ...string) (Driver, error) {
	node, err := exec.LookPath("node")
	if err != nil {
		return nil, fmt.Errorf("%s client: node is not on PATH: %w", name, ErrNotInstalled)
	}
	ts := filepath.Join(dir(), "typescript")
	if _, err := os.Stat(filepath.Join(ts, "node_modules", filepath.FromSlash(pkg), "package.json")); err != nil {
		return nil, fmt.Errorf("%s client: %s is not in %s/node_modules: %w", name, pkg, ts, ErrNotInstalled)
	}
	return &process{name: name, cmd: append([]string{node, filepath.Join(ts, "driver.mjs")}, args...), cwd: ts}, nil
}

// process drives a client SDK in a subprocess over the line protocol. The
// subprocess starts on first use and is replaced after a timeout, so one
// stuck call cannot poison the calls after it.
type process struct {
	name string
	cmd  []string
	cwd  string

	mu     sync.Mutex
	run    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan []byte
	stderr *tail
	next   int
}

func (p *process) Name() string { return p.name }

func (p *process) start() error {
	if p.run != nil {
		return nil
	}
	c := exec.Command(p.cmd[0], p.cmd[1:]...)
	c.Dir = p.cwd
	// A plain environment: no proxy settings between the client and
	// localhost, and nothing of the caller's that a client might send on.
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "NO_PROXY=127.0.0.1,localhost", "PYTHONUNBUFFERED=1", "NODE_NO_WARNINGS=1"}
	in, err := c.StdinPipe()
	if err != nil {
		return err
	}
	out, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	p.stderr = &tail{max: 16 << 10}
	c.Stderr = p.stderr
	if err := c.Start(); err != nil {
		return fmt.Errorf("%s driver: %w", p.name, err)
	}
	lines := make(chan []byte, 16)
	go func() {
		defer close(lines)
		r := bufio.NewReaderSize(out, 1<<20)
		for {
			line, err := r.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				lines <- bytes.Clone(line)
			}
			if err != nil {
				return
			}
		}
	}()
	p.run, p.stdin, p.lines = c, in, lines
	return nil
}

func (p *process) kill() {
	if p.run == nil {
		return
	}
	_ = p.stdin.Close()
	_ = p.run.Process.Kill()
	_, _ = p.run.Process.Wait()
	p.run, p.stdin, p.lines = nil, nil, nil
}

func (p *process) roundTrip(ctx context.Context, req map[string]any) (Reply, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.start(); err != nil {
		return Reply{}, err
	}
	p.next++
	id := p.next
	req["id"] = id
	line, err := json.Marshal(req)
	if err != nil {
		return Reply{}, err
	}
	if _, err := p.stdin.Write(append(line, '\n')); err != nil {
		tail := p.stderr.String()
		p.kill()
		return Reply{}, fmt.Errorf("%s driver: write: %v\nstderr: %s", p.name, err, tail)
	}
	for {
		select {
		case <-ctx.Done():
			tail := p.stderr.String()
			p.kill()
			return Reply{}, fmt.Errorf("%s driver: no reply to %v %v: %w\nstderr: %s", p.name, req["op"], req["method"], ctx.Err(), tail)
		case raw, ok := <-p.lines:
			if !ok {
				tail := p.stderr.String()
				p.kill()
				return Reply{}, fmt.Errorf("%s driver exited before replying to %v %v\nstderr: %s", p.name, req["op"], req["method"], tail)
			}
			var env struct {
				ID int `json:"id"`
				Reply
			}
			if err := json.Unmarshal(raw, &env); err != nil {
				return Reply{}, fmt.Errorf("%s driver wrote a line that is not a reply: %s", p.name, bytes.TrimSpace(raw))
			}
			if env.ID != id {
				continue // a late reply to an abandoned request
			}
			return env.Reply, nil
		}
	}
}

func toMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}

func (p *process) Connect(ctx context.Context, o ConnectOptions) (Reply, error) {
	req := toMap(o)
	req["op"] = "connect"
	return p.roundTrip(ctx, req)
}

func (p *process) Call(ctx context.Context, o CallOptions) (Reply, error) {
	req := toMap(o)
	req["op"] = "call"
	return p.roundTrip(ctx, req)
}

func (p *process) Close() error {
	p.mu.Lock()
	running := p.run != nil
	p.mu.Unlock()
	if !running {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := p.roundTrip(ctx, map[string]any{"op": "close"})
	p.mu.Lock()
	p.kill()
	p.mu.Unlock()
	return err
}

// Stderr is the end of what the subprocess wrote to stderr, for diagnosis.
func (p *process) Stderr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stderr == nil {
		return ""
	}
	return p.stderr.String()
}

// tail keeps the last max bytes written to it.
type tail struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(b), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

package servers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This file checks each available reference server against SPEC.md directly,
// with no gateway in between. It uses the official Go SDK client where the
// check can be expressed through it, and plain HTTP with JSON-RPC bodies where
// the test needs to see the wire (progress order, tool error versus protocol
// error, argument bytes, headers).

const (
	newProtocol  = "2026-07-28"
	instructions = "Reference server.\n\n## Notes\nUse search first."
	pixelPNG     = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	hiddenURI    = "ref://hidden/thing"
)

type toolSpec struct {
	name        string
	description string
	required    []string
	props       map[string]string // property -> JSON type
	readOnly    bool
	destructive bool
}

var contractTools = []toolSpec{
	{"echo", "Return the text.", []string{"text"}, map[string]string{"text": "string"}, true, false},
	{"add", "Add two numbers.", []string{"a", "b"}, map[string]string{"a": "number", "b": "number"}, true, false},
	{"search", "Search pages.", []string{"query", "telemetry"}, map[string]string{"query": "string", "limit": "integer", "space": "string", "telemetry": "object"}, true, false},
	{"delete_page", "Delete a page.", []string{"id"}, map[string]string{"id": "string"}, false, true},
	{"get_report", "Return the report.", nil, map[string]string{}, true, false},
	{"slow_count", "Count slowly, reporting progress.", []string{"steps"}, map[string]string{"steps": "integer", "delayMs": "integer"}, true, false},
	{"fail", "Always fails.", nil, map[string]string{}, false, false},
	{"crash", "Always crashes.", nil, map[string]string{}, false, false},
	{"picture", "Return a picture.", nil, map[string]string{}, true, false},
	{"ask", "Ask which project.", nil, map[string]string{}, false, false},
	{"inspect", "Describe the request.", nil, map[string]string{}, true, false},
}

type resourceSpec struct{ uri, name, mime, text string }

var contractResources = []resourceSpec{
	{"ref://docs/guide", "guide", "text/markdown", "# Guide\nSee ref://docs/faq and https://example.com/x."},
	{"ref://docs/faq", "faq", "text/plain", "FAQ"},
	{"ref://secrets/key", "key", "text/plain", "sk-test-123"},
}

func TestConformance(t *testing.T) {
	for _, kind := range Kinds {
		t.Run(kind, func(t *testing.T) {
			if err := Available(kind); err != nil {
				SkipLoudly(t, kind, err)
			}
			s := Start(t, kind)
			t.Logf("%s server: sdk %s %s, protocol versions %v, url %s", kind, s.SDK, s.SDKVersion, s.ProtocolVersions, s.URL)
			if len(s.Unsupported) > 0 {
				msg := fmt.Sprintf("%s server declares unsupported: %v (these checks are skipped)", kind, s.Unsupported)
				fmt.Fprintf(os.Stderr, "\n*** %s ***\n\n", msg)
				t.Log(msg)
			}
			c := &checker{s: s}
			t.Run("startup", c.startup)
			t.Run("identity", c.identity)
			t.Run("tools/list", c.toolsList)
			t.Run("echo", c.echo)
			t.Run("add", c.add)
			t.Run("search", c.search)
			t.Run("delete_page", c.deletePage)
			t.Run("get_report", c.getReport)
			t.Run("slow_count", c.slowCount)
			t.Run("fail_vs_crash", c.failVsCrash)
			t.Run("picture", c.picture)
			t.Run("ask", c.ask)
			t.Run("inspect", c.inspect)
			t.Run("prompts", c.prompts)
			t.Run("resources", c.resources)
			t.Run("template", c.template)
			t.Run("hidden", c.hidden)
			if extra := s.ExtraStdout(); extra != "" {
				t.Errorf("stdout after the startup line must be empty, got %q", extra)
			}
		})
	}
}

// TestStop checks the process contract: the server exits when stdin closes,
// and when it gets SIGTERM.
func TestStop(t *testing.T) {
	for _, kind := range Kinds {
		t.Run(kind, func(t *testing.T) {
			if err := Available(kind); err != nil {
				SkipLoudly(t, kind, err)
			}
			t.Run("stdin", func(t *testing.T) {
				s := Start(t, kind)
				s.CloseStdin()
				select {
				case <-s.Exited():
				case <-time.After(5 * time.Second):
					t.Errorf("%s server still running 5s after stdin closed", kind)
				}
			})
			t.Run("sigterm", func(t *testing.T) {
				s := Start(t, kind)
				if err := s.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				select {
				case <-s.Exited():
				case <-time.After(5 * time.Second):
					t.Errorf("%s server still running 5s after SIGTERM", kind)
				}
			})
		})
	}
}

type checker struct {
	s *Server
}

func (c *checker) skipIfUnsupported(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if c.s.IsUnsupported(n) {
			t.Skipf("%s server lists %q as unsupported", c.s.Kind, n)
		}
	}
}

// connect opens a Go SDK client session. answer, if non-nil, answers
// elicitation requests; progress, if non-nil, receives progress notifications.
func (c *checker) connect(t *testing.T, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "conformance", Version: "0.0.1"}, opts)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: c.s.URL, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func (c *checker) callTool(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(ctxT(t), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("tools/call %s: %v", name, err)
	}
	return res
}

// --- startup and identity ---

func (c *checker) startup(t *testing.T) {
	s := c.s
	if want := "http://127.0.0.1:"; !strings.HasPrefix(s.URL, want) || !strings.HasSuffix(s.URL, "/mcp") {
		t.Errorf("url %q: want %s<port>/mcp", s.URL, want)
	}
	if s.SDK == "" || s.SDKVersion == "" {
		t.Errorf("sdk %q sdkVersion %q: both must be set", s.SDK, s.SDKVersion)
	}
	if len(s.ProtocolVersions) == 0 {
		t.Fatal("protocolVersions is empty")
	}
	if !sort.SliceIsSorted(s.ProtocolVersions, func(i, j int) bool { return s.ProtocolVersions[i] > s.ProtocolVersions[j] }) {
		t.Errorf("protocolVersions %v: want newest first", s.ProtocolVersions)
	}
	known := []string{"ask", "echo", "add", "search", "delete_page", "get_report", "slow_count", "fail", "crash", "picture", "inspect", "greet", "summarize", "guide", "faq", "key", "note", hiddenURI}
	for _, r := range contractResources {
		known = append(known, r.uri)
	}
	known = append(known, "ref://notes/{id}")
	for _, u := range s.Unsupported {
		if !slices.Contains(known, u) {
			t.Errorf("unsupported lists %q, which is not a name from the contract", u)
		}
	}
}

func (c *checker) identity(t *testing.T) {
	cs := c.connect(t, nil)
	ir := cs.InitializeResult()
	if ir == nil {
		t.Fatal("no initialize/discover result")
	}
	if slices.Contains(c.s.ProtocolVersions, newProtocol) && ir.ProtocolVersion != newProtocol {
		t.Errorf("negotiated %q with the Go SDK client; server lists %s, so want it", ir.ProtocolVersion, newProtocol)
	}
	if !slices.Contains(c.s.ProtocolVersions, ir.ProtocolVersion) {
		t.Errorf("negotiated %q, not in the startup line's protocolVersions %v", ir.ProtocolVersion, c.s.ProtocolVersions)
	}
	if ir.ServerInfo == nil {
		t.Fatal("no serverInfo")
	}
	if want := "refserver-" + c.s.Kind; ir.ServerInfo.Name != want || ir.ServerInfo.Version != "1.0.0" {
		t.Errorf("serverInfo = %s %s, want %s 1.0.0", ir.ServerInfo.Name, ir.ServerInfo.Version, want)
	}
	if ir.Instructions != instructions {
		t.Errorf("instructions = %q, want %q", ir.Instructions, instructions)
	}
}

// --- tools ---

func (c *checker) toolsList(t *testing.T) {
	cs := c.connect(t, nil)
	res, err := cs.ListTools(ctxT(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		got[tool.Name] = tool
	}
	for _, spec := range contractTools {
		tool, ok := got[spec.name]
		delete(got, spec.name)
		if c.s.IsUnsupported(spec.name) {
			if ok {
				t.Errorf("tool %s is listed but declared unsupported", spec.name)
			}
			continue
		}
		if !ok {
			t.Errorf("tool %s is missing", spec.name)
			continue
		}
		if tool.Description != spec.description {
			t.Errorf("%s description = %q, want %q", spec.name, tool.Description, spec.description)
		}
		var schema struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		remarshal(t, tool.InputSchema, &schema)
		if schema.Type != "object" {
			t.Errorf("%s inputSchema type = %q, want object", spec.name, schema.Type)
		}
		if !sameSet(schema.Required, spec.required) {
			t.Errorf("%s required = %v, want %v", spec.name, schema.Required, spec.required)
		}
		gotProps := map[string]string{}
		for name, raw := range schema.Properties {
			var p struct {
				Type string `json:"type"`
			}
			json.Unmarshal(raw, &p)
			gotProps[name] = p.Type
		}
		if !reflect.DeepEqual(gotProps, spec.props) {
			t.Errorf("%s properties = %v, want %v", spec.name, gotProps, spec.props)
		}
		if spec.name == "search" {
			var tel struct {
				Properties map[string]struct {
					Type string `json:"type"`
				} `json:"properties"`
				Required []string `json:"required"`
			}
			json.Unmarshal(schema.Properties["telemetry"], &tel)
			if tel.Properties["intent"].Type != "string" || !sameSet(tel.Required, []string{"intent"}) {
				t.Errorf("search telemetry schema = %s, want object with required string intent", schema.Properties["telemetry"])
			}
		}
		if spec.name == "slow_count" {
			var d struct {
				Default any `json:"default"`
			}
			json.Unmarshal(schema.Properties["delayMs"], &d)
			if d.Default != float64(20) {
				t.Errorf("slow_count delayMs default = %v, want 20", d.Default)
			}
		}
		ann := tool.Annotations
		readOnly := ann != nil && ann.ReadOnlyHint
		destructive := ann != nil && ann.DestructiveHint != nil && *ann.DestructiveHint
		if readOnly != spec.readOnly {
			t.Errorf("%s readOnlyHint = %v, want %v", spec.name, readOnly, spec.readOnly)
		}
		if spec.destructive && !destructive {
			t.Errorf("%s destructiveHint not true", spec.name)
		}
		if !spec.readOnly && !spec.destructive && ann != nil && (ann.ReadOnlyHint || destructive) {
			t.Errorf("%s has annotations %+v, want none", spec.name, ann)
		}
	}
	for name := range got {
		t.Errorf("unexpected tool %s", name)
	}
}

func (c *checker) echo(t *testing.T) {
	c.skipIfUnsupported(t, "echo")
	cs := c.connect(t, nil)
	res := c.callTool(t, cs, "echo", map[string]any{"text": "héllo <world> & \"quotes\""})
	wantText(t, res, "héllo <world> & \"quotes\"")
	wantStructured(t, res, `{"text":"héllo <world> & \"quotes\""}`)
}

func (c *checker) add(t *testing.T) {
	c.skipIfUnsupported(t, "add")
	cs := c.connect(t, nil)
	res := c.callTool(t, cs, "add", map[string]any{"a": 2, "b": 3})
	wantText(t, res, "5")
	wantStructured(t, res, `{"sum":5}`)
	res = c.callTool(t, cs, "add", map[string]any{"a": 1, "b": 1.5})
	wantText(t, res, "2.5")
	wantStructured(t, res, `{"sum":2.5}`)
}

// search must echo the arguments exactly as received, so it is checked on the
// wire with keys in a non-alphabetical order.
func (c *checker) search(t *testing.T) {
	c.skipIfUnsupported(t, "search")
	w := c.wire(t)
	args := `{"query":"pages","telemetry":{"intent":"find"},"space":"eng","limit":5}`
	msgs := w.call(t, "tools/call", "search", fmt.Sprintf(`{"name":"search","arguments":%s}`, args), nil)
	res := finalResult(t, msgs)
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
	}
	mustUnmarshal(t, res, &r)
	if len(r.Content) != 1 || r.Content[0].Type != "text" {
		t.Fatalf("content = %+v, want one text block", r.Content)
	}
	if r.Content[0].Text != args {
		t.Errorf("text = %s\nwant   %s (the arguments as received, compact)", r.Content[0].Text, args)
	}
	jsonEqual(t, "structuredContent", r.StructuredContent, `{"args":`+args+`}`)
}

func (c *checker) deletePage(t *testing.T) {
	c.skipIfUnsupported(t, "delete_page")
	cs := c.connect(t, nil)
	res := c.callTool(t, cs, "delete_page", map[string]any{"id": "p-42"})
	wantText(t, res, "deleted p-42")
}

func (c *checker) getReport(t *testing.T) {
	c.skipIfUnsupported(t, "get_report")
	cs := c.connect(t, nil)
	res := c.callTool(t, cs, "get_report", map[string]any{})
	wantText(t, res, "Report for ana@example.com. Card 4111 1111 1111 1111. Host db-internal-7.")
	wantStructured(t, res, `{"user":{"email":"ana@example.com","name":"Ana"},"rows":[{"id":1,"secret":"s1"},{"id":2,"secret":"s2"}],"note":null}`)
}

func (c *checker) slowCount(t *testing.T) {
	c.skipIfUnsupported(t, "slow_count")
	const steps = 3

	t.Run("wire", func(t *testing.T) {
		// On the wire, the progress notifications must arrive in order,
		// on the request's own stream, before the response.
		w := c.wire(t)
		msgs := w.call(t, "tools/call", "slow_count", `{"name":"slow_count","arguments":{"steps":3,"delayMs":10}}`, map[string]any{"progressToken": "tok-1"})
		var seen []float64
		for i, m := range msgs {
			if m.Method == "notifications/progress" {
				var p struct {
					ProgressToken any     `json:"progressToken"`
					Progress      float64 `json:"progress"`
					Total         float64 `json:"total"`
					Message       string  `json:"message"`
				}
				mustUnmarshal(t, m.Params, &p)
				n := len(seen) + 1
				if p.ProgressToken != "tok-1" || p.Progress != float64(n) || p.Total != steps ||
					p.Message != fmt.Sprintf("step %d of %d: reading db-internal-7", n, steps) {
					t.Errorf("progress #%d = %+v", n, p)
				}
				seen = append(seen, p.Progress)
				continue
			}
			if m.isResponse() && i != len(msgs)-1 {
				t.Errorf("response is message %d of %d; progress must come first", i+1, len(msgs))
			}
		}
		if len(seen) != steps {
			t.Errorf("got %d progress notifications %v, want %d", len(seen), seen, steps)
		}
		wantWireText(t, finalResult(t, msgs), "counted 3")
	})

	t.Run("client", func(t *testing.T) {
		var mu sync.Mutex
		var got []string
		cs := c.connect(t, &mcp.ClientOptions{
			ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
				mu.Lock()
				defer mu.Unlock()
				got = append(got, fmt.Sprintf("%v %v/%v %s", req.Params.ProgressToken, req.Params.Progress, req.Params.Total, req.Params.Message))
			},
		})
		res, err := cs.CallTool(ctxT(t), &mcp.CallToolParams{
			Name: "slow_count", Arguments: map[string]any{"steps": steps, "delayMs": 5},
			Meta: mcp.Meta{"progressToken": "abc"},
		})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, res, "counted 3")
		// The Go SDK client retires a call as soon as its response is read but
		// runs notification handlers on a separate queue, so the last
		// handler can run just after CallTool returns. Wait for it; the
		// order relative to the response is checked on the wire above.
		deadline := time.Now().Add(2 * time.Second)
		for {
			mu.Lock()
			n := len(got)
			mu.Unlock()
			if n >= steps || time.Now().After(deadline) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		want := []string{
			"abc 1/3 step 1 of 3: reading db-internal-7",
			"abc 2/3 step 2 of 3: reading db-internal-7",
			"abc 3/3 step 3 of 3: reading db-internal-7",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("progress = %q\nwant %q", got, want)
		}
	})

	t.Run("no_token", func(t *testing.T) {
		w := c.wire(t)
		msgs := w.call(t, "tools/call", "slow_count", `{"name":"slow_count","arguments":{"steps":2}}`, nil)
		for _, m := range msgs {
			if m.Method == "notifications/progress" {
				t.Errorf("progress sent without a progress token: %s", m.Params)
			}
		}
		wantWireText(t, finalResult(t, msgs), "counted 2")
	})
}

func (c *checker) failVsCrash(t *testing.T) {
	t.Run("fail", func(t *testing.T) {
		c.skipIfUnsupported(t, "fail")
		w := c.wire(t)
		msgs := w.call(t, "tools/call", "fail", `{"name":"fail","arguments":{}}`, nil)
		last := msgs[len(msgs)-1]
		if last.Error != nil {
			t.Fatalf("fail returned a JSON-RPC error %s; want a tool error result", last.Error)
		}
		var r struct {
			IsError bool `json:"isError"`
		}
		mustUnmarshal(t, last.Result, &r)
		if !r.IsError {
			t.Errorf("isError = false, want true")
		}
		wantWireText(t, last.Result, "boom")

		cs := c.connect(t, nil)
		res := c.callTool(t, cs, "fail", map[string]any{})
		if !res.IsError {
			t.Errorf("Go client: isError = false")
		}
		wantText(t, res, "boom")
	})
	t.Run("crash", func(t *testing.T) {
		c.skipIfUnsupported(t, "crash")
		w := c.wire(t)
		msgs := w.call(t, "tools/call", "crash", `{"name":"crash","arguments":{}}`, nil)
		last := msgs[len(msgs)-1]
		if last.Error == nil || last.Result != nil {
			t.Fatalf("crash: want a JSON-RPC error and no result, got result=%s error=%s", last.Result, last.Error)
		}
		var e struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		mustUnmarshal(t, last.Error, &e)
		t.Logf("crash error: code %d message %q", e.Code, e.Message)

		cs := c.connect(t, nil)
		if res, err := cs.CallTool(ctxT(t), &mcp.CallToolParams{Name: "crash", Arguments: map[string]any{}}); err == nil {
			t.Errorf("Go client: crash returned a result (isError=%v), want an error", res.IsError)
		}
	})
}

func (c *checker) picture(t *testing.T) {
	c.skipIfUnsupported(t, "picture")
	cs := c.connect(t, nil)
	res := c.callTool(t, cs, "picture", map[string]any{})
	if len(res.Content) != 3 {
		t.Fatalf("got %d content blocks, want 3", len(res.Content))
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); !ok || tc.Text != "a picture" {
		t.Errorf("block 1 = %#v, want text %q", res.Content[0], "a picture")
	}
	png, _ := base64.StdEncoding.DecodeString(pixelPNG)
	if ic, ok := res.Content[1].(*mcp.ImageContent); !ok || ic.MIMEType != "image/png" || !bytes.Equal(ic.Data, png) {
		t.Errorf("block 2 = %#v, want the 1x1 PNG", res.Content[1])
	}
	if rl, ok := res.Content[2].(*mcp.ResourceLink); !ok || rl.URI != "ref://docs/guide" || rl.Name != "guide" {
		t.Errorf("block 3 = %#v, want resource link ref://docs/guide named guide", res.Content[2])
	}
}

func (c *checker) ask(t *testing.T) {
	c.skipIfUnsupported(t, "ask")
	for _, tc := range []struct {
		action string
		want   string
	}{
		{"accept", "project=apollo"},
		{"decline", "declined"},
		{"cancel", "cancelled"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			var asked *mcp.ElicitParams
			cs := c.connect(t, &mcp.ClientOptions{
				ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
					asked = req.Params
					res := &mcp.ElicitResult{Action: tc.action}
					if tc.action == "accept" {
						res.Content = map[string]any{"project": "apollo"}
					}
					return res, nil
				},
			})
			res := c.callTool(t, cs, "ask", map[string]any{})
			wantText(t, res, tc.want)
			if asked == nil {
				t.Fatal("the elicitation handler was not called")
			}
			if asked.Message != "Which project?" {
				t.Errorf("elicitation message = %q", asked.Message)
			}
			var schema struct {
				Type       string `json:"type"`
				Properties map[string]struct {
					Type string `json:"type"`
				} `json:"properties"`
				Required []string `json:"required"`
			}
			remarshal(t, asked.RequestedSchema, &schema)
			if schema.Type != "object" || len(schema.Properties) != 1 || schema.Properties["project"].Type != "string" || !sameSet(schema.Required, []string{"project"}) {
				t.Errorf("requested schema = %+v, want one required string field project", schema)
			}
		})
	}
}

func (c *checker) inspect(t *testing.T) {
	c.skipIfUnsupported(t, "inspect")
	w := c.wire(t)
	w.extraHeaders = http.Header{
		"Authorization": {"Bearer secret-token"},
		"Cookie":        {"session=secret"},
		"X-Conformance": {"yes"},
	}
	msgs := w.call(t, "tools/call", "inspect", `{"name":"inspect","arguments":{}}`, map[string]any{"trace": "t-1"})
	res := finalResult(t, msgs)
	wantWireText(t, res, "inspected")
	var r struct {
		StructuredContent struct {
			ProtocolVersion *string           `json:"protocolVersion"`
			ClientInfo      map[string]any    `json:"clientInfo"`
			Capabilities    map[string]any    `json:"capabilities"`
			Meta            map[string]any    `json:"meta"`
			Headers         map[string]string `json:"headers"`
		} `json:"structuredContent"`
	}
	mustUnmarshal(t, res, &r)
	sc := r.StructuredContent
	if sc.ProtocolVersion == nil || *sc.ProtocolVersion != w.version {
		t.Errorf("protocolVersion = %v, want %s", sc.ProtocolVersion, w.version)
	}
	if sc.ClientInfo == nil || sc.ClientInfo["name"] != "conformance-wire" {
		t.Errorf("clientInfo = %v, want name conformance-wire", sc.ClientInfo)
	}
	if sc.Capabilities == nil {
		t.Errorf("capabilities = null, want the declared capabilities")
	}
	if sc.Meta == nil || sc.Meta["trace"] != "t-1" {
		t.Errorf("meta = %v, want the request's _meta with trace t-1", sc.Meta)
	}
	for _, h := range []string{"accept", "content-type", "x-conformance"} {
		if _, ok := sc.Headers[h]; !ok {
			t.Errorf("headers lack %q: %v", h, sc.Headers)
		}
	}
	if w.version >= newProtocol {
		for _, h := range []string{"mcp-protocol-version", "mcp-method", "mcp-name"} {
			if _, ok := sc.Headers[h]; !ok {
				t.Errorf("headers lack %q: %v", h, sc.Headers)
			}
		}
	}
	for name := range sc.Headers {
		if name != strings.ToLower(name) {
			t.Errorf("header %q is not lower-cased", name)
		}
		if name == "authorization" || name == "cookie" {
			t.Errorf("headers include %q", name)
		}
	}

	// The same through the Go SDK client.
	cs := c.connect(t, nil)
	cres := c.callTool(t, cs, "inspect", map[string]any{})
	var got struct {
		ProtocolVersion *string        `json:"protocolVersion"`
		ClientInfo      map[string]any `json:"clientInfo"`
	}
	remarshal(t, cres.StructuredContent, &got)
	if got.ProtocolVersion == nil || *got.ProtocolVersion != cs.InitializeResult().ProtocolVersion {
		t.Errorf("Go client: protocolVersion = %v, want %s", got.ProtocolVersion, cs.InitializeResult().ProtocolVersion)
	}
	if got.ClientInfo == nil || got.ClientInfo["name"] != "conformance" {
		t.Errorf("Go client: clientInfo = %v", got.ClientInfo)
	}
}

// --- prompts ---

func (c *checker) prompts(t *testing.T) {
	cs := c.connect(t, nil)
	res, err := cs.ListPrompts(ctxT(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	type arg struct {
		name     string
		required bool
	}
	want := map[string][]arg{
		"greet":     {{"who", true}},
		"summarize": {{"topic", true}, {"tone", false}},
	}
	got := map[string][]arg{}
	for _, p := range res.Prompts {
		var args []arg
		for _, a := range p.Arguments {
			args = append(args, arg{a.Name, a.Required})
		}
		got[p.Name] = args
	}
	for name, args := range want {
		if c.s.IsUnsupported(name) {
			delete(got, name)
			continue
		}
		if !reflect.DeepEqual(got[name], args) {
			t.Errorf("prompt %s arguments = %v, want %v", name, got[name], args)
		}
		delete(got, name)
	}
	for name := range got {
		t.Errorf("unexpected prompt %s", name)
	}

	for _, tc := range []struct {
		name string
		args map[string]string
		want string
	}{
		{"greet", map[string]string{"who": "Ana"}, "Hello, Ana!"},
		{"summarize", map[string]string{"topic": "the outage"}, "Summarize the outage in a neutral tone."},
		{"summarize", map[string]string{"topic": "Q3", "tone": "formal"}, "Summarize Q3 in a formal tone."},
	} {
		if c.s.IsUnsupported(tc.name) {
			t.Logf("skipping prompt %s: unsupported", tc.name)
			continue
		}
		pr, err := cs.GetPrompt(ctxT(t), &mcp.GetPromptParams{Name: tc.name, Arguments: tc.args})
		if err != nil {
			t.Errorf("prompts/get %s %v: %v", tc.name, tc.args, err)
			continue
		}
		if len(pr.Messages) != 1 || pr.Messages[0].Role != "user" {
			t.Errorf("prompts/get %s: want one user message, got %d", tc.name, len(pr.Messages))
			continue
		}
		if txt, ok := pr.Messages[0].Content.(*mcp.TextContent); !ok || txt.Text != tc.want {
			t.Errorf("prompts/get %s %v = %#v, want text %q", tc.name, tc.args, pr.Messages[0].Content, tc.want)
		}
	}
}

// --- resources ---

func (c *checker) resources(t *testing.T) {
	cs := c.connect(t, nil)
	res, err := cs.ListResources(ctxT(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*mcp.Resource{}
	for _, r := range res.Resources {
		got[r.URI] = r
	}
	if _, ok := got[hiddenURI]; ok {
		t.Errorf("%s is listed; it must not be", hiddenURI)
	}
	for _, spec := range contractResources {
		r, ok := got[spec.uri]
		delete(got, spec.uri)
		if c.s.IsUnsupported(spec.uri) || c.s.IsUnsupported(spec.name) {
			continue
		}
		if !ok {
			t.Errorf("resource %s is not listed", spec.uri)
			continue
		}
		if r.Name != spec.name || r.MIMEType != spec.mime {
			t.Errorf("resource %s = name %q mime %q, want %q %q", spec.uri, r.Name, r.MIMEType, spec.name, spec.mime)
		}
		c.wantRead(t, cs, spec.uri, spec.mime, spec.text)
	}
	for uri := range got {
		t.Errorf("unexpected resource %s", uri)
	}

	if _, err := cs.ReadResource(ctxT(t), &mcp.ReadResourceParams{URI: "ref://missing/nothing"}); err == nil {
		t.Errorf("reading an unknown URI succeeded; want the SDK's not-found error")
	} else {
		t.Logf("unknown URI error: %v", err)
	}
}

func (c *checker) template(t *testing.T) {
	c.skipIfUnsupported(t, "note", "ref://notes/{id}")
	cs := c.connect(t, nil)
	res, err := cs.ListResourceTemplates(ctxT(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ResourceTemplates) != 1 {
		t.Fatalf("got %d templates, want 1", len(res.ResourceTemplates))
	}
	rt := res.ResourceTemplates[0]
	if rt.URITemplate != "ref://notes/{id}" || rt.Name != "note" || rt.MIMEType != "text/plain" {
		t.Errorf("template = %q %q %q, want ref://notes/{id} note text/plain", rt.URITemplate, rt.Name, rt.MIMEType)
	}
	c.wantRead(t, cs, "ref://notes/42", "text/plain", "note 42")
	c.wantRead(t, cs, "ref://notes/abc-1", "text/plain", "note abc-1")
}

func (c *checker) hidden(t *testing.T) {
	c.skipIfUnsupported(t, hiddenURI)
	cs := c.connect(t, nil)
	c.wantRead(t, cs, hiddenURI, "", "hidden thing")
}

func (c *checker) wantRead(t *testing.T, cs *mcp.ClientSession, uri, mime, text string) {
	t.Helper()
	res, err := cs.ReadResource(ctxT(t), &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		t.Errorf("resources/read %s: %v", uri, err)
		return
	}
	if len(res.Contents) != 1 {
		t.Errorf("resources/read %s: %d contents, want 1", uri, len(res.Contents))
		return
	}
	rc := res.Contents[0]
	if rc.URI != uri || rc.Text != text || (mime != "" && rc.MIMEType != mime) {
		t.Errorf("resources/read %s = uri %q mime %q text %q; want %q %q", uri, rc.URI, rc.MIMEType, rc.Text, mime, text)
	}
}

// --- result helpers ---

func wantText(t *testing.T, res *mcp.CallToolResult, want string) {
	t.Helper()
	if len(res.Content) != 1 {
		t.Errorf("got %d content blocks, want 1 text block %q", len(res.Content), want)
		return
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Errorf("content is %T, want text %q", res.Content[0], want)
		return
	}
	if tc.Text != want {
		t.Errorf("text = %q, want %q", tc.Text, want)
	}
}

func wantStructured(t *testing.T, res *mcp.CallToolResult, want string) {
	t.Helper()
	got, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	jsonEqual(t, "structuredContent", got, want)
}

func wantWireText(t *testing.T, result json.RawMessage, want string) {
	t.Helper()
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	mustUnmarshal(t, result, &r)
	if len(r.Content) != 1 || r.Content[0].Type != "text" || r.Content[0].Text != want {
		t.Errorf("content = %+v, want one text block %q", r.Content, want)
	}
}

func jsonEqual(t *testing.T, what string, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Errorf("%s is not JSON: %s", what, got)
		return
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad want %s: %v", want, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s = %s\nwant %s", what, got, want)
	}
}

func remarshal(t *testing.T, from, to any) {
	t.Helper()
	data, err := json.Marshal(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, to); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
}

func mustUnmarshal(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// --- plain HTTP JSON-RPC ---

// wireMsg is one JSON-RPC message read off the wire.
type wireMsg struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func (m wireMsg) isResponse() bool { return m.Method == "" && (m.Result != nil || m.Error != nil) }

// wire speaks JSON-RPC over plain HTTP. On a server that speaks 2026-07-28 it
// sends stateless requests carrying the per-request _meta and the MCP headers;
// otherwise it performs the legacy initialize handshake and uses the session.
type wire struct {
	url          string
	version      string
	sessionID    string
	nextID       int
	extraHeaders http.Header
	client       *http.Client
}

func (c *checker) wire(t *testing.T) *wire {
	t.Helper()
	w := &wire{url: c.s.URL, client: &http.Client{Timeout: 20 * time.Second}}
	if slices.Contains(c.s.ProtocolVersions, newProtocol) {
		w.version = newProtocol
		return w
	}
	w.version = c.s.ProtocolVersions[0]
	msgs, hdr := w.post(t, "initialize", "", fmt.Sprintf(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":%q,"capabilities":{},"clientInfo":{"name":"conformance-wire","version":"0.0.1"}}}`, w.version))
	var ir struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	mustUnmarshal(t, finalResult(t, msgs), &ir)
	w.version = ir.ProtocolVersion
	w.sessionID = hdr.Get("Mcp-Session-Id")
	w.post(t, "notifications/initialized", "", `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	return w
}

// call sends a request. paramsJSON is the params object without _meta; meta
// holds extra _meta keys (such as progressToken).
func (w *wire) call(t *testing.T, method, name, paramsJSON string, meta map[string]any) []wireMsg {
	t.Helper()
	var params map[string]json.RawMessage
	mustUnmarshal(t, []byte(paramsJSON), &params)
	m := map[string]any{}
	for k, v := range meta {
		m[k] = v
	}
	if w.version >= newProtocol {
		m["io.modelcontextprotocol/protocolVersion"] = w.version
		m["io.modelcontextprotocol/clientCapabilities"] = map[string]any{}
		m["io.modelcontextprotocol/clientInfo"] = map[string]any{"name": "conformance-wire", "version": "0.0.1"}
	}
	if len(m) > 0 {
		raw, _ := json.Marshal(m)
		params["_meta"] = raw
	}
	w.nextID++
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": w.nextID, "method": method, "params": params})
	msgs, _ := w.post(t, method, name, string(body))
	return msgs
}

func (w *wire) post(t *testing.T, method, name, body string) ([]wireMsg, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, w.url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if method != "initialize" {
		req.Header.Set("Mcp-Protocol-Version", w.version)
	}
	if w.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", w.sessionID)
	}
	if w.version >= newProtocol {
		req.Header.Set("Mcp-Method", method)
		if name != "" {
			req.Header.Set("Mcp-Name", name)
		}
	}
	for k, vs := range w.extraHeaders {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := w.client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		return nil, resp.Header
	}
	ct := resp.Header.Get("Content-Type")
	var msgs []wireMsg
	switch {
	case strings.HasPrefix(ct, "text/event-stream"):
		msgs = readSSE(t, resp.Body)
	case strings.HasPrefix(ct, "application/json"):
		data, _ := io.ReadAll(resp.Body)
		var m wireMsg
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("POST %s: HTTP %d, body %s", method, resp.StatusCode, data)
		}
		msgs = []wireMsg{m}
	default:
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s: HTTP %d, content type %q, body %s", method, resp.StatusCode, ct, data)
	}
	return msgs, resp.Header
}

// readSSE reads JSON-RPC messages from an SSE body until it ends.
func readSSE(t *testing.T, r io.Reader) []wireMsg {
	t.Helper()
	var msgs []wireMsg
	var data strings.Builder
	flush := func() {
		if data.Len() == 0 {
			return
		}
		var m wireMsg
		if err := json.Unmarshal([]byte(data.String()), &m); err != nil {
			t.Errorf("bad SSE data %q: %v", data.String(), err)
		} else {
			msgs = append(msgs, m)
		}
		data.Reset()
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	flush()
	return msgs
}

// finalResult returns the result of the response, failing on an error.
func finalResult(t *testing.T, msgs []wireMsg) json.RawMessage {
	t.Helper()
	for _, m := range msgs {
		if m.isResponse() {
			if m.Error != nil {
				t.Fatalf("JSON-RPC error: %s", m.Error)
			}
			return m.Result
		}
	}
	t.Fatalf("no response among %d messages", len(msgs))
	return nil
}

// TestAgreement checks that the available servers agree with each other on
// what the contract compares across languages: tool names, descriptions and
// input schemas, prompt and resource listings, and the text and structured
// results of the deterministic calls. Text is compared byte for byte; JSON
// values are compared after decoding, since key order inside a JSON object
// carries no meaning.
func TestAgreement(t *testing.T) {
	type snapshot struct {
		kind string
		vals map[string]string
	}
	var snaps []snapshot
	for _, kind := range Kinds {
		if err := Available(kind); err != nil {
			fmt.Fprintf(os.Stderr, "\n*** TestAgreement: SKIPPING %s reference server: %v ***\n\n", kind, err)
			t.Logf("skipping %s: %v", kind, err)
			continue
		}
		s := Start(t, kind)
		c := &checker{s: s}
		snaps = append(snaps, snapshot{kind, c.snapshot(t)})
	}
	if len(snaps) < 2 {
		t.Skipf("only %d server available; nothing to compare", len(snaps))
	}
	base := snaps[0]
	for _, other := range snaps[1:] {
		keys := map[string]bool{}
		for k := range base.vals {
			keys[k] = true
		}
		for k := range other.vals {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			a, aok := base.vals[k]
			b, bok := other.vals[k]
			if !aok || !bok {
				// One side declared the item unsupported or lacks it; the
				// conformance test reports that.
				continue
			}
			if a != b {
				t.Errorf("%s differs:\n  %-10s %s\n  %-10s %s", k, base.kind, a, other.kind, b)
			}
		}
	}
}

// snapshot collects the cross-language comparable values of one server.
func (c *checker) snapshot(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	canon := func(v any) string {
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("<%v>", err)
		}
		var x any
		if err := json.Unmarshal(data, &x); err != nil {
			return string(data)
		}
		data, _ = json.Marshal(x) // maps marshal with sorted keys
		return string(data)
	}
	cs := c.connect(t, &mcp.ClientOptions{
		ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"project": "apollo"}}, nil
		},
	})
	ctx := ctxT(t)
	if ir := cs.InitializeResult(); ir != nil {
		out["instructions"] = ir.Instructions
	}
	if res, err := cs.ListTools(ctx, nil); err == nil {
		for _, tool := range res.Tools {
			out["tool "+tool.Name+" description"] = tool.Description
			out["tool "+tool.Name+" inputSchema"] = canon(tool.InputSchema)
			out["tool "+tool.Name+" outputSchema"] = canon(tool.OutputSchema)
		}
	}
	if res, err := cs.ListPrompts(ctx, nil); err == nil {
		for _, p := range res.Prompts {
			out["prompt "+p.Name] = canon(p)
		}
	}
	if res, err := cs.ListResources(ctx, nil); err == nil {
		for _, r := range res.Resources {
			out["resource "+r.URI] = canon(r)
		}
	}
	if res, err := cs.ListResourceTemplates(ctx, nil); err == nil {
		for _, r := range res.ResourceTemplates {
			out["template "+r.URITemplate] = canon(r)
		}
	}
	calls := []struct {
		name string
		args any
	}{
		{"echo", map[string]any{"text": "héllo <world>"}},
		{"add", map[string]any{"a": 2, "b": 3}},
		{"add", map[string]any{"a": 1, "b": 1.5}},
		{"search", json.RawMessage(`{"query":"q","telemetry":{"intent":"find"},"limit":3}`)},
		{"delete_page", map[string]any{"id": "p1"}},
		{"get_report", map[string]any{}},
		{"slow_count", map[string]any{"steps": 1, "delayMs": 1}},
		{"fail", map[string]any{}},
		{"picture", map[string]any{}},
		{"ask", map[string]any{}},
	}
	for i, call := range calls {
		if c.s.IsUnsupported(call.name) {
			continue
		}
		key := fmt.Sprintf("call %d %s", i, call.name)
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: call.name, Arguments: call.args})
		if err != nil {
			out[key+" error"] = "error"
			continue
		}
		out[key+" isError"] = fmt.Sprint(res.IsError)
		for j, block := range res.Content {
			if tc, ok := block.(*mcp.TextContent); ok {
				out[fmt.Sprintf("%s text[%d]", key, j)] = tc.Text
			} else {
				out[fmt.Sprintf("%s content[%d]", key, j)] = canon(block)
			}
		}
		out[key+" structuredContent"] = canon(res.StructuredContent)
	}
	for _, p := range []struct {
		name string
		args map[string]string
	}{{"greet", map[string]string{"who": "Ana"}}, {"summarize", map[string]string{"topic": "X"}}} {
		if res, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: p.name, Arguments: p.args}); err == nil {
			out["prompts/get "+p.name] = canon(res.Messages)
		}
	}
	uris := []string{"ref://docs/guide", "ref://docs/faq", "ref://secrets/key", "ref://notes/7", hiddenURI}
	for _, uri := range uris {
		if res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri}); err == nil {
			out["read "+uri] = canon(res.Contents)
		}
	}
	return out
}

func TestAvailableUnknownKind(t *testing.T) {
	if err := Available("cobol"); err == nil {
		t.Error("Available(cobol) = nil, want an error")
	}
}

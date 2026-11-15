// Command go is the Go reference MCP server for the hooks end-to-end harness.
//
// It implements the contract in pkg/hooks/harness/servers/SPEC.md on the
// official Go SDK (github.com/modelcontextprotocol/go-sdk), serving Streamable
// HTTP in the SDK's stateless mode, which is what lets the SDK speak protocol
// version 2026-07-28.
//
// Run it from the repository root:
//
//	go run ./pkg/hooks/harness/servers/go --port 0
//
// The first stdout line is the startup JSON object; logs go to stderr. The
// process exits on SIGTERM or when stdin is closed.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	sdkModule    = "github.com/modelcontextprotocol/go-sdk"
	instructions = "Reference server.\n\n## Notes\nUse search first."
	pixelPNG     = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	hiddenURI    = "ref://hidden/thing"
)

// startup is the first line written to stdout.
type startup struct {
	URL              string   `json:"url"`
	Language         string   `json:"language"`
	SDK              string   `json:"sdk"`
	SDKVersion       string   `json:"sdkVersion"`
	ProtocolVersions []string `json:"protocolVersions"`
	Unsupported      []string `json:"unsupported"`
}

func main() {
	port := flag.Int("port", 0, "port to listen on (0 picks a free port)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(*port, logger); err != nil {
		logger.Error("refserver-go failed", "err", err)
		os.Exit(1)
	}
}

func run(port int, logger *slog.Logger) error {
	// The SDK logs every stateless session at info level; keep its warnings
	// and errors only.
	sdkLogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	server := newServer(sdkLogger)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, Logger: sdkLogger})
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)

	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	line, err := json.Marshal(startup{
		URL:              "http://" + ln.Addr().String() + "/mcp",
		Language:         "go",
		SDK:              sdkModule,
		SDKVersion:       sdkVersion(),
		ProtocolVersions: protocolVersions(),
		Unsupported:      []string{},
	})
	if err != nil {
		return err
	}
	out := bufio.NewWriter(os.Stdout)
	out.Write(line)
	out.WriteByte('\n')
	if err := out.Flush(); err != nil {
		return err
	}

	logger.Info("listening", "url", "http://"+ln.Addr().String()+"/mcp")
	httpServer := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- httpServer.Serve(ln) }()

	stop := make(chan string, 2)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, os.Interrupt)
	go func() { stop <- (<-sigs).String() }()
	go func() {
		io.Copy(io.Discard, os.Stdin)
		stop <- "stdin closed"
	}()

	select {
	case reason := <-stop:
		logger.Info("shutting down", "reason", reason)
	case err := <-served:
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		httpServer.Close()
	}
	logger.Info("stopped")
	return nil
}

// protocolVersions is what this SDK build speaks on a stateless Streamable
// HTTP transport, newest first, read from the SDK itself.
func protocolVersions() []string {
	probe := &mcp.StreamableServerTransport{Stateless: true}
	var out []string
	for _, v := range mcp.SupportedProtocolVersions() {
		if probe.SupportsProtocolVersion(v) {
			out = append(out, v)
		}
	}
	return out
}

// sdkVersion reads the SDK module version from the binary's build info.
func sdkVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range bi.Deps {
			if dep.Path == sdkModule {
				if dep.Replace != nil {
					dep = dep.Replace
				}
				return strings.TrimPrefix(dep.Version, "v")
			}
		}
	}
	return "unknown"
}

func ptr[T any](v T) *T { return &v }

var (
	readOnly    = &mcp.ToolAnnotations{ReadOnlyHint: true}
	destructive = &mcp.ToolAnnotations{DestructiveHint: ptr(true)}
)

const noArgs = `{"type":"object","properties":{}}`

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func newServer(logger *slog.Logger) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "refserver-go", Version: "1.0.0"}, &mcp.ServerOptions{
		Instructions: instructions,
		Logger:       logger,
	})
	addTools(s)
	addPrompts(s)
	addResources(s)
	return s
}

func addTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "echo", Description: "Return the text.", Annotations: readOnly,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
		Text string `json:"text"`
	}) (*mcp.CallToolResult, any, error) {
		res := text(in.Text)
		res.StructuredContent = map[string]any{"text": in.Text}
		return res, nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "add", Description: "Add two numbers.", Annotations: readOnly,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"}},"required":["a","b"]}`),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
		A float64 `json:"a"`
		B float64 `json:"b"`
	}) (*mcp.CallToolResult, any, error) {
		sum := in.A + in.B
		res := text(strconv.FormatFloat(sum, 'f', -1, 64))
		res.StructuredContent = map[string]any{"sum": sum}
		return res, nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "search", Description: "Search pages.", Annotations: readOnly,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer"},"space":{"type":"string"},"telemetry":{"type":"object","properties":{"intent":{"type":"string"}},"required":["intent"]}},"required":["query","telemetry"]}`),
	}, func(_ context.Context, req *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
		// Echo the arguments exactly as received: compact the raw bytes
		// rather than re-marshal a decoded map, which would reorder keys.
		var compact bytes.Buffer
		if err := json.Compact(&compact, req.Params.Arguments); err != nil {
			return nil, nil, err
		}
		res := text(compact.String())
		res.StructuredContent = map[string]any{"args": json.RawMessage(compact.Bytes())}
		return res, nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "delete_page", Description: "Delete a page.", Annotations: destructive,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
	}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
		ID string `json:"id"`
	}) (*mcp.CallToolResult, any, error) {
		return text("deleted " + in.ID), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_report", Description: "Return the report.", Annotations: readOnly,
		InputSchema: json.RawMessage(noArgs),
	}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		res := text("Report for ana@example.com. Card 4111 1111 1111 1111. Host db-internal-7.")
		res.StructuredContent = json.RawMessage(`{"user":{"email":"ana@example.com","name":"Ana"},"rows":[{"id":1,"secret":"s1"},{"id":2,"secret":"s2"}],"note":null}`)
		return res, nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "slow_count", Description: "Count slowly, reporting progress.", Annotations: readOnly,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"steps":{"type":"integer"},"delayMs":{"type":"integer","default":20}},"required":["steps"]}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Steps   int `json:"steps"`
		DelayMs int `json:"delayMs"`
	}) (*mcp.CallToolResult, any, error) {
		if token := req.Params.GetProgressToken(); token != nil {
			for i := 1; i <= in.Steps; i++ {
				select {
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				case <-time.After(time.Duration(in.DelayMs) * time.Millisecond):
				}
				err := req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
					ProgressToken: token,
					Progress:      float64(i),
					Total:         float64(in.Steps),
					Message:       fmt.Sprintf("step %d of %d: reading db-internal-7", i, in.Steps),
				})
				if err != nil {
					return nil, nil, err
				}
			}
		}
		return text(fmt.Sprintf("counted %d", in.Steps)), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "fail", Description: "Always fails.",
		InputSchema: json.RawMessage(noArgs),
	}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		// A plain error from a typed handler becomes a tool error
		// (isError: true, text = the error message).
		return nil, nil, errors.New("boom")
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "crash", Description: "Always crashes.",
		InputSchema: json.RawMessage(noArgs),
	}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		// A *jsonrpc.Error from a typed handler is returned to the client
		// as a JSON-RPC error rather than a tool result.
		return nil, nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "crash"}
	})

	png, err := base64.StdEncoding.DecodeString(pixelPNG)
	if err != nil {
		panic(err)
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "picture", Description: "Return a picture.", Annotations: readOnly,
		InputSchema: json.RawMessage(noArgs),
	}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: "a picture"},
			&mcp.ImageContent{Data: png, MIMEType: "image/png"},
			&mcp.ResourceLink{URI: "ref://docs/guide", Name: "guide"},
		}}, nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "ask", Description: "Ask which project.",
		InputSchema: json.RawMessage(noArgs),
	}, func(_ context.Context, req *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
		// Multi round-trip (SEP-2322): the first call returns an input
		// request; the client retries with the user's answer. For clients
		// older than 2026-07-28 the SDK's middleware turns this into a
		// legacy elicitation/create request, which needs a stateful session.
		resp, ok := req.Params.InputResponses["project"]
		if !ok {
			return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{
				"project": &mcp.ElicitParams{
					Message:         "Which project?",
					RequestedSchema: json.RawMessage(`{"type":"object","properties":{"project":{"type":"string"}},"required":["project"]}`),
				},
			}}, nil, nil
		}
		er, ok := resp.(*mcp.ElicitResult)
		if !ok {
			return nil, nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "inputResponses.project is not an elicitation result"}
		}
		switch er.Action {
		case "accept":
			return text(fmt.Sprintf("project=%v", er.Content["project"])), nil, nil
		case "decline":
			return text("declined"), nil, nil
		default:
			return text("cancelled"), nil, nil
		}
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "inspect", Description: "Describe the request.", Annotations: readOnly,
		InputSchema: json.RawMessage(noArgs),
	}, func(_ context.Context, req *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
		res := text("inspected")
		res.StructuredContent = inspect(req)
		return res, nil, nil
	})
}

// inspect describes the request as the server saw it.
func inspect(req *mcp.CallToolRequest) map[string]any {
	out := map[string]any{
		"protocolVersion": nil,
		"clientInfo":      nil,
		"capabilities":    nil,
		"meta":            nil,
		"headers":         map[string]string{},
	}
	if v := req.ProtocolVersion(); v != "" {
		out["protocolVersion"] = v
	}
	if ci := req.ClientInfo(); ci != nil {
		out["clientInfo"] = ci
	}
	meta := req.Params.GetMeta()
	// Prefer the capabilities exactly as the client declared them in _meta
	// (2026-07-28); fall back to what the SDK recorded for older versions.
	if raw, ok := meta[mcp.MetaKeyClientCapabilities]; ok && raw != nil {
		out["capabilities"] = raw
	} else if caps := req.ClientCapabilities(); caps != nil {
		out["capabilities"] = caps
	}
	if meta != nil {
		out["meta"] = meta
	}
	headers := map[string]string{}
	if req.Extra != nil {
		for name, values := range req.Extra.Header {
			lower := strings.ToLower(name)
			switch lower {
			case "authorization", "proxy-authorization", "cookie":
				continue
			}
			headers[lower] = strings.Join(values, ", ")
		}
	}
	out["headers"] = headers
	return out
}

func addPrompts(s *mcp.Server) {
	s.AddPrompt(&mcp.Prompt{
		Name:      "greet",
		Arguments: []*mcp.PromptArgument{{Name: "who", Required: true}},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		who, ok := req.Params.Arguments["who"]
		if !ok {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: `missing required argument "who"`}
		}
		return userMessage("Hello, " + who + "!"), nil
	})
	s.AddPrompt(&mcp.Prompt{
		Name: "summarize",
		Arguments: []*mcp.PromptArgument{
			{Name: "topic", Required: true},
			{Name: "tone"},
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		topic, ok := req.Params.Arguments["topic"]
		if !ok {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: `missing required argument "topic"`}
		}
		tone, ok := req.Params.Arguments["tone"]
		if !ok {
			tone = "neutral"
		}
		return userMessage("Summarize " + topic + " in a " + tone + " tone."), nil
	})
}

func userMessage(s string) *mcp.GetPromptResult {
	return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: s}}}}
}

func textResource(uri, mime, body string) *mcp.ReadResourceResult {
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: mime, Text: body}}}
}

func addResources(s *mcp.Server) {
	for _, r := range []struct{ uri, name, mime, body string }{
		{"ref://docs/guide", "guide", "text/markdown", "# Guide\nSee ref://docs/faq and https://example.com/x."},
		{"ref://docs/faq", "faq", "text/plain", "FAQ"},
		{"ref://secrets/key", "key", "text/plain", "sk-test-123"},
	} {
		s.AddResource(&mcp.Resource{URI: r.uri, Name: r.name, MIMEType: r.mime},
			func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return textResource(r.uri, r.mime, r.body), nil
			})
	}

	s.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "ref://notes/{id}", Name: "note", MIMEType: "text/plain"},
		func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			uri := req.Params.URI
			return textResource(uri, "text/plain", "note "+strings.TrimPrefix(uri, "ref://notes/")), nil
		})

	// The SDK only reads URIs that are registered (listed) or match a
	// template, and has no option to register a resource without listing it.
	// So the hidden resource is registered normally, which keeps reading it
	// entirely on the SDK's path, and a receiving middleware (the SDK's
	// extension point for intercepting requests) drops it from resources/list.
	s.AddResource(&mcp.Resource{URI: hiddenURI, Name: "thing", MIMEType: "text/plain"},
		func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return textResource(hiddenURI, "text/plain", "hidden thing"), nil
		})
	s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if lr, ok := res.(*mcp.ListResourcesResult); ok && err == nil && method == "resources/list" {
				lr.Resources = slices.DeleteFunc(lr.Resources, func(r *mcp.Resource) bool { return r.URI == hiddenURI })
			}
			return res, err
		}
	})
}

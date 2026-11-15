package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewGo is the official Go SDK, used in this process. It has the same three
// operations as the subprocess drivers, so the battery treats all clients
// alike.
func NewGo() (Driver, error) { return &goDriver{}, nil }

type goDriver struct {
	mu      sync.Mutex
	session *mcp.ClientSession
	seq     int

	// the call in flight
	cmu          sync.Mutex
	answers      []Answer
	progress     []map[string]any
	elicitations []map[string]any
}

func (g *goDriver) Name() string { return "go" }

func (g *goDriver) sdk() SDK {
	s := SDK{Name: "github.com/modelcontextprotocol/go-sdk", Version: goSDKVersion(), Language: "go"}
	if g.session != nil && g.session.InitializeResult() != nil {
		s.ProtocolVersion = g.session.InitializeResult().ProtocolVersion
	}
	return s
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	if b.token != "" {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.next.RoundTrip(r)
}

func (g *goDriver) Connect(ctx context.Context, o ConnectOptions) (Reply, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.session != nil {
		_ = g.session.Close()
		g.session = nil
	}
	opts := &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			ev := map[string]any{"progress": r.Params.Progress}
			if r.Params.Total != 0 {
				ev["total"] = r.Params.Total
			}
			if r.Params.Message != "" {
				ev["message"] = r.Params.Message
			}
			g.cmu.Lock()
			g.progress = append(g.progress, ev)
			g.cmu.Unlock()
		},
	}
	if _, ok := o.Capabilities["elicitation"]; ok {
		opts.ElicitationHandler = func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			g.cmu.Lock()
			defer g.cmu.Unlock()
			g.elicitations = append(g.elicitations, toMap(r.Params))
			if len(g.answers) == 0 {
				return nil, errors.New("driver: no scripted elicitation answer left")
			}
			a := g.answers[0]
			g.answers = g.answers[1:]
			return &mcp.ElicitResult{Action: a.Action, Content: a.Content}, nil
		}
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "harness-go-client", Version: "1.0.0"}, opts)
	// A transport of its own, so no connection is shared between checks.
	tr := &mcp.StreamableClientTransport{
		Endpoint:             o.URL,
		HTTPClient:           &http.Client{Transport: bearer{token: o.Token, next: &http.Transport{}}},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}
	cs, err := c.Connect(ctx, tr, nil)
	if err != nil {
		return Reply{Error: goError(err, false), SDK: g.sdk()}, nil
	}
	g.session = cs
	return Reply{SDK: g.sdk()}, nil
}

func goError(err error, cancelled bool) *Error {
	if cancelled {
		return &Error{Kind: "cancelled", Message: err.Error()}
	}
	var we *jsonrpc.Error
	if errors.As(err, &we) {
		var data any
		if len(we.Data) > 0 {
			_ = json.Unmarshal(we.Data, &data)
		}
		return &Error{Kind: "rpc", Code: float64(we.Code), Message: we.Message, Data: data}
	}
	return &Error{Kind: "other", Type: fmt.Sprintf("%T", err), Message: err.Error()}
}

func (g *goDriver) Call(ctx context.Context, o CallOptions) (Reply, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.session == nil {
		return Reply{Error: &Error{Kind: "driver", Message: "not connected"}, SDK: g.sdk()}, nil
	}
	g.cmu.Lock()
	g.answers, g.progress, g.elicitations = append([]Answer(nil), o.Elicit...), nil, nil
	g.cmu.Unlock()

	cctx, cancel := ctx, context.CancelFunc(func() {})
	if o.CancelAfterMs > 0 {
		cctx, cancel = context.WithTimeout(ctx, time.Duration(o.CancelAfterMs)*time.Millisecond)
	}
	defer cancel()
	res, err := g.dispatch(cctx, o)
	out := Reply{}
	if err != nil {
		out.Error = goError(err, o.CancelAfterMs > 0 && cctx.Err() != nil)
	} else {
		out.Result = toMap(res)
	}
	// The Go SDK hands progress to its handler on another goroutine, so a
	// call can return before the last event is delivered. Wait until the
	// count stops moving.
	if o.Progress {
		g.settle()
	}
	g.cmu.Lock()
	out.Progress, out.Elicitations = g.progress, g.elicitations
	g.cmu.Unlock()
	out.SDK = g.sdk()
	return out, nil
}

func (g *goDriver) settle() {
	last, still := -1, 0
	for i := 0; i < 100 && still < 6; i++ {
		g.cmu.Lock()
		n := len(g.progress)
		g.cmu.Unlock()
		if n == last {
			still++
		} else {
			last, still = n, 0
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (g *goDriver) dispatch(ctx context.Context, o CallOptions) (any, error) {
	cs := g.session
	str := func(k string) string { s, _ := o.Params[k].(string); return s }
	switch o.Method {
	case "tools/list":
		return cs.ListTools(ctx, &mcp.ListToolsParams{Cursor: str("cursor")})
	case "prompts/list":
		return cs.ListPrompts(ctx, &mcp.ListPromptsParams{Cursor: str("cursor")})
	case "resources/list":
		return cs.ListResources(ctx, &mcp.ListResourcesParams{Cursor: str("cursor")})
	case "resources/templates/list":
		return cs.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{Cursor: str("cursor")})
	case "tools/call":
		p := &mcp.CallToolParams{Name: str("name"), Arguments: o.Params["arguments"]}
		if p.Arguments == nil {
			p.Arguments = map[string]any{}
		}
		if o.Progress {
			g.seq++
			p.SetProgressToken(fmt.Sprintf("go-token-%d", g.seq))
		}
		return cs.CallTool(ctx, p)
	case "prompts/get":
		args := map[string]string{}
		if m, ok := o.Params["arguments"].(map[string]any); ok {
			for k, v := range m {
				args[k] = fmt.Sprint(v)
			}
		}
		p := &mcp.GetPromptParams{Name: str("name"), Arguments: args}
		if o.Progress {
			g.seq++
			p.SetProgressToken(fmt.Sprintf("go-token-%d", g.seq))
		}
		return cs.GetPrompt(ctx, p)
	case "resources/read":
		p := &mcp.ReadResourceParams{URI: str("uri")}
		if o.Progress {
			g.seq++
			p.SetProgressToken(fmt.Sprintf("go-token-%d", g.seq))
		}
		return cs.ReadResource(ctx, p)
	}
	return nil, fmt.Errorf("driver: unsupported method %s", o.Method)
}

func (g *goDriver) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.session != nil {
		err := g.session.Close()
		g.session = nil
		return err
	}
	return nil
}

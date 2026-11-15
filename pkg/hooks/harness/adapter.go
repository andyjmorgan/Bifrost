package harness

import (
	"context"
	"encoding/json"
	"iter"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
)

// This file is the only place the harness calls the rules engine. The
// gateway speaks HTTP and SSE; everything that decides policy goes through
// here.
//
// The reply path is events in, events out. adapter.replies hands every
// upstream event to the engine's stream entry points (ToolReplyStream,
// PromptReplyStream, ReadReplyStream), which route each one to its hook and
// yield what the client should receive.

// upEvent is one message from an upstream: a notification, or the final
// response.
type upEvent struct {
	Method string          // set for a notification
	Params json.RawMessage // the notification's params
	Result json.RawMessage // the final result
	Error  *hooks.RPCError // the final error
}

func (e upEvent) final() bool { return e.Method == "" }

// clientEvent is one message for the client: a notification to relay, or
// the final outcome.
type clientEvent struct {
	Method  string
	Params  json.RawMessage
	Outcome *hooks.Outcome
}

// begun is the request side of a forwarded method: the engine's outcome,
// and how to hand it the upstream's reply stream.
type begun struct {
	Outcome hooks.Outcome
	stream  func(ctx context.Context, in iter.Seq2[hooks.UpstreamEvent, error]) iter.Seq2[hooks.ReplyEvent, error]
}

type adapter struct {
	e *hooks.Engine
}

func (a adapter) discover(ctx context.Context, r hooks.Request, version string) hooks.Outcome {
	return a.e.Discover(ctx, r, version)
}

// connect is server/connect for one server.
func (a adapter) connect(ctx context.Context, r hooks.Request, s *hooks.Server, version string) (hooks.ConnectInfo, []hooks.Hit) {
	return a.e.Connect(ctx, r, s, version)
}

// list answers a listing from the catalog. ok is false for a method that is
// not a listing.
func (a adapter) list(ctx context.Context, r hooks.Request, method string) (hooks.Outcome, bool) {
	switch method {
	case "tools/list":
		return a.e.ListTools(ctx, r), true
	case "prompts/list":
		return a.e.ListPrompts(ctx, r), true
	case "resources/list":
		return a.e.ListResources(ctx, r), true
	case "resources/templates/list":
		return a.e.ListTemplates(ctx, r), true
	}
	return hooks.Outcome{}, false
}

// switches is server/config for one server.
func (a adapter) switches(ctx context.Context, r hooks.Request, s *hooks.Server) (map[string]bool, []hooks.Hit) {
	return a.e.Config(ctx, r, s)
}

func invalid(err error) *hooks.RPCError {
	return &hooks.RPCError{Code: hooks.CodeInvalidParams, Message: "invalid params: " + err.Error()}
}

// begin runs the request side of a forwarded method.
func (a adapter) begin(ctx context.Context, r hooks.Request, method string, params json.RawMessage) (begun, *hooks.RPCError) {
	switch method {
	case "tools/call":
		var p mcp.CallToolParamsRaw
		if err := json.Unmarshal(params, &p); err != nil {
			return begun{}, invalid(err)
		}
		return a.beginTool(ctx, r, &p), nil
	case "prompts/get":
		var p mcp.GetPromptParams
		if err := json.Unmarshal(params, &p); err != nil {
			return begun{}, invalid(err)
		}
		out := a.e.GetPrompt(ctx, r, &p)
		return begun{Outcome: out, stream: func(ctx context.Context, in iter.Seq2[hooks.UpstreamEvent, error]) iter.Seq2[hooks.ReplyEvent, error] {
			return a.e.PromptReplyStream(ctx, r, out.Call, &p, in)
		}}, nil
	case "resources/read":
		var p mcp.ReadResourceParams
		if err := json.Unmarshal(params, &p); err != nil {
			return begun{}, invalid(err)
		}
		out := a.e.ReadResource(ctx, r, &p)
		return begun{Outcome: out, stream: func(ctx context.Context, in iter.Seq2[hooks.UpstreamEvent, error]) iter.Seq2[hooks.ReplyEvent, error] {
			return a.e.ReadReplyStream(ctx, r, out.Call, &p, in)
		}}, nil
	case "resources/directory/read":
		var p mcp.ReadResourceParams
		if err := json.Unmarshal(params, &p); err != nil {
			return begun{}, invalid(err)
		}
		out := a.e.ReadDirectory(ctx, r, &p)
		return begun{Outcome: out, stream: func(ctx context.Context, in iter.Seq2[hooks.UpstreamEvent, error]) iter.Seq2[hooks.ReplyEvent, error] {
			return a.e.ReadReplyStream(ctx, r, out.Call, &p, in)
		}}, nil
	}
	return begun{}, &hooks.RPCError{Code: -32601, Message: "method not found: " + method}
}

func (a adapter) beginTool(ctx context.Context, r hooks.Request, p *mcp.CallToolParamsRaw) begun {
	out := a.e.CallTool(ctx, r, p)
	return begun{Outcome: out, stream: func(ctx context.Context, in iter.Seq2[hooks.UpstreamEvent, error]) iter.Seq2[hooks.ReplyEvent, error] {
		return a.e.ToolReplyStream(ctx, r, out.Call, p, in)
	}}
}

// replies turns the upstream's events into the client's events, through the
// engine. The engine drops or reshapes progress with the family's progress
// hook, drops log messages and unknown notifications, and sends the final
// message down the result or input path.
func (a adapter) replies(ctx context.Context, b begun, in iter.Seq2[upEvent, error]) iter.Seq2[clientEvent, error] {
	up := func(yield func(hooks.UpstreamEvent, error) bool) {
		for ev, err := range in {
			if !yield(hooks.UpstreamEvent{Method: ev.Method, Params: ev.Params, Result: ev.Result, Error: ev.Error}, err) {
				return
			}
		}
	}
	return func(yield func(clientEvent, error) bool) {
		for ev, err := range b.stream(ctx, up) {
			if !yield(clientEvent{Method: ev.Method, Params: ev.Params, Outcome: ev.Final}, err) {
				return
			}
		}
	}
}

// instructions answers the mux's own get_server_instructions tool.
func (a adapter) instructions(ctx context.Context, r hooks.Request, args json.RawMessage) hooks.Outcome {
	var p struct {
		Slug string `json:"slug"`
	}
	_ = json.Unmarshal(args, &p)
	return a.e.ServerInstructions(ctx, r, p.Slug)
}

// Search mode's own tools. The engine implements them; the gateway only
// names them and wraps their output as tool results.
const (
	toolSearch     = "tool_search"
	toolListTools  = "list_tools"
	toolDefinition = "get_tool_definition"
	toolExecute    = "execute_tool"
)

func searchTools() []any {
	obj := func(props map[string]any, required ...string) map[string]any {
		req := []any{}
		for _, r := range required {
			req = append(req, r)
		}
		return map[string]any{"type": "object", "properties": props, "required": req}
	}
	str := map[string]any{"type": "string"}
	return []any{
		map[string]any{"name": toolSearch, "description": "Search the tools on this gateway.", "inputSchema": obj(map[string]any{"query": str, "limit": map[string]any{"type": "integer"}}, "query")},
		map[string]any{"name": toolListTools, "description": "List every tool on this gateway by server.", "inputSchema": obj(map[string]any{})},
		map[string]any{"name": toolDefinition, "description": "Return the definitions of tools by exact name.", "inputSchema": obj(map[string]any{"names": map[string]any{"type": "array", "items": str}}, "names")},
		map[string]any{"name": toolExecute, "description": "Call a tool by exact name.", "inputSchema": obj(map[string]any{"name": str, "arguments": map[string]any{"type": "object"}}, "name")},
	}
}

// search runs one of search mode's tools. ok is false for any other name.
func (a adapter) search(ctx context.Context, r hooks.Request, p *mcp.CallToolParamsRaw) (begun, bool) {
	own := func(out hooks.Outcome) begun {
		if out.Error == nil && out.Result != nil {
			text, isText := out.Result.(string)
			if !isText {
				b, _ := json.Marshal(out.Result)
				text = string(b)
			}
			res := map[string]any{"resultType": "complete", "content": []any{map[string]any{"type": "text", "text": text}}}
			if !isText {
				res["structuredContent"] = out.Result
			}
			out.Result = res
		}
		return begun{Outcome: out}
	}
	switch p.Name {
	case toolSearch:
		var q struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		_ = json.Unmarshal(p.Arguments, &q)
		return own(a.e.ToolSearch(ctx, r, q.Query, q.Limit)), true
	case toolListTools:
		return own(a.e.ListToolsYAML(ctx, r)), true
	case toolDefinition:
		var q struct {
			Names []string `json:"names"`
		}
		_ = json.Unmarshal(p.Arguments, &q)
		return own(a.e.ToolDefinitions(ctx, r, q.Names)), true
	case toolExecute:
		var q struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(p.Arguments, &q)
		out := a.e.ExecuteTool(ctx, r, q.Name, q.Arguments, p)
		call := &mcp.CallToolParamsRaw{Name: q.Name, Arguments: q.Arguments, RequestState: p.RequestState, InputResponses: p.InputResponses}
		return begun{Outcome: out, stream: func(ctx context.Context, in iter.Seq2[hooks.UpstreamEvent, error]) iter.Seq2[hooks.ReplyEvent, error] {
			return a.e.ToolReplyStream(ctx, r, out.Call, call, in)
		}}, true
	case hooks.InstructionsTool:
		return begun{Outcome: a.instructions(ctx, r, p.Arguments)}, true
	}
	return begun{}, false
}

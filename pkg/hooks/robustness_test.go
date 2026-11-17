package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// These tests pin one promise: whatever a client or an upstream server sends,
// the engine returns an outcome. It may refuse; it must not panic.
// fault_test.go covers the safety net for a panic that happens anyway.

// oddValues is every JSON kind, including the shapes real clients and
// servers get wrong: null for "not given", strings for numbers, scalars
// where an object is expected.
var oddValues = []string{
	`null`, `true`, `false`, `0`, `-1`, `1.5`, `1e30`, `9007199254740993`, `""`, `"x"`, `"50"`, `"2026-01-01T00:00:00Z"`,
	`[]`, `[null]`, `[1,"a",{}]`, `{}`, `{"a":null}`, `{"a":{"b":[null,{"c":1}]}}`,
}

// panicLog counts the panics of a test and reports the first ten.
type panicLog struct {
	t     *testing.T
	mu    sync.Mutex
	count int
}

// run calls fn and records a panic instead of letting it end the test.
func (p *panicLog) run(label string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.count++
			if p.count <= 10 {
				p.t.Errorf("panic on %s: %v\n%s", label, r, firstFrames(debug.Stack()))
			}
		}
	}()
	fn()
}

// done says how many panics there were, if more than were shown.
func (p *panicLog) done() {
	if p.count > 10 {
		p.t.Errorf("%d panics in total; the first 10 are shown", p.count)
	}
}

// firstFrames is the top of a stack, cut to the engine's own frames.
func firstFrames(stack []byte) string {
	lines := strings.Split(string(stack), "\n")
	var out []string
	for i, l := range lines {
		if strings.Contains(l, "pkg/hooks/") && !strings.Contains(l, "_test.go") {
			if i > 0 {
				out = append(out, strings.TrimSpace(lines[i-1]))
			}
			out = append(out, strings.TrimSpace(l))
			if len(out) >= 8 {
				break
			}
		}
	}
	return strings.Join(out, "\n")
}

// Every JSON kind in every argument, and as the arguments value itself.
func TestOddArgumentsNeverPanic(t *testing.T) {
	e := &Engine{}
	set := mustCompile(t, typedRules())
	p := &panicLog{t: t}
	defer p.done()
	ctx := context.Background()
	names := []string{"limit", "q", "flag", "tags", "opts", "from", "org", "mode", "unknown"}
	n := 0
	for _, route := range []string{"direct", "mux"} {
		name := "t"
		if route == "mux" {
			name = "a" + Separator + "t"
		}
		call := func(raw string) {
			n++
			p.run(route+" arguments "+raw, func() {
				s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: set, Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
				out := e.CallTool(ctx, newRequest(route, s), &mcp.CallToolParamsRaw{Name: name, Arguments: json.RawMessage(raw)})
				if out.Forward == nil && out.Result == nil && out.Error == nil {
					t.Errorf("no outcome for %s arguments %s", route, raw)
				}
			})
		}
		for _, v := range append(oddValues, ``, `{`, `{"q":"a"}{"q":"b"}`, `{"q":"a","q":"b"}`) {
			call(v) // the arguments value itself
		}
		for _, k := range names {
			for _, v := range oddValues {
				call(fmt.Sprintf(`{"q":"abcd",%q:%s}`, k, v))
				call(fmt.Sprintf(`{%q:%s}`, k, v))
			}
		}
	}
	t.Logf("%d calls, %d panics", n, p.count)
}

// Every JSON kind in every part of an upstream tool result.
func TestOddToolResultsNeverPanic(t *testing.T) {
	e := &Engine{}
	set := mustCompile(t, typedRules())
	p := &panicLog{t: t}
	defer p.done()
	ctx := context.Background()
	var replies []string
	for _, v := range oddValues {
		replies = append(replies,
			v,
			fmt.Sprintf(`{"content":%s}`, v),
			fmt.Sprintf(`{"content":[%s]}`, v),
			fmt.Sprintf(`{"content":[{"type":%s,"text":%s}]}`, v, v),
			fmt.Sprintf(`{"content":[{"type":"text","text":"x"}],"structuredContent":%s}`, v),
			fmt.Sprintf(`{"content":[{"type":"text","text":"x"}],"structuredContent":{"a":%s,"rows":%s}}`, v, v),
			fmt.Sprintf(`{"content":[{"type":"resource_link","uri":%s,"name":%s}]}`, v, v),
			fmt.Sprintf(`{"content":[{"type":"resource","resource":%s}]}`, v),
			fmt.Sprintf(`{"content":[{"type":"image","data":%s,"mimeType":%s}]}`, v, v),
			fmt.Sprintf(`{"content":[],"isError":%s}`, v),
			fmt.Sprintf(`{"content":[],"_meta":%s}`, v),
			fmt.Sprintf(`{"resultType":%s,"content":[]}`, v),
			fmt.Sprintf(`{"resultType":"input_required","inputRequests":%s,"requestState":%s}`, v, v),
			fmt.Sprintf(`{"resultType":"input_required","inputRequests":{"k":%s}}`, v),
			fmt.Sprintf(`{"resultType":"input_required","inputRequests":{"k":{"method":%s,"params":%s}}}`, v, v),
			fmt.Sprintf(`{"resultType":"task","task":%s}`, v),
		)
	}
	replies = append(replies, ``, `{`, `[`, `nul`)
	n := 0
	for _, route := range []string{"direct", "mux"} {
		name := "t"
		if route == "mux" {
			name = "a" + Separator + "t"
		}
		for _, raw := range replies {
			n++
			p.run(route+" result "+raw, func() {
				s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: set, Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
				r := newRequest(route, s)
				params := &mcp.CallToolParamsRaw{Name: name, Arguments: json.RawMessage(`{"q":"abc"}`)}
				first := e.CallTool(ctx, r, params)
				if first.Forward == nil {
					t.Fatalf("the plain call was not forwarded: %+v", first.Error)
				}
				out := e.UpstreamToolReply(ctx, r, first.Call, params, json.RawMessage(raw))
				if out.Result == nil && out.Error == nil {
					t.Errorf("no outcome for %s result %s", route, raw)
				}
			})
		}
	}
	t.Logf("%d replies, %d panics", n, p.count)
}

// Every JSON kind in every part of a tool's input schema, through listing
// and calling. Schemas come from upstream servers and are the least
// reliable input the engine has.
func TestOddSchemasNeverPanic(t *testing.T) {
	e := &Engine{}
	rules := append(typedRules(),
		Rule{Hook: "tools/list.post", When: "true", Then: map[string]string{"tool.outputSchema": "dropSchema(tool.outputSchema, ['limit', 'opts.depth', 'tags.*', '*'])"}},
	)
	set := mustCompile(t, rules)
	p := &panicLog{t: t}
	defer p.done()
	ctx := context.Background()
	var schemas []string
	for _, v := range oddValues {
		schemas = append(schemas,
			v,
			fmt.Sprintf(`{"type":%s}`, v),
			fmt.Sprintf(`{"type":"object","properties":%s}`, v),
			fmt.Sprintf(`{"type":"object","properties":{"limit":%s,"q":%s}}`, v, v),
			fmt.Sprintf(`{"type":"object","properties":{"limit":{"type":%s,"maximum":%s,"minimum":%s,"enum":%s,"const":%s,"default":%s}}}`, v, v, v, v, v, v),
			fmt.Sprintf(`{"type":"object","properties":{"limit":{"type":"integer"}},"required":%s}`, v),
			fmt.Sprintf(`{"type":"object","properties":{"limit":{"type":"integer"}},"required":[%s]}`, v),
			fmt.Sprintf(`{"type":"object","properties":{"limit":{"type":"integer"}},"additionalProperties":%s}`, v),
			fmt.Sprintf(`{"type":"object","properties":{"limit":{"type":"integer"}},"patternProperties":%s}`, v),
			fmt.Sprintf(`{"type":"object","properties":{"limit":{"type":"integer"}},"patternProperties":{"(":%s}}`, v),
			fmt.Sprintf(`{"type":"object","properties":{"opts":{"type":"object","properties":%s}}}`, v),
			fmt.Sprintf(`{"type":"object","properties":{"tags":{"type":"array","items":%s}}}`, v),
			fmt.Sprintf(`{"type":"object","allOf":%s,"anyOf":%s,"oneOf":%s}`, v, v, v),
			fmt.Sprintf(`{"type":"object","properties":{"limit":{"$ref":%s}},"$defs":%s}`, v, v),
		)
	}
	n := 0
	for _, route := range []string{"direct", "mux"} {
		name := "t"
		if route == "mux" {
			name = "a" + Separator + "t"
		}
		for _, raw := range schemas {
			var schema any
			if json.Unmarshal([]byte(raw), &schema) != nil {
				continue
			}
			n++
			p.run(route+" schema "+raw, func() {
				tool := &mcp.Tool{Name: "t", InputSchema: schema, OutputSchema: schema}
				s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: set, Catalog: Catalog{Tools: []*mcp.Tool{tool}}}
				r := newRequest(route, s)
				if out := e.ListTools(ctx, r); out.Result == nil && out.Error == nil {
					t.Errorf("no listing outcome for schema %s", raw)
				}
				for _, args := range []string{`{}`, `{"limit":5,"q":"abcd"}`, `{"limit":null}`, `{"limit":"5","opts":{"depth":2},"tags":["x"]}`} {
					out := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: name, Arguments: json.RawMessage(args)})
					if out.Forward == nil && out.Result == nil && out.Error == nil {
						t.Errorf("no call outcome for schema %s", raw)
					}
				}
			})
		}
	}
	t.Logf("%d schemas, %d panics", n, p.count)
}

// Every JSON kind in every part of a resource read reply.
func TestOddReadRepliesNeverPanic(t *testing.T) {
	e := &Engine{}
	set := mustCompile(t, []Rule{
		{Hook: "resources/read.post.result", When: "true", Then: map[string]string{"response.result.contents": "response.result.contents.map(c, has(c.text) ? {'uri': c.uri, ?'mimeType': c.?mimeType, 'text': regex.replace(c.text, 'x', 'y')} : c)"}},
	})
	p := &panicLog{t: t}
	defer p.done()
	ctx := context.Background()
	var replies []string
	for _, v := range oddValues {
		replies = append(replies,
			v,
			fmt.Sprintf(`{"contents":%s}`, v),
			fmt.Sprintf(`{"contents":[%s]}`, v),
			fmt.Sprintf(`{"contents":[{"uri":%s,"text":%s,"mimeType":%s}]}`, v, v, v),
			fmt.Sprintf(`{"contents":[{"uri":"file:///doc","blob":%s}]}`, v),
			fmt.Sprintf(`{"contents":[{"uri":"file:///doc","text":"x","_meta":%s}]}`, v),
			fmt.Sprintf(`{"resultType":%s,"contents":[]}`, v),
			fmt.Sprintf(`{"resultType":"input_required","inputRequests":%s}`, v),
		)
	}
	replies = append(replies, ``, `{`)
	n := 0
	for _, route := range []string{"direct", "mux"} {
		uri := "file:///doc"
		if route == "mux" {
			uri = PrefixURI("a", uri)
		}
		for _, raw := range replies {
			n++
			p.run(route+" read reply "+raw, func() {
				s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: set, Catalog: Catalog{Resources: []*mcp.Resource{{URI: "file:///doc", Name: "doc"}}}}
				r := newRequest(route, s)
				params := &mcp.ReadResourceParams{URI: uri}
				first := e.ReadResource(ctx, r, params)
				if first.Forward == nil {
					t.Fatalf("the plain read was not forwarded: %+v", first.Error)
				}
				out := e.UpstreamReadReply(ctx, r, first.Call, params, json.RawMessage(raw))
				if out.Result == nil && out.Error == nil {
					t.Errorf("no outcome for %s read reply %s", route, raw)
				}
			})
		}
	}
	t.Logf("%d replies, %d panics", n, p.count)
}

// The real catalog: every top-level argument of every real tool, set to
// one value of each JSON kind in turn.
func TestOddArgumentsOnRealToolsNeverPanic(t *testing.T) {
	tools := realTools(t)
	e := &Engine{}
	set := mustCompile(t, typedRules())
	p := &panicLog{t: t}
	defer p.done()
	ctx := context.Background()
	s := &Server{Slug: "dd", Generation: "1", Rules: set, Catalog: Catalog{Tools: tools}}
	r := newRequest("direct", s)
	var n, forwarded, refused atomic.Int64
	parallel(len(tools), 0, func(i int) {
		tool := tools[i]
		schema, _ := tool.InputSchema.(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		for k := range props {
			for _, v := range []string{`null`, `true`, `1.5`, `"50"`, `[null]`, `{"a":null}`} {
				n.Add(1)
				raw := fmt.Sprintf(`{%q:%s}`, k, v)
				p.run(tool.Name+" "+raw, func() {
					out := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: tool.Name, Arguments: json.RawMessage(raw)})
					switch {
					case out.Forward != nil:
						forwarded.Add(1)
					case out.Result != nil || out.Error != nil:
						refused.Add(1)
					default:
						t.Errorf("no outcome for %s %s", tool.Name, raw)
					}
				})
			}
		}
	})
	if forwarded.Load() == 0 || refused.Load() == 0 {
		t.Errorf("expected both forwards and refusals, got %d and %d", forwarded.Load(), refused.Load())
	}
	t.Logf("%d calls on %d real tools: %d forwarded, %d refused, %d panics", n.Load(), len(tools), forwarded.Load(), refused.Load(), p.count)
}

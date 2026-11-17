package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// prompts/list and prompts/get: what each hook reads after a rename.

// A prompt whose name and argument a list rule renamed: the list hook, the
// pre hook, the progress hook and the elicit hook each read the client's
// names on one side and the server's on the other, before and after a
// continuation round.
func TestEveryPromptHookReadsBothSides(t *testing.T) {
	ctx := context.Background()
	sides := "prompt.name + '<' + prompt.upstream.name + ' ' + request.params.name + '<' + request.upstream.params.name" +
		" + ' client=' + request.params.arguments.area + '/' + string(has(request.params.arguments.space))" +
		" + ' server=' + request.upstream.params.arguments.space + '/' + string(has(request.upstream.params.arguments.area))" +
		" + ' args=' + prompt.arguments.map(a, a.name).join(',') + '<' + prompt.upstream.arguments.map(a, a.name).join(',')" +
		" + ' params=' + string('area' in prompt.params) + string('space' in prompt.params) + ' desc=' + prompt.description + '<' + prompt.upstream.description"
	want := "summary<confluence_summary summary<confluence_summary client=ENG+later/false server=ENG+later/false args=area,tone<space,tone params=truefalse desc=gateway words<server words"
	cat := Catalog{Prompts: []*mcp.Prompt{{Name: "confluence_summary", Description: "server words", Arguments: []*mcp.PromptArgument{{Name: "space", Required: true}, {Name: "tone"}}}}}
	s := newServer(t, cat,
		Rule{Hook: "prompts/list.post", When: "prompt.upstream.name == 'confluence_summary'", Then: map[string]string{"prompt.name": "'summary'", "prompt.params.space.name": "'area'", "prompt.description": "'gateway words'"}},
		Rule{Hook: "prompts/list.post", When: "prompt.name == 'summary'", Then: map[string]string{"prompt.title": "prompt.name + '<' + prompt.upstream.name + ' ' + prompt.arguments.map(a, a.name).join(',') + '<' + prompt.upstream.arguments.map(a, a.name).join(',')"}},
		Rule{Hook: "prompts/get.pre", When: "prompt.name == 'summary' && prompt.upstream.name == 'confluence_summary' && request.params.name == 'summary'", Then: map[string]string{"request.params.arguments.tone": "'pre'"}},
		Rule{Hook: "prompts/get.pre", When: "request.params.arguments.tone == 'pre'", Then: map[string]string{"request.params.arguments.area": "request.params.arguments.area + '+later'"}},
		Rule{Hook: "prompts/get.post.progress", When: "true", Then: map[string]string{"progress.params.message": sides}},
		Rule{Hook: "prompts/get.post.elicit", When: "true", Then: map[string]string{"elicitation.params.message": sides}},
	)
	e, r := &Engine{}, newRequest("direct", s)
	if got := plainJSON(e.ListPrompts(ctx, r).Result); !strings.Contains(got, `"title":"summary<confluence_summary area,tone<space,tone"`) {
		t.Errorf("list hook: %s", got)
	}
	if o := e.GetPrompt(ctx, r, &mcp.GetPromptParams{Name: "confluence_summary", Arguments: map[string]string{"area": "ENG"}}); o.Forward != nil {
		t.Errorf("a get by the server's name was forwarded")
	}
	if o := e.GetPrompt(ctx, r, &mcp.GetPromptParams{Name: "summary", Arguments: map[string]string{"space": "ENG"}}); o.Forward != nil {
		t.Errorf("a get with the server's argument name was forwarded")
	}
	p := &mcp.GetPromptParams{Name: "summary", Arguments: map[string]string{"area": "ENG"}}
	o := e.GetPrompt(ctx, r, p)
	if o.Forward == nil {
		t.Fatalf("not forwarded: %s", outcomeText(o))
	}
	if got := asJSON(o.Forward.Params); !strings.Contains(got, `"name":"confluence_summary"`) || !strings.Contains(got, `"space":"ENG+later"`) || strings.Contains(got, `"area"`) {
		t.Errorf("forwarded %s", got)
	}
	first := collect(e.PromptReplyStream(ctx, r, o.Call, p, (&upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progress":1,"message":"half"}`), resultEvent(serverQuestion)}}).seq()))
	if len(first.notes) != 1 || !strings.Contains(plainJSON(first.notes[0].Params), want) {
		t.Errorf("progress hook: %s", asJSON(first.notes))
	}
	if got := plainJSON(first.final.Result); !strings.Contains(got, want) {
		t.Errorf("elicit hook: %s", got)
	}
	ir, ok := first.final.Result.(InputRequired)
	if !ok {
		t.Fatalf("the server's question did not reach the client")
	}
	p2 := &mcp.GetPromptParams{Name: "summary", Arguments: map[string]string{"area": "ENG"}, RequestState: ir.RequestState, InputResponses: serverAnswer()}
	o2 := e.GetPrompt(ctx, r, p2)
	if o2.Forward == nil {
		t.Fatalf("the continuation was not forwarded: %s", outcomeText(o2))
	}
	second := collect(e.PromptReplyStream(ctx, r, o2.Call, p2, (&upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progress":2,"message":"done"}`), resultEvent(`{"messages":[]}`)}}).seq()))
	if len(second.notes) != 1 || !strings.Contains(plainJSON(second.notes[0].Params), want) {
		t.Errorf("progress hook after the continuation: %s", asJSON(second.notes))
	}
}

// After a list rule renames a prompt, its elicit and progress hooks still
// read the server's name under prompt.upstream: a deny written against it
// refuses the server's question and drops a progress event.
func TestPromptReplyHooksReadTheUpstreamNameAfterARename(t *testing.T) {
	for _, hook := range []string{"prompts/get.post.elicit", "prompts/get.post.progress"} {
		t.Run(hook, func(t *testing.T) {
			s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent},
				Catalog: Catalog{Prompts: []*mcp.Prompt{{Name: "original"}}},
				Rules: mustCompileUncapped(t,
					Rule{Hook: "prompts/list.post", When: "true", Then: map[string]string{"prompt.name": "'public'"}},
					Rule{Hook: "server.session", When: "true", Then: map[string]string{"session.capabilities.elicitation": "true"}},
					Rule{Hook: hook, When: "prompt.upstream.name == 'original'", Then: map[string]string{"decision": "'deny'"}},
				),
			}
			e := &Engine{}
			r := newRequest("direct", s)
			p := &mcp.GetPromptParams{Name: "public"}
			first := e.GetPrompt(context.Background(), r, p)
			if first.Forward == nil || first.Call.Target != "original" {
				t.Fatal("renamed prompt did not forward under its upstream name")
			}
			if hook == "prompts/get.post.progress" {
				u := &upstreamSeq{events: []UpstreamEvent{
					progressEvent(`{"progressToken":"p","progress":1,"message":"sensitive"}`),
					resultEvent(`{"messages":[]}`),
				}}
				got := collect(e.PromptReplyStream(context.Background(), r, first.Call, p, u.seq()))
				if len(got.notes) != 0 || got.final == nil || got.final.Error != nil {
					t.Fatalf("upstream-name deny must drop progress only: notes=%d final=%v", len(got.notes), got.final != nil)
				}
				return
			}
			raw := json.RawMessage(`{"resultType":"input_required","inputRequests":{"q":{"method":"elicitation/create","params":{"mode":"form","message":"sensitive","requestedSchema":{"type":"object","properties":{"answer":{"type":"string"}}}}}}}`)
			out := e.UpstreamPromptReply(context.Background(), r, first.Call, p, raw)
			if out.Error == nil || out.Result != nil {
				t.Fatalf("upstream-name deny must refuse the reply after a rename: error=%v result=%T", out.Error, out.Result)
			}
		})
	}
}

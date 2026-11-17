package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Regressions from the third review's fix pass (November 15, 2026): each
// test reproduced a finding and now holds its fix.

func TestCodexV3FixUnknownFlushOnConsumerExit(t *testing.T) { // V3F-G01
	for _, panics := range []bool{false, true} {
		name := "return_false_control"
		if panics {
			name = "consumer_panic"
		}
		t.Run(name, func(t *testing.T) {
			if panics {
			}
			c := newStreamCase(t, "direct", nil, Limits{})
			var recorded []Unknown
			c.e.OnUnknown = func(u Unknown) { recorded = append(recorded, u) }
			u := &upstreamSeq{events: []UpstreamEvent{
				{Method: "notifications/vendor", Params: json.RawMessage(`{"field":"value"}`)},
				progressEvent(`{"progressToken":"p","progress":1}`),
				resultEvent(`{"content":[]}`),
			}}
			sentinel := errors.New("test consumer panic")
			var recovered any
			notes, finals := 0, 0
			func() {
				defer func() { recovered = recover() }()
				for ev, err := range c.e.ToolReplyStream(context.Background(), c.r, c.call, c.params, u.seq()) {
					if err != nil {
						t.Fatal(err)
					}
					if ev.Final != nil {
						finals++
					} else {
						notes++
					}
					if panics {
						panic(sentinel)
					}
					break
				}
			}()
			if (panics && recovered != sentinel) || (!panics && recovered != nil) {
				t.Fatalf("consumer panic was changed: %v", recovered)
			}
			if notes != 1 || finals != 0 || !u.closed || u.pulled != 2 {
				t.Fatalf("consumer departure must stop pulling without a synthetic final: notes=%d finals=%d closed=%v pulled=%d", notes, finals, u.closed, u.pulled)
			}
			if len(recorded) != 1 {
				t.Fatalf("unknown record must reach the callback once on every exit: got %d", len(recorded))
			}
		})
	}
}

func TestCodexV3FixEmptySetNeedsAList(t *testing.T) { // V3F-G02
	var ex Expect
	if err := json.Unmarshal([]byte(`{"path":"client.result.tools","set":[]}`), &ex); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, report string
		wantPass     bool
	}{
		{"empty_list_control", `{"client":{"result":{"tools":[]}}}`, true},
		{"nonempty_list_control", `{"client":{"result":{"tools":[{"name":"secret"}]}}}`, false},
		{"null_control", `{"client":{"result":{"tools":null}}}`, false},
		{"error_instead_of_listing", `{"client":{"error":{"code":-32603,"message":"broken"}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "error_instead_of_listing" {
			}
			var report any
			if err := json.Unmarshal([]byte(tc.report), &report); err != nil {
				t.Fatal(err)
			}
			failure := ex.Check(report, nil)
			if (failure == "") != tc.wantPass {
				t.Fatalf("set:[] pass=%v, want %v; diagnostic=%q", failure == "", tc.wantPass, failure)
			}
		})
	}
}

func TestCodexV3FixReplyHooksReadExposedTool(t *testing.T) { // V3F-G03
	for _, hook := range []string{"tools/call.post.result", "tools/call.post.elicit", "tools/call.post.progress"} {
		for _, exposed := range []bool{false, true} {
			name := hook + "/upstream_control"
			when := "tool.upstream.name == 't'"
			if exposed {
				name = hook + "/exposed_name"
				when = "tool.name == 'public' && tool.upstream.name == 't'"
			}
			t.Run(name, func(t *testing.T) {
				if exposed {
				}
				c := newStreamCase(t, "direct", nil, Limits{})
				c.r.Servers[0].Rules = codexSet(t,
					Rule{Hook: "tools/list.post", When: "true", Then: map[string]string{"tool.name": "'public'"}},
					Rule{Hook: "server.session", When: "true", Then: map[string]string{"session.capabilities.elicitation": "true"}},
					Rule{Hook: hook, When: when, Then: map[string]string{"decision": "'deny'"}},
				)
				c.params.Name = "public"
				first := c.e.CallTool(context.Background(), c.r, c.params)
				if first.Forward == nil || first.Call.Transform.Upstream != "t" || first.Call.Transform.Exposed != "public" {
					t.Fatal("renamed tool must forward under its original name")
				}
				c.call = first.Call
				if hook == "tools/call.post.progress" {
					got := c.run(&upstreamSeq{events: []UpstreamEvent{
						progressEvent(`{"progressToken":"p","progress":1,"message":"sensitive"}`),
						resultEvent(`{"content":[]}`),
					}})
					if len(got.notes) != 0 || got.final == nil || got.final.Error != nil {
						t.Fatalf("deny on %s must drop progress only: notes=%d final=%v", when, len(got.notes), got.final != nil)
					}
					return
				}
				raw := json.RawMessage(`{"content":[{"type":"text","text":"sensitive"}]}`)
				if hook == "tools/call.post.elicit" {
					raw = json.RawMessage(`{"resultType":"input_required","inputRequests":{"q":{"method":"elicitation/create","params":{"mode":"form","message":"sensitive","requestedSchema":{"type":"object","properties":{"answer":{"type":"string"}}}}}}}`)
				}
				out := c.e.UpstreamToolReply(context.Background(), c.r, c.call, c.params, raw)
				b, err := json.Marshal(out.Result)
				if err != nil {
					t.Fatal(err)
				}
				var result map[string]any
				if err := json.Unmarshal(b, &result); err != nil {
					t.Fatal(err)
				}
				if out.Error != nil || result["isError"] != true {
					t.Fatalf("deny on %s must return a tool error: rpcError=%v isError=%v resultType=%v", when, out.Error, result["isError"], result["resultType"])
				}
			})
		}
	}
}

func TestCodexV3FixFlagsCompilerEdges(t *testing.T) { // V3F-C01
	for _, expr := range []string{
		"has(.request.flags.debug)", "['search'].exists(k, request.flags[k])",
		"[request.flags].exists(f, f.search)", "request.flags.exists(k, k == 'search')",
		"[1].all(x, has(request.flags.debug))", "(true ? request.flags : {}).search",
		"request.flags['serach']", "has(.request.flags.serach)",
	} {
		t.Run("reject/"+expr, func(t *testing.T) {
			if _, problems := Compile([]Rule{{Hook: "tools/list.post", When: expr, Then: map[string]string{"decision": "'deny'"}}}); problems == nil {
				t.Fatalf("non-named or unknown flag access compiled: %s", expr)
			}
		})
	}
	for _, expr := range []string{
		"[1].all(x, request.flags.search)", "has(.request.flags.search) && .request.flags.search",
		"request.flags['search']", "[{'search': true}].exists(flags, flags.search)",
	} {
		t.Run("accept/"+expr, func(t *testing.T) {
			if _, problems := Compile([]Rule{{Hook: "tools/list.post", When: expr, Then: map[string]string{"decision": "'deny'"}}}); problems != nil {
				t.Fatalf("valid named selector did not compile: %v", problems)
			}
		})
	}
}

func TestCodexV3FixPromptReplyKeepsUpstreamIdentity(t *testing.T) { // V3F-G04
	for _, hook := range []string{"prompts/get.post.elicit", "prompts/get.post.progress"} {
		t.Run(hook, func(t *testing.T) {
			s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent},
				Catalog: Catalog{Prompts: []*mcp.Prompt{{Name: "original"}}},
				Rules: codexSet(t,
					Rule{Hook: "prompts/list.post", When: "true", Then: map[string]string{"prompt.name": "'public'"}},
					Rule{Hook: "server.session", When: "true", Then: map[string]string{"session.capabilities.elicitation": "true"}},
					Rule{Hook: hook, When: "prompt.upstream.name == 'original'", Then: map[string]string{"decision": "'deny'"}},
				),
			}
			e := &Engine{}
			r := reviewRequest("direct", s)
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

func TestCodexV3FixResourceHooksKeepUpstreamFields(t *testing.T) { // V3F-G05
	for _, hook := range []string{"resources/read.pre", "resources/read.post.result", "resources/read.post.elicit", "resources/read.post.progress"} {
		t.Run(hook, func(t *testing.T) {
			e, r, p := codexV3ReadSetup(t,
				Rule{Hook: "resources/list.post", When: "true", Then: map[string]string{"resource.title": "'Public'"}},
				Rule{Hook: "server.session", When: "true", Then: map[string]string{"session.capabilities.elicitation": "true"}},
				Rule{Hook: hook, When: "resource.upstream.title == 'Original'", Then: map[string]string{"decision": "'deny'"}},
			)
			r.Servers[0].Catalog.Resources = []*mcp.Resource{{Name: "secret", URI: p.URI, Title: "Original"}}
			first := e.ReadResource(context.Background(), r, p)
			if hook == "resources/read.pre" {
				if first.Error == nil || first.Forward != nil {
					t.Fatalf("upstream-title deny must survive resources/list.post: error=%v forward=%v", first.Error, first.Forward != nil)
				}
				return
			}
			if first.Forward == nil {
				t.Fatal("setup: resource did not forward")
			}
			if hook == "resources/read.post.progress" {
				u := &upstreamSeq{events: []UpstreamEvent{
					progressEvent(`{"progressToken":"p","progress":1,"message":"sensitive"}`),
					resultEvent(`{"contents":[]}`),
				}}
				got := collect(e.ReadReplyStream(context.Background(), r, first.Call, p, u.seq()))
				if len(got.notes) != 0 || got.final == nil || got.final.Error != nil {
					t.Fatalf("upstream-title deny must drop progress only: notes=%d final=%v", len(got.notes), got.final != nil)
				}
				return
			}
			raw := json.RawMessage(`{"contents":[{"uri":"file:///secret.txt","text":"sensitive"}]}`)
			if hook == "resources/read.post.elicit" {
				raw = json.RawMessage(`{"resultType":"input_required","inputRequests":{"q":{"method":"elicitation/create","params":{"mode":"form","message":"sensitive","requestedSchema":{"type":"object","properties":{"answer":{"type":"string"}}}}}}}`)
			}
			out := e.UpstreamReadReply(context.Background(), r, first.Call, p, raw)
			if out.Error == nil || out.Result != nil {
				t.Fatalf("upstream-title deny must refuse the reply after resources/list.post: error=%v result=%T", out.Error, out.Result)
			}
		})
	}
}

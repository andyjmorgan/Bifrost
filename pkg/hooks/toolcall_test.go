package hooks

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// tools/call: what each hook reads, how arguments are decoded and checked,
// and what a call rule does to them.

// After a rename of the tool and of a parameter, with a default, a hidden pin
// and a pre rule that adds an argument, every reply hook reads the client's
// names on one side and the server's on the other. That holds on the first
// round and on the round after the server asked its own question, where the
// request arrives again with a request state.
func TestEveryToolHookReadsBothSidesBeforeAndAfterAServersQuestion(t *testing.T) {
	ctx := context.Background()
	sides := "tool.name + '<' + tool.upstream.name + ' ' + request.params.name + '<' + request.upstream.params.name" +
		" + ' client=' + request.params.arguments.query + '/' + string(has(request.params.arguments.cql)) + '/' + string(has(request.params.arguments.space))" +
		" + ' server=' + request.upstream.params.arguments.cql + '/' + request.upstream.params.arguments.space + '/' + string(request.upstream.params.arguments.limit) + '/' + string(has(request.upstream.params.arguments.query))" +
		" + ' params=' + string('query' in tool.params) + string('cql' in tool.params) + ' up=' + string('cql' in tool.upstream.inputSchema.properties) + string('query' in tool.upstream.inputSchema.properties)" +
		" + ' desc=' + tool.description + '<' + tool.upstream.description"
	want := "baz<foo baz<foo client=q+later/false/false server=q+later/ENG/20/false params=truefalse up=truefalse desc=gateway words<server words"
	cat := Catalog{Tools: []*mcp.Tool{{Name: "foo", Description: "server words", InputSchema: map[string]any{"type": "object", "properties": map[string]any{
		"cql": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}, "space": map[string]any{"type": "string"}, "note": map[string]any{"type": "string"}}}}}}
	s := newServer(t, cat,
		Rule{Hook: "tools/list.post", When: "tool.upstream.name == 'foo'", Then: map[string]string{"tool.name": "'baz'", "tool.params.cql.name": "'query'", "tool.params.limit.default": "20", "tool.params.space.pin": "'ENG'", "tool.params.space.hide": "true", "tool.description": "'gateway words'"}},
		Rule{Hook: "tools/call.pre", When: "tool.name == 'baz' && tool.upstream.name == 'foo' && request.params.name == 'baz'", Then: map[string]string{"request.params.arguments.note": "'pre'"}},
		// A later rule sees the earlier rule's change.
		Rule{Hook: "tools/call.pre", When: "request.params.arguments.note == 'pre'", Then: map[string]string{"request.params.arguments.query": "request.params.arguments.query + '+later'"}},
		Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{KeyConfirm: "elicit('sure', 'Really?')"}},
		Rule{Hook: "tools/call.post.progress", When: "true", Then: map[string]string{"progress.params.message": sides}},
		Rule{Hook: "tools/call.post.progress", When: "true", Then: map[string]string{"progress.params.message": "progress.params.message + ' | server said ' + progress.upstream.params.message"}},
		Rule{Hook: "tools/call.post.elicit", When: "true", Then: map[string]string{"elicitation.params.message": sides}},
		Rule{Hook: "tools/call.post.elicit", When: "true", Then: map[string]string{"elicitation.params.message": "elicitation.params.message + ' | server said ' + elicitation.upstream.params.message"}},
		Rule{Hook: "tools/call.post.result", When: "true", Then: map[string]string{"response.result.content": "[{'type': 'text', 'text': " + sides + " + ' | server said ' + response.upstream.result.content[0].text}]"}},
	)
	e, r := &Engine{}, newRequest("direct", s)
	for name, args := range map[string]string{"foo": `{"query":"q"}`, "baz": `{"cql":"q"}`} {
		if o := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: name, Arguments: json.RawMessage(args)}); o.Forward != nil || o.Error == nil {
			t.Errorf("a call to %s with %s was not refused: %s", name, args, outcomeText(o))
		}
	}
	args := json.RawMessage(`{"query":"q"}`)
	asked := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "baz", Arguments: args})
	ir, ok := asked.Result.(InputRequired)
	if !ok {
		t.Fatalf("the gateway did not ask: %s", outcomeText(asked))
	}
	p2 := &mcp.CallToolParamsRaw{Name: "baz", Arguments: args, RequestState: ir.RequestState, InputResponses: yesTo("sure")}
	o2 := e.CallTool(ctx, r, p2)
	if o2.Forward == nil {
		t.Fatalf("the confirmed call was not forwarded: %s", outcomeText(o2))
	}
	if got := asJSON(o2.Forward.Params); !strings.Contains(got, `"name":"foo"`) || !strings.Contains(got, `"cql":"q+later"`) || !strings.Contains(got, `"space":"ENG"`) || !strings.Contains(got, `"limit":20`) {
		t.Errorf("forwarded %s", got)
	}
	first := collect(e.ToolReplyStream(ctx, r, o2.Call, p2, (&upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progress":1,"message":"half"}`), resultEvent(serverQuestion)}}).seq()))
	if len(first.notes) != 1 || !strings.Contains(plainJSON(first.notes[0].Params), want+" | server said half") {
		t.Errorf("progress on the first round: %s", asJSON(first.notes))
	}
	if got := plainJSON(first.final.Result); !strings.Contains(got, want+" | server said which?") {
		t.Errorf("elicit hook on the first round: %s", got)
	}
	ir3, ok := first.final.Result.(InputRequired)
	if !ok {
		t.Fatalf("the server's question did not reach the client: %s", outcomeText(*first.final))
	}
	p3 := &mcp.CallToolParamsRaw{Name: "baz", Arguments: args, RequestState: ir3.RequestState, InputResponses: serverAnswer()}
	o3 := e.CallTool(ctx, r, p3)
	if o3.Forward == nil {
		t.Fatalf("the answer to the server's question was not forwarded: %s", outcomeText(o3))
	}
	second := collect(e.ToolReplyStream(ctx, r, o3.Call, p3, (&upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progress":2,"message":"done"}`), resultEvent(`{"content":[{"type":"text","text":"ok"}]}`)}}).seq()))
	if len(second.notes) != 1 || !strings.Contains(plainJSON(second.notes[0].Params), want+" | server said done") {
		t.Errorf("progress after the continuation: %s", asJSON(second.notes))
	}
	if got := plainJSON(second.final.Result); !strings.Contains(got, want+" | server said ok") {
		t.Errorf("result hook after the continuation: %s", got)
	}
}

// After a list rule renames a tool, the result, elicit and progress hooks
// read the new name as tool.name and the server's as tool.upstream.name. A
// deny written against either refuses the result or the question as a tool
// error, and drops a progress event.
func TestToolReplyHooksReadTheExposedNameAndTheUpstreamName(t *testing.T) {
	for _, hook := range []string{"tools/call.post.result", "tools/call.post.elicit", "tools/call.post.progress"} {
		for _, exposed := range []bool{false, true} {
			name := hook + "/upstream_control"
			when := "tool.upstream.name == 't'"
			if exposed {
				name = hook + "/exposed_name"
				when = "tool.name == 'public' && tool.upstream.name == 't'"
			}
			t.Run(name, func(t *testing.T) {
				c := newStreamCase(t, "direct", nil, Limits{})
				c.r.Servers[0].Rules = mustCompileUncapped(t,
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

// Arguments are one JSON document. A second document after the first is
// refused, not ignored.
func TestArgumentsFollowedByMoreJSONAreRefused(t *testing.T) {
	for _, raw := range []string{`{} {"admin":true}`, `{} []`, `{} null`} {
		if got, err := decodeArgs(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted multiple JSON documents %q as %v", raw, got)
		}
	}
}

// decodeArgs never accepts text that is not valid JSON. Fuzz targets are
// bounded to short inputs, and their seeds run with the ordinary tests.
func FuzzDecodeArgs(f *testing.F) {
	for _, s := range []string{`{}`, `{"x":1}`, `{"x":1,"x":2}`, `{} {}`, `{"id":9007199254740993}`, `{"a":[{"b":true}]}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 8192 {
			t.Skip()
		}
		_, err := decodeArgs(json.RawMessage(s))
		trim := strings.TrimSpace(s)
		if err == nil && trim != "" && !json.Valid([]byte(s)) {
			t.Fatalf("accepted invalid JSON: %q", s)
		}
	})
}

// additionalProperties: false still refuses an unknown argument when the
// schema also has an allOf with an empty member.
func TestAClosedSchemaStaysClosedBesideAnEmptyAllOf(t *testing.T) {
	tool := &mcp.Tool{Name: "t", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}},
		"additionalProperties": false, "allOf": []any{map[string]any{}},
	}}
	s := &Server{Slug: "a", Rules: mustCompileUncapped(t), Catalog: Catalog{Tools: []*mcp.Tool{tool}}}
	e := &Engine{}
	r := Request{Route: "direct", Servers: []*Server{s}}
	control := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"ok"}`)})
	if control.Forward == nil {
		t.Fatalf("valid control did not forward: %+v", control)
	}
	out := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"ok","extra":"must not forward"}`)})
	if out.Forward != nil || out.Error == nil || out.Error.Code != CodeInvalidParams {
		t.Fatalf("allOf:[{}] disabled explicit additionalProperties:false: %+v", out)
	}
}

// An escaped dot in a `then` key is part of the name:
// request.params.arguments.a\.b sets the argument "a.b" and leaves the b
// inside a alone.
func TestAnEscapedDotInAnArgumentKeyNamesOneArgument(t *testing.T) {
	s := newServer(t, anyArgsTool, Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{`request.params.arguments.a\.b`: "'patched'"}})
	o := (&Engine{}).CallTool(context.Background(), newRequest("direct", s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"a":{"b":"nested"}}`)})
	if o.Forward == nil || !strings.Contains(asJSON(o.Forward.Params), `"a.b":"patched"`) || !strings.Contains(asJSON(o.Forward.Params), `"a":{"b":"nested"}`) {
		t.Errorf("escaped argument key landed elsewhere: %s", asJSON(o.Forward))
	}
}

// An audit rule changes nothing, so it cannot be what supplies a required
// argument that a list rule hid. The call is not forwarded without the
// argument.
func TestAnAuditRuleCannotSupplyAHiddenRequiredArgument(t *testing.T) {
	e, r := serverRequest(t,
		Rule{Hook: "tools/list.post", When: "true", Then: map[string]string{"tool.params.tenant.hide": "true"}},
		Rule{Mode: "audit", Hook: "tools/call.pre", When: "true", Then: map[string]string{"request.params.arguments.tenant": "'A'"}},
	)
	r.Servers[0].Catalog.Tools[0].InputSchema.(map[string]any)["required"] = []any{"tenant"}
	out := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t"})
	if out.Forward != nil {
		t.Fatalf("audit patch satisfied hidden-required validation without supplying tenant: %+v", out.Forward)
	}
}

// The realistic policy has exact, predictable effects on a call: a limit over
// the exposed maximum is refused before the call rules run, a limit within it
// is forwarded as sent, and env "prod" is refused for a member.
func TestTheRealisticPolicyGivesExactCallOutcomes(t *testing.T) {
	set, probs := Compile(realisticRules())
	if probs != nil {
		t.Fatal(probs)
	}
	e := &Engine{}
	cat := []*mcp.Tool{{Name: "search", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"limit": map[string]any{"type": "integer"}, "env": map[string]any{"type": "string"}, "q": map[string]any{"type": "string"}}}}}
	s := &Server{Slug: "h", Rules: set, Catalog: Catalog{Tools: cat}}
	r := Request{Route: "direct", Servers: []*Server{s}, Caller: map[string]any{"userId": "u", "role": "member"}}
	cases := []struct {
		args string
		want string // "forward:<limit>", "refused" for a deny result, or "error"
	}{
		{`{"q":"a","limit":250}`, "error"},     // over the exposed maximum: refused before call rules run
		{`{"q":"a","limit":20}`, "forward:20"}, // within it: forwarded as sent
		{`{"q":"a","env":"prod"}`, "refused"},
		{`{"q":"a","env":"staging","limit":100}`, "forward:100"},
	}
	for _, c := range cases {
		out := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "search", Arguments: json.RawMessage(c.args)})
		got := "error"
		switch {
		case out.Forward != nil:
			args := out.Forward.Params.(*mcp.CallToolParams).Arguments.(map[string]any)
			got = "forward"
			if l, ok := args["limit"]; ok {
				b, _ := json.Marshal(l)
				got += ":" + string(b)
			}
		case out.Result != nil:
			got = "refused"
		}
		if got != c.want {
			t.Errorf("%s: got %s want %s (%+v)", c.args, got, c.want, out.Error)
		}
	}
}

// TestRegexReplaceOnLargeNotebook rewrites "datadog" to "Bits" across a 1 MB
// create_datadog_notebook call (16 markdown cells, 100 occurrences) in a
// tools/call rule, and reports time, allocation and outcome.
func TestRegexReplaceOnLargeNotebook(t *testing.T) {
	tool := &mcp.Tool{Name: "create_datadog_notebook", InputSchema: map[string]any{"type": "object"}}
	if all, err := loadToolRef("../../internal/e2e/testdata/real", "datadog.json", 0); err == nil {
		for _, x := range all {
			if x.Name == "create_datadog_notebook" {
				tool = x
			}
		}
	}
	const cells, total, hits = 16, 1 << 20, 100
	filler := strings.Repeat("Latency is fine across the fleet. ", total/cells/34)
	var cs []any
	n := 0
	for i := 0; i < cells; i++ {
		var b strings.Builder
		per := hits / cells
		if i < hits%cells {
			per++
		}
		chunk := len(filler) / (per + 1)
		for j := 0; j <= per; j++ {
			b.WriteString(filler[j*chunk : (j+1)*chunk])
			if j < per {
				b.WriteString(" Datadog ")
				n++
			}
		}
		cs = append(cs, map[string]any{"type": "notebook_cells", "attributes": map[string]any{"definition": map[string]any{"type": "markdown", "text": b.String()}}})
	}
	args, _ := json.Marshal(map[string]any{"name": "Incident review", "cells": cs})
	if n != hits {
		t.Fatalf("built %d occurrences", n)
	}
	rules := []Rule{
		{Hook: "tools/list.post", When: "'telemetry' in tool.params", Then: map[string]string{"tool.params.telemetry.hide": "true", "tool.params.telemetry.pin": "{'intent': 'mind your own business'}"}},
		{Hook: "tools/call.pre", When: "tool.name == 'create_datadog_notebook'", Then: map[string]string{
			"request.params.arguments.cells": "request.params.arguments.cells.map(c, {'type': c.type, 'attributes': {'definition': {'type': 'markdown', 'text': regex.replace(c.attributes.definition.text, '(?i)datadog', 'Bits')}}})"}},
	}
	// The default CEL cost limit (1,000,000) refuses this call: regex.replace
	// is charged by input size. A 1 MB rewrite needs a higher limit.
	if def, _ := Compile(rules); def != nil {
		e := &Engine{}
		s := &Server{Slug: "dd", Rules: def, Catalog: Catalog{Tools: []*mcp.Tool{tool}}}
		out := e.CallTool(context.Background(), Request{Route: "direct", Servers: []*Server{s}, Caller: map[string]any{"userId": "u"}}, &mcp.CallToolParamsRaw{Name: "create_datadog_notebook", Arguments: args})
		if out.Forward != nil {
			t.Fatal("default cost limit forwarded a 1 MB rewrite; expected refusal")
		}
	}
	set, probs := Compile(rules, Options{CostLimit: 50_000_000})
	if probs != nil {
		t.Fatal(probs)
	}
	e := &Engine{}
	s := &Server{Slug: "dd", Rules: set, Catalog: Catalog{Tools: []*mcp.Tool{tool}}}
	r := Request{Route: "direct", Servers: []*Server{s}, Caller: map[string]any{"userId": "u"}}
	var ms0, ms1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms0)
	t0 := time.Now()
	out := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "create_datadog_notebook", Arguments: args})
	d := time.Since(t0)
	runtime.ReadMemStats(&ms1)
	t.Logf("args %d bytes; call took %v, allocated %.1f MiB in %d allocations", len(args), d, float64(ms1.TotalAlloc-ms0.TotalAlloc)/(1<<20), ms1.Mallocs-ms0.Mallocs)
	if out.Forward == nil {
		t.Fatalf("not forwarded: error=%+v result=%+v hits=%+v", out.Error, out.Result, out.Hits)
	}
	fwd, _ := json.Marshal(out.Forward.Params.(*mcp.CallToolParams).Arguments)
	if c := strings.Count(strings.ToLower(string(fwd)), "datadog"); c != 0 {
		t.Fatalf("%d occurrences of datadog left", c)
	}
	if c := strings.Count(string(fwd), "Bits"); c != hits {
		t.Fatalf("%d replacements, want %d", c, hits)
	}
	if !strings.Contains(string(fwd), `"telemetry":{"intent":"mind your own business"}`) {
		t.Fatal("telemetry pin missing")
	}
}

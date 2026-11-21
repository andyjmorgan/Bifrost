package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The state behind a request-state key: what it is bound to, and how often it
// can be used.

// jsonStates keeps state as JSON, as the gateway's own store does, so a
// test sees what survives being written and read back.
type jsonStates struct{ m map[string][]byte }

func (s *jsonStates) Put(key string, st State, _ time.Duration) {
	b, _ := json.Marshal(st)
	s.m[key] = b
}

func (s *jsonStates) Take(key string) (State, bool) {
	b, ok := s.m[key]
	delete(s.m, key)
	var st State
	if !ok || json.Unmarshal(b, &st) != nil {
		return State{}, false
	}
	return st, true
}

// Every state key works once: the key of each of two gateway questions, and
// the key around the server's own question. That holds when the state is
// written out as JSON and read back, as the gateway's store does, with an
// argument an earlier rule set as a nested map. The same is true on
// prompts/get, resources/read and a directory read.
func TestEveryStateKeyWorksOnce(t *testing.T) {
	ctx := context.Background()
	args := json.RawMessage(`{"q":"x","n":1,"m":{"a":[1,2.5]}}`)
	s := newServer(t, anyArgsTool,
		Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{"request.params.arguments.added": "{'k': [1, 2.5, 'x'], 'n': 3}"}},
		Rule{Hook: "tools/call.pre", When: "true", Then: confirmThen("a", "A?")},
		Rule{Hook: "tools/call.pre", When: "true", Then: confirmThen("b", "B?")})
	e, r := &Engine{States: &jsonStates{m: map[string][]byte{}}}, newRequest("direct", s)
	call := func(state string, in mcp.InputResponseMap) (Outcome, *mcp.CallToolParamsRaw) {
		p := &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: state, InputResponses: in}
		return e.CallTool(ctx, r, p), p
	}
	o1, _ := call("", nil)
	k1 := requestState(o1)
	o2, _ := call(k1, yesTo("a"))
	k2 := requestState(o2)
	if k1 == "" || k2 == "" || k1 == k2 {
		t.Fatalf("the two questions were not asked in turn: %s / %s", outcomeText(o1), outcomeText(o2))
	}
	if o, _ := call(k1, yesTo("b")); o.Forward != nil {
		t.Error("the first key was taken a second time")
	}
	o3, p3 := call(k2, yesTo("b"))
	if o3.Call == nil {
		t.Fatalf("two confirmations did not forward the call: %s", outcomeText(o3))
	}
	k3 := requestState(e.UpstreamToolReply(ctx, r, o3.Call, p3, json.RawMessage(serverQuestion)))
	o4, _ := call(k3, serverAnswer())
	if o4.Forward == nil || !strings.Contains(asJSON(o4.Forward.Params), `"requestState":"UP"`) {
		t.Fatalf("the answer to the server's question was not forwarded with the server's state: %s", outcomeText(o4))
	}
	for i, k := range []string{k1, k2, k3} {
		if o, _ := call(k, serverAnswer()); o.Forward != nil {
			t.Errorf("key %d forwarded the call again", i+1)
		}
		if o, _ := call(k, nil); o.Forward != nil {
			t.Errorf("key %d forwarded the call again with no answer", i+1)
		}
	}

	cat := Catalog{Prompts: []*mcp.Prompt{{Name: "p", Arguments: []*mcp.PromptArgument{{Name: "tone"}}}}, Resources: []*mcp.Resource{{URI: "docs://a", Name: "a"}, {URI: "docs://dir/", Name: "d"}}}
	s = newServer(t, cat,
		Rule{Hook: "prompts/get.pre", When: "true", Then: confirmThen("sure", "Really?")},
		Rule{Hook: "resources/read.pre", When: "true", Then: confirmThen("sure", "Really?")})
	e, r = &Engine{}, newRequest("direct", s)
	pa := map[string]string{"tone": "x"}
	kp := requestState(e.GetPrompt(ctx, r, &mcp.GetPromptParams{Name: "p", Arguments: pa}))
	pp := &mcp.GetPromptParams{Name: "p", Arguments: pa, RequestState: kp, InputResponses: yesTo("sure")}
	op := e.GetPrompt(ctx, r, pp)
	if op.Call == nil {
		t.Fatalf("a confirmed prompts/get was not forwarded: %s", outcomeText(op))
	}
	if o := e.GetPrompt(ctx, r, pp); o.Forward != nil {
		t.Error("prompts/get: the key was taken twice")
	}
	ku := requestState(e.UpstreamPromptReply(ctx, r, op.Call, pp, json.RawMessage(serverQuestion)))
	n := 0
	for i := 0; i < 3; i++ {
		if o := e.GetPrompt(ctx, r, &mcp.GetPromptParams{Name: "p", Arguments: pa, RequestState: ku, InputResponses: serverAnswer()}); o.Forward != nil {
			n++
		}
	}
	if n != 1 {
		t.Errorf("prompts/get: the key around the server's question forwarded %d times, want 1", n)
	}
	for _, dir := range []bool{false, true} {
		uri, read := "docs://a", e.ReadResource
		if dir {
			uri, read = "docs://dir/", e.ReadDirectory
		}
		k := requestState(read(ctx, r, &mcp.ReadResourceParams{URI: uri}))
		pr := &mcp.ReadResourceParams{URI: uri, RequestState: k, InputResponses: yesTo("sure")}
		or := read(ctx, r, pr)
		if or.Call == nil {
			t.Fatalf("a confirmed read of %s was not forwarded: %s", uri, outcomeText(or))
		}
		if o := read(ctx, r, pr); o.Forward != nil {
			t.Errorf("%s: the key was taken twice", uri)
		}
		if dir {
			continue
		}
		ku := requestState(e.UpstreamReadReply(ctx, r, or.Call, pr, json.RawMessage(serverQuestion)))
		n := 0
		for i := 0; i < 3; i++ {
			if o := read(ctx, r, &mcp.ReadResourceParams{URI: uri, RequestState: ku, InputResponses: serverAnswer()}); o.Forward != nil {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s: the key around the server's question forwarded %d times, want 1", uri, n)
		}
	}
}

// The request state a client holds is an opaque 128-bit key. Thirty-two
// answers that present it at once forward one call and refuse the rest.
func TestAStateKeyIsOpaqueAndRedeemedOnce(t *testing.T) {
	c := newStreamCase(t, "direct", nil, Limits{})
	c.r.Principal = "opaque-principal"
	c.call.Server.Rules = mustCompileUncapped(t, Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{"confirm": "elicit('ok','Proceed?')"}})
	first := c.e.CallTool(context.Background(), c.r, c.params)
	ir, ok := first.Result.(InputRequired)
	if !ok {
		t.Fatalf("setup: no challenge: %+v", first)
	}
	if !strings.HasPrefix(ir.RequestState, stateKeyPrefix) || len(ir.RequestState) != 34 {
		t.Fatalf("not a 128-bit opaque key: %q", ir.RequestState)
	}
	var accepted, refused atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := *c.params
			p.RequestState = ir.RequestState
			p.InputResponses = yesTo("ok")
			o := c.e.CallTool(context.Background(), c.r, &p)
			if o.Forward != nil {
				accepted.Add(1)
			} else if o.Error != nil {
				refused.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 || refused.Load() != 31 {
		t.Fatalf("accepted=%d refused=%d", accepted.Load(), refused.Load())
	}
}

// Confirmation state is bound to the server it was issued under. The same
// user on a server of the same slug, target and generation in another
// organization cannot answer it.
func TestStateIsBoundToTheServer(t *testing.T) {
	e, r := serverRequest(t, Rule{Hook: "tools/call.pre", When: "true", Then: confirmThen("ok", "Continue?")})
	first := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t"})
	ir, ok := first.Result.(InputRequired)
	if !ok {
		t.Fatalf("initial confirmation: %+v", first)
	}
	// The same user holds a same-slug server in another organization.
	// Server IDs differ; target and generation deliberately do not.
	s2 := *r.Servers[0]
	s2.ID = "server-b"
	r.Servers = []*Server{&s2}
	r.Caller = map[string]any{"userId": "u", "orgId": "org-b"}
	out := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", RequestState: ir.RequestState, InputResponses: yesTo("ok")})
	if out.Forward != nil || out.Error == nil {
		t.Fatalf("state crossed server identities: %+v", out)
	}
}

// State issued under one generation of a server's rules is refused under the
// next, with a message that says the policy is older.
func TestStateFromAnOlderGenerationIsRefused(t *testing.T) {
	e, r := serverRequest(t, Rule{Hook: "tools/call.pre", When: "true", Then: confirmThen("ok", "Continue?")})
	first := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t"})
	ir, ok := first.Result.(InputRequired)
	if !ok {
		t.Fatalf("initial confirmation: %+v", first)
	}
	r.Servers[0].Generation = "2"
	out := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", RequestState: ir.RequestState, InputResponses: yesTo("ok")})
	if out.Forward != nil || out.Error == nil || !strings.Contains(out.Error.Message, "older policy") {
		t.Fatalf("state from an older generation was not refused as older policy: %+v", out)
	}
}

// An engine with nothing configured does not take state a client wrote. There
// is no signing key to leave out: state is kept in the store and the client
// holds only a key the store issued.
func TestAnEngineWithNothingConfiguredTakesNoStateAClientWrote(t *testing.T) {
	s := newServer(t, oneTool(), Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{KeyConfirm: "elicit('sure', 'Really?')"}})
	forged := "eyJtIjoidG9vbHMvY2FsbCIsImEiOnsic3VyZSI6eyJhY3Rpb24iOiJhY2NlcHQifX19."
	o := (&Engine{}).CallTool(context.Background(), newRequest("direct", s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"x"}`), RequestState: forged})
	if o.Forward != nil {
		t.Error("a client wrote its own confirmation and the call was forwarded")
	}
}

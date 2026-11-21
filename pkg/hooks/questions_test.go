package hooks

import (
	"context"
	"encoding/json"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Confirmations the gateway asks for, and questions a server asks: what an
// answer covers, who may give it, and how often it counts.

// A gateway confirmation is satisfied only through its own state: not by
// input responses alone, not for other arguments, not by another principal,
// not twice, and not by anything but accept with the box ticked.
func TestAConfirmationNeedsItsOwnState(t *testing.T) {
	ctx := context.Background()
	s := newServer(t, oneTool(), Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{KeyConfirm: "elicit('sure', 'Really?')"}})
	e, r := &Engine{}, newRequest("direct", s)
	args := json.RawMessage(`{"q":"x"}`)
	ask := func() string {
		t.Helper()
		ir, ok := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args}).Result.(InputRequired)
		if !ok {
			t.Fatal("the gateway did not ask")
		}
		return ir.RequestState
	}
	refused := func(what string, o Outcome) {
		t.Helper()
		if o.Forward != nil {
			t.Errorf("%s: the call was forwarded", what)
		}
	}
	refused("input responses with no state", e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, InputResponses: yesTo("sure")}))
	refused("a made-up state key", e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: "c_00000000000000000000000000000000", InputResponses: yesTo("sure")}))
	refused("the state with no answer", e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: ask()}))
	refused("other arguments", e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"y"}`), RequestState: ask(), InputResponses: yesTo("sure")}))
	other := newRequest("direct", s)
	other.Principal = "someone-else"
	state := ask()
	refused("another principal", e.CallTool(ctx, other, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: state, InputResponses: yesTo("sure")}))
	refused("the owner, after another principal spent the state", e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: state, InputResponses: yesTo("sure")}))
	for _, ans := range []*mcp.ElicitResult{{Action: "decline"}, {Action: "accept"}, {Action: "accept", Content: map[string]any{"confirm": false}}, {Action: "accept", Content: map[string]any{"confirm": "true"}}, {Action: "ACCEPT", Content: map[string]any{"confirm": true}}} {
		refused("the answer "+asJSON(ans), e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: ask(), InputResponses: mcp.InputResponseMap{KeyPrefix + "sure": ans}}))
	}
	// State lives in the gateway's store. Anything a client writes for
	// itself is not a key the store issued.
	forged := "eyJtIjoidG9vbHMvY2FsbCIsImEiOnsic3VyZSI6eyJhY3Rpb24iOiJhY2NlcHQifX19.c2lnbmVk"
	refused("state the client wrote itself", e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: forged}))
	state = ask()
	p := &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: state, InputResponses: yesTo("sure")}
	if o := e.CallTool(ctx, r, p); o.Forward == nil {
		t.Fatalf("a confirmed call was not forwarded: %s", outcomeText(o))
	}
	refused("the same state a second time", e.CallTool(ctx, r, p))
}

// An answer counts only for the question it was given to: not for another
// tool, another server, another set of rules, an expired state, a key
// that was not asked, a second rule that uses the same key for other words,
// another method, or arguments with any other value. The same arguments with
// their keys in another order, with other white space, or with a number
// written another way (1.0 or 1e0 for 1) are the same arguments.
func TestAnAnswerIsForItsOwnQuestion(t *testing.T) {
	ctx := context.Background()
	args := json.RawMessage(`{"q":"x"}`)
	cat := Catalog{Tools: []*mcp.Tool{{Name: "t", InputSchema: qSchema}, {Name: "u", InputSchema: qSchema}},
		Prompts: []*mcp.Prompt{{Name: "t"}}, Resources: []*mcp.Resource{{URI: "docs://a", Name: "a"}}}
	rule := Rule{Hook: "tools/call.pre", When: "true", Then: confirmThen("sure", "Really?")}
	more := []Rule{rule, {Hook: "prompts/get.pre", When: "true", Then: confirmThen("sure", "Really?")}, {Hook: "resources/read.pre", When: "true", Then: confirmThen("sure", "Really?")}}
	s := newServer(t, cat, more...)
	e, r := &Engine{}, newRequest("direct", s)
	ask := func() string {
		t.Helper()
		k := requestState(e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args}))
		if k == "" {
			t.Fatal("the gateway did not ask")
		}
		return k
	}
	refused := func(what string, o Outcome) {
		t.Helper()
		if o.Forward != nil {
			t.Errorf("%s: forwarded", what)
		}
	}
	refused("another tool", e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "u", Arguments: args, RequestState: ask(), InputResponses: yesTo("sure")}))
	refused("a prompt of the same name", e.GetPrompt(ctx, r, &mcp.GetPromptParams{Name: "t", RequestState: ask(), InputResponses: yesTo("sure")}))
	refused("a read", e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: "docs://a", RequestState: ask(), InputResponses: yesTo("sure")}))
	kr := requestState(e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: "docs://a"}))
	refused("a read's answer on a directory read", e.ReadDirectory(ctx, r, &mcp.ReadResourceParams{URI: "docs://a", RequestState: kr, InputResponses: yesTo("sure")}))
	s2 := newServer(t, cat, more...)
	s2.Slug = "b"
	mux := newRequest("direct", s)
	mux.Route, mux.Servers = "mux", []*Server{s, s2}
	km := requestState(e.CallTool(ctx, mux, &mcp.CallToolParamsRaw{Name: "a__t", Arguments: args}))
	refused("another server", e.CallTool(ctx, mux, &mcp.CallToolParamsRaw{Name: "b__t", Arguments: args, RequestState: km, InputResponses: yesTo("sure")}))
	changed := newServer(t, cat, rule, Rule{Hook: "tools/call.pre", When: "false", Then: map[string]string{KeyDecision: "'deny'"}})
	refused("other rules", e.CallTool(ctx, newRequest("direct", changed), &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: ask(), InputResponses: yesTo("sure")}))
	late := newRequest("direct", s)
	late.Now = time.Now().Add(StateTTL + time.Minute)
	refused("an expired state", e.CallTool(ctx, late, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: ask(), InputResponses: yesTo("sure")}))
	both := yesTo("sure")
	both[KeyPrefix+"other"] = both[KeyPrefix+"sure"]
	refused("an answer to a key that was not asked", e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: ask(), InputResponses: both}))

	// One key, two questions: the answer to the first is not an answer to
	// the second.
	two := newServer(t, oneTool(),
		Rule{Hook: "tools/call.pre", When: "true", Then: confirmThen("sure", "Really?")},
		Rule{Hook: "tools/call.pre", When: "true", Then: confirmThen("sure", "Delete everything?")})
	r2 := newRequest("direct", two)
	k := requestState(e.CallTool(ctx, r2, &mcp.CallToolParamsRaw{Name: "t", Arguments: args}))
	refused("the same key for other words", e.CallTool(ctx, r2, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: k, InputResponses: yesTo("sure")}))

	// The arguments in other encodings.
	enc := newServer(t, anyArgsTool, rule)
	re := newRequest("direct", enc)
	base := `{"n":1,"m":{"a":[1,2,{"z":null}],"b":"x"},"big":12345678901234567890}`
	for alt, same := range map[string]bool{
		`{"m":{"b":"x","a":[1,2,{"z":null}]},"big":12345678901234567890,"n":1}`:                                 true,
		` { "n" : 1 , "m" : { "a" : [ 1 , 2 , { "z" : null } ] , "b" : "x" } , "big" : 12345678901234567890 } `: true,
		`{"n":1,"m":{"a":[1,2,{"z":null}],"b":"x"},"big":12345678901234567890}`:                                 true,
		`{"n":1.0,"m":{"a":[1,2,{"z":null}],"b":"x"},"big":12345678901234567890}`:                               true, // the same number, written another way
		`{"n":1e0,"m":{"a":[1,2,{"z":null}],"b":"x"},"big":12345678901234567890}`:                               true,
		`{"n":"1","m":{"a":[1,2,{"z":null}],"b":"x"},"big":12345678901234567890}`:                               false,
		`{"n":1,"m":{"a":[1,2,{"z":null}],"b":"x"},"big":12345678901234567891}`:                                 false,
		`{"n":1,"m":{"a":[2,1,{"z":null}],"b":"x"},"big":12345678901234567890}`:                                 false,
		`{"n":1,"m":{"a":[1,2,{}],"b":"x"},"big":12345678901234567890}`:                                         false,
	} {
		k := requestState(e.CallTool(ctx, re, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(base)}))
		o := e.CallTool(ctx, re, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(alt), RequestState: k, InputResponses: yesTo("sure")})
		if (o.Forward != nil) != same {
			t.Errorf("%s: forwarded=%v, want %v", alt, o.Forward != nil, same)
		}
	}
}

// What a question shows is covered, wherever the rule took it from: a value
// read straight from _meta in the question, with the argument set by a later
// rule, and a value cut to 80 characters in the question and set in full by
// the same rule. An audit rule that confirms never asks.
func TestAQuestionCoversWhatItShows(t *testing.T) {
	ctx := context.Background()
	cat := Catalog{Tools: []*mcp.Tool{{Name: "deploy", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"env": map[string]any{"type": "string"}}}}}}
	swap := func(what, a, b string, rules ...Rule) {
		t.Helper()
		e, r := &Engine{}, newRequest("direct", newServer(t, cat, rules...))
		k := requestState(e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "deploy", Arguments: json.RawMessage(`{}`), Meta: mcp.Meta{"env": a}}))
		if k == "" {
			t.Fatalf("%s: the gateway did not ask", what)
		}
		o := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "deploy", Arguments: json.RawMessage(`{}`), Meta: mcp.Meta{"env": b}, RequestState: k, InputResponses: yesTo("sure")})
		if o.Forward != nil {
			t.Errorf("%s: asked about %q and forwarded %s", what, a, asJSON(o.Forward.Params))
		}
	}
	swap("shown from _meta, set later", "staging", "production",
		Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{KeyConfirm: "elicit('sure', 'Deploy to {env}?', {'env': request.params.?_meta.?env.orValue('staging')})"}},
		Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{"request.params.arguments.env": "request.params.?_meta.?env.orValue('staging')"}})
	long := strings.Repeat("a", 80)
	swap("shown cut to 80 characters", long+"-staging", long+"-production",
		Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{KeyConfirm: "elicit('sure', 'Deploy to {env}?', {'env': request.params._meta.env})", "request.params.arguments.env": "request.params._meta.env"}})

	s := newServer(t, oneTool(), Rule{Hook: "tools/call.pre", Mode: ModeAudit, When: "true", Then: confirmThen("a", "A?")})
	o := (&Engine{}).CallTool(ctx, newRequest("direct", s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"x"}`)})
	if o.Forward == nil || len(o.Hits) != 1 || o.Hits[0].Effect != "would elicit" {
		t.Errorf("an audit rule that confirms: forwarded=%v hits=%s", o.Forward != nil, asJSON(o.Hits))
	}
}

// A confirmation covers the arguments as a pre rule set them before the
// question, not only the ones the client sent. A rule that takes an argument
// from the request's _meta or headers cannot be asked about one value and
// forward another.
func TestAConfirmationCoversArgumentsARuleSet(t *testing.T) {
	ctx := context.Background()
	cat := Catalog{Tools: []*mcp.Tool{{Name: "deploy", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"env": map[string]any{"type": "string"}}}}}}
	s := newServer(t, cat,
		Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{"request.params.arguments.env": "request.params.?_meta.?env.orValue('staging')"}},
		Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{KeyConfirm: "elicit('sure', 'Deploy to {env}?', {'env': request.params.arguments.env})"}})
	e, r := &Engine{}, newRequest("direct", s)
	asked := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "deploy", Arguments: json.RawMessage(`{}`), Meta: mcp.Meta{"env": "staging"}})
	ir := asked.Result.(InputRequired)
	if !strings.Contains(outcomeText(asked), `Deploy to \"staging\"?`) {
		t.Fatalf("asked %s", outcomeText(asked))
	}
	o := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "deploy", Arguments: json.RawMessage(`{}`), Meta: mcp.Meta{"env": "production"}, RequestState: ir.RequestState, InputResponses: yesTo("sure")})
	if o.Forward != nil && strings.Contains(asJSON(o.Forward.Params), `"env":"production"`) {
		t.Errorf("the user confirmed staging and the gateway forwarded %s", asJSON(o.Forward.Params))
	}
}

// A confirmation is bound to the exact numbers in the arguments. Two numbers
// that differ only beyond what a float64 holds are different arguments, so a
// yes to one does not forward the other.
func TestAConfirmationTellsApartNumbersThatRoundToTheSameFloat(t *testing.T) {
	for _, pair := range [][2]string{{"9007199254740992.0", "9007199254740993.0"}, {"0.100000000000000001", "0.100000000000000002"}, {"1e-400", "2e-400"}} {
		t.Run(pair[0], func(t *testing.T) {
			a, _ := new(big.Rat).SetString(pair[0])
			b, _ := new(big.Rat).SetString(pair[1])
			if a.Cmp(b) == 0 {
				t.Fatal("test requires distinct exact numbers")
			}
			s := newServer(t, anyArgsTool, Rule{Hook: "tools/call.pre", When: "true", Then: confirmThen("sure", "Proceed?")})
			e, r := &Engine{}, newRequest("direct", s)
			arg := func(n string) json.RawMessage { return json.RawMessage(`{"nested":{"values":[` + n + `]}}`) }
			first := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", Arguments: arg(pair[0])})
			key := requestState(first)
			if key == "" {
				t.Fatalf("no question: %s", outcomeText(first))
			}
			o := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", Arguments: arg(pair[1]), RequestState: key, InputResponses: yesTo("sure")})
			if o.Forward != nil {
				t.Errorf("yes for %s forwarded different number %s: %s", pair[0], pair[1], asJSON(o.Forward.Params))
			}
		})
	}
}

// Confirmation state is bound to an opaque principal the gateway supplies.
// One principal cannot answer another's challenge, and with no principal at
// all the engine refuses to ask rather than bind a challenge to nobody.
func TestConfirmationIsBoundToThePrincipal(t *testing.T) {
	e := &Engine{}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: confirmEveryCallSet, Catalog: Catalog{Tools: []*mcp.Tool{{Name: "t", InputSchema: map[string]any{"type": "object"}}}}}
	ctx := context.Background()
	caps := map[string]any{"capabilities": map[string]any{"elicitation": map[string]any{}}}
	req := func(principal string, caller map[string]any) Request {
		return Request{Route: "direct", Servers: []*Server{s}, Principal: principal, Caller: caller, Client: caps}
	}
	ask := func(r Request) (string, Outcome) {
		out := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"id":1}`)})
		ir, _ := out.Result.(InputRequired)
		return ir.RequestState, out
	}
	var yes mcp.InputResponseMap
	_ = json.Unmarshal([]byte(`{"bifrost.ok":{"action":"accept","content":{"confirm":true}}}`), &yes)
	answer := func(r Request, state string) Outcome {
		return e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"id":1}`), RequestState: state, InputResponses: yes})
	}

	// No principal and no user id: the engine does not ask.
	for _, r := range []Request{req("", nil), req("", map[string]any{"email": "ana@example.com"}), req("", map[string]any{"userId": nil}), req("", map[string]any{"userId": ""})} {
		state, out := ask(r)
		b, _ := json.Marshal(out.Result)
		if state != "" || out.Forward != nil || !strings.Contains(string(b), "caller identity") {
			t.Errorf("a challenge was issued to nobody: state %q, result %s", state, b)
		}
	}

	// A principal with no user id works: a shared key, for example.
	key := req("key:shared-1", map[string]any{"email": "ana@example.com"})
	state, _ := ask(key)
	if state == "" {
		t.Fatal("no challenge for a principal without a user id")
	}
	// Another principal cannot answer it, even with the same caller fields.
	other := req("key:shared-2", map[string]any{"email": "ana@example.com"})
	if out := answer(other, state); out.Forward != nil || out.Error == nil || !strings.Contains(out.Error.Message, "another caller") {
		t.Fatalf("another principal answered the challenge: %+v", out.Error)
	}
	// Two callers behind the same shared principal are the same principal.
	state, _ = ask(key)
	if out := answer(req("key:shared-1", map[string]any{"email": "bo@example.com"}), state); out.Forward == nil {
		t.Errorf("the same principal could not answer: %+v", out.Error)
	}
	// With no principal the user id is used, and a principal that merely
	// equals another caller's user id is a different principal.
	state, _ = ask(req("", map[string]any{"userId": "u1"}))
	if out := answer(req("u1", nil), state); out.Forward != nil {
		t.Error("a principal equal to a user id answered that user's challenge")
	}
	state, _ = ask(req("", map[string]any{"userId": "u1"}))
	if out := answer(req("", map[string]any{"userId": "u1"}), state); out.Forward == nil {
		t.Errorf("the same user could not answer: %+v", out.Error)
	}
}

// Many clients answering at once: each challenge is redeemed exactly once.
func TestConfirmationIsRedeemedOnceUnderConcurrency(t *testing.T) {
	e := &Engine{}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: confirmEveryCallSet, Catalog: Catalog{Tools: []*mcp.Tool{{Name: "t", InputSchema: map[string]any{"type": "object"}}}}}
	r := newRequest("direct", s)
	ctx := context.Background()
	var yes mcp.InputResponseMap
	_ = json.Unmarshal([]byte(`{"bifrost.ok":{"action":"accept","content":{"confirm":true}}}`), &yes)
	for round := 0; round < 50; round++ {
		first := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"id":1}`)})
		ir, ok := first.Result.(InputRequired)
		if !ok {
			t.Fatalf("no confirmation asked: %+v", first.Error)
		}
		if strings.Contains(ir.RequestState, ".") || len(ir.RequestState) < 20 {
			t.Fatalf("the request state is not an opaque key: %q", ir.RequestState)
		}
		var forwarded atomic.Int64
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"id":1}`), RequestState: ir.RequestState, InputResponses: yes})
				if out.Forward != nil {
					forwarded.Add(1)
				}
			}()
		}
		wg.Wait()
		if n := forwarded.Load(); n != 1 {
			t.Fatalf("round %d: the call was forwarded %d times for one confirmation", round, n)
		}
	}
}

// Sixteen answers to one confirmation, released at the same moment: one is
// forwarded and every other one gets an error.
func TestAnswersRacingForOneConfirmationForwardOneCall(t *testing.T) {
	s := newServer(t, oneTool(), Rule{Hook: "tools/call.pre", When: "true", Then: confirmThen("sure", "Proceed?")})
	e, r := &Engine{}, newRequest("direct", s)
	key := requestState(e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t"}))
	if key == "" {
		t.Fatal("no question")
	}
	start, results := make(chan struct{}), make(chan Outcome, 16)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", RequestState: key, InputResponses: yesTo("sure")})
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	n := 0
	for o := range results {
		if o.Forward != nil {
			n++
		} else if o.Error == nil {
			t.Errorf("unexpected losing answer: %s", outcomeText(o))
		}
	}
	if n != 1 {
		t.Errorf("%d answers forwarded, want one", n)
	}
}

// One confirmation, one forwarded call. When the server answers a confirmed
// call with its own question, the gateway hands the client a state key that
// carries the confirmation. The key is spent when it is used, so a client
// that presents it again does not get the call forwarded again.
func TestOneConfirmationForwardsOneCallThroughAServersQuestion(t *testing.T) {
	ctx := context.Background()
	s := newServer(t, oneTool(), Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{KeyConfirm: "elicit('sure', 'Really?')"}})
	e, r := &Engine{}, newRequest("direct", s)
	args := json.RawMessage(`{"q":"x"}`)
	ir := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args}).Result.(InputRequired)
	p := &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: ir.RequestState, InputResponses: yesTo("sure")}
	confirmed := e.CallTool(ctx, r, p)
	if confirmed.Call == nil {
		t.Fatalf("the confirmed call was not forwarded: %s", outcomeText(confirmed))
	}
	envelope := e.UpstreamToolReply(ctx, r, confirmed.Call, p, json.RawMessage(serverQuestion)).Result.(InputRequired).RequestState
	forwards := 0
	for i := 0; i < 3; i++ {
		if o := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: envelope, InputResponses: serverAnswer()}); o.Forward != nil {
			forwards++
		}
	}
	if o := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: envelope}); o.Forward != nil {
		forwards++
	}
	if forwards > 1 {
		t.Errorf("one confirmation forwarded the call %d more times through the same envelope; want at most 1", forwards)
	}
}

// The gateway and the server never ask in the same round. A rule that asks
// a new question on the round that answers a server's question has the
// request refused, so the client sends it again: asking would lose the
// server's state and the user's answer to the server.
func TestAGatewayQuestionOnTheServersRoundIsRefused(t *testing.T) {
	ctx := context.Background()
	args := json.RawMessage(`{"q":"x"}`)
	s := newServer(t, oneTool(),
		Rule{Hook: "tools/call.pre", When: "true", Then: confirmThen("a", "A?")},
		Rule{Hook: "tools/call.pre", When: "request.params.?_meta.?late.orValue(false)", Then: confirmThen("b", "B?")})
	e, r := &Engine{}, newRequest("direct", s)
	k1 := requestState(e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args}))
	p2 := &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: k1, InputResponses: yesTo("a")}
	o2 := e.CallTool(ctx, r, p2)
	if o2.Call == nil {
		t.Fatalf("the confirmed call was not forwarded: %s", outcomeText(o2))
	}
	k3 := requestState(e.UpstreamToolReply(ctx, r, o2.Call, p2, json.RawMessage(serverQuestion)))
	if k3 == "" {
		t.Fatal("the server's question did not reach the client")
	}
	// Without the new question, the answer to the server goes through.
	plain := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: k3, InputResponses: serverAnswer()})
	if plain.Forward == nil || !strings.Contains(asJSON(plain.Forward.Params), `"requestState":"UP"`) {
		t.Fatalf("control: the answer to the server was not forwarded with its state: %s", outcomeText(plain))
	}
	// With it, the request is refused and nothing is forwarded.
	k3 = requestState(e.UpstreamToolReply(ctx, r, plain.Call, p2, json.RawMessage(serverQuestion)))
	o := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: k3, InputResponses: serverAnswer(), Meta: mcp.Meta{"late": true}})
	if o.Forward != nil || requestState(o) != "" {
		t.Fatalf("a new gateway question on the server's round was forwarded or asked: %s", outcomeText(o))
	}
	if got := outcomeText(o); !strings.Contains(got, "Send it again") {
		t.Errorf("the client was not told to send the request again: %s", got)
	}
}

// A yes counts for one question: its words and the arguments when it was
// asked. Two rules that ask in the same words under the same key, with a rule
// between them that changes an argument, ask two questions. The user is asked
// twice, and the call then goes ahead.
func TestTheSameQuestionAskedTwiceWithASetBetweenCanBeAnswered(t *testing.T) {
	ctx := context.Background()
	args := json.RawMessage(`{"q":"x"}`)
	s := newServer(t, oneTool(),
		Rule{Hook: "tools/call.pre", When: "true", Then: confirmThen("sure", "Really?")},
		Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{"request.params.arguments.q": "'changed'"}},
		Rule{Hook: "tools/call.pre", When: "true", Then: confirmThen("sure", "Really?")})
	e, r := &Engine{}, newRequest("direct", s)
	state, in := "", mcp.InputResponseMap(nil)
	for round := 0; round < 4; round++ {
		o := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: state, InputResponses: in})
		if o.Forward != nil {
			return
		}
		if state = requestState(o); state == "" {
			t.Fatalf("round %d: the request can never be confirmed: %s", round+1, outcomeText(o))
		}
		in = yesTo("sure")
	}
	t.Error("the gateway kept asking")
}

// No message the client can receive uses a retired name of the rule language:
// the reply to a confirmed call does not mention has(input.…).
func TestNoClientMessageUsesTheRetiredInputName(t *testing.T) {
	ctx := context.Background()
	s := newServer(t, oneTool(), Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{KeyConfirm: "elicit('sure', 'Really?')"}})
	e, r := &Engine{}, newRequest("direct", s)
	args := json.RawMessage(`{"q":"x"}`)
	ir := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args}).Result.(InputRequired)
	o := e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: args, RequestState: ir.RequestState, InputResponses: yesTo("sure")})
	if got := outcomeText(o); strings.Contains(got, "has(input.") {
		t.Errorf("the client was told %s", got)
	}
}

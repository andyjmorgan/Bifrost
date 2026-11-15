package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- helpers ---------------------------------------------------------------------

// upstreamSeq plays events as an upstream would, and records how far the
// engine read and whether it let go of the stream.
type upstreamSeq struct {
	events []UpstreamEvent
	fail   error // yielded after the events, when set
	pulled int
	closed bool
}

func (u *upstreamSeq) seq() iter.Seq2[UpstreamEvent, error] {
	return func(yield func(UpstreamEvent, error) bool) {
		defer func() { u.closed = true }()
		for _, ev := range u.events {
			u.pulled++
			if !yield(ev, nil) {
				return
			}
		}
		if u.fail != nil {
			yield(UpstreamEvent{}, u.fail)
		}
	}
}

func progressEvent(params string) UpstreamEvent {
	return UpstreamEvent{Method: ProgressMethod, Params: json.RawMessage(params)}
}

func resultEvent(result string) UpstreamEvent {
	return UpstreamEvent{Result: json.RawMessage(result)}
}

// reply is everything one stream yielded.
type reply struct {
	notes []ReplyEvent
	final *Outcome
	err   error
	order []string // "note" or "final", in the order yielded
}

func collect(seq iter.Seq2[ReplyEvent, error]) reply {
	var r reply
	for ev, err := range seq {
		switch {
		case err != nil:
			r.err = err
		case ev.Final != nil:
			r.final = ev.Final
			r.order = append(r.order, "final")
		default:
			r.notes = append(r.notes, ev)
			r.order = append(r.order, "note")
		}
	}
	return r
}

// streamCase is one tool behind one rule set, with a forwarded call ready
// for its reply.
type streamCase struct {
	e      *Engine
	r      Request
	call   *CallContext
	params *mcp.CallToolParamsRaw
}

func newStreamCase(t *testing.T, route string, rules []Rule, limits Limits) *streamCase {
	t.Helper()
	e := &Engine{Signer: Signer{Key: []byte("k")}, Limits: limits}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: mustCompile(t, rules), Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
	r := reviewRequest(route, s)
	name := "t"
	if route == "mux" {
		name = "a" + Separator + "t"
	}
	p := &mcp.CallToolParamsRaw{Name: name, Arguments: json.RawMessage(`{"q":"abc","limit":5}`)}
	first := e.CallTool(context.Background(), r, p)
	if first.Forward == nil {
		t.Fatalf("the call was not forwarded: %+v %+v", first.Error, first.Result)
	}
	return &streamCase{e: e, r: r, call: first.Call, params: p}
}

func (c *streamCase) run(u *upstreamSeq) reply {
	return collect(c.e.ToolReplyStream(context.Background(), c.r, c.call, c.params, u.seq()))
}

func textOf(t *testing.T, o *Outcome) string {
	t.Helper()
	if o == nil {
		t.Fatal("no final outcome")
	}
	b, err := json.Marshal(o.Result)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Content []struct{ Text string } `json:"content"`
		IsError bool                    `json:"isError"`
	}
	_ = json.Unmarshal(b, &m)
	if len(m.Content) == 0 {
		return ""
	}
	return m.Content[0].Text
}

const okResult = `{"resultType":"complete","content":[{"type":"text","text":"done"}]}`

// ---- the shape of a reply ---------------------------------------------------------

// A single JSON body is a stream of one event, and gives the same outcome
// as the single-message entry point.
func TestStreamOfOneEventMatchesTheSingleReply(t *testing.T) {
	c := newStreamCase(t, "direct", []Rule{{Hook: "tools/result", When: "true", Then: map[string]string{"content": "[{'type': 'text', 'text': 'shaped'}]"}}}, Limits{})
	got := c.run(&upstreamSeq{events: []UpstreamEvent{resultEvent(okResult)}})
	if got.err != nil || len(got.notes) != 0 || got.final == nil {
		t.Fatalf("got %+v", got)
	}
	want := c.e.UpstreamToolReply(context.Background(), c.r, c.call, c.params, json.RawMessage(okResult))
	a, _ := json.Marshal(got.final.Result)
	b, _ := json.Marshal(want.Result)
	if string(a) != string(b) || textOf(t, got.final) != "shaped" {
		t.Errorf("stream %s, single %s", a, b)
	}
}

// With no progress rules, events are relayed in order, byte for byte, and
// the final result still goes through the result rules.
func TestProgressIsRelayedInOrderThenTheResult(t *testing.T) {
	c := newStreamCase(t, "direct", []Rule{{Hook: "tools/result", When: "true", Then: map[string]string{"content": "[{'type': 'text', 'text': 'shaped'}]"}}}, Limits{})
	p1 := `{"progressToken":"gw-1","progress":0.1,"total":12345678901234567890,"message":"one"}`
	p2 := `{"progressToken":"gw-1","progress":2}`
	u := &upstreamSeq{events: []UpstreamEvent{progressEvent(p1), progressEvent(p2), resultEvent(okResult)}}
	got := c.run(u)
	if strings.Join(got.order, ",") != "note,note,final" {
		t.Fatalf("order %v", got.order)
	}
	if string(got.notes[0].Params) != p1 || string(got.notes[1].Params) != p2 || got.notes[0].Method != ProgressMethod {
		t.Errorf("progress was changed: %s | %s", got.notes[0].Params, got.notes[1].Params)
	}
	if textOf(t, got.final) != "shaped" {
		t.Errorf("the result rule did not run: %q", textOf(t, got.final))
	}
	if !u.closed {
		t.Error("the upstream stream was not released")
	}
}

// ---- the progress hook: drop or reshape ------------------------------------------

func TestProgressRulesDropAndReshape(t *testing.T) {
	rules := []Rule{
		{Name: "Drop card numbers", Hook: "tools/progress", When: "progress.message.matches('[0-9]{4} [0-9]{4}')", Then: map[string]string{"decision": "'deny'", "reason": "'card number'"}},
		{Name: "Hide the host", Hook: "tools/progress", When: "progress.message.contains('db-internal')", Then: map[string]string{"message": "progress.message.replace('db-internal', 'the database')"}},
		{Name: "Chain", Hook: "tools/progress", When: "progress.message.contains('the database')", Then: map[string]string{"message": "progress.message + '.'"}},
	}
	c := newStreamCase(t, "direct", rules, Limits{})
	u := &upstreamSeq{events: []UpstreamEvent{
		progressEvent(`{"progressToken":7,"progress":1,"total":3,"message":"Reading card 4111 1111"}`),
		progressEvent(`{"progressToken":7,"progress":2.5,"total":12345678901234567890,"message":"Querying db-internal","_meta":{"x":1}}`),
		progressEvent(`{"progressToken":7,"progress":3,"total":3,"message":"Finishing"}`),
		resultEvent(okResult),
	}}
	got := c.run(u)
	if len(got.notes) != 2 {
		t.Fatalf("%d events relayed, want 2: %+v", len(got.notes), got.notes)
	}
	// The reshaped event: message changed by two chained rules; the token,
	// the numbers and _meta exactly as sent.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(got.notes[0].Params, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["message"]) != `"Querying the database."` {
		t.Errorf("message = %s", m["message"])
	}
	if string(m["progressToken"]) != "7" || string(m["progress"]) != "2.5" || string(m["total"]) != "12345678901234567890" || string(m["_meta"]) != `{"x":1}` {
		t.Errorf("a rule changed more than the message: %s", got.notes[0].Params)
	}
	// The untouched event is relayed byte for byte.
	if string(got.notes[1].Params) != `{"progressToken":7,"progress":3,"total":3,"message":"Finishing"}` {
		t.Errorf("untouched event changed: %s", got.notes[1].Params)
	}
	// The final outcome carries the progress hits, for the audit trail.
	var effects []string
	for _, h := range got.final.Hits {
		if h.Hook == "tools/progress" {
			effects = append(effects, h.Rule+":"+h.Effect)
		}
	}
	if strings.Join(effects, ",") != "Drop card numbers:deny,Hide the host:set,Chain:set" {
		t.Errorf("progress hits = %v", effects)
	}
	if textOf(t, got.final) != "done" {
		t.Errorf("final = %q", textOf(t, got.final))
	}
}

// Progress rules read the call's arguments, the tool and the numbers.
func TestProgressRulesSeeTheCallAndTheNumbers(t *testing.T) {
	rules := []Rule{
		{Hook: "tools/progress", When: "upstream.name == 't' && args.q == 'abc' && args.limit == 5 && caller.userId == 'u' && has(progress.total) && progress.progress >= progress.total",
			Then: map[string]string{"message": "'complete'"}},
		{Hook: "tools/progress", When: "!has(progress.total)", Then: map[string]string{"decision": "'deny'"}},
	}
	c := newStreamCase(t, "mux", rules, Limits{})
	got := c.run(&upstreamSeq{events: []UpstreamEvent{
		progressEvent(`{"progressToken":"p","progress":1}`),
		progressEvent(`{"progressToken":"p","progress":1,"total":2}`),
		progressEvent(`{"progressToken":"p","progress":2,"total":2}`),
		resultEvent(okResult),
	}})
	if len(got.notes) != 2 {
		t.Fatalf("%d events relayed, want 2", len(got.notes))
	}
	if string(got.notes[0].Params) != `{"progressToken":"p","progress":1,"total":2}` {
		t.Errorf("first relayed event: %s", got.notes[0].Params)
	}
	if !strings.Contains(string(got.notes[1].Params), `"message":"complete"`) {
		t.Errorf("second relayed event: %s", got.notes[1].Params)
	}
}

// A rule that errors drops its own event. It never refuses the call.
func TestProgressRuleErrorDropsOnlyThatEvent(t *testing.T) {
	c := newStreamCase(t, "direct", []Rule{{Hook: "tools/progress", When: "progress.total > 1", Then: map[string]string{"message": "'x'"}}}, Limits{})
	got := c.run(&upstreamSeq{events: []UpstreamEvent{
		progressEvent(`{"progressToken":"p","progress":1}`), // no total: the rule errors
		progressEvent(`{"progressToken":"p","progress":1,"total":1}`),
		resultEvent(okResult),
	}})
	if len(got.notes) != 1 || string(got.notes[0].Params) != `{"progressToken":"p","progress":1,"total":1}` {
		t.Fatalf("relayed %+v", got.notes)
	}
	if got.final == nil || got.final.Error != nil || textOf(t, got.final) != "done" {
		t.Fatalf("the call did not complete: %+v", got.final)
	}
	errs := 0
	for _, h := range got.final.Hits {
		if h.Hook == "tools/progress" && h.Effect == "error" {
			errs++
		}
	}
	if errs != 1 {
		t.Errorf("%d error hits, want 1", errs)
	}
}

// An audit rule on progress changes nothing.
func TestProgressAuditRuleChangesNothing(t *testing.T) {
	c := newStreamCase(t, "direct", []Rule{{Mode: ModeAudit, Hook: "tools/progress", When: "true", Then: map[string]string{"decision": "'deny'"}}}, Limits{})
	p := `{"progressToken":"p","progress":1}`
	got := c.run(&upstreamSeq{events: []UpstreamEvent{progressEvent(p), resultEvent(okResult)}})
	if len(got.notes) != 1 || string(got.notes[0].Params) != p {
		t.Fatalf("an audit rule changed the stream: %+v", got.notes)
	}
	if len(got.final.Hits) == 0 || got.final.Hits[0].Effect != "would deny" {
		t.Errorf("hits = %+v", got.final.Hits)
	}
}

// The server/config switch turns progress off for a server.
func TestProgressSwitchOffDropsEveryEvent(t *testing.T) {
	c := newStreamCase(t, "direct", []Rule{{Hook: "server/config", When: "true", Then: map[string]string{"progress": "false"}}}, Limits{})
	got := c.run(&upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progressToken":"p","progress":1}`), resultEvent(okResult)}})
	if len(got.notes) != 0 || textOf(t, got.final) != "done" {
		t.Fatalf("got %+v", got)
	}
}

// Progress rules have their own allowance. When it is spent the remaining
// events are dropped, and the final response still gets its full budget.
func TestProgressAllowanceSpentDropsEventsNotTheResult(t *testing.T) {
	rules := []Rule{
		{Hook: "tools/progress", When: "true", Then: map[string]string{"message": "'x'"}},
		{Hook: "tools/result", When: "true", Then: map[string]string{"content": "[{'type': 'text', 'text': 'shaped'}]"}},
	}
	c := newStreamCase(t, "direct", rules, Limits{ProgressBudget: time.Nanosecond})
	var events []UpstreamEvent
	for i := 0; i < 500; i++ {
		events = append(events, progressEvent(`{"progressToken":"p","progress":1}`))
	}
	events = append(events, resultEvent(okResult))
	got := c.run(&upstreamSeq{events: events})
	// The event in flight when the allowance runs out may get through; no
	// later one does.
	if len(got.notes) > 1 {
		t.Fatalf("%d events relayed after the allowance was spent", len(got.notes))
	}
	if textOf(t, got.final) != "shaped" {
		t.Fatalf("the result lost its budget: %+v", got.final)
	}
	issues := strings.Join(got.final.Issues, "\n")
	if !strings.Contains(issues, "dropped 499 progress events") && !strings.Contains(issues, "dropped 500 progress events") {
		t.Errorf("issues = %v", got.final.Issues)
	}
	// One flood must not leave one hit per event.
	if n := len(got.final.Hits); n > 5 {
		t.Errorf("%d hits for a flood of dropped events", n)
	}
}

// ---- other events ----------------------------------------------------------------

// Log lines and unknown notifications are dropped and counted.
func TestOtherNotificationsAreDropped(t *testing.T) {
	c := newStreamCase(t, "direct", nil, Limits{})
	got := c.run(&upstreamSeq{events: []UpstreamEvent{
		{Method: "notifications/message", Params: json.RawMessage(`{"level":"info","data":"secret"}`)},
		{Method: "notifications/message", Params: json.RawMessage(`{"level":"info","data":"secret"}`)},
		{Method: "notifications/tools/list_changed"},
		{Method: "notifications/made/up", Params: json.RawMessage(`{"x":1}`)},
		progressEvent(`{"progressToken":"p","progress":1}`),
		progressEvent(`[1,2]`), // relayed as received when no rule needs to read it
		resultEvent(okResult),
	}})
	if len(got.notes) != 2 {
		t.Fatalf("relayed %+v", got.notes)
	}
	issues := strings.Join(got.final.Issues, "\n")
	for _, want := range []string{"dropped 2 notifications/message", "dropped 1 notifications/tools/list_changed", "dropped 1 notifications/made/up"} {
		if !strings.Contains(issues, want) {
			t.Errorf("issues lack %q: %v", want, got.final.Issues)
		}
	}
}

// A malformed progress event is dropped when a rule has to read it.
func TestMalformedProgressIsDroppedUnderRules(t *testing.T) {
	c := newStreamCase(t, "direct", []Rule{{Hook: "tools/progress", When: "false", Then: map[string]string{"message": "'x'"}}}, Limits{})
	for _, bad := range []string{`[1,2]`, `"text"`, `null`, `{`, ``, `{"progress":"x","message":5}`} {
		got := c.run(&upstreamSeq{events: []UpstreamEvent{progressEvent(bad), resultEvent(okResult)}})
		if got.final == nil || textOf(t, got.final) != "done" {
			t.Fatalf("%q broke the call: %+v", bad, got)
		}
		if len(got.notes) > 1 {
			t.Errorf("%q yielded %d events", bad, len(got.notes))
		}
	}
}

// ---- the final message ----------------------------------------------------------

// Nothing sent can be taken back: a result refused at the end still follows
// the progress that was relayed.
func TestRefusedResultStillFollowsRelayedProgress(t *testing.T) {
	c := newStreamCase(t, "direct", []Rule{{Hook: "tools/result", When: "true", Then: map[string]string{"decision": "'deny'", "reason": "'No results from this tool.'"}}}, Limits{})
	got := c.run(&upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progressToken":"p","progress":1}`), resultEvent(okResult)}})
	if strings.Join(got.order, ",") != "note,final" {
		t.Fatalf("order %v", got.order)
	}
	if textOf(t, got.final) != "No results from this tool." {
		t.Errorf("final = %q", textOf(t, got.final))
	}
}

// The upstream's own error passes through as the final message.
func TestUpstreamErrorPassesThrough(t *testing.T) {
	c := newStreamCase(t, "direct", nil, Limits{})
	got := c.run(&upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progressToken":"p","progress":1}`), {Error: &RPCError{Code: -32000, Message: "upstream failed"}}}})
	if len(got.notes) != 1 || got.final == nil || got.final.Error == nil || got.final.Error.Message != "upstream failed" || got.final.Result != nil {
		t.Fatalf("got %+v", got.final)
	}
}

// An input request from the upstream is a final message like any other.
func TestUpstreamInputRequestEndsTheStream(t *testing.T) {
	c := newStreamCase(t, "direct", []Rule{{Hook: "server/connect", When: "true", Then: map[string]string{"capabilities.elicitation": "true"}}}, Limits{})
	ask := `{"resultType":"input_required","requestState":"up1","inputRequests":{"q":{"method":"elicitation/create","params":{"message":"Which?","requestedSchema":{"type":"object","properties":{}}}}}}`
	got := c.run(&upstreamSeq{events: []UpstreamEvent{resultEvent(ask)}})
	ir, ok := got.final.Result.(InputRequired)
	if !ok || ir.InputRequests["q"] == nil || strings.Contains(ir.RequestState, "up1") {
		t.Fatalf("got %+v", got.final)
	}
}

// A task handle is refused: tasks are an extension the gateway does not
// support, and a later fetch of the result would skip every result rule.
func TestTaskHandleIsRefused(t *testing.T) {
	c := newStreamCase(t, "direct", nil, Limits{})
	handle := `{"resultType":"task","taskId":"abc","status":"working","createdAt":"2026-11-15T00:00:00Z","lastUpdatedAt":"2026-11-15T00:00:00Z","ttlMs":60000}`
	for _, got := range []*Outcome{
		c.run(&upstreamSeq{events: []UpstreamEvent{resultEvent(handle)}}).final,
		func() *Outcome {
			o := c.e.UpstreamToolReply(context.Background(), c.r, c.call, c.params, json.RawMessage(handle))
			return &o
		}(),
	} {
		b, _ := json.Marshal(got.Result)
		if strings.Contains(string(b), "abc") || !strings.Contains(string(b), `"isError":true`) {
			t.Errorf("the handle reached the client: %s", b)
		}
		if !strings.Contains(strings.Join(got.Issues, "\n"), "task handle") {
			t.Errorf("issues = %v", got.Issues)
		}
	}
}

// ---- how a stream ends -----------------------------------------------------------

// A reply that never completes ends with an error, so no client is left
// waiting. The client is told the request may have run; the cause is for
// the administrator only.
func TestLostReplyEndsWithAnError(t *testing.T) {
	c := newStreamCase(t, "direct", nil, Limits{})
	broke := errors.New("connection reset by 10.0.0.7:443")
	for _, u := range []*upstreamSeq{
		{events: []UpstreamEvent{progressEvent(`{"progressToken":"p","progress":1}`)}},              // the stream just ends
		{events: []UpstreamEvent{progressEvent(`{"progressToken":"p","progress":1}`)}, fail: broke}, // the stream breaks
		{}, // nothing at all
	} {
		got := c.run(u)
		if got.err != nil || got.final == nil || got.final.Error == nil || got.final.Result != nil {
			t.Fatalf("got %+v", got)
		}
		if got.final.Error.Code != CodeUpstreamLost || !strings.Contains(got.final.Error.Message, "may or may not have run") {
			t.Errorf("error = %+v", got.final.Error)
		}
		if strings.Contains(got.final.Error.Message, "10.0.0.7") {
			t.Errorf("the client message carries the cause: %q", got.final.Error.Message)
		}
		if got.order[len(got.order)-1] != "final" {
			t.Errorf("the error is not the last event: %v", got.order)
		}
		if !strings.Contains(strings.Join(got.final.Issues, "\n"), "upstream reply lost") {
			t.Errorf("issues = %v", got.final.Issues)
		}
		if len(u.events) > 0 && len(got.notes) != 1 {
			t.Errorf("%d progress events relayed before the loss, want 1", len(got.notes))
		}
	}
	// The cause reaches the administrator.
	got := c.run(&upstreamSeq{fail: broke})
	if !strings.Contains(strings.Join(got.final.Issues, "\n"), "10.0.0.7") {
		t.Errorf("the cause is missing from the issues: %v", got.final.Issues)
	}
}

// Anything after the final response is never read.
func TestNothingIsReadAfterTheResponse(t *testing.T) {
	c := newStreamCase(t, "direct", nil, Limits{})
	u := &upstreamSeq{events: []UpstreamEvent{resultEvent(okResult), progressEvent(`{"progressToken":"p","progress":9}`), resultEvent(okResult)}}
	got := c.run(u)
	if len(got.notes) != 0 || got.final == nil || u.pulled != 1 || !u.closed {
		t.Fatalf("pulled %d, closed %v, got %+v", u.pulled, u.closed, got)
	}
}

// When the client goes away the engine stops reading, which lets the
// gateway close the upstream stream. The spec treats that as cancellation.
func TestClientLeavingStopsTheUpstreamRead(t *testing.T) {
	c := newStreamCase(t, "direct", nil, Limits{})
	var events []UpstreamEvent
	for i := 0; i < 100; i++ {
		events = append(events, progressEvent(`{"progressToken":"p","progress":1}`))
	}
	events = append(events, resultEvent(okResult))
	u := &upstreamSeq{events: events}
	seen := 0
	for range c.e.ToolReplyStream(context.Background(), c.r, c.call, c.params, u.seq()) {
		seen++
		if seen == 3 {
			break
		}
	}
	if u.pulled != 3 || !u.closed {
		t.Fatalf("the engine read %d events after the client left at 3; released: %v", u.pulled, u.closed)
	}
}

// ---- panics on a stream ----------------------------------------------------------

// A panic while handling an event refuses the request, once, and stops.
func TestPanicOnAStreamFailsClosed(t *testing.T) {
	log := &faultLog{}
	c := newStreamCase(t, "direct", nil, Limits{})
	c.e.OnFault = log.add
	c.call.Server.Rules = boomSet(t, "tools/result", false) // the result rule panics inside CEL
	got := c.run(&upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progressToken":"p","progress":1}`), resultEvent(okResult)}})
	if got.final == nil || got.final.Fault == nil || got.final.Fault.Rule == nil || got.final.Fault.Rule.When != "boom()" {
		t.Fatalf("no fault with the rule: %+v", got.final)
	}
	if b, _ := json.Marshal(got.final.Result); strings.Contains(string(b), "done") {
		t.Fatalf("the result got through a panicking rule: %s", b)
	}

	// A panic in the engine's own code, outside CEL, while reading the stream.
	c2 := newStreamCase(t, "direct", nil, Limits{})
	c2.e.OnFault = log.add
	bad := func(yield func(UpstreamEvent, error) bool) {
		yield(progressEvent(`{"progressToken":"p","progress":1}`), nil)
		panic("stream reader bug")
	}
	var r reply
	mustNotPanic(t, "stream", func() { r = collect(c2.e.ToolReplyStream(context.Background(), c2.r, c2.call, c2.params, bad)) })
	if len(r.notes) != 1 || r.final == nil || r.final.Error == nil || r.final.Error.Code != CodeInternal || r.final.Fault == nil || r.final.Fault.Call == nil || r.final.Fault.Call.Target != "t" {
		t.Fatalf("got %+v", r.final)
	}
	if n := len(log.all()); n != 2 {
		t.Errorf("OnFault called %d times, want 2", n)
	}
}

// A panic in the gateway's own loop body is the gateway's. The engine must
// not swallow it or answer it with a refusal.
func TestConsumerPanicPassesThrough(t *testing.T) {
	c := newStreamCase(t, "direct", nil, Limits{})
	u := &upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progressToken":"p","progress":1}`), resultEvent(okResult)}}
	yields := 0
	var caught any
	func() {
		defer func() { caught = recover() }()
		for range c.e.ToolReplyStream(context.Background(), c.r, c.call, c.params, u.seq()) {
			yields++
			panic("gateway bug")
		}
	}()
	if caught != "gateway bug" || yields != 1 {
		t.Fatalf("caught %v after %d yields", caught, yields)
	}
}

// ---- the other two families ------------------------------------------------------

func TestPromptAndReadStreamsUseTheirOwnProgressHook(t *testing.T) {
	rules := []Rule{
		{Hook: "prompts/progress", When: "name == 'summarize'", Then: map[string]string{"message": "'prompt: ' + progress.message"}},
		{Hook: "resources/progress", When: "uri == 'file:///doc'", Then: map[string]string{"message": "'read: ' + progress.message"}},
		{Hook: "tools/progress", When: "true", Then: map[string]string{"decision": "'deny'"}},
	}
	e := &Engine{Signer: Signer{Key: []byte("k")}}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: mustCompile(t, rules), Catalog: Catalog{
		Prompts:   []*mcp.Prompt{{Name: "summarize"}},
		Resources: []*mcp.Resource{{URI: "file:///doc", Name: "doc"}},
	}}
	r := reviewRequest("direct", s)
	ctx := context.Background()
	ev := progressEvent(`{"progressToken":"p","progress":1,"message":"working"}`)

	pp := &mcp.GetPromptParams{Name: "summarize"}
	pf := e.GetPrompt(ctx, r, pp)
	if pf.Forward == nil {
		t.Fatalf("prompt not forwarded: %+v", pf.Error)
	}
	got := collect(e.PromptReplyStream(ctx, r, pf.Call, pp, (&upstreamSeq{events: []UpstreamEvent{ev, resultEvent(`{"messages":[]}`)}}).seq()))
	if len(got.notes) != 1 || !strings.Contains(string(got.notes[0].Params), `"message":"prompt: working"`) || got.final == nil || got.final.Error != nil {
		t.Errorf("prompt stream: %+v %+v", got.notes, got.final)
	}

	rp := &mcp.ReadResourceParams{URI: "file:///doc"}
	rf := e.ReadResource(ctx, r, rp)
	if rf.Forward == nil {
		t.Fatalf("read not forwarded: %+v", rf.Error)
	}
	got = collect(e.ReadReplyStream(ctx, r, rf.Call, rp, (&upstreamSeq{events: []UpstreamEvent{ev, resultEvent(`{"contents":[{"uri":"file:///doc","text":"x"}]}`)}}).seq()))
	if len(got.notes) != 1 || !strings.Contains(string(got.notes[0].Params), `"message":"read: working"`) || got.final == nil || got.final.Error != nil {
		t.Errorf("read stream: %+v %+v", got.notes, got.final)
	}
}

// ---- what a progress rule may set ------------------------------------------------

func TestProgressRulesCompileOnlyWhatTheyMaySet(t *testing.T) {
	ok := []Rule{
		{Hook: "tools/progress", When: "progress.progress > 1", Then: map[string]string{"decision": "'deny'", "reason": "'noise'"}},
		{Hook: "prompts/progress", When: "true", Then: map[string]string{"message": "''"}},
		{Hook: "resources/progress", When: "has(progress.total)", Then: map[string]string{"message": "string(progress.progress)"}},
	}
	if _, probs := Compile(ok); probs != nil {
		t.Fatalf("valid progress rules refused: %v", probs)
	}
	for _, bad := range []Rule{
		{Hook: "tools/progress", When: "true", Then: map[string]string{"progress": "1"}},
		{Hook: "tools/progress", When: "true", Then: map[string]string{"total": "1"}},
		{Hook: "tools/progress", When: "true", Then: map[string]string{"progressToken": "'x'"}},
		{Hook: "tools/progress", When: "true", Then: map[string]string{"content": "[]"}},
		{Hook: "tools/progress", When: "result.isError", Then: map[string]string{"decision": "'deny'"}},
		{Hook: "tools/result", When: "progress.progress > 1", Then: map[string]string{"decision": "'deny'"}},
	} {
		if _, probs := Compile([]Rule{bad}); probs == nil {
			t.Errorf("compiled, but should not: %+v", bad)
		}
	}
}

// A message of the wrong type is a rule error, so the event is dropped.
func TestProgressMessageOfTheWrongTypeDropsTheEvent(t *testing.T) {
	c := newStreamCase(t, "direct", []Rule{{Hook: "tools/progress", When: "true", Then: map[string]string{"message": "5"}}}, Limits{})
	got := c.run(&upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progressToken":"p","progress":1}`), resultEvent(okResult)}})
	if len(got.notes) != 0 || textOf(t, got.final) != "done" {
		t.Fatalf("got %+v", got)
	}
}

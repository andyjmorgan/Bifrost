package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Things the engine does not recognise: passed on, dropped or refused, and
// recorded by shape and never by value.

// findUnknown is the record of a kind and a name, or nil.
func findUnknown(list []Unknown, kind, name string) *Unknown {
	for i := range list {
		if list[i].Kind == kind && list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

// Things the engine does not recognise are passed on, dropped or refused as
// the protocol requires, and each is recorded by its shape: the kind, the
// name, the field names and their types. No value is ever written to a
// record.
func TestUnknownsAreRecordedByShapeNeverByValue(t *testing.T) {
	var mu sync.Mutex
	var reported []Unknown
	e := &Engine{OnUnknown: func(u Unknown) {
		mu.Lock()
		reported = append(reported, u)
		mu.Unlock()
	}}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: mustCompile(t, nil), Catalog: Catalog{
		Tools:     []*mcp.Tool{typedTool()},
		Prompts:   []*mcp.Prompt{{Name: "p"}},
		Templates: []*mcp.ResourceTemplate{{URITemplate: "repo://{owner}/contents{/path*}", Name: "contents"}, {URITemplate: "file:///docs/{name}", Name: "docs"}},
	}}
	r := newRequest("direct", s)
	ctx := context.Background()
	p := &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"abc"}`)}
	first := e.CallTool(ctx, r, p)
	if first.Forward == nil {
		t.Fatalf("not forwarded: %+v", first.Error)
	}

	// Content blocks of types the protocol version does not define.
	result := `{"content":[
		{"type":"text","text":"fine"},
		{"type":"video","url":"https://secret.example/clip","frames":[1,2,3],"meta":{"a":1,"b":2}},
		{"type":"video","url":"https://secret.example/other","frames":[]},
		{"text":"a block with no type"},
		"not even an object"]}`
	out := e.UpstreamToolReply(ctx, r, first.Call, p, json.RawMessage(result))
	b, _ := json.Marshal(out.Result)
	for _, want := range []string{`"video"`, "https://secret.example/clip", "a block with no type", "not even an object"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the gateway changed content it did not recognise; missing %s in %s", want, b)
		}
	}
	video := findUnknown(out.Unknown, UnknownContentBlock, "video")
	if video == nil || video.Count != 2 || video.Action != "passed" || video.Server != "a" || video.Target != "t" {
		t.Fatalf("video record = %+v in %+v", video, out.Unknown)
	}
	if video.Shape["type"] != "string" || video.Shape["url"] != "string" || video.Shape["frames"] != "array[3]" || video.Shape["meta"] != "object{2}" {
		t.Errorf("video shape = %v", video.Shape)
	}
	untyped := findUnknown(out.Unknown, UnknownContentBlock, "")
	if untyped == nil || untyped.Count != 2 {
		t.Errorf("untyped blocks record = %+v", untyped)
	}
	rec, _ := json.Marshal(out.Unknown)
	for _, secret := range []string{"secret.example", "a block with no type", "not even an object", "fine"} {
		if strings.Contains(string(rec), secret) {
			t.Errorf("a record carries a value: %q in %s", secret, rec)
		}
	}

	// A result type the engine does not handle is passed on and recorded; a
	// task handle is refused and recorded.
	out = e.UpstreamToolReply(ctx, r, first.Call, p, json.RawMessage(`{"resultType":"partial","content":[{"type":"text","text":"x"}]}`))
	if u := findUnknown(out.Unknown, UnknownResultType, "partial"); u == nil || u.Action != "passed" {
		t.Errorf("partial record = %+v", out.Unknown)
	}
	out = e.UpstreamToolReply(ctx, r, first.Call, p, json.RawMessage(`{"resultType":"task","taskId":"abc","status":"working"}`))
	if u := findUnknown(out.Unknown, UnknownResultType, "task"); u == nil || u.Action != "refused" || u.Shape["taskId"] != "string" {
		t.Errorf("task record = %+v", out.Unknown)
	}

	// An input request that is not an elicitation is refused and recorded.
	out = e.UpstreamToolReply(ctx, r, first.Call, p, json.RawMessage(`{"resultType":"input_required","inputRequests":{"s":{"method":"sampling/createMessage","params":{"messages":[],"maxTokens":5}}}}`))
	if _, passed := out.Result.(InputRequired); passed {
		t.Error("a sampling request was passed to the client")
	}
	if u := findUnknown(out.Unknown, UnknownInputRequest, "sampling/createMessage"); u == nil || u.Action != "refused" || u.Shape["maxTokens"] != "number" {
		t.Errorf("sampling record = %+v", out.Unknown)
	}

	// A prompt message with an unknown content type.
	pp := &mcp.GetPromptParams{Name: "p"}
	pf := e.GetPrompt(ctx, r, pp)
	out = e.UpstreamPromptReply(ctx, r, pf.Call, pp, json.RawMessage(`{"messages":[{"role":"user","content":{"type":"hologram","depth":3}},{"role":"user","content":{"type":"text","text":"hi"}}]}`))
	if u := findUnknown(out.Unknown, UnknownContentBlock, "hologram"); u == nil || u.Target != "p" || u.Shape["depth"] != "number" {
		t.Errorf("hologram record = %+v", out.Unknown)
	}
	if b, _ := json.Marshal(out.Result); !strings.Contains(string(b), "hologram") {
		t.Errorf("the unknown prompt content was not passed on: %s", b)
	}

	// Notifications on a stream: unknown ones are dropped and recorded; a log
	// message is a known type and is only counted.
	got := collect(e.ToolReplyStream(ctx, r, first.Call, p, (&upstreamSeq{events: []UpstreamEvent{
		{Method: "notifications/vendor/heartbeat", Params: json.RawMessage(`{"beat":1,"host":"db-internal-7"}`)},
		{Method: "notifications/vendor/heartbeat", Params: json.RawMessage(`{"beat":2,"host":"db-internal-7"}`)},
		{Method: "notifications/message", Params: json.RawMessage(`{"level":"info","data":"x"}`)},
		resultEvent(`{"content":[{"type":"text","text":"done"}]}`),
	}}).seq()))
	if len(got.notes) != 0 {
		t.Errorf("%d unknown notifications were relayed", len(got.notes))
	}
	hb := findUnknown(got.final.Unknown, UnknownNotification, "notifications/vendor/heartbeat")
	if hb == nil || hb.Count != 2 || hb.Action != "dropped" || hb.Shape["beat"] != "number" || hb.Shape["host"] != "string" {
		t.Errorf("heartbeat record = %+v", got.final.Unknown)
	}
	if findUnknown(got.final.Unknown, UnknownNotification, "notifications/message") != nil {
		t.Error("a log message was recorded as unknown")
	}
	if rec, _ := json.Marshal(got.final.Unknown); strings.Contains(string(rec), "db-internal") {
		t.Errorf("a record carries a value: %s", rec)
	}

	// A template form the gateway cannot parse.
	lt := e.ListTemplates(ctx, r)
	if u := findUnknown(lt.Unknown, UnknownTemplateForm, "{/…*}"); u == nil || u.Target != "repo://{owner}/contents{/path*}" || u.Action != "passed" {
		t.Errorf("template record = %+v", lt.Unknown)
	}
	if len(lt.Unknown) != 1 {
		t.Errorf("a template the gateway can parse was recorded: %+v", lt.Unknown)
	}

	// Every record also went to the callback, once.
	mu.Lock()
	defer mu.Unlock()
	counts := map[string]int{}
	for _, u := range reported {
		counts[u.Kind+" "+u.Name]++
	}
	for key, want := range map[string]int{
		"content_block video": 1, "content_block ": 1, "result_type partial": 1, "result_type task": 1,
		"input_request sampling/createMessage": 1, "content_block hologram": 1,
		"notification notifications/vendor/heartbeat": 1, "template_form {/…*}": 1,
	} {
		if counts[key] != want {
			t.Errorf("callback got %q %d times, want %d (all: %v)", key, counts[key], want, counts)
		}
	}
	// The key groups the same thing from the same server across requests.
	if (Unknown{Server: "a", Kind: "content_block", Name: "video", Target: "t"}).Key() != (Unknown{Server: "a", Kind: "content_block", Name: "video", Target: "other"}).Key() {
		t.Error("the key depends on the target")
	}
}

// Nothing known is ever reported, and a callback that panics is ignored.
func TestKnownThingsAreNotReportedAsUnknown(t *testing.T) {
	calls := 0
	e := &Engine{OnUnknown: func(Unknown) { calls++; panic("logger down") }}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: mustCompile(t, nil), Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
	r := newRequest("direct", s)
	ctx := context.Background()
	p := &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"abc"}`)}
	first := e.CallTool(ctx, r, p)
	known := `{"resultType":"complete","content":[{"type":"text","text":"a"},{"type":"image","data":"AA==","mimeType":"image/png"},{"type":"audio","data":"AA==","mimeType":"audio/wav"},{"type":"resource_link","uri":"x://y","name":"y"},{"type":"resource","resource":{"uri":"x://y","text":"z"}}],"structuredContent":{"a":1}}`
	out := e.UpstreamToolReply(ctx, r, first.Call, p, json.RawMessage(known))
	if len(out.Unknown) != 0 || calls != 0 {
		t.Fatalf("known content was reported: %+v", out.Unknown)
	}
	var got Outcome
	mustNotPanic(t, "a panicking callback", func() {
		got = e.UpstreamToolReply(ctx, r, first.Call, p, json.RawMessage(`{"content":[{"type":"video"}]}`))
	})
	if calls != 1 || len(got.Unknown) != 1 || got.Error != nil {
		t.Errorf("calls %d, unknown %+v, error %v", calls, got.Unknown, got.Error)
	}
}

// An unknown record is free of values, and that includes field names: a key
// in the payload that is itself data, such as an email address, is not
// written to the record.
func TestAnUnknownRecordHoldsNoFieldNameFromThePayload(t *testing.T) {
	c := newStreamCase(t, "direct", nil, Limits{})
	const secret = "synthetic-customer-secret@example.invalid"
	var recorded []Unknown
	c.e.OnUnknown = func(u Unknown) { recorded = append(recorded, u) }
	raw := json.RawMessage(`{"content":[{"type":"vendor_record","` + secret + `":"value"}]}`)
	o := c.e.UpstreamToolReply(context.Background(), c.r, c.call, c.params, raw)
	if o.Error != nil {
		t.Fatalf("setup: %+v", o)
	}
	b, err := json.Marshal(recorded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secret) {
		t.Fatalf("supposedly value-free telemetry contains payload data in a field name: %s", b)
	}
}

// The filters that decide which server-chosen names reach an unknown
// record. They guard what is written to logs.
func TestUnknownRecordsKeepOnlySafeNames(t *testing.T) {
	for k, want := range map[string]bool{
		"type": true, "_meta": true, "$schema": true, "mime-type": true, "v2": true, "a123": true,
		"": false, "1abc": false, "-x": false, "a1234": false, "alice@example.com": false,
		"path/to": false, "has space": false, "a$b": false, "naïve": false,
		"abcdefghijklmnopqrstuvwxyzabcdefg":    false, // 33 characters
		"550e8400-e29b-41d4-a716-446655440000": false,
	} {
		if got := fieldName(k); got != want {
			t.Errorf("fieldName(%q) = %v, want %v", k, got, want)
		}
	}
	for name, kept := range map[string]bool{
		"notifications/tools/list_changed": true, "resource_link": true, "x.y-z": true, "v123456": true,
		"alice@example.com": false, "with space": false, "id-1234567": false,
		strings.Repeat("a", 65): false,
	} {
		got := safeName(name)
		if kept && got != name {
			t.Errorf("safeName(%q) = %q, want it kept", name, got)
		}
		if !kept && (got == name || strings.Contains(got, name)) {
			t.Errorf("safeName(%q) = %q, want the name withheld", name, got)
		}
	}
	for in, want := range map[string]string{
		"docs://a/b?token=secret": "docs://a/b",
		"docs://a/b#frag":         "docs://a/b",
		"plain":                   "plain",
	} {
		if got := safeTarget(in); got != want {
			t.Errorf("safeTarget(%q) = %q, want %q", in, got, want)
		}
	}
	if got := safeTarget("x://" + strings.Repeat("a", 400)); len([]rune(got)) != 257 || !strings.HasSuffix(got, "…") {
		t.Errorf("a long target is not cut at 256 characters: %d", len([]rune(got)))
	}
	for in, lacks := range map[string]string{
		"{/customerPath*}": "customerPath",
		"{?secretQuery}":   "secretQuery",
		"{id}":             "id}",
	} {
		if got := templateForm(in); strings.Contains(got, lacks) {
			t.Errorf("templateForm(%q) = %q, which still holds the variable name", in, got)
		}
	}
}

// Unknown notifications seen before the upstream connection broke are still
// on the final outcome and still reach the callback.
func TestUnknownsAreRecordedWhenTheReplyIsLost(t *testing.T) {
	c := newStreamCase(t, "direct", nil, Limits{})
	var recorded []Unknown
	c.e.OnUnknown = func(u Unknown) { recorded = append(recorded, u) }
	u := &upstreamSeq{events: []UpstreamEvent{{Method: "notifications/vendor", Params: json.RawMessage(`{"field":"value"}`)}}, fail: errors.New("connection reset")}
	got := c.run(u)
	if got.final == nil || got.final.Error == nil || got.final.Error.Code != CodeUpstreamLost {
		t.Fatalf("setup: %+v", got.final)
	}
	if len(got.final.Unknown) != 1 || len(recorded) != 1 {
		t.Fatalf("unknown evidence lost on broken stream: outcome=%d callback=%d", len(got.final.Unknown), len(recorded))
	}
}

// When the consumer of a stream stops early, by returning or by panicking,
// the engine stops reading the upstream, sends no made-up final event, lets
// the consumer's panic through unchanged, and still reports the unknown
// records it had gathered, once.
func TestUnknownsAreReportedWhenTheConsumerLeavesEarly(t *testing.T) {
	for _, panics := range []bool{false, true} {
		name := "return_false_control"
		if panics {
			name = "consumer_panic"
		}
		t.Run(name, func(t *testing.T) {
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

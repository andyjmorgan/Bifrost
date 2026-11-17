package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Regressions from the third review (November 15, 2026): each test
// reproduced a finding against the design note's follow-ups and now holds
// its fix.

func codexV3ReadSetup(t *testing.T, rules ...Rule) (*Engine, Request, *mcp.ReadResourceParams) {
	t.Helper()
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: codexSet(t, rules...)}
	r := Request{Route: "direct", Principal: "review-principal", Servers: []*Server{s}, Client: map[string]any{"capabilities": map[string]any{"elicitation": map[string]any{}}}}
	return &Engine{}, r, &mcp.ReadResourceParams{URI: "file:///secret.txt"}
}

func TestCodexV3BareUpstreamFieldsFailClosed(t *testing.T) { // V3-G01
	for _, hook := range []string{"resources/list.post", "resources/read.pre", "resources/read.post.result"} {
		for _, field := range []string{"name", "mimeType"} {
			t.Run(hook+"/"+field, func(t *testing.T) {
				e, r, p := codexV3ReadSetup(t, Rule{Hook: hook, When: "resource.upstream." + field + " == 'secret'", Then: map[string]string{"decision": "'deny'"}})
				o := e.ReadResource(context.Background(), r, p)
				if hook == "resources/read.post.result" {
					if o.Forward == nil {
						t.Fatalf("setup did not forward: %+v", o)
					}
					o = e.UpstreamReadReply(context.Background(), r, o.Call, p, json.RawMessage(`{"contents":[{"uri":"file:///secret.txt","text":"sensitive"}]}`))
				}
				if o.Error == nil || o.Forward != nil || o.Result != nil {
					t.Fatalf("missing upstream.%s silently became a default at %s: forward=%v result=%v error=%v", field, hook, o.Forward != nil, o.Result, o.Error)
				}
			})
		}
	}
}

func TestCodexV3BareInputAndProgressFieldsFailClosed(t *testing.T) { // V3-G02
	for _, hook := range []string{"resources/read.post.elicit", "resources/read.post.progress"} {
		t.Run(hook, func(t *testing.T) {
			e, r, p := codexV3ReadSetup(t,
				Rule{Hook: "server.session", When: "true", Then: map[string]string{"session.capabilities.elicitation": "true"}},
				Rule{Hook: hook, When: "resource.name == 'secret'", Then: map[string]string{"decision": "'deny'"}})
			first := e.ReadResource(context.Background(), r, p)
			if first.Forward == nil {
				t.Fatalf("setup: %+v", first)
			}
			if hook == "resources/read.post.elicit" {
				o := e.UpstreamReadReply(context.Background(), r, first.Call, p, json.RawMessage(`{"resultType":"input_required","inputRequests":{"q":{"method":"elicitation/create","params":{"mode":"form","message":"Give me the password","requestedSchema":{"type":"object","properties":{"answer":{"type":"string"}}}}}}}`))
				if o.Error == nil || o.Result != nil {
					t.Fatalf("input hook treated absent name as empty and relayed the question: result type=%T error=%v", o.Result, o.Error)
				}
			} else {
				u := &upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progressToken":"p","progress":1,"message":"sensitive"}`), resultEvent(`{"contents":[]}`)}}
				got := collect(e.ReadReplyStream(context.Background(), r, first.Call, p, u.seq()))
				if len(got.notes) != 0 || got.final == nil || got.final.Error != nil {
					t.Fatalf("absent name should drop the event and leave final intact: %+v", got)
				}
			}
		})
	}
}

func TestCodexV3PrefixlessUnsupportedTemplateDeny(t *testing.T) { // V3-G03
	e, r, p := codexV3ReadSetup(t, Rule{Hook: "resources/templates/list.post", When: "true", Then: map[string]string{"decision": "'deny'"}})
	// RFC 6570 expansion: base="file:///secret.txt", q="x".
	r.Servers[0].Catalog.Templates = []*mcp.ResourceTemplate{{Name: "any", URITemplate: "{+base}{?q}"}}
	p.URI = "file:///secret.txt?q=x"
	listed := e.ListTemplates(context.Background(), r)
	if listed.Error != nil || len(listed.Result.(*mcp.ListResourceTemplatesResult).ResourceTemplates) != 0 {
		t.Fatalf("setup: template was not hidden: %+v", listed)
	}
	o := e.ReadResource(context.Background(), r, p)
	if o.Forward != nil || o.Error == nil {
		t.Fatalf("URI produced by denied unsupported template forwarded: %+v", o)
	}
}

func TestCodexV3NativeSchemeAmbiguityIsNotDiscarded(t *testing.T) { // V3-G04
	e, r, p := codexV3ReadSetup(t)
	r.Route, r.Servers[0].Slug = "mux", "git"
	p.URI = "git+ssh://host/repo"
	for _, slug := range []string{"b", "c"} {
		r.Servers = append(r.Servers, &Server{Slug: slug, Catalog: Catalog{Resources: []*mcp.Resource{{Name: "repo", URI: p.URI}}}})
	}
	o := e.ReadResource(context.Background(), r, p)
	if o.Forward != nil || o.Error == nil || !strings.Contains(o.Error.Message, "Ambiguous URI") {
		t.Fatalf("two native-URI owners must yield ambiguity, not a guessed prefix: %+v", o)
	}
}

func TestCodexV3StreamCleanupPanicCannotSendSecondFinal(t *testing.T) { // V3-G05
	c := newStreamCase(t, "direct", nil, Limits{})
	var reported int
	c.e.OnFault = func(Fault) { reported++ }
	u := func(yield func(UpstreamEvent, error) bool) {
		defer func() { panic("upstream close failed") }()
		yield(resultEvent(`{"content":[{"type":"text","text":"done"}]}`), nil)
	}
	finals := 0
	for ev, err := range c.e.ToolReplyStream(context.Background(), c.r, c.call, c.params, u) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Final != nil {
			finals++
		}
	}
	if finals != 1 || reported != 1 {
		t.Fatalf("want one final and one admin fault, got finals=%d faults=%d", finals, reported)
	}
}

func TestCodexV3ProgressRewriteRespectsSizeCap(t *testing.T) { // V3-G06
	c := newStreamCase(t, "direct", []Rule{{Hook: "tools/call.post.progress", When: "true", Then: map[string]string{"progress.params.message": "'" + strings.Repeat("x", 4096) + "'"}}}, Limits{MaxResponseBytes: 1024})
	u := &upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progressToken":"p","progress":1,"message":"small"}`), resultEvent(`{"content":[]}`)}}
	got := c.run(u)
	if got.final == nil || got.final.Error != nil {
		t.Fatalf("final must retain its allowance: %+v", got.final)
	}
	for _, ev := range got.notes {
		if len(ev.Params) > c.e.Limits.maxBytes() {
			t.Fatalf("shaped progress of %d bytes exceeded the %d byte cap", len(ev.Params), c.e.Limits.maxBytes())
		}
	}
}

func TestCodexV3ProgressPanicReachesFaultCallback(t *testing.T) { // V3-G07
	c := newStreamCase(t, "direct", nil, Limits{})
	c.call.Server.Rules = boomSet(t, "tools/call.post.progress", false)
	var faults []Fault
	c.e.OnFault = func(f Fault) { faults = append(faults, f) }
	u := &upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progressToken":"p","progress":1}`), resultEvent(`{"content":[]}`)}}
	got := c.run(u)
	if len(got.notes) != 0 || got.final == nil || got.final.Error != nil {
		t.Fatalf("a progress failure must drop only that event: %+v", got)
	}
	if len(faults) != 1 || got.final.Fault == nil {
		t.Fatalf("CEL panic in progress silently omitted from fault reporting: callbacks=%d finalFault=%v hits=%+v", len(faults), got.final.Fault, got.final.Hits)
	}
}

func TestCodexV3PublicSetListingDoesNotPanic(t *testing.T) { // V3-G08
	for _, workers := range []int{1, 2} {
		t.Run(fmt.Sprint(workers), func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("exported Set.ListTools lets panic reach its caller: %T", p)
				}
			}()
			// JSON catalogs can contain null entries; the Engine wrapper handles it.
			var cat []*mcp.Tool
			if err := json.Unmarshal([]byte(`[{"name":"t","inputSchema":{"type":"object"}},null]`), &cat); err != nil {
				t.Fatal(err)
			}
			got := codexSet(t).ListTools(context.Background(), cat, Context{}, workers)
			if len(got.Issues) == 0 {
				t.Error("malformed catalog was not reported")
			}
		})
	}
}

func TestCodexV3UnknownRecordCannotContainPayloadKeys(t *testing.T) { // V3-G09
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

func TestCodexV3UnknownNotificationsSurviveLostReply(t *testing.T) { // V3-G10
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

// These controls are always on: they verify accepted decisions, not gaps.
func TestCodexV3UnlistedReadsAndGuardedFields(t *testing.T) { // V3-C01
	for _, field := range []string{"resource.name", "resource.upstream.name"} {
		t.Run(field, func(t *testing.T) {
			e, r, p := codexV3ReadSetup(t, Rule{Hook: "resources/list.post", When: "resource.uri.startsWith('private://') && " + field + " == 'secret'", Then: map[string]string{"decision": "'deny'"}})
			o := e.ReadResource(context.Background(), r, p)
			if o.Forward == nil {
				t.Fatalf("false URI guard should allow unlisted read: %+v", o)
			}
		})
	}
}

func TestCodexV3ConfirmationConcurrentRedemption(t *testing.T) { // V3-C02
	c := newStreamCase(t, "direct", nil, Limits{})
	c.r.Principal = "opaque-review-principal"
	c.call.Server.Rules = codexSet(t, Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{"confirm": "elicit('ok','Proceed?')"}})
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
			p.InputResponses = mcp.InputResponseMap{KeyPrefix + "ok": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}}
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

func TestCodexV3VersionGatesWithExplicitMissingVersion(t *testing.T) { // V3-C03
	for _, version := range []string{"", "unknown", "2025-03-26", "2026-07-28 "} {
		t.Run(fmt.Sprintf("version=%q", version), func(t *testing.T) {
			c := newStreamCase(t, "direct", []Rule{
				{Hook: "server.session", When: "true", Then: map[string]string{"session.capabilities.elicitation": "true"}},
				{Hook: "server.settings", When: "true", Then: map[string]string{"settings.progress": "true", "settings.tasks": "true"}},
			}, Limits{})
			c.call.Server.Info.ProtocolVersion = version
			ci, _ := c.e.Connect(context.Background(), c.r, c.call.Server, "review")
			if _, yes := ci.Capabilities["elicitation"]; yes {
				t.Fatal("advertised elicitation")
			}
			sw, _ := c.e.Config(context.Background(), c.r, c.call.Server)
			if sw["progress"] || sw["tasks"] {
				t.Fatalf("gate overriden: %v", sw)
			}
			got := c.run(&upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progressToken":"p","progress":1}`), resultEvent(`{"content":[]}`)}})
			if len(got.notes) != 0 || got.final == nil || got.final.Error != nil {
				t.Fatalf("version should suppress progress and preserve result: %+v", got)
			}
			b, _ := json.Marshal(got.final.Result)
			if !strings.Contains(string(b), `"resultType":"complete"`) {
				t.Fatalf("missing discriminator: %s", b)
			}
		})
	}
}

package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resources/list and resources/read: what each hook reads, what a result rule
// may change, and reads of a URI the server does not list.

// A resource a list rule retitled: every read hook sees the gateway's title
// on the item and the server's under upstream, on the direct route and on
// the mux, where the client's URI carries the slug and rules see it without.
func TestEveryResourceHookReadsBothSides(t *testing.T) {
	ctx := context.Background()
	sides := "resource.uri + ' ' + resource.title + '<' + resource.upstream.title + ' ' + request.params.uri + '<' + request.upstream.params.uri"
	want := "docs://a gateway title<server title docs://a<docs://a"
	cat := Catalog{Resources: []*mcp.Resource{{URI: "docs://a", Name: "a", Title: "server title"}}}
	s := newServer(t, cat,
		Rule{Hook: "resources/list.post", When: "true", Then: map[string]string{"resource.title": "'gateway title'"}},
		Rule{Hook: "resources/list.post", When: "resource.title == 'gateway title' && resource.upstream.title == 'server title'", Then: map[string]string{"resource.description": "'later rule saw both'"}},
		Rule{Hook: "resources/read.pre", When: "resource.title != 'gateway title' || resource.upstream.title != 'server title' || request.params.uri != resource.uri", Then: map[string]string{KeyDecision: "'deny'", KeyReason: "'pre saw the wrong sides'"}},
		Rule{Hook: "resources/read.post.progress", When: "true", Then: map[string]string{"progress.params.message": sides}},
		Rule{Hook: "resources/read.post.elicit", When: "true", Then: map[string]string{"elicitation.params.message": sides}},
		Rule{Hook: "resources/read.post.result", When: "true", Then: map[string]string{"response.result.contents": "response.result.contents.map(c, {'uri': c.uri, 'text': " + sides + " + ' | server said ' + response.upstream.result.contents[0].text})"}},
	)
	e := &Engine{}
	for route, uri := range map[string]string{"direct": "docs://a", "mux": "a+docs://a"} {
		r := newRequest("direct", s)
		r.Route = route
		if got := asJSON(e.ListResources(ctx, r).Result); !strings.Contains(got, "later rule saw both") {
			t.Errorf("%s list hook: %s", route, got)
		}
		p := &mcp.ReadResourceParams{URI: uri}
		o := e.ReadResource(ctx, r, p)
		if o.Forward == nil {
			t.Fatalf("%s: not forwarded: %s", route, outcomeText(o))
		}
		first := collect(e.ReadReplyStream(ctx, r, o.Call, p, (&upstreamSeq{events: []UpstreamEvent{progressEvent(`{"progress":1,"message":"half"}`), resultEvent(serverQuestion)}}).seq()))
		if len(first.notes) != 1 || !strings.Contains(plainJSON(first.notes[0].Params), want) {
			t.Errorf("%s progress hook: %s", route, asJSON(first.notes))
		}
		if got := plainJSON(first.final.Result); !strings.Contains(got, want) {
			t.Errorf("%s elicit hook: %s", route, got)
		}
		ir, ok := first.final.Result.(InputRequired)
		if !ok {
			t.Fatalf("%s: the server's question did not reach the client", route)
		}
		p2 := &mcp.ReadResourceParams{URI: uri, RequestState: ir.RequestState, InputResponses: serverAnswer()}
		o2 := e.ReadResource(ctx, r, p2)
		if o2.Call == nil {
			t.Fatalf("%s: the continuation was not forwarded: %s", route, outcomeText(o2))
		}
		done := e.UpstreamReadReply(ctx, r, o2.Call, p2, json.RawMessage(`{"contents":[{"uri":"docs://a","text":"A"}]}`))
		if got := plainJSON(done.Result); !strings.Contains(got, want+" | server said A") {
			t.Errorf("%s result hook after the continuation: %s", route, got)
		}
	}
}

// After a list rule retitles a resource, every read hook still reads the
// server's title under resource.upstream: a deny written against it refuses
// the read on the pre hook, refuses the result and the server's question, and
// drops a progress event.
func TestResourceHooksReadUpstreamFieldsAfterAListRule(t *testing.T) {
	for _, hook := range []string{"resources/read.pre", "resources/read.post.result", "resources/read.post.elicit", "resources/read.post.progress"} {
		t.Run(hook, func(t *testing.T) {
			e, r, p := unlistedRead(t,
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

// The read result hook runs once over the whole response. A rule cannot
// bring in an entry the read did not return, in any of the forms tried; an
// entry the caller may not read is gone before rules run and is not under
// response.upstream either; one entry that makes a rule fail refuses the
// whole read; an audit rule changes nothing.
func TestAReadResultRuleCannotBringInAnEntry(t *testing.T) {
	cat := Catalog{Resources: []*mcp.Resource{{URI: "docs://a", Name: "a"}, {URI: "docs://b", Name: "b"}, {URI: "docs://hidden", Name: "hidden"}}}
	hide := Rule{Hook: "resources/list.post", When: "resource.uri == 'docs://hidden'", Then: map[string]string{KeyDecision: "'deny'"}}
	set := func(expr string) Rule {
		return Rule{Hook: "resources/read.post.result", When: "true", Then: map[string]string{"response.result.contents": expr}}
	}
	reply := `{"contents":[{"uri":"docs://a","text":"A"},{"uri":"docs://b","text":"B"},{"uri":"docs://hidden","text":"HIDDEN"}]}`
	for name, rule := range map[string]Rule{
		"the hidden entry":        set("response.result.contents + [{'uri': 'docs://hidden', 'text': 'x'}]"),
		"a new URI":               set("response.result.contents + [{'uri': 'docs://new', 'text': 'x'}]"),
		"an entry with no URI":    set("response.result.contents + [{'text': 'x'}]"),
		"an entry that is no map": set("response.result.contents + ['x']"),
		"a URI that is a number":  set("[{'uri': 5, 'text': 'x'}]"),
		"a trailing slash":        set("[{'uri': 'docs://a/', 'text': 'x'}]"),
	} {
		o := readReply(t, newServer(t, cat, hide, rule), "docs://a", reply)
		if o.Error == nil || strings.Contains(outcomeText(o), "HIDDEN") {
			t.Errorf("a rule added %s: %s", name, outcomeText(o))
		}
	}
	o := readReply(t, newServer(t, cat, hide, set("response.upstream.result.contents")), "docs://a", reply)
	if got := outcomeText(o); o.Error != nil || strings.Contains(got, "HIDDEN") || !strings.Contains(got, `"text":"B"`) {
		t.Errorf("response.upstream held an entry the caller may not read: %s", got)
	}
	o = readReply(t, newServer(t, cat, hide, Rule{Hook: "resources/read.post.result", When: "response.result.contents.exists(c, c.text.contains('secret'))", Then: map[string]string{KeyDecision: "'deny'"}}),
		"docs://a", `{"contents":[{"uri":"docs://a","text":"A"},{"uri":"docs://b","blob":"QUJD"}]}`)
	if o.Error == nil {
		t.Errorf("an entry the rule could not evaluate passed with the rest: %s", outcomeText(o))
	}
	o = readReply(t, newServer(t, cat, hide, set("response.result.contents.filter(c, !c.text.contains('secret'))")),
		"docs://a", `{"contents":[{"uri":"docs://a","text":"A"},{"uri":"docs://b","blob":"QUJD"}]}`)
	if o.Error == nil {
		t.Errorf("a set whose value could not be evaluated passed: %s", outcomeText(o))
	}
	o = readReply(t, newServer(t, cat, hide, Rule{Hook: "resources/read.post.result", Mode: ModeAudit, When: "true", Then: map[string]string{"response.result.contents": "[]"}}), "docs://a", reply)
	if got := outcomeText(o); !strings.Contains(got, `"text":"A"`) || !strings.Contains(got, `"text":"B"`) {
		t.Errorf("an audit rule changed the contents: %s", got)
	}
}

// A read result's resultType and _meta are in the document a rule reads,
// under response.result and under response.upstream.result. A rule that
// compares them does not fail on a reply that has them.
func TestAReadResultRuleCanReadResultTypeAndMeta(t *testing.T) {
	for _, field := range []string{"response.result.resultType", "response.upstream.result.resultType", "response.result._meta.label", "response.upstream.result._meta.label"} {
		t.Run(field, func(t *testing.T) {
			s := newServer(t, Catalog{}, Rule{Hook: "resources/read.post.result", When: field + " == 'block'", Then: map[string]string{KeyDecision: "'deny'"}})
			o := readReply(t, s, "docs://a", `{"resultType":"complete","_meta":{"label":"safe"},"contents":[{"uri":"docs://a","text":"OK"}]}`)
			if o.Error != nil {
				t.Errorf("present response field vanished: %s", outcomeText(o))
			}
		})
	}
}

// An entry of a read result is a closed map in the tree. A server can still
// send another key on it, and the gateway passes that key to the client and
// into the document a rule reads. A rule that reads such a key through a
// macro variable, behind a guard, is saved.
func TestAKeyAServerAddsToAReadEntryCanBeReadByARule(t *testing.T) {
	s := newServer(t, Catalog{Resources: []*mcp.Resource{{URI: "docs://a", Name: "a"}}})
	o := readReply(t, s, "docs://a", `{"contents":[{"uri":"docs://a","text":"A","annotations":{"audience":["user"]}}]}`)
	if !strings.Contains(outcomeText(o), `"annotations"`) {
		t.Skip("the gateway no longer passes the key on")
	}
	for _, when := range []string{"response.result.contents.exists(c, has(c.annotations))", "response.result.contents.exists(c, c.?annotations.?audience.orValue([]).size() > 0)"} {
		if probs, _ := saveRule("resources/read.post.result", when, nil); probs != nil {
			t.Errorf("refused: %s: %v", when, probs)
		}
	}
}

// An entry of a read result that is not an object, or whose uri is not a
// string, is not turned into an entry for the URI that was read: a string, a
// number and a null do not each become {"uri": …} with no text and no blob.
// The client gets the well-formed entry alone, or an error.
func TestAReadEntryThatIsNotAnObjectIsNotTurnedIntoOne(t *testing.T) {
	s := newServer(t, Catalog{Resources: []*mcp.Resource{{URI: "docs://a", Name: "a"}}})
	o := readReply(t, s, "docs://a", `{"contents":[{"uri":"docs://a","text":"A"},"junk",5,null,{"uri":5,"text":"other"}]}`)
	contents, _ := o.Result.(map[string]any)["contents"].([]any)
	if o.Error == nil && len(contents) != 1 {
		t.Errorf("the server sent one well-formed entry and the client got %d: %s", len(contents), outcomeText(o))
	}
}

// A resource's annotations can be set as a whole map. A key MCP does not name
// in that map must not be seen by the next list rule and then lost on the way
// to the client: the set is refused, or the client gets the key.
func TestAResourceAnnotationMCPDoesNotNameIsRefusedOrKept(t *testing.T) {
	then := map[string]string{"resource.annotations": "{'risk': 'high', 'priority': 0.5}"}
	if probs, _ := saveRule("resources/list.post", "true", then); probs != nil {
		return // refused when saved: nothing is lost
	}
	s := newServer(t, Catalog{Resources: []*mcp.Resource{{URI: "docs://a", Name: "a"}}},
		Rule{Hook: "resources/list.post", When: "true", Then: then},
		Rule{Hook: "resources/list.post", When: "true", Then: map[string]string{"resource.description": "'risk=' + dyn(resource.annotations).?risk.orValue('lost')"}})
	got := asJSON((&Engine{}).ListResources(context.Background(), newRequest("direct", s)).Result)
	if strings.Contains(got, "risk=high") && !strings.Contains(got, `"risk":"high"`) {
		t.Errorf("the next rule saw the annotation and the client did not get it: %s", got)
	}
}

// Annotations set as a whole map on a resource or a template are checked key
// by key when the request arrives: a key MCP does not name fails the rule.
func TestAnnotationsSetAsAWholeMapAreCheckedKeyByKey(t *testing.T) {
	for _, hook := range []string{"resources/list.post", "resources/templates/list.post"} {
		root := "resource"
		cat := Catalog{Resources: []*mcp.Resource{{URI: "docs://a", Name: "a"}}}
		if strings.Contains(hook, "templates") {
			root = "template"
			cat.Templates = []*mcp.ResourceTemplate{{URITemplate: "docs://h/{id}", Name: "t"}}
		}
		s := newServer(t, cat, Rule{Hook: hook, When: "true", Then: map[string]string{root + ".annotations": "{'risk': 'high'}"}})
		var o Outcome
		if root == "resource" {
			o = (&Engine{}).ListResources(context.Background(), newRequest("direct", s))
		} else {
			o = (&Engine{}).ListTemplates(context.Background(), newRequest("direct", s))
		}
		if !strings.Contains(hitErrors(o.Hits), `has no "risk"`) {
			t.Errorf("whole-map fixed keys not checked: %s %s", hook, outcomeText(o))
		}
	}
}

// On a read of a URI the server does not list, the resource has no name and
// no mimeType. A rule that reads one of them bare under resource.upstream
// fails, and the failure is closed on the list hook, the pre hook and the
// result hook: an absent field does not read as an empty default.
func TestAResourceRuleReadingAnAbsentUpstreamFieldFailsClosed(t *testing.T) {
	for _, hook := range []string{"resources/list.post", "resources/read.pre", "resources/read.post.result"} {
		for _, field := range []string{"name", "mimeType"} {
			t.Run(hook+"/"+field, func(t *testing.T) {
				e, r, p := unlistedRead(t, Rule{Hook: hook, When: "resource.upstream." + field + " == 'secret'", Then: map[string]string{"decision": "'deny'"}})
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

// On the reply hooks of a read of an unlisted URI, a rule that reads the
// absent resource.name fails closed: the server's question is refused on the
// elicit hook, and on the progress hook the event is dropped and the result
// left intact.
func TestAnElicitOrProgressRuleReadingAnAbsentFieldFailsClosed(t *testing.T) {
	for _, hook := range []string{"resources/read.post.elicit", "resources/read.post.progress"} {
		t.Run(hook, func(t *testing.T) {
			e, r, p := unlistedRead(t,
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

// A list rule that denies by URI prefix and then reads the resource's name
// does not stop an unlisted read of another URI: the guard is false, so the
// absent name is never read. This is by design.
func TestAnUnlistedReadPassesADenyWhoseURIGuardIsFalse(t *testing.T) {
	for _, field := range []string{"resource.name", "resource.upstream.name"} {
		t.Run(field, func(t *testing.T) {
			e, r, p := unlistedRead(t, Rule{Hook: "resources/list.post", When: "resource.uri.startsWith('private://') && " + field + " == 'secret'", Then: map[string]string{"decision": "'deny'"}})
			o := e.ReadResource(context.Background(), r, p)
			if o.Forward == nil {
				t.Fatalf("false URI guard should allow unlisted read: %+v", o)
			}
		})
	}
}

// A template in a form the gateway cannot parse, listed beside a resource,
// does not get in the way of an exact read of that resource or of its reply.
func TestATemplateTheGatewayCannotParseDoesNotBreakAnExactRead(t *testing.T) {
	s := &Server{Slug: "a", Rules: mustCompileUncapped(t), Catalog: Catalog{
		Resources: []*mcp.Resource{{Name: "doc", URI: "file:///doc"}},
		Templates: []*mcp.ResourceTemplate{{Name: "query", URITemplate: "file:///x{?q}"}},
	}}
	e := &Engine{}
	r := Request{Route: "direct", Servers: []*Server{s}}
	p := &mcp.ReadResourceParams{URI: "file:///doc"}
	call := e.ReadResource(context.Background(), r, p)
	if call.Forward == nil {
		t.Fatalf("exact listed resource was not readable: %+v", call)
	}
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("unrelated unsupported template caused a read-result panic: %v", p)
		}
	}()
	out := e.UpstreamReadReply(context.Background(), r, call.Call, p, json.RawMessage(`{"resultType":"complete","contents":[{"uri":"file:///doc","text":"ok"}]}`))
	if out.Error != nil || out.Result == nil {
		t.Fatalf("exact read reply failed: %+v", out)
	}
}

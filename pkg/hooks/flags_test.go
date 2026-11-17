package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Request flags: search is a flag beside the route, and rules read flags by
// name.

// The route is "direct" or "mux" and the two are exclusive. Search mode is
// a separate flag that can be on for either, and rules read it as `search`.
func TestSearchIsAFlagNotARoute(t *testing.T) {
	rules := []Rule{
		{Name: "mux only", Hook: "tools/call.post.result", When: "request.route == 'mux'", Then: map[string]string{"response.result.content": "[{'type': 'text', 'text': 'mux'}]"}},
		{Name: "direct only", Hook: "tools/call.post.result", When: "request.route == 'direct'", Then: map[string]string{"response.result.content": "[{'type': 'text', 'text': 'direct'}]"}},
		{Name: "search only", Hook: "tools/call.post.result", When: "request.flags.search", Then: map[string]string{"response.result.structuredContent": "{'search': true}"}},
		{Name: "never", Hook: "tools/call.post.result", When: "request.route == 'search'", Then: map[string]string{"decision": "'deny'"}},
	}
	e := &Engine{}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: mustCompile(t, rules), Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		route     string
		search    bool
		tool      string
		wantRoute string
		wantFlag  bool
	}{
		{"direct", "direct", false, "t", "direct", false},
		{"direct with search", "direct", true, "t", "direct", true},
		{"mux", "mux", false, "a" + Separator + "t", "mux", false},
		{"mux with search", "mux", true, "a" + Separator + "t", "mux", true},
	} {
		r := newRequest(tc.route, s)
		r.Flags = map[string]bool{FlagSearch: tc.search}
		p := &mcp.CallToolParamsRaw{Name: tc.tool, Arguments: json.RawMessage(`{"q":"abc"}`)}
		first := e.CallTool(ctx, r, p)
		if first.Forward == nil {
			t.Fatalf("%s: not forwarded: %+v %+v", tc.name, first.Error, first.Result)
		}
		out := e.UpstreamToolReply(ctx, r, first.Call, p, json.RawMessage(`{"content":[{"type":"text","text":"x"}]}`))
		b, _ := json.Marshal(out.Result)
		if !strings.Contains(string(b), `"text":"`+tc.wantRoute+`"`) {
			t.Errorf("%s: rules saw the wrong route: %s", tc.name, b)
		}
		if got := strings.Contains(string(b), `"search":true`); got != tc.wantFlag {
			t.Errorf("%s: search flag seen by rules = %v, want %v: %s", tc.name, got, tc.wantFlag, b)
		}
		if strings.Contains(string(b), "refused") {
			t.Errorf("%s: a rule saw route == 'search': %s", tc.name, b)
		}
	}
	// The mux's own instructions tool is listed on the plain mux, and not in
	// search mode.
	lists := func(route string, search bool) bool {
		r := newRequest(route, s)
		r.Flags = map[string]bool{FlagSearch: search}
		out := e.ListTools(ctx, r)
		b, _ := json.Marshal(out.Result)
		return strings.Contains(string(b), InstructionsTool)
	}
	if !lists("mux", false) || lists("mux", true) || lists("direct", false) || lists("direct", true) {
		t.Errorf("instructions tool listed: mux %v, mux+search %v, direct %v, direct+search %v",
			lists("mux", false), lists("mux", true), lists("direct", false), lists("direct", true))
	}
}

// Flags are read by name. Every known flag is always there, false when the
// request did not set it; a name that is not a flag is refused when the
// rule is saved.
func TestFlagsAreNamedAndChecked(t *testing.T) {
	rules := []Rule{
		{Hook: "tools/call.post.result", When: "request.flags.cli && !request.flags.search", Then: map[string]string{"response.result.content": "[{'type': 'text', 'text': 'cli'}]"}},
		{Hook: "tools/call.post.result", When: "request.flags.search && request.flags.cli", Then: map[string]string{"response.result.content": "[{'type': 'text', 'text': 'both'}]"}},
	}
	e := &Engine{}
	s := &Server{Slug: "a", Generation: "1", Info: ServerInfo{ProtocolVersion: ProtocolCurrent}, Rules: mustCompile(t, rules), Catalog: Catalog{Tools: []*mcp.Tool{typedTool()}}}
	ctx := context.Background()
	for _, tc := range []struct {
		flags map[string]bool
		want  string
	}{
		{nil, "x"},
		{map[string]bool{}, "x"},
		{map[string]bool{"cli": true}, "cli"},
		{map[string]bool{"cli": true, "search": true}, "both"},
		{map[string]bool{"search": true}, "x"},
		{map[string]bool{"cli": false}, "x"},
		{map[string]bool{"made-up": true}, "x"}, // a name the engine does not know is ignored
	} {
		r := newRequest("direct", s)
		r.Flags = tc.flags
		p := &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"abc"}`)}
		first := e.CallTool(ctx, r, p)
		if first.Forward == nil {
			t.Fatalf("flags %v: not forwarded: %+v", tc.flags, first.Error)
		}
		out := e.UpstreamToolReply(ctx, r, first.Call, p, json.RawMessage(`{"content":[{"type":"text","text":"x"}]}`))
		b, _ := json.Marshal(out.Result)
		if !strings.Contains(string(b), `"text":"`+tc.want+`"`) {
			t.Errorf("flags %v: got %s, want text %q", tc.flags, b, tc.want)
		}
	}
	for _, bad := range []string{
		"request.flags.serach",                      // a typo
		"request.flags.madeup == true",              // not a flag
		"'search' in request.flags",                 // must be read by name
		"request.flags['serach']",                   // a typo, by key
		"size(request.flags) > 0",                   // must be read by name
		"has(request.flags.debug)",                  // not a flag, even under has()
		"flags.search",                              // flags live under the request
		"search",                                    // there was never a top-level one
		"request.route == 'mux' && request.flags.x", // one bad flag in a longer condition
	} {
		if _, probs := Compile([]Rule{{Hook: "tools/list.post", When: bad, Then: map[string]string{"decision": "'deny'"}}}); probs == nil {
			t.Errorf("compiled, but should not: %s", bad)
		}
	}
	for _, good := range []string{"request.flags.search", "request.flags.cli", "!request.flags.cli", "has(request.flags.cli) && request.flags.cli",
		"request.flags['search']", "request.route == 'mux' && (request.flags.search || request.flags.cli)"} {
		if _, probs := Compile([]Rule{{Hook: "tools/list.post", When: good, Then: map[string]string{"decision": "'deny'"}}}); probs != nil {
			t.Errorf("did not compile: %s: %v", good, probs)
		}
	}
}

// Flags are read by name, and the name must be a flag, in every form an
// expression can take: under has(), with a leading dot, by a constant key,
// inside a macro. A computed key, a macro over the flags, a ternary and an
// unknown name are refused when the rule is saved. A macro variable that
// happens to be called flags is not the request's flags.
func TestFlagsAreReadByNameInEveryForm(t *testing.T) {
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

package hooks

import (
	"context"
	"encoding/json"
	"math/rand"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestListingIsByteConsistent lists the real corpus twice, once with the
// catalog shuffled, and requires identical bytes (F35: caches stay
// byte-consistent whatever order the upstream returns).
func TestListingIsByteConsistent(t *testing.T) {
	tools := realTools(t)
	set, probs := Compile(realisticRules())
	if probs != nil {
		t.Fatal(probs)
	}
	// This tests ordering, not the budget; under the race detector the
	// summed rule time of a parallel listing exceeds the 2 s default.
	e := &Engine{Signer: Signer{Key: []byte("k")}, Limits: Limits{Budget: time.Minute}}
	list := func(cat []*mcp.Tool) []byte {
		s := &Server{Slug: "dd", Rules: set, Catalog: Catalog{Tools: cat}}
		out := e.ListTools(context.Background(), Request{Route: "mux", Servers: []*Server{s}, Caller: map[string]any{"role": "member"}})
		if out.Error != nil || out.Result == nil {
			t.Fatalf("listing failed: %+v", out.Error)
		}
		if n := len(out.Result.(*mcp.ListToolsResult).Tools); n < 1000 {
			t.Fatalf("only %d tools listed; expected the real corpus", n)
		}
		b, err := json.Marshal(out.Result)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	a := list(tools)
	shuffled := append([]*mcp.Tool(nil), tools...)
	rand.New(rand.NewSource(7)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	b := list(shuffled)
	if string(a) != string(b) {
		t.Fatalf("listing bytes differ with catalog order (%d vs %d bytes)", len(a), len(b))
	}
}

// TestRealCallsPolicyOracle checks exact outcomes for the real call
// examples where the realistic policy's effect is predictable: a limit over
// 100 is clamped to 100, env "prod" is refused for members.
func TestPolicyCallOracle(t *testing.T) {
	set, probs := Compile(realisticRules())
	if probs != nil {
		t.Fatal(probs)
	}
	e := &Engine{Signer: Signer{Key: []byte("k")}}
	cat := []*mcp.Tool{{Name: "search", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"limit": map[string]any{"type": "integer"}, "env": map[string]any{"type": "string"}, "q": map[string]any{"type": "string"}}}}}
	s := &Server{Slug: "h", Rules: set, Catalog: Catalog{Tools: cat}}
	r := Request{Route: "direct", Servers: []*Server{s}, Caller: map[string]any{"userId": "u", "role": "member"}}
	cases := []struct {
		args string
		want string // "forward:<limit>" or "refused"
	}{
		{`{"q":"a","limit":250}`, "error"}, // over the exposed maximum: refused before call rules run
		{`{"q":"a","limit":20}`, "error"},  // over the modify maximum? no: 20 is fine
		{`{"q":"a","env":"prod"}`, "refused"},
		{`{"q":"a","env":"staging","limit":100}`, "forward:100"},
	}
	cases[1].want = "forward:20"
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

func FuzzScenarioLoader(f *testing.F) {
	for _, s := range []string{`[]`, `{"name":"x","servers":[],"steps":[]}`, `{"name":"x"}{"name":"y"}`, `[{"servers":[{"slug":"a","rules":[{"hook":"tools/filter","when":"true","then":{"decision":"'deny'"}}]}],"steps":[{"op":"tools/list"}]}]`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 4096 {
			t.Skip()
		}
		ss, err := ParseScenarios([]byte(s))
		if err != nil {
			return
		}
		for _, sc := range ss {
			if sc == nil || len(sc.Steps) > 8 || len(sc.Servers) > 4 {
				continue
			}
			sc.Run(context.Background()) // must not panic
		}
	})
}

// TestExpectRequiresPresentValues: missing, null, empty and wrong-type
// values are not interchangeable in len and notContains (V2-F05).
func TestExpectRequiresPresentValues(t *testing.T) {
	zero, one := 0, 1
	out := map[string]any{"empty": []any{}, "null": nil, "num": 3, "list": []any{"a"}}
	cases := []struct {
		ex   Expect
		pass bool
	}{
		{Expect{Path: "empty", Len: &zero}, true},
		{Expect{Path: "missing", Len: &zero}, false},
		{Expect{Path: "null", Len: &zero}, false},
		{Expect{Path: "num", Len: &zero}, false},
		{Expect{Path: "list", Len: &one}, true},
		{Expect{Path: "list", NotContains: "secret"}, true},
		{Expect{Path: "missing", NotContains: "secret"}, false},
		{Expect{Path: "missing[*].name", NotContains: "secret"}, false},
	}
	for _, c := range cases {
		if got := c.ex.check(out, nil) == ""; got != c.pass {
			t.Errorf("%s len=%v notContains=%v: pass=%v, want %v", c.ex.Path, c.ex.Len, c.ex.NotContains, got, c.pass)
		}
	}
}

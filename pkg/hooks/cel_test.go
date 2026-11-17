package hooks

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// What a rule's expression may name, and what it costs.

// CEL takes // comments, and a comment runs to the end of the expression: the
// engine compiles a condition that holds one as the text before it. This
// matters to the text form of a rule, which joins continuation lines with a
// space, so whatever follows a comment on a later line is lost with it.
func TestACELCommentRunsToTheEndOfTheExpression(t *testing.T) {
	cat := Catalog{Tools: []*mcp.Tool{{Name: "a", InputSchema: qSchema}, {Name: "b", InputSchema: qSchema}}}
	s := newServer(t, cat, Rule{Hook: "tools/list.post", When: "tool.name == 'a' // or the other one || tool.name == 'b'", Then: map[string]string{KeyDecision: "'deny'"}})
	if got := asJSON((&Engine{}).ListTools(context.Background(), newRequest("direct", s)).Result); !strings.Contains(got, `"name":"b"`) || strings.Contains(got, `"name":"a"`) {
		t.Errorf("listed %s", got)
	}
}

// The variable that carries the mux context to slugUris, and the function
// behind the macro, are not in the tree. A rule that names either is refused
// when it is saved.
func TestARuleCannotNameTheEnginesInternalVariables(t *testing.T) {
	for _, when := range []string{"__mux.slug == 'a'", "size(__mux.owned.exact) > 0", "__slugUris('see docs://x', {'route': 'mux', 'slug': 'other', 'owned': {'exact': ['docs://x']}}) != ''"} {
		if probs, _ := saveRule("resources/read.post.result", when, nil); probs == nil {
			t.Errorf("%s compiled", when)
		}
	}
}

// The CEL cost of a helper such as drop() grows with the data it walks, so
// the cost limit bounds a helper as it bounds a plain expression.
func TestAHelpersCostGrowsWithWhatItWalks(t *testing.T) {
	env, err := envFor(Hooks["tools/call.post.result"])
	if err != nil {
		t.Fatal(err)
	}
	ce, err := compileExpr(env, Hooks["tools/call.post.result"], "drop(response.result.structuredContent, ['items.*.secret'])", nil, Options{CostLimit: 1 << 40})
	p := ce.prg
	if err != nil {
		t.Fatal(err)
	}
	cost := func(n int) uint64 {
		rows := make([]any, n)
		for i := range rows {
			rows[i] = map[string]any{"secret": "remove", "public": "keep"}
		}
		_, details, err := p.Eval(map[string]any{"response": map[string]any{"result": map[string]any{"structuredContent": map[string]any{"items": rows}}}})
		if err != nil {
			t.Fatal(err)
		}
		if details == nil || details.ActualCost() == nil {
			t.Fatal("no cost tracking")
		}
		return *details.ActualCost()
	}
	small, large := cost(1), cost(10000)
	if large <= small {
		t.Fatalf("helper traversed 10000 rows for the same CEL cost: small=%d large=%d", small, large)
	}
}

package rules

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLastMatchWins(t *testing.T) {
	c, probs := Compile([]Rule{
		{Name: "wildcard", Action: Allow, Kinds: []string{"tool"}, When: `tool.name.startsWith("get_")`},
		{Name: "no destructive", Action: Deny, When: `tool.annotations.destructiveHint`},
	})
	if probs != nil {
		t.Fatal(probs)
	}
	f := false
	tool := &mcp.Tool{Name: "get_page", Annotations: &mcp.ToolAnnotations{DestructiveHint: &f}}
	ok, hits := c.Decide(context.Background(), false, Subject{Kind: "tool", Name: tool.Name, Def: tool})
	if !ok || len(hits) != 1 {
		t.Fatalf("wildcard should re-enable: %v %v", ok, hits)
	}
	tool2 := &mcp.Tool{Name: "get_rid"} // destructiveHint defaults to true
	ok, hits = c.Decide(context.Background(), true, Subject{Kind: "tool", Name: tool2.Name, Def: tool2})
	if ok || len(hits) != 2 {
		t.Fatalf("deny should win last: %v %v", ok, hits)
	}
	// A prompt skips the tool-only rule; the any-kind rule reads tool.* on
	// it, errors, and an error denies: scope rules to kinds.
	ok, hits = c.Decide(context.Background(), true, Subject{Kind: "prompt", Name: "summarize", Def: map[string]any{"name": "summarize"}})
	if ok || len(hits) != 1 || hits[0].Error == "" {
		t.Fatalf("unscoped rule on a prompt should fail closed: %v %v", ok, hits)
	}
	scoped, _ := Compile([]Rule{{Name: "no destructive", Action: Deny, Kinds: []string{"tool"}, When: `tool.annotations.destructiveHint`}})
	if ok, _ := scoped.Decide(context.Background(), true, Subject{Kind: "prompt", Name: "summarize"}); !ok {
		t.Fatal("prompt should keep its base outcome under a tool-scoped rule")
	}
}

func TestCompileProblems(t *testing.T) {
	_, probs := Compile([]Rule{{Name: "bad", Action: "maybe", When: "1 +"}, {Name: "str", Action: Allow, When: `"x"`}})
	if len(probs) != 2 {
		t.Fatalf("want 2 problems, got %v", probs)
	}
}

func TestEvalErrorFailsClosed(t *testing.T) {
	c, _ := Compile([]Rule{{Name: "boom", Action: Allow, When: `tool.meta["x"] == "y"`}})
	ok, hits := c.Decide(context.Background(), true, Subject{Kind: "tool", Name: "a", Def: &mcp.Tool{Name: "a"}})
	if ok || len(hits) != 1 || hits[0].Error == "" {
		t.Fatalf("missing key should deny with an error: %v %v", ok, hits)
	}
}

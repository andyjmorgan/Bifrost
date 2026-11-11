package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestApply(t *testing.T) {
	f := false
	p := &Policy{Name: "p", Rules: []Rule{
		{Name: "hide deletes", When: `tool.baseName.startsWith("delete") || (tool.annotations.destructiveHint && !tool.annotations.readOnlyHint)`, Then: map[string]string{"visible": "false"}},
		{Name: "confirm sends", When: `tool.baseName.matches("^send_")`, Then: map[string]string{"confirm": "true", "confirmMessage": `"Send via " + server.slug + "?"`, "description": `tool.description + " (confirmed)"`}},
		{Name: "meta", When: `has(tool.meta.x) && tool.meta.x == "y"`, Then: map[string]string{"annotations.readOnlyHint": "true"}},
		{Name: "missing key is a non-match", When: `tool.meta.missing == 1`, Then: map[string]string{"visible": "false"}},
		{Name: "sees earlier mutations", When: `tool.description.endsWith("(confirmed)")`, Then: map[string]string{"title": `"Guarded " + tool.baseName`}},
	}}
	c, err := Compile(p)
	if err != nil {
		t.Fatal(err)
	}
	tools := []*mcp.Tool{
		{Name: "delete_note", Description: "d"},
		{Name: "send_message", Description: "s", Annotations: &mcp.ToolAnnotations{DestructiveHint: &f}},
		{Name: "read_note", Description: "r", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}, Meta: mcp.Meta{"x": "y"}},
		{Name: "unannotated", Description: "u"}, // destructiveHint defaults to true
	}
	ds := Apply(context.Background(), []*Compiled{c}, tools, Server{Slug: "tg"}, "mux")
	if ds[0].Visible {
		t.Error("delete_note should be hidden")
	}
	if !ds[1].Visible || !ds[1].Confirm || ds[1].ConfirmMessage != "Send via tg?" || ds[1].Tool.Description != "s (confirmed)" || ds[1].Tool.Title != "Guarded send_message" {
		t.Errorf("send_message: %+v tool=%+v", ds[1], ds[1].Tool)
	}
	if !ds[2].Visible || !ds[2].Tool.Annotations.ReadOnlyHint {
		t.Errorf("read_note: %+v", ds[2])
	}
	if ds[3].Visible {
		t.Error("unannotated should be hidden: destructiveHint defaults to true")
	}
	if len(ds[3].Hits) == 0 || ds[3].Hits[len(ds[3].Hits)-1].Error == "" {
		t.Errorf("expected the missing-key rule to record an error, hits=%+v", ds[3].Hits)
	}
	if tools[1].Description != "s" || tools[1].Title != "" {
		t.Error("upstream tool objects must not be mutated")
	}
}

func TestInvertIsAllowList(t *testing.T) {
	p := &Policy{Name: "allow", Invert: true, Rules: []Rule{{When: `tool.annotations.readOnlyHint`, Then: map[string]string{"visible": "true"}}}}
	c, err := Compile(p)
	if err != nil {
		t.Fatal(err)
	}
	ds := Apply(context.Background(), []*Compiled{c}, []*mcp.Tool{{Name: "a"}, {Name: "b", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}}, Server{}, "direct")
	if ds[0].Visible || !ds[1].Visible {
		t.Errorf("got %v %v", ds[0].Visible, ds[1].Visible)
	}
}

func TestCompileProblems(t *testing.T) {
	_, err := Compile(&Policy{Rules: []Rule{
		{When: `tool.name +`, Then: map[string]string{"visible": `"x"`, "bogus": "true"}},
		{When: ``, Then: map[string]string{}},
	}})
	var ps Problems
	if !errors.As(err, &ps) {
		t.Fatalf("expected Problems, got %v", err)
	}
	fields := map[string]bool{}
	for _, p := range ps {
		fields[p.Field] = true
		if p.Rule != 0 && p.Rule != 1 {
			t.Errorf("bad rule index %d", p.Rule)
		}
	}
	for _, want := range []string{"", "visible", "bogus"} {
		if !fields[want] {
			t.Errorf("missing problem for field %q in %v", want, ps)
		}
	}
	disabled := false
	if _, err := Compile(&Policy{Rules: []Rule{{Enabled: &disabled, When: `broken +`, Then: map[string]string{"visible": "false"}}}}); err != nil {
		t.Errorf("disabled rules must not be compiled: %v", err)
	}
}

func TestNameCarriesSlug(t *testing.T) {
	c, err := Compile(&Policy{Rules: []Rule{
		{When: `tool.name.startsWith("coin")`, Then: map[string]string{"annotations.destructiveHint": "true"}},
		{When: `tool.baseName == "flip"`, Then: map[string]string{"name": `"toss"`}},
		{When: `tool.name == "coinserver__toss"`, Then: map[string]string{"visible": "false"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ds := Apply(context.Background(), []*Compiled{c}, []*mcp.Tool{{Name: "flip"}, {Name: "pick"}}, Server{Slug: "coinserver"}, "direct")
	if ds[0].Tool.Annotations == nil || ds[0].Tool.Annotations.DestructiveHint == nil || !*ds[0].Tool.Annotations.DestructiveHint {
		t.Error("tool.name should carry the slug prefix on every route")
	}
	if ds[0].Tool.Name != "toss" || ds[0].Upstream != "flip" {
		t.Errorf("name assignment sets the bare name: got %q upstream %q", ds[0].Tool.Name, ds[0].Upstream)
	}
	if ds[0].Visible {
		t.Error("a later rule should see the renamed tool as coinserver__toss")
	}
	if !ds[1].Visible || ds[1].Tool.Name != "pick" {
		t.Error("pick should be untouched")
	}
}

func TestRegexReplace(t *testing.T) {
	c, err := Compile(&Policy{Rules: []Rule{
		{When: `true`, Then: map[string]string{"description": `tool.description.regexReplace("\\(deprecated:[^)]*\\)\\s*", "").regexReplace("^(\\w+)", "${1}!")`}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ds := Apply(context.Background(), []*Compiled{c}, []*mcp.Tool{{Name: "a", Description: "Fetch (deprecated: use x) a page."}}, Server{Slug: "s"}, "direct")
	if got := ds[0].Tool.Description; got != "Fetch! a page." {
		t.Errorf("got %q", got)
	}
	bad, err := Compile(&Policy{Rules: []Rule{{When: `true`, Then: map[string]string{"description": `tool.description.regexReplace("(", "")`}}}})
	if err != nil {
		t.Fatal(err)
	}
	ds = Apply(context.Background(), []*Compiled{bad}, []*mcp.Tool{{Name: "a", Description: "x"}}, Server{}, "direct")
	if ds[0].Tool.Description != "x" || len(ds[0].Hits) == 0 || ds[0].Hits[0].Error == "" {
		t.Errorf("a bad pattern should leave the tool alone and record an error: %+v", ds[0])
	}
}

package hooks

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// An absent container is absent, not null: a tool with no inputSchema and a
// skill with no frontmatter have no such key, so has() is false for both.
func TestAnAbsentContainerIsAbsentNotNull(t *testing.T) {
	tool := subject(KindTool, newToolState(prepare(&mcp.Tool{Name: "bare"})).resolve)
	if v, ok := tool["inputSchema"]; ok {
		t.Errorf("tool.inputSchema is present as %v; tool.upstream has it: %v", v, tool["upstream"].(map[string]any)["inputSchema"] != nil)
	}
	sk := Skill{URI: "skill://dyn/SKILL.md", Resources: json.RawMessage(`"dynamic"`)}
	it := &item{kind: KindSkill, id: sk.URI, view: NewView(skillItem(sk))}
	if v, ok := subject(KindSkill, it.resolve)["frontmatter"]; ok {
		t.Errorf("skill.frontmatter is present as %s", asJSON(v))
	}
}

package hooks

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Skills: a manifest vouches for the bytes of the files it lists, so a rule
// may not change them, and a denied skill cannot be read.

// A file a skill's manifest vouches for keeps its bytes whatever a rule that
// depends on the request sets: new text, a blob in place of the text, or the
// file's URI written another way. A rule can still drop the entry.
func TestAManifestFileKeepsItsBytes(t *testing.T) {
	text := "---\nname: demo\ndescription: d\n---\nbody"
	cat := Catalog{Skills: []Skill{{URI: "skill://demo/SKILL.md", Frontmatter: map[string]any{"name": "demo", "description": "d"},
		Resources: json.RawMessage(`[{"uri":"skill://demo/SKILL.md","digest":"sha256:x","size":10}]`), Files: map[string]File{"skill://demo/SKILL.md": {Text: &text}}}}}
	reply := asJSON(map[string]any{"contents": []any{map[string]any{"uri": "skill://demo/SKILL.md", "text": text}}})
	for name, expr := range map[string]string{
		"new text":         "response.result.contents.map(c, {'uri': c.uri, 'text': 'EVIL'})",
		"a blob":           "response.result.contents.map(c, {'uri': c.uri, 'blob': 'RVZJTA=='})",
		"the URI in caps":  "response.result.contents.map(c, {'uri': 'SKILL://demo/SKILL.md', 'text': 'EVIL'})",
		"a second entry":   "response.result.contents + [{'uri': 'skill://demo/SKILL.md', 'text': 'EVIL'}]",
		"a dotted variant": "[{'uri': 'skill://demo/x/../SKILL.md', 'text': 'EVIL'}]",
	} {
		s := newServer(t, cat, Rule{Hook: "resources/read.post.result", When: "request.caller.role == 'member'", Then: map[string]string{"response.result.contents": expr}})
		o := readReply(t, s, "skill://demo/SKILL.md", reply)
		if got := outcomeText(o); strings.Contains(got, "EVIL") || strings.Contains(got, "RVZJTA") || (o.Error == nil && !strings.Contains(got, `body`)) {
			t.Errorf("%s changed the file: %s", name, got)
		}
	}
	s := newServer(t, cat, Rule{Hook: "resources/read.post.result", When: "request.caller.role == 'member'", Then: map[string]string{"response.result.contents": "[]"}})
	if got := outcomeText(readReply(t, s, "skill://demo/SKILL.md", reply)); !strings.Contains(got, `"contents":[]`) {
		t.Errorf("a rule could not drop the entry: %s", got)
	}
}

// A file under a nested skill's folder that the outer skill's manifest lists
// keeps its bytes, although the nested skill's own manifest does not list it.
// The outer skill is listed with the server's digest for that file, so a
// rule's change to it must not stand.
func TestAFileTheOuterManifestListsKeepsItsBytes(t *testing.T) {
	text, inner, extra := "---\nname: demo\ndescription: d\n---\nbody", "---\nname: sub\ndescription: d\n---\ninner", "extra"
	cat := Catalog{Skills: []Skill{
		{URI: "skill://demo/SKILL.md", Frontmatter: map[string]any{"name": "demo", "description": "d"},
			Resources: json.RawMessage(`[{"uri":"skill://demo/SKILL.md","digest":"sha256:x","size":10},{"uri":"skill://demo/sub/x.md","digest":"sha256:y","size":5}]`),
			Files:     map[string]File{"skill://demo/SKILL.md": {Text: &text}, "skill://demo/sub/x.md": {Text: &extra}}},
		{URI: "skill://demo/sub/SKILL.md", Frontmatter: map[string]any{"name": "sub", "description": "d"},
			Resources: json.RawMessage(`[{"uri":"skill://demo/sub/SKILL.md","digest":"sha256:z","size":10}]`),
			Files:     map[string]File{"skill://demo/sub/SKILL.md": {Text: &inner}}},
	}}
	s := newServer(t, cat, Rule{Hook: "resources/read.post.result", When: "request.caller.role == 'member'", Then: map[string]string{"response.result.contents": "response.result.contents.map(c, {'uri': c.uri, 'text': 'EVIL'})"}})
	listing := asJSON((&Engine{}).ListSkills(context.Background(), newRequest("direct", s)).Result)
	if !strings.Contains(listing, `{"uri":"skill://demo/sub/x.md","digest":"sha256:y","size":5}`) {
		t.Fatalf("control: the outer manifest does not vouch for the file: %s", listing)
	}
	if got := outcomeText(readReply(t, s, "skill://demo/sub/x.md", `{"contents":[{"uri":"skill://demo/sub/x.md","text":"extra"}]}`)); strings.Contains(got, "EVIL") {
		t.Errorf("a file the outer manifest lists was changed: %s", got)
	}
}

// A file that two skills' manifests both list is never rewritten by a rule,
// so neither manifest can promise bytes a read does not return. The nested
// skill is listed with the digest of the server's bytes, a read returns those
// bytes, and an issue says the rule's change was not applied.
func TestAFileTwoSkillsListIsNeverRewritten(t *testing.T) {
	outerURI, innerURI, sharedURI := "skill://demo/SKILL.md", "skill://demo/sub/SKILL.md", "skill://demo/sub/x.md"
	outerText, innerText, sharedText := "---\nname: demo\ndescription: outer\n---\n", "---\nname: sub\ndescription: inner\n---\n", "hunter2"
	makeSkill := func(uri string, files map[string]string, name string) Skill {
		var manifest []SkillResource
		fs := map[string]File{}
		for _, u := range sortedKeys(files) {
			text := files[u]
			manifest = append(manifest, SkillResource{URI: u, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(text))), Size: int64(len(text))})
			fs[u] = File{Text: &text}
		}
		return Skill{URI: uri, Frontmatter: map[string]any{"name": name, "description": "test"}, Resources: json.RawMessage(asJSON(manifest)), Files: fs}
	}
	outer := makeSkill(outerURI, map[string]string{outerURI: outerText, sharedURI: sharedText}, "demo")
	inner := makeSkill(innerURI, map[string]string{innerURI: innerText, sharedURI: sharedText}, "sub")
	s := newServer(t, Catalog{Skills: []Skill{outer, inner}},
		Rule{Hook: "resources/read.post.result", When: "true", Then: map[string]string{"response.result.contents": "response.result.contents.map(c, {'uri': c.uri, 'text': c.text.replace('name: demo', 'name: renamed').replace('hunter2', 'REDACTED')})"}})
	e, r := &Engine{}, newRequest("direct", s)
	listed := e.GetSkill(context.Background(), r, innerURI)
	if listed.Error != nil {
		t.Fatalf("skills/get failed: %s", outcomeText(listed))
	}
	// A file two skills answer for is never rewritten, so no manifest can
	// promise bytes a read does not return: both keep the server's.
	wantDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(sharedText)))
	if !strings.Contains(asJSON(listed.Result), wantDigest) {
		t.Fatalf("inner manifest does not describe the server's bytes: %s", outcomeText(listed))
	}
	o := readReply(t, s, sharedURI, `{"contents":[{"uri":"skill://demo/sub/x.md","text":"hunter2"}]}`)
	if !strings.Contains(asJSON(o.Result), "hunter2") {
		t.Errorf("read disagrees with the manifest: %s", outcomeText(o))
	}
	if !strings.Contains(strings.Join(o.Issues, "\n"), "was not applied") {
		t.Errorf("a rule's change was set aside and no issue says so: %v", o.Issues)
	}
}

// A file a nested skill's manifest lists keeps its bytes when the outer
// skill's resources are dynamic.
func TestAFileUnderADynamicOuterSkillIsNotRewritten(t *testing.T) {
	text := "hunter2"
	inner := Skill{URI: "skill://demo/sub/SKILL.md", Frontmatter: map[string]any{"name": "sub"},
		Resources: json.RawMessage(`[{"uri":"skill://demo/sub/x.md","digest":"original","size":7}]`), Files: map[string]File{"skill://demo/sub/x.md": {Text: &text}}}
	outer := Skill{URI: "skill://demo/SKILL.md", Resources: json.RawMessage(`"dynamic"`)}
	s := newServer(t, Catalog{Skills: []Skill{outer, inner}}, Rule{Hook: "resources/read.post.result", When: "true", Then: map[string]string{"response.result.contents": "response.result.contents.map(c, {'uri': c.uri, 'text': 'REDACTED'})"}})
	o := readReply(t, s, "skill://demo/sub/x.md", `{"contents":[{"uri":"skill://demo/sub/x.md","text":"hunter2"}]}`)
	if strings.Contains(asJSON(o.Result), "REDACTED") {
		t.Errorf("dynamic outer skill's file was rewritten: %s", outcomeText(o))
	}
}

// Only the files a skill's manifest lists have their bytes put back after the
// rules run. A rule's change to another entry under the skill's folder, which
// the manifest does not vouch for, either stands or is recorded as an issue.
// It is never lost behind a hit that says "set".
func TestAChangeToASkillFileTheManifestDoesNotListIsNotLostSilently(t *testing.T) {
	text := "---\nname: demo\ndescription: d\n---\nbody"
	cat := Catalog{Skills: []Skill{{URI: "skill://demo/SKILL.md", Frontmatter: map[string]any{"name": "demo", "description": "d"},
		Resources: json.RawMessage(`[{"uri":"skill://demo/SKILL.md","digest":"sha256:x","size":10}]`), Files: map[string]File{"skill://demo/SKILL.md": {Text: &text}}}}}
	s := newServer(t, cat, Rule{Hook: "resources/read.post.result", When: "true", Then: map[string]string{
		"response.result.contents": "response.result.contents.map(c, {'uri': c.uri, 'text': c.text.replace('hunter2', '[redacted]')})"}})
	o := readReply(t, s, "skill://demo/notes.md", `{"contents":[{"uri":"skill://demo/notes.md","text":"the password is hunter2"}]}`)
	if got := outcomeText(o); strings.Contains(got, "hunter2") && len(o.Issues) == 0 {
		t.Errorf("the manifest does not list notes.md, the rule redacted it, and the client got %s with no issue", got)
	}
}

// A rule that does not read the request is applied to a skill's files ahead
// of time, from the text the catalog holds. A file the manifest lists and the
// catalog holds no text for cannot be rewritten that way. A redaction that
// applies to every other read is then applied on the read or recorded as an
// issue. It is not lost behind a hit that says "set".
func TestAManifestFileWithNoHeldTextIsRedactedOrReported(t *testing.T) {
	text := "---\nname: demo\ndescription: d\n---\nbody"
	cat := Catalog{Skills: []Skill{{URI: "skill://demo/SKILL.md", Frontmatter: map[string]any{"name": "demo", "description": "d"},
		Resources: json.RawMessage(`[{"uri":"skill://demo/SKILL.md","digest":"sha256:x","size":10},{"uri":"skill://demo/extra.md","digest":"sha256:y","size":13}]`),
		Files:     map[string]File{"skill://demo/SKILL.md": {Text: &text}}}}}
	s := newServer(t, cat, Rule{Hook: "resources/read.post.result", When: "true", Then: map[string]string{
		"response.result.contents": "response.result.contents.map(c, has(c.text) ? {'uri': c.uri, 'text': c.text.replace('hunter2', '[redacted]')} : c)"}})
	o := readReply(t, s, "skill://demo/extra.md", `{"contents":[{"uri":"skill://demo/extra.md","text":"extra hunter2"}]}`)
	if got := outcomeText(o); strings.Contains(got, "hunter2") && len(o.Issues) == 0 {
		t.Errorf("the rule redacted the file, the client got %s, and no issue was recorded", got)
	}
}

// A read of a skill file returns two entries under one URI and a rule drops
// one of them. The dropped entry does not come back in place of the one that
// was kept.
func TestAnEntryARuleDroppedFromASkillReadStaysDropped(t *testing.T) {
	s := newServer(t, Catalog{Skills: []Skill{{URI: "skill://demo/SKILL.md", Resources: json.RawMessage(`"dynamic"`)}}},
		Rule{Hook: "resources/read.post.result", When: "true", Then: map[string]string{"response.result.contents": "response.result.contents.filter(c, !c.text.contains('SECRET'))"}})
	o := readReply(t, s, "skill://demo/x.md", `{"contents":[{"uri":"skill://demo/x.md","text":"public"},{"uri":"skill://demo/x.md","text":"SECRET"}]}`)
	if strings.Contains(asJSON(o.Result), "SECRET") {
		t.Errorf("the dropped entry replaced the retained entry: %s", outcomeText(o))
	}
}

// response.upstream is the server's response. For a skill file that the
// gateway rewrote ahead of time it still holds the server's text, not the
// rewritten text.
func TestResponseUpstreamOfASkillFileIsWhatTheServerSent(t *testing.T) {
	text := "---\nname: demo\ndescription: d\n---\nserver body"
	cat := Catalog{Skills: []Skill{{URI: "skill://demo/SKILL.md", Frontmatter: map[string]any{"name": "demo", "description": "d"},
		Resources: json.RawMessage(`[{"uri":"skill://demo/SKILL.md","digest":"sha256:x","size":10}]`), Files: map[string]File{"skill://demo/SKILL.md": {Text: &text}}}}}
	s := newServer(t, cat,
		Rule{Hook: "resources/read.post.result", When: "true", Then: map[string]string{"response.result.contents": "response.result.contents.map(c, c.uri.endsWith('/SKILL.md') ? {'uri': c.uri, 'text': c.text.replace('server body', 'gateway body')} : c)"}},
		Rule{Hook: "resources/read.post.result", When: "true", Then: map[string]string{"response.result.contents": "response.result.contents.map(c, c.uri.endsWith('/probe') ? {'uri': c.uri, 'text': response.upstream.result.contents[0].text} : c)"}},
	)
	reply := asJSON(map[string]any{"contents": []any{map[string]any{"uri": "skill://demo/SKILL.md", "text": text}, map[string]any{"uri": "docs://other/probe", "text": "-"}}})
	o := readReply(t, s, "skill://demo/SKILL.md", reply)
	contents, _ := o.Result.(map[string]any)["contents"].([]any)
	if len(contents) != 2 {
		t.Fatalf("got %s", outcomeText(o))
	}
	if seen, _ := contents[1].(map[string]any)["text"].(string); !strings.Contains(seen, "server body") {
		t.Errorf("response.upstream held %q, not what the server sent", seen)
	}
}

// A skill a list rule denies is not reachable through its root with a query
// added, as a read or as a directory read.
func TestADeniedSkillIsNotReadThroughItsRootWithAQuery(t *testing.T) {
	s := newServer(t, Catalog{Skills: []Skill{{URI: "skill://demo/SKILL.md", Resources: json.RawMessage(`"dynamic"`)}}},
		Rule{Hook: "skills/list.post", When: "true", Then: map[string]string{KeyDecision: "'deny'"}})
	e, r := &Engine{}, newRequest("direct", s)
	for _, uri := range []string{"skill://demo?view=raw", "skill://demo/?view=raw"} {
		if o := e.ReadResource(context.Background(), r, &mcp.ReadResourceParams{URI: uri}); o.Forward != nil {
			t.Errorf("denied skill root read forwarded: %s", asJSON(o.Forward))
		}
		if o := e.ReadDirectory(context.Background(), r, &mcp.ReadResourceParams{URI: uri}); o.Forward != nil {
			t.Errorf("denied skill directory forwarded: %s", asJSON(o.Forward))
		}
	}
}

// Frontmatter with no description, or with a closing fence that is not a line
// of its own, is refused.
func TestFrontmatterNeedsADescriptionAndAClosingFence(t *testing.T) {
	for name, text := range map[string]string{
		"missing-description": "---\nname: widget\n---\nBody\n",
		"bad-closing-fence":   "---\nname: widget\ndescription: x\n---not-a-fence\nBody\n",
	} {
		t.Run(name, func(t *testing.T) {
			if fm, err := ParseFrontmatter(text); err == nil {
				t.Fatalf("invalid frontmatter accepted: %#v", fm)
			}
		})
	}
}

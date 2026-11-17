package hooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Skills: the listing, the files a skill vouches for, and the rewrite plan
// that keeps a skill's manifest and its files in step when a result rule
// changes text.

// Skill is one entry of skills/list (Skills extension), plus the file
// contents the engine needs for opt-in rewriting.
type Skill struct {
	URI         string          `json:"uri"`
	Frontmatter map[string]any  `json:"frontmatter"`
	Resources   json.RawMessage `json:"resources"` // a manifest array, or "dynamic"
	Files       map[string]File `json:"files,omitempty"`
}

// File is a skill file's content. Text is a pointer so an empty text file
// is distinguishable from a binary or absent one.
type File struct {
	Text     *string `json:"text,omitempty"`
	Blob     string  `json:"blob,omitempty"`
	MIMEType string  `json:"mimeType,omitempty"`
}

// SkillResource is one manifest entry.
type SkillResource struct {
	URI    string `json:"uri"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// Root is the skill's folder: its uri without /SKILL.md.
func (sk Skill) Root() string { return strings.TrimSuffix(sk.URI, "/SKILL.md") }

// manifest is a skill's list of files. ok is false for a dynamic skill,
// which has none.
type manifest struct {
	files []SkillResource
	ok    bool
}

func (sk Skill) manifest() ([]SkillResource, bool) {
	var ms []SkillResource
	if json.Unmarshal(sk.Resources, &ms) != nil {
		return nil, false
	}
	return ms, true
}

// manifestOf returns a visible skill's manifest.
func (v *resView) manifestOf(sk Skill) ([]SkillResource, bool) {
	m := v.manifests[sk.URI]
	return m.files, m.ok
}

func inManifest(ms []SkillResource, uri string) bool {
	for _, m := range ms {
		if canonicalURI(m.URI) == canonicalURI(uri) {
			return true
		}
	}
	return false
}

func skillItem(sk Skill) map[string]any {
	m := map[string]any{"uri": sk.URI, "frontmatter": sk.Frontmatter}
	var v any
	_ = json.Unmarshal(sk.Resources, &v)
	m["resources"] = v
	return m
}

// skillView splits a server's skills into visible ones and denied roots. A
// skill nested under a denied skill is denied with it.
func (e *Engine) skillView(ctx context.Context, r Request, s *Server) (visible []Skill, deniedRoots []string, hits []Hit) {
	for _, sk := range s.Catalog.Skills {
		it := &item{kind: KindSkill, id: sk.URI, view: NewView(skillItem(sk)), label: sk.URI}
		o := s.Rules.run(ctx, "skills/list.post", sk.URI, it.resolve, r.context(s).roots("skills/list", nil), func(string, any) error { return nil })
		hits = append(hits, o.hits...)
		if o.denied {
			deniedRoots = append(deniedRoots, sk.Root())
			continue
		}
		visible = append(visible, sk)
	}
	var keep []Skill
	for _, sk := range visible {
		if !deniedUnder(sk.URI, deniedRoots) {
			keep = append(keep, sk)
		}
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].URI < keep[j].URI })
	return keep, deniedRoots, hits
}

// ListSkills answers skills/list.
func (e *Engine) ListSkills(parent context.Context, r Request) (ret Outcome) {
	defer e.recovered(&ret, "skills/list", callRef{method: "skills/list"})
	ctx, cancel := e.ctx(parent)
	defer cancel()
	var o Outcome
	out := []map[string]any{}
	for _, s := range sortedServers(r.Servers) {
		if !e.config(ctx, r, s, "resources", &o) {
			continue
		}
		v := e.resourceView(ctx, r, s)
		o.Hits = append(o.Hits, v.hits...)
		for _, sk := range v.skills {
			entry, issues, hits := e.skillEntry(ctx, r, s, v, sk)
			o.Hits = append(o.Hits, hits...)
			o.Issues = append(o.Issues, prefixed(s.Slug, issues)...)
			out = append(out, entry)
		}
	}
	o.Result = map[string]any{"resultType": "complete", "skills": out, "ttlMs": e.Limits.ttl(), "cacheScope": "private"}
	return e.finishOutcome(ctx, o)
}

// GetSkill answers skills/get.
func (e *Engine) GetSkill(parent context.Context, r Request, uri string) (ret Outcome) {
	defer e.recovered(&ret, "skills/get", callRef{method: "skills/get", target: uri})
	ctx, cancel := e.ctx(parent)
	defer cancel()
	s, up, o, err := e.route(ctx, r, uri, "skills/get")
	if err != nil {
		o.Error = err
		return o
	}
	v := e.resourceView(ctx, r, s)
	o.Hits = append(o.Hits, v.hits...)
	for _, sk := range v.skills {
		if canonicalURI(sk.URI) == canonicalURI(up) {
			entry, issues, hits := e.skillEntry(ctx, r, s, v, sk)
			o.Hits = append(o.Hits, hits...)
			o.Issues = append(o.Issues, prefixed(s.Slug, issues)...)
			o.Result = map[string]any{"resultType": "complete", "skill": entry, "ttlMs": e.Limits.ttl(), "cacheScope": "private"}
			return e.finishOutcome(ctx, o)
		}
	}
	o.Error = &RPCError{Code: CodeInvalidParams, Message: "unknown skill " + uri}
	return o
}

// skillEntry builds the entry the client sees from the skill's plan, then
// applies the mux prefix to every URI.
func (e *Engine) skillEntry(ctx context.Context, r Request, s *Server, v *resView, sk Skill) (map[string]any, []string, []Hit) {
	var issues []string
	entry := skillItem(sk)
	plan := e.planSkill(ctx, r, s, v, sk)
	if plan.reason != "" {
		issues = append(issues, sk.URI+": "+plan.reason)
	}
	ms := plan.manifest
	if ms != nil {
		var kept []SkillResource
		for _, m := range ms {
			if !underRoot(m.URI, sk.Root()) {
				issues = append(issues, sk.URI+": manifest entry outside the skill folder dropped: "+m.URI)
				continue
			}
			if nonCanonical(m.URI) {
				issues = append(issues, sk.URI+": manifest entry whose URI is not in canonical form dropped: "+m.URI)
				continue
			}
			kept = append(kept, m)
		}
		ms = kept
		entry["resources"] = ms
	}
	entry["frontmatter"] = plan.fm
	if r.mux() {
		entry["uri"] = PrefixURI(s.Slug, sk.URI)
		if ms != nil {
			pm := make([]SkillResource, len(ms))
			for i, m := range ms {
				pm[i] = m
				pm[i].URI = PrefixURI(s.Slug, m.URI)
			}
			entry["resources"] = pm
		}
	}
	return entry, issues, plan.hits
}

// skillPlan is the single rewrite decision for one skill, used for the
// entry and for every file read, so they cannot disagree.
type skillPlan struct {
	status   string // "none" (no rules), "skipped", "rejected", "applied"
	reason   string
	files    map[string]string // uri -> rewritten text, for files whose text changed
	manifest []SkillResource   // with recomputed digests and sizes
	fm       map[string]any    // the skill's frontmatter after the rewrite
	hits     []Hit
}

// planSkill decides the rewrite for a skill. Mutations are skipped for
// dynamic skills and for rules that read the caller, the client or the
// clock. The plan is all or nothing: a failure or a name change keeps every
// upstream byte.
func (e *Engine) planSkill(ctx context.Context, r Request, s *Server, v *resView, sk Skill) *skillPlan {
	if p, ok := v.plans[sk.URI]; ok {
		return p
	}
	p := e.makeSkillPlan(ctx, r, s, v, sk)
	v.plans[sk.URI] = p
	return p
}

func (e *Engine) makeSkillPlan(ctx context.Context, r Request, s *Server, v *resView, sk Skill) *skillPlan {
	p := &skillPlan{status: "none", fm: sk.Frontmatter}
	ms, isManifest := v.manifestOf(sk)
	p.manifest = ms
	if !s.Rules.Has("resources/read.post.result") {
		return p
	}
	if !isManifest {
		p.status, p.reason = "skipped", "dynamic skills are served unchanged"
		return p
	}
	for _, c := range s.Rules.byHook["resources/read.post.result"] {
		if !c.audit && c.vars["request"] {
			p.status, p.reason = "skipped", "resources/read.post.result rules depend on the request, so skill files are served unchanged"
			return p
		}
	}
	files := map[string]string{}
	out := make([]SkillResource, len(ms))
	copy(out, ms)
	fm := sk.Frontmatter
	for i, m := range ms {
		f, ok := sk.Files[m.URI]
		if !ok || f.Text == nil {
			continue
		}
		if _, readable, _ := e.checkURI(ctx, r, s, v, m.URI); !readable {
			continue // never fetch or rewrite a file the caller cannot read
		}
		if v.shared(sk, m.URI) {
			// Another skill answers for this file too. Two plans could
			// promise different bytes for it, so it keeps the server's.
			continue
		}
		text, hits, denied, err := e.applyResultRules(ctx, r, s, v, m.URI, *f.Text, f.MIMEType)
		p.hits = append(p.hits, hits...)
		if err != nil || denied {
			p.status, p.reason = "rejected", "rewrite of "+m.URI+" failed"
			p.files, p.manifest, p.fm = nil, ms, sk.Frontmatter
			return p
		}
		if text == *f.Text {
			continue
		}
		if m.URI == sk.URI {
			nfm, err := ParseFrontmatter(text)
			if err != nil {
				p.status, p.reason = "rejected", "rewritten SKILL.md has invalid frontmatter: "+err.Error()
				p.files, p.manifest, p.fm = nil, ms, sk.Frontmatter
				return p
			}
			if nfm["name"] != sk.Frontmatter["name"] {
				p.status, p.reason = "rejected", fmt.Sprintf("rewrite would change the skill name from %v to %v; rejected", sk.Frontmatter["name"], nfm["name"])
				p.files, p.manifest, p.fm = nil, ms, sk.Frontmatter
				return p
			}
			fm = nfm
		}
		files[m.URI] = text
		sum := sha256.Sum256([]byte(text))
		out[i].Digest = "sha256:" + hex.EncodeToString(sum[:])
		out[i].Size = int64(len(text))
	}
	p.status, p.files, p.manifest, p.fm = "applied", files, out, fm
	return p
}

// applyResultRules runs resources/read.post.result over the text of one
// file, as a read that returned that file alone. It is how a skill's files
// are rewritten ahead of time. A rule that drops the file or turns it into
// something that is not text fails the rewrite.
func (e *Engine) applyResultRules(ctx context.Context, r Request, s *Server, v *resView, uri, text, mime string) (string, []Hit, bool, error) {
	content := map[string]any{"uri": uri, "text": text}
	if mime != "" {
		content["mimeType"] = mime
	}
	roots := r.context(s).roots("resources/read", map[string]any{"uri": uri})
	requestOf(roots)["upstream"] = map[string]any{"params": map[string]any{"uri": uri}}
	out, hits, _, denied, err := e.readRules(ctx, r, s, v, uri, roots, nil, []any{content}, []any{content})
	if denied || err != nil {
		return text, hits, denied, err
	}
	if len(out) != 1 {
		return text, hits, false, fmt.Errorf("a rule removed %s", uri)
	}
	t, ok := out[0].(map[string]any)["text"].(string)
	if !ok {
		return text, hits, false, fmt.Errorf("a rule left %s with no text", uri)
	}
	return t, hits, false, nil
}

// shared reports a file that a skill other than sk also answers for: the
// other skill's manifest lists it, or the other skill is dynamic and the
// file is under its folder.
func (v *resView) shared(sk Skill, uri string) bool {
	for _, other := range v.skills {
		if other.URI == sk.URI || !underRoot(uri, other.Root()) {
			continue
		}
		if ms, isManifest := v.manifestOf(other); !isManifest || inManifest(ms, uri) {
			return true
		}
	}
	return false
}

// vouchedBy returns a visible skill that vouches for the bytes of a file:
// the file is the skill's SKILL.md, the skill's manifest lists it, or the
// skill is dynamic, with no manifest, and the file is under its folder.
// Skills nest, so every skill the file is under is asked, not only the
// nearest: an outer skill's manifest may list a file in an inner folder.
func (v *resView) vouchedBy(uri string) (Skill, bool) {
	n := canonicalURI(uri)
	var dynamic *Skill
	for i, sk := range v.skills {
		if !underRoot(uri, sk.Root()) {
			continue
		}
		ms, isManifest := v.manifestOf(sk)
		if n == canonicalURI(sk.URI) || (isManifest && inManifest(ms, uri)) {
			return sk, true
		}
		if !isManifest && dynamic == nil {
			dynamic = &v.skills[i]
		}
	}
	if dynamic != nil {
		return *dynamic, true
	}
	return Skill{}, false
}

// ParseFrontmatter parses and validates the YAML frontmatter of a SKILL.md:
// an opening "---" line, a closing line that is exactly "---", and string
// name and description. A UTF-8 byte order mark and CRLF line endings are
// accepted.
func ParseFrontmatter(text string) (map[string]any, error) {
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return nil, fmt.Errorf("missing frontmatter")
	}
	body := text[4:]
	end := -1
	for i := 0; i < len(body); {
		j := strings.IndexByte(body[i:], '\n')
		line := body[i:]
		if j >= 0 {
			line = body[i : i+j]
		}
		if line == "---" {
			end = i
			break
		}
		if j < 0 {
			break
		}
		i += j + 1
	}
	if end < 0 {
		return nil, fmt.Errorf("unterminated frontmatter")
	}
	var m map[string]any
	if err := yaml.Unmarshal([]byte(body[:end]), &m); err != nil {
		return nil, err
	}
	for _, k := range []string{"name", "description"} {
		if s, ok := m[k].(string); !ok || s == "" {
			return nil, fmt.Errorf("frontmatter needs a string %s", k)
		}
	}
	return m, nil
}

package hooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.yaml.in/yaml/v3"
)

// ---- prompts ------------------------------------------------------------------------

func promptItems(ps []*mcp.Prompt) []map[string]any {
	out := make([]map[string]any, len(ps))
	for i, p := range ps {
		out[i] = toMap(p)
	}
	return out
}

// ListPrompts lists every server's exposed prompts, slug a-z then name a-z.
func (e *Engine) ListPrompts(parent context.Context, r Request) (ret Outcome) {
	defer e.recovered(&ret, "prompts/list", callRef{method: "prompts/list"})
	ctx, cancel := e.ctx(parent)
	defer cancel()
	var o Outcome
	res := &mcp.ListPromptsResult{Prompts: []*mcp.Prompt{}}
	res.TTLMs, res.CacheScope = e.Limits.ttl(), "private"
	for _, s := range sortedServers(r.Servers) {
		if !e.config(ctx, r, s, "prompts", &o) {
			continue
		}
		ls, issues, hits := s.Rules.listItems(ctx, KindPrompt, "prompts/filter", "prompts/modify", "name", promptItems(s.Catalog.Prompts), r.context(s), e.Limits.Workers)
		o.Hits = append(o.Hits, hits...)
		o.Issues = append(o.Issues, prefixed(s.Slug, issues)...)
		for _, l := range ls {
			var p mcp.Prompt
			b, _ := json.Marshal(l.value)
			_ = json.Unmarshal(b, &p)
			if r.mux() {
				p.Name = s.Slug + Separator + p.Name
			}
			res.Prompts = append(res.Prompts, &p)
		}
	}
	o.Result = res
	return e.finishOutcome(ctx, o)
}

func prefixed(slug string, xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = slug + ": " + x
	}
	return out
}

func (e *Engine) resolvePrompt(ctx context.Context, r Request, name string) (*Server, *listed, Outcome, error) {
	var o Outcome
	slug := ""
	if r.mux() {
		var ok bool
		slug, name, ok = strings.Cut(name, Separator)
		if !ok || slug == "" || name == "" {
			return nil, nil, o, &RPCError{Code: CodeInvalidParams, Message: "prompt name must be <slug>" + Separator + "<prompt>"}
		}
	} else if len(r.Servers) == 1 {
		slug = r.Servers[0].Slug
	}
	s := r.server(slug)
	if s == nil || !e.config(ctx, r, s, "prompts", &o) {
		return nil, nil, o, &RPCError{Code: CodeInvalidParams, Message: "unknown prompt " + name}
	}
	ls, _, hits := s.Rules.listItems(ctx, KindPrompt, "prompts/filter", "prompts/modify", "name", promptItems(s.Catalog.Prompts), r.context(s), e.Limits.Workers)
	o.Hits = append(o.Hits, hits...)
	for i := range ls {
		if ls[i].exposedID == name {
			return s, &ls[i], o, nil
		}
	}
	return s, nil, o, &RPCError{Code: CodeInvalidParams, Message: "unknown prompt " + name}
}

// GetPrompt runs the request side of prompts/get.
func (e *Engine) GetPrompt(parent context.Context, r Request, p *mcp.GetPromptParams) (ret Outcome) {
	defer e.recovered(&ret, "prompts/get", promptCallRef(p))
	ctx, cancel := e.ctx(parent)
	defer cancel()
	s, l, o, err := e.resolvePrompt(ctx, r, p.Name)
	if err != nil {
		o.Error = err.(*RPCError)
		return o
	}
	args := map[string]any{}
	for k, v := range p.Arguments {
		args[k] = v
	}
	if _, err := l.args.reverse(p.Arguments); err != nil {
		o.Error = &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		return o
	}
	hash := argsHash(args)
	st, answers, upResp, serr := e.continuation(r, s, "prompts/get", l.upstreamID, hash, p.RequestState, p.InputResponses)
	if serr == errDeclined {
		o.Hits = append(o.Hits, declinedHit("prompts/get", l.exposedID))
	}
	if serr != nil {
		o.Error = serr
		return o
	}
	it := &item{kind: KindPrompt, view: NewView(l.value), args: l.args}
	up := upstreamPrompt(s, l.upstreamID)
	resolve := func(name string) (any, bool) {
		if name == "upstream" {
			return derived(KindPrompt, up), true
		}
		return it.resolve(name)
	}
	vars := r.context(s).vars()
	vars["args"] = args
	vars["input"] = answers
	co := s.Rules.run(ctx, "prompts/get", l.exposedID, resolve, vars, func(key string, val any) error {
		parts := splitPath(key)
		args[parts[1]] = val
		vars["args"] = args
		return nil
	})
	o.Hits = append(o.Hits, co.hits...)
	switch {
	case co.denied:
		o.Error = &RPCError{Code: CodeInvalidParams, Message: denyOr(co.reason, "prompts/get was refused by policy")}
		return o
	case co.elicit != nil:
		res, err := e.elicitation(r, s, "prompts/get", l.upstreamID, hash, st, answers, co.elicit)
		if err != nil {
			o.Error = &RPCError{Code: CodeInvalidParams, Message: err.Error()}
			return o
		}
		o.Result = res
		return o
	}
	strArgs := map[string]string{}
	for k, v := range args {
		if sv, ok := v.(string); ok {
			strArgs[k] = sv
		} else {
			strArgs[k] = fmt.Sprint(v)
		}
	}
	upArgs, err := l.args.reverse(strArgs)
	if err != nil {
		o.Error = &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		return o
	}
	fp := &mcp.GetPromptParams{Name: l.upstreamID, Arguments: upArgs}
	if st != nil && st.UpstreamRound {
		fp.RequestState, fp.InputResponses = st.Upstream, upResp
	}
	if expired(ctx) {
		return budgetOutcome(o)
	}
	ci, chits := e.Connect(ctx, r, s, e.version())
	o.Hits = append(o.Hits, chits...)
	if ci.ForwardMeta {
		fp.Meta = forwardMeta(p.Meta)
	}
	o.Forward = &Forward{Slug: s.Slug, Method: "prompts/get", Params: fp, Connect: &ci}
	o.Call = &CallContext{Server: s, Method: "prompts/get", Target: l.upstreamID, Exposed: l.exposedID, Item: l.value, Args: toAnyMap(upArgs), Hash: hash, State: st, Answers: answers}
	return o
}

func upstreamPrompt(s *Server, name string) map[string]any {
	for _, p := range s.Catalog.Prompts {
		if p.Name == name {
			return toMap(p)
		}
	}
	return map[string]any{"name": name}
}

// UpstreamPromptReply handles the upstream's answer to a forwarded
// prompts/get, using the CallContext GetPrompt returned with the forward.
func (e *Engine) UpstreamPromptReply(parent context.Context, r Request, call *CallContext, p *mcp.GetPromptParams, raw json.RawMessage) (ret Outcome) {
	defer e.recovered(&ret, "prompts/get reply", promptCallRef(p))
	ctx, cancel := e.ctx(parent)
	defer cancel()
	if call == nil {
		return errOutcome(-32603, "no forwarded request to reply to")
	}
	if len(raw) > e.Limits.maxBytes() {
		return errOutcome(CodeTooLarge, "upstream result exceeds gateway size limit")
	}
	s := call.Server
	var o Outcome
	m, ok := decodeExact(raw).(map[string]any)
	if !ok {
		return errOutcome(-32603, "upstream result is not a JSON object")
	}
	var seen unknowns
	defer func() { ret.Unknown = append(ret.Unknown, seen.list()...) }()
	if rt, _ := m["resultType"].(string); rt != "" && rt != "complete" && rt != "input_required" {
		seen.add(UnknownResultType, rt, s.Slug, call.Target, "passed", m)
	}
	inputUnknowns(&seen, s.Slug, call.Target, m)
	if msgs, ok := m["messages"].([]any); ok {
		for _, x := range msgs {
			if mm, ok := x.(map[string]any); ok {
				// A prompt message holds one content block, or a list of them.
				if list, isList := mm["content"].([]any); isList {
					contentUnknowns(&seen, s.Slug, call.Target, list)
				} else if mm["content"] != nil {
					contentUnknowns(&seen, s.Slug, call.Target, []any{mm["content"]})
				}
			}
		}
	}
	if isInputRequired(m) {
		if msg, ok := inputAllowed(r, s); !ok {
			o.Issues = append(o.Issues, s.Slug+": "+call.Target+" asked for input and was refused: "+msg)
			o.Error = &RPCError{Code: CodeInvalidParams, Message: msg}
			return e.finishOutcome(ctx, o)
		}
		it := &item{kind: KindPrompt, view: NewView(call.Item), args: newArgState(call.Item)}
		out, hits, err := e.inputHook(ctx, r, s, "prompts/input", call.Exposed, it.resolve, call.Args, rawRequests(m))
		o.Hits = append(o.Hits, hits...)
		if err != nil {
			o.Error = &RPCError{Code: CodeInvalidParams, Message: err.Error()}
			return e.finishOutcome(ctx, o)
		}
		upState, _ := m["requestState"].(string)
		st, err := e.wrapState(r, s, call, p.RequestState, upState, out)
		if err != nil {
			o.Error = &RPCError{Code: CodeInvalidParams, Message: err.Error()}
			return e.finishOutcome(ctx, o)
		}
		o.Result = InputRequired{InputRequests: out, RequestState: st}
		return e.finishOutcome(ctx, o)
	}
	if r.mux() {
		m = prefixResultURIs(m, s.Slug)
	}
	o.Result = complete(m)
	return e.finishOutcome(ctx, o)
}

// ---- skills -------------------------------------------------------------------------

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

func (sk Skill) manifest() ([]SkillResource, bool) {
	var ms []SkillResource
	if json.Unmarshal(sk.Resources, &ms) != nil {
		return nil, false
	}
	return ms, true
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
		o := s.Rules.run(ctx, "skills/filter", sk.URI, it.resolve, r.context(s).vars(), func(string, any) error { return nil })
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
		if normalizeURI(sk.URI) == normalizeURI(up) {
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
	p := &skillPlan{status: "none", fm: sk.Frontmatter}
	ms, isManifest := sk.manifest()
	p.manifest = ms
	if !s.Rules.Has("resources/result") {
		return p
	}
	if !isManifest {
		p.status, p.reason = "skipped", "dynamic skills are served unchanged"
		return p
	}
	for _, c := range s.Rules.byHook["resources/result"] {
		if !c.audit && (c.vars["caller"] || c.vars["client"] || c.vars["now"]) {
			p.status, p.reason = "skipped", "resources/result rules depend on the request, so skill files are served unchanged"
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
		text, hits, denied, err := e.applyResultRules(ctx, r, s, v, m.URI, *f.Text, f.MIMEType, true)
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
			if !underRoot(m.URI, sk.Root()) || nonCanonical(m.URI) {
				issues = append(issues, sk.URI+": manifest entry outside the skill folder dropped: "+m.URI)
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

// skillFor returns the visible skill whose folder contains uri, preferring
// the deepest (a nested skill's own files belong to it for planning).
func (v *resView) skillFor(uri string) (Skill, bool) {
	var best Skill
	found := false
	for _, sk := range v.skills {
		if underRoot(uri, sk.Root()) && (!found || len(sk.Root()) > len(best.Root())) {
			best, found = sk, true
		}
	}
	return best, found
}

// applyResultRules runs resources/result over one content item. text may
// be empty and binary items pass text=="" with isText false; a deny blocks
// either. mutate=false evaluates decisions but keeps the text.
func (e *Engine) applyResultRules(ctx context.Context, r Request, s *Server, v *resView, uri, text, mime string, isText bool) (string, []Hit, bool, error) {
	t, hits, _, denied, err := e.applyResultRulesReason(ctx, r, s, v, uri, text, mime, isText)
	return t, hits, denied, err
}

// applyResultRulesReason is applyResultRules plus the deny reason the
// administrator wrote, if any.
func (e *Engine) applyResultRulesReason(ctx context.Context, r Request, s *Server, v *resView, uri, text, mime string, isText bool) (string, []Hit, string, bool, error) {
	res := map[string]any{"uri": uri}
	known, listed := v.visibleRes[normalizeURI(uri)]
	if listed {
		res = known
	}
	it := &item{kind: KindResource, id: uri, view: NewView(res), label: uri, bare: !listed}
	content := map[string]any{"uri": uri}
	if mime != "" {
		content["mimeType"] = mime
	}
	if isText {
		content["text"] = text
	}
	cv := NewView(content)
	vars := r.context(s).vars()
	vars["args"] = map[string]any{"uri": uri}
	vars["result"] = resultVar(map[string]any{"contents": []any{content}})
	vars[muxVar] = muxContext(r.Route, s.Slug, v.owned())
	o := s.Rules.run(ctx, "resources/result", uri, it.resolve, vars, func(key string, val any) error {
		if key == "text" && !isText {
			return nil // binary content keeps its bytes
		}
		cv.Set([]string{key}, val)
		vars["result"] = resultVar(map[string]any{"contents": []any{cv.Materialize()}})
		return nil
	})
	if o.denied {
		return text, o.hits, o.reason, true, nil
	}
	if o.err != nil {
		return text, o.hits, "", false, o.err
	}
	if !isText {
		return text, o.hits, "", false, nil
	}
	t, _ := cv.Materialize()["text"].(string)
	return t, o.hits, "", false, nil
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

// ---- resources ----------------------------------------------------------------------

func resourceItems(rs []*mcp.Resource) []map[string]any {
	out := make([]map[string]any, len(rs))
	for i, x := range rs {
		out[i] = toMap(x)
	}
	return out
}

func templateItems(ts []*mcp.ResourceTemplate) []map[string]any {
	out := make([]map[string]any, len(ts))
	for i, x := range ts {
		out[i] = toMap(x)
	}
	return out
}

// ListResources lists every server's exposed resources, slug a-z then URI
// a-z. An entry appears only if the same check a read uses allows it.
func (e *Engine) ListResources(parent context.Context, r Request) (ret Outcome) {
	defer e.recovered(&ret, "resources/list", callRef{method: "resources/list"})
	ctx, cancel := e.ctx(parent)
	defer cancel()
	var o Outcome
	res := &mcp.ListResourcesResult{Resources: []*mcp.Resource{}}
	res.TTLMs, res.CacheScope = e.Limits.ttl(), "private"
	for _, s := range sortedServers(r.Servers) {
		if !e.config(ctx, r, s, "resources", &o) {
			continue
		}
		v := e.resourceView(ctx, r, s)
		o.Hits = append(o.Hits, v.hits...)
		o.Issues = append(o.Issues, prefixed(s.Slug, v.issues)...)
		var uris []string
		for u := range v.visibleRes {
			uris = append(uris, u)
		}
		sort.Strings(uris)
		for _, n := range uris {
			value := v.visibleRes[n]
			upURI, _ := value["uri"].(string)
			if _, ok, _ := e.checkURI(ctx, r, s, v, upURI); !ok {
				continue
			}
			var x mcp.Resource
			b, _ := json.Marshal(value)
			_ = json.Unmarshal(b, &x)
			x.URI = upURI
			if r.mux() {
				x.URI = PrefixURI(s.Slug, x.URI)
			}
			res.Resources = append(res.Resources, &x)
		}
	}
	o.Result = res
	return e.finishOutcome(ctx, o)
}

func deniedUnder(uri string, roots []string) bool {
	for _, root := range roots {
		if underRoot(uri, root) {
			return true
		}
	}
	return false
}

// ListTemplates lists every server's exposed resource templates, slug a-z
// then template a-z.
func (e *Engine) ListTemplates(parent context.Context, r Request) (ret Outcome) {
	defer e.recovered(&ret, "resources/templates/list", callRef{method: "resources/templates/list"})
	ctx, cancel := e.ctx(parent)
	defer cancel()
	var o Outcome
	var seen unknowns
	defer func() { ret.Unknown = append(ret.Unknown, seen.list()...) }()
	res := &mcp.ListResourceTemplatesResult{ResourceTemplates: []*mcp.ResourceTemplate{}}
	res.TTLMs, res.CacheScope = e.Limits.ttl(), "private"
	for _, s := range sortedServers(r.Servers) {
		if !e.config(ctx, r, s, "resources", &o) {
			continue
		}
		ls, issues, hits := s.Rules.listItems(ctx, KindTemplate, "templates/filter", "templates/modify", "uriTemplate", templateItems(s.Catalog.Templates), r.context(s), e.Limits.Workers)
		o.Hits = append(o.Hits, hits...)
		o.Issues = append(o.Issues, prefixed(s.Slug, issues)...)
		for _, l := range ls {
			// A template form the gateway cannot parse is listed as it is.
			// Reads under it work; it only matters for denies and routing.
			if ct := compileTemplate(l.upstreamID); ct.unsupported != "" {
				seen.add(UnknownTemplateForm, ct.unsupported, s.Slug, l.upstreamID, "passed", nil)
			}
		}
		for _, l := range ls {
			var x mcp.ResourceTemplate
			b, _ := json.Marshal(l.value)
			_ = json.Unmarshal(b, &x)
			x.URITemplate = l.upstreamID
			if r.mux() {
				x.URITemplate = PrefixURI(s.Slug, x.URITemplate)
			}
			res.ResourceTemplates = append(res.ResourceTemplates, &x)
		}
	}
	o.Result = res
	return e.finishOutcome(ctx, o)
}

// route finds the server for a URI. On the mux the slug prefix decides; a
// URI without one goes through fallback resolution. On 1:1 the URI is used
// as is. Non-canonical URIs are refused.
func (e *Engine) route(ctx context.Context, r Request, uri, method string) (*Server, string, Outcome, *RPCError) {
	var o Outcome
	if nonCanonical(uri) {
		return nil, "", o, &RPCError{Code: CodeInvalidParams, Message: "URI is not in canonical form (dot segments, empty segments or encoded unreserved characters): " + uri}
	}
	if !r.mux() {
		if len(r.Servers) != 1 {
			return nil, "", o, &RPCError{Code: CodeInvalidParams, Message: "not found: " + uri}
		}
		s := r.Servers[0]
		if !e.config(ctx, r, s, "resources", &o) {
			return nil, "", o, &RPCError{Code: CodeInvalidParams, Message: "not found: " + uri}
		}
		return s, uri, o, nil
	}
	if slug, rest, ok := SplitURI(uri); ok {
		s := r.server(slug)
		// A native scheme can contain "+", so a URI with no slug prefix can
		// look as if it had one: git+ssh://host/repo reads as slug "git".
		// When the named server does not claim the rest, try the whole URI
		// as an unprefixed one before settling on the prefixed reading.
		if s == nil || !e.resourceView(ctx, r, s).claims(rest) {
			var fo Outcome
			if fs, fu, out, ferr := e.fallback(ctx, r, uri, method, &fo); ferr == nil {
				return fs, fu, out, nil
			}
		}
		if s == nil || !e.config(ctx, r, s, "resources", &o) {
			return nil, "", o, &RPCError{Code: CodeInvalidParams, Message: "not found: " + uri}
		}
		return s, rest, o, nil
	}
	return e.fallback(ctx, r, uri, method, &o)
}

// fallback resolves an unprefixed URI to one of the caller's servers, or
// reports it unknown or ambiguous. A server is a candidate only when it
// claims the URI (it lists it, or a template could produce it) and nothing
// denies the read. It never guesses: a URI no server claims is not found on
// the mux, and needs its slug prefix.
func (e *Engine) fallback(ctx context.Context, r Request, uri, method string, o *Outcome) (*Server, string, Outcome, *RPCError) {
	var exact, tmpl []Candidate
	var unavailable []string
	n := normalizeURI(uri)
	for _, s := range sortedServers(r.Servers) {
		if !e.config(ctx, r, s, "resources", o) {
			continue
		}
		if s.Unavailable {
			unavailable = append(unavailable, s.Slug)
			continue
		}
		v := e.resourceView(ctx, r, s)
		o.Hits = append(o.Hits, v.hits...)
		if _, ok, _ := e.checkURI(ctx, r, s, v, uri); !ok || !v.claims(uri) {
			continue
		}
		isExact := v.listedRes[n]
		for _, sk := range v.skills {
			if normalizeURI(sk.URI) == n {
				isExact = true
			}
			if ms, ok := sk.manifest(); ok {
				for _, m := range ms {
					if normalizeURI(m.URI) == n {
						isExact = true
					}
				}
			}
		}
		c := Candidate{Slug: s.Slug, URI: PrefixURI(s.Slug, uri), Exact: isExact}
		if isExact {
			exact = append(exact, c)
		} else {
			tmpl = append(tmpl, c)
		}
	}
	ev := &FallbackEvent{URI: uri, Method: method}
	pick := func(c []Candidate, viaTemplate bool) (*Server, string, Outcome, *RPCError) {
		ev.Candidates, ev.Template = len(c), viaTemplate
		o.Fallback = ev
		if len(unavailable) > 0 {
			ev.Outcome = "unavailable"
			return nil, "", *o, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("cannot resolve %s: %s unavailable; retry with a prefixed URI", uri, strings.Join(unavailable, ", "))}
		}
		if len(c) > 1 {
			ev.Outcome = "ambiguous"
			return nil, "", *o, ambiguousError(uri, c)
		}
		ev.Outcome, ev.Slug = "resolved", c[0].Slug
		return r.server(c[0].Slug), uri, *o, nil
	}
	if len(exact) > 0 {
		return pick(exact, false)
	}
	if len(tmpl) > 0 {
		return pick(tmpl, true)
	}
	if len(unavailable) > 0 {
		return pick(nil, false)
	}
	ev.Outcome = "not_found"
	o.Fallback = ev
	return nil, "", *o, &RPCError{Code: CodeInvalidParams, Message: "not found: " + uri}
}

// ReadResource runs the request side of resources/read.
func (e *Engine) ReadResource(parent context.Context, r Request, p *mcp.ReadResourceParams) Outcome {
	return e.read(parent, r, p, "resources/read")
}

// ReadDirectory runs the request side of resources/directory/read.
func (e *Engine) ReadDirectory(parent context.Context, r Request, p *mcp.ReadResourceParams) Outcome {
	return e.read(parent, r, p, "resources/directory/read")
}

func (e *Engine) read(parent context.Context, r Request, p *mcp.ReadResourceParams, method string) (ret Outcome) {
	defer e.recovered(&ret, method, readCallRef(method, p))
	ctx, cancel := e.ctx(parent)
	defer cancel()
	s, up, o, rerr := e.route(ctx, r, p.URI, method)
	if rerr != nil {
		o.Error = rerr
		return o
	}
	v := e.resourceView(ctx, r, s)
	o.Hits = append(o.Hits, v.hits...)
	res, ok, hits := e.checkURI(ctx, r, s, v, up)
	o.Hits = append(o.Hits, hits...)
	if !ok {
		o.Error = &RPCError{Code: CodeInvalidParams, Message: "not found: " + p.URI}
		return o
	}
	args := map[string]any{"uri": up}
	hash := argsHash(args)
	st, answers, upResp, serr := e.continuation(r, s, method, up, hash, p.RequestState, p.InputResponses)
	if serr == errDeclined {
		o.Hits = append(o.Hits, declinedHit(method, p.URI))
	}
	if serr != nil {
		o.Error = serr
		return o
	}
	it := &item{kind: KindResource, id: up, view: NewView(res), label: up, bare: !v.listedRes[normalizeURI(up)]}
	vars := r.context(s).vars()
	vars["args"] = args
	vars["input"] = answers
	co := s.Rules.run(ctx, "resources/read", up, it.resolve, vars, func(string, any) error { return nil })
	o.Hits = append(o.Hits, co.hits...)
	if expired(ctx) {
		return budgetOutcome(o)
	}
	switch {
	case co.denied:
		o.Error = &RPCError{Code: CodeInvalidParams, Message: denyOr(co.reason, method+" was refused by policy")}
		return o
	case co.elicit != nil:
		out, err := e.elicitation(r, s, method, up, hash, st, answers, co.elicit)
		if err != nil {
			o.Error = &RPCError{Code: CodeInvalidParams, Message: err.Error()}
			return o
		}
		o.Result = out
		return o
	}
	fp := &mcp.ReadResourceParams{URI: up}
	if st != nil && st.UpstreamRound {
		fp.RequestState, fp.InputResponses = st.Upstream, upResp
	}
	ci, chits := e.Connect(ctx, r, s, e.version())
	o.Hits = append(o.Hits, chits...)
	if ci.ForwardMeta {
		fp.Meta = forwardMeta(p.Meta)
	}
	if expired(ctx) {
		return budgetOutcome(o)
	}
	o.Forward = &Forward{Slug: s.Slug, Method: method, Params: fp, Connect: &ci}
	o.Call = &CallContext{Server: s, Method: method, Target: up, Exposed: p.URI, Item: res, Args: args, Hash: hash, State: st, Answers: answers}
	return o
}

// UpstreamReadReply handles the upstream's answer to a forwarded read or
// directory read, using the CallContext read returned with the forward.
func (e *Engine) UpstreamReadReply(parent context.Context, r Request, call *CallContext, p *mcp.ReadResourceParams, raw json.RawMessage) (ret Outcome) {
	defer e.recovered(&ret, "resources/read reply", readCallRef("resources/read", p))
	ctx, cancel := e.ctx(parent)
	defer cancel()
	if call == nil {
		return errOutcome(-32603, "no forwarded request to reply to")
	}
	if len(raw) > e.Limits.maxBytes() {
		return errOutcome(CodeTooLarge, "upstream result exceeds gateway size limit")
	}
	s, up, method := call.Server, call.Target, call.Method
	var o Outcome
	m, ok := decodeExact(raw).(map[string]any)
	if !ok {
		return errOutcome(-32603, "upstream result is not a JSON object")
	}
	var seen unknowns
	defer func() { ret.Unknown = append(ret.Unknown, seen.list()...) }()
	if rt, _ := m["resultType"].(string); rt != "" && rt != "complete" && rt != "input_required" {
		seen.add(UnknownResultType, rt, s.Slug, up, "passed", m)
	}
	inputUnknowns(&seen, s.Slug, up, m)
	v := e.resourceView(ctx, r, s)
	if isInputRequired(m) {
		if msg, ok := inputAllowed(r, s); !ok {
			o.Issues = append(o.Issues, s.Slug+": "+up+" asked for input and was refused: "+msg)
			o.Error = &RPCError{Code: CodeInvalidParams, Message: msg}
			return e.finishOutcome(ctx, o)
		}
		it := &item{kind: KindResource, id: up, view: NewView(call.Item), label: up}
		out, hits, err := e.inputHook(ctx, r, s, "resources/input", up, it.resolve, call.Args, rawRequests(m))
		o.Hits = append(o.Hits, hits...)
		if err != nil {
			o.Error = &RPCError{Code: CodeInvalidParams, Message: err.Error()}
			return e.finishOutcome(ctx, o)
		}
		upState, _ := m["requestState"].(string)
		st, err := e.wrapState(r, s, call, p.RequestState, upState, out)
		if err != nil {
			o.Error = &RPCError{Code: CodeInvalidParams, Message: err.Error()}
			return e.finishOutcome(ctx, o)
		}
		o.Result = InputRequired{InputRequests: out, RequestState: st}
		return e.finishOutcome(ctx, o)
	}
	m = copyMap(m)
	if method == "resources/directory/read" {
		kept := []any{}
		children, _ := m["resources"].([]any)
		for _, c := range children {
			cm, _ := c.(map[string]any)
			cu, _ := cm["uri"].(string)
			if cu == "" || nonCanonical(cu) || !underRoot(cu, up) || normalizeURI(cu) == normalizeURI(up) {
				o.Issues = append(o.Issues, "dropped directory entry outside "+up+": "+cu)
				continue
			}
			if _, ok, _ := e.checkURI(ctx, r, s, v, cu); !ok {
				continue
			}
			kept = append(kept, cm)
		}
		m["resources"] = kept
	} else {
		contents, _ := m["contents"].([]any)
		kept := []any{}
		for _, c := range contents {
			cm, _ := c.(map[string]any)
			cu, _ := cm["uri"].(string)
			if cu == "" {
				cu = up
			}
			if nonCanonical(cu) {
				o.Issues = append(o.Issues, "dropped content with a non-canonical URI: "+cu)
				continue
			}
			if normalizeURI(cu) != normalizeURI(up) {
				if _, ok, _ := e.checkURI(ctx, r, s, v, cu); !ok {
					o.Issues = append(o.Issues, "dropped content for "+cu+": not readable by this caller")
					continue
				}
			}
			text, isText := cm["text"].(string)
			mime, _ := cm["mimeType"].(string)
			mutate := true
			if sk, inSkill := v.skillFor(cu); inSkill {
				plan := e.planSkill(ctx, r, s, v, sk)
				switch plan.status {
				case "applied":
					if t, changed := plan.files[cu]; changed && isText {
						cm = copyMap(cm)
						cm["text"] = t
						text = t
					}
					mutate = false // the plan already applied this file's rewrite
				case "skipped", "rejected":
					mutate = false // serve the upstream bytes; decisions still apply
				}
			}
			nt, hits, reason, denied, err := e.applyResultRulesReason(ctx, r, s, v, cu, text, mime, isText)
			o.Hits = append(o.Hits, hits...)
			if denied {
				if reason == "" {
					reason = "the read result was refused by policy"
				}
				o.Error = &RPCError{Code: CodeInvalidParams, Message: reason}
				return e.finishOutcome(ctx, o)
			}
			if err != nil {
				o.Error = &RPCError{Code: CodeInvalidParams, Message: "the read result could not be checked"}
				return e.finishOutcome(ctx, o)
			}
			if mutate && isText && nt != text {
				cm = copyMap(cm)
				cm["text"] = nt
			}
			kept = append(kept, cm)
		}
		m["contents"] = kept
	}
	if r.mux() {
		m = prefixResultURIs(m, s.Slug)
	}
	m["ttlMs"], m["cacheScope"] = e.Limits.ttl(), "private"
	o.Result = complete(m)
	return e.finishOutcome(ctx, o)
}

func toAnyMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

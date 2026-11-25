package hooks

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// switchDefaults are the server.settings defaults.
var switchDefaults = map[string]bool{"tools": true, "prompts": true, "resources": true, "tasks": true, "instructions": true, "serverInfo": false, "searchRoster": true, "progress": true}

// Config evaluates server.settings: each switch starts at its default and
// matching rules set it. Errors fail closed by switching the kind off.
func (e *Engine) Config(ctx context.Context, r Request, s *Server) (sw map[string]bool, hits []Hit) {
	defer e.recoveredDirect(ctx, "server.settings", s, func(h Hit) {
		sw = map[string]bool{}
		for k := range switchDefaults {
			sw[k] = false // fail closed: every switch off
		}
		hits = []Hit{h}
	})
	out := map[string]bool{}
	if s.Broken != nil {
		for k := range switchDefaults {
			out[k] = false
		}
		return out, []Hit{{Hook: "server.settings", Rule: "(server)", Item: s.Slug, Mode: ModeEnforce, Effect: "deny", Error: "rules do not compile: " + s.Broken.Error()}}
	}
	for k, v := range switchDefaults {
		out[k] = v
	}
	o := s.Rules.run(ctx, "server.settings", s.Slug, nil, r.context(s).roots("server/discover", nil), func(key string, val any) error {
		out[key] = val.(bool)
		return nil
	})
	if o.err != nil {
		for k := range switchDefaults {
			out[k] = false // fail closed: every switch off
		}
		return out, o.hits
	}
	hits = o.hits
	// Progress and tasks work differently before the front door's version,
	// and the gateway does not translate them.
	for _, gate := range []struct{ name, reason string }{
		{"progress", "progress is only relayed from servers on " + ProtocolCurrent},
		{"tasks", "tasks are only passed through for servers on " + ProtocolCurrent},
	} {
		if !out[gate.name] || s.current() {
			continue
		}
		out[gate.name] = false
		// The default is on, so only say so when a rule asked for it.
		for _, h := range o.hits {
			for _, f := range h.Fields {
				if f == "settings."+gate.name {
					hits = append(hits, gateHit("server.settings", s.Slug, gate.name, gate.reason))
				}
			}
		}
	}
	return out, hits
}

// config reports whether a switch is on, recording hits on o.
func (e *Engine) config(ctx context.Context, r Request, s *Server, name string, o *Outcome) bool {
	sw, hits := e.Config(ctx, r, s)
	o.Hits = append(o.Hits, hits...)
	if s.Broken != nil {
		msg := s.Slug + ": rules do not compile: " + s.Broken.Error()
		dup := false
		for _, is := range o.Issues {
			if is == msg {
				dup = true
			}
		}
		if !dup {
			o.Issues = append(o.Issues, msg)
		}
	}
	return sw[name]
}

// ServerMeta is a server's title, description and instructions after
// server/discover.post.
type ServerMeta struct {
	Title        string `json:"title"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
}

// Modify evaluates server/discover.post.
func (e *Engine) Modify(ctx context.Context, r Request, s *Server) (meta ServerMeta, hits []Hit, err error) {
	defer e.recoveredDirect(ctx, "server/discover.post", s, func(h Hit) {
		meta, hits, err = ServerMeta{}, []Hit{h}, fmt.Errorf("server/discover.post could not be evaluated")
	})
	said := map[string]any{}
	for k, v := range map[string]string{"title": s.Info.Title, "description": s.Info.Description, "instructions": s.Info.Instructions} {
		if v != "" {
			said[k] = v // nothing is filled in for what the server did not say
		}
	}
	view := NewView(said)
	resolve := func(name string) (any, bool) {
		switch name {
		case "title", "description", "instructions":
			v, ok := view.Get(name)
			return v, ok && v != nil
		case "upstream":
			return view.Upstream(), true
		}
		return nil, false
	}
	o := s.Rules.run(ctx, "server/discover.post", s.Slug, resolve, r.context(s).roots("server/discover", nil), func(key string, val any) error {
		view.Set([]string{key}, val)
		return nil
	})
	m := view.Materialize()
	meta = ServerMeta{}
	meta.Title, _ = m["title"].(string)
	meta.Description, _ = m["description"].(string)
	meta.Instructions, _ = m["instructions"].(string)
	if o.err != nil {
		return ServerMeta{}, o.hits, o.err
	}
	return meta, o.hits, nil
}

// Connect evaluates server.session: what the gateway tells the upstream
// about itself. The default is anonymous: client "bifrost", no elicitation,
// _meta stripped.
type ConnectInfo struct {
	ClientInfo   map[string]any `json:"clientInfo"`
	Capabilities map[string]any `json:"capabilities"`
	ForwardMeta  bool           `json:"forwardMeta"`
}

// Connect computes the outbound identity for a server.
func (e *Engine) Connect(ctx context.Context, r Request, s *Server, version string) (info ConnectInfo, hits []Hit) {
	defer e.recoveredDirect(ctx, "server.session", s, func(h Hit) {
		// Fail closed: the anonymous default, with nothing advertised.
		info = ConnectInfo{ClientInfo: map[string]any{"name": "bifrost", "version": version}, Capabilities: map[string]any{}}
		hits = []Hit{h}
	})
	ci := ConnectInfo{ClientInfo: map[string]any{"name": "bifrost", "version": version}, Capabilities: map[string]any{}}
	o := s.Rules.run(ctx, "server.session", s.Slug, nil, r.context(s).roots("server/discover", nil), func(key string, val any) error {
		switch key {
		case "clientInfo":
			ci.ClientInfo = val.(map[string]any)
		case "capabilities.elicitation":
			if val.(bool) {
				ci.Capabilities["elicitation"] = map[string]any{}
			} else {
				delete(ci.Capabilities, "elicitation")
			}
		case "_meta":
			ci.ForwardMeta = val.(bool)
		}
		return nil
	})
	if o.err != nil {
		return ConnectInfo{ClientInfo: map[string]any{"name": "bifrost", "version": version}, Capabilities: map[string]any{}}, o.hits
	}
	hits = o.hits
	if _, on := ci.Capabilities["elicitation"]; on {
		switch {
		case !s.current():
			delete(ci.Capabilities, "elicitation")
			hits = append(hits, gateHit("server.session", s.Slug, "capabilities.elicitation", "elicitation is only offered to servers on "+ProtocolCurrent))
		case !r.clientCan("elicitation"):
			// Capabilities arrive with each request. A server must not be told
			// about one this request's client did not declare.
			delete(ci.Capabilities, "elicitation")
			hits = append(hits, gateHit("server.session", s.Slug, "capabilities.elicitation", "this request's client did not declare elicitation"))
		}
	}
	return ci, hits
}

// ---- discover ----------------------------------------------------------------------

// InstructionsTool is the gateway's own tool on the mux.
const InstructionsTool = "get_server_instructions"

func instructionsTool() *mcp.Tool {
	f := false
	return &mcp.Tool{
		Name:        InstructionsTool,
		Title:       "Get server instructions",
		Description: "Return the full instructions of one server on this gateway, by slug. The gateway's own instructions show only excerpts.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"slug": map[string]any{"type": "string"}}, "required": []any{"slug"}, "additionalProperties": false},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &f, IdempotentHint: true, OpenWorldHint: &f},
	}
}

// DiscoverResult is the gateway's own server/discover answer.
type DiscoverResult struct {
	SupportedVersions []string       `json:"supportedVersions"`
	Capabilities      map[string]any `json:"capabilities"`
	Instructions      string         `json:"instructions,omitempty"`
	Meta              map[string]any `json:"_meta,omitempty"`
	TTLMs             int            `json:"ttlMs"`
	CacheScope        string         `json:"cacheScope"`
	ResultType        string         `json:"resultType"`
}

// MetaServerInfo is where 2026-07-28 puts display identity in discover.
const MetaServerInfo = "io.modelcontextprotocol/serverInfo"

// ExtSkills is the Skills extension's capability key.
const ExtSkills = "io.modelcontextprotocol/skills"

// Discover answers server/discover. On 1:1 it reflects the one server after
// server.settings and server/discover.post; on the mux it combines slug-labelled
// instruction excerpts within budgets. listChanged is always false: the
// gateway sends no change notifications. Completions and logging are never
// advertised.
func (e *Engine) Discover(parent context.Context, r Request, version string) (ret Outcome) {
	defer e.recovered(&ret, "server/discover", callRef{method: "server/discover"})
	ctx, cancel := e.ctx(parent)
	defer cancel()
	var o Outcome
	info := map[string]any{"name": "bifrost", "version": version}
	if len(e.Icons) > 0 {
		info["icons"] = e.Icons
	}
	res := DiscoverResult{SupportedVersions: []string{"2026-07-28"}, TTLMs: e.Limits.ttl(), CacheScope: "private", ResultType: "complete"}
	caps := map[string]any{}
	anyOn := func(kind string) bool {
		for _, s := range r.Servers {
			if e.config(ctx, r, s, kind, &o) {
				return true
			}
		}
		return false
	}
	if anyOn("tools") || r.Route == "mux" {
		caps["tools"] = map[string]any{"listChanged": false}
	}
	if anyOn("prompts") {
		caps["prompts"] = map[string]any{"listChanged": false}
	}
	if anyOn("resources") {
		caps["resources"] = map[string]any{"listChanged": false, "subscribe": false}
		for _, s := range r.Servers {
			if len(s.Catalog.Skills) > 0 && e.config(ctx, r, s, "resources", &o) {
				caps["extensions"] = map[string]any{ExtSkills: map[string]any{"directoryRead": true}}
				break
			}
		}
	}
	res.Capabilities = caps
	if !r.mux() && len(r.Servers) == 1 {
		s := r.Servers[0]
		sw, hits := e.Config(ctx, r, s)
		o.Hits = append(o.Hits, hits...)
		meta, hits, err := e.Modify(ctx, r, s)
		o.Hits = append(o.Hits, hits...)
		if err != nil {
			o.Issues = append(o.Issues, s.Slug+": server/discover.post: "+err.Error())
		}
		if sw["instructions"] {
			res.Instructions = meta.Instructions
		}
		if sw["serverInfo"] {
			info = map[string]any{"name": s.Info.Name, "version": s.Info.Version}
			if len(s.Info.Icons) > 0 {
				info["icons"] = s.Info.Icons
			}
		}
		if meta.Title != "" {
			info["title"] = meta.Title
		}
		// The extensions the gateway passes through, each as the server
		// advertises it.
		for _, name := range Extensions {
			if v, on := e.extensionOn(ctx, r, s, name, &o); on {
				exts, _ := caps["extensions"].(map[string]any)
				if exts == nil {
					exts = map[string]any{}
					caps["extensions"] = exts
				}
				exts[name] = v
			}
		}
		res.Meta = map[string]any{MetaServerInfo: info}
		o.Result = res
		return e.finishOutcome(ctx, o)
	}
	res.Meta = map[string]any{MetaServerInfo: info}
	res.Instructions = e.muxInstructions(ctx, r, &o)
	o.Result = res
	return e.finishOutcome(ctx, o)
}

// TruncationMark ends an excerpt that was cut.
const TruncationMark = "… [truncated; call get_server_instructions for the full text]"

func (e *Engine) muxInstructions(ctx context.Context, r Request, o *Outcome) string {
	per, all := e.Limits.InstructionsPer, e.Limits.InstructionsAll
	if per <= 0 {
		per = 600
	}
	if all <= 0 {
		all = 4000
	}
	var b strings.Builder
	b.WriteString("This gateway serves several MCP servers. Tool and prompt names are written <slug>__<name>. Resources and skills from each server are served under <slug>+<scheme>; a reference inside a server's content to <scheme>://<path> means <slug>+<scheme>://<path>.\n")
	used := 0
	for _, s := range sortedServers(r.Servers) {
		sw, hits := e.Config(ctx, r, s)
		o.Hits = append(o.Hits, hits...)
		if !sw["instructions"] {
			continue
		}
		meta, hits, err := e.Modify(ctx, r, s)
		o.Hits = append(o.Hits, hits...)
		if err != nil || strings.TrimSpace(meta.Instructions) == "" {
			continue
		}
		text := meta.Instructions
		limit := per
		if all-used < limit {
			limit = all - used
		}
		if limit <= 0 {
			fmt.Fprintf(&b, "\n## %s\n%s\n", s.Slug, TruncationMark)
			continue
		}
		if utf8.RuneCountInString(text) > limit {
			used += limit
			text = string([]rune(text)[:limit]) + TruncationMark
		} else {
			used += utf8.RuneCountInString(text)
		}
		fmt.Fprintf(&b, "\n## %s\n%s\n", s.Slug, quoteLines(text))
	}
	return b.String()
}

// ServerInstructions answers the get_server_instructions tool.
func (e *Engine) ServerInstructions(parent context.Context, r Request, slug string) (ret Outcome) {
	defer e.recovered(&ret, "server/instructions", callRef{method: "server/instructions", target: slug})
	ctx, cancel := e.ctx(parent)
	defer cancel()
	var o Outcome
	s := r.server(slug)
	if s == nil {
		o.Result = toolError("unknown slug " + slug)
		return o
	}
	if !e.config(ctx, r, s, "instructions", &o) {
		o.Result = toolError("instructions are not available for " + slug)
		return o
	}
	meta, hits, err := e.Modify(ctx, r, s)
	o.Hits = append(o.Hits, hits...)
	if err != nil {
		o.Result = toolError("instructions are not available for " + slug)
		return o
	}
	o.Result = &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: meta.Instructions}}}
	return e.finishOutcome(ctx, o)
}

// quoteLines quotes every line of an upstream excerpt ("> "), so upstream
// text cannot start a heading that poses as another server's section. Line
// endings are normalized first. This is presentation, not a trust boundary.
func quoteLines(text string) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

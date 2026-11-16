package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// switchDefaults are the server/config defaults.
var switchDefaults = map[string]bool{"tools": true, "prompts": true, "resources": true, "tasks": false, "instructions": true, "serverInfo": false, "searchRoster": true, "progress": true}

// Config evaluates server/config: each switch starts at its default and
// matching rules set it. Errors fail closed by switching the kind off.
func (e *Engine) Config(ctx context.Context, r Request, s *Server) (sw map[string]bool, hits []Hit) {
	defer e.recoveredDirect(ctx, "server/config", s, func(h Hit) {
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
		return out, []Hit{{Hook: "server/config", Rule: "(entitlement)", Item: s.Slug, Mode: ModeEnforce, Effect: "deny", Error: "rules do not compile: " + s.Broken.Error()}}
	}
	for k, v := range switchDefaults {
		out[k] = v
	}
	o := s.Rules.run(ctx, "server/config", s.Slug, nil, r.context(s).vars(), func(key string, val any) error {
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
	// Tasks are an extension the gateway does not support yet. The switch
	// stays off for every server until result rules also run on task results.
	if out["tasks"] {
		out["tasks"] = false
		hits = append(hits, gateHit("server/config", s.Slug, "tasks", "tasks are not supported; the switch stays off"))
	}
	if out["progress"] && !s.current() {
		out["progress"] = false
		// The default is on, so only say so when a rule asked for it.
		for _, h := range o.hits {
			for _, f := range h.Fields {
				if f == "progress" {
					hits = append(hits, gateHit("server/config", s.Slug, "progress", "progress is only relayed from servers on "+ProtocolCurrent))
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
// server/modify.
type ServerMeta struct {
	Title        string `json:"title"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
}

// Modify evaluates server/modify.
func (e *Engine) Modify(ctx context.Context, r Request, s *Server) (meta ServerMeta, hits []Hit, err error) {
	defer e.recoveredDirect(ctx, "server/modify", s, func(h Hit) {
		meta, hits, err = ServerMeta{}, []Hit{h}, fmt.Errorf("server/modify could not be evaluated")
	})
	view := NewView(map[string]any{"title": s.Info.Title, "description": s.Info.Description, "instructions": s.Info.Instructions})
	resolve := func(name string) (any, bool) {
		switch name {
		case "title", "description", "instructions":
			return view.Get(name)
		case "upstream":
			return view.Upstream(), true
		}
		return nil, false
	}
	o := s.Rules.run(ctx, "server/modify", s.Slug, resolve, r.context(s).vars(), func(key string, val any) error {
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

// Connect evaluates server/connect: what the gateway tells the upstream
// about itself. The default is anonymous: client "bifrost", no elicitation,
// _meta stripped.
type ConnectInfo struct {
	ClientInfo   map[string]any `json:"clientInfo"`
	Capabilities map[string]any `json:"capabilities"`
	ForwardMeta  bool           `json:"forwardMeta"`
}

// Connect computes the outbound identity for a server.
func (e *Engine) Connect(ctx context.Context, r Request, s *Server, version string) (info ConnectInfo, hits []Hit) {
	defer e.recoveredDirect(ctx, "server/connect", s, func(h Hit) {
		// Fail closed: the anonymous default, with nothing advertised.
		info = ConnectInfo{ClientInfo: map[string]any{"name": "bifrost", "version": version}, Capabilities: map[string]any{}}
		hits = []Hit{h}
	})
	ci := ConnectInfo{ClientInfo: map[string]any{"name": "bifrost", "version": version}, Capabilities: map[string]any{}}
	o := s.Rules.run(ctx, "server/connect", s.Slug, nil, r.context(s).vars(), func(key string, val any) error {
		switch key {
		case "clientInfo":
			ci.ClientInfo = val.(map[string]any)
		case "capabilities.elicitation":
			if val.(bool) {
				ci.Capabilities["elicitation"] = map[string]any{}
			} else {
				delete(ci.Capabilities, "elicitation")
			}
		case "meta":
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
			hits = append(hits, gateHit("server/connect", s.Slug, "capabilities.elicitation", "elicitation is only offered to servers on "+ProtocolCurrent))
		case !r.clientCan("elicitation"):
			// Capabilities arrive with each request. A server must not be told
			// about one this request's client did not declare.
			delete(ci.Capabilities, "elicitation")
			hits = append(hits, gateHit("server/connect", s.Slug, "capabilities.elicitation", "this request's client did not declare elicitation"))
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
// server/config and server/modify; on the mux it combines slug-labelled
// instruction excerpts within budgets. listChanged is always false: the
// gateway sends no change notifications. Completions and logging are never
// advertised.
func (e *Engine) Discover(parent context.Context, r Request, version string) (ret Outcome) {
	defer e.recovered(&ret, "server/discover", callRef{method: "server/discover"})
	ctx, cancel := e.ctx(parent)
	defer cancel()
	var o Outcome
	info := map[string]any{"name": "bifrost", "version": version}
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
			o.Issues = append(o.Issues, s.Slug+": server/modify: "+err.Error())
		}
		if sw["instructions"] {
			res.Instructions = meta.Instructions
		}
		if sw["serverInfo"] {
			info = map[string]any{"name": s.Info.Name, "version": s.Info.Version}
		}
		if meta.Title != "" {
			info["title"] = meta.Title
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
			b.WriteString(fmt.Sprintf("\n## %s\n%s\n", s.Slug, TruncationMark))
			continue
		}
		if utf8.RuneCountInString(text) > limit {
			used += limit
			text = string([]rune(text)[:limit]) + TruncationMark
		} else {
			used += utf8.RuneCountInString(text)
		}
		b.WriteString(fmt.Sprintf("\n## %s\n%s\n", s.Slug, quoteLines(text)))
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

// ---- upstream results -----------------------------------------------------------------

// UpstreamToolReply handles what the upstream answered to a forwarded
// tools/call. call is the CallContext CallTool returned with the forward:
// reply hooks see exactly the arguments that were sent. An input_required
// (with input requests, state, or both) goes through tools/elicit and is
// wrapped in gateway state; a result goes through tools/result and then,
// on the mux, gets the slug prefix on structured URIs (last, so URIs rules
// add are prefixed too). p is the client's call, for its request state.
func (e *Engine) UpstreamToolReply(parent context.Context, r Request, call *CallContext, p *mcp.CallToolParamsRaw, raw json.RawMessage) (ret Outcome) {
	defer e.recovered(&ret, "tools/call reply", toolCallRef(p))
	ctx, cancel := e.ctx(parent)
	defer cancel()
	if call == nil || call.Transform == nil {
		return errOutcome(-32603, "no forwarded call to reply to")
	}
	if len(raw) > e.Limits.maxBytes() {
		return errOutcome(CodeTooLarge, "upstream result exceeds gateway size limit")
	}
	s, tr := call.Server, call.Transform
	var o Outcome
	result, ok := decodeExact(raw).(map[string]any)
	if !ok {
		return errOutcome(-32603, "upstream result is not a JSON object")
	}
	itemResolve := replyResolve(call)
	var seen unknowns
	defer func() { ret.Unknown = append(ret.Unknown, seen.list()...) }()
	if rt, _ := result["resultType"].(string); rt != "" && rt != "complete" && rt != "input_required" {
		action := "passed"
		if rt == "task" {
			action = "refused"
		}
		seen.add(UnknownResultType, rt, s.Slug, tr.Upstream, action, result)
	}
	inputUnknowns(&seen, s.Slug, tr.Upstream, result)
	contentUnknowns(&seen, s.Slug, tr.Upstream, result["content"])
	if rt, _ := result["resultType"].(string); rt == "task" {
		// Tasks are an extension the gateway does not support and never
		// declares upstream. A handle returned anyway is refused: its result
		// would later be fetched without passing through any result rule.
		o.Issues = append(o.Issues, s.Slug+": "+tr.Upstream+" returned a task handle; tasks are not supported, so it was refused")
		o.Result = toolError("This tool started a background task, which this gateway does not support.")
		return e.finishOutcome(ctx, o)
	}
	if isInputRequired(result) {
		if msg, ok := inputAllowed(r, s); !ok {
			o.Issues = append(o.Issues, s.Slug+": "+tr.Upstream+" asked for input and was refused: "+msg)
			o.Result = toolError(msg)
			return e.finishOutcome(ctx, o)
		}
		reqs, hits, err := e.elicitHook(ctx, r, s, "tools/elicit", tr.Exposed, itemResolve, call.Args, rawRequests(result))
		o.Hits = append(o.Hits, hits...)
		if err != nil {
			o.Result = toolError(err.Error())
			return e.finishOutcome(ctx, o)
		}
		upState, _ := result["requestState"].(string)
		st, err := e.wrapState(r, s, call, p.RequestState, upState, reqs)
		if err != nil {
			o.Result = toolError(err.Error())
			return e.finishOutcome(ctx, o)
		}
		o.Result = InputRequired{InputRequests: reqs, RequestState: st}
		return e.finishOutcome(ctx, o)
	}
	view := NewView(result)
	vars := r.context(s).vars()
	vars["args"] = celArgs(call.Args)
	vars["result"] = resultVar(result)
	setKeys := map[string]bool{}
	ro := s.Rules.run(ctx, "tools/result", tr.Exposed, itemResolve, vars, func(key string, val any) error {
		setKeys[key] = true
		view.Set([]string{key}, val)
		vars["result"] = resultVar(view.Materialize())
		return nil
	})
	o.Hits = append(o.Hits, ro.hits...)
	if ro.denied {
		o.Result = toolError(denyText(ro.reason, tr.Exposed))
		return e.finishOutcome(ctx, o)
	}
	final := view.Materialize()
	if v, ok := final["structuredContent"]; ok && v == nil && setKeys["structuredContent"] {
		final = copyMap(final)
		delete(final, "structuredContent") // a rule set it to null: plain output only
	}
	if r.mux() {
		final = prefixResultURIs(final, s.Slug)
	}
	o.Result = complete(final)
	return e.finishOutcome(ctx, o)
}

// isInputRequired recognizes both permitted input_required forms: input
// requests with optional state, and state only.
func isInputRequired(m map[string]any) bool {
	if m["resultType"] == "input_required" {
		return true
	}
	if reqs, ok := m["inputRequests"].(map[string]any); ok && len(reqs) > 0 {
		return true
	}
	return false
}

func rawRequests(m map[string]any) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	reqs, _ := m["inputRequests"].(map[string]any)
	for k, v := range reqs {
		b, _ := json.Marshal(v)
		out[k] = b
	}
	return out
}

// finishOutcome is the last step of every reply: the budget check and the
// size cap on the serialized result.
func (e *Engine) finishOutcome(ctx context.Context, o Outcome) Outcome {
	if expired(ctx) {
		return budgetOutcome(o)
	}
	if o.Result != nil {
		if b, err := json.Marshal(o.Result); err == nil && len(b) > e.Limits.maxBytes() {
			o.Result = nil
			o.Error = &RPCError{Code: CodeTooLarge, Message: "result exceeds gateway size limit after shaping"}
		}
	}
	return o
}

// resultVar is the `result` variable: the result plus `text`, all text
// content joined.
func resultVar(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = celValue(v)
	}
	var parts []string
	if cs, ok := m["content"].([]any); ok {
		for _, c := range cs {
			if cm, ok := c.(map[string]any); ok && cm["type"] == "text" {
				if t, ok := cm["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
	}
	if cs, ok := m["contents"].([]any); ok {
		for _, c := range cs {
			if cm, ok := c.(map[string]any); ok {
				if t, ok := cm["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
	}
	out["text"] = strings.Join(parts, "\n")
	if _, ok := out["isError"]; !ok {
		out["isError"] = false
	}
	if meta, ok := m["_meta"].(map[string]any); ok {
		out["meta"] = meta
	} else {
		out["meta"] = map[string]any{}
	}
	return out
}

// elicitHook runs an elicit hook over each question the upstream asks. A deny
// refuses the whole round; message changes apply to the request.
func (e *Engine) elicitHook(ctx context.Context, r Request, s *Server, hook, label string, resolve func(string) (any, bool), upArgs map[string]any, in map[string]json.RawMessage) (mcp.InputRequestMap, []Hit, error) {
	out := mcp.InputRequestMap{}
	var hits []Hit
	for _, key := range sortedKeys(in) {
		if strings.HasPrefix(key, KeyPrefix) {
			return nil, hits, fmt.Errorf("upstream input request %q uses the gateway's reserved prefix", key)
		}
		var wire struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(in[key], &wire); err != nil || wire.Params == nil {
			return nil, hits, fmt.Errorf("upstream input request %q is invalid", key)
		}
		if wire.Method != "elicitation/create" {
			return nil, hits, fmt.Errorf("upstream input request %q uses %q; only elicitation is supported", key, wire.Method)
		}
		req := copyMap(wire.Params)
		req["key"], req["method"] = key, wire.Method
		view := NewView(req)
		vars := r.context(s).vars()
		vars["args"] = celArgs(upArgs)
		vars["request"] = req
		o := s.Rules.run(ctx, hook, label, resolve, vars, func(k string, val any) error {
			view.Set([]string{k}, val)
			vars["request"] = view.Materialize()
			return nil
		})
		hits = append(hits, o.hits...)
		if o.denied {
			return nil, hits, fmt.Errorf("%s", denyOr(o.reason, "The upstream server asked for input, which policy does not allow."))
		}
		final := view.Materialize()
		delete(final, "key")
		delete(final, "method")
		b, _ := json.Marshal(final)
		var ep mcp.ElicitParams
		if err := json.Unmarshal(b, &ep); err != nil {
			return nil, hits, fmt.Errorf("upstream input request %q is not an elicitation", key)
		}
		out[key] = &ep
	}
	return out, hits, nil
}

// inputUnknowns reports input requests that are not elicitations. The
// gateway refuses them: it can only relay a question it can show a rule.
func inputUnknowns(u *unknowns, server, target string, result map[string]any) {
	reqs, _ := result["inputRequests"].(map[string]any)
	for _, k := range sortedKeys(reqs) {
		m, _ := reqs[k].(map[string]any)
		if method, _ := m["method"].(string); method != "elicitation/create" {
			u.add(UnknownInputRequest, method, server, target, "refused", m["params"])
		}
	}
}

// inputAllowed reports whether an upstream's input request may be passed to
// this request's client, and what to say when it may not.
func inputAllowed(r Request, s *Server) (string, bool) {
	if !s.current() {
		return "The server asked for input, which the gateway only supports for servers on " + ProtocolCurrent + ".", false
	}
	if !r.clientCan("elicitation") {
		return "The server asked for input, which this client does not support.", false
	}
	return "", true
}

// complete marks a result as final. The front door's version requires
// resultType on every result; older servers do not send it.
func complete(m map[string]any) map[string]any {
	if _, ok := m["resultType"]; ok {
		return m
	}
	out := copyMap(m)
	out["resultType"] = "complete"
	return out
}

func denyOr(reason, def string) string {
	if reason != "" {
		return reason
	}
	return def
}

// wrapState builds the gateway's envelope around an upstream round: the
// forwarded call's binding, earlier gateway answers, and the upstream's own
// state (which may be absent). It needs a policy generation.
func (e *Engine) wrapState(r Request, s *Server, call *CallContext, clientToken, upstreamState string, reqs mcp.InputRequestMap) (string, error) {
	if r.principal() == "" {
		return "", fmt.Errorf("The server asked for input, which is not available without a caller identity.")
	}
	now := r.Now
	if now.IsZero() {
		now = timeNow()
	}
	st := State{Challenge: fmt.Sprintf("u%x", now.UnixNano()), Method: call.Method, Caller: r.principal(), Entitlement: entitlementID(s),
		Target: call.Target, ArgsHash: call.Hash, Generation: generation(s), Expires: now.Add(StateTTL).Unix(), Upstream: upstreamState, UpstreamRound: true}
	if len(call.Answers) > 0 {
		st.Answers = map[string]map[string]any{}
		for k, v := range call.Answers {
			if m, ok := v.(map[string]any); ok {
				st.Answers[k] = m
			}
		}
	}
	for k := range reqs {
		st.UpPending = append(st.UpPending, k)
	}
	sort.Strings(st.UpPending)
	return e.Signer.Seal(st), nil
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

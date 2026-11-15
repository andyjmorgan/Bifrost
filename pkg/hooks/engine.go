package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MaxArgDepth bounds nesting in client arguments.
const MaxArgDepth = 64

// Separator joins slug and name on the mux route.
const Separator = "__"

// Server is one entitlement as the engine sees it: a slug on one upstream
// server, that server's metadata and cached catalog, and the entitlement's
// compiled rules.
type Server struct {
	Slug        string
	Entitlement map[string]any
	Info        ServerInfo
	Rules       *Set
	Generation  string
	Catalog     Catalog
	// Unavailable marks a server whose catalog could not be fetched; fallback
	// resolution refuses to pick a match while one is unknown.
	Unavailable bool
	// Broken holds the reason the entitlement's rules do not compile. A
	// broken entitlement fails closed: it exposes nothing.
	Broken error
}

// ProtocolCurrent is the protocol version the gateway's front door speaks.
// An upstream may speak any version, but only one on this version can be
// offered elicitation and progress. A server on an older version, or one
// whose version is not known, gets neither, whatever the rules say: those
// features work differently before this version, and the gateway does not
// translate them.
const ProtocolCurrent = "2026-07-28"

// current reports whether the upstream speaks the front door's version.
func (s *Server) current() bool { return s != nil && s.Info.ProtocolVersion == ProtocolCurrent }

// gateHit records a setting the gateway overrode, so an administrator can
// see why a rule had no effect.
func gateHit(hook, slug, field, reason string) Hit {
	return Hit{Hook: hook, Rule: "(gateway)", Item: slug, Mode: ModeEnforce, Effect: "set", Fields: []string{field}, Reason: reason}
}

// ServerInfo is what discover said about the upstream.
type ServerInfo struct {
	Name            string `json:"name,omitempty"`
	Version         string `json:"version,omitempty"`
	Title           string `json:"title,omitempty"`
	Description     string `json:"description,omitempty"`
	Instructions    string `json:"instructions,omitempty"`
	URL             string `json:"url,omitempty"`
	ProtocolVersion string `json:"protocolVersion,omitempty"`
}

// Catalog is an upstream's cached listings, in SDK shapes.
type Catalog struct {
	Tools     []*mcp.Tool             `json:"tools,omitempty"`
	Prompts   []*mcp.Prompt           `json:"prompts,omitempty"`
	Resources []*mcp.Resource         `json:"resources,omitempty"`
	Templates []*mcp.ResourceTemplate `json:"resourceTemplates,omitempty"`
	Skills    []Skill                 `json:"skills,omitempty"`
}

// Limits bound what the engine accepts and produces.
type Limits struct {
	MaxResponseBytes int           `json:"maxResponseBytes,omitempty"` // upstream responses over this are rejected; 0 = 10 MiB
	Budget           time.Duration `json:"budget,omitempty"`           // time for all rules in one request; 0 = 2s
	TTLMs            int           `json:"ttlMs,omitempty"`            // ttlMs on gateway list results; 0 = 300000
	InstructionsPer  int           `json:"instructionsPer,omitempty"`  // mux excerpt budget per server; 0 = 600
	InstructionsAll  int           `json:"instructionsAll,omitempty"`  // mux excerpt budget in total; 0 = 4000
	Workers          int           `json:"workers,omitempty"`
	CostLimit        uint64        `json:"costLimit,omitempty"`   // CEL cost per evaluation; 0 = DefaultCostLimit
	AuditBudget      time.Duration `json:"auditBudget,omitempty"` // time for audit rules in one request; 0 = 100ms
	// ProgressBudget is the time for progress rules across one reply
	// stream; 0 means the same as Budget. When it is spent, later progress
	// events are dropped and the final response keeps its own Budget.
	ProgressBudget time.Duration `json:"progressBudget,omitempty"`
}

func (l Limits) progressBudget() time.Duration {
	if l.ProgressBudget <= 0 {
		return l.budget()
	}
	return l.ProgressBudget
}

func (l Limits) maxBytes() int {
	if l.MaxResponseBytes <= 0 {
		return 10 << 20
	}
	return l.MaxResponseBytes
}

func (l Limits) budget() time.Duration {
	if l.Budget <= 0 {
		return 2 * time.Second
	}
	return l.Budget
}

func (l Limits) auditBudget() time.Duration {
	if l.AuditBudget <= 0 {
		return 100 * time.Millisecond
	}
	return l.AuditBudget
}

func (l Limits) ttl() int {
	if l.TTLMs <= 0 {
		return 300000
	}
	return l.TTLMs
}

// Engine evaluates requests against a caller's servers.
type Engine struct {
	Signer Signer
	Limits Limits
	// Version is the gateway version sent upstream in clientInfo.
	Version string
	// OnFault, if set, is called once for every panic the engine catches.
	// The same record is on the Outcome; the callback is for gateways that
	// want one place to log from.
	OnFault func(Fault)
	// OnUnknown, if set, is called for each thing the engine did not
	// recognise: an unfamiliar content block, notification, result type,
	// input request or template form. The same records are on the Outcome.
	OnUnknown func(Unknown)
	// States holds the gateway's own confirmation challenges. Nil means the
	// in-memory stand-in, which is only correct for a single instance.
	// TODO: the gateway supplies a shared store here (a Redis TTL key or
	// similar) before it runs more than one instance.
	States StateStore

	memOnce sync.Once
	mem     *MemoryStates
}

func (e *Engine) version() string {
	if e.Version == "" {
		return "dev"
	}
	return e.Version
}

// Request is the per-request context: who is calling, from what, on which
// route, and which entitlements the route exposes.
type Request struct {
	// Route is how the client reached the gateway: "direct" for one server,
	// or "mux" for every server the caller holds. The two are exclusive.
	// The value "search" is still accepted and means mux with the search
	// flag on; it is how older scenario files say it.
	Route string
	// Flags are the switches the client set on this request, such as
	// ?search=true, by name. They are not routes: any of them can be on for
	// either route. Rules read them as flags.<name>; see Flags for the list.
	Flags  map[string]bool
	Caller map[string]any // userId, email, role, type, keyName, orgId, clientId
	// Principal identifies whoever is making the request, as one opaque
	// string the gateway chooses: the user under a per-user credential, the
	// key under a shared one. Confirmation state is bound to it, so a
	// challenge issued to one principal cannot be answered by another. When
	// it is empty the engine falls back to Caller["userId"]; with neither,
	// it cannot bind state and refuses to ask for confirmation.
	Principal string
	Client    map[string]any // name, version, capabilities (from this request's _meta)
	Now       time.Time
	Servers   []*Server // direct: exactly one; mux/search: every entitlement on the route (already narrowed by ?slugs=)
}

func (r Request) context(s *Server) Context {
	srv := map[string]any{
		"slug": s.Slug, "name": s.Info.Name, "title": s.Info.Title, "description": s.Info.Description,
		"instructions": s.Info.Instructions, "protocolVersion": s.Info.ProtocolVersion, "url": s.Info.URL,
	}
	return Context{Server: srv, Entitlement: s.Entitlement, Caller: r.Caller, Client: r.Client, Route: r.route(), Flags: r.flags(), Now: r.Now}
}

// route is the route rules see: "direct" or "mux", never "search".
func (r Request) route() string {
	if r.mux() {
		return "mux"
	}
	return "direct"
}

// search reports whether the request is in search mode, on either route.
func (r Request) search() bool { return r.Flags[FlagSearch] || r.Route == "search" }

// flags is the request's flags with the older spelling of search mode
// folded in.
func (r Request) flags() map[string]bool {
	if r.Route != "search" || r.Flags[FlagSearch] {
		return r.Flags
	}
	out := make(map[string]bool, len(r.Flags)+1)
	for k, v := range r.Flags {
		out[k] = v
	}
	out[FlagSearch] = true
	return out
}

// plainMux is the mux route without search mode, where the gateway lists
// its own instructions tool beside the servers' tools.
func (r Request) plainMux() bool { return r.mux() && !r.search() }

// principal is the identity confirmation state is bound to, or "" when the
// gateway supplied none.
func (r Request) principal() string {
	if r.Principal != "" {
		return r.Principal
	}
	if id, ok := r.Caller["userId"]; ok && id != nil {
		if s := fmt.Sprint(id); s != "" {
			return "user:" + s
		}
	}
	return ""
}

// errNoPrincipal refuses a confirmation that could not be bound to anyone.
var errNoPrincipal = errors.New("This request needs confirmation, which is not available without a caller identity.")

func (r Request) mux() bool { return r.Route == "mux" || r.Route == "search" }

func (r Request) server(slug string) *Server {
	for _, s := range r.Servers {
		if s.Slug == slug {
			return s
		}
	}
	return nil
}

func (r Request) clientCan(capability string) bool {
	caps, _ := r.Client["capabilities"].(map[string]any)
	v, ok := caps[capability]
	if !ok || v == nil {
		return false
	}
	if b, isBool := v.(bool); isBool {
		return b
	}
	return true
}

// RPCError is a JSON-RPC error returned to the client.
type RPCError struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("%d: %s", e.Code, e.Message) }

// Error codes.
const (
	CodeInvalidParams = -32602
	CodeUnknownTool   = -32602 // the spec's code for an unknown tool; -32004 is the Go SDK's own "server is closing"
	CodeTooLarge      = -32013
)

// Forward is what the gateway would send upstream.
type Forward struct {
	Slug   string `json:"slug"`
	Method string `json:"method"`
	Params any    `json:"params"`
	// Timeout set by a tools/call rule.
	Timeout string `json:"timeout,omitempty"`
	// Connect is what server/connect decided the gateway tells the
	// upstream: client identity, capabilities and whether _meta is forwarded.
	Connect *ConnectInfo `json:"connect,omitempty"`
}

// FallbackEvent describes an unprefixed URI resolved (or not) on the mux.
// The gateway records it in audit and counts it in metrics.
type FallbackEvent struct {
	URI        string `json:"uri"`
	Method     string `json:"method"`
	Outcome    string `json:"outcome"` // resolved | ambiguous | not_found | unavailable
	Slug       string `json:"slug,omitempty"`
	Candidates int    `json:"candidates"`
	Template   bool   `json:"template,omitempty"` // resolved through a template rather than an exact listing
}

// Outcome is the result of one engine operation.
type Outcome struct {
	Result  any       `json:"result,omitempty"`
	Error   *RPCError `json:"error,omitempty"`
	Forward *Forward  `json:"forward,omitempty"`
	Hits    []Hit     `json:"hits,omitempty"`
	Issues  []string  `json:"issues,omitempty"`
	// Call carries what was actually forwarded, for the reply handlers. It
	// is set only with Forward.
	Call *CallContext `json:"-"`
	// Fallback is set when an unprefixed URI went through fallback resolution.
	Fallback *FallbackEvent `json:"fallback,omitempty"`
	// Fault is set when the engine caught a panic while handling the
	// request. The gateway should log it.
	Fault *Fault `json:"fault,omitempty"`
	// Unknown lists what the engine did not recognise in this request, by
	// shape and never by value. The gateway should log it for investigation.
	Unknown []Unknown `json:"unknown,omitempty"`
}

// CallContext is the immutable record of a forwarded request: the server,
// the exposed item, the exact upstream arguments and the gateway state that
// authorized it. Reply handlers use it instead of recomputing anything.
type CallContext struct {
	Server    *Server
	Method    string
	Target    string // upstream tool or prompt name, or resource URI
	Exposed   string
	Transform *ToolTransform
	Item      map[string]any // the exposed item's fields (prompts and resources)
	// Bare marks a resource the server does not list: rules may read its
	// `uri` and nothing else, on the reply side as on the request side.
	Bare    bool
	Args    map[string]any // exactly what was sent upstream
	Hash    string
	State   *State
	Answers map[string]any // gateway answers validated for this request, carried into an upstream round
}

// expired reports a spent rule budget; every success and forward path
// checks it last.
func expired(ctx context.Context) bool { return ctx.Err() != nil || budgetFrom(ctx).enforceExhausted() }

func budgetOutcome(o Outcome) Outcome {
	o.Result, o.Forward, o.Call = nil, nil, nil
	o.Error = &RPCError{Code: -32603, Message: "rule evaluation exceeded its time budget"}
	return o
}

func errOutcome(code int64, msg string) Outcome {
	return Outcome{Error: &RPCError{Code: code, Message: msg}}
}

func toolError(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// ctx starts a request: the parent context (cancellation) plus the rule
// budgets. The enforcement budget is accounted time, not a wall-clock
// deadline, so audit work cannot spend it.
func (e *Engine) ctx(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return withBudget(parent, e.Limits.budget(), e.Limits.auditBudget()), func() {}
}

// ---- tools/list ---------------------------------------------------------------------

// ListedTools is a tools/list result plus the gateway's working data.
type ListedTools struct {
	Result *mcp.ListToolsResult `json:"result"`
	Hits   []Hit                `json:"hits,omitempty"`
	Issues []string             `json:"issues,omitempty"`
}

// ListTools lists every server's exposed tools. On the mux, names are
// prefixed slug__name. Output is ordered by slug a-z, then name a-z.
func (e *Engine) ListTools(parent context.Context, r Request) (ret Outcome) {
	defer e.recovered(&ret, "tools/list", callRef{method: "tools/list"})
	ctx, cancel := e.ctx(parent)
	defer cancel()
	res := &mcp.ListToolsResult{Tools: []*mcp.Tool{}}
	res.TTLMs, res.CacheScope = e.Limits.ttl(), "private"
	var o Outcome
	servers := sortedServers(r.Servers)
	for _, s := range servers {
		if !e.config(ctx, r, s, "tools", &o) {
			continue
		}
		tl := s.Rules.listTools(ctx, s.Catalog.Tools, r.context(s), e.Limits.Workers, "")
		o.Hits = append(o.Hits, tl.Hits...)
		for _, is := range tl.Issues {
			o.Issues = append(o.Issues, s.Slug+": "+is)
		}
		for _, et := range tl.Tools {
			t := et.Tool
			if r.mux() {
				c := *t
				c.Name = s.Slug + Separator + t.Name
				t = &c
			}
			res.Tools = append(res.Tools, t)
		}
	}
	if r.plainMux() {
		res.Tools = append(res.Tools, instructionsTool())
	}
	o.Result = res
	return e.finishOutcome(ctx, o)
}

func sortedServers(in []*Server) []*Server {
	out := append([]*Server(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// resolveTool finds a tool by exposed name on the route. It runs filter and
// modify again (rules are never cached), so the mapping is always current.
func (e *Engine) resolveTool(ctx context.Context, r Request, name string) (*Server, *ExposedTool, []Hit, error) {
	slug := ""
	if r.mux() {
		var ok bool
		slug, name, ok = strings.Cut(name, Separator)
		if !ok || slug == "" || name == "" {
			return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: "tool name must be <slug>" + Separator + "<tool>"}
		}
	} else if len(r.Servers) == 1 {
		slug = r.Servers[0].Slug
	}
	s := r.server(slug)
	if s == nil {
		return nil, nil, nil, &RPCError{Code: CodeUnknownTool, Message: "unknown tool " + name}
	}
	var o Outcome
	if !e.config(ctx, r, s, "tools", &o) {
		return nil, nil, o.Hits, &RPCError{Code: CodeUnknownTool, Message: "unknown tool " + name}
	}
	only := ""
	if !s.Rules.renames() {
		// No rule can rename a tool, so the exposed name is the upstream
		// name: evaluate just that tool (and any duplicates of it).
		only = name
	}
	tl := s.Rules.listTools(ctx, s.Catalog.Tools, r.context(s), e.Limits.Workers, only)
	for i := range tl.Tools {
		if tl.Tools[i].Tool.Name == name {
			return s, &tl.Tools[i], append(o.Hits, tl.Hits...), nil
		}
	}
	return s, nil, append(o.Hits, tl.Hits...), &RPCError{Code: CodeUnknownTool, Message: "unknown tool " + name}
}

// ---- tools/call ---------------------------------------------------------------------

// CallTool runs the call side of a tools/call: resolve, effective arguments,
// call rules, reversal. It returns either a result for the client (a deny,
// an elicitation, an error) or the request to forward upstream.
func (e *Engine) CallTool(parent context.Context, r Request, p *mcp.CallToolParamsRaw) (ret Outcome) {
	defer e.recovered(&ret, "tools/call", toolCallRef(p))
	ctx, cancel := e.ctx(parent)
	defer cancel()
	s, et, hits, err := e.resolveTool(ctx, r, p.Name)
	if err != nil {
		var re *RPCError
		errors.As(err, &re)
		return Outcome{Error: re, Hits: hits}
	}
	o := Outcome{Hits: hits}
	in, err := decodeArgs(p.Arguments)
	if err != nil {
		o.Error = &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		return o
	}
	tr := et.Transform
	args, issue, err := tr.effectiveArgs(in)
	if err != nil {
		o.Error = &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		return o
	}
	if issue != "" {
		o.Issues = append(o.Issues, s.Slug+": "+issue)
	}
	hash := argsHash(args)
	st, answers, upResponses, serr := e.continuation(r, s, "tools/call", tr.Upstream, hash, p.RequestState, p.InputResponses)
	if serr == errDeclined {
		o.Hits = append(o.Hits, declinedHit("tools/call", tr.Exposed))
		o.Result = toolError(errDeclined.Message)
		return o
	}
	if serr != nil {
		o.Error = serr
		return o
	}
	var ept *preparedTool
	if et.Changed {
		ept = buildPrepared(et.Tool) // a per-request object: do not cache it
	} else {
		ept = prepare(et.Tool)
	}
	ts := newToolState(ept)
	upDerived := prepare(upstreamTool(s, tr.Upstream)).derived
	resolve := func(name string) (any, bool) {
		if name == "upstream" {
			return upDerived, true
		}
		return ts.resolve(name)
	}
	vars := r.context(s).vars()
	vars["args"] = celArgs(args)
	vars["input"] = answers
	var timeout time.Duration
	co := s.Rules.run(ctx, "tools/call", tr.Exposed, resolve, vars, func(key string, val any) error {
		parts := splitPath(key)
		switch parts[0] {
		case "args":
			if err := tr.patchable(parts[1]); err != nil {
				return err
			}
			args[parts[1]] = val
			vars["args"] = celArgs(args)
		case "timeout":
			timeout = val.(time.Duration)
		}
		return nil
	})
	o.Hits = append(o.Hits, co.hits...)
	if expired(ctx) {
		return budgetOutcome(o)
	}
	switch {
	case co.denied:
		o.Result = toolError(denyText(co.reason, tr.Exposed))
		return o
	case co.elicit != nil:
		res, err := e.elicitation(r, s, "tools/call", tr.Upstream, hash, st, answers, co.elicit)
		if err != nil {
			o.Result = toolError(err.Error())
			return o
		}
		o.Result = res
		return o
	}
	// Pins always win, including over call-rule patches.
	for k, pv := range tr.VisiblePins {
		args[k] = pv
	}
	if err := tr.checkConstraints(args); err != nil {
		o.Error = &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		return o
	}
	upArgs := tr.upstreamArgs(args)
	if missing := tr.missingRequired(upArgs); len(missing) > 0 {
		o.Result = toolError(fmt.Sprintf("The call to %s was refused: required parameters were not supplied by policy.", tr.Exposed))
		o.Issues = append(o.Issues, fmt.Sprintf("%s: hidden required parameters %v were not set by any rule", s.Slug, missing))
		return o
	}
	ci, chits := e.Connect(ctx, r, s, e.version())
	o.Hits = append(o.Hits, chits...)
	fp := &mcp.CallToolParams{Name: tr.Upstream, Arguments: upArgs}
	if ci.ForwardMeta {
		fp.Meta = forwardMeta(p.Meta)
	}
	if st != nil && st.UpstreamRound {
		fp.RequestState = st.Upstream
		fp.InputResponses = upResponses
	}
	if expired(ctx) {
		return budgetOutcome(o)
	}
	o.Forward = &Forward{Slug: s.Slug, Method: "tools/call", Params: fp, Connect: &ci}
	if timeout > 0 {
		o.Forward.Timeout = timeout.String()
	}
	o.Call = &CallContext{Server: s, Method: "tools/call", Target: tr.Upstream, Exposed: tr.Exposed, Transform: tr, Args: upArgs, Hash: hash, State: st, Answers: answers}
	return o
}

func denyText(reason, tool string) string {
	if reason == "" {
		return fmt.Sprintf("The call to %s was refused by policy.", tool)
	}
	return reason
}

func upstreamTool(s *Server, name string) *mcp.Tool {
	for _, t := range s.Catalog.Tools {
		if t.Name == name {
			return t
		}
	}
	return &mcp.Tool{Name: name}
}

// decodeArgs parses arguments keeping numbers exact (big integers survive)
// and rejecting duplicate keys, which different JSON parsers resolve
// differently.
func decodeArgs(raw json.RawMessage) (map[string]any, error) {
	if t := jsonTrim(string(raw)); t == "" || t == "null" {
		if t == "" && len(raw) > 0 {
			return nil, errors.New("arguments: not JSON")
		}
		return map[string]any{}, nil
	}
	if err := noDuplicateKeys(raw); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("arguments: %w", err)
	}
	if jsonTrim(string(raw[dec.InputOffset():])) != "" {
		return nil, errors.New("arguments: unexpected data after the JSON object")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("arguments must be an object")
	}
	return m, nil
}

func noDuplicateKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	type frame struct {
		obj  bool
		keys map[string]bool
		key  bool // next token is a key
	}
	var stack []*frame
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("arguments: %w", err)
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				if top != nil && top.obj {
					top.key = true
				}
				if len(stack) >= MaxArgDepth {
					return fmt.Errorf("arguments: nested deeper than %d levels", MaxArgDepth)
				}
				stack = append(stack, &frame{obj: true, keys: map[string]bool{}, key: true})
				continue
			case '[':
				if top != nil && top.obj {
					top.key = true
				}
				if len(stack) >= MaxArgDepth {
					return fmt.Errorf("arguments: nested deeper than %d levels", MaxArgDepth)
				}
				stack = append(stack, &frame{})
				continue
			case '}', ']':
				stack = stack[:len(stack)-1]
				continue
			}
		case string:
			if top != nil && top.obj && top.key {
				if top.keys[t] {
					return fmt.Errorf("arguments: duplicate key %q", t)
				}
				top.keys[t] = true
				top.key = false
				continue
			}
		}
		if top != nil && top.obj {
			top.key = true
		}
	}
}

// celArgs converts exact JSON numbers for CEL (int64 when integral and in
// range, float64 otherwise); the forwarded arguments keep the originals.
func celArgs(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = celValue(v)
	}
	return out
}

func celValue(v any) any {
	switch t := v.(type) {
	case json.Number:
		return celNumber(t)
	case map[string]any:
		return celArgs(t)
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = celValue(x)
		}
		return out
	}
	return v
}

// ---- elicitation --------------------------------------------------------------------

// continuation validates a presented requestState and splits inputResponses
// into the gateway's answers and the upstream's.
func (e *Engine) continuation(r Request, s *Server, method, target, hash, token string, responses mcp.InputResponseMap) (*State, map[string]any, mcp.InputResponseMap, *RPCError) {
	answers := map[string]any{}
	var upstream mcp.InputResponseMap
	gatewayKeys := false
	for k := range responses {
		if strings.HasPrefix(k, KeyPrefix) {
			gatewayKeys = true
		}
	}
	if token == "" {
		if gatewayKeys {
			return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: "input responses for the gateway require the gateway's request state"}
		}
		if len(responses) > 0 {
			return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: "input responses require a request state"}
		}
		return nil, answers, nil, nil
	}
	var st State
	if isStateKey(token) {
		// A gateway confirmation. Taking it removes it, so it can be answered
		// once, whether or not the answer turns out to be valid.
		var ok bool
		if st, ok = e.states().Take(token); !ok {
			return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: "invalid request state"}
		}
	} else {
		// The signed envelope around an upstream's own state. It never
		// carries a gateway confirmation.
		var err error
		if st, err = e.Signer.Open(token); err != nil || len(st.Pending) > 0 {
			return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: "invalid request state"}
		}
	}
	now := r.Now
	if now.IsZero() {
		now = time.Now()
	}
	switch {
	case now.Unix() > st.Expires:
		return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: "request state expired"}
	case st.Method != method || st.Target != target:
		return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: "request state belongs to another request"}
	case r.principal() == "" || st.Caller != r.principal() || st.Entitlement != entitlementID(s):
		return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: "request state belongs to another caller"}
	case st.Generation != generation(s):
		return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: "request state is from an older policy"}
	case st.ArgsHash != hash:
		return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: "arguments changed since the confirmation was requested"}
	}
	for k, v := range st.Answers {
		answers[k] = v
	}
	pending := map[string]bool{}
	for _, k := range st.Pending {
		pending[k] = true
	}
	upPending := map[string]bool{}
	for _, k := range st.UpPending {
		upPending[k] = true
	}
	for k, v := range responses {
		if strings.HasPrefix(k, KeyPrefix) {
			key := strings.TrimPrefix(k, KeyPrefix)
			if !pending[key] {
				return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("no pending input request %q", k)}
			}
			answer := responseMap(v)
			if !confirmed(answer) {
				return nil, nil, nil, errDeclined
			}
			answers[key] = answer
			continue
		}
		if !upPending[k] {
			return nil, nil, nil, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("no pending input request %q", k)}
		}
		if upstream == nil {
			upstream = mcp.InputResponseMap{}
		}
		upstream[k] = v
	}
	return &st, answers, upstream, nil
}

// errDeclined is returned when the user answered a gateway confirmation
// with anything but yes. The request stops here: it is never forwarded and
// no rule sees the answer.
var errDeclined = &RPCError{Code: CodeInvalidParams, Message: "Declined."}

// confirmed reports a gateway confirmation answered with accept and the
// confirm box ticked. A decline, a cancel, an accept with the box unticked
// and an accept with no content are all a no.
func confirmed(answer map[string]any) bool {
	if answer["action"] != "accept" {
		return false
	}
	content, _ := answer["content"].(map[string]any)
	yes, _ := content["confirm"].(bool)
	return yes
}

// declinedHit records a refused confirmation for the audit trail.
func declinedHit(hook, item string) Hit {
	return Hit{Hook: hook, Rule: "(confirmation)", Item: item, Mode: ModeEnforce, Effect: "deny", Reason: "the user did not confirm"}
}

func responseMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}

// elicitation turns an elicit() marker into an input_required result, or an
// error when the client cannot elicit.
func (e *Engine) elicitation(r Request, s *Server, method, target, hash string, prev *State, answers map[string]any, marker map[string]any) (any, error) {
	key, _ := marker[ElicitKey].(string)
	msg, _ := marker["message"].(string)
	if !r.clientCan("elicitation") {
		if em, _ := marker["error"].(string); em != "" {
			return nil, errors.New(em)
		}
		return nil, fmt.Errorf("This request needs confirmation (%s), which this client does not support.", msg)
	}
	if _, done := answers[key]; done {
		return nil, fmt.Errorf("confirmation %q was already answered; the rule asking for it must check has(input.%s)", key, key)
	}
	if r.principal() == "" {
		return nil, errNoPrincipal
	}
	now := r.Now
	if now.IsZero() {
		now = time.Now()
	}
	st := State{Challenge: fmt.Sprintf("%x", now.UnixNano()), Method: method, Caller: r.principal(), Entitlement: entitlementID(s),
		Target: target, ArgsHash: hash, Generation: generation(s), Expires: now.Add(StateTTL).Unix(), Pending: []string{key}}
	if len(answers) > 0 {
		st.Answers = map[string]map[string]any{}
		for k, v := range answers {
			if m, ok := v.(map[string]any); ok {
				st.Answers[k] = m
			}
		}
	}
	reqs := mcp.InputRequestMap{KeyPrefix + key: &mcp.ElicitParams{
		Mode:    "form",
		Message: msg,
		RequestedSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"confirm": map[string]any{"type": "boolean", "title": "Confirm", "description": msg}},
			"required":   []string{"confirm"},
		},
	}}
	key = newStateKey()
	e.states().Put(key, st, StateTTL)
	return InputRequired{InputRequests: reqs, RequestState: key}, nil
}

// InputRequired is an input_required result (2026-07-28 multi round trip).
type InputRequired struct {
	InputRequests mcp.InputRequestMap
	RequestState  string
}

// MarshalJSON writes the wire shape.
func (ir InputRequired) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"resultType": "input_required", "inputRequests": ir.InputRequests, "requestState": ir.RequestState})
}

// StripMeta returns the catalog with every item's _meta reduced to the
// allowed keys (an empty _meta is removed). The gateway applies it when it
// fetches a catalog, before any rule runs; the original is not modified.
func (c Catalog) StripMeta(allow []string) Catalog {
	ok := map[string]bool{}
	for _, k := range allow {
		ok[k] = true
	}
	strip := func(m mcp.Meta) mcp.Meta {
		var out mcp.Meta
		for k, v := range m {
			if ok[k] {
				if out == nil {
					out = mcp.Meta{}
				}
				out[k] = v
			}
		}
		return out
	}
	out := Catalog{Skills: c.Skills}
	for _, t := range c.Tools {
		cp := *t
		cp.Meta = strip(t.Meta)
		out.Tools = append(out.Tools, &cp)
	}
	for _, p := range c.Prompts {
		cp := *p
		cp.Meta = strip(p.Meta)
		out.Prompts = append(out.Prompts, &cp)
	}
	for _, r := range c.Resources {
		cp := *r
		cp.Meta = strip(r.Meta)
		out.Resources = append(out.Resources, &cp)
	}
	for _, t := range c.Templates {
		cp := *t
		cp.Meta = strip(t.Meta)
		out.Templates = append(out.Templates, &cp)
	}
	return out
}

// entitlementID is the immutable identity state binds to: the entitlement
// id when the gateway supplies one, the slug otherwise.
func entitlementID(s *Server) string {
	if id, ok := s.Entitlement["id"]; ok && id != nil && fmt.Sprint(id) != "" {
		return fmt.Sprint(id)
	}
	return s.Slug
}

// forwardMeta keeps client metadata but drops the protocol-reserved request
// keys: the upstream request carries its own version, identity and
// capabilities, decided by server/connect.
func forwardMeta(in mcp.Meta) mcp.Meta {
	var out mcp.Meta
	for k, v := range in {
		if strings.HasPrefix(k, "io.modelcontextprotocol/") {
			continue
		}
		if out == nil {
			out = mcp.Meta{}
		}
		out[k] = v
	}
	return out
}

// generation is the policy identity state binds to: the gateway's
// generation plus the compiled rules' fingerprint. It is never empty.
func generation(s *Server) string {
	fp := "none"
	if s.Rules != nil && s.Rules.Fingerprint != "" {
		fp = s.Rules.Fingerprint
	}
	return s.Generation + "/" + fp
}

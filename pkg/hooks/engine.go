package hooks

import (
	"context"
	"fmt"
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

// Server is one server as the engine sees it: its slug, its metadata and
// cached catalog, and its compiled rules.
type Server struct {
	// ID is the server's immutable identity, which confirmation state is
	// bound to. When it is empty the slug is used.
	ID         string
	Slug       string
	Info       ServerInfo
	Rules      *Set
	Generation string
	Catalog    Catalog
	// Unavailable marks a server whose catalog could not be fetched; fallback
	// resolution refuses to pick a match while one is unknown.
	Unavailable bool
	// Broken holds the reason the server's rules do not compile. A broken
	// server fails closed: it exposes nothing.
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
	Icons           []mcp.Icon `json:"icons,omitempty"`
	Name            string     `json:"name,omitempty"`
	Version         string     `json:"version,omitempty"`
	Title           string     `json:"title,omitempty"`
	Description     string     `json:"description,omitempty"`
	Instructions    string     `json:"instructions,omitempty"`
	URL             string     `json:"url,omitempty"`
	ProtocolVersion string     `json:"protocolVersion,omitempty"`
	// Extensions are the extensions the server advertises, by name, with
	// the server's settings for each.
	Extensions map[string]any `json:"extensions,omitempty"`
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
	// States holds the state of each request that waits for the user's
	// answer. A gateway with more than one instance must supply one they all
	// share. Nil means MemoryStates, which is right for a single instance
	// only.
	States StateStore
	// Tasks holds the record of each task a client follows through the
	// gateway. A gateway with more than one instance must supply one they
	// all share. Nil means MemoryTasks, which is right for a single
	// instance only.
	Tasks TaskStore

	memOnce  sync.Once
	mem      *MemoryStates
	taskOnce sync.Once
	memTasks *MemoryTasks
}

func (e *Engine) version() string {
	if e.Version == "" {
		return "dev"
	}
	return e.Version
}

// Request is the per-request context: who is calling, from what, on which
// route, and which servers the route exposes.
type Request struct {
	// Route is how the client reached the gateway: "direct" for one server,
	// or "mux" for every server the caller holds. The two are exclusive.
	// Anything but "mux" is the direct route.
	Route string
	// Flags are the switches the client set on this request, such as
	// ?search=true, by name. They are not routes: any of them can be on for
	// either route. Rules read them as flags.<name>; see Flags for the list.
	Flags  map[string]bool
	Caller map[string]any // what the gateway knows of the caller, such as userId, email and type; the gateway defines it
	// Principal identifies whoever is making the request, as one opaque
	// string the gateway chooses: the user under a per-user credential, the
	// key under a shared one. Confirmation state is bound to it, so a
	// challenge issued to one principal cannot be answered by another. When
	// it is empty the engine falls back to Caller["userId"]; with neither,
	// it cannot bind state and refuses to ask for confirmation.
	Principal string
	// Client is the client as it describes itself on this request, in MCP's
	// words: clientInfo (name, version, title) and capabilities.
	Client map[string]any
	// Headers are the request's HTTP headers a gateway lets rules read, by
	// name. A gateway passes only the ones on its allow-list, which starts
	// empty: a header that carries a credential must never be on it.
	Headers map[string]string
	Now     time.Time
	Servers []*Server // direct: exactly one; mux/search: every server on the route (already narrowed by ?slugs=)
}

func (r Request) context(s *Server) Context {
	srv := map[string]any{
		"id": serverID(s), "slug": s.Slug, "name": s.Info.Name, "title": s.Info.Title, "description": s.Info.Description,
		"instructions": s.Info.Instructions, "protocolVersion": s.Info.ProtocolVersion, "url": s.Info.URL,
	}
	for k, v := range srv {
		if v == "" {
			delete(srv, k) // nothing is filled in for what the gateway does not know
		}
	}
	return Context{Server: srv, Caller: r.Caller, Client: r.Client, Headers: r.Headers, Route: r.route(), Flags: r.Flags, Now: r.Now}
}

// route is the route rules see: "direct" or "mux".
func (r Request) route() string {
	if r.mux() {
		return "mux"
	}
	return "direct"
}

// search reports whether the request is in search mode, on either route.
func (r Request) search() bool { return r.Flags[FlagSearch] }

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

// clientMessage is a refusal worded for the person at the client: a full
// sentence the client shows as it is. It travels as an error, and is not
// one of the engine's own failures.
type clientMessage string

func (m clientMessage) Error() string { return string(m) }

func (r Request) mux() bool { return r.Route == "mux" }

func (r Request) server(slug string) *Server {
	for _, s := range r.Servers {
		if s.Slug == slug {
			return s
		}
	}
	return nil
}

func (r Request) clientCan(capability string) bool {
	if capability == "elicitation" && r.Flags[FlagCLI] {
		// A request from the command line client did not come in through
		// MCP: there is no client to show a question to, whatever the
		// request's client map says. So nothing can be asked: a rule that
		// wants confirmation refuses, a server is not told elicitation is
		// available, and a server's own question is refused.
		return false
	}
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
	// Connect is what server.session decided the gateway tells the
	// upstream: client identity, capabilities and whether _meta is forwarded.
	Connect *ConnectInfo `json:"connect,omitempty"`
	// Extensions are the client's opt-ins the server is told of on this
	// request, by extension name, with the client's settings for each.
	Extensions map[string]any `json:"extensions,omitempty"`
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
	// Task carries the task a tasks/get, tasks/update or tasks/cancel
	// forward belongs to, for UpstreamTaskReply. It is set only with
	// Forward.
	Task *TaskCall `json:"-"`
	// Cancel is a tasks/cancel the gateway sends to a server without
	// waiting for its reply, when a task ends at the gateway: a rule denied
	// it, or the server's policy changed.
	Cancel *Forward `json:"cancel,omitempty"`
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
	Bare bool
	// resolve gives rules this call's item: its fields as the client sees
	// them, after every list rule, and `upstream` as the server sent it.
	// It is built once, on the request side, and every reply hook reads the
	// same one, so an item cannot mean one thing in tools/call and another
	// in tools/call.post.result.
	resolve func(name string) (any, bool)
	// view is the server's resource view as the read's pre hook saw it,
	// for the reply to use without working it out again.
	view *resView
	// Params is MCP's part of the request as the client knows it and as the
	// pre rules left it: the exposed name or URI, the arguments under the
	// names the client uses, and _meta. Reply hooks read it as
	// request.params.
	Params    map[string]any
	Args      map[string]any // exactly what was sent upstream; reply hooks read it as request.upstream.params.arguments
	Hash      string
	State     *State
	Confirmed map[string]bool // what the user has confirmed on this request, carried into an upstream round
	// Tasks marks a tools/call the server was told it may answer with a
	// task. Name and ClientArgs are the call as the client made it, which a
	// task keeps so its later polls can be checked against the same tool.
	Tasks      bool
	Name       string
	ClientArgs map[string]any
}

// roots builds the branches a reply hook reads: the request with both sides
// of its params, to which the caller adds what came back.
func (c *CallContext) roots(r Request) map[string]any {
	roots := r.context(c.Server).roots(c.Method, c.Params)
	req := requestOf(roots)
	up := map[string]any{}
	if strings.HasPrefix(c.Method, "resources/") { // a read or a directory read
		up["uri"] = c.Target
	} else {
		up["name"] = c.Target
		up["arguments"] = celArgs(c.Args)
	}
	req["upstream"] = map[string]any{"params": up}
	return roots
}

// callParams is MCP's part of a call or a get: the name, the arguments and,
// when the client sent one, _meta.
func callParams(name string, args any, meta mcp.Meta) map[string]any {
	p := map[string]any{"name": name, "arguments": args}
	if len(meta) > 0 {
		p["_meta"] = map[string]any(meta)
	}
	return p
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

// ctx starts a request: the parent context (cancellation) plus the rule
// budgets. The enforcement budget is accounted time, not a wall-clock
// deadline, so audit work cannot spend it.
func (e *Engine) ctx(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return withMemo(withBudget(parent, e.Limits.budget(), e.Limits.auditBudget())), func() {}
}

func sortedServers(in []*Server) []*Server {
	out := append([]*Server(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
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

// serverID is the immutable identity state binds to: the server's id when
// the gateway supplies one, the slug otherwise.
func serverID(s *Server) string {
	if s.ID != "" {
		return s.ID
	}
	return s.Slug
}

// forwardMeta keeps client metadata but drops the protocol-reserved request
// keys: the upstream request carries its own version, identity and
// capabilities, decided by server.session.
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

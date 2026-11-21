package hooks

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Tasks: a tools/call the server answers with a task handle runs in the
// background, and the client follows it with tasks/get, answers its
// questions with tasks/update, and stops it with tasks/cancel. The gateway
// passes tasks through on the direct route, for a server that offers them
// and a client that asks for them on the call.
//
// The client never sees the server's task id. The gateway keeps a record of
// each task under an id of its own, tied to the principal, route and server
// that started it, and to the server's policy at that moment. A poll that
// does not match all of them is told the task is unknown. A change of policy
// ends the task.
//
// What reaches the client goes through the rules: every status through
// tools/call.post.task, a question through tools/call.post.elicit, and the
// finished result through tools/call.post.result, with the tree of the call
// that started the task.

// ExtTasks is the Tasks extension's capability key.
const ExtTasks = "io.modelcontextprotocol/tasks"

// Extensions are the extensions the gateway passes through on the direct
// route: it advertises each one a server advertises, with the server's
// settings, and passes a client's opt-in on to the server.
var Extensions = []string{ExtTasks, ExtSkills}

// taskIDPrefix marks a task id the gateway issued.
const taskIDPrefix = "t_"

// TaskTTLMax bounds how long the gateway keeps a task's record, whatever
// the server says. TaskTTLDefault is used when the server gives no ttlMs.
const (
	TaskTTLMax     = 24 * time.Hour
	TaskTTLDefault = time.Hour
)

// Task statuses.
const (
	TaskWorking       = "working"
	TaskInputRequired = "input_required"
	TaskCompleted     = "completed"
	TaskFailed        = "failed"
	TaskCancelled     = "cancelled"
)

// TaskRecord is what the gateway keeps of one task. The client holds only
// ID.
type TaskRecord struct {
	ID         string          `json:"id"`
	UpstreamID string          `json:"up"`
	Server     string          `json:"s"` // server id
	Slug       string          `json:"slug"`
	Route      string          `json:"route"`
	Principal  string          `json:"u"`
	Generation string          `json:"g"`
	Name       string          `json:"name"`   // the tool name the client called
	Target     string          `json:"target"` // the server's tool name
	ClientArgs json.RawMessage `json:"args"`   // the arguments under the client's names, after the pre rules
	Args       json.RawMessage `json:"upArgs"` // the arguments the server received
	Meta       json.RawMessage `json:"meta,omitempty"`
	Hash       string          `json:"h"`
	OptIn      json.RawMessage `json:"optIn,omitempty"` // the client's settings for the extension
	Pending    []string        `json:"p,omitempty"`     // input keys the client was last asked
	Expires    int64           `json:"x"`
}

// TaskStore keeps task records between the requests that follow a task.
// A gateway with more than one instance must supply a store they all share.
type TaskStore interface {
	Put(id string, rec TaskRecord, ttl time.Duration)
	Get(id string) (TaskRecord, bool)
	Delete(id string)
}

// MemoryTasks is a TaskStore in this process's memory, for tests and a
// single instance only.
type MemoryTasks struct {
	mu      sync.Mutex
	entries map[string]memoryTask
}

type memoryTask struct {
	rec     TaskRecord
	expires time.Time
}

// Put stores a record for ttl.
func (m *MemoryTasks) Put(id string, rec TaskRecord, ttl time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = map[string]memoryTask{}
	}
	now := time.Now()
	for k, e := range m.entries {
		if now.After(e.expires) {
			delete(m.entries, k)
		}
	}
	m.entries[id] = memoryTask{rec: rec, expires: now.Add(ttl)}
}

// Get returns a record that has not expired.
func (m *MemoryTasks) Get(id string) (TaskRecord, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok || time.Now().After(e.expires) {
		return TaskRecord{}, false
	}
	return e.rec, true
}

// Delete removes a record.
func (m *MemoryTasks) Delete(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, id)
}

// Len reports how many records are held, expired ones included.
func (m *MemoryTasks) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

func (e *Engine) tasks() TaskStore {
	if e.Tasks != nil {
		return e.Tasks
	}
	e.taskOnce.Do(func() { e.memTasks = &MemoryTasks{} })
	return e.memTasks
}

func newTaskID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("no randomness for a task id: " + err.Error())
	}
	return taskIDPrefix + hex.EncodeToString(b[:])
}

// TaskCall is a forwarded tasks/get, tasks/update or tasks/cancel: the
// task's record and the call that started it, rebuilt for the reply hooks.
type TaskCall struct {
	Method string
	Record TaskRecord
	Call   *CallContext
}

// ---- extensions ---------------------------------------------------------------

// clientExtension is the client's settings for an extension on this
// request, when it declared the extension.
func (r Request) clientExtension(name string) (any, bool) {
	caps, _ := r.Client["capabilities"].(map[string]any)
	exts, _ := caps["extensions"].(map[string]any)
	v, ok := exts[name]
	if !ok || v == nil {
		return nil, false
	}
	return v, true
}

// extension is the server's settings for an extension, when the server
// advertises it.
func (s *Server) extension(name string) (any, bool) {
	v, ok := s.Info.Extensions[name]
	if !ok || v == nil {
		return nil, false
	}
	return v, true
}

// extensionOn reports whether the gateway passes an extension through for
// this server on this request, and with what server settings. It is only
// ever on the direct route, for a server that advertises it.
func (e *Engine) extensionOn(ctx context.Context, r Request, s *Server, name string, o *Outcome) (any, bool) {
	if r.mux() {
		return nil, false
	}
	v, ok := s.extension(name)
	if !ok {
		return nil, false
	}
	switch name {
	case ExtTasks:
		if !s.current() || !e.config(ctx, r, s, "tasks", o) || !e.config(ctx, r, s, "tools", o) {
			return nil, false
		}
	case ExtSkills:
		if !e.config(ctx, r, s, "resources", o) {
			return nil, false
		}
	default:
		return nil, false
	}
	return v, true
}

// taskOptIn is the client's tasks settings to pass to the server on a
// tools/call, when the client asked for tasks on this call and the gateway
// passes tasks through for the server.
func (e *Engine) taskOptIn(ctx context.Context, r Request, s *Server, o *Outcome) (any, bool) {
	opt, ok := r.clientExtension(ExtTasks)
	if !ok {
		return nil, false
	}
	if _, on := e.extensionOn(ctx, r, s, ExtTasks, o); !on {
		return nil, false
	}
	return opt, true
}

// ---- the handle ---------------------------------------------------------------

// validTaskID reports whether an id is one a client can be given: at most
// 128 printable ASCII characters and no spaces.
func validTaskID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] <= ' ' || id[i] > '~' {
			return false
		}
	}
	return true
}

func knownStatus(s string) bool {
	switch s {
	case TaskWorking, TaskInputRequired, TaskCompleted, TaskFailed, TaskCancelled:
		return true
	}
	return false
}

// taskFields are the fields of a task's status the client may see, besides
// its id, its questions, its result and its error.
var taskFields = []string{"status", "statusMessage", "createdAt", "lastUpdatedAt", "ttlMs", "pollIntervalMs"}

// taskStarted handles a task handle a server returned for a tools/call it
// was told it may answer with one: it records the task under an id of the
// gateway's own and gives the client the handle, after tools/call.post.task.
func (e *Engine) taskStarted(ctx context.Context, r Request, call *CallContext, result map[string]any) Outcome {
	s, tr := call.Server, call.Transform
	var o Outcome
	upID, _ := result["taskId"].(string)
	status, _ := result["status"].(string)
	if !validTaskID(upID) || !knownStatus(status) {
		o.Issues = append(o.Issues, s.Slug+": "+tr.Upstream+" returned a task handle the gateway cannot read")
		o.Result = toolError("The server started a background task, but its handle could not be read.")
		if validTaskID(upID) {
			o.Cancel = cancelForward(s, upID)
		}
		return e.finishOutcome(ctx, o)
	}
	if r.principal() == "" {
		o.Result = toolError("The server started a background task, which is not available without a caller identity.")
		o.Cancel = cancelForward(s, upID)
		return e.finishOutcome(ctx, o)
	}
	task, hits, reason, denied := e.taskHook(ctx, r, call, result)
	o.Hits = append(o.Hits, hits...)
	if denied {
		o.Result = toolError(denyOr(reason, toolRefused(tr.Exposed)))
		o.Cancel = cancelForward(s, upID)
		return e.finishOutcome(ctx, o)
	}
	now := r.Now
	if now.IsZero() {
		now = timeNow()
	}
	ttl := taskTTL(result["ttlMs"])
	rec := TaskRecord{
		ID: newTaskID(), UpstreamID: upID, Server: serverID(s), Slug: s.Slug, Route: r.route(), Principal: r.principal(),
		Generation: generation(s), Name: call.Name, Target: tr.Upstream, Hash: call.Hash, Expires: now.Add(ttl).Unix(),
	}
	rec.ClientArgs, _ = json.Marshal(call.ClientArgs)
	rec.Args, _ = json.Marshal(call.Args)
	if m, ok := call.Params["_meta"]; ok {
		rec.Meta, _ = json.Marshal(m)
	}
	if opt, ok := r.clientExtension(ExtTasks); ok {
		rec.OptIn, _ = json.Marshal(opt)
	}
	e.tasks().Put(rec.ID, rec, ttl)
	task["taskId"] = rec.ID
	task["resultType"] = "task"
	o.Result = task
	return e.finishOutcome(ctx, o)
}

// taskTTL is how long to keep a task's record: what the server said, within
// TaskTTLMax, or TaskTTLDefault when it said nothing.
func taskTTL(v any) time.Duration {
	var ms float64
	switch t := v.(type) {
	case json.Number:
		ms, _ = t.Float64()
	case float64:
		ms = t
	case int64:
		ms = float64(t)
	default:
		return TaskTTLDefault
	}
	d := time.Duration(ms) * time.Millisecond
	if d <= 0 {
		return TaskTTLDefault
	}
	if d > TaskTTLMax {
		return TaskTTLMax
	}
	return d
}

// taskHook runs tools/call.post.task on a task's status. It returns the
// status as the client is to see it: the fields of taskFields the server
// sent, after the rules.
func (e *Engine) taskHook(ctx context.Context, r Request, call *CallContext, result map[string]any) (map[string]any, []Hit, string, bool) {
	said := map[string]any{}
	for _, k := range taskFields {
		if v, ok := result[k]; ok && v != nil {
			said[k] = v
		}
	}
	view := NewView(copyMap(said))
	roots := call.roots(r)
	upstream := celMap(said)
	task := func() map[string]any {
		t := celMap(view.Materialize())
		t["upstream"] = upstream
		return t
	}
	roots["task"] = task()
	ro := call.Server.Rules.run(ctx, "tools/call.post.task", call.Exposed, replyResolve(call), roots, func(key string, val any) error {
		view.Set([]string{key}, val)
		roots["task"] = task()
		return nil
	})
	if ro.denied {
		return nil, ro.hits, ro.reason, true
	}
	out := view.Materialize()
	if v, ok := out["statusMessage"]; ok && v == nil {
		delete(out, "statusMessage") // a rule set it to null: no message
	}
	return out, ro.hits, "", false
}

// cancelForward is the tasks/cancel the gateway sends when it ends a task.
func cancelForward(s *Server, upID string) *Forward {
	return &Forward{Slug: s.Slug, Method: "tasks/cancel", Params: map[string]any{"taskId": upID},
		Extensions: map[string]any{ExtTasks: map[string]any{}}}
}

// ---- following a task ---------------------------------------------------------

// unknownTask is what a client is told about a task id it may not follow.
// Clients read -32602 as an unknown task.
func unknownTask() *RPCError {
	return &RPCError{Code: CodeInvalidParams, Message: "unknown task"}
}

// taskFor finds the record of a task this request may follow, and the call
// that started it. A record that is unknown, expired, or belongs to another
// principal, route or server is unknown. A record whose server's policy has
// changed ends the task: the outcome then holds the client's answer.
func (e *Engine) taskFor(ctx context.Context, r Request, id string, o *Outcome) (*TaskCall, bool) {
	if !strings.HasPrefix(id, taskIDPrefix) || r.principal() == "" {
		o.Error = unknownTask()
		return nil, false
	}
	rec, ok := e.tasks().Get(id)
	now := r.Now
	if now.IsZero() {
		now = timeNow()
	}
	if !ok || rec.Principal != r.principal() || rec.Route != r.route() || now.Unix() > rec.Expires {
		o.Error = unknownTask()
		return nil, false
	}
	var s *Server
	for _, c := range r.Servers {
		if serverID(c) == rec.Server {
			s = c
		}
	}
	if s == nil {
		o.Error = unknownTask()
		return nil, false
	}
	call, why := e.taskCall(ctx, r, s, rec, o)
	if call == nil {
		e.tasks().Delete(rec.ID)
		o.Issues = append(o.Issues, s.Slug+": task "+rec.ID+" ended: "+why)
		o.Cancel = cancelForward(s, rec.UpstreamID)
		return nil, false
	}
	return &TaskCall{Record: rec, Call: call}, true
}

// policyEnded is the error a task ends with when the gateway ends it.
func policyEnded() map[string]any {
	return map[string]any{"code": -32603, "message": "The task was ended because the server's policy changed."}
}

// taskCall rebuilds the call that started a task, so its replies see the
// same tree. It returns nil, and why, when the server's policy is no longer
// the one the task started under, or the tool is no longer there for this
// caller.
func (e *Engine) taskCall(ctx context.Context, r Request, s *Server, rec TaskRecord, o *Outcome) (*CallContext, string) {
	if generation(s) != rec.Generation {
		o.Result = map[string]any{"resultType": "complete", "taskId": rec.ID, "status": TaskFailed, "error": policyEnded()}
		return nil, "the server's policy changed"
	}
	rs, et, hits, err := e.resolveTool(ctx, r, rec.Name)
	o.Hits = append(o.Hits, hits...)
	if err != nil || rs != s || et.Transform.Upstream != rec.Target {
		o.Result = map[string]any{"resultType": "complete", "taskId": rec.ID, "status": TaskFailed, "error": policyEnded()}
		return nil, "its tool is no longer available"
	}
	clientArgs, err1 := decodeArgs(rec.ClientArgs)
	args, err2 := decodeArgs(rec.Args)
	if err1 != nil || err2 != nil {
		o.Result = map[string]any{"resultType": "complete", "taskId": rec.ID, "status": TaskFailed, "error": policyEnded()}
		return nil, "its record could not be read"
	}
	var meta map[string]any
	if len(rec.Meta) > 0 {
		_ = json.Unmarshal(rec.Meta, &meta)
	}
	params := map[string]any{"name": et.Transform.Exposed, "arguments": celArgs(clientArgs)}
	if len(meta) > 0 {
		params["_meta"] = meta
	}
	return &CallContext{Server: s, Method: "tools/call", Target: rec.Target, Exposed: et.Transform.Exposed, Transform: et.Transform,
		Params: params, Args: args, Hash: rec.Hash, resolve: toolResolver(s, et), Tasks: true, Name: rec.Name, ClientArgs: clientArgs}, ""
}

// taskForward is a tasks/* request to the task's server.
func (e *Engine) taskForward(ctx context.Context, r Request, tc *TaskCall, method string, params map[string]any, o *Outcome) {
	session, hits := e.Connect(ctx, r, tc.Call.Server, e.version())
	o.Hits = append(o.Hits, hits...)
	var opt any = map[string]any{}
	if len(tc.Record.OptIn) > 0 {
		_ = json.Unmarshal(tc.Record.OptIn, &opt)
	}
	params["taskId"] = tc.Record.UpstreamID
	tc.Method = method
	o.Forward = &Forward{Slug: tc.Call.Server.Slug, Method: method, Params: params, Connect: &session, Extensions: map[string]any{ExtTasks: opt}}
	o.Task = tc
}

// GetTask answers a client's tasks/get: the forward to the task's server,
// or the client's answer when the task is not one it may follow.
func (e *Engine) GetTask(parent context.Context, r Request, id string) (ret Outcome) {
	defer e.recovered(&ret, "tasks/get", callRef{method: "tasks/get", target: id})
	ctx, cancel := e.ctx(parent)
	defer cancel()
	var o Outcome
	tc, ok := e.taskFor(ctx, r, id, &o)
	if !ok {
		return e.finishOutcome(ctx, o)
	}
	e.taskForward(ctx, r, tc, "tasks/get", map[string]any{}, &o)
	return o
}

// UpdateTask answers a client's tasks/update: its answers go on to the
// server, for the questions the client was last asked and no others.
func (e *Engine) UpdateTask(parent context.Context, r Request, id string, responses map[string]json.RawMessage) (ret Outcome) {
	defer e.recovered(&ret, "tasks/update", callRef{method: "tasks/update", target: id})
	ctx, cancel := e.ctx(parent)
	defer cancel()
	var o Outcome
	tc, ok := e.taskFor(ctx, r, id, &o)
	if !ok {
		return e.finishOutcome(ctx, o)
	}
	asked := map[string]bool{}
	for _, k := range tc.Record.Pending {
		asked[k] = true
	}
	answers := map[string]any{}
	for _, k := range sortedKeys(responses) {
		if !asked[k] {
			o.Error = &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("the task did not ask %q", k)}
			return e.finishOutcome(ctx, o)
		}
		answers[k] = json.RawMessage(bytes.Clone(responses[k]))
	}
	if len(answers) == 0 {
		o.Error = &RPCError{Code: CodeInvalidParams, Message: "inputResponses is required"}
		return e.finishOutcome(ctx, o)
	}
	e.taskForward(ctx, r, tc, "tasks/update", map[string]any{"inputResponses": answers}, &o)
	return o
}

// CancelTask answers a client's tasks/cancel.
func (e *Engine) CancelTask(parent context.Context, r Request, id string) (ret Outcome) {
	defer e.recovered(&ret, "tasks/cancel", callRef{method: "tasks/cancel", target: id})
	ctx, cancel := e.ctx(parent)
	defer cancel()
	var o Outcome
	tc, ok := e.taskFor(ctx, r, id, &o)
	if !ok {
		return e.finishOutcome(ctx, o)
	}
	e.taskForward(ctx, r, tc, "tasks/cancel", map[string]any{}, &o)
	return o
}

// UpstreamTaskReply handles what a server answered to a forwarded tasks/*
// request. tc is the TaskCall the engine returned with the forward.
func (e *Engine) UpstreamTaskReply(parent context.Context, r Request, tc *TaskCall, raw json.RawMessage) (ret Outcome) {
	defer e.recovered(&ret, "tasks reply", callRef{method: "tasks reply"})
	ctx, cancel := e.ctx(parent)
	defer cancel()
	if tc == nil || tc.Call == nil {
		return errOutcome(-32603, "no forwarded task request to reply to")
	}
	if len(raw) > e.Limits.maxBytes() {
		return errOutcome(CodeTooLarge, "upstream result exceeds gateway size limit")
	}
	result, ok := decodeExact(raw).(map[string]any)
	if !ok {
		return errOutcome(-32603, "upstream result is not a JSON object")
	}
	rec, call, s := tc.Record, tc.Call, tc.Call.Server
	var o Outcome
	if tc.Method != "tasks/get" {
		if tc.Method == "tasks/update" {
			rec.Pending = nil
			e.tasks().Put(rec.ID, rec, recordTTL(r, rec))
		}
		o.Result = map[string]any{"resultType": "complete"}
		return e.finishOutcome(ctx, o)
	}
	if id, _ := result["taskId"].(string); id != "" && id != rec.UpstreamID {
		return errOutcome(-32603, "the server answered for another task")
	}
	status, _ := result["status"].(string)
	end := func(reason string) Outcome {
		e.tasks().Delete(rec.ID)
		o.Cancel = cancelForward(s, rec.UpstreamID)
		o.Result = map[string]any{"resultType": "complete", "taskId": rec.ID, "status": TaskFailed,
			"error": map[string]any{"code": -32603, "message": reason}}
		return e.finishOutcome(ctx, o)
	}
	switch status {
	case TaskWorking, TaskInputRequired:
		task, hits, reason, denied := e.taskHook(ctx, r, call, result)
		o.Hits = append(o.Hits, hits...)
		if denied {
			return end(denyOr(reason, toolRefused(call.Exposed)))
		}
		if status == TaskInputRequired {
			if msg, ok := inputAllowed(r, s); !ok {
				o.Issues = append(o.Issues, s.Slug+": task "+rec.ID+" asked for input and was ended: "+msg)
				return end(msg)
			}
			var seen unknowns
			inputUnknowns(&seen, s.Slug, call.Target, result)
			o.Unknown = append(o.Unknown, seen.list()...)
			reqs, hits, err := e.elicitHook(ctx, r, call, "tools/call.post.elicit", call.Exposed, replyResolve(call), rawRequests(result))
			o.Hits = append(o.Hits, hits...)
			if err != nil {
				return end(err.Error())
			}
			rec.Pending = rec.Pending[:0]
			for k := range reqs {
				rec.Pending = append(rec.Pending, k)
			}
			sort.Strings(rec.Pending)
			e.tasks().Put(rec.ID, rec, recordTTL(r, rec))
			task["inputRequests"] = reqs
		}
		task["taskId"] = rec.ID
		task["resultType"] = "complete"
		o.Result = task
		return e.finishOutcome(ctx, o)
	case TaskCompleted:
		res, ok := result["result"].(map[string]any)
		if !ok {
			return end("The task finished without a result the gateway can read.")
		}
		var seen unknowns
		contentUnknowns(&seen, s.Slug, call.Target, res["content"])
		o.Unknown = append(o.Unknown, seen.list()...)
		ro := e.toolResult(ctx, r, call, res)
		o.Hits = append(o.Hits, ro.Hits...)
		o.Result = map[string]any{"resultType": "complete", "taskId": rec.ID, "status": TaskCompleted, "result": ro.Result}
		copyTaskFields(o.Result.(map[string]any), result, "statusMessage", "createdAt", "lastUpdatedAt", "ttlMs", "pollIntervalMs")
		return e.finishOutcome(ctx, o)
	case TaskFailed, TaskCancelled:
		// The server's own error passes as it is: no rule sees an error.
		out := map[string]any{"resultType": "complete", "taskId": rec.ID, "status": status}
		copyTaskFields(out, result, "error", "statusMessage", "createdAt", "lastUpdatedAt", "ttlMs", "pollIntervalMs")
		o.Result = out
		return e.finishOutcome(ctx, o)
	}
	var seen unknowns
	seen.add(UnknownResultType, "task status "+status, s.Slug, call.Target, "refused", result)
	o.Unknown = append(o.Unknown, seen.list()...)
	return end("The server reported a task status the gateway does not know.")
}

func copyTaskFields(dst, src map[string]any, keys ...string) {
	for _, k := range keys {
		if v, ok := src[k]; ok && v != nil {
			dst[k] = v
		}
	}
}

// recordTTL is how much longer a record has to live.
func recordTTL(r Request, rec TaskRecord) time.Duration {
	now := r.Now
	if now.IsZero() {
		now = timeNow()
	}
	d := time.Unix(rec.Expires, 0).Sub(now)
	if d <= 0 {
		return time.Second
	}
	return d
}

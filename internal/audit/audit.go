// Package audit records every inbound and upstream HTTP exchange with
// redacted headers and JSON-RPC bodies, Sandbox-style.
package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/store"
)

// MaxBody caps how much of a body is persisted.
const MaxBody = 256 << 10

// Writer is the slice of the store the recorder needs.
type Writer interface {
	InsertAuditEvent(ctx context.Context, e *store.AuditEvent, payload *store.AuditPayload) error
}

// Recorder writes events asynchronously so request latency is unaffected.
type Recorder struct {
	w     Writer
	log   *slog.Logger
	queue chan job
}

type job struct {
	event   *store.AuditEvent
	payload *store.AuditPayload
}

// NewRecorder starts the background writer.
func NewRecorder(ctx context.Context, w Writer, log *slog.Logger) *Recorder {
	r := &Recorder{w: w, log: log, queue: make(chan job, 1024)}
	go r.run(ctx)
	return r
}

func (r *Recorder) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-r.queue:
			wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := r.w.InsertAuditEvent(wctx, j.event, j.payload); err != nil {
				r.log.Error("audit write failed", "err", err, "requestId", j.event.RequestID)
			}
			cancel()
		}
	}
}

// Record enqueues an event; it never blocks the caller for long.
func (r *Recorder) Record(e *store.AuditEvent, p *store.AuditPayload) {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	select {
	case r.queue <- job{e, p}:
	default:
		r.log.Warn("audit queue full, dropping event", "requestId", e.RequestID)
	}
}

// ---- context plumbing -----------------------------------------------------

type ctxKey int

const (
	keyRequestID ctxKey = iota
	keyInboundID
	keyTarget
	keyIssues
)

// issues collects per-request problems that did not fail the exchange
// (a server skipped by the mux). Shared by pointer across goroutines.
type issues struct {
	mu   sync.Mutex
	list []string
}

func withIssues(ctx context.Context) context.Context {
	return context.WithValue(ctx, keyIssues, &issues{})
}

// AddIssue notes a non-fatal problem on the inbound exchange; the inbound
// event's outcome becomes partial.
func AddIssue(ctx context.Context, msg string) {
	if is, ok := ctx.Value(keyIssues).(*issues); ok {
		is.mu.Lock()
		is.list = append(is.list, msg)
		is.mu.Unlock()
	}
}

// Issues returns the problems noted so far.
func Issues(ctx context.Context) []string {
	is, ok := ctx.Value(keyIssues).(*issues)
	if !ok {
		return nil
	}
	is.mu.Lock()
	defer is.mu.Unlock()
	return append([]string(nil), is.list...)
}

// Target names the downstream server an exchange is for.
type Target struct {
	ServerID uuid.UUID
	Slug     string
	Route    string // mux | direct | api
}

// WithRequestID tags the context with the inbound correlation id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyRequestID, id)
}

// RequestID returns the correlation id, or "".
func RequestID(ctx context.Context) string {
	s, _ := ctx.Value(keyRequestID).(string)
	return s
}

func withInboundID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, keyInboundID, id)
}

func inboundID(ctx context.Context) *uuid.UUID {
	id, ok := ctx.Value(keyInboundID).(uuid.UUID)
	if !ok {
		return nil
	}
	return &id
}

// Inherit copies the request id and inbound event id from src onto dst.
func Inherit(src, dst context.Context) context.Context {
	if id := RequestID(src); id != "" {
		dst = WithRequestID(dst, id)
	}
	if id := inboundID(src); id != nil {
		dst = withInboundID(dst, *id)
	}
	if is, ok := src.Value(keyIssues).(*issues); ok {
		dst = context.WithValue(dst, keyIssues, is)
	}
	return dst
}

// WithTarget records which server an exchange is for. The value is held by
// pointer so a handler that resolves the server after the inbound middleware
// ran (the mux route) can fill it in with SetTarget.
func WithTarget(ctx context.Context, t Target) context.Context {
	if cur, ok := ctx.Value(keyTarget).(*Target); ok {
		// Already placed by an outer middleware: update in place so that
		// middleware sees the route and the slug set later.
		if t.Route != "" {
			cur.Route = t.Route
		}
		if t.ServerID != uuid.Nil {
			cur.ServerID, cur.Slug = t.ServerID, t.Slug
		}
		return ctx
	}
	return context.WithValue(ctx, keyTarget, &t)
}

// SetTarget fills in the server on an existing target, if one is on the context.
func SetTarget(ctx context.Context, serverID uuid.UUID, slug string) {
	if t, ok := ctx.Value(keyTarget).(*Target); ok {
		t.ServerID, t.Slug = serverID, slug
	}
}

// TargetFrom returns a copy of the target, if any.
func TargetFrom(ctx context.Context) (Target, bool) {
	t, ok := ctx.Value(keyTarget).(*Target)
	if !ok {
		return Target{}, false
	}
	return *t, true
}

// ---- redaction ------------------------------------------------------------

var sensitive = map[string]bool{
	"authorization": true, "proxy-authorization": true, "cookie": true, "set-cookie": true,
	"x-api-key": true, "api-key": true, "x-auth-token": true,
}

// Redact flattens headers, masking anything credential-like.
func Redact(h http.Header, extra ...string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		lk := strings.ToLower(k)
		masked := sensitive[lk]
		for _, e := range extra {
			if e != "" && strings.EqualFold(e, k) {
				masked = true
			}
		}
		if masked {
			out[k] = "[redacted]"
			continue
		}
		out[k] = strings.Join(v, ", ")
	}
	return out
}

// ---- JSON-RPC sniffing --------------------------------------------------------

// RPCInfo is what we can learn from a JSON-RPC request body.
type RPCInfo struct {
	Method          string
	ID              string
	ToolName        string
	ProtocolVersion string
	IsNotification  bool
}

// Sniff extracts method/id/tool from a JSON-RPC request (single or batch;
// batch reports the first message).
func Sniff(body []byte) RPCInfo {
	var info RPCInfo
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return info
	}
	if body[0] == '[' {
		var arr []json.RawMessage
		if json.Unmarshal(body, &arr) == nil && len(arr) > 0 {
			body = arr[0]
		}
	}
	var msg struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
		Params struct {
			Name string `json:"name"`
			Meta struct {
				PV string `json:"io.modelcontextprotocol/protocolVersion"`
			} `json:"_meta"`
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"params"`
	}
	if json.Unmarshal(body, &msg) != nil {
		return info
	}
	info.Method = msg.Method
	info.IsNotification = len(msg.ID) == 0 || string(msg.ID) == "null"
	info.ID = strings.Trim(string(msg.ID), `"`)
	if msg.Method == "tools/call" {
		info.ToolName = msg.Params.Name
	}
	info.ProtocolVersion = msg.Params.Meta.PV
	if info.ProtocolVersion == "" {
		info.ProtocolVersion = msg.Params.ProtocolVersion
	}
	return info
}

// ---- inbound middleware -----------------------------------------------------

type captureWriter struct {
	http.ResponseWriter
	status int
	n      int64
	buf    bytes.Buffer
}

func (c *captureWriter) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if room := MaxBody - c.buf.Len(); room > 0 {
		c.buf.Write(b[:min(room, len(b))])
	}
	c.n += int64(len(b))
	return c.ResponseWriter.Write(b)
}

func (c *captureWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *captureWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// Inbound wraps an MCP or API handler, recording the exchange. route labels
// the entry point (mux, direct, api). The slug is read from the Target on
// the context when a later middleware set one.
func (r *Recorder) Inbound(route string, withPayload bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			start := time.Now()
			reqID := RequestID(req.Context())
			if reqID == "" {
				reqID = uuid.NewString()
			}
			eventID := uuid.New()
			ctx := withIssues(withInboundID(WithRequestID(auth.WithCallerSlot(req.Context()), reqID), eventID))
			ctx = WithTarget(ctx, Target{Route: route})
			req = req.WithContext(ctx)

			var reqBody []byte
			if req.Body != nil {
				reqBody, _ = io.ReadAll(io.LimitReader(req.Body, 8<<20))
				req.Body.Close()
				req.Body = io.NopCloser(bytes.NewReader(reqBody))
			}
			cw := &captureWriter{ResponseWriter: w}
			next.ServeHTTP(cw, req)

			caller := auth.CallerFrom(req.Context())
			info := Sniff(reqBody)
			if info.ProtocolVersion == "" {
				info.ProtocolVersion = req.Header.Get("Mcp-Protocol-Version")
			}
			e := &store.AuditEvent{
				ID: eventID, CreatedAt: start, Hop: store.HopInbound, Route: route,
				HTTPMethod: req.Method, Path: req.URL.Path, McpMethod: info.Method, ToolName: info.ToolName,
				JSONRPCID: info.ID, ProtocolVersion: info.ProtocolVersion, StatusCode: cw.status,
				RequestBytes: int64(len(reqBody)), ResponseBytes: cw.n, DurationMS: time.Since(start).Milliseconds(),
				RequestID: reqID, ActorType: "anonymous",
			}
			if e.StatusCode == 0 {
				e.StatusCode = http.StatusOK
			}
			if caller != nil {
				e.OrgID = caller.OrgID
				e.ActorType = string(caller.Type)
				e.ActorSubject = caller.Subject
				e.ActorUserID = caller.UserID
				e.ActorUserID = caller.UserID
				e.APIKeyID = caller.APIKeyID
				e.APIKeyPrefix = caller.APIKeyPrefix
			}
			if t, ok := TargetFrom(req.Context()); ok {
				id := t.ServerID
				if id != uuid.Nil {
					e.ServerID = &id
				}
				e.Slug = t.Slug
			}
			e.Outcome = outcomeFor(e.StatusCode, cw.buf.Bytes())
			if e.Outcome == store.OutcomeSuccess && len(Issues(req.Context())) > 0 {
				e.Outcome = store.OutcomePartial
				e.ErrorType = "upstream_partial"
			}
			if e.Outcome == store.OutcomeError {
				e.ErrorType = "http_" + http.StatusText(e.StatusCode)
				if e.StatusCode < 400 {
					e.ErrorType = "jsonrpc_error"
				}
			}
			if e.OrgID == "" {
				// Unauthenticated hits have no tenant; keep them under a sentinel so
				// denied attempts remain visible to operators.
				e.OrgID = "-"
			}
			var payload *store.AuditPayload
			if withPayload {
				payload = &store.AuditPayload{
					RequestHeaders: Redact(req.Header), RequestBody: clip(reqBody),
					ResponseHeaders: Redact(cw.Header()), ResponseBody: cw.buf.String(),
				}
			}
			r.Record(e, payload)
		})
	}
}

func outcomeFor(status int, body []byte) store.Outcome {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return store.OutcomeDenied
	case status >= 400:
		return store.OutcomeError
	}
	// A 200 carrying a JSON-RPC error (JSON or SSE framed) is still an error.
	if bytes.Contains(body, []byte(`"error":{`)) && !bytes.Contains(body, []byte(`"result":`)) {
		return store.OutcomeError
	}
	return store.OutcomeSuccess
}

func clip(b []byte) string {
	if len(b) > MaxBody {
		return string(b[:MaxBody]) + "…"
	}
	return string(b)
}

// ---- upstream transport -------------------------------------------------------

// RecordSkipped writes an upstream-hop event for a server the gateway could
// not even contact (credential pending or broken, connect failure), so a
// fan-out that silently dropped a server still leaves a trace.
func (r *Recorder) RecordSkipped(ctx context.Context, srv *store.Server, route, method, errType, msg string) {
	e := &store.AuditEvent{
		CreatedAt: time.Now(), Hop: store.HopUpstream, ParentID: inboundID(ctx), Route: route,
		HTTPMethod: http.MethodPost, Path: srv.URL, McpMethod: method, StatusCode: 0,
		Outcome: store.OutcomeError, ErrorType: errType, RequestID: RequestID(ctx),
		ActorType: string(auth.ActorSystem), Slug: srv.Slug, OrgID: srv.OrgID,
	}
	id := srv.ID
	e.ServerID = &id
	if caller := auth.CallerFrom(ctx); caller != nil {
		e.ActorType = string(caller.Type)
		e.ActorSubject = caller.Subject
		e.ActorUserID = caller.UserID
		e.APIKeyID = caller.APIKeyID
		e.APIKeyPrefix = caller.APIKeyPrefix
	}
	r.Record(e, &store.AuditPayload{ResponseBody: msg})
}

// Transport returns a RoundTripper that records each upstream exchange,
// linking it to the inbound event on the context. redactHeader names a
// credential header to mask in addition to the standard set.
func (r *Recorder) Transport(next http.RoundTripper, redactHeader string) http.RoundTripper {
	return &auditTransport{rec: r, next: next, redact: redactHeader}
}

type auditTransport struct {
	rec    *Recorder
	next   http.RoundTripper
	redact string
}

func (t *auditTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	ctx := req.Context()
	var reqBody []byte
	if req.Body != nil && req.Body != http.NoBody {
		reqBody, _ = io.ReadAll(io.LimitReader(req.Body, 8<<20))
		req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
		if req.GetBody == nil {
			req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(reqBody)), nil }
		}
	}
	caller := auth.CallerFrom(ctx)
	target, _ := TargetFrom(ctx)
	info := Sniff(reqBody)
	if info.ProtocolVersion == "" {
		info.ProtocolVersion = req.Header.Get("Mcp-Protocol-Version")
	}
	e := &store.AuditEvent{
		CreatedAt: start, Hop: store.HopUpstream, ParentID: inboundID(ctx), Route: target.Route,
		HTTPMethod: req.Method, Path: req.URL.String(), McpMethod: info.Method, ToolName: info.ToolName,
		JSONRPCID: info.ID, ProtocolVersion: info.ProtocolVersion, RequestBytes: int64(len(reqBody)),
		RequestID: RequestID(ctx), ActorType: string(auth.ActorSystem), Slug: target.Slug, OrgID: "-",
	}
	if target.ServerID != uuid.Nil {
		id := target.ServerID
		e.ServerID = &id
	}
	if caller != nil {
		e.OrgID = caller.OrgID
		e.ActorType = string(caller.Type)
		e.ActorSubject = caller.Subject
		e.ActorUserID = caller.UserID
		e.APIKeyID = caller.APIKeyID
		e.APIKeyPrefix = caller.APIKeyPrefix
	}
	if e.Route == "" {
		e.Route = "api"
	}
	payload := &store.AuditPayload{RequestHeaders: Redact(req.Header, t.redact), RequestBody: clip(reqBody)}

	resp, err := t.next.RoundTrip(req)
	if err != nil {
		e.StatusCode = 0
		e.Outcome = store.OutcomeError
		e.ErrorType = "transport"
		e.DurationMS = time.Since(start).Milliseconds()
		payload.ResponseBody = err.Error()
		t.rec.Record(e, payload)
		return nil, err
	}
	e.StatusCode = resp.StatusCode
	payload.ResponseHeaders = Redact(resp.Header, t.redact)
	// Bodies may stream (SSE); record once the consumer finishes with it.
	resp.Body = &tailBody{ReadCloser: resp.Body, done: func(body []byte, n int64) {
		e.ResponseBytes = n
		e.DurationMS = time.Since(start).Milliseconds()
		e.Outcome = outcomeFor(resp.StatusCode, body)
		if e.Outcome != store.OutcomeSuccess {
			e.ErrorType = "http_" + http.StatusText(resp.StatusCode)
			if resp.StatusCode < 400 {
				e.ErrorType = "jsonrpc_error"
			}
		}
		payload.ResponseBody = string(body)
		t.rec.Record(e, payload)
	}}
	return resp, nil
}

type tailBody struct {
	io.ReadCloser
	buf  bytes.Buffer
	n    int64
	done func([]byte, int64)
	once bool
}

func (b *tailBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.n += int64(n)
		if room := MaxBody - b.buf.Len(); room > 0 {
			b.buf.Write(p[:min(room, n)])
		}
	}
	if err == io.EOF {
		b.finish()
	}
	return n, err
}

func (b *tailBody) Close() error {
	b.finish()
	return b.ReadCloser.Close()
}

func (b *tailBody) finish() {
	if b.once {
		return
	}
	b.once = true
	b.done(b.buf.Bytes(), b.n)
}

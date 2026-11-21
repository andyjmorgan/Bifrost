package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"github.com/andyjmorgan/Bifrost/internal/store"
)

// Capture keeps the raw result a server sent for one method's request on a
// session. The client library decodes a result into its own types and
// drops what they have no field for, such as the id and status of a task
// handle. A caller that needs those reads the result here.
type Capture struct {
	Method string // the method whose result to keep, such as tools/call
	// Limit bounds how many bytes of the response are kept.
	Limit int

	mu   sync.Mutex
	id   json.RawMessage
	body *bytes.Buffer
}

// Result is the result the server sent, as it sent it, or nil when there
// was none.
func (c *Capture) Result() json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.body == nil {
		return nil
	}
	for _, msg := range messages(c.body.Bytes()) {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(msg, &m) == nil && len(m.Result) > 0 && bytes.Equal(bytes.TrimSpace(m.ID), bytes.TrimSpace(c.id)) {
			return m.Result
		}
	}
	return nil
}

// messages are the JSON-RPC messages in a response body: the body itself
// when it is one JSON value, or the data of each SSE event.
func messages(body []byte) [][]byte {
	t := bytes.TrimSpace(body)
	if len(t) > 0 && t[0] == '{' && json.Valid(t) {
		return [][]byte{t}
	}
	var out [][]byte
	var data []byte
	flush := func() {
		if len(data) > 0 {
			out = append(out, data)
		}
		data = nil
	}
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		switch {
		case len(line) == 0:
			flush()
		case bytes.HasPrefix(line, []byte("data:")):
			d := bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" "))
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, d...)
		}
	}
	flush()
	return out
}

// captureTransport tees the response to the captured method's request.
type captureTransport struct {
	next http.RoundTripper
	c    *Capture
}

func (t captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var id json.RawMessage
	if req.Method == http.MethodPost && req.GetBody != nil {
		if b, err := req.GetBody(); err == nil {
			var m struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.NewDecoder(io.LimitReader(b, 1<<20)).Decode(&m) == nil && m.Method == t.c.Method {
				id = m.ID
			}
			b.Close()
		}
	}
	resp, err := t.next.RoundTrip(req)
	if err != nil || id == nil {
		return resp, err
	}
	t.c.mu.Lock()
	t.c.id, t.c.body = id, &bytes.Buffer{}
	buf := t.c.body
	t.c.mu.Unlock()
	limit := t.c.Limit
	if limit <= 0 {
		limit = 10 << 20
	}
	resp.Body = &teeBody{r: resp.Body, w: buf, mu: &t.c.mu, left: limit}
	return resp, nil
}

type teeBody struct {
	r    io.ReadCloser
	w    *bytes.Buffer
	mu   *sync.Mutex
	left int
}

func (b *teeBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 && b.left > 0 {
		k := n
		if k > b.left {
			k = b.left
		}
		b.mu.Lock()
		b.w.Write(p[:k])
		b.mu.Unlock()
		b.left -= k
	}
	return n, err
}

func (b *teeBody) Close() error { return b.r.Close() }

// ErrNoReply is returned by Call when the server's response held no reply
// to the request.
var ErrNoReply = errors.New("upstream: the response held no reply to the request")

// Call sends one request to a server on its own, without a session, as the
// stateless protocol (2026-07-28) allows, and returns the server's result or
// its JSON-RPC error. params must carry the request's _meta. name is the
// Mcp-Name header, when the method has one. It is for methods the client
// library has no call for, such as the tasks extension's.
func (c *Connector) Call(ctx context.Context, srv *store.Server, protocolVersion, method, name string, params map[string]any, limit int) (json.RawMessage, *jsonrpc.Error, error) {
	dctx, cancel := detach(ctx, targetFor(ctx, srv))
	defer cancel()
	hc, err := c.HTTPClient(dctx, srv)
	if err != nil {
		return nil, nil, err
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(dctx, http.MethodPost, srv.URL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", protocolVersion)
	req.Header.Set("Mcp-Method", method)
	if name != "" {
		req.Header.Set("Mcp-Name", name)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("upstream %s: %s: %w", srv.Slug, method, err)
	}
	defer resp.Body.Close()
	if limit <= 0 {
		limit = 10 << 20
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, nil, fmt.Errorf("upstream %s: %s: %w", srv.Slug, method, err)
	}
	if len(raw) > limit {
		return nil, nil, fmt.Errorf("upstream %s: %s: the response exceeds %d bytes", srv.Slug, method, limit)
	}
	for _, msg := range messages(raw) {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *jsonrpc.Error  `json:"error"`
		}
		if json.Unmarshal(msg, &m) != nil || strings.TrimSpace(string(m.ID)) != "1" {
			continue
		}
		if m.Error != nil {
			return nil, m.Error, nil
		}
		if len(m.Result) > 0 {
			return m.Result, nil, nil
		}
	}
	if resp.StatusCode >= 400 {
		return nil, nil, fmt.Errorf("upstream %s: %s: HTTP %d", srv.Slug, method, resp.StatusCode)
	}
	return nil, nil, ErrNoReply
}

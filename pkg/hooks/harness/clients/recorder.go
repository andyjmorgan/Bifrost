package clients

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Seen is one HTTP request a client sent, and how it was answered.
type Seen struct {
	HTTPMethod string
	Path       string
	Header     http.Header
	Body       []byte
	// RPCMethod, ID and Params are the JSON-RPC message, when the body was one.
	RPCMethod string
	ID        json.RawMessage
	Params    map[string]any
	// Status and ContentType are the response's.
	Status      int
	ContentType string
	// ClientGone is set when the client closed the connection before the
	// response ended, which is how a 2026-07-28 client cancels over HTTP.
	ClientGone bool
}

// Meta is the request's _meta object, empty when absent.
func (s Seen) Meta() map[string]any {
	m, _ := s.Params["_meta"].(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

// Recorder sits between a client and the gateway and records what the
// client actually sends. The gateway itself does not keep client requests,
// and the SDKs hide them.
type Recorder struct {
	srv    *httptest.Server
	target *url.URL

	mu    sync.Mutex
	seen  []*Seen
	hints bool
}

// PatchCacheHints makes the recorder add ttlMs and cacheScope to listing
// and read results that lack them.
//
// It exists for one case only: pointing a client straight at the harness's
// mock server, which leaves those two fields out, while the Python SDK
// requires them on cacheable results. Never turn it on in front of the
// gateway. Nothing the gateway sends is patched by any test in this
// package; the SDKs see its replies byte for byte.
func (r *Recorder) PatchCacheHints(on bool) {
	r.mu.Lock()
	r.hints = on
	r.mu.Unlock()
}

func (r *Recorder) hinted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hints
}

// patchMessage adds ttlMs and cacheScope to a JSON-RPC response whose
// result is cacheable and lacks them. It reports whether it changed
// anything; a message it does not change keeps its bytes.
func patchMessage(body []byte) ([]byte, bool) {
	var msg map[string]json.RawMessage
	if json.Unmarshal(body, &msg) != nil {
		return body, false
	}
	raw, ok := msg["result"]
	if !ok {
		return body, false
	}
	var res map[string]json.RawMessage
	if json.Unmarshal(raw, &res) != nil || res == nil {
		return body, false
	}
	cacheable := false
	for _, k := range []string{"tools", "prompts", "resources", "resourceTemplates", "contents", "supportedVersions"} {
		if _, ok := res[k]; ok {
			cacheable = true
		}
	}
	if !cacheable {
		return body, false
	}
	changed := false
	if _, has := res["ttlMs"]; !has {
		res["ttlMs"], changed = json.RawMessage(`0`), true
	}
	if _, has := res["cacheScope"]; !has {
		res["cacheScope"], changed = json.RawMessage(`"private"`), true
	}
	if !changed {
		return body, false
	}
	msg["result"], _ = json.Marshal(res)
	out, _ := json.Marshal(msg)
	return out, true
}

// patchLine patches one SSE data line.
func patchLine(line []byte) []byte {
	const prefix = "data:"
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return line
	}
	out, changed := patchMessage(bytes.TrimSpace(line[len(prefix):]))
	if !changed {
		return line
	}
	return append(append([]byte("data: "), out...), '\n')
}

// patchedBody rewrites a stream line by line as it is read, so events still
// reach the client as they are sent.
type patchedBody struct {
	src io.ReadCloser
	br  *bufio.Reader
	buf []byte
}

func (p *patchedBody) Read(b []byte) (int, error) {
	if len(p.buf) == 0 {
		line, err := p.br.ReadBytes('\n')
		if len(line) > 0 {
			p.buf = patchLine(line)
		}
		if len(p.buf) == 0 {
			return 0, err
		}
	}
	n := copy(b, p.buf)
	p.buf = p.buf[n:]
	return n, nil
}

func (p *patchedBody) Close() error { return p.src.Close() }

type seenKey struct{}

// NewRecorder starts a recording proxy in front of target, a base URL such
// as the gateway's. It stops when the test ends.
func NewRecorder(t testing.TB, target string) *Recorder {
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	r := &Recorder{target: u}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			pr.Out.Host = u.Host
		},
		FlushInterval: -1, // relay each stream event as it arrives
		ModifyResponse: func(resp *http.Response) error {
			if s, ok := resp.Request.Context().Value(seenKey{}).(*Seen); ok {
				r.mu.Lock()
				s.Status, s.ContentType = resp.StatusCode, resp.Header.Get("Content-Type")
				r.mu.Unlock()
			}
			switch ct := resp.Header.Get("Content-Type"); {
			case !r.hinted():
			case strings.HasPrefix(ct, "text/event-stream"):
				resp.Body = &patchedBody{src: resp.Body, br: bufio.NewReaderSize(resp.Body, 64<<10)}
				resp.Header.Del("Content-Length")
			case strings.HasPrefix(ct, "application/json"):
				body, err := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if err != nil {
					return err
				}
				body, _ = patchMessage(body)
				resp.Body = io.NopCloser(bytes.NewReader(body))
				resp.ContentLength = int64(len(body))
				resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(http.StatusBadGateway) },
	}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(req.Body, 64<<20))
		s := &Seen{HTTPMethod: req.Method, Path: req.URL.Path, Header: req.Header.Clone(), Body: body}
		var msg struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
			Params map[string]any  `json:"params"`
		}
		if json.Unmarshal(body, &msg) == nil {
			s.RPCMethod, s.ID, s.Params = msg.Method, msg.ID, msg.Params
		}
		r.mu.Lock()
		r.seen = append(r.seen, s)
		r.mu.Unlock()
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		// The proxy aborts the handler with a panic when the client leaves
		// mid-stream, so this is recorded on the way out, not after.
		defer func() {
			if req.Context().Err() != nil {
				r.mu.Lock()
				s.ClientGone = true
				r.mu.Unlock()
			}
		}()
		proxy.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), seenKey{}, s)))
	}))
	t.Cleanup(func() {
		r.srv.CloseClientConnections()
		r.srv.Close()
	})
	return r
}

// Via rewrites an endpoint under the target so it goes through the recorder.
func (r *Recorder) Via(endpoint string) string {
	return r.srv.URL + strings.TrimPrefix(endpoint, strings.TrimSuffix(r.target.String(), "/"))
}

// Seen returns the recorded requests for a JSON-RPC method, oldest first.
// An empty method returns all of them.
func (r *Recorder) Seen(method string) []Seen {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Seen
	for _, s := range r.seen {
		if method == "" || s.RPCMethod == method {
			out = append(out, *s)
		}
	}
	return out
}

// Forget drops what was recorded.
func (r *Recorder) Forget() {
	r.mu.Lock()
	r.seen = nil
	r.mu.Unlock()
}

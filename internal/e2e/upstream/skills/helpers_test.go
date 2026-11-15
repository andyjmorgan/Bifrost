package skills

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// rawClient speaks 2026-07-28 JSON-RPC over HTTP with the required _meta and
// headers, so tests see the exact wire shapes.
type rawClient struct {
	t   *testing.T
	url string
	id  int
}

type rpcResp struct {
	Status int
	Result json.RawMessage
	Error  *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
}

func newTestServer(t *testing.T, opts Options) (*Server, *rawClient) {
	t.Helper()
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return s, &rawClient{t: t, url: ts.URL}
}

func reqMeta() map[string]any {
	return map[string]any{
		MetaProtocolVersion:    ProtocolVersion,
		MetaClientInfo:         map[string]any{"name": "skills-test", "version": "1.0.0"},
		MetaClientCapabilities: map[string]any{},
	}
}

// do sends method with params (params._meta is added) and extra headers.
func (c *rawClient) do(method string, params map[string]any, hdr map[string]string) rpcResp {
	c.t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	if _, ok := params["_meta"]; !ok {
		params["_meta"] = reqMeta()
	}
	c.id++
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	req, _ := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	req.Header.Set("Mcp-Method", method)
	if u, ok := params["uri"].(string); ok && method == "resources/read" {
		req.Header.Set("Mcp-Name", u)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var out rpcResp
	out.Status = res.StatusCode
	if len(b) > 0 {
		if err := json.Unmarshal(b, &out); err != nil {
			c.t.Fatalf("%s: decode %q: %v", method, b, err)
		}
	}
	return out
}

// ok sends a request, requires success, and decodes the result into v.
func (c *rawClient) ok(method string, params map[string]any, v any) map[string]any {
	c.t.Helper()
	r := c.do(method, params, nil)
	if r.Status != 200 || r.Error != nil {
		c.t.Fatalf("%s %v: status %d error %+v", method, params, r.Status, r.Error)
	}
	var generic map[string]any
	if err := json.Unmarshal(r.Result, &generic); err != nil {
		c.t.Fatal(err)
	}
	if generic["resultType"] != "complete" {
		c.t.Fatalf("%s: resultType = %v", method, generic["resultType"])
	}
	if v != nil {
		if err := json.Unmarshal(r.Result, v); err != nil {
			c.t.Fatal(err)
		}
	}
	return generic
}

// errCode sends a request and returns the JSON-RPC error code (0 if none).
func (c *rawClient) errCode(method string, params map[string]any) int {
	c.t.Helper()
	r := c.do(method, params, nil)
	if r.Error == nil {
		return 0
	}
	return r.Error.Code
}

type listSkillsRes struct {
	Skills     []Skill `json:"skills"`
	NextCursor string  `json:"nextCursor"`
}

func (c *rawClient) listAllSkills(t *testing.T) []Skill {
	var all []Skill
	cursor := ""
	for {
		p := map[string]any{}
		if cursor != "" {
			p["cursor"] = cursor
		}
		var res listSkillsRes
		c.ok("skills/list", p, &res)
		all = append(all, res.Skills...)
		if res.NextCursor == "" {
			return all
		}
		cursor = res.NextCursor
	}
}

type readRes struct {
	Contents []ResourceContents `json:"contents"`
	TTLMs    int                `json:"ttlMs"`
}

// readBytes reads a resource and returns its raw bytes and mimeType.
func (c *rawClient) readBytes(t *testing.T, uri string) ([]byte, string) {
	t.Helper()
	var res readRes
	c.ok("resources/read", map[string]any{"uri": uri}, &res)
	if len(res.Contents) != 1 {
		t.Fatalf("read %s: %d contents", uri, len(res.Contents))
	}
	rc := res.Contents[0]
	if rc.URI != uri {
		t.Fatalf("read %s: content uri %s", uri, rc.URI)
	}
	switch {
	case rc.Text != nil && rc.Blob == nil:
		return []byte(*rc.Text), rc.MIMEType
	case rc.Blob != nil && rc.Text == nil:
		b, err := base64.StdEncoding.DecodeString(*rc.Blob)
		if err != nil {
			t.Fatal(err)
		}
		return b, rc.MIMEType
	}
	t.Fatalf("read %s: need exactly one of text/blob", uri)
	return nil, ""
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

package skills

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Options configures a test server. The zero value serves the synthetic
// skill set with the extension and directoryRead declared.
type Options struct {
	// Name and Version populate serverInfo. Defaults: "skills-test-server", "0.1.0".
	Name, Version string
	// Instructions is returned from server/discover and initialize.
	Instructions string

	// DisableSkillsExtension omits io.modelcontextprotocol/skills from the
	// declared capabilities; skills/* and resources/directory/read then answer
	// -32601 (HTTP 404). Toggle at runtime with SetSkillsExtension.
	DisableSkillsExtension bool
	// DisableDirectoryRead declares the extension as {} instead of
	// {"directoryRead": true}; resources/directory/read then answers -32601.
	DisableDirectoryRead bool

	// NoSynthetic drops the built-in skill set (including the dynamic skill
	// and file:///README.md), leaving only Files and Dir content.
	NoSynthetic bool
	// Files adds or overrides files by URI at construction. Use SetFile to
	// change content later.
	Files map[string][]byte

	// Dir, when set, is walked with LoadDir and its skills are served under
	// skill://<DirPrefix>/... . DirPrefix defaults to "anthropic"; set
	// DirPrefixNone to serve them at skill://<path>.
	Dir           string
	DirPrefix     string
	DirPrefixNone bool

	// ListedResources is what resources/list returns (URIs that do not exist
	// are dropped). nil means DefaultListedResources.
	ListedResources []string
	// Unlisted names SKILL.md URIs left out of skills/list. skills/get still
	// answers for them.
	Unlisted []string

	// PageSize bounds skills/list, resources/list, resources/templates/list and
	// resources/directory/read pages. 0 disables pagination.
	PageSize int
	// TTLMs is the ttlMs on cacheable results (nil means 60000). Results for
	// dynamic content always carry 0.
	TTLMs *int
	// CacheScope is "public" (default) or "private".
	CacheScope string
}

// Server is the test server. It implements http.Handler.
type Server struct {
	opts       Options
	name, ver  string
	ttl        int
	scope      string
	extEnabled atomic.Bool
	dirRead    atomic.Bool

	mu       sync.RWMutex
	files    map[string]*file
	dynRoots []string
	dyn      map[string]func() []byte
	listed   []string
	unlisted map[string]bool
	skipped  []SkippedSkill

	reqMu sync.Mutex
	reqs  []Request
}

var _ http.Handler = (*Server)(nil)

// New builds a server. It fails only when Options.Dir cannot be read.
func New(opts Options) (*Server, error) {
	s := &Server{
		opts:     opts,
		name:     cmpOr(opts.Name, "skills-test-server"),
		ver:      cmpOr(opts.Version, "0.1.0"),
		ttl:      60000,
		scope:    cmpOr(opts.CacheScope, "public"),
		files:    map[string]*file{},
		dyn:      map[string]func() []byte{},
		unlisted: map[string]bool{},
	}
	if opts.TTLMs != nil {
		s.ttl = *opts.TTLMs
	}
	s.extEnabled.Store(!opts.DisableSkillsExtension)
	s.dirRead.Store(!opts.DisableDirectoryRead)
	if !opts.NoSynthetic {
		for u, b := range DefaultFiles() {
			s.files[u] = newFile(u, b)
		}
		s.dynRoots, s.dyn = dynamicFiles()
	}
	if opts.Dir != "" {
		prefix := cmpOr(opts.DirPrefix, "anthropic")
		if opts.DirPrefixNone {
			prefix = ""
		}
		files, skipped, err := LoadDir(opts.Dir, prefix)
		if err != nil {
			return nil, err
		}
		for u, b := range files {
			s.files[u] = newFile(u, b)
		}
		s.skipped = skipped
	}
	for u, b := range opts.Files {
		s.files[u] = newFile(u, b)
	}
	s.listed = opts.ListedResources
	if s.listed == nil {
		s.listed = slices.Clone(DefaultListedResources)
	}
	for _, u := range opts.Unlisted {
		s.unlisted[u] = true
	}
	return s, nil
}

// MustNew is New that panics on error.
func MustNew(opts Options) *Server {
	s, err := New(opts)
	if err != nil {
		panic(err)
	}
	return s
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ---- runtime control ----

// SetFile adds or replaces a static file. Manifests reflect it immediately.
func (s *Server) SetFile(uri string, content []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[uri] = newFile(uri, content)
}

// RemoveFile deletes a static file.
func (s *Server) RemoveFile(uri string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, uri)
}

// File returns a copy of a static file's bytes.
func (s *Server) File(uri string) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok := s.files[uri]
	if !ok {
		return nil, false
	}
	return bytes.Clone(f.content), true
}

// FileDigest returns the current digest and size of a static file.
func (s *Server) FileDigest(uri string) (digest string, size int, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok := s.files[uri]
	if !ok {
		return "", 0, false
	}
	return f.digest, len(f.content), true
}

// URIs returns every static and dynamic file URI, sorted.
func (s *Server) URIs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.allURIs()
}

// SetSkillsExtension toggles the extension declaration and skills/* methods.
func (s *Server) SetSkillsExtension(enabled bool) { s.extEnabled.Store(enabled) }

// SetDirectoryRead toggles directoryRead and resources/directory/read.
func (s *Server) SetDirectoryRead(enabled bool) { s.dirRead.Store(enabled) }

// SetListedResources replaces what resources/list returns.
func (s *Server) SetListedResources(uris []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listed = slices.Clone(uris)
}

// SetUnlisted replaces the set of SKILL.md URIs hidden from skills/list.
func (s *Server) SetUnlisted(uris []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unlisted = map[string]bool{}
	for _, u := range uris {
		s.unlisted[u] = true
	}
}

// Skipped reports SKILL.md files under Options.Dir that were not served.
func (s *Server) Skipped() []SkippedSkill { return slices.Clone(s.skipped) }

// Skills returns the entries of every served skill, listed or not, sorted by
// URI. This is what skills/list would return with no Unlisted and no paging.
func (s *Server) Skills() []Skill {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.allSkills(false)
}

// GetSkill returns the skills/get entry for a SKILL.md URI.
func (s *Server) GetSkill(uri string) (Skill, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.skillEntry(uri)
}

// Requests returns every message received so far, in arrival order.
func (s *Server) Requests() []Request {
	s.reqMu.Lock()
	defer s.reqMu.Unlock()
	return slices.Clone(s.reqs)
}

// RequestsFor returns the received messages with the given method.
func (s *Server) RequestsFor(method string) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Method == method {
			out = append(out, r)
		}
	}
	return out
}

// ResetRequests clears the request log.
func (s *Server) ResetRequests() {
	s.reqMu.Lock()
	defer s.reqMu.Unlock()
	s.reqs = nil
}

// ---- model ----

func (s *Server) allURIs() []string {
	out := make([]string, 0, len(s.files)+len(s.dyn))
	for u := range s.files {
		out = append(out, u)
	}
	for u := range s.dyn {
		if _, dup := s.files[u]; !dup {
			out = append(out, u)
		}
	}
	slices.Sort(out)
	return out
}

func (s *Server) isDynamic(uri string) bool {
	_, ok := s.dyn[uri]
	return ok
}

// read returns a file's bytes (generating dynamic content).
func (s *Server) read(uri string) ([]byte, bool) {
	if f, ok := s.files[uri]; ok {
		return f.content, true
	}
	if g, ok := s.dyn[uri]; ok {
		return g(), true
	}
	return nil, false
}

func (s *Server) exists(uri string) bool {
	_, ok := s.files[uri]
	return ok || s.isDynamic(uri)
}

func (s *Server) dynamicRoot(root string) bool { return slices.Contains(s.dynRoots, root) }

func (s *Server) skillEntry(uri string) (Skill, error) {
	root, ok := skillRoot(uri)
	if !ok {
		return Skill{}, fmt.Errorf("%s is not the URI of a SKILL.md", uri)
	}
	content, ok := s.read(uri)
	if !ok {
		return Skill{}, fmt.Errorf("No skill is served at %s", uri)
	}
	fm, err := validateSkill(uri, content)
	if err != nil {
		return Skill{}, fmt.Errorf("No valid skill is served at %s: %v", uri, err)
	}
	if s.isDynamic(uri) && s.dynamicRoot(root) {
		return Skill{URI: uri, Frontmatter: fm, Resources: json.RawMessage(`"dynamic"`)}, nil
	}
	prefix := root + "/"
	var res []SkillResource
	var keys []string
	for u := range s.files {
		if strings.HasPrefix(u, prefix) {
			keys = append(keys, u)
		}
	}
	slices.Sort(keys)
	for _, u := range keys {
		f := s.files[u]
		res = append(res, SkillResource{URI: u, Digest: f.digest, Size: len(f.content)})
	}
	raw, err := json.Marshal(res)
	if err != nil {
		return Skill{}, err
	}
	return Skill{URI: uri, Frontmatter: fm, Resources: raw}, nil
}

func (s *Server) allSkills(respectUnlisted bool) []Skill {
	var out []Skill
	for _, u := range s.allURIs() {
		if path.Base(u) != "SKILL.md" {
			continue
		}
		if respectUnlisted && s.unlisted[u] {
			continue
		}
		sk, err := s.skillEntry(u)
		if err != nil {
			continue
		}
		out = append(out, sk)
	}
	return out
}

func (s *Server) resourceMeta(uri string) Resource {
	r := Resource{URI: uri, Name: path.Base(uri)}
	if f, ok := s.files[uri]; ok {
		r.MIMEType = f.mime
		n := len(f.content)
		r.Size = &n
	} else if s.isDynamic(uri) {
		r.MIMEType = MIMEType(uri, nil)
	}
	if path.Base(uri) == "SKILL.md" {
		if f, ok := s.files[uri]; ok {
			if fm, err := validateSkill(uri, f.content); err == nil {
				r.Name, _ = fm["name"].(string)
				r.Description, _ = fm["description"].(string)
			}
		}
	}
	return r
}

// ---- pagination ----

func encodeCursor(off int) string {
	return base64.RawURLEncoding.EncodeToString([]byte("off:" + strconv.Itoa(off)))
}

func decodeCursor(c *string) (int, error) {
	if c == nil || *c == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(*c)
	if err != nil {
		return 0, errors.New("invalid cursor")
	}
	n, ok := strings.CutPrefix(string(b), "off:")
	if !ok {
		return 0, errors.New("invalid cursor")
	}
	off, err := strconv.Atoi(n)
	if err != nil || off < 0 {
		return 0, errors.New("invalid cursor")
	}
	return off, nil
}

func paginate[T any](items []T, cursor *string, size int) ([]T, string, error) {
	off, err := decodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	if off > len(items) {
		return nil, "", errors.New("invalid cursor")
	}
	if size <= 0 {
		return items[off:], "", nil
	}
	end := min(off+size, len(items))
	next := ""
	if end < len(items) {
		next = encodeCursor(end)
	}
	return items[off:end], next, nil
}

// ---- HTTP / JSON-RPC ----

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
	status  int
}

func errInvalidParams(format string, a ...any) *rpcError {
	return &rpcError{Code: CodeInvalidParams, Message: fmt.Sprintf(format, a...), status: http.StatusOK}
}

type incoming struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type commonParams struct {
	Meta   map[string]any `json:"_meta"`
	Cursor *string        `json:"cursor"`
	URI    *string        `json:"uri"`
	// initialize
	ProtocolVersion string `json:"protocolVersion"`
}

// ServeHTTP implements stateless Streamable HTTP: one JSON-RPC message per
// POST, answered with a single application/json response.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		writeJSON(w, http.StatusBadRequest, errorResponse(nil, &rpcError{Code: CodeInvalidRequest, Message: "batch requests are not supported"}))
		return
	}
	var msg incoming
	if err := json.Unmarshal(body, &msg); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse(nil, &rpcError{Code: CodeParseError, Message: "parse error: " + err.Error()}))
		return
	}
	if msg.JSONRPC != "2.0" || msg.Method == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse(msg.ID, &rpcError{Code: CodeInvalidRequest, Message: "not a JSON-RPC 2.0 request or notification"}))
		return
	}
	if string(msg.ID) == "null" {
		msg.ID = nil
	}

	s.reqMu.Lock()
	s.reqs = append(s.reqs, Request{
		Time:   time.Now(),
		Method: msg.Method,
		ID:     bytes.Clone(msg.ID),
		Params: bytes.Clone(msg.Params),
		Header: r.Header.Clone(),
	})
	s.reqMu.Unlock()

	if msg.ID == nil { // notification
		w.WriteHeader(http.StatusAccepted)
		return
	}

	var p commonParams
	if len(msg.Params) > 0 && string(msg.Params) != "null" {
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			writeJSON(w, http.StatusOK, errorResponse(msg.ID, errInvalidParams("invalid params: %v", err)))
			return
		}
	}
	if rerr := checkHeaders(r, msg.Method, p); rerr != nil {
		writeJSON(w, rerr.status, errorResponse(msg.ID, rerr))
		return
	}

	result, rerr := s.dispatch(msg.Method, p)
	if rerr != nil {
		status := rerr.status
		if status == 0 {
			status = http.StatusOK
		}
		writeJSON(w, status, errorResponse(msg.ID, rerr))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
}

func checkHeaders(r *http.Request, method string, p commonParams) *rpcError {
	mismatch := func(msg string) *rpcError {
		return &rpcError{Code: CodeHeaderMismatch, Message: msg, status: http.StatusBadRequest}
	}
	metaVer, _ := p.Meta[MetaProtocolVersion].(string)
	if method != "initialize" {
		if hv := r.Header.Get("MCP-Protocol-Version"); hv != "" && metaVer != "" && hv != metaVer {
			return mismatch(fmt.Sprintf("MCP-Protocol-Version header %q does not match _meta protocolVersion %q", hv, metaVer))
		}
		if metaVer != "" && !slices.Contains(SupportedVersions, metaVer) {
			return &rpcError{
				Code:    CodeUnsupportedProtocolVersion,
				Message: "unsupported protocol version " + metaVer,
				Data:    map[string]any{"supported": SupportedVersions, "requested": metaVer},
				status:  http.StatusBadRequest,
			}
		}
	}
	if hm := r.Header.Get("Mcp-Method"); hm != "" && hm != method {
		return mismatch(fmt.Sprintf("Mcp-Method header %q does not match method %q", hm, method))
	}
	if hn := r.Header.Get("Mcp-Name"); hn != "" && p.URI != nil && method == "resources/read" {
		if decodeHeaderValue(hn) != *p.URI {
			return mismatch(fmt.Sprintf("Mcp-Name header %q does not match params.uri %q", hn, *p.URI))
		}
	}
	return nil
}

func decodeHeaderValue(v string) string {
	if inner, ok := strings.CutPrefix(v, "=?base64?"); ok {
		if inner, ok = strings.CutSuffix(inner, "?="); ok {
			if b, err := base64.StdEncoding.DecodeString(inner); err == nil {
				return string(b)
			}
		}
	}
	return v
}

func errorResponse(id json.RawMessage, e *rpcError) map[string]any {
	var idv any = json.RawMessage("null")
	if id != nil {
		idv = id
	}
	return map[string]any{"jsonrpc": "2.0", "id": idv, "error": e}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) capabilities() map[string]any {
	caps := map[string]any{"resources": map[string]any{}}
	if s.extEnabled.Load() {
		settings := map[string]any{}
		if s.dirRead.Load() {
			settings["directoryRead"] = true
		}
		caps["extensions"] = map[string]any{ExtensionID: settings}
	}
	return caps
}

func (s *Server) cacheable(m map[string]any, ttl int) map[string]any {
	m["resultType"] = "complete"
	m["ttlMs"] = ttl
	m["cacheScope"] = s.scope
	return m
}

func (s *Server) dispatch(method string, p commonParams) (map[string]any, *rpcError) {
	notFound := &rpcError{Code: CodeMethodNotFound, Message: "method not found: " + method, status: http.StatusNotFound}
	serverInfo := map[string]any{"name": s.name, "version": s.ver}
	switch method {
	case "initialize":
		v := p.ProtocolVersion
		if !slices.Contains(SupportedVersions, v) || v >= ProtocolVersion {
			v = "2025-11-25"
		}
		res := map[string]any{
			"resultType":      "complete",
			"protocolVersion": v,
			"capabilities":    s.capabilities(),
			"serverInfo":      serverInfo,
		}
		if s.opts.Instructions != "" {
			res["instructions"] = s.opts.Instructions
		}
		return res, nil

	case "ping":
		return map[string]any{"resultType": "complete"}, nil

	case "server/discover":
		res := map[string]any{
			"supportedVersions": SupportedVersions,
			"capabilities":      s.capabilities(),
			"_meta":             map[string]any{MetaServerInfo: serverInfo},
		}
		if s.opts.Instructions != "" {
			res["instructions"] = s.opts.Instructions
		}
		return s.cacheable(res, s.ttl), nil

	case "resources/list":
		s.mu.RLock()
		var all []Resource
		for _, u := range s.listed {
			if s.exists(u) {
				all = append(all, s.resourceMeta(u))
			}
		}
		s.mu.RUnlock()
		page, next, err := paginate(all, p.Cursor, s.opts.PageSize)
		if err != nil {
			return nil, errInvalidParams("%v", err)
		}
		res := map[string]any{"resources": nonNil(page)}
		if next != "" {
			res["nextCursor"] = next
		}
		return s.cacheable(res, s.ttl), nil

	case "resources/templates/list":
		all := []ResourceTemplate{{
			URITemplate: SkillTemplate,
			Name:        "skill-files",
			Description: "Any file of any skill served by this server, addressed as skill://<skill-path>/<file-path>.",
		}}
		page, next, err := paginate(all, p.Cursor, s.opts.PageSize)
		if err != nil {
			return nil, errInvalidParams("%v", err)
		}
		res := map[string]any{"resourceTemplates": nonNil(page)}
		if next != "" {
			res["nextCursor"] = next
		}
		return s.cacheable(res, s.ttl), nil

	case "resources/read":
		if p.URI == nil || *p.URI == "" {
			return nil, errInvalidParams("params.uri is required")
		}
		uri := *p.URI
		s.mu.RLock()
		b, ok := s.read(uri)
		dynamic := s.isDynamic(uri)
		var mimeType string
		if ok {
			if f, isStatic := s.files[uri]; isStatic {
				mimeType = f.mime
			} else {
				mimeType = MIMEType(uri, b)
			}
		}
		_, _, isDir := children(uri, s.allURIs())
		s.mu.RUnlock()
		if !ok {
			if isDir {
				return nil, errInvalidParams("%s is a directory resource; use resources/directory/read", uri)
			}
			return nil, errInvalidParams("Resource not found: %s", uri)
		}
		ttl := s.ttl
		if dynamic {
			ttl = 0
		}
		return s.cacheable(map[string]any{"contents": []ResourceContents{contents(uri, mimeType, b)}}, ttl), nil

	case "skills/list":
		if !s.extEnabled.Load() {
			return nil, notFound
		}
		s.mu.RLock()
		all := s.allSkills(true)
		s.mu.RUnlock()
		page, next, err := paginate(all, p.Cursor, s.opts.PageSize)
		if err != nil {
			return nil, errInvalidParams("%v", err)
		}
		res := map[string]any{"skills": nonNil(page)}
		if next != "" {
			res["nextCursor"] = next
		}
		return s.cacheable(res, s.ttl), nil

	case "skills/get":
		if !s.extEnabled.Load() {
			return nil, notFound
		}
		if p.URI == nil || *p.URI == "" {
			return nil, errInvalidParams("params.uri is required")
		}
		s.mu.RLock()
		sk, err := s.skillEntry(*p.URI)
		s.mu.RUnlock()
		if err != nil {
			return nil, errInvalidParams("%v", err)
		}
		ttl := s.ttl
		if sk.IsDynamic() {
			ttl = 0
		}
		return s.cacheable(map[string]any{"skill": sk}, ttl), nil

	case "resources/directory/read":
		if !s.extEnabled.Load() || !s.dirRead.Load() {
			return nil, notFound
		}
		if p.URI == nil || *p.URI == "" {
			return nil, errInvalidParams("params.uri is required")
		}
		uri := *p.URI
		s.mu.RLock()
		files, dirs, ok := children(uri, s.allURIs())
		var all []Resource
		for _, d := range dirs {
			all = append(all, Resource{URI: d, Name: path.Base(d), MIMEType: DirectoryMIMEType})
		}
		for _, f := range files {
			all = append(all, s.resourceMeta(f))
		}
		isFile := s.exists(uri)
		s.mu.RUnlock()
		if !ok {
			if isFile {
				return nil, errInvalidParams("%s is not a directory resource", uri)
			}
			return nil, errInvalidParams("No directory resource at %s", uri)
		}
		slices.SortFunc(all, func(a, b Resource) int { return strings.Compare(a.URI, b.URI) })
		page, next, err := paginate(all, p.Cursor, s.opts.PageSize)
		if err != nil {
			return nil, errInvalidParams("%v", err)
		}
		res := map[string]any{"resources": nonNil(page)}
		if next != "" {
			res["nextCursor"] = next
		}
		return s.cacheable(res, s.ttl), nil
	}
	return nil, notFound
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

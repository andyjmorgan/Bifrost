package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/internal/audit"
	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/cache"
	"github.com/andyjmorgan/Bifrost/internal/store"
	"github.com/andyjmorgan/Bifrost/internal/toolsearch"
	"github.com/andyjmorgan/Bifrost/internal/upstream"
)

// Search mode (?search=true) replaces the flat tool list with a small,
// fixed surface: namespaces (servers) are described up front, and the
// model lists, searches, loads and executes tools on demand. Shaped after
// OpenAI's tool-search namespaces so the hierarchy is familiar, but it
// works for any MCP client.
const (
	SearchTool  = "tool_search"
	ListTool    = "list_tools"
	ExecuteTool = "execute_tool"
	DefineTool  = "get_tool_definition"
	// ListPageSize is the page size of list_tools.
	ListPageSize = 200
)

type searchKey struct{}

type progressKey struct{}

// progressFn reports fan-out progress to the client (MCP notifications/progress).
type progressFn func(done, total float64, msg string)

func withProgress(ctx context.Context, fn progressFn) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

func progressFrom(ctx context.Context) progressFn {
	fn, _ := ctx.Value(progressKey{}).(progressFn)
	return fn
}

func withSearchMode(ctx context.Context) context.Context {
	return context.WithValue(ctx, searchKey{}, true)
}

func searchMode(ctx context.Context) bool {
	v, _ := ctx.Value(searchKey{}).(bool)
	return v
}

// catalogEntry is one server's view in the catalogue: its cached initialize
// result and its tools (unprefixed), or the error that made it unavailable.
type catalogEntry struct {
	srv   *store.Server
	init  cache.Init
	tools []*mcp.Tool // what the slug exposes: inside the ceiling and the selection
	err   error
}

// catalog gathers every enabled server on the route with its tools, from
// cache where live, fetching (and caching) the misses concurrently. With
// force, every server is asked afresh. Unavailable servers are recorded in
// the audit (issue + skip event) and returned with err set.
// catalog lists every exposure the caller holds (one on direct) with its
// tools cut to the ceiling and the selection.
func (g *Gateway) catalog(ctx context.Context, caller *auth.Caller, route, slug string, force bool) ([]catalogEntry, error) {
	var servers []*store.Server
	var cached map[uuid.UUID]store.CacheBundle
	var conflicts map[uuid.UUID]error
	if route == "direct" {
		srv, bundle, err := g.exposure(ctx, caller, slug, cache.NSTools, cache.NSInit)
		if err != nil {
			return nil, &jsonrpc.Error{Code: -32004, Message: "unknown server"}
		}
		servers = []*store.Server{srv}
		cached = map[uuid.UUID]store.CacheBundle{srv.ID: bundle}
	} else {
		var err error
		servers, cached, conflicts, err = g.exposures(ctx, caller, cache.NSTools, cache.NSInit)
		if err != nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
		}
	}
	var (
		mu      sync.Mutex
		entries []catalogEntry
		wg      sync.WaitGroup
	)
	// Progress: three steps (connecting, connected+listing, done) for every
	// server that actually needs a connection, on one monotonic counter since
	// servers finish concurrently. Servers answered from cache are silent, so
	// a search over a warm cache reports nothing.
	report := progressFrom(ctx)
	fetching := 0
	for _, srv := range servers {
		if !srv.Enabled && route != "direct" {
			continue
		}
		if _, hit := decodeTools(cached[srv.ID][cache.NSTools]); force || !hit {
			fetching++
		}
	}
	total := float64(fetching * 3)
	var step int
	var stepMu sync.Mutex
	advance := func(n int, msg string) {
		if report == nil {
			return
		}
		// Send under the lock so notifications leave in counter order; the
		// spec wants progress to increase with every notification.
		stepMu.Lock()
		defer stepMu.Unlock()
		step += n
		report(float64(step), total, msg)
	}
	for _, srv := range servers {
		if !srv.Enabled && route != "direct" {
			continue
		}
		if c, ok := conflicts[srv.ID]; ok {
			audit.AddIssue(ctx, srv.Slug+": "+c.Error())
			mu.Lock()
			entries = append(entries, catalogEntry{srv: srv, err: c})
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(srv *store.Server) {
			defer wg.Done()
			e := catalogEntry{srv: srv}
			bundle := cached[srv.ID]
			if raw := bundle[cache.NSInit]; raw != nil {
				_ = json.Unmarshal(raw, &e.init)
			}
			hit := false
			steps := 0
			if !force {
				e.tools, hit = decodeTools(bundle[cache.NSTools])
			}
			if !hit {
				// A forced refresh is about the tool list, not the protocol:
				// start at the cached version too. Upgrades are re-discovered
				// when the init row expires, which bounds detection by one TTL.
				stage := func(msg string) { advance(1, srv.Slug+": "+msg) }
				e.tools, steps, e.err = g.fetchTools(ctx, srv, e.init.ProtocolVersion, force, stage)
			}
			if e.err == nil {
				if !hit {
					g.observeTools(ctx, srv, e.tools)
				}
				e.tools = cutTools(srv, e.tools)
			}
			if e.err != nil {
				errType := "upstream"
				switch {
				case errors.Is(e.err, upstream.ErrCredentialPending):
					errType = "credential_pending"
				case errors.Is(e.err, upstream.ErrCredentialBroken):
					errType = "credential_broken"
				}
				g.log.Warn("catalog: server unavailable", "slug", srv.Slug, "type", errType, "err", e.err)
				audit.AddIssue(ctx, srv.Slug+": "+e.err.Error())
				if g.rec != nil {
					g.rec.RecordSkipped(ctx, srv, route, "tools/list", errType, e.err.Error())
				}
			}
			mu.Lock()
			entries = append(entries, e)
			mu.Unlock()
			// Stages this caller did not report itself (a failed connect, or a
			// fetch shared with another caller) still count towards the total.
			switch {
			case e.err != nil:
				advance(3-steps, fmt.Sprintf("%s: unavailable (%s)", srv.Slug, oneLine(e.err.Error(), 120)))
			case hit:
				// served from cache: no progress
			default:
				advance(3-steps, fmt.Sprintf("%s: %s", srv.Slug, plural(len(e.tools), "tool")))
			}
		}(srv)
	}
	wg.Wait()
	sort.Slice(entries, func(i, j int) bool { return entries[i].srv.Slug < entries[j].srv.Slug })
	return entries, nil
}

func unavailableOf(entries []catalogEntry) []map[string]string {
	var out []map[string]string
	for _, e := range entries {
		if e.err != nil {
			out = append(out, map[string]string{"slug": e.srv.Slug, "name": e.srv.Name, "error": e.err.Error()})
		}
	}
	return out
}

// ---- tools/list in search mode ---------------------------------------------

func (g *Gateway) searchToolsList(ctx context.Context, caller *auth.Caller, route, slug string) (mcp.Result, error) {
	entries, err := g.catalog(ctx, caller, route, slug, false)
	if err != nil {
		return nil, err
	}
	tools := []*mcp.Tool{searchToolDef(entries, route), listToolDef(), defineToolDef(), executeToolDef()}
	if route == "mux" {
		tools = append(tools, refreshToolDef())
	}
	out := &mcp.ListToolsResult{Tools: tools}
	if u := unavailableOf(entries); len(u) > 0 {
		out.Meta = mcp.Meta{MetaKeyUnavailable: u}
	}
	return out, nil
}

// roster is the up-front view of the namespaces: what is connected and what
// each is for, so the model searches with intent.
func roster(entries []catalogEntry) string {
	var sb strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&sb, "- %s: %s", e.srv.Slug, e.srv.Name)
		if e.srv.Description != "" {
			sb.WriteString(" — " + strings.TrimSpace(e.srv.Description))
		}
		if e.init.Instructions != "" {
			sb.WriteString(" Server notes: " + oneLine(e.init.Instructions, 300))
		}
		if e.err != nil {
			sb.WriteString(" (unavailable)")
		} else {
			if n := len(e.tools); n == 1 {
				sb.WriteString(" (1 tool)")
			} else {
				fmt.Fprintf(&sb, " (%d tools)", n)
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

func searchToolDef(entries []catalogEntry, route string) *mcp.Tool {
	desc := "Search the tools behind this gateway and get their full definitions, ranked by relevance to your query " +
		"(names, descriptions, input schemas and the server each belongs to). Name a server in the query to see its tools. " +
		"Call results with execute_tool using the returned name.\n\nConnected servers:\n" + roster(entries)
	if route == "direct" {
		desc = "Search this server's tools and get their full definitions, ranked by relevance to your query. Call results with execute_tool.\n\n" + roster(entries)
	}
	return &mcp.Tool{
		Name:        SearchTool,
		Title:       "Search tools",
		Annotations: gatewayHints(true),
		Description: desc,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "What you want to do, or a tool or server name."},
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 10, "description": "Maximum results."},
			},
			"required":             []string{"query"},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tools": toolList(),
				"hint":  map[string]any{"type": "string"},
			},
			"required": []string{"tools"},
		},
	}
}

// toolList is the output schema shared by tool_search and get_tool_definition.
func toolList() map[string]any {
	return map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"server": map[string]any{"type": "string", "description": "Slug of the server that owns the tool."},
				"tool":   map[string]any{"type": "object", "description": "The full MCP tool definition; its name is the callable name."},
			},
			"required": []string{"server", "tool"},
		},
	}
}

func listToolDef() *mcp.Tool {
	props := map[string]any{
		"server":    map[string]any{"type": "string", "description": "Only this server (slug from the connected servers list)."},
		"cursor":    map[string]any{"type": "string", "description": "Opaque cursor from a previous page."},
		"summaries": map[string]any{"type": "boolean", "default": false, "description": "Include a one-line summary per tool (costs ~5x the tokens of names alone)."},
	}
	desc := fmt.Sprintf("List every tool name grouped by server (%d per page) without loading schemas. Use get_tool_definition to load the ones you want.", ListPageSize)
	return &mcp.Tool{
		Name:        ListTool,
		Title:       "List tools",
		Annotations: gatewayHints(true),
		Description: desc,
		InputSchema: map[string]any{"type": "object", "properties": props, "additionalProperties": false},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tools": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"server":  map[string]any{"type": "string"},
							"name":    map[string]any{"type": "string", "description": "The callable name."},
							"summary": map[string]any{"type": "string"},
						},
						"required": []string{"server", "name"},
					},
				},
				"total":      map[string]any{"type": "integer", "description": "Tools matching the filter across all pages."},
				"nextCursor": map[string]any{"type": "string", "description": "Present when more pages follow."},
			},
			"required": []string{"tools", "total"},
		},
	}
}

func defineToolDef() *mcp.Tool {
	return &mcp.Tool{
		Name:        DefineTool,
		Title:       "Get tool definition",
		Annotations: gatewayHints(true),
		Description: "Get the full definitions (description and input schema) of tools you already know by name, for example from list_tools. Exact lookup, no ranking.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"names": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string"}, "description": "Tool names as shown by list_tools."},
			},
			"required":             []string{"names"},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tools":    toolList(),
				"notFound": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"hint":     map[string]any{"type": "string"},
			},
			"required": []string{"tools"},
		},
	}
}

func executeToolDef() *mcp.Tool {
	props := map[string]any{
		"name":      map[string]any{"type": "string", "description": "Tool name exactly as returned by tool_search or list_tools."},
		"arguments": map[string]any{"type": "object", "description": "Arguments matching the tool's input schema.", "additionalProperties": true},
	}
	return &mcp.Tool{
		Name:        ExecuteTool,
		Title:       "Execute tool",
		Annotations: gatewayHints(false),
		Description: "Execute a tool found with tool_search. The result is the tool's own result, passed through unchanged.",
		InputSchema: map[string]any{"type": "object", "properties": props, "required": []string{"name"}, "additionalProperties": false},
	}
}

// ---- tools/call in search mode ---------------------------------------------

func isSearchTool(name string) bool {
	return name == SearchTool || name == ListTool || name == ExecuteTool || name == DefineTool
}

func (g *Gateway) searchCall(ctx context.Context, caller *auth.Caller, route, slug string, p *mcp.CallToolParamsRaw) (mcp.Result, error) {
	switch p.Name {
	case ExecuteTool:
		return g.executeTool(ctx, caller, route, slug, p.Arguments)
	case ListTool:
		var in struct {
			Server    string `json:"server"`
			Cursor    string `json:"cursor"`
			Summaries bool   `json:"summaries"`
		}
		if err := decodeArgs(p.Arguments, &in); err != nil {
			return nil, err
		}
		entries, err := g.catalog(ctx, caller, route, slug, false)
		if err != nil {
			return nil, err
		}
		return listToolsResult(entries, route, in.Server, in.Cursor, in.Summaries), nil
	case DefineTool:
		var in struct {
			Names []string `json:"names"`
		}
		if err := decodeArgs(p.Arguments, &in); err != nil {
			return nil, err
		}
		if len(in.Names) == 0 {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "names is required"}
		}
		entries, err := g.catalog(ctx, caller, route, slug, false)
		if err != nil {
			return nil, err
		}
		return defineToolsResult(entries, route, in.Names), nil
	case SearchTool:
		var in struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := decodeArgs(p.Arguments, &in); err != nil {
			return nil, err
		}
		entries, err := g.catalog(ctx, caller, route, slug, false)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(in.Query) == "" {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "query is required"}
		}
		res, n := g.toolSearchResult(entries, route, in.Query, in.Limit)
		if n == 0 {
			g.log.Info("tool_search miss", "query", in.Query, "route", route)
		}
		return res, nil
	}
	return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "unknown tool " + p.Name}
}

func decodeArgs(raw json.RawMessage, into any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid arguments: " + err.Error()}
	}
	return nil
}

func listToolsResult(entries []catalogEntry, route, server, cursor string, summaries bool) *mcp.CallToolResult {
	type row struct {
		Namespace string `json:"server"`
		Name      string `json:"name"` // the callable name (slug__tool on the mux route)
		Summary   string `json:"summary,omitempty"`
	}
	var rows []row
	// Slugs are lowercase a-z0-9; accept any casing (and a server's display
	// name) so "Obsidian" or "Web Search" find the right server.
	server = strings.ToLower(strings.TrimSpace(server))
	for _, e := range entries {
		if server != "" && e.srv.Slug != server && strings.ToLower(e.srv.Name) != server {
			continue
		}
		for _, t := range e.tools {
			rows = append(rows, row{Namespace: e.srv.Slug, Name: toolName(route, e.srv.Slug, t.Name), Summary: oneLine(firstLine(t.Description), 160)})
		}
	}
	offset := 0
	if cursor != "" {
		if b, err := base64.RawURLEncoding.DecodeString(cursor); err == nil {
			offset, _ = strconv.Atoi(string(b))
		}
	}
	if offset < 0 || offset > len(rows) {
		offset = len(rows)
	}
	end := offset + ListPageSize
	if end > len(rows) {
		end = len(rows)
	}
	page := rows[offset:end]
	if !summaries {
		for i := range page {
			page[i].Summary = ""
		}
	}
	if page == nil {
		page = []row{}
	}
	out := map[string]any{"tools": page, "total": len(rows)}
	if end < len(rows) {
		out["nextCursor"] = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(end)))
	}
	return structured(out, false)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// toolSearchResult ranks the catalogue for query and returns the matching
// tools with full schemas, named as the model must call them.
func (g *Gateway) toolSearchResult(entries []catalogEntry, route, query string, limit int) (*mcp.CallToolResult, int) {
	if limit <= 0 {
		limit = 10
	}
	var docs []toolsearch.Doc
	for _, e := range entries {
		for _, t := range e.tools {
			docs = append(docs, toolsearch.Doc{Namespace: e.srv.Slug, ServerName: e.srv.Name, ServerDesc: e.srv.Description, Instructions: e.init.Instructions, Tool: t})
		}
	}
	hits := toolsearch.Build(docs).Search(query, limit)
	type result struct {
		Server string    `json:"server"`
		Tool   *mcp.Tool `json:"tool"`
	}
	out := make([]result, 0, len(hits))
	for _, h := range hits {
		c := *h.Doc.Tool
		c.Name = toolName(route, h.Doc.Namespace, h.Doc.Tool.Name)
		out = append(out, result{Server: h.Doc.Namespace, Tool: &c})
	}
	res := map[string]any{"tools": out}
	if len(out) == 0 {
		res["hint"] = "No tools matched. Connected servers:\n" + roster(entries)
	}
	return structured(res, false), len(out)
}

// defineToolsResult looks tools up by their callable name (case-insensitive)
// and returns the full definitions, naming any it could not find.
func defineToolsResult(entries []catalogEntry, route string, names []string) *mcp.CallToolResult {
	type result struct {
		Server string    `json:"server"`
		Tool   *mcp.Tool `json:"tool"`
	}
	found := []result{}
	var missing []string
	for _, want := range names {
		var hit *result
		for _, e := range entries {
			for _, t := range e.tools {
				if strings.EqualFold(toolName(route, e.srv.Slug, t.Name), strings.TrimSpace(want)) {
					c := *t
					c.Name = toolName(route, e.srv.Slug, t.Name)
					hit = &result{Server: e.srv.Slug, Tool: &c}
					break
				}
			}
			if hit != nil {
				break
			}
		}
		if hit == nil {
			missing = append(missing, want)
			continue
		}
		found = append(found, *hit)
	}
	res := map[string]any{"tools": found}
	if len(missing) > 0 {
		res["notFound"] = missing
		res["hint"] = "Use list_tools or tool_search to find the right name."
	}
	return structured(res, len(found) == 0)
}

// structured builds a tool result whose text block is the serialized JSON of
// its structured content, as the spec asks of tools that return structured
// content, so text-only clients see the same data.
func structured(v map[string]any, isError bool) *mcp.CallToolResult {
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte(`{"error":"` + err.Error() + `"}`)
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(b)}},
		StructuredContent: v,
		IsError:           isError,
	}
}

// toolName is the callable name for a tool on a route: slug__tool on the mux,
// the bare name on a direct route.
func toolName(route, slug, name string) string {
	if route == "mux" {
		return slug + Separator + name
	}
	return name
}

func (g *Gateway) executeTool(ctx context.Context, caller *auth.Caller, route, slug string, raw json.RawMessage) (mcp.Result, error) {
	var in struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return nil, err
	}
	if in.Name == "" {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "name is required"}
	}
	args := in.Arguments
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	// Same name form as the flat route, so callTool does the slug split.
	return g.callTool(ctx, caller, route, slug, &mcp.CallToolParamsRaw{Name: in.Name, Arguments: args})
}

// gatewayHints annotates a gateway-owned tool: read-only ones read the
// catalogue, execute_tool inherits whatever the target does, so it stays at
// the spec defaults except for the closed world.
func gatewayHints(readOnly bool) *mcp.ToolAnnotations {
	f := false
	if readOnly {
		return &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &f, IdempotentHint: true, OpenWorldHint: &f}
	}
	return &mcp.ToolAnnotations{OpenWorldHint: &f}
}

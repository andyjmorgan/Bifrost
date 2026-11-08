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

	"github.com/andyjmorgan/mcp-gateway/internal/audit"
	"github.com/andyjmorgan/mcp-gateway/internal/auth"
	"github.com/andyjmorgan/mcp-gateway/internal/cache"
	"github.com/andyjmorgan/mcp-gateway/internal/store"
	"github.com/andyjmorgan/mcp-gateway/internal/toolsearch"
	"github.com/andyjmorgan/mcp-gateway/internal/upstream"
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
	// ListPageSize is the page size of list_tools.
	ListPageSize = 200
)

type searchKey struct{}

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
	tools []*mcp.Tool
	err   error
}

// catalog gathers every enabled server on the route with its tools, from
// cache where live, fetching (and caching) the misses concurrently. With
// force, every server is asked afresh. Unavailable servers are recorded in
// the audit (issue + skip event) and returned with err set.
func (g *Gateway) catalog(ctx context.Context, caller *auth.Caller, route, slug string, force bool) ([]catalogEntry, error) {
	var servers []*store.Server
	var cached map[uuid.UUID]store.CacheBundle
	if route == "direct" {
		srv, bundle, err := g.store.GetServerBySlugWithCache(ctx, caller.OwnerID, slug, cache.NSTools, cache.NSInit)
		if err != nil {
			return nil, &jsonrpc.Error{Code: -32004, Message: "unknown server"}
		}
		servers = []*store.Server{srv}
		cached = map[uuid.UUID]store.CacheBundle{srv.ID: bundle}
	} else {
		var err error
		servers, cached, err = g.store.ListServersWithCache(ctx, caller.OwnerID, cache.NSTools, cache.NSInit)
		if err != nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
		}
	}
	var (
		mu      sync.Mutex
		entries []catalogEntry
		wg      sync.WaitGroup
	)
	for _, srv := range servers {
		if !srv.Enabled && route != "direct" {
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
			if !force {
				e.tools, hit = decodeTools(bundle[cache.NSTools])
			}
			if !hit {
				hint := ""
				if !force {
					hint = e.init.ProtocolVersion
				}
				if e.tools, e.err = g.toolsOf(ctx, srv, hint); e.err == nil {
					g.cacheTools(srv, e.tools)
				}
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
	tools := []*mcp.Tool{searchToolDef(entries, route), listToolDef(route), executeToolDef(route)}
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
	desc := "Find tools behind this gateway and load their full definitions. " +
		"Pass `paths` to load every tool of one or more namespaces, `names` to load specific tools you saw in list_tools, " +
		"or `query` to rank tools by relevance (BM25 over names, descriptions and input schemas). Then call them with execute_tool.\n\n" +
		"Connected namespaces:\n" + roster(entries)
	if route == "direct" {
		desc = "Find tools on this server and load their full definitions. Pass `names` for specific tools or `query` to rank by relevance. Then call them with execute_tool.\n\n" + roster(entries)
	}
	return &mcp.Tool{
		Name:        SearchTool,
		Description: desc,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Namespaces (server slugs) whose tools to load in full."},
				"names": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Specific tool names to load, as `namespace/name`, `namespace__name` or a bare name."},
				"query": map[string]any{"type": "string", "description": "Free-text search over names, descriptions and schemas."},
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 10, "description": "Maximum query results."},
			},
			"additionalProperties": false,
		},
	}
}

func listToolDef(route string) *mcp.Tool {
	props := map[string]any{
		"cursor":    map[string]any{"type": "string", "description": "Opaque cursor from a previous page."},
		"summaries": map[string]any{"type": "boolean", "default": false, "description": "Include a one-line summary per tool (costs ~5x the tokens of names alone)."},
	}
	desc := fmt.Sprintf("List tool names grouped by namespace (%d per page) without loading schemas. Use tool_search with `names` to load the ones you need.", ListPageSize)
	if route == "mux" {
		props["path"] = map[string]any{"type": "string", "description": "Restrict to one namespace (server slug)."}
	}
	return &mcp.Tool{
		Name:        ListTool,
		Description: desc,
		InputSchema: map[string]any{"type": "object", "properties": props, "additionalProperties": false},
	}
}

func executeToolDef(route string) *mcp.Tool {
	props := map[string]any{
		"name":      map[string]any{"type": "string", "description": "Tool name as returned by tool_search."},
		"arguments": map[string]any{"type": "object", "description": "Arguments matching the tool's input schema.", "additionalProperties": true},
	}
	if route == "mux" {
		props["namespace"] = map[string]any{"type": "string", "description": "Namespace (server slug) the tool belongs to. Optional when `name` is already `namespace__name`."}
	}
	return &mcp.Tool{
		Name:        ExecuteTool,
		Description: "Execute a tool found with tool_search. The result is the tool's own result, passed through unchanged.",
		InputSchema: map[string]any{"type": "object", "properties": props, "required": []string{"name"}, "additionalProperties": false},
	}
}

// ---- tools/call in search mode ---------------------------------------------

func isSearchTool(name string) bool {
	return name == SearchTool || name == ListTool || name == ExecuteTool
}

func (g *Gateway) searchCall(ctx context.Context, caller *auth.Caller, route, slug string, p *mcp.CallToolParamsRaw) (mcp.Result, error) {
	switch p.Name {
	case ExecuteTool:
		return g.executeTool(ctx, caller, route, slug, p.Arguments)
	case ListTool:
		var in struct {
			Path      string `json:"path"`
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
		return listToolsResult(entries, route, in.Path, in.Cursor, in.Summaries), nil
	case SearchTool:
		var in struct {
			Paths []string `json:"paths"`
			Names []string `json:"names"`
			Query string   `json:"query"`
			Limit int      `json:"limit"`
		}
		if err := decodeArgs(p.Arguments, &in); err != nil {
			return nil, err
		}
		entries, err := g.catalog(ctx, caller, route, slug, false)
		if err != nil {
			return nil, err
		}
		res := g.toolSearchResult(entries, in.Paths, in.Names, in.Query, in.Limit)
		if in.Query != "" && len(res.StructuredContent.(map[string]any)["namespaces"].([]map[string]any)) == 0 {
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

func listToolsResult(entries []catalogEntry, route, path, cursor string, summaries bool) *mcp.CallToolResult {
	type row struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		Summary   string `json:"summary,omitempty"`
	}
	var rows []row
	for _, e := range entries {
		if path != "" && e.srv.Slug != path {
			continue
		}
		for _, t := range e.tools {
			rows = append(rows, row{Namespace: e.srv.Slug, Name: t.Name, Summary: oneLine(firstLine(t.Description), 160)})
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
	structured := map[string]any{"tools": page, "total": len(rows)}
	// Text form: the namespace once as a header, bare names beneath. Cheaper
	// than repeating the prefix per line and unambiguous to reassemble.
	var sb strings.Builder
	lastNS := ""
	for _, r := range page {
		if r.Namespace != lastNS {
			fmt.Fprintf(&sb, "%s:\n", r.Namespace)
			lastNS = r.Namespace
		}
		sb.WriteString("  " + r.Name)
		if r.Summary != "" {
			sb.WriteString(" — " + r.Summary)
		}
		sb.WriteByte('\n')
	}
	if end < len(rows) {
		next := base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(end)))
		structured["nextCursor"] = next
		fmt.Fprintf(&sb, "\n%d more; pass cursor %q for the next page.", len(rows)-end, next)
	}
	if len(page) == 0 {
		sb.WriteString("No tools.")
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: sb.String()}}, StructuredContent: structured}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// toolSearchResult resolves paths, names and query against the catalogue and
// returns full definitions grouped by namespace.
func (g *Gateway) toolSearchResult(entries []catalogEntry, paths, names []string, query string, limit int) *mcp.CallToolResult {
	if limit <= 0 {
		limit = 10
	}
	byNS := map[string]*catalogEntry{}
	for i := range entries {
		byNS[entries[i].srv.Slug] = &entries[i]
	}
	// picked[ns][tool] preserves insertion order per namespace via a slice.
	type pick struct {
		ns    string
		tools []*mcp.Tool
		seen  map[string]bool
	}
	picks := map[string]*pick{}
	order := []string{}
	add := func(ns string, t *mcp.Tool) {
		p := picks[ns]
		if p == nil {
			p = &pick{ns: ns, seen: map[string]bool{}}
			picks[ns] = p
			order = append(order, ns)
		}
		if !p.seen[t.Name] {
			p.seen[t.Name] = true
			p.tools = append(p.tools, t)
		}
	}
	for _, ns := range paths {
		if e := byNS[ns]; e != nil {
			for _, t := range e.tools {
				add(ns, t)
			}
		}
	}
	for _, n := range names {
		ns, name := splitToolRef(n)
		for _, e := range entries {
			if ns != "" && e.srv.Slug != ns {
				continue
			}
			for _, t := range e.tools {
				if t.Name == name {
					add(e.srv.Slug, t)
				}
			}
		}
	}
	if query != "" {
		var docs []toolsearch.Doc
		for _, e := range entries {
			for _, t := range e.tools {
				docs = append(docs, toolsearch.Doc{Namespace: e.srv.Slug, ServerName: e.srv.Name, ServerDesc: e.srv.Description, Instructions: e.init.Instructions, Tool: t})
			}
		}
		for _, h := range toolsearch.Build(docs).Search(query, limit) {
			add(h.Doc.Namespace, h.Doc.Tool)
		}
	}
	groups := make([]map[string]any, 0, len(order))
	var sb strings.Builder
	for _, ns := range order {
		e := byNS[ns]
		p := picks[ns]
		groups = append(groups, map[string]any{
			"namespace":   ns,
			"name":        e.srv.Name,
			"description": e.srv.Description,
			"tools":       p.tools,
		})
		for _, t := range p.tools {
			fmt.Fprintf(&sb, "%s/%s — %s\n", ns, t.Name, oneLine(firstLine(t.Description), 160))
		}
	}
	if len(groups) == 0 {
		sb.WriteString("No tools matched. Namespaces available:\n" + roster(entries))
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: sb.String()}},
		StructuredContent: map[string]any{"namespaces": groups},
	}
}

// splitToolRef accepts "ns/name", "ns__name" or "name".
func splitToolRef(s string) (ns, name string) {
	if i := strings.IndexByte(s, '/'); i > 0 {
		return s[:i], s[i+1:]
	}
	if ns, name, ok := strings.Cut(s, Separator); ok && ns != "" && name != "" {
		return ns, name
	}
	return "", s
}

func (g *Gateway) executeTool(ctx context.Context, caller *auth.Caller, route, slug string, raw json.RawMessage) (mcp.Result, error) {
	var in struct {
		Namespace string          `json:"namespace"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return nil, err
	}
	if in.Name == "" {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "name is required"}
	}
	ns, name := splitToolRef(in.Name)
	if in.Namespace != "" {
		ns = in.Namespace
	}
	full := name
	if route == "mux" {
		if ns == "" {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "namespace is required on the mux route (or pass name as namespace__tool)"}
		}
		full = ns + Separator + name
	} else if ns != "" && ns != slug {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "namespace does not match this route"}
	}
	args := in.Arguments
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	return g.callTool(ctx, caller, route, slug, &mcp.CallToolParamsRaw{Name: full, Arguments: args})
}

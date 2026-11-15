package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/internal/toolsearch"
)

// exposedCatalog is every exposed tool on the route, after filter and
// modify, ordered slug a-z then name a-z. Search mode reads only this, so it
// can never expose a tool the flat list would not.
func (e *Engine) exposedCatalog(ctx context.Context, r Request) ([]toolsearch.Doc, []Hit) {
	var docs []toolsearch.Doc
	var hits []Hit
	for _, s := range sortedServers(r.Servers) {
		var o Outcome
		if !e.config(ctx, r, s, "tools", &o) {
			hits = append(hits, o.Hits...)
			continue
		}
		hits = append(hits, o.Hits...)
		instr, title, desc := "", s.Info.Name, s.Info.Description
		if sw, _ := e.Config(ctx, r, s); sw["searchRoster"] {
			if meta, _, err := e.Modify(ctx, r, s); err == nil {
				if sw["instructions"] {
					instr = meta.Instructions
				}
				if meta.Title != "" {
					title = meta.Title
				}
				desc = meta.Description
			}
		} else {
			title, desc = "", ""
		}
		tl := s.Rules.ListTools(ctx, s.Catalog.Tools, r.context(s), e.Limits.Workers)
		hits = append(hits, tl.Hits...)
		for _, et := range tl.Tools {
			docs = append(docs, toolsearch.Doc{Namespace: s.Slug, ServerName: title, ServerDesc: desc, Instructions: instr, Tool: et.Tool})
		}
	}
	return docs, hits
}

// ToolSearch ranks exposed tools with BM25.
func (e *Engine) ToolSearch(parent context.Context, r Request, query string, limit int) Outcome {
	ctx, cancel := e.ctx(parent)
	defer cancel()
	docs, hits := e.exposedCatalog(ctx, r)
	if limit <= 0 {
		limit = 10
	}
	var out []map[string]any
	for _, h := range toolsearch.Build(docs).Search(query, limit) {
		out = append(out, map[string]any{"name": exposedName(r, h.Doc.Namespace, h.Doc.Tool.Name), "description": h.Doc.Tool.Description, "score": h.Score})
	}
	if out == nil {
		out = []map[string]any{}
	}
	return e.finishOutcome(ctx, Outcome{Result: map[string]any{"tools": out}, Hits: hits})
}

// ListToolsYAML renders exposed tools grouped by slug, names indented, in a
// YAML-style text. Names that are not plain identifiers are quoted, so an
// upstream name or slug cannot inject structure.
func (e *Engine) ListToolsYAML(parent context.Context, r Request) Outcome {
	ctx, cancel := e.ctx(parent)
	defer cancel()
	docs, hits := e.exposedCatalog(ctx, r)
	var b strings.Builder
	last := "\x00"
	for _, d := range docs {
		if d.Namespace != last {
			b.WriteString(yamlScalar(d.Namespace) + ":\n")
			last = d.Namespace
		}
		b.WriteString("  - " + yamlScalar(d.Tool.Name) + "\n")
	}
	return e.finishOutcome(ctx, Outcome{Result: b.String(), Hits: hits})
}

func yamlScalar(s string) string {
	plain := s != ""
	for i, c := range s {
		ok := c == '_' || c == '-' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || (c == '.' && i > 0)
		if !ok {
			plain = false
			break
		}
	}
	switch strings.ToLower(s) {
	case "true", "false", "null", "yes", "no", "on", "off", "~":
		plain = false
	}
	if plain {
		return s
	}
	return strconv.Quote(s)
}

// ToolDefinitions returns exposed definitions for exact names (slug__tool).
func (e *Engine) ToolDefinitions(parent context.Context, r Request, names []string) Outcome {
	ctx, cancel := e.ctx(parent)
	defer cancel()
	docs, hits := e.exposedCatalog(ctx, r)
	byName := map[string]*mcp.Tool{}
	for _, d := range docs {
		byName[exposedName(r, d.Namespace, d.Tool.Name)] = d.Tool
	}
	found := []any{}
	missing := []string{}
	for _, n := range names {
		if t, ok := byName[n]; ok { // exact: names are case-sensitive
			c := *t
			c.Name = n
			found = append(found, &c)
		} else {
			missing = append(missing, n)
		}
	}
	return e.finishOutcome(ctx, Outcome{Result: map[string]any{"tools": found, "unknown": missing}, Hits: hits})
}

// ExecuteTool is search mode's execute_tool: exactly a tools/call.
func (e *Engine) ExecuteTool(parent context.Context, r Request, name string, args json.RawMessage, p *mcp.CallToolParamsRaw) Outcome {
	call := &mcp.CallToolParamsRaw{Name: name, Arguments: args}
	if p != nil {
		call.InputResponses, call.RequestState = p.InputResponses, p.RequestState
	}
	return e.CallTool(parent, r, call)
}

var _ = fmt.Sprint

// exposedName is the one formatter for tool names a client sees: prefixed
// with the slug on the mux, bare on 1:1.
func exposedName(r Request, slug, name string) string {
	if r.mux() {
		return slug + Separator + name
	}
	return name
}

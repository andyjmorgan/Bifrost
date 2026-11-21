package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/internal/audit"
	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/cache"
	"github.com/andyjmorgan/Bifrost/internal/store"
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
// catalog lists every server the caller holds (one on direct) with its
// tools cut to its selection.
func (g *Gateway) catalog(ctx context.Context, caller *auth.Caller, route, slug string, force bool) ([]catalogEntry, error) {
	var servers []*store.Server
	var cached map[uuid.UUID]store.CacheBundle
	if route == "direct" {
		srv, bundle, err := g.grantedServer(ctx, caller, slug, cache.NSTools, cache.NSInit)
		if err != nil {
			return nil, &jsonrpc.Error{Code: -32004, Message: "unknown server"}
		}
		servers = []*store.Server{srv}
		cached = map[uuid.UUID]store.CacheBundle{srv.ID: bundle}
	} else {
		var err error
		servers, cached, err = g.granted(ctx, caller, cache.NSTools, cache.NSInit)
		if err != nil {
			return nil, rpcErr(err)
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
				cut := g.cutterFor(ctx, srv, e.init)
				if cut.broken != nil {
					e.err = fmt.Errorf("rules do not compile: %w", cut.broken)
				} else {
					e.tools = cut.tools(e.tools)
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

// ---- tools/list in search mode ---------------------------------------------

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// ---- tools/call in search mode ---------------------------------------------

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

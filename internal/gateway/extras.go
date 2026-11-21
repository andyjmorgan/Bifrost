package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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

// Prompts and resources ride the same machinery as tools: per-server lists
// cached under their own namespaces, singleflighted refills, and a fan-out
// across servers. This file fetches and caches those lists. What a caller
// sees of them is decided in hooks.go, by the rules engine.

// MetaKeyServer marks which server a merged resource or template came from.
const MetaKeyServer = "ai.andrewmorgan.bifrost/server"

// listing is what one upstream returned for a namespace, as JSON.
type listing struct {
	srv  *store.Server
	init cache.Init
	raw  json.RawMessage
	err  error
}

// fetchList returns the cached list for (srv, ns) or fetches it with fetch,
// which pages through the upstream and returns the JSON array. Concurrent
// misses share one fetch; the write is guarded by the server's updated_at.
func (g *Gateway) fetchList(ctx context.Context, srv *store.Server, ns, protocolVersion string, force bool, fetch func(*upstream.Session) (json.RawMessage, error)) (json.RawMessage, error) {
	key := srv.ID.String() + "|" + ns
	if force {
		key += "!"
	}
	ch := g.flights.DoChan(key, func() (any, error) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), g.conn.Timeout())
		defer cancel()
		if !force {
			if raw, err := g.store.CacheGet(fctx, ns, srv.ID.String()); err == nil && len(raw) > 0 {
				return raw, nil
			}
		}
		sess, err := g.conn.OpenWith(fctx, srv, protocolVersion)
		if err != nil {
			return nil, err
		}
		defer sess.Close()
		if !declares(sess.InitializeResult(), ns) {
			return json.RawMessage("[]"), nil
		}
		raw, err := fetch(sess)
		if err != nil {
			// A server without the surface answers "method not found": that is
			// an empty list, not an outage.
			var je *jsonrpc.Error
			if errors.As(err, &je) && je.Code == jsonrpc.CodeMethodNotFound {
				raw = json.RawMessage("[]")
			} else {
				return nil, err
			}
		}
		g.observeRaw(fctx, srv, ns, raw)
		if g.toolsTTL > 0 {
			id := srv.ID
			switch err := g.store.CacheSet(fctx, ns, id.String(), &id, srv.UpdatedAt, raw, g.toolsTTL); {
			case errors.Is(err, store.ErrStale):
				g.log.Info("cache write skipped: server changed during fetch", "slug", srv.Slug, "ns", ns)
			case err != nil:
				g.log.Warn("cache write failed", "slug", srv.Slug, "ns", ns, "err", err)
			}
		}
		return raw, nil
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.(json.RawMessage), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// declares reports whether an initialize result advertises the surface a
// namespace is cached under.
func declares(ir *mcp.InitializeResult, ns string) bool {
	if ir == nil || ir.Capabilities == nil {
		return true
	}
	switch ns {
	case cache.NSPrompts:
		return ir.Capabilities.Prompts != nil
	case cache.NSResources, cache.NSTemplates:
		return ir.Capabilities.Resources != nil
	}
	return true
}

// pagedFetchers return the JSON array for a namespace by paging the upstream.
func fetchPrompts(sess *upstream.Session) (json.RawMessage, error) {
	var out []*mcp.Prompt
	cursor := ""
	for {
		res, err := sess.ListPrompts(sess.Ctx, &mcp.ListPromptsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		out = append(out, res.Prompts...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	if out == nil {
		out = []*mcp.Prompt{}
	}
	return json.Marshal(out)
}

func fetchResources(sess *upstream.Session) (json.RawMessage, error) {
	var out []*mcp.Resource
	cursor := ""
	for {
		res, err := sess.ListResources(sess.Ctx, &mcp.ListResourcesParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		out = append(out, res.Resources...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	if out == nil {
		out = []*mcp.Resource{}
	}
	return json.Marshal(out)
}

func fetchTemplates(sess *upstream.Session) (json.RawMessage, error) {
	var out []*mcp.ResourceTemplate
	cursor := ""
	for {
		res, err := sess.ListResourceTemplates(sess.Ctx, &mcp.ListResourceTemplatesParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		out = append(out, res.ResourceTemplates...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	if out == nil {
		out = []*mcp.ResourceTemplate{}
	}
	return json.Marshal(out)
}

// listings fetches one namespace for every server on the route (one on
// direct), cached where possible, concurrently otherwise.
func (g *Gateway) listings(ctx context.Context, caller *auth.Caller, route, slug, ns string, force bool, fetch func(*upstream.Session) (json.RawMessage, error)) ([]listing, error) {
	var servers []*store.Server
	var cached map[uuid.UUID]store.CacheBundle
	if route == "direct" {
		srv, bundle, err := g.grantedServer(ctx, caller, slug, ns, cache.NSInit)
		if err != nil {
			return nil, &jsonrpc.Error{Code: -32004, Message: "unknown server"}
		}
		servers = []*store.Server{srv}
		cached = map[uuid.UUID]store.CacheBundle{srv.ID: bundle}
	} else {
		var err error
		servers, cached, err = g.granted(ctx, caller, ns, cache.NSInit)
		if err != nil {
			return nil, rpcErr(err)
		}
	}
	var (
		mu  sync.Mutex
		out []listing
		wg  sync.WaitGroup
	)
	for _, srv := range servers {
		if !srv.Enabled && route != "direct" {
			continue
		}
		wg.Add(1)
		go func(srv *store.Server) {
			defer wg.Done()
			l := listing{srv: srv}
			bundle := cached[srv.ID]
			if raw := bundle[cache.NSInit]; raw != nil {
				_ = json.Unmarshal(raw, &l.init)
			}
			switch {
			case bundle[cache.NSInit] != nil && !l.init.Has(ns):
				// The server declared no such surface: nothing to ask.
				l.raw = json.RawMessage("[]")
			case bundle[ns] != nil && !force:
				l.raw = bundle[ns]
			default:
				l.raw, l.err = g.fetchList(ctx, srv, ns, l.init.ProtocolVersion, force, fetch)
			}
			if l.err == nil {
				cut := g.cutterFor(ctx, srv, l.init)
				if cut.broken != nil {
					l.err = fmt.Errorf("rules do not compile: %w", cut.broken)
				} else {
					l.raw = cut.raw(ns, l.raw)
				}
			}
			if l.err != nil {
				g.log.Warn("listing: server unavailable", "slug", srv.Slug, "ns", ns, "err", l.err)
				audit.AddIssue(ctx, srv.Slug+": "+l.err.Error())
				if g.rec != nil {
					g.rec.RecordSkipped(ctx, srv, route, ns+"/list", "upstream", l.err.Error())
				}
			}
			mu.Lock()
			out = append(out, l)
			mu.Unlock()
		}(srv)
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].srv.Slug < out[j].srv.Slug })
	return out, nil
}

// ---- prompts ----------------------------------------------------------------

// ---- resources --------------------------------------------------------------

// ---- helpers ----------------------------------------------------------------

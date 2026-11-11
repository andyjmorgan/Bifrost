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

// Prompts and resources ride the same machinery as tools: per-server lists
// cached under their own namespaces, singleflighted refills, fan-out on the
// mux route with unavailable servers reported in _meta. Policies do not
// apply to them yet. On the mux route prompt names are prefixed slug__name
// like tools; resource URIs are left alone (a URI is the resource's
// identity) and resources/read finds the owning server from the cached lists.

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
		srv, bundle, err := g.store.GetServerBySlugWithCache(ctx, caller.OwnerID, slug, ns, cache.NSInit)
		if err != nil {
			return nil, &jsonrpc.Error{Code: -32004, Message: "unknown server"}
		}
		servers = []*store.Server{srv}
		cached = map[uuid.UUID]store.CacheBundle{srv.ID: bundle}
	} else {
		var err error
		servers, cached, err = g.store.ListServersWithCache(ctx, caller.OwnerID, ns, cache.NSInit)
		if err != nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
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

func unavailableListings(ls []listing) []map[string]string {
	var out []map[string]string
	for _, l := range ls {
		if l.err != nil {
			out = append(out, map[string]string{"server": l.srv.Slug, "error": oneLine(l.err.Error(), 200)})
		}
	}
	return out
}

func withUnavailable(meta *mcp.Meta, ls []listing) {
	if u := unavailableListings(ls); len(u) > 0 {
		if *meta == nil {
			*meta = mcp.Meta{}
		}
		(*meta)[MetaKeyUnavailable] = u
	}
}

// ---- prompts ----------------------------------------------------------------

func (g *Gateway) listPrompts(ctx context.Context, caller *auth.Caller, route, slug string) (mcp.Result, error) {
	ls, err := g.listings(ctx, caller, route, slug, cache.NSPrompts, false, fetchPrompts)
	if err != nil {
		return nil, err
	}
	out := &mcp.ListPromptsResult{Prompts: []*mcp.Prompt{}}
	for _, l := range ls {
		if l.err != nil {
			if route == "direct" {
				return nil, upstreamError(l.srv, l.err)
			}
			continue
		}
		var ps []*mcp.Prompt
		if err := json.Unmarshal(l.raw, &ps); err != nil {
			continue
		}
		for _, p := range ps {
			if route == "mux" {
				c := *p
				c.Name = l.srv.Slug + Separator + p.Name
				p = &c
			}
			out.Prompts = append(out.Prompts, p)
		}
	}
	withUnavailable(&out.Meta, ls)
	return out, nil
}

func (g *Gateway) getPrompt(ctx context.Context, caller *auth.Caller, route, slug string, p *mcp.GetPromptParams) (mcp.Result, error) {
	name := p.Name
	if route == "mux" {
		var ok bool
		slug, name, ok = strings.Cut(p.Name, Separator)
		if !ok || slug == "" || name == "" {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("prompt name must be <slug>%s<prompt>", Separator)}
		}
	}
	srv, cached, err := g.serverFor(ctx, caller, strings.ToLower(slug))
	if err != nil {
		return nil, err
	}
	sess, err := g.conn.OpenWith(ctx, srv, initHint(cached))
	if err != nil {
		return nil, upstreamError(srv, err)
	}
	defer sess.Close()
	res, err := sess.GetPrompt(sess.Ctx, &mcp.GetPromptParams{Name: name, Arguments: p.Arguments, Meta: forwardMeta(p.Meta)})
	if err != nil {
		return nil, upstreamError(srv, err)
	}
	return res, nil
}

// ---- resources --------------------------------------------------------------

func (g *Gateway) listResources(ctx context.Context, caller *auth.Caller, route, slug string) (mcp.Result, error) {
	ls, err := g.listings(ctx, caller, route, slug, cache.NSResources, false, fetchResources)
	if err != nil {
		return nil, err
	}
	out := &mcp.ListResourcesResult{Resources: []*mcp.Resource{}}
	for _, l := range ls {
		if l.err != nil {
			if route == "direct" {
				return nil, upstreamError(l.srv, l.err)
			}
			continue
		}
		var rs []*mcp.Resource
		if err := json.Unmarshal(l.raw, &rs); err != nil {
			continue
		}
		for _, r := range rs {
			if route == "mux" {
				c := *r
				c.Meta = tagged(r.Meta, l.srv.Slug)
				r = &c
			}
			out.Resources = append(out.Resources, r)
		}
	}
	withUnavailable(&out.Meta, ls)
	return out, nil
}

func (g *Gateway) listResourceTemplates(ctx context.Context, caller *auth.Caller, route, slug string) (mcp.Result, error) {
	ls, err := g.listings(ctx, caller, route, slug, cache.NSTemplates, false, fetchTemplates)
	if err != nil {
		return nil, err
	}
	out := &mcp.ListResourceTemplatesResult{ResourceTemplates: []*mcp.ResourceTemplate{}}
	for _, l := range ls {
		if l.err != nil {
			if route == "direct" {
				return nil, upstreamError(l.srv, l.err)
			}
			continue
		}
		var ts []*mcp.ResourceTemplate
		if err := json.Unmarshal(l.raw, &ts); err != nil {
			continue
		}
		for _, t := range ts {
			if route == "mux" {
				c := *t
				c.Meta = tagged(t.Meta, l.srv.Slug)
				t = &c
			}
			out.ResourceTemplates = append(out.ResourceTemplates, t)
		}
	}
	withUnavailable(&out.Meta, ls)
	return out, nil
}

func (g *Gateway) readResource(ctx context.Context, caller *auth.Caller, route, slug string, p *mcp.ReadResourceParams) (mcp.Result, error) {
	if route == "mux" {
		owner, err := g.resourceOwner(ctx, caller, p.URI)
		if err != nil {
			return nil, err
		}
		slug = owner
	}
	srv, cached, err := g.serverFor(ctx, caller, strings.ToLower(slug))
	if err != nil {
		return nil, err
	}
	sess, err := g.conn.OpenWith(ctx, srv, initHint(cached))
	if err != nil {
		return nil, upstreamError(srv, err)
	}
	defer sess.Close()
	res, err := sess.ReadResource(sess.Ctx, &mcp.ReadResourceParams{URI: p.URI, Meta: forwardMeta(p.Meta)})
	if err != nil {
		return nil, upstreamError(srv, err)
	}
	return res, nil
}

// resourceOwner finds which server serves uri on the mux route: an exact
// match in the cached resource lists, then a template whose literal prefix
// (up to the first "{") matches, then the same again after a refresh.
func (g *Gateway) resourceOwner(ctx context.Context, caller *auth.Caller, uri string) (string, error) {
	for _, force := range []bool{false, true} {
		rs, err := g.listings(ctx, caller, "mux", "", cache.NSResources, force, fetchResources)
		if err != nil {
			return "", err
		}
		for _, l := range rs {
			if l.err != nil {
				continue
			}
			var items []*mcp.Resource
			if json.Unmarshal(l.raw, &items) == nil {
				for _, r := range items {
					if r.URI == uri {
						return l.srv.Slug, nil
					}
				}
			}
		}
		ts, err := g.listings(ctx, caller, "mux", "", cache.NSTemplates, force, fetchTemplates)
		if err != nil {
			return "", err
		}
		best, bestLen := "", -1
		for _, l := range ts {
			if l.err != nil {
				continue
			}
			var items []*mcp.ResourceTemplate
			if json.Unmarshal(l.raw, &items) == nil {
				for _, t := range items {
					prefix := t.URITemplate
					if i := strings.IndexByte(prefix, '{'); i >= 0 {
						prefix = prefix[:i]
					}
					if prefix != "" && strings.HasPrefix(uri, prefix) && len(prefix) > bestLen {
						best, bestLen = l.srv.Slug, len(prefix)
					}
				}
			}
		}
		if best != "" {
			return best, nil
		}
	}
	return "", &jsonrpc.Error{Code: -32002, Message: "resource not found: " + uri}
}

// ---- helpers ----------------------------------------------------------------

func (g *Gateway) serverFor(ctx context.Context, caller *auth.Caller, slug string) (*store.Server, store.CacheBundle, error) {
	srv, cached, err := g.store.GetServerBySlugWithCache(ctx, caller.OwnerID, slug, cache.NSInit)
	if err != nil {
		return nil, nil, &jsonrpc.Error{Code: -32004, Message: "unknown server " + slug}
	}
	if !srv.Enabled {
		return nil, nil, &jsonrpc.Error{Code: -32004, Message: "server " + slug + " is disabled"}
	}
	audit.SetTarget(ctx, srv.ID, srv.Slug)
	return srv, cached, nil
}

// forwardMeta keeps client metadata (progress tokens) but drops the inbound
// protocol marker: the upstream session negotiated its own version.
func forwardMeta(in mcp.Meta) mcp.Meta {
	var out mcp.Meta
	for k, v := range in {
		if k == mcp.MetaKeyProtocolVersion {
			continue
		}
		if out == nil {
			out = mcp.Meta{}
		}
		out[k] = v
	}
	return out
}

func tagged(in mcp.Meta, slug string) mcp.Meta {
	out := mcp.Meta{}
	for k, v := range in {
		out[k] = v
	}
	out[MetaKeyServer] = slug
	return out
}

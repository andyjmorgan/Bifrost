package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/cache"
	"github.com/andyjmorgan/Bifrost/internal/store"
	"github.com/andyjmorgan/Bifrost/pkg/hooks"
)

// Skills: on the direct route the gateway serves the skills extension's
// skills/list and skills/get from the server's skills, through
// skills/list.post. A skill's files are read with resources/read, through
// resources/read.post.result, as the engine already does. The client
// library has no call for skills/list, so the gateway sends it on its own.

// skillsPages bounds how many pages of skills/list the gateway follows.
const skillsPages = 20

// skillParams are the params of a skills/* request.
type skillParams struct {
	mcp.ParamsBase
	URI    string  `json:"uri,omitempty"`
	Cursor *string `json:"cursor,omitempty"`
}

// addSkillMethods lets the front door's server receive the skills methods.
// The middleware answers them; the handler registered here never runs.
func addSkillMethods(srv *mcp.Server) {
	for _, m := range []string{"skills/list", "skills/get"} {
		_ = mcp.AddReceivingCustomMethod(srv, m, func(context.Context, *mcp.ServerSession, *skillParams) (*rawResult, error) {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found"}
		})
	}
}

func (g *Gateway) hookSkills(ctx context.Context, caller *auth.Caller, route, slug, method string, req mcp.Request) (mcp.Result, error) {
	p, _ := req.GetParams().(*skillParams)
	if p == nil {
		p = &skillParams{}
	}
	if method == "skills/get" && p.URI == "" {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "uri is required"}
	}
	ld, err := g.load(ctx, caller, route, slug, metaOf(req), cache.NSResources, cache.NSTemplates, cache.NSSkills)
	if err != nil {
		return nil, err
	}
	if x := ld.only(); x != nil && x.err != nil && x.hs.Unavailable {
		return nil, upstreamError(x.srv, x.err)
	}
	if method == "skills/get" {
		return g.done(ctx, g.engine.GetSkill(ctx, ld.req, p.URI), nil)
	}
	return g.done(ctx, g.engine.ListSkills(ctx, ld.req), nil)
}

// fetchSkills is a server's whole skills/list, as a JSON array of entries,
// from the cache or fetched and cached.
func (g *Gateway) fetchSkills(ctx context.Context, srv *store.Server) (json.RawMessage, error) {
	key := srv.ID.String() + "|" + cache.NSSkills
	v, err, _ := g.flights.Do(key, func() (any, error) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), g.conn.Timeout())
		defer cancel()
		if raw, err := g.store.CacheGet(fctx, cache.NSSkills, srv.ID.String()); err == nil && len(raw) > 0 {
			return raw, nil
		}
		var all []json.RawMessage
		var cursor string
		for page := 0; page < skillsPages; page++ {
			params := map[string]any{"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion": hooks.ProtocolCurrent,
				metaClientInfo: map[string]any{"name": "bifrost", "version": g.version},
				metaClientCaps: map[string]any{"extensions": map[string]any{hooks.ExtSkills: map[string]any{}}},
			}}
			if cursor != "" {
				params["cursor"] = cursor
			}
			raw, rpcErr, err := g.conn.Call(fctx, srv, hooks.ProtocolCurrent, "skills/list", "", params, 0)
			if err != nil {
				return nil, err
			}
			if rpcErr != nil {
				if rpcErr.Code == jsonrpc.CodeMethodNotFound {
					return json.RawMessage("[]"), nil
				}
				return nil, fmt.Errorf("skills/list: %d %s", rpcErr.Code, rpcErr.Message)
			}
			var res struct {
				Skills     []json.RawMessage `json:"skills"`
				NextCursor string            `json:"nextCursor"`
			}
			if err := json.Unmarshal(raw, &res); err != nil {
				return nil, fmt.Errorf("skills/list: %w", err)
			}
			all = append(all, res.Skills...)
			if res.NextCursor == "" {
				break
			}
			cursor = res.NextCursor
		}
		raw, err := json.Marshal(all)
		if err != nil {
			return nil, err
		}
		if all == nil {
			raw = json.RawMessage("[]")
		}
		if g.toolsTTL > 0 {
			id := srv.ID
			if err := g.store.CacheSet(fctx, cache.NSSkills, id.String(), &id, srv.UpdatedAt, raw, g.toolsTTL); err != nil && !errors.Is(err, store.ErrStale) {
				g.log.Warn("cache write failed", "slug", srv.Slug, "ns", cache.NSSkills, "err", err)
			}
		}
		return json.RawMessage(raw), nil
	})
	if err != nil {
		return nil, err
	}
	return v.(json.RawMessage), nil
}

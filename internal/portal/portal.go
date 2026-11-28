// Package portal answers "which servers does this caller hold, and what may
// they reach on each": the member portal of the REST API and the Connect
// API both list from here, so the two cannot drift.
package portal

import (
	"context"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/ruletext"
	"github.com/andyjmorgan/Bifrost/internal/store"
)

// Store is the slice of the store the listing reads.
type Store interface {
	ListGrantedWithCache(ctx context.Context, userID string, ns ...string) ([]*store.Server, map[uuid.UUID]store.CacheBundle, error)
	ListCapabilities(ctx context.Context, serverID uuid.UUID) ([]*store.Capability, error)
}

// Server is one server the caller holds.
type Server struct {
	ID           string          `json:"id"`
	Slug         string          `json:"slug"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	MCPURL       string          `json:"mcpUrl"`
	URL          string          `json:"url"`
	Enabled      bool            `json:"enabled"`
	Selection    store.Selection `json:"selection"`
	Capabilities []string        `json:"capabilities"` // "kind:name" seen on the server and in the allow lists (rules not applied here)
	HasRules     bool            `json:"hasRules"`     // the server has rules on some hook
}

// List returns the servers granted to userID, ordered by slug. publicBase
// is the gateway's external origin, from which each server's MCP URL is
// built. A server whose catalog cannot be read is listed without one.
func List(ctx context.Context, st Store, publicBase, userID string) ([]Server, error) {
	held, _, err := st.ListGrantedWithCache(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Server, 0, len(held))
	for _, s := range held {
		d := Server{ID: s.ID.String(), Slug: s.Slug, Name: s.Name, Description: s.Description, MCPURL: publicBase + "/" + s.Slug + "/mcp", URL: s.URL,
			Enabled: s.Enabled, Selection: s.Selection, Capabilities: []string{}}
		if hs, err := ruletext.ParseStored(s.Hooks); err != nil || !hs.Empty() {
			d.HasRules = true
		}
		if caps, err := st.ListCapabilities(ctx, s.ID); err == nil {
			for _, cp := range caps {
				if allowsKind(s, cp.Kind, cp.Name) {
					d.Capabilities = append(d.Capabilities, cp.Kind+":"+cp.Name)
				}
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// allowsKind mirrors the gateway's cut for the listing (resources by exact
// pattern match only; the gateway also matches globs).
func allowsKind(s *store.Server, kind, name string) bool {
	list, ok := s.Selection[store.SelectionKey(kind)]
	if !ok {
		return true
	}
	for _, x := range list {
		if x == name {
			return true
		}
	}
	return false
}

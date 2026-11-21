package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Capability kinds, as the ceiling records them.
const (
	KindTool     = "tool"
	KindPrompt   = "prompt"
	KindResource = "resource"
	KindTemplate = "template"
)

// Selection is a server's allow lists, keyed by kind: "tools",
// "prompts", "resources" (URI patterns: * within a segment, ** as a
// prefix) and "templates". A key present means only what it names; a key
// absent means everything of that kind. Advanced rules run after these.
type Selection map[string][]string

// Allow list keys.
const (
	SelTools     = "tools"
	SelPrompts   = "prompts"
	SelResources = "resources"
	SelTemplates = "templates"
)

// Key maps a capability kind to its allow-list key.
func SelectionKey(kind string) string {
	switch kind {
	case KindTool:
		return SelTools
	case KindPrompt:
		return SelPrompts
	case KindResource:
		return SelResources
	case KindTemplate:
		return SelTemplates
	}
	return ""
}

// Grant gives one user the right to use a server.
type Grant struct {
	UserID    string
	GrantedBy string
	GrantedAt time.Time
}

// Capability is one thing a server has been seen to offer: governance data
// for the server page and its rule editor, never a filter.
type Capability struct {
	ServerID    uuid.UUID
	Kind        string
	Name        string
	Fingerprint string
	Definition  json.RawMessage // the item as the server last sent it
	FirstSeenAt time.Time
	LastSeenAt  time.Time
}

// Seen is one observed item: its fingerprint and definition.
type Seen struct {
	Fingerprint string
	Definition  json.RawMessage
}

// listGrants returns the grants of the given servers, by server.
func (p *Postgres) listGrants(ctx context.Context, ids ...uuid.UUID) (map[uuid.UUID][]Grant, error) {
	rows, err := p.pool.Query(ctx, `select server_id, user_id, granted_by, granted_at from server_grants where server_id = any($1) order by server_id, user_id`, ids)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID][]Grant{}
	for rows.Next() {
		var id uuid.UUID
		var g Grant
		if err := rows.Scan(&id, &g.UserID, &g.GrantedBy, &g.GrantedAt); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
		out[id] = append(out[id], g)
	}
	return out, rows.Err()
}

// withGrants fills each server's grants.
func (p *Postgres) withGrants(ctx context.Context, servers ...*Server) error {
	ids := make([]uuid.UUID, len(servers))
	for i, s := range servers {
		ids[i] = s.ID
	}
	grants, err := p.listGrants(ctx, ids...)
	if err != nil {
		return err
	}
	for _, s := range servers {
		s.Grants = grants[s.ID]
		if s.Grants == nil {
			s.Grants = []Grant{}
		}
	}
	return nil
}

func insertGrants(ctx context.Context, tx pgx.Tx, id uuid.UUID, grants []Grant, by string) error {
	for _, g := range grants {
		if _, err := tx.Exec(ctx, `insert into server_grants (server_id, user_id, granted_by) values ($1,$2,$3) on conflict do nothing`, id, g.UserID, by); err != nil {
			return fmt.Errorf("store: %w", err)
		}
	}
	return nil
}

// SetGrants replaces a server's grants and bumps its generation.
func (p *Postgres) SetGrants(ctx context.Context, id uuid.UUID, grants []Grant, by string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `update servers set generation=generation+1 where id=$1`, id)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `delete from server_grants where server_id=$1`, id); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if err := insertGrants(ctx, tx, id, grants, by); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// UpdateRules writes a server's selection and hooks and bumps its
// generation. It leaves updated_at alone: that marks the configuration a
// catalog was fetched with, which rules do not change.
func (p *Postgres) UpdateRules(ctx context.Context, s *Server) error {
	if s.Selection == nil {
		s.Selection = Selection{}
	}
	sel, _ := json.Marshal(s.Selection)
	if len(s.Hooks) == 0 {
		s.Hooks = json.RawMessage("{}")
	}
	err := p.pool.QueryRow(ctx, `update servers set selection=$2, hooks=$3, generation=generation+1 where id=$1 returning generation`,
		s.ID, sel, s.Hooks).Scan(&s.Generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// ---- granted servers ------------------------------------------------------------

// The servers a user holds, each with its live cache. The gateway only
// ever reaches a server the caller holds. A caller with no user holds
// nothing.
const grantedSQL = `select ` + serverColsS + `,
coalesce(jsonb_object_agg(c.ns, c.value) filter (where c.ns is not null), '{}'::jsonb)
from servers s
join server_grants g on g.server_id = s.id and g.user_id = $1
left join cache c on c.server_id = s.id and c.ns = any($2) and c.expires_at > now()
where true
`

func scanGranted(row pgx.Row) (*Server, CacheBundle, error) {
	var raw []byte
	s, err := scanServerInto(row, &raw)
	if err != nil {
		return nil, nil, err
	}
	b := CacheBundle{}
	_ = json.Unmarshal(raw, &b)
	return s, b, nil
}

// ListGrantedWithCache returns every server the caller holds, by slug,
// with the live cache entries of the given namespaces.
func (p *Postgres) ListGrantedWithCache(ctx context.Context, userID string, ns ...string) ([]*Server, map[uuid.UUID]CacheBundle, error) {
	rows, err := p.pool.Query(ctx, grantedSQL+`group by s.id order by s.slug`, userID, ns)
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	var out []*Server
	cached := map[uuid.UUID]CacheBundle{}
	for rows.Next() {
		s, b, err := scanGranted(rows)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, s)
		cached[s.ID] = b
	}
	return out, cached, rows.Err()
}

// GetGrantedBySlugWithCache resolves a slug the caller holds.
func (p *Postgres) GetGrantedBySlugWithCache(ctx context.Context, userID, slug string, ns ...string) (*Server, CacheBundle, error) {
	return scanGranted(p.pool.QueryRow(ctx, grantedSQL+`and s.slug = $3 group by s.id`, userID, ns, slug))
}

// GetGrantedBySlug resolves a slug the caller holds, without cache.
func (p *Postgres) GetGrantedBySlug(ctx context.Context, userID, slug string) (*Server, error) {
	s, _, err := p.GetGrantedBySlugWithCache(ctx, userID, slug)
	return s, err
}

// ---- catalog (what a server has been seen to offer) ----------------------------

// ObserveCapabilities records what a server offered: new rows for anything
// unseen, last_seen, fingerprint and definition refreshed on the rest.
func (p *Postgres) ObserveCapabilities(ctx context.Context, serverID uuid.UUID, kind string, items map[string]Seen) error {
	if len(items) == 0 {
		return nil
	}
	for name, it := range items {
		def := it.Definition
		if len(def) == 0 {
			def = nil
		}
		if _, err := p.pool.Exec(ctx, `insert into server_capabilities (server_id, kind, name, fingerprint, definition) values ($1,$2,$3,$4,$5)
			on conflict (server_id, kind, name) do update set last_seen_at=now(), fingerprint=excluded.fingerprint, definition=coalesce(excluded.definition, server_capabilities.definition)`,
			serverID, kind, name, it.Fingerprint, def); err != nil {
			return fmt.Errorf("store: %w", err)
		}
	}
	return nil
}

// ListCapabilities returns what a server has been seen to offer, by kind and name.
func (p *Postgres) ListCapabilities(ctx context.Context, serverID uuid.UUID) ([]*Capability, error) {
	rows, err := p.pool.Query(ctx, `select sc.server_id, sc.kind, sc.name, sc.fingerprint, sc.definition, sc.first_seen_at, sc.last_seen_at
		from server_capabilities sc where sc.server_id=$1
		order by sc.kind, sc.name`, serverID)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	out := []*Capability{}
	for rows.Next() {
		var c Capability
		if err := rows.Scan(&c.ServerID, &c.Kind, &c.Name, &c.Fingerprint, &c.Definition, &c.FirstSeenAt, &c.LastSeenAt); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

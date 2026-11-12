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

// Selection is what an entitlement exposes of its server, inside the
// ceiling. All means everything; otherwise the named capabilities. Resource
// entries are URI patterns (glob with * and **), templates are URI templates.
type Selection struct {
	All       bool     `json:"all,omitempty"`
	Tools     []string `json:"tools,omitempty"`
	Prompts   []string `json:"prompts,omitempty"`
	Resources []string `json:"resources,omitempty"`
	Templates []string `json:"templates,omitempty"`
}

// Entitlement is a named, grantable view of one server under its own slug.
type Entitlement struct {
	ID          uuid.UUID
	OrgID       string
	ServerID    uuid.UUID
	Slug        string
	Name        string
	Description string
	Selection   Selection
	Policies    json.RawMessage // entitlement policies (rules), opaque to the store
	Generation  int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Grants      []Grant
}

// Grant gives a user or a role the right to use an entitlement.
type Grant struct {
	SubjectType string // "user" | "role"
	SubjectID   string // user id, or "admin" | "member"
	GrantedBy   string
	GrantedAt   time.Time
}

// Capability is one row of a server's ceiling.
type Capability struct {
	ServerID    uuid.UUID
	Kind        string
	Name        string
	Fingerprint string
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	ApprovedAt  *time.Time
}

const entCols = `id, org_id, server_id, slug, name, description, selection, policies, generation, created_at, updated_at`

func scanEntitlement(row pgx.Row) (*Entitlement, error) {
	var e Entitlement
	var sel []byte
	err := row.Scan(&e.ID, &e.OrgID, &e.ServerID, &e.Slug, &e.Name, &e.Description, &sel, &e.Policies, &e.Generation, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	_ = json.Unmarshal(sel, &e.Selection)
	if len(e.Policies) == 0 {
		e.Policies = json.RawMessage("[]")
	}
	return &e, nil
}

// CreateEntitlement inserts; ErrConflict when the slug is taken in the org.
func (p *Postgres) CreateEntitlement(ctx context.Context, e *Entitlement) error {
	sel, _ := json.Marshal(e.Selection)
	if len(e.Policies) == 0 {
		e.Policies = json.RawMessage("[]")
	}
	err := p.pool.QueryRow(ctx, `insert into entitlements (id, org_id, server_id, slug, name, description, selection, policies)
		values ($1,$2,$3,$4,$5,$6,$7,$8) returning generation, created_at, updated_at`,
		e.ID, e.OrgID, e.ServerID, e.Slug, e.Name, e.Description, sel, e.Policies).Scan(&e.Generation, &e.CreatedAt, &e.UpdatedAt)
	if isUnique(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// UpdateEntitlement writes name, description, selection and policies (not
// the slug) and bumps the generation.
func (p *Postgres) UpdateEntitlement(ctx context.Context, e *Entitlement) error {
	sel, _ := json.Marshal(e.Selection)
	if len(e.Policies) == 0 {
		e.Policies = json.RawMessage("[]")
	}
	err := p.pool.QueryRow(ctx, `update entitlements set name=$3, description=$4, selection=$5, policies=$6, generation=generation+1, updated_at=now()
		where org_id=$1 and id=$2 returning generation, updated_at`, e.OrgID, e.ID, e.Name, e.Description, sel, e.Policies).Scan(&e.Generation, &e.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// DeleteEntitlement removes an entitlement and its grants.
func (p *Postgres) DeleteEntitlement(ctx context.Context, org string, id uuid.UUID) error {
	tag, err := p.pool.Exec(ctx, `delete from entitlements where org_id=$1 and id=$2`, org, id)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetEntitlement fetches one with its grants.
func (p *Postgres) GetEntitlement(ctx context.Context, org string, id uuid.UUID) (*Entitlement, error) {
	e, err := scanEntitlement(p.pool.QueryRow(ctx, `select `+entCols+` from entitlements where org_id=$1 and id=$2`, org, id))
	if err != nil {
		return nil, err
	}
	if e.Grants, err = p.listGrants(ctx, e.ID); err != nil {
		return nil, err
	}
	return e, nil
}

// EntitlementSlugTaken reports whether the org already uses slug.
func (p *Postgres) EntitlementSlugTaken(ctx context.Context, org, slug string) (bool, error) {
	var taken bool
	if err := p.pool.QueryRow(ctx, `select exists(select 1 from entitlements where org_id=$1 and slug=$2)`, org, slug).Scan(&taken); err != nil {
		return false, fmt.Errorf("store: %w", err)
	}
	return taken, nil
}

// ListEntitlements returns the org's entitlements with grants, optionally
// for one server, by server then name.
func (p *Postgres) ListEntitlements(ctx context.Context, org string, serverID *uuid.UUID) ([]*Entitlement, error) {
	rows, err := p.pool.Query(ctx, `select `+entCols+` from entitlements where org_id=$1 and ($2::uuid is null or server_id=$2) order by server_id, lower(name)`, org, serverID)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	var out []*Entitlement
	for rows.Next() {
		e, err := scanEntitlement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	for _, e := range out {
		if e.Grants, err = p.listGrants(ctx, e.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (p *Postgres) listGrants(ctx context.Context, id uuid.UUID) ([]Grant, error) {
	rows, err := p.pool.Query(ctx, `select subject_type, subject_id, granted_by, granted_at from entitlement_grants where entitlement_id=$1 order by subject_type, subject_id`, id)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	out := []Grant{}
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.SubjectType, &g.SubjectID, &g.GrantedBy, &g.GrantedAt); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetGrants replaces an entitlement's grants.
func (p *Postgres) SetGrants(ctx context.Context, org string, id uuid.UUID, grants []Grant, by string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var exists bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from entitlements where org_id=$1 and id=$2)`, org, id).Scan(&exists); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `delete from entitlement_grants where entitlement_id=$1`, id); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	for _, g := range grants {
		if _, err := tx.Exec(ctx, `insert into entitlement_grants (entitlement_id, subject_type, subject_id, granted_by) values ($1,$2,$3,$4) on conflict do nothing`, id, g.SubjectType, g.SubjectID, by); err != nil {
			return fmt.Errorf("store: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `update entitlements set generation=generation+1, updated_at=now() where id=$1`, id); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return tx.Commit(ctx)
}

// ---- exposures ----------------------------------------------------------------

// Exposures are the entitlements a caller holds, each joined to its server
// and the server's live cache. The gateway only ever reaches a server
// through one of these. A system caller (no user) holds nothing.
const exposureSQL = `select s.id, s.org_id, e.slug, s.name, s.url, s.description, s.auth_kind, s.enabled, s.created_at, s.updated_at,
s.exclusive, s.ceiling_mode, s.ceiling_seeded, e.id, e.name, e.selection, e.policies, e.generation,
coalesce((select jsonb_agg(sc.kind || ':' || sc.name) from server_capabilities sc where sc.server_id = s.id and sc.approved_at is not null), '[]'::jsonb),
coalesce(jsonb_object_agg(c.ns, c.value) filter (where c.ns is not null), '{}'::jsonb)
from entitlements e
join servers s on s.id = e.server_id
left join cache c on c.server_id = s.id and c.ns = any($2) and c.expires_at > now()
where e.org_id = $1
and exists (select 1 from entitlement_grants g where g.entitlement_id = e.id
  and ((g.subject_type = 'user' and g.subject_id = $3) or (g.subject_type = 'role' and g.subject_id = $4)))
`

func scanExposure(row pgx.Row) (*Server, CacheBundle, error) {
	var s Server
	var sel, approved, raw []byte
	err := row.Scan(&s.ID, &s.OrgID, &s.Slug, &s.Name, &s.URL, &s.Description, &s.AuthKind, &s.Enabled, &s.CreatedAt, &s.UpdatedAt,
		&s.Exclusive, &s.CeilingMode, &s.CeilingSeeded, &s.EntitlementID, &s.EntitlementName, &sel, &s.Policies, &s.Generation, &approved, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	_ = json.Unmarshal(sel, &s.Selection)
	var names []string
	_ = json.Unmarshal(approved, &names)
	s.Approved = map[string]bool{}
	for _, n := range names {
		s.Approved[n] = true
	}
	b := CacheBundle{}
	_ = json.Unmarshal(raw, &b)
	return &s, b, nil
}

// ListExposuresWithCache returns every entitlement the caller holds, as a
// server row carrying the entitlement's slug and selection, with the live
// cache entries of the given namespaces.
func (p *Postgres) ListExposuresWithCache(ctx context.Context, org, userID, role string, ns ...string) ([]*Server, map[uuid.UUID]CacheBundle, error) {
	rows, err := p.pool.Query(ctx, exposureSQL+`group by s.id, e.id order by e.slug`, org, ns, userID, role)
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	var out []*Server
	cached := map[uuid.UUID]CacheBundle{}
	for rows.Next() {
		s, b, err := scanExposure(rows)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, s)
		cached[s.ID] = b
	}
	return out, cached, rows.Err()
}

// GetExposureBySlugWithCache resolves a slug the caller holds.
func (p *Postgres) GetExposureBySlugWithCache(ctx context.Context, org, userID, role, slug string, ns ...string) (*Server, CacheBundle, error) {
	return scanExposure(p.pool.QueryRow(ctx, exposureSQL+`and e.slug = $5 group by s.id, e.id`, org, ns, userID, role, slug))
}

// GetExposureBySlug resolves a slug the caller holds, without cache.
func (p *Postgres) GetExposureBySlug(ctx context.Context, org, userID, role, slug string) (*Server, error) {
	s, _, err := p.GetExposureBySlugWithCache(ctx, org, userID, role, slug)
	return s, err
}

// ---- ceiling ------------------------------------------------------------------

// ObserveCapabilities records what a server offered: new rows for anything
// unseen, last_seen bumped on the rest. The first time a kind is seen on a
// server (its tools, then later perhaps its prompts) the whole batch is
// approved, as is everything on a server in open mode; after that a new
// capability of that kind waits for an admin.
// It reports whether new rows were approved.
func (p *Postgres) ObserveCapabilities(ctx context.Context, serverID uuid.UUID, kind string, names map[string]string) (bool, error) {
	if len(names) == 0 {
		return false, nil
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("store: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var mode string
	var seeded bool
	if err := tx.QueryRow(ctx, `select ceiling_mode, ceiling_seeded from servers where id=$1 for update`, serverID).Scan(&mode, &seeded); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, fmt.Errorf("store: %w", err)
	}
	var seenKind bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from server_capabilities where server_id=$1 and kind=$2)`, serverID, kind).Scan(&seenKind); err != nil {
		return false, fmt.Errorf("store: %w", err)
	}
	auto := mode == "open" || !seeded || !seenKind
	for name, fp := range names {
		if _, err := tx.Exec(ctx, `insert into server_capabilities (server_id, kind, name, fingerprint, approved_at)
			values ($1,$2,$3,$4, case when $5 then now() end)
			on conflict (server_id, kind, name) do update set last_seen_at=now(), fingerprint=excluded.fingerprint`, serverID, kind, name, fp, auto); err != nil {
			return false, fmt.Errorf("store: %w", err)
		}
	}
	if !seeded {
		if _, err := tx.Exec(ctx, `update servers set ceiling_seeded=true where id=$1`, serverID); err != nil {
			return false, fmt.Errorf("store: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("store: %w", err)
	}
	return auto, nil
}

// ListCapabilities returns a server's ceiling rows, pending first.
func (p *Postgres) ListCapabilities(ctx context.Context, org string, serverID uuid.UUID) ([]*Capability, error) {
	rows, err := p.pool.Query(ctx, `select sc.server_id, sc.kind, sc.name, sc.fingerprint, sc.first_seen_at, sc.last_seen_at, sc.approved_at
		from server_capabilities sc join servers s on s.id = sc.server_id where s.org_id=$1 and sc.server_id=$2
		order by (sc.approved_at is null) desc, sc.kind, sc.name`, org, serverID)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	out := []*Capability{}
	for rows.Next() {
		var c Capability
		if err := rows.Scan(&c.ServerID, &c.Kind, &c.Name, &c.Fingerprint, &c.FirstSeenAt, &c.LastSeenAt, &c.ApprovedAt); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// SetCapabilityApproval approves or revokes rows (all when names is nil).
func (p *Postgres) SetCapabilityApproval(ctx context.Context, org string, serverID uuid.UUID, names []string, approve bool) error {
	var exists bool
	if err := p.pool.QueryRow(ctx, `select exists(select 1 from servers where org_id=$1 and id=$2)`, org, serverID).Scan(&exists); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	q := `update server_capabilities set approved_at = case when $2 then coalesce(approved_at, now()) end where server_id=$1`
	args := []any{serverID, approve}
	if names != nil {
		q += ` and (kind || ':' || name) = any($3)`
		args = append(args, names)
	}
	if _, err := p.pool.Exec(ctx, q, args...); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if _, err := p.pool.Exec(ctx, `update servers set updated_at=now() where id=$1`, serverID); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/andyjmorgan/Mcp-Bifrost/internal/policy"
)

const policyCols = `id, owner_id, name, description, invert, rules, created_at, updated_at`

func scanPolicy(row pgx.Row) (*policy.Policy, error) {
	var p policy.Policy
	var rules []byte
	if err := row.Scan(&p.ID, &p.OwnerID, &p.Name, &p.Description, &p.Invert, &rules, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(rules, &p.Rules); err != nil {
		return nil, fmt.Errorf("store: policy %s rules: %w", p.ID, err)
	}
	if p.Rules == nil {
		p.Rules = []policy.Rule{}
	}
	return &p, nil
}

// ListPolicies returns an owner's policies by name.
func (p *Postgres) ListPolicies(ctx context.Context, owner string) ([]*policy.Policy, error) {
	rows, err := p.pool.Query(ctx, `select `+policyCols+` from policies where owner_id=$1 order by lower(name)`, owner)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	var out []*policy.Policy
	for rows.Next() {
		pol, err := scanPolicy(rows)
		if err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
		out = append(out, pol)
	}
	if out == nil {
		out = []*policy.Policy{}
	}
	return out, rows.Err()
}

// GetPolicy fetches one policy or ErrNotFound.
func (p *Postgres) GetPolicy(ctx context.Context, owner string, id uuid.UUID) (*policy.Policy, error) {
	pol, err := scanPolicy(p.pool.QueryRow(ctx, `select `+policyCols+` from policies where owner_id=$1 and id=$2`, owner, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	return pol, nil
}

// CreatePolicy inserts pol (ID assigned here) and fills its timestamps.
// A duplicate name is ErrConflict.
func (p *Postgres) CreatePolicy(ctx context.Context, pol *policy.Policy) error {
	if pol.ID == uuid.Nil {
		pol.ID = uuid.New()
	}
	rules, err := json.Marshal(pol.Rules)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	err = p.pool.QueryRow(ctx, `insert into policies (id, owner_id, name, description, invert, rules) values ($1,$2,$3,$4,$5,$6) returning created_at, updated_at`,
		pol.ID, pol.OwnerID, pol.Name, pol.Description, pol.Invert, rules).Scan(&pol.CreatedAt, &pol.UpdatedAt)
	if isUnique(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// UpdatePolicy writes name, description, invert and rules, and refreshes
// updated_at (which keys the compiled-policy cache).
func (p *Postgres) UpdatePolicy(ctx context.Context, pol *policy.Policy) error {
	rules, err := json.Marshal(pol.Rules)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	err = p.pool.QueryRow(ctx, `update policies set name=$3, description=$4, invert=$5, rules=$6, updated_at=now() where owner_id=$1 and id=$2 returning updated_at`,
		pol.OwnerID, pol.ID, pol.Name, pol.Description, pol.Invert, rules).Scan(&pol.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if isUnique(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// DeletePolicy removes a policy and, by cascade, its bindings.
func (p *Postgres) DeletePolicy(ctx context.Context, owner string, id uuid.UUID) error {
	tag, err := p.pool.Exec(ctx, `delete from policies where owner_id=$1 and id=$2`, owner, id)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Binding attaches a policy to a route: ServerID nil is the global /mcp
// route, otherwise the server's own route.
type Binding struct {
	ServerID *uuid.UUID
	Position int
	Policy   *policy.Policy
}

// ListBindings returns every binding of an owner with its policy, ordered by
// route then position. One query serves a whole fan-out.
func (p *Postgres) ListBindings(ctx context.Context, owner string) ([]Binding, error) {
	rows, err := p.pool.Query(ctx, `select b.server_id, b.position, `+prefixed(policyCols, "p.")+`
from policy_bindings b join policies p on p.id=b.policy_id
where b.owner_id=$1 order by b.server_id nulls first, b.position, lower(p.name)`, owner)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	var out []Binding
	for rows.Next() {
		var b Binding
		var pol policy.Policy
		var rules []byte
		if err := rows.Scan(&b.ServerID, &b.Position, &pol.ID, &pol.OwnerID, &pol.Name, &pol.Description, &pol.Invert, &rules, &pol.CreatedAt, &pol.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
		if err := json.Unmarshal(rules, &pol.Rules); err != nil {
			return nil, fmt.Errorf("store: policy %s rules: %w", pol.ID, err)
		}
		b.Policy = &pol
		out = append(out, b)
	}
	return out, rows.Err()
}

// SetBindings replaces the ordered policies bound to one route. Every id
// must be one of the owner's policies, otherwise ErrNotFound.
func (p *Postgres) SetBindings(ctx context.Context, owner string, serverID *uuid.UUID, policyIDs []uuid.UUID) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if len(policyIDs) > 0 {
		var n int
		if err := tx.QueryRow(ctx, `select count(*) from policies where owner_id=$1 and id = any($2)`, owner, policyIDs).Scan(&n); err != nil {
			return fmt.Errorf("store: %w", err)
		}
		if n != len(uniqueIDs(policyIDs)) {
			return ErrNotFound
		}
	}
	if serverID == nil {
		_, err = tx.Exec(ctx, `delete from policy_bindings where owner_id=$1 and server_id is null`, owner)
	} else {
		_, err = tx.Exec(ctx, `delete from policy_bindings where owner_id=$1 and server_id=$2`, owner, *serverID)
	}
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	for i, id := range uniqueIDs(policyIDs) {
		if _, err := tx.Exec(ctx, `insert into policy_bindings (owner_id, server_id, policy_id, position) values ($1,$2,$3,$4)`, owner, serverID, id, i); err != nil {
			return fmt.Errorf("store: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

func uniqueIDs(ids []uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// prefixed qualifies a comma-separated column list with a table alias.
func prefixed(cols, alias string) string {
	parts := strings.Split(cols, ", ")
	for i := range parts {
		parts[i] = alias + parts[i]
	}
	return strings.Join(parts, ", ")
}

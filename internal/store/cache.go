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

// CacheGet returns the live value for (ns, key) or ErrNotFound. Expired rows
// are filtered here as well as by the RLS policy, since the bootstrap role
// bypasses RLS.
func (p *Postgres) CacheGet(ctx context.Context, ns, key string) (json.RawMessage, error) {
	var v json.RawMessage
	err := p.pool.QueryRow(ctx, `select value from cache where ns=$1 and key=$2 and expires_at > now()`, ns, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	return v, nil
}

// CacheSet stores value under (ns, key) for ttl, replacing any earlier row.
// serverID (nil for gateway-wide entries such as the mux tools/list) ties the
// row to a server for the joined lookup and cascade delete.
func (p *Postgres) CacheSet(ctx context.Context, ns, key string, serverID *uuid.UUID, value json.RawMessage, ttl time.Duration) error {
	_, err := p.pool.Exec(ctx, `insert into cache (ns, key, server_id, value, expires_at) values ($1,$2,$3,$4,now()+$5)
on conflict (ns, key) do update set server_id=excluded.server_id, value=excluded.value, expires_at=excluded.expires_at`,
		ns, key, serverID, value, ttl)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// CacheDelete removes one entry, or every entry in ns when key is "".
func (p *Postgres) CacheDelete(ctx context.Context, ns, key string) error {
	var err error
	if key == "" {
		_, err = p.pool.Exec(ctx, `delete from cache where ns=$1`, ns)
	} else {
		_, err = p.pool.Exec(ctx, `delete from cache where ns=$1 and key=$2`, ns, key)
	}
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// CacheInvalidateServer drops every cached entry tied to the server.
func (p *Postgres) CacheInvalidateServer(ctx context.Context, serverID uuid.UUID) error {
	if _, err := p.pool.Exec(ctx, `delete from cache where server_id=$1`, serverID); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// GetServerBySlugWithCache returns the server and, in the same query, its live
// cache entry in ns (nil when absent or expired). One indexed join, no second
// round trip on the hot path.
func (p *Postgres) GetServerBySlugWithCache(ctx context.Context, owner, slug, ns string) (*Server, json.RawMessage, error) {
	var s Server
	var v json.RawMessage
	err := p.pool.QueryRow(ctx, `select s.id, s.owner_id, s.slug, s.name, s.url, s.description, s.auth_kind, s.enabled, s.created_at, s.updated_at, c.value
from servers s
left join cache c on c.server_id = s.id and c.ns = $3 and c.expires_at > now()
where s.owner_id=$1 and s.slug=$2
order by c.expires_at desc nulls last
limit 1`, owner, slug, ns).
		Scan(&s.ID, &s.OwnerID, &s.Slug, &s.Name, &s.URL, &s.Description, &s.AuthKind, &s.Enabled, &s.CreatedAt, &s.UpdatedAt, &v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	return &s, v, nil
}

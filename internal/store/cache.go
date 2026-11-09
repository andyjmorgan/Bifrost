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
// serverID (nil for gateway-wide entries) ties the row to a server for the
// joined lookup and cascade delete. ifUpdatedAt, when set with a serverID,
// makes the write conditional: it lands only while the server row still has
// that updated_at, so a fetch that started before an edit or credential
// change cannot overwrite the row with a list from the old configuration.
// Such a write returns ErrStale.
func (p *Postgres) CacheSet(ctx context.Context, ns, key string, serverID *uuid.UUID, ifUpdatedAt time.Time, value json.RawMessage, ttl time.Duration) error {
	const upsert = ` on conflict (ns, key) do update set server_id=excluded.server_id, value=excluded.value, expires_at=excluded.expires_at`
	if serverID != nil && !ifUpdatedAt.IsZero() {
		tag, err := p.pool.Exec(ctx, `insert into cache (ns, key, server_id, value, expires_at)
select $1, $2, $3, $4, now()+$5 from servers where id=$3 and updated_at=$6`+upsert,
			ns, key, serverID, value, ttl, ifUpdatedAt)
		if err != nil {
			return fmt.Errorf("store: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrStale
		}
		return nil
	}
	_, err := p.pool.Exec(ctx, `insert into cache (ns, key, server_id, value, expires_at) values ($1,$2,$3,$4,now()+$5)`+upsert,
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

// CacheInvalidateOwner drops every cached entry for all of an owner's servers.
func (p *Postgres) CacheInvalidateOwner(ctx context.Context, owner string) error {
	if _, err := p.pool.Exec(ctx, `delete from cache where server_id in (select id from servers where owner_id=$1)`, owner); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// PurgeExpiredCache deletes expired rows; the belt to pg_cron's braces (and
// the only reaper where pg_cron is not installed).
func (p *Postgres) PurgeExpiredCache(ctx context.Context) (int64, error) {
	tag, err := p.pool.Exec(ctx, `delete from cache where expires_at <= now()`)
	if err != nil {
		return 0, fmt.Errorf("store: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CacheInvalidateServer drops every cached entry tied to the server.
func (p *Postgres) CacheInvalidateServer(ctx context.Context, serverID uuid.UUID) error {
	if _, err := p.pool.Exec(ctx, `delete from cache where server_id=$1`, serverID); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// CacheBundle is a server's live cache entries keyed by namespace.
type CacheBundle map[string]json.RawMessage

const serverWithCacheSQL = `select s.id, s.owner_id, s.slug, s.name, s.url, s.description, s.auth_kind, s.enabled, s.created_at, s.updated_at,
coalesce(jsonb_object_agg(c.ns, c.value) filter (where c.ns is not null), '{}'::jsonb)
from servers s
left join cache c on c.server_id = s.id and c.ns = any($2) and c.expires_at > now()
`

func scanServerWithCache(row pgx.Row) (*Server, CacheBundle, error) {
	var s Server
	var raw []byte
	err := row.Scan(&s.ID, &s.OwnerID, &s.Slug, &s.Name, &s.URL, &s.Description, &s.AuthKind, &s.Enabled, &s.CreatedAt, &s.UpdatedAt, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	b := CacheBundle{}
	_ = json.Unmarshal(raw, &b)
	return &s, b, nil
}

// ListServersWithCache returns an owner's servers (oldest first) and, per
// server, its live cache entries in the given namespaces, from one query.
func (p *Postgres) ListServersWithCache(ctx context.Context, owner string, ns ...string) ([]*Server, map[uuid.UUID]CacheBundle, error) {
	rows, err := p.pool.Query(ctx, serverWithCacheSQL+`where s.owner_id=$1 group by s.id order by s.created_at`, owner, ns)
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	var out []*Server
	cached := map[uuid.UUID]CacheBundle{}
	for rows.Next() {
		s, b, err := scanServerWithCache(rows)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, s)
		cached[s.ID] = b
	}
	return out, cached, rows.Err()
}

// GetServerBySlugWithCache returns the server and, in the same query, its
// live cache entries in the given namespaces. One indexed join, no second
// round trip on the hot path.
func (p *Postgres) GetServerBySlugWithCache(ctx context.Context, owner, slug string, ns ...string) (*Server, CacheBundle, error) {
	return scanServerWithCache(p.pool.QueryRow(ctx, serverWithCacheSQL+`where s.owner_id=$1 and s.slug=$3 group by s.id`, owner, ns, slug))
}

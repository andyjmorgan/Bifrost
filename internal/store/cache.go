package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
func (p *Postgres) CacheSet(ctx context.Context, ns, key string, value, meta json.RawMessage, ttl time.Duration) error {
	if meta == nil {
		meta = json.RawMessage(`{}`)
	}
	_, err := p.pool.Exec(ctx, `insert into cache (ns, key, value, meta, expires_at) values ($1,$2,$3,$4,now()+$5)
on conflict (ns, key) do update set value=excluded.value, meta=excluded.meta, created_at=now(), expires_at=excluded.expires_at`,
		ns, key, value, meta, ttl)
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

// CacheInvalidateServer drops every cached entry whose meta names the server.
func (p *Postgres) CacheInvalidateServer(ctx context.Context, serverID string) error {
	if _, err := p.pool.Exec(ctx, `delete from cache where meta->>'serverId' = $1`, serverID); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

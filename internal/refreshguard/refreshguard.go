// Package refreshguard keeps a credential migration and a token refresh from
// running at the same time, across processes.
//
// A refresh token rotates: the authorization server hands out a new one and
// retires the old. If a token were refreshed while its row was being copied
// from one store to the other, the copy would hold the retired token and the
// server would need consent again. So every refresh holds the guard shared,
// for as long as it talks to the token endpoint and writes the result, and
// `gateway migrate-credentials` holds it exclusive for its whole run. The
// command waits for refreshes in flight to finish, and refreshes wait for
// the command.
//
// The guard is a Postgres advisory lock on the gateway's database, so it
// works between the command and a running gateway, and between replicas. It
// is tied to a database session: a process that dies lets go of it.
package refreshguard

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// key is the advisory lock both sides take: "bifrostc" in ASCII.
const key int64 = 0x626966726f737463

// Guard is the lock on one database. The zero value is not usable; a nil
// *Guard guards nothing, which is right for a process with no database.
type Guard struct {
	pool *pgxpool.Pool
}

// New builds the guard on the gateway's database.
func New(pool *pgxpool.Pool) *Guard { return &Guard{pool: pool} }

// Shared is held by a token refresh. It blocks while a migration runs, until
// ctx ends. Call release when the refreshed token has been stored.
func (g *Guard) Shared(ctx context.Context) (release func(), err error) {
	return g.hold(ctx, "pg_advisory_lock_shared", "pg_advisory_unlock_shared")
}

// Exclusive is held by a credential migration for its whole run. It waits
// for every refresh in flight to finish and keeps new ones from starting.
func (g *Guard) Exclusive(ctx context.Context) (release func(), err error) {
	return g.hold(ctx, "pg_advisory_lock", "pg_advisory_unlock")
}

// hold takes the lock on a connection of its own, which it keeps until
// release: an advisory lock belongs to the session that took it.
func (g *Guard) hold(ctx context.Context, lock, unlock string) (func(), error) {
	if g == nil || g.pool == nil {
		return func() {}, nil
	}
	conn, err := g.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("refreshguard: %w", err)
	}
	if _, err := conn.Exec(ctx, "select "+lock+"($1)", key); err != nil {
		// A cancelled wait may still have been granted the lock; closing the
		// session is the one way to be sure it is not left held.
		_ = conn.Conn().Close(context.WithoutCancel(ctx))
		conn.Release()
		return nil, fmt.Errorf("refreshguard: %w", err)
	}
	return func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), "select "+unlock+"($1)", key); err != nil {
			_ = conn.Conn().Close(context.WithoutCancel(ctx))
		}
		conn.Release()
	}, nil
}

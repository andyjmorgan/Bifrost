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
	"errors"
	"fmt"
	"sync"
	"time"

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

// Held is the exclusive guard while a migration has it.
type Held interface {
	// Check reports whether the guard is still held. The lock lives in one
	// database session; if that session is lost (the server restarted, the
	// connection was cut, somebody terminated it) the lock went with it and
	// refreshes are running again. A migration asks before every credential
	// it moves and stops when the answer is an error.
	Check(ctx context.Context) error
	// Release lets go of the guard. It is safe to call after a failed Check.
	Release()
}

// ErrLost is the guard's session having gone away while it was held.
var ErrLost = errors.New("refreshguard: the lock's database session was lost; the guard is no longer held")

// Exclusive is held by a credential migration for its whole run. It waits
// for every refresh in flight to finish and keeps new ones from starting.
func (g *Guard) Exclusive(ctx context.Context) (Held, error) {
	if g == nil || g.pool == nil {
		return unguarded{}, nil
	}
	conn, err := g.acquire(ctx, "pg_advisory_lock")
	if err != nil {
		return nil, err
	}
	h := &held{conn: conn, unlock: "pg_advisory_unlock"}
	// The backend this lock lives in, so a test can end it.
	if err := conn.QueryRow(ctx, "select pg_backend_pid()").Scan(&h.pid); err != nil {
		h.Release()
		return nil, fmt.Errorf("refreshguard: %w", err)
	}
	return h, nil
}

// unguarded is the guard of a process with no database.
type unguarded struct{}

func (unguarded) Check(context.Context) error { return nil }
func (unguarded) Release()                    {}

// held is a lock taken on a connection of its own.
type held struct {
	conn   *pgxpool.Conn
	unlock string
	pid    uint32
	once   sync.Once
}

// Check asks the lock's own session whether it still holds the lock. A
// session that answers holds it: a session-level advisory lock ends only
// with an unlock or with the session. Any failure to answer counts as lost,
// because the run cannot tell the difference and must not go on unguarded.
func (h *held) Check(ctx context.Context) error {
	var n int
	// A lock taken with one bigint key shows as its two halves, objsubid 1.
	err := h.conn.QueryRow(ctx, `select count(*) from pg_locks where locktype='advisory' and pid=pg_backend_pid() and granted
		and objsubid=1 and ((classid::bigint << 32) | objid::bigint) = $1`, key).Scan(&n)
	if err != nil {
		return fmt.Errorf("%w (%v)", ErrLost, err)
	}
	if n == 0 {
		return ErrLost
	}
	return nil
}

func (h *held) Release() {
	h.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := h.conn.Exec(ctx, "select "+h.unlock+"($1)", key); err != nil {
			_ = h.conn.Conn().Close(ctx)
		}
		h.conn.Release()
	})
}

// hold takes the lock on a connection of its own, which it keeps until
// release: an advisory lock belongs to the session that took it.
func (g *Guard) hold(ctx context.Context, lock, unlock string) (func(), error) {
	if g == nil || g.pool == nil {
		return func() {}, nil
	}
	conn, err := g.acquire(ctx, lock)
	if err != nil {
		return nil, err
	}
	return func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), "select "+unlock+"($1)", key); err != nil {
			_ = conn.Conn().Close(context.WithoutCancel(ctx))
		}
		conn.Release()
	}, nil
}

// acquire takes a connection out of the pool and the lock on it.
func (g *Guard) acquire(ctx context.Context, lock string) (*pgxpool.Conn, error) {
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
	return conn, nil
}

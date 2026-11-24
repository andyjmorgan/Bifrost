package refreshguard

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("MCPGW_TEST_DSN")
	if dsn == "" {
		t.Skip("set MCPGW_TEST_DSN to a throwaway Postgres to run the guard tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// The guard says so when the session its lock lives in is gone: a migration
// that carried on would be moving tokens with refreshes running again.
func TestTheGuardKnowsWhenItsSessionIsLost(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	h, err := New(pool).Exclusive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Release()
	if err := h.Check(ctx); err != nil {
		t.Fatalf("a guard that is held says it is not: %v", err)
	}

	// The database ends the session, as a restart or a cut connection would.
	var ended bool
	if err := pool.QueryRow(ctx, `select pg_terminate_backend($1)`, h.(*held).pid).Scan(&ended); err != nil || !ended {
		t.Fatalf("terminate the guard's session: %v %v", ended, err)
	}
	if err := h.Check(ctx); !errors.Is(err, ErrLost) {
		t.Fatalf("a guard whose session is gone answered %v, want ErrLost", err)
	}
	// And the lock really is free again: a refresh would get through.
	got := make(chan error, 1)
	go func() {
		release, err := New(pool).Shared(ctx)
		if err == nil {
			release()
		}
		got <- err
	}()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the lock of a lost session is still held")
	}
	h.Release() // after a failed Check: must not panic or hang
}

// A guard with no database guards nothing and never reports itself lost.
func TestANilGuardIsNeverLost(t *testing.T) {
	var g *Guard
	h, err := g.Exclusive(context.Background())
	if err != nil || h.Check(context.Background()) != nil {
		t.Fatalf("%v", err)
	}
	h.Release()
}

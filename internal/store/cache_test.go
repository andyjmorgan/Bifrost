package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/andyjmorgan/Bifrost/internal/db"
)

// The cache tests need a real Postgres: the point is the SQL. Set
// MCPGW_TEST_DSN to a throwaway database to run them.
func testStore(t *testing.T) *Postgres {
	t.Helper()
	dsn := os.Getenv("MCPGW_TEST_DSN")
	if dsn == "" {
		t.Skip("set MCPGW_TEST_DSN to a throwaway Postgres to run the store tests")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return NewPostgres(pool)
}

// A confirmation is redeemed with CacheTake. It must hand the value to
// exactly one caller, however many ask at once, and never hand out an
// expired one.
func TestCacheTakeGivesAValueToExactlyOneCaller(t *testing.T) {
	p := testStore(t)
	ctx := context.Background()
	const ns = "confirm-test"
	t.Cleanup(func() { _ = p.CacheDelete(ctx, ns, "") })

	if _, err := p.CacheTake(ctx, ns, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("taking a key that is not there: %v", err)
	}
	if err := p.CacheSet(ctx, ns, "k", nil, time.Time{}, json.RawMessage(`{"c":"one"}`), time.Minute); err != nil {
		t.Fatal(err)
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := p.CacheTake(ctx, ns, "k")
			if err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
				if string(v) != `{"c": "one"}` && string(v) != `{"c":"one"}` {
					t.Errorf("took %s", v)
				}
			} else if !errors.Is(err, ErrNotFound) {
				t.Errorf("take: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d callers took the value, want exactly 1", wins)
	}
	if _, err := p.CacheGet(ctx, ns, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the value is still there after it was taken: %v", err)
	}
	// An expired entry is not handed out.
	if err := p.CacheSet(ctx, ns, "old", nil, time.Time{}, json.RawMessage(`{}`), -time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CacheTake(ctx, ns, "old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an expired value was taken: %v", err)
	}
}

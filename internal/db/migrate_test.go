package db

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// scratchDatabase creates an empty database beside the one MCPGW_TEST_DSN
// names, and drops it when the test ends. The test is skipped without the
// variable: it needs a throwaway Postgres it may create databases on.
func scratchDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("MCPGW_TEST_DSN")
	if dsn == "" {
		t.Skip("set MCPGW_TEST_DSN to a throwaway Postgres to run the migration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	name := "mig_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	if _, err := admin.Exec(ctx, "create database "+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	// Cleanups run last in, first out: close this pool, then drop.
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "drop database if exists "+name+" with (force)") })
	t.Cleanup(pool.Close)
	return pool
}

// migrateBefore applies, as Migrate would, every migration that sorts
// before version.
func migrateBefore(t *testing.T, pool *pgxpool.Pool, version string) {
	t.Helper()
	ctx := context.Background()
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	if _, err := pool.Exec(ctx, `create table if not exists schema_migrations (version text primary key, applied_at timestamptz not null default now())`); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		v := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		if v >= version {
			break
		}
		sql, err := migrations.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("migration %s: %v", v, err)
		}
		if _, err := pool.Exec(ctx, `insert into schema_migrations (version) values ($1)`, v); err != nil {
			t.Fatal(err)
		}
	}
}

// 0017 adds the keyring credential kinds to a database that already holds
// servers. Every row from before must come through as it was, the old kinds
// must keep working, and a keyring credential must say whose keyring.
func TestKeyringCredentialsMigrationKeepsExistingRows(t *testing.T) {
	const version = "0017_keyring_credentials"
	pool := scratchDatabase(t)
	ctx := context.Background()
	migrateBefore(t, pool, version)

	// One server of each kind the gateway had, with its credential.
	type row struct {
		id   uuid.UUID
		kind string
		cols string // extra credential columns
		vals []any
	}
	before := []row{
		{kind: "none"},
		{kind: "basic", cols: "basic_username, secret_blob", vals: []any{"svc", []byte("sealed-basic")}},
		{kind: "header", cols: "header_name, secret_blob", vals: []any{"X-Api-Key", []byte("sealed-header")}},
		{kind: "oauth", cols: "oauth_issuer, oauth_client_id, oauth_registration_kind, secret_blob", vals: []any{"https://as.example", "client", "dcr", []byte("sealed-oauth")}},
	}
	for i := range before {
		r := &before[i]
		r.id = uuid.New()
		if _, err := pool.Exec(ctx, `insert into servers (id, slug, name, url, auth_kind) values ($1,$2,$3,$4,$5)`,
			r.id, "old-"+r.kind, r.kind, "https://"+r.kind+".example/mcp", r.kind); err != nil {
			t.Fatalf("%s server: %v", r.kind, err)
		}
		cols, marks, args := "server_id, kind", "$1,$2", []any{r.id, r.kind}
		if r.cols != "" {
			cols += ", " + r.cols
			for j, v := range r.vals {
				marks += fmt.Sprintf(",$%d", j+3)
				args = append(args, v)
			}
		}
		if _, err := pool.Exec(ctx, `insert into credentials (`+cols+`) values (`+marks+`)`, args...); err != nil {
			t.Fatalf("%s credential: %v", r.kind, err)
		}
	}
	snapshot := func() string {
		var out string
		if err := pool.QueryRow(ctx, `select coalesce(string_agg(s.slug || '|' || s.auth_kind || '|' || c.kind || '|' || c.state || '|' ||
			coalesce(c.basic_username,'') || '|' || coalesce(c.header_name,'') || '|' || coalesce(c.oauth_issuer,'') || '|' ||
			coalesce(c.oauth_client_id,'') || '|' || coalesce(encode(c.secret_blob,'hex'),''), ';' order by s.slug), '')
			from servers s join credentials c on c.server_id = s.id`).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	was := snapshot()

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if now := snapshot(); now != was {
		t.Fatalf("existing rows changed:\n was %s\n now %s", was, now)
	}
	var withRef int
	if err := pool.QueryRow(ctx, `select count(*) from credentials where keyring_owner is not null or keyring_provider is not null
		or keyring_account is not null or keyring_secret_name is not null`).Scan(&withRef); err != nil || withRef != 0 {
		t.Fatalf("%d existing rows gained a keyring reference (err %v)", withRef, err)
	}

	// The old kinds are still accepted and unknown ones still refused.
	exec := func(sql string, args ...any) error {
		_, err := pool.Exec(ctx, sql, args...)
		return err
	}
	if err := exec(`update servers set auth_kind='header' where id=$1`, before[0].id); err != nil {
		t.Fatalf("an old kind was refused after the migration: %v", err)
	}
	if err := exec(`update servers set auth_kind='vault' where id=$1`, before[0].id); err == nil {
		t.Fatal("an unknown auth kind was accepted")
	}
	if err := exec(`update credentials set kind='vault' where server_id=$1`, before[0].id); err == nil {
		t.Fatal("an unknown credential kind was accepted")
	}

	// The new kinds are accepted, with their reference and its owner.
	id := before[0].id
	if err := exec(`update servers set auth_kind='keyring_oauth' where id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := exec(`update credentials set kind='keyring_oauth' where server_id=$1`, id); err == nil {
		t.Fatal("a keyring OAuth credential was stored without its reference")
	}
	if err := exec(`update credentials set kind='keyring_oauth', keyring_provider='x', keyring_account='alice' where server_id=$1`, id); err == nil {
		t.Fatal("a keyring OAuth credential was stored without an owner")
	}
	if err := exec(`update credentials set kind='keyring_oauth', keyring_owner='u1', keyring_provider='x', keyring_account='alice' where server_id=$1`, id); err != nil {
		t.Fatalf("a keyring OAuth credential was refused: %v", err)
	}
	if err := exec(`update credentials set kind='keyring_secret', keyring_owner='u1', keyring_provider=null, keyring_account=null where server_id=$1`, id); err == nil {
		t.Fatal("a keyring secret credential was stored without a name")
	}
	if err := exec(`update credentials set kind='keyring_secret', keyring_owner='u1', keyring_secret_name='atlas', keyring_provider=null, keyring_account=null where server_id=$1`, id); err != nil {
		t.Fatalf("a keyring secret credential was refused: %v", err)
	}

	// Running it again changes nothing.
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

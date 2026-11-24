package store

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"
)

// sealedRow makes a server's credential a row as an older gateway left it:
// sealed in the gateway's own columns, of the given kind. The store has no
// way to write one any more, so the test does it with SQL. The blob is
// opaque bytes here; nothing in the store opens it.
func sealedRow(t *testing.T, p *Postgres, id uuid.UUID, kind AuthKind) {
	t.Helper()
	_, err := p.pool.Exec(context.Background(), `insert into credentials (server_id, kind, state, secret_blob, basic_username, header_name,
		oauth_token_endpoint, oauth_client_id, oauth_scopes, oauth_has_refresh_token, last_refreshed_at, migrated_from)
		values ($1,$2,'ok',$3,'svc','X-Api-Key','https://as.example/token','client','{mcp:tools}',true,now(),null)`, id, string(kind), []byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
}

// leftInOldColumns counts how many of the columns the gateway used to seal a
// credential in still hold something for a server.
func leftInOldColumns(t *testing.T, p *Postgres, id uuid.UUID) int {
	t.Helper()
	var n int
	err := p.pool.QueryRow(context.Background(), `select num_nonnulls(secret_blob, basic_username, header_name, oauth_issuer, oauth_authorization_endpoint,
		oauth_token_endpoint, oauth_registration_endpoint, oauth_registration_kind, oauth_client_id, oauth_resource, oauth_token_type, oauth_expires_at,
		last_refreshed_at, migrated_from) + cardinality(oauth_scopes) + oauth_has_refresh_token::int from credentials where server_id=$1`, id).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A credential still in the gateway's old store is listed at start, read as
// the kind it is, and left exactly as it is by everything except a new
// credential, which replaces it whole.
func TestACredentialInTheOldStoreIsListedAndOnlyReplacedWhole(t *testing.T) {
	p := testStore(t)
	ctx := context.Background()

	var (
		unmoved []string
		first   *Server // the Basic one
	)
	for _, kind := range []AuthKind{AuthBasic, AuthHeader, AuthOAuth} {
		s := newTestServer(t, p, kind)
		sealedRow(t, p, s.ID, kind)
		unmoved = append(unmoved, s.Slug)
		if first == nil {
			first = s
		}

		c, err := p.GetCredential(ctx, s.ID)
		if err != nil || c.Kind != kind || !c.Kind.Unmigrated() || c.Kind.FromKeyring() || c.State != StateOK {
			t.Fatalf("%s: read as %+v (%v)", kind, c, err)
		}
	}
	// Servers the keyring holds a credential for, or that have none, are not listed.
	moved := newTestServer(t, p, AuthKeyringSecret)
	if err := p.UpsertCredential(ctx, &Credential{ServerID: moved.ID, Kind: AuthKeyringSecret, State: StateOK, KeyringOwner: "owner", KeyringSecretName: "mcp-" + moved.Slug}); err != nil {
		t.Fatal(err)
	}
	none := newTestServer(t, p, AuthNone)
	if err := p.UpsertCredential(ctx, &Credential{ServerID: none.ID, Kind: AuthNone, State: StateOK}); err != nil {
		t.Fatal(err)
	}
	bare := newTestServer(t, p, AuthNone) // no credential row at all

	listed := func() []string {
		t.Helper()
		got, err := p.UnmigratedCredentials(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := listed()
	for _, slug := range unmoved {
		if !slices.Contains(got, slug) {
			t.Fatalf("%s is not listed as unmoved: %v", slug, got)
		}
	}
	for _, s := range []*Server{moved, none, bare} {
		if slices.Contains(got, s.Slug) {
			t.Fatalf("%s (%s) is listed as unmoved", s.Slug, s.AuthKind)
		}
	}

	// Marking the credential, or editing the server, is not a new credential:
	// the sealed columns stay for `gateway migrate-credentials`.
	before := leftInOldColumns(t, p, first.ID)
	if before == 0 {
		t.Fatal("the seeded row holds nothing in the old columns")
	}
	if err := p.SetCredentialState(ctx, first.ID, StateBroken, "refused"); err != nil {
		t.Fatal(err)
	}
	first.Description = "edited"
	if err := p.UpdateServer(ctx, first); err != nil {
		t.Fatal(err)
	}
	if after := leftInOldColumns(t, p, first.ID); after != before {
		t.Fatalf("an edit that was not a new credential changed the old columns: %d of them hold something, was %d", after, before)
	}
	if c, err := p.GetCredential(ctx, first.ID); err != nil || c.Kind != AuthBasic || c.State != StateBroken || c.StateReason != "refused" || c.LastError != "refused" {
		t.Fatalf("the state was not stored on the unmoved row: %+v (%v)", c, err)
	}

	// A new credential replaces the row whole: nothing sealed is left beside it.
	if err := p.UpsertCredential(ctx, &Credential{ServerID: first.ID, Kind: AuthKeyringSecret, State: StateOK, KeyringOwner: "owner", KeyringSecretName: "mcp-" + first.Slug}); err != nil {
		t.Fatal(err)
	}
	if left := leftInOldColumns(t, p, first.ID); left != 0 {
		t.Fatalf("a replaced credential left %d of the old columns filled", left)
	}
	if c, err := p.GetCredential(ctx, first.ID); err != nil || c.Kind != AuthKeyringSecret || c.State != StateOK || c.StateReason != "" || c.LastError != "" || c.KeyringSecretName != "mcp-"+first.Slug {
		t.Fatalf("the new credential reads as %+v (%v)", c, err)
	}
	if slices.Contains(listed(), first.Slug) {
		t.Fatal("a server whose credential was replaced is still listed as unmoved")
	}
}

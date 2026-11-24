package credmigrate_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/credmigrate"
	"github.com/andyjmorgan/Bifrost/internal/keyringref"
	"github.com/andyjmorgan/Bifrost/internal/store"
)

// hookVault is the keyring with places for a test to act as somebody else
// would at that moment of a run.
type hookVault struct {
	credmigrate.Vault
	beforeSnapshot    func(slug string) // before the run reads what the keyring holds for a server
	beforeImport      func(slug string) // before the copy is written
	afterImport       func(slug string) // after the copy is written, before the switch
	beforeImportOAuth func(slug string)
}

func (v *hookVault) HasSecret(ctx context.Context, ref keyringref.Ref) (bool, error) {
	if v.beforeSnapshot != nil {
		v.beforeSnapshot(strings.TrimPrefix(ref.Secret, "mcp-"))
	}
	return v.Vault.HasSecret(ctx, ref)
}

func (v *hookVault) ImportSecret(ctx context.Context, owner, tenant uuid.UUID, use keyringref.Use, in keyringref.ServerSecret) (keyringref.Ref, error) {
	if v.beforeImport != nil {
		v.beforeImport(use.Slug)
	}
	ref, err := v.Vault.ImportSecret(ctx, owner, tenant, use, in)
	if v.afterImport != nil {
		v.afterImport(use.Slug)
	}
	return ref, err
}

func (v *hookVault) ImportOAuth(ctx context.Context, owner, tenant uuid.UUID, use keyringref.Use, c keyringref.ServerConnection) (keyringref.Ref, error) {
	if v.beforeImportOAuth != nil {
		v.beforeImportOAuth(use.Slug)
	}
	return v.Vault.ImportOAuth(ctx, owner, tenant, use, c)
}

// exec runs SQL as a running gateway, or a person at psql, would.
func (r *rig) exec(sql string, args ...any) {
	r.t.Helper()
	if _, err := r.pool.Exec(context.Background(), sql, args...); err != nil {
		r.t.Fatal(err)
	}
}

// gatewayStores is the first half of a running gateway replacing a server's
// credential with a new header value: the value goes into the keyring under
// the server's name.
func (r *rig) gatewayStores(slug, value string) {
	r.t.Helper()
	if _, err := r.kr.Source().PutServerSecret(context.Background(), keyringref.Use{Slug: slug},
		keyringref.ServerSecret{Scheme: keyringref.SchemeHeader, HeaderName: "X-Api-Key", Value: value}); err != nil {
		r.t.Fatal(err)
	}
}

// gatewayPoints is the second half: the row becomes the reference.
func (r *rig) gatewayPoints(id uuid.UUID, slug string) {
	r.t.Helper()
	r.exec(`update credentials set kind='keyring_secret', keyring_owner=$2, keyring_secret_name=$3, secret_blob=null, basic_username=null, header_name=null,
		migrated_from=null, updated_at=now() where server_id=$1`, id, keyringref.GatewayOwner, "mcp-"+slug)
	r.exec(`update servers set auth_kind='keyring_secret' where id=$1`, id)
}

func (r *rig) headerServer(name string) uuid.UUID {
	return r.server(name, credRow{Kind: store.AuthHeader, HeaderName: "X-Api-Key"}, &sealed{HeaderValue: headerValue})
}

// A rollback takes back what the command put in the keyring and nothing
// else. A server that was moved can afterwards be pointed at a secret in
// somebody's own keyring, and its row may still say it was moved; that
// secret is theirs. The rollback must not read it out into the gateway's
// row, and must not delete it.
func TestRollbackNeverTakesFromAPersonsKeyring(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	lent := r.server("lent-later", credRow{Kind: store.AuthBasic, BasicUsername: "svc"}, &sealed{Password: password})
	samename := r.server("same-name", credRow{Kind: store.AuthBasic, BasicUsername: "svc"}, &sealed{Password: password})
	chosen := r.headerServer("chosen")
	as := newAuthServer(t)
	oauth, _ := r.oauthServer("oauth-lent", as, "", time.Now().Add(time.Hour))
	if report, sum, err := r.run(credmigrate.Options{Kind: credmigrate.KindStatic}, nil); err != nil || sum.Moved != 3 {
		t.Fatalf("static move: %v %+v\n%s", err, sum, report)
	}
	if report, sum, err := r.run(credmigrate.Options{Kind: credmigrate.KindOAuth}, nil); err != nil || sum.Moved != 1 {
		t.Fatalf("oauth move: %v %+v\n%s", err, sum, report)
	}

	// Alice has two secrets of her own: one under a name she chose, and one
	// that happens to carry the name the move gives the second server.
	owner, tenant := uuid.MustParse(alice), uuid.New()
	mine := keyringref.ServerSecret{Scheme: keyringref.SchemeHeader, HeaderName: "X-Alice", Value: replaced}
	for _, name := range []string{"alices-own", r.slug("same-name")} {
		if _, err := r.kr.Migration().ImportSecret(ctx, owner, tenant, keyringref.Use{Slug: name}, mine); err != nil {
			t.Fatal(err)
		}
	}
	// The servers are pointed at them, as the servers API does it at the
	// commit that keeps migrated_from on a replaced row.
	r.exec(`update credentials set keyring_owner=$2, keyring_secret_name='mcp-alices-own', updated_at=now() where server_id=$1`, lent, alice)
	r.exec(`update credentials set keyring_owner=$2, updated_at=now() where server_id=$1`, samename, alice)
	// A third is pointed at another entry of the gateway's own space, and
	// the OAuth server at an account in Alice's keyring.
	r.exec(`update credentials set keyring_secret_name='something-else', updated_at=now() where server_id=$1`, chosen)
	r.exec(`update credentials set keyring_owner=$2, keyring_provider='x', keyring_account='alice', updated_at=now() where server_id=$1`, oauth, alice)
	if r.migratedFrom(lent) != "basic" || r.migratedFrom(oauth) != "oauth" {
		t.Fatal("the rows do not look moved; the test would prove nothing")
	}

	for _, includeNew := range []bool{false, true} {
		report, sum, err := r.run(credmigrate.Options{Kind: credmigrate.KindStatic, Rollback: true, IncludeNew: includeNew}, nil)
		if err != nil || sum.Moved != 0 || sum.Failed != 0 || sum.Skipped != 4 {
			t.Fatalf("static rollback (include new %v): %v %+v\n%s", includeNew, err, sum, report)
		}
		if got := lines(report, "skip"); len(got) != 4 || !strings.Contains(report, "lent from a user's keyring") || !strings.Contains(report, "somebody chose it") {
			t.Fatalf("the report does not say why each was left:\n%s", report)
		}
		report, sum, err = r.run(credmigrate.Options{Kind: credmigrate.KindOAuth, Rollback: true, IncludeNew: includeNew}, nil)
		if err != nil || sum.Moved != 0 || sum.Failed != 0 || !strings.Contains(report, "lent from a user's keyring") {
			t.Fatalf("oauth rollback (include new %v): %v %+v\n%s", includeNew, err, sum, report)
		}
	}
	// Alice's secrets are where they were, and the rows still point at them.
	for _, name := range []string{"mcp-alices-own", "mcp-" + r.slug("same-name")} {
		if h, err := r.held(alice, name); err != nil || h.Name != "X-Alice" || h.Value != replaced {
			t.Fatalf("Alice's secret %s was taken or changed: %v", name, err)
		}
	}
	for name, id := range map[string]uuid.UUID{"lent": lent, "same name": samename, "chosen": chosen} {
		if c := r.cred(id); c.Kind != store.AuthKeyringSecret || len(c.SecretBlob) != 0 {
			t.Fatalf("%s: the row was rolled back to %s with %d sealed bytes", name, c.Kind, len(c.SecretBlob))
		}
	}
	if c := r.cred(oauth); c.Kind != store.AuthKeyringOAuth || c.KeyringOwner != alice || len(c.SecretBlob) != 0 {
		t.Fatalf("the OAuth row was rolled back to %s", c.Kind)
	}
}

// A server whose credential is replaced after the run has listed it, and
// before its turn, is not moved from the stale listing: the keyring keeps
// the new value.
func TestACredentialReplacedBeforeItsTurnIsLeftAlone(t *testing.T) {
	r := newRig(t)
	r.headerServer("a")
	b := r.headerServer("b")
	v := &hookVault{Vault: r.kr.Migration()}
	v.beforeImport = func(slug string) {
		if slug == r.slug("a") { // while the first server is being copied, an operator replaces the second's credential
			r.gatewayStores(r.slug("b"), replaced)
			r.gatewayPoints(b, r.slug("b"))
		}
	}
	report, sum, err := r.run(credmigrate.Options{Kind: credmigrate.KindStatic}, v)
	if err != nil || sum.Moved != 1 || sum.Failed != 0 {
		t.Fatalf("%v %+v\n%s", err, sum, report)
	}
	if h, err := r.held(keyringref.GatewayOwner, "mcp-"+r.slug("b")); err != nil || h.Value != replaced {
		t.Fatalf("the keyring does not hold the server's new credential: %v", err)
	}
	if c := r.cred(b); c.Kind != store.AuthKeyringSecret || r.migratedFrom(b) != "" {
		t.Fatalf("the replaced server's row was rewritten: %s", c.Kind)
	}
}

// A server deleted while it is being copied leaves nothing behind in the
// keyring.
func TestAServerDeletedDuringTheRunLeavesNoCopy(t *testing.T) {
	r := newRig(t)
	a := r.headerServer("a")
	v := &hookVault{Vault: r.kr.Migration()}
	v.afterImport = func(string) { r.exec(`delete from servers where id=$1`, a) }
	report, sum, err := r.run(credmigrate.Options{Kind: credmigrate.KindStatic}, v)
	if !errors.Is(err, credmigrate.ErrFailed) || sum.Moved != 0 || sum.Failed != 1 || !strings.Contains(report, "changed during the run") {
		t.Fatalf("%v %+v\n%s", err, sum, report)
	}
	if _, err := r.held(keyringref.GatewayOwner, "mcp-"+r.slug("a")); !errors.Is(err, keyringref.ErrNotFound) {
		t.Fatalf("the copy of a deleted server's credential is still in the keyring: %v", err)
	}
}

// A server given a new credential while its old one is being copied: the
// gateway stored the new value under the server's name, the copy overwrote
// it, and then the row changed. The switch is not made, and the keyring is
// put back to the new value, which the run had read before it copied.
func TestACredentialReplacedDuringItsCopyIsPutBack(t *testing.T) {
	r := newRig(t)
	a := r.headerServer("a")
	v := &hookVault{Vault: r.kr.Migration()}
	v.beforeSnapshot = func(string) { r.gatewayStores(r.slug("a"), replaced) }
	v.afterImport = func(string) { r.gatewayPoints(a, r.slug("a")) }
	report, sum, err := r.run(credmigrate.Options{Kind: credmigrate.KindStatic}, v)
	if !errors.Is(err, credmigrate.ErrFailed) || sum.Failed != 1 || !strings.Contains(report, "changed during the run") || !strings.Contains(report, "put back") {
		t.Fatalf("%v %+v\n%s", err, sum, report)
	}
	if h, err := r.held(keyringref.GatewayOwner, "mcp-"+r.slug("a")); err != nil || h.Value != replaced {
		t.Fatalf("the keyring holds the old credential under the server's name: %v", err)
	}
	if r.migratedFrom(a) != "" {
		t.Fatal("the row was switched although it had changed")
	}
	// The next run finds nothing to do for it.
	if report, sum, err = r.run(credmigrate.Options{Kind: credmigrate.KindStatic}, nil); err != nil || sum.Moved != 0 || sum.Failed != 0 {
		t.Fatalf("second run: %v %+v\n%s", err, sum, report)
	}
}

// An OAuth consent that lands while the connection is being copied: the row
// gains its tokens after the run read it without any. The run must not
// switch to its copy, which has none, and clear the row. It leaves the row,
// removes the copy, and the next run moves the connection with its tokens.
func TestAConsentThatLandsDuringTheRunIsNotLost(t *testing.T) {
	r := newRig(t)
	id := r.server("pending", credRow{Kind: store.AuthOAuth, OAuthTokenEndpoint: "https://as.example/token", OAuthRegistrationKind: "cimd",
		OAuthClientID: "https://gateway.test/.well-known/mcp-client-metadata.json", OAuthResource: "https://pending.example/mcp"}, &sealed{})
	r.exec(`update credentials set state='pending', state_reason='awaiting consent' where server_id=$1`, id)
	access, refresh := "access-"+uuid.NewString(), "refresh-"+uuid.NewString()

	v := &hookVault{Vault: r.kr.Migration()}
	v.beforeImportOAuth = func(string) {
		b, _ := json.Marshal(sealed{AccessToken: access, RefreshToken: refresh})
		blob, err := r.sealer.Seal(b, id[:])
		if err != nil {
			t.Fatal(err)
		}
		// What the gateway's callback writes when the code is redeemed.
		r.exec(`update credentials set secret_blob=$2, state='ok', state_reason=null, oauth_has_refresh_token=true, last_refreshed_at=now(), updated_at=now() where server_id=$1`, id, blob)
		v.beforeImportOAuth = nil
	}
	report, sum, err := r.run(credmigrate.Options{Kind: credmigrate.KindOAuth}, v)
	if !errors.Is(err, credmigrate.ErrFailed) || sum.Moved != 0 || sum.Failed != 1 || !strings.Contains(report, "changed during the run") {
		t.Fatalf("%v %+v\n%s", err, sum, report)
	}
	if strings.Contains(report, access) || strings.Contains(report, refresh) {
		t.Fatal("the report carries a token")
	}
	if c := r.cred(id); c.Kind != store.AuthOAuth || r.opened(id).RefreshToken != refresh {
		t.Fatalf("the row lost the tokens the consent had just stored: %s", c.Kind)
	}
	ref := keyringref.Ref{Owner: keyringref.GatewayOwner, Kind: keyringref.KindOAuth, Provider: "mcp-" + r.slug("pending"), Account: keyringref.ServerAccount}
	if _, err := r.kr.Migration().ExportOAuth(context.Background(), ref, keyringref.Use{}); !errors.Is(err, keyringref.ErrNotFound) {
		t.Fatalf("the copy without tokens was left in the keyring: %v", err)
	}
	// The next run moves the connection as it now is.
	if report, sum, err = r.run(credmigrate.Options{Kind: credmigrate.KindOAuth}, nil); err != nil || sum.Moved != 1 {
		t.Fatalf("second run: %v %+v\n%s", err, sum, report)
	}
	if c := r.exported(r.slug("pending")); !c.Connected || c.AccessToken != access || c.RefreshToken != refresh {
		t.Fatal("the connection did not arrive with its tokens")
	}
}

// guardKey is refreshguard's advisory lock key, to find the session that
// holds it.
const guardKey int64 = 0x626966726f737463

// A run whose refresh guard is lost stops there. The guard is one database
// session; when it ends, refreshes run again, and a token copied after that
// could be retired under the copy. The server in hand is left as it was,
// with no copy behind, and the next run carries on.
func TestTheRunStopsWhenTheGuardIsLost(t *testing.T) {
	r := newRig(t)
	a := r.headerServer("a")
	b := r.headerServer("b")
	c := r.headerServer("c")
	v := &hookVault{Vault: r.kr.Migration()}
	v.beforeImport = func(slug string) {
		if slug != r.slug("b") {
			return
		}
		// The database ends the session the guard lives in, as a restart or
		// a cut connection would, while the second server is being copied.
		var ended bool
		err := r.pool.QueryRow(context.Background(), `select pg_terminate_backend(pid) from pg_locks where locktype='advisory' and mode='ExclusiveLock' and granted
			and objsubid=1 and ((classid::bigint << 32) | objid::bigint) = $1 and database = (select oid from pg_database where datname = current_database())`, guardKey).Scan(&ended)
		if err != nil || !ended {
			t.Errorf("end the guard's session: %v %v", ended, err)
		}
	}
	report, sum, err := r.run(credmigrate.Options{Kind: credmigrate.KindStatic}, v)
	if !errors.Is(err, credmigrate.ErrGuardLost) || sum.Moved != 1 || !strings.Contains(report, "refresh guard was lost") {
		t.Fatalf("%v %+v\n%s", err, sum, report)
	}
	if r.cred(a).Kind != store.AuthKeyringSecret {
		t.Fatal("the server moved before the guard was lost is not moved")
	}
	for name, id := range map[string]uuid.UUID{"b": b, "c": c} {
		if got := r.cred(id); got.Kind != store.AuthHeader || r.opened(id).HeaderValue != headerValue {
			t.Fatalf("server %s was touched after the guard was lost: %s", name, got.Kind)
		}
		if _, err := r.held(keyringref.GatewayOwner, "mcp-"+r.slug(name)); !errors.Is(err, keyringref.ErrNotFound) {
			t.Fatalf("server %s has a copy in the keyring: %v", name, err)
		}
	}
	// Run again: it takes the guard anew and finishes.
	if report, sum, err = r.run(credmigrate.Options{Kind: credmigrate.KindStatic}, nil); err != nil || sum.Moved != 2 || sum.Already != 1 {
		t.Fatalf("second run: %v %+v\n%s", err, sum, report)
	}
}

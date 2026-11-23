package credmigrate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andyjmorgan/Bifrost/internal/credmigrate"
	"github.com/andyjmorgan/Bifrost/internal/crypto"
	"github.com/andyjmorgan/Bifrost/internal/db"
	"github.com/andyjmorgan/Bifrost/internal/keyring"
	krcontracts "github.com/andyjmorgan/Bifrost/internal/keyring/contracts"
	"github.com/andyjmorgan/Bifrost/internal/keyring/store/memstore"
	"github.com/andyjmorgan/Bifrost/internal/keyringref"
	"github.com/andyjmorgan/Bifrost/internal/refreshguard"
	"github.com/andyjmorgan/Bifrost/internal/store"
)

// Values a test stores. Assembled at run time so nothing credential-shaped
// sits in the source.
var (
	password    = strings.Join([]string{"basic", "password", "fixture"}, "-")
	headerValue = strings.Join([]string{"header", "value", "fixture"}, "-")
	replaced    = strings.Join([]string{"replaced", "in", "keyring"}, "-")
)

const alice = "5f0e7c1a-3b1d-4c55-8a40-1d2b3c4d5e61"

var gatewayOwner = uuid.MustParse(keyringref.GatewayOwner)

// rig is a gateway database with a real sealer, and the real keyring on its
// in-memory store.
type rig struct {
	t      *testing.T
	pool   *pgxpool.Pool
	st     *store.Postgres
	sealer *crypto.Sealer
	kr     *keyring.Keyring
	ms     *memstore.Mem
	tag    string // prefix of this test's slugs
	slugs  []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dsn := os.Getenv("MCPGW_TEST_DSN")
	if dsn == "" {
		t.Skip("set MCPGW_TEST_DSN to a throwaway Postgres to run the migration tests")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	sealer, err := crypto.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ms := memstore.New()
	kr, err := keyring.New(ctx, &keyring.Config{
		ActiveKekID: "local:v1", Keks: map[string]string{"local:v1": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
		OAuthRedirectBaseURL: "https://gateway.test/keyring", UIBaseURL: "https://gateway.test", AuditChannelCap: 10000,
	}, ms, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, pool: pool, st: store.NewPostgres(pool), sealer: sealer, kr: kr, ms: ms, tag: "m" + uuid.NewString()[:6]}
}

// server registers a server whose credential is cred, sealing sec into it
// when given, and removes it when the test ends.
func (r *rig) server(name string, cred store.Credential, sec *store.Secret) uuid.UUID {
	r.t.Helper()
	ctx := context.Background()
	now := time.Now()
	s := &store.Server{ID: uuid.New(), Slug: r.tag + "-" + name, Name: name, URL: "https://" + name + ".example/mcp", AuthKind: cred.Kind, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := r.st.CreateServer(ctx, s, alice); err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = r.st.DeleteServer(context.Background(), s.ID) })
	r.slugs = append(r.slugs, s.Slug)
	cred.ServerID, cred.State, cred.OAuthScopes = s.ID, store.StateOK, []string{}
	if sec != nil {
		b, _ := json.Marshal(sec)
		blob, err := r.sealer.Seal(b, s.ID[:])
		if err != nil {
			r.t.Fatal(err)
		}
		cred.SecretBlob = blob
	}
	if err := r.st.UpsertCredential(ctx, &cred); err != nil {
		r.t.Fatal(err)
	}
	return s.ID
}

func (r *rig) slug(name string) string { return r.tag + "-" + name }

// run runs the command over this test's servers and returns its report.
func (r *rig) run(o credmigrate.Options, v credmigrate.Vault) (string, credmigrate.Summary, error) {
	r.t.Helper()
	var out bytes.Buffer
	if v == nil {
		v = r.kr.Migration()
	}
	if o.Owner == uuid.Nil {
		o.Owner = gatewayOwner
	}
	o.Out, o.Servers = &out, r.slugs
	sum, err := credmigrate.Run(context.Background(), credmigrate.Deps{Pool: r.pool, Sealer: r.sealer, Vault: v, Guard: refreshguard.New(r.pool)}, o)
	for _, secret := range []string{password, headerValue, replaced} {
		if strings.Contains(out.String(), secret) {
			r.t.Fatalf("the report carries a credential:\n%s", out.String())
		}
	}
	return out.String(), sum, err
}

func (r *rig) cred(id uuid.UUID) *store.Credential {
	r.t.Helper()
	c, err := r.st.GetCredential(context.Background(), id)
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

func (r *rig) authKind(id uuid.UUID) store.AuthKind {
	r.t.Helper()
	s, err := r.st.GetServer(context.Background(), id)
	if err != nil {
		r.t.Fatal(err)
	}
	return s.AuthKind
}

func (r *rig) migratedFrom(id uuid.UUID) string {
	r.t.Helper()
	var from *string
	if err := r.pool.QueryRow(context.Background(), `select migrated_from from credentials where server_id=$1`, id).Scan(&from); err != nil {
		r.t.Fatal(err)
	}
	if from == nil {
		return ""
	}
	return *from
}

// opened unseals a server's blob.
func (r *rig) opened(id uuid.UUID) store.Secret {
	r.t.Helper()
	pt, err := r.sealer.Open(r.cred(id).SecretBlob, id[:])
	if err != nil {
		r.t.Fatalf("the blob does not open: %v", err)
	}
	var s store.Secret
	if err := json.Unmarshal(pt, &s); err != nil {
		r.t.Fatal(err)
	}
	return s
}

// held reads a secret out of the keyring, as the gateway would send it.
func (r *rig) held(owner, name string) (keyringref.Header, error) {
	return r.kr.Source().SecretHeader(context.Background(), keyringref.Ref{Owner: owner, Kind: keyringref.KindSecret, Secret: name}, keyringref.Use{Slug: "test"})
}

// everyKind seeds one server of each kind and returns their ids by name.
func (r *rig) everyKind() map[string]uuid.UUID {
	return map[string]uuid.UUID{
		"basic":  r.server("basic", store.Credential{Kind: store.AuthBasic, BasicUsername: "svc"}, &store.Secret{Password: password}),
		"header": r.server("header", store.Credential{Kind: store.AuthHeader, HeaderName: "X-Api-Key"}, &store.Secret{HeaderValue: headerValue}),
		"oauth": r.server("oauth", store.Credential{Kind: store.AuthOAuth, OAuthTokenEndpoint: "https://as.example/token", OAuthClientID: "c"},
			&store.Secret{AccessToken: "a", RefreshToken: "r"}),
		"lent": r.server("lent", store.Credential{Kind: store.AuthKeyringSecret, KeyringOwner: alice, KeyringSecretName: "atlas"}, nil),
	}
}

func lines(report, status string) []string {
	var out []string
	for _, l := range strings.Split(report, "\n") {
		if strings.HasPrefix(l, status+" ") {
			out = append(out, l)
		}
	}
	return out
}

// A dry run names what it would move and changes nothing.
func TestDryRunChangesNothing(t *testing.T) {
	r := newRig(t)
	ids := r.everyKind()
	report, sum, err := r.run(credmigrate.Options{Kind: credmigrate.KindStatic, DryRun: true}, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, report)
	}
	if sum.Moved != 2 || sum.Skipped != 2 || sum.Failed != 0 {
		t.Fatalf("summary %+v\n%s", sum, report)
	}
	if got := lines(report, "would move"); len(got) != 2 || !strings.Contains(report, `keyring secret "mcp-`+r.slug("basic")+`"`) {
		t.Fatalf("the report does not name what would move:\n%s", report)
	}
	if !strings.Contains(report, "dry run") || !strings.Contains(report, "keyring owner: the gateway") {
		t.Fatalf("the report does not say it is a dry run for the gateway's space:\n%s", report)
	}
	for name, kind := range map[string]store.AuthKind{"basic": store.AuthBasic, "header": store.AuthHeader, "oauth": store.AuthOAuth, "lent": store.AuthKeyringSecret} {
		if got := r.cred(ids[name]).Kind; got != kind {
			t.Fatalf("%s became %s in a dry run", name, got)
		}
	}
	if _, err := r.held(keyringref.GatewayOwner, "mcp-"+r.slug("basic")); !errors.Is(err, keyringref.ErrNotFound) {
		t.Fatalf("a dry run wrote to the keyring: %v", err)
	}
}

// The move, a second run, and the rollback, over one server of every kind.
func TestStaticCredentialsMoveAndComeBack(t *testing.T) {
	r := newRig(t)
	ids := r.everyKind()

	report, sum, err := r.run(credmigrate.Options{Kind: credmigrate.KindStatic}, nil)
	if err != nil || sum.Moved != 2 || sum.Skipped != 2 {
		t.Fatalf("move: %v %+v\n%s", err, sum, report)
	}
	for name, from := range map[string]string{"basic": "basic", "header": "header"} {
		c := r.cred(ids[name])
		want := keyringref.Ref{Owner: keyringref.GatewayOwner, Kind: keyringref.KindSecret, Secret: "mcp-" + r.slug(name)}
		if c.Kind != store.AuthKeyringSecret || c.KeyringRef() != want || r.authKind(ids[name]) != store.AuthKeyringSecret {
			t.Fatalf("%s is %s %+v after the move", name, c.Kind, c.KeyringRef())
		}
		if len(c.SecretBlob) != 0 || c.BasicUsername != "" || c.HeaderName != "" {
			t.Fatalf("%s kept part of its credential in the gateway's store", name)
		}
		if got := r.migratedFrom(ids[name]); got != from {
			t.Fatalf("%s remembers it was %q, want %q", name, got, from)
		}
	}
	if h, err := r.held(keyringref.GatewayOwner, "mcp-"+r.slug("header")); err != nil || h.Name != "X-Api-Key" || h.Value != headerValue {
		t.Fatalf("the header credential is not in the keyring as it was: %q (%v)", h.Name, err)
	}
	if h, err := r.held(keyringref.GatewayOwner, "mcp-"+r.slug("basic")); err != nil || h.Name != "Authorization" || !strings.HasPrefix(h.Value, "Basic ") {
		t.Fatalf("the Basic credential is not in the keyring as it was: %q (%v)", h.Name, err)
	}
	// What was not the command's to move is as it was.
	if c := r.cred(ids["oauth"]); c.Kind != store.AuthOAuth || len(c.SecretBlob) == 0 {
		t.Fatal("the OAuth credential was touched by a static run")
	}
	if c := r.cred(ids["lent"]); c.KeyringOwner != alice || r.migratedFrom(ids["lent"]) != "" {
		t.Fatal("a reference into a person's keyring was touched")
	}

	// A second run has nothing to do.
	report, sum, err = r.run(credmigrate.Options{Kind: credmigrate.KindStatic}, nil)
	if err != nil || sum.Moved != 0 || sum.Already != 2 {
		t.Fatalf("second run: %v %+v\n%s", err, sum, report)
	}

	// The header value is replaced in the keyring, as the UI would. The
	// rollback must bring back that value, not the one that was moved.
	if _, err := r.kr.Source().PutServerSecret(context.Background(), keyringref.Use{Slug: r.slug("header")}, keyringref.ServerSecret{Scheme: keyringref.SchemeHeader, HeaderName: "X-Api-Key", Value: replaced}); err != nil {
		t.Fatal(err)
	}

	report, sum, err = r.run(credmigrate.Options{Kind: credmigrate.KindStatic, Rollback: true, DryRun: true}, nil)
	if err != nil || sum.Moved != 2 || len(lines(report, "would restore")) != 2 || r.cred(ids["basic"]).Kind != store.AuthKeyringSecret {
		t.Fatalf("rollback dry run: %v %+v\n%s", err, sum, report)
	}
	report, sum, err = r.run(credmigrate.Options{Kind: credmigrate.KindStatic, Rollback: true}, nil)
	if err != nil || sum.Moved != 2 || sum.Skipped != 2 {
		t.Fatalf("rollback: %v %+v\n%s", err, sum, report)
	}
	if c := r.cred(ids["basic"]); c.Kind != store.AuthBasic || c.BasicUsername != "svc" || c.KeyringOwner != "" || r.authKind(ids["basic"]) != store.AuthBasic || r.opened(ids["basic"]).Password != password {
		t.Fatalf("the Basic credential did not come back as it was: %s", c.Kind)
	}
	if c := r.cred(ids["header"]); c.Kind != store.AuthHeader || c.HeaderName != "X-Api-Key" || r.opened(ids["header"]).HeaderValue != replaced {
		t.Fatalf("the header credential did not come back with the keyring's value: %s", c.Kind)
	}
	if r.migratedFrom(ids["basic"]) != "" {
		t.Fatal("a restored row still says it was moved")
	}
	for _, name := range []string{"basic", "header"} {
		if _, err := r.held(keyringref.GatewayOwner, "mcp-"+r.slug(name)); !errors.Is(err, keyringref.ErrNotFound) {
			t.Fatalf("the keyring still holds %s after the rollback: %v", name, err)
		}
	}
	if c := r.cred(ids["lent"]); c.Kind != store.AuthKeyringSecret || c.KeyringOwner != alice {
		t.Fatal("the rollback took a reference into a person's keyring")
	}
	// And a second rollback has nothing to do.
	if report, sum, err = r.run(credmigrate.Options{Kind: credmigrate.KindStatic, Rollback: true}, nil); err != nil || sum.Moved != 0 || sum.Already != 2 {
		t.Fatalf("second rollback: %v %+v\n%s", err, sum, report)
	}
}

// failingVault is the keyring with a fault a test turns on.
type failingVault struct {
	credmigrate.Vault
	failImport func(slug string) bool
	garble     bool
}

func (v *failingVault) ImportSecret(ctx context.Context, owner, tenant uuid.UUID, use keyringref.Use, in keyringref.ServerSecret) (keyringref.Ref, error) {
	if v.failImport != nil && v.failImport(use.Slug) {
		return keyringref.Ref{}, errors.New("the keyring is unreachable")
	}
	return v.Vault.ImportSecret(ctx, owner, tenant, use, in)
}

func (v *failingVault) ExportSecret(ctx context.Context, ref keyringref.Ref, use keyringref.Use) (keyringref.ServerSecret, error) {
	out, err := v.Vault.ExportSecret(ctx, ref, use)
	if v.garble {
		out.Value += "x"
	}
	return out, err
}

// A run that fails part way leaves each server whole, on one side or the
// other, and the next run finishes the job.
func TestAnInterruptedRunResumes(t *testing.T) {
	r := newRig(t)
	ids := r.everyKind()
	v := &failingVault{Vault: r.kr.Migration(), failImport: func(slug string) bool { return slug == r.slug("header") }}

	report, sum, err := r.run(credmigrate.Options{Kind: credmigrate.KindStatic}, v)
	if !errors.Is(err, credmigrate.ErrFailed) || sum.Moved != 1 || sum.Failed != 1 {
		t.Fatalf("first run: %v %+v\n%s", err, sum, report)
	}
	if r.cred(ids["basic"]).Kind != store.AuthKeyringSecret {
		t.Fatal("the server before the failure was not moved")
	}
	if c := r.cred(ids["header"]); c.Kind != store.AuthHeader || r.opened(ids["header"]).HeaderValue != headerValue {
		t.Fatal("the server that failed is not as it was")
	}

	v.failImport = nil
	report, sum, err = r.run(credmigrate.Options{Kind: credmigrate.KindStatic}, v)
	if err != nil || sum.Moved != 1 || sum.Already != 1 {
		t.Fatalf("resumed run: %v %+v\n%s", err, sum, report)
	}
	if h, err := r.held(keyringref.GatewayOwner, "mcp-"+r.slug("header")); err != nil || h.Value != headerValue {
		t.Fatalf("the resumed server is not in the keyring: %v", err)
	}
}

// A copy that does not read back the same is not trusted: the gateway's row
// stays, sealed blob and all.
func TestACopyThatDoesNotMatchIsNotSwitchedTo(t *testing.T) {
	r := newRig(t)
	ids := r.everyKind()
	report, sum, err := r.run(credmigrate.Options{Kind: credmigrate.KindStatic}, &failingVault{Vault: r.kr.Migration(), garble: true})
	if !errors.Is(err, credmigrate.ErrFailed) || sum.Failed != 2 || sum.Moved != 0 {
		t.Fatalf("%v %+v\n%s", err, sum, report)
	}
	if c := r.cred(ids["basic"]); c.Kind != store.AuthBasic || r.opened(ids["basic"]).Password != password {
		t.Fatal("the gateway's row was switched to a copy that did not match")
	}
}

// Moved into a person's keyring, a credential lands under that person, and
// an unrelated secret of the same name is never overwritten.
func TestMovingIntoAPersonsKeyring(t *testing.T) {
	r := newRig(t)
	ids := r.everyKind()
	owner := uuid.MustParse(alice)
	tenant := uuid.New()
	// Alice already holds a secret under the name the Basic server would take.
	if _, err := r.kr.Migration().ImportSecret(context.Background(), owner, tenant, keyringref.Use{Slug: r.slug("basic")},
		keyringref.ServerSecret{Scheme: keyringref.SchemeHeader, HeaderName: "X-Other", Value: "something-else"}); err != nil {
		t.Fatal(err)
	}

	report, sum, err := r.run(credmigrate.Options{Kind: credmigrate.KindStatic, Owner: owner}, nil)
	if !errors.Is(err, credmigrate.ErrFailed) || sum.Moved != 1 || sum.Failed != 1 {
		t.Fatalf("%v %+v\n%s", err, sum, report)
	}
	if !strings.Contains(report, "keyring owner: user "+alice) || !strings.Contains(report, "already holds a different secret") {
		t.Fatalf("the report does not say whose keyring, or why one failed:\n%s", report)
	}
	if r.cred(ids["basic"]).Kind != store.AuthBasic {
		t.Fatal("the server whose name was taken was moved")
	}
	if h, err := r.held(alice, "mcp-"+r.slug("basic")); err != nil || h.Name != "X-Other" {
		t.Fatalf("Alice's own secret was overwritten: %q (%v)", h.Name, err)
	}
	c := r.cred(ids["header"])
	if c.Kind != store.AuthKeyringSecret || c.KeyringOwner != alice {
		t.Fatalf("the header credential is %s owned by %q", c.Kind, c.KeyringOwner)
	}
	// It landed in Alice's tenant, beside her other secret.
	rows, err := r.ms.ListAPIKeys(krcontracts.WithCaller(context.Background(), krcontracts.Caller{UserID: owner}), owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.TenantID != tenant {
			t.Fatalf("%s was written under tenant %s, want Alice's %s", row.Name, row.TenantID, tenant)
		}
	}
	// Her rollback takes back what was moved and nothing else.
	if report, sum, err = r.run(credmigrate.Options{Kind: credmigrate.KindStatic, Owner: owner, Rollback: true}, nil); err != nil || sum.Moved != 1 {
		t.Fatalf("rollback: %v %+v\n%s", err, sum, report)
	}
	if c := r.cred(ids["lent"]); c.Kind != store.AuthKeyringSecret {
		t.Fatal("the rollback took a reference Alice set herself")
	}
}

// A credential the gateway stored in the keyring from the start was never
// moved, so a rollback leaves it unless it is asked to take those too.
func TestRollbackTakesNewCredentialsOnlyWhenAsked(t *testing.T) {
	r := newRig(t)
	slug := r.slug("fresh")
	ref, err := r.kr.Source().PutServerSecret(context.Background(), keyringref.Use{Slug: slug}, keyringref.ServerSecret{Scheme: keyringref.SchemeHeader, HeaderName: "X-Api-Key", Value: headerValue})
	if err != nil {
		t.Fatal(err)
	}
	id := r.server("fresh", store.Credential{Kind: store.AuthKeyringSecret, KeyringOwner: ref.Owner, KeyringSecretName: ref.Secret}, nil)

	if report, sum, err := r.run(credmigrate.Options{Kind: credmigrate.KindStatic, Rollback: true}, nil); err != nil || sum.Moved != 0 || sum.Skipped != 1 {
		t.Fatalf("%v %+v\n%s", err, sum, report)
	}
	if report, sum, err := r.run(credmigrate.Options{Kind: credmigrate.KindStatic, Rollback: true, IncludeNew: true}, nil); err != nil || sum.Moved != 1 {
		t.Fatalf("%v %+v\n%s", err, sum, report)
	}
	if c := r.cred(id); c.Kind != store.AuthHeader || r.opened(id).HeaderValue != headerValue {
		t.Fatalf("the credential did not come back: %s", c.Kind)
	}
}

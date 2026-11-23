// Package credmigrate moves the credentials the gateway used to store itself
// into the keyring, and back: the one-off command `gateway
// migrate-credentials`.
//
// The gateway sealed a server's Basic password, header value or OAuth tokens
// in its own table under its master key. The keyring is where credentials
// live now. The command copies each one into the keyring, checks the copy
// reads back the same, and only then turns the gateway's row into a
// reference and clears the sealed blob. Run again, it skips what is already
// moved, so it can be stopped at any point and resumed. With -rollback it
// does the reverse for the rows it moved, taking the keyring's current value
// as the truth, since a token may have been refreshed since.
//
// This package is the only code that still reads and writes the gateway's
// sealed credential columns.
package credmigrate

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andyjmorgan/Bifrost/internal/keyringref"
)

// Kinds of credential the command moves, one per run.
const (
	// KindStatic is Basic and header credentials.
	KindStatic = "static"
	// KindOAuth is OAuth registrations and tokens.
	KindOAuth = "oauth"
)

// actor is who the keyring's audit trail says made the command's changes.
const actor = "migrate-credentials"

// Sealer opens and seals the gateway's own credential blobs.
type Sealer interface {
	Seal(plaintext, aad []byte) ([]byte, error)
	Open(blob, aad []byte) ([]byte, error)
}

// Vault is the keyring as the command uses it (keyring.Migration).
type Vault interface {
	OwnerTenant(ctx context.Context, owner uuid.UUID) (uuid.UUID, error)
	HasSecret(ctx context.Context, ref keyringref.Ref) (bool, error)
	ImportSecret(ctx context.Context, owner, tenant uuid.UUID, use keyringref.Use, in keyringref.ServerSecret) (keyringref.Ref, error)
	ExportSecret(ctx context.Context, ref keyringref.Ref, use keyringref.Use) (keyringref.ServerSecret, error)
	RemoveSecret(ctx context.Context, ref keyringref.Ref, use keyringref.Use) error
}

// Guard keeps token refreshes from running while credentials are moved
// (refreshguard.Guard).
type Guard interface {
	Exclusive(ctx context.Context) (release func(), err error)
}

// Options is what the command was asked to do.
type Options struct {
	// Kind is KindStatic or KindOAuth.
	Kind string
	// DryRun prints what would be done, names only, and changes nothing.
	DryRun bool
	// Rollback moves credentials back from the keyring into the gateway's
	// own store: the rows the command moved earlier, and with IncludeNew
	// also the ones the gateway has stored in the keyring since.
	Rollback bool
	// Owner is the keyring user the moved credentials belong to:
	// keyringref.GatewayOwner unless the operator named a person.
	Owner uuid.UUID
	// IncludeNew widens a rollback to every credential the gateway holds in
	// the keyring for a server, not only the ones this command moved there.
	IncludeNew bool
	// Servers limits the run to these slugs. Empty means every server.
	Servers []string
	// Out is where the report is printed. It never receives a secret.
	Out io.Writer
}

// Deps is what the command works with.
type Deps struct {
	Pool   *pgxpool.Pool // the gateway's database
	Sealer Sealer
	Vault  Vault
	Guard  Guard
}

// Summary counts what a run did.
type Summary struct {
	Moved   int // moved, or in a dry run, would be
	Already int // already where the run would put them
	Skipped int // not this run's to move
	Failed  int
}

// ErrFailed is returned when at least one credential could not be moved.
// The report names each one.
var ErrFailed = errors.New("credmigrate: some credentials were not moved; see the report")

// row is a server's credential as the command reads it.
type row struct {
	serverID     uuid.UUID
	slug, name   string
	url          string
	kind         string
	username     string
	header       string
	blob         []byte
	owner        string
	secretName   string
	provider     string
	account      string
	migratedFrom string
}

// was is the kind the operator set: for a row the command has moved, what
// it was before.
func (r row) was() string {
	if r.migratedFrom != "" {
		return r.migratedFrom
	}
	switch r.kind {
	case "keyring_secret", "keyring_oauth":
		return "keyring"
	}
	return r.kind
}

func (r row) use() keyringref.Use {
	return keyringref.Use{ServerID: r.serverID.String(), Slug: r.slug, Actor: actor}
}

// secret is the decrypted content of a sealed blob (store.Secret).
type secret struct {
	Password     string `json:"password,omitempty"`
	HeaderValue  string `json:"headerValue,omitempty"`
	AccessToken  string `json:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
}

type runner struct {
	Deps
	o      Options
	tenant uuid.UUID
	sum    Summary
}

// Run carries out one migration, or one rollback, and prints a line for
// each server. It returns ErrFailed when any credential was left unmoved;
// the others are done and stay done.
func Run(ctx context.Context, d Deps, o Options) (Summary, error) {
	if o.Kind != KindStatic && o.Kind != KindOAuth {
		return Summary{}, fmt.Errorf("credmigrate: -kind must be %q or %q", KindStatic, KindOAuth)
	}
	if o.Owner == uuid.Nil {
		return Summary{}, errors.New("credmigrate: an owner is required")
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	r := &runner{Deps: d, o: o}

	// Hold refreshes off for the whole run, and keep a second run out.
	if !o.DryRun {
		release, err := d.Guard.Exclusive(ctx)
		if err != nil {
			return Summary{}, err
		}
		defer release()
	}
	tenant, err := d.Vault.OwnerTenant(ctx, o.Owner)
	if err != nil {
		return Summary{}, fmt.Errorf("credmigrate: read the owner's keyring: %w", err)
	}
	r.tenant = tenant

	rows, err := r.load(ctx)
	if err != nil {
		return Summary{}, err
	}
	r.header()
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			// Stopped between servers: each one is whole, and a later run resumes.
			fmt.Fprintf(o.Out, "stopped: %v\n", err)
			break
		}
		r.one(ctx, row)
	}
	r.footer()
	if r.sum.Failed > 0 {
		return r.sum, ErrFailed
	}
	return r.sum, ctx.Err()
}

func (r *runner) ownerLabel() string {
	if r.o.Owner.String() == keyringref.GatewayOwner {
		return "the gateway"
	}
	return "user " + r.o.Owner.String()
}

func (r *runner) header() {
	what := map[string]string{KindStatic: "Basic and header credentials", KindOAuth: "OAuth registrations and tokens"}[r.o.Kind]
	dir := "gateway store -> keyring"
	if r.o.Rollback {
		dir = "keyring -> gateway store (rollback)"
	}
	mode := ""
	if r.o.DryRun {
		mode = " [dry run: nothing is changed]"
	}
	fmt.Fprintf(r.o.Out, "migrate-credentials: %s, %s%s\n", what, dir, mode)
	fmt.Fprintf(r.o.Out, "keyring owner: %s\n", r.ownerLabel())
}

func (r *runner) footer() {
	verb := "moved"
	if r.o.Rollback {
		verb = "restored"
	}
	if r.o.DryRun {
		verb = "to be " + verb
	}
	fmt.Fprintf(r.o.Out, "%d %s, %d already done, %d skipped, %d failed\n", r.sum.Moved, verb, r.sum.Already, r.sum.Skipped, r.sum.Failed)
}

// say prints one server's line: what happened, the kind, the slug, and the
// keyring name. Names only.
func (r *runner) say(status string, row row, detail string) {
	fmt.Fprintf(r.o.Out, "%-13s %-7s %-24s %s\n", status, row.was(), row.slug, detail)
}

func (r *runner) fail(row row, err error) {
	r.sum.Failed++
	r.say("FAILED", row, err.Error())
}

// load reads every server's credential, in slug order.
func (r *runner) load(ctx context.Context) ([]row, error) {
	rows, err := r.Pool.Query(ctx, `select s.id, s.slug, s.name, s.url, c.kind, coalesce(c.basic_username,''), coalesce(c.header_name,''), c.secret_blob,
		coalesce(c.keyring_owner,''), coalesce(c.keyring_secret_name,''), coalesce(c.keyring_provider,''), coalesce(c.keyring_account,''), coalesce(c.migrated_from,'')
		from servers s join credentials c on c.server_id = s.id where c.kind <> 'none' order by s.slug`)
	if err != nil {
		return nil, fmt.Errorf("credmigrate: read credentials: %w", err)
	}
	defer rows.Close()
	only := map[string]bool{}
	for _, s := range r.o.Servers {
		only[s] = true
	}
	var out []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.serverID, &x.slug, &x.name, &x.url, &x.kind, &x.username, &x.header, &x.blob,
			&x.owner, &x.secretName, &x.provider, &x.account, &x.migratedFrom); err != nil {
			return nil, fmt.Errorf("credmigrate: read credentials: %w", err)
		}
		if len(only) == 0 || only[x.slug] {
			out = append(out, x)
			delete(only, x.slug)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("credmigrate: read credentials: %w", err)
	}
	if len(only) > 0 {
		missing := make([]string, 0, len(only))
		for s := range only {
			missing = append(missing, s)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("credmigrate: no server with a credential has the slug %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// one handles a single server.
func (r *runner) one(ctx context.Context, row row) {
	switch {
	case r.o.Kind == KindStatic && !r.o.Rollback:
		r.moveStatic(ctx, row)
	case r.o.Kind == KindStatic:
		r.restoreStatic(ctx, row)
	case !r.o.Rollback:
		r.moveOAuth(ctx, row)
	default:
		r.restoreOAuth(ctx, row)
	}
}

func (r *runner) open(row row) (*secret, error) {
	if len(row.blob) == 0 {
		return nil, errors.New("the sealed credential is empty")
	}
	pt, err := r.Sealer.Open(row.blob, row.serverID[:])
	if err != nil {
		return nil, errors.New("the sealed credential does not open with this master key")
	}
	var s secret
	if err := json.Unmarshal(pt, &s); err != nil {
		return nil, errors.New("the sealed credential is not readable")
	}
	return &s, nil
}

func (r *runner) seal(row row, s *secret) ([]byte, error) {
	b, _ := json.Marshal(s)
	return r.Sealer.Seal(b, row.serverID[:])
}

// same compares two secret values without leaking where they differ.
func same(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// ---- static credentials ---------------------------------------------------

func (r *runner) moveStatic(ctx context.Context, row row) {
	ref := keyringref.Ref{Owner: r.o.Owner.String(), Kind: keyringref.KindSecret, Secret: keyringref.ServerCredentialName(row.slug)}
	target := fmt.Sprintf("-> keyring secret %q", ref.Secret)
	switch row.kind {
	case "basic", "header":
	case "keyring_secret":
		if row.migratedFrom != "" {
			r.sum.Already++
			r.say("already", row, fmt.Sprintf("keyring secret %q (moved earlier, was %s)", row.secretName, row.migratedFrom))
			return
		}
		fallthrough
	default:
		r.sum.Skipped++
		r.say("skip", row, "not a Basic or header credential in the gateway's store")
		return
	}

	// A person's keyring may already hold an unrelated secret under the
	// server's name. The gateway's own space cannot: every name in it is a
	// server's.
	if r.o.Owner.String() != keyringref.GatewayOwner {
		taken, err := r.Vault.HasSecret(ctx, ref)
		if err != nil {
			r.fail(row, fmt.Errorf("read the keyring: %w", err))
			return
		}
		if taken {
			existing, err := r.Vault.ExportSecret(ctx, ref, row.use())
			sec, oerr := r.open(row)
			if err != nil || oerr != nil || !same(existing.Value, staticValue(row, sec)) {
				r.fail(row, fmt.Errorf("the owner's keyring already holds a different secret named %q; rename or delete it first", ref.Secret))
				return
			}
		}
	}
	if r.o.DryRun {
		r.sum.Moved++
		r.say("would move", row, target)
		return
	}

	sec, err := r.open(row)
	if err != nil {
		r.fail(row, err)
		return
	}
	in := keyringref.ServerSecret{URL: row.url, Value: staticValue(row, sec)}
	if row.kind == "basic" {
		in.Scheme, in.Username = keyringref.SchemeBasic, row.username
	} else {
		in.Scheme, in.HeaderName = keyringref.SchemeHeader, row.header
	}
	// 1. Copy. The gateway's row still decides what is sent; a run that
	// stops here is repeated from the top and overwrites the copy.
	if _, err := r.Vault.ImportSecret(ctx, r.o.Owner, r.tenant, row.use(), in); err != nil {
		r.fail(row, fmt.Errorf("write the keyring: %w", err))
		return
	}
	// 2. Read the copy back and compare it with the original.
	back, err := r.Vault.ExportSecret(ctx, ref, row.use())
	if err != nil {
		r.fail(row, fmt.Errorf("read the copy back: %w", err))
		return
	}
	if back.Scheme != in.Scheme || back.Username != in.Username || !strings.EqualFold(back.HeaderName, headerOr(in)) || !same(back.Value, in.Value) {
		r.fail(row, errors.New("the keyring's copy does not match the original; the gateway's row is unchanged"))
		return
	}
	// 3. Switch the gateway's row to the reference and clear the blob.
	if err := r.flip(ctx, row, "keyring_secret", `kind='keyring_secret', keyring_owner=$2, keyring_secret_name=$3, keyring_provider=null, keyring_account=null,
		basic_username=null, header_name=null, secret_blob=null, migrated_from=$4, state='ok', state_reason=null`,
		ref.Owner, ref.Secret, row.kind); err != nil {
		r.fail(row, err)
		return
	}
	r.sum.Moved++
	r.say("moved", row, target)
}

// staticValue is the password or header value in a sealed static credential.
func staticValue(row row, s *secret) string {
	if s == nil {
		return ""
	}
	if row.kind == "basic" {
		return s.Password
	}
	return s.HeaderValue
}

// headerOr is the header a static credential is sent in: Basic goes in
// Authorization.
func headerOr(in keyringref.ServerSecret) string {
	if in.Scheme == keyringref.SchemeBasic {
		return ""
	}
	return in.HeaderName
}

func (r *runner) restoreStatic(ctx context.Context, row row) {
	ref := keyringref.Ref{Owner: row.owner, Kind: keyringref.KindSecret, Secret: row.secretName}
	source := fmt.Sprintf("<- keyring secret %q", row.secretName)
	switch {
	case row.kind == "basic" || row.kind == "header":
		r.sum.Already++
		r.say("already", row, "in the gateway's store")
		return
	case row.kind != "keyring_secret" || !r.mine(row, "basic", "header"):
		r.sum.Skipped++
		r.say("skip", row, "not a credential this command moved")
		return
	}
	if r.o.DryRun {
		r.sum.Moved++
		r.say("would restore", row, source)
		return
	}
	// The keyring's value is the truth: it may have been replaced since the move.
	held, err := r.Vault.ExportSecret(ctx, ref, row.use())
	if err != nil {
		r.fail(row, fmt.Errorf("read the keyring: %w", err))
		return
	}
	var (
		blob []byte
		kind = "header"
	)
	if held.Scheme == keyringref.SchemeBasic {
		kind = "basic"
		blob, err = r.seal(row, &secret{Password: held.Value})
	} else {
		blob, err = r.seal(row, &secret{HeaderValue: held.Value})
	}
	if err != nil {
		r.fail(row, errors.New("seal the credential for the gateway's store"))
		return
	}
	if err := r.flip(ctx, row, kind, `kind=$2, basic_username=nullif($3,''), header_name=nullif($4,''), secret_blob=$5,
		keyring_owner=null, keyring_secret_name=null, keyring_provider=null, keyring_account=null, migrated_from=null, state='ok', state_reason=null`,
		kind, held.Username, held.HeaderName, blob); err != nil {
		r.fail(row, err)
		return
	}
	// The gateway's row decides again. The keyring's copy is now nobody's.
	if err := r.Vault.RemoveSecret(ctx, ref, row.use()); err != nil {
		fmt.Fprintf(r.o.Out, "%-13s %-7s %-24s restored, but the keyring secret %q could not be deleted: %v\n", "note", kind, row.slug, row.secretName, err)
	}
	r.sum.Moved++
	r.say("restored", row, source)
}

// mine reports whether a keyring reference is one the rollback may take
// back: one this command moved from one of the kinds in from, or, with
// IncludeNew, any the gateway itself holds.
func (r *runner) mine(row row, from ...string) bool {
	for _, f := range from {
		if row.migratedFrom == f {
			return true
		}
	}
	return r.o.IncludeNew && row.migratedFrom == "" && row.owner == keyringref.GatewayOwner
}

// flip rewrites a server's credential row and its server's auth kind in one
// transaction. set is the assignment list, with $1 the server; the row must
// still be of the kind it was read as, so a credential somebody replaced in
// the meantime is left alone.
func (r *runner) flip(ctx context.Context, row row, newKind, set string, args ...any) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("switch the gateway's row: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	n := len(args) + 2
	tag, err := tx.Exec(ctx, fmt.Sprintf(`update credentials set %s, updated_at=now() where server_id=$1 and kind=$%d`, set, n),
		append(append([]any{row.serverID}, args...), row.kind)...)
	if err != nil {
		return fmt.Errorf("switch the gateway's row: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("the credential changed while it was being moved; nothing was switched. Run the command again")
	}
	if _, err := tx.Exec(ctx, `update servers set auth_kind=$2, updated_at=now() where id=$1`, row.serverID, newKind); err != nil {
		return fmt.Errorf("switch the gateway's row: %w", err)
	}
	if err := tx.Commit(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		return fmt.Errorf("switch the gateway's row: %w", err)
	}
	return nil
}

// ---- OAuth credentials ----------------------------------------------------

func (r *runner) moveOAuth(_ context.Context, row row) {
	r.sum.Skipped++
	r.say("skip", row, "OAuth credentials are not moved by this version")
}

func (r *runner) restoreOAuth(_ context.Context, row row) {
	r.sum.Skipped++
	r.say("skip", row, "OAuth credentials are not moved by this version")
}

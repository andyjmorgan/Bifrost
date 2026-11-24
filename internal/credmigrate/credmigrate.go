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
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andyjmorgan/Bifrost/internal/keyringref"
	"github.com/andyjmorgan/Bifrost/internal/refreshguard"
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

	ProviderTaken(ctx context.Context, ref keyringref.Ref) (bool, error)
	ImportOAuth(ctx context.Context, owner, tenant uuid.UUID, use keyringref.Use, c keyringref.ServerConnection) (keyringref.Ref, error)
	ExportOAuth(ctx context.Context, ref keyringref.Ref, use keyringref.Use) (keyringref.ServerConnection, error)
	RemoveOAuth(ctx context.Context, ref keyringref.Ref, use keyringref.Use) error
}

// Guard keeps token refreshes from running while credentials are moved
// (refreshguard.Guard).
type Guard interface {
	Exclusive(ctx context.Context) (refreshguard.Held, error)
}

// ErrGuardLost is returned when the refresh guard's database session went
// away during a run. The run stops before the next credential: with the
// guard gone, refreshes are running again, and a token copied now could be
// retired under the copy. Every server is still whole, and a later run
// carries on.
var ErrGuardLost = errors.New("credmigrate: the refresh guard was lost during the run; it stopped before the next credential. Run the command again")

// errChanged is a credential row that is not as it was read: somebody
// replaced the credential, a consent landed, or the server was deleted.
var errChanged = errors.New("the credential changed during the run; nothing was switched. Run the command again")

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
	// IgnoreOtherSessions lets an OAuth run go ahead although other
	// sessions that are not this version of Bifrost are connected to the
	// database. See otherSessions.
	IgnoreOtherSessions bool
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

	state, stateReason     string
	issuer, authzEndpoint  string
	tokenEndpoint          string
	registrationEndpoint   string
	registrationKind       string
	clientID, resource     string
	scopes                 []string
	tokenType              string
	expiresAt, refreshedAt *time.Time

	// updatedAt is when the row was last written. Every write to a
	// credential row moves it, so a row whose updatedAt is the one that was
	// read is the row that was read.
	updatedAt time.Time
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

// secret is the decrypted content of a sealed blob, as the gateway wrote it
// when it sealed credentials itself.
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
	// held is the exclusive refresh guard; nil in a dry run, which changes
	// nothing and so holds nothing.
	held refreshguard.Held
}

// guarded reports whether the run still holds the refresh guard.
func (r *runner) guarded(ctx context.Context) error {
	if r.held == nil {
		return nil
	}
	if err := r.held.Check(ctx); err != nil {
		return fmt.Errorf("%w (%v)", ErrGuardLost, err)
	}
	return nil
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

	// A gateway that does not hold the refresh guard could rotate a token
	// while its row is being copied. Refuse to move OAuth rows beside one.
	if o.Kind == KindOAuth {
		others, err := otherSessions(ctx, d.Pool)
		if err != nil {
			return Summary{}, err
		}
		if others != "" {
			fmt.Fprintf(o.Out, "other sessions on this database that are not this version of Bifrost: %s\n", others)
			if !o.DryRun && !o.IgnoreOtherSessions {
				return Summary{}, errors.New("credmigrate: refusing to move OAuth credentials while other sessions are connected. " +
					"An older gateway does not hold off token refreshes during the move and could strand a refresh token: stop it first. " +
					"If the sessions are something else (psql, a backup), run again with -ignore-other-sessions")
			}
		}
	}

	// Hold refreshes off for the whole run, and keep a second run out. The
	// call waits for every refresh in flight to finish.
	if !o.DryRun {
		held, err := d.Guard.Exclusive(ctx)
		if err != nil {
			return Summary{}, err
		}
		defer held.Release()
		r.held = held
	}
	tenant, err := d.Vault.OwnerTenant(ctx, o.Owner)
	if err != nil {
		return Summary{}, fmt.Errorf("credmigrate: read the owner's keyring: %w", err)
	}
	r.tenant = tenant

	rows, err := r.load(ctx, uuid.Nil)
	if err != nil {
		return Summary{}, err
	}
	r.header()
	var lost error
	for _, listed := range rows {
		if err := ctx.Err(); err != nil {
			// Stopped between servers: each one is whole, and a later run resumes.
			fmt.Fprintf(o.Out, "stopped: %v\n", err)
			break
		}
		// The guard is one database session. If it is gone, so is the lock.
		if lost = r.guarded(ctx); lost != nil {
			fmt.Fprintf(o.Out, "stopped: the refresh guard was lost before %s; nothing is half-moved\n", listed.slug)
			break
		}
		// The list was read when the run began. Each server is read again at
		// its turn, so what is moved is what the row holds now.
		row, err := r.reload(ctx, listed)
		if err != nil {
			r.fail(listed, err)
			continue
		}
		if row == nil {
			r.sum.Skipped++
			r.say("skip", listed, "the server or its credential was removed after the run began")
			continue
		}
		if lost = r.one(ctx, *row); lost != nil {
			fmt.Fprintf(o.Out, "stopped: the refresh guard was lost at %s; its row was not switched\n", listed.slug)
			break
		}
	}
	r.footer()
	switch {
	case lost != nil:
		return r.sum, lost
	case r.sum.Failed > 0:
		return r.sum, ErrFailed
	}
	return r.sum, ctx.Err()
}

// reload reads one server's credential again. It returns nil when the
// server, or its credential, is gone.
func (r *runner) reload(ctx context.Context, listed row) (*row, error) {
	rows, err := r.load(ctx, listed.serverID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
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

// load reads every server's credential, in slug order, or with one set to a
// server's id, that server's alone.
func (r *runner) load(ctx context.Context, one uuid.UUID) ([]row, error) {
	rows, err := r.Pool.Query(ctx, `select s.id, s.slug, s.name, s.url, c.kind, coalesce(c.basic_username,''), coalesce(c.header_name,''), c.secret_blob,
		coalesce(c.keyring_owner,''), coalesce(c.keyring_secret_name,''), coalesce(c.keyring_provider,''), coalesce(c.keyring_account,''), coalesce(c.migrated_from,''),
		c.state, coalesce(c.state_reason,''), coalesce(c.oauth_issuer,''), coalesce(c.oauth_authorization_endpoint,''), coalesce(c.oauth_token_endpoint,''),
		coalesce(c.oauth_registration_endpoint,''), coalesce(c.oauth_registration_kind,''), coalesce(c.oauth_client_id,''), coalesce(c.oauth_resource,''),
		c.oauth_scopes, coalesce(c.oauth_token_type,''), c.oauth_expires_at, c.last_refreshed_at, c.updated_at
		from servers s join credentials c on c.server_id = s.id
		where c.kind <> 'none' and ($1::uuid is null or s.id = $1) order by s.slug`, oneOrAll(one))
	if err != nil {
		return nil, fmt.Errorf("credmigrate: read credentials: %w", err)
	}
	defer rows.Close()
	// only is the slugs the run is limited to; found is the ones that exist.
	only, found := map[string]bool{}, map[string]bool{}
	if one == uuid.Nil {
		for _, s := range r.o.Servers {
			only[s] = true
		}
	}
	var out []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.serverID, &x.slug, &x.name, &x.url, &x.kind, &x.username, &x.header, &x.blob,
			&x.owner, &x.secretName, &x.provider, &x.account, &x.migratedFrom,
			&x.state, &x.stateReason, &x.issuer, &x.authzEndpoint, &x.tokenEndpoint,
			&x.registrationEndpoint, &x.registrationKind, &x.clientID, &x.resource,
			&x.scopes, &x.tokenType, &x.expiresAt, &x.refreshedAt, &x.updatedAt); err != nil {
			return nil, fmt.Errorf("credmigrate: read credentials: %w", err)
		}
		if len(only) == 0 || only[x.slug] {
			out = append(out, x)
			found[x.slug] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("credmigrate: read credentials: %w", err)
	}
	if len(found) < len(only) {
		var missing []string
		for s := range only {
			if !found[s] {
				missing = append(missing, s)
			}
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("credmigrate: no server with a credential has the slug %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// oneOrAll is the load query's parameter: a server's id, or NULL for all.
func oneOrAll(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// one handles a single server. The only error it returns is ErrGuardLost,
// which ends the run; everything else is counted and reported on the
// server's line.
func (r *runner) one(ctx context.Context, row row) error {
	var err error
	switch {
	case r.o.Kind == KindStatic && !r.o.Rollback:
		err = r.moveStatic(ctx, row)
	case r.o.Kind == KindStatic:
		err = r.restoreStatic(ctx, row)
	case !r.o.Rollback:
		err = r.moveOAuth(ctx, row)
	default:
		err = r.restoreOAuth(ctx, row)
	}
	if errors.Is(err, ErrGuardLost) {
		return err
	}
	return nil
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

func (r *runner) moveStatic(ctx context.Context, row row) error {
	ref := keyringref.Ref{Owner: r.o.Owner.String(), Kind: keyringref.KindSecret, Secret: keyringref.ServerCredentialName(row.slug)}
	target := fmt.Sprintf("-> keyring secret %q", ref.Secret)
	switch row.kind {
	case "basic", "header":
	case "keyring_secret":
		if row.migratedFrom != "" {
			r.sum.Already++
			r.say("already", row, fmt.Sprintf("keyring secret %q (moved earlier, was %s)", row.secretName, row.migratedFrom))
			return nil
		}
		fallthrough
	default:
		r.sum.Skipped++
		r.say("skip", row, "not a Basic or header credential in the gateway's store")
		return nil
	}
	person := r.o.Owner.String() != keyringref.GatewayOwner

	// What the keyring holds under the server's name before anything is
	// written there. It is needed twice: a person's keyring may hold an
	// unrelated secret of that name, which must not be overwritten; and if
	// the switch below does not happen, the keyring is put back as it was.
	// A dry run for the gateway's own space decrypts nothing.
	var prev *keyringref.ServerSecret
	if person || !r.o.DryRun {
		var err error
		if prev, err = r.heldSecret(ctx, ref, row); err != nil {
			r.fail(row, fmt.Errorf("read the keyring: %w", err))
			return nil
		}
	}
	if person && prev != nil {
		if sec, err := r.open(row); err != nil || !same(prev.Value, staticValue(row, sec)) {
			r.fail(row, fmt.Errorf("the owner's keyring already holds a different secret named %q; rename or delete it first", ref.Secret))
			return nil
		}
	}
	if r.o.DryRun {
		r.sum.Moved++
		r.say("would move", row, target)
		return nil
	}

	sec, err := r.open(row)
	if err != nil {
		r.fail(row, err)
		return nil
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
		return nil
	}
	// 2. Read the copy back and compare it with the original.
	back, err := r.Vault.ExportSecret(ctx, ref, row.use())
	if err != nil {
		r.fail(row, fmt.Errorf("read the copy back: %w", err))
		return nil
	}
	if !sameSecret(back, in) {
		r.fail(row, errors.New("the keyring's copy does not match the original; the gateway's row is unchanged"))
		return nil
	}
	// 3. Switch the gateway's row to the reference and clear the blob, if
	// the row is still the one that was copied.
	err = r.flip(ctx, row, "keyring_secret", `kind='keyring_secret', keyring_owner=$2, keyring_secret_name=$3, keyring_provider=null, keyring_account=null,
		basic_username=null, header_name=null, secret_blob=null, migrated_from=$4, state='ok', state_reason=null`,
		ref.Owner, ref.Secret, row.kind)
	switch {
	case errors.Is(err, ErrGuardLost):
		_ = r.undoSecret(ctx, row, ref, in, prev)
		return err
	case errors.Is(err, errChanged):
		r.fail(row, r.undoSecret(ctx, row, ref, in, prev))
		return nil
	case err != nil:
		r.fail(row, err)
		return nil
	}
	r.sum.Moved++
	r.say("moved", row, target)
	return nil
}

// heldSecret is what the keyring holds under ref, or nil when it holds
// nothing there.
func (r *runner) heldSecret(ctx context.Context, ref keyringref.Ref, row row) (*keyringref.ServerSecret, error) {
	has, err := r.Vault.HasSecret(ctx, ref)
	if err != nil || !has {
		return nil, err
	}
	s, err := r.Vault.ExportSecret(ctx, ref, row.use())
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// sameSecret reports whether two static credentials are sent identically.
func sameSecret(a, b keyringref.ServerSecret) bool {
	return a.Scheme == b.Scheme && a.Username == b.Username && strings.EqualFold(headerOr(a), headerOr(b)) && same(a.Value, b.Value)
}

// undoSecret tidies the keyring after a copy whose switch did not happen
// because the row changed under the run. It returns what to report.
//
// The copy was written under the server's name, mcp-<slug>. What that name
// should hold now depends on what the row has become:
//
//   - Nothing refers to it (the server was deleted, or given a credential
//     kept elsewhere): the copy is removed, so no orphan is left. A person's
//     keyring that held the same secret before the run keeps it.
//   - The row refers to it: somebody gave the server a new credential while
//     the old one was being copied, and the gateway stored it under the same
//     name. If the keyring holds something other than the copy, that is the
//     new credential and it is left alone. If it holds the copy, the copy
//     has overwritten the new credential: what the keyring held before the
//     copy is put back when that was read, and otherwise the report says to
//     set the credential again.
func (r *runner) undoSecret(ctx context.Context, row row, ref keyringref.Ref, copied keyringref.ServerSecret, prev *keyringref.ServerSecret) error {
	now, err := r.reload(ctx, row)
	if err != nil {
		return fmt.Errorf("%w (the row could not be read again, so the keyring's copy %q was left: %v)", errChanged, ref.Secret, err)
	}
	if now == nil || now.kind != "keyring_secret" || now.owner != ref.Owner || now.secretName != ref.Secret {
		if prev != nil && !ref.HeldByGateway() {
			return errChanged // the person's secret was there before, with this value
		}
		if err := r.Vault.RemoveSecret(ctx, ref, row.use()); err != nil {
			return fmt.Errorf("%w (the keyring's copy %q could not be removed: %v)", errChanged, ref.Secret, err)
		}
		return errChanged
	}
	held, err := r.Vault.ExportSecret(ctx, ref, row.use())
	if err != nil || !sameSecret(held, copied) {
		return errChanged // the keyring holds the server's new credential, not the copy
	}
	if prev != nil && sameSecret(*prev, copied) {
		return errChanged // the new credential is the same as the old
	}
	if prev != nil {
		if _, err := r.Vault.ImportSecret(ctx, r.o.Owner, r.tenant, row.use(), *prev); err == nil {
			return fmt.Errorf("%w (the keyring's credential was put back as it was before the copy)", errChanged)
		}
	}
	return fmt.Errorf("%w. The server was given a new credential while its old one was being copied, and the keyring may hold the old one: set the server's credential again", errChanged)
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

func (r *runner) restoreStatic(ctx context.Context, row row) error {
	ref := keyringref.Ref{Owner: row.owner, Kind: keyringref.KindSecret, Secret: row.secretName}
	source := fmt.Sprintf("<- keyring secret %q", row.secretName)
	switch {
	case row.kind == "basic" || row.kind == "header":
		r.sum.Already++
		r.say("already", row, "in the gateway's store")
		return nil
	case row.kind != "keyring_secret":
		r.sum.Skipped++
		r.say("skip", row, "not a credential this command moved")
		return nil
	}
	if ok, why := r.mine(row, row.secretName, "basic", "header"); !ok {
		r.sum.Skipped++
		r.say("skip", row, why)
		return nil
	}
	if r.o.DryRun {
		r.sum.Moved++
		r.say("would restore", row, source)
		return nil
	}
	// The keyring's value is the truth: it may have been replaced since the move.
	held, err := r.Vault.ExportSecret(ctx, ref, row.use())
	if err != nil {
		r.fail(row, fmt.Errorf("read the keyring: %w", err))
		return nil
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
		return nil
	}
	// Nothing has been written yet, so a row that changed is simply left.
	err = r.flip(ctx, row, kind, `kind=$2, basic_username=nullif($3,''), header_name=nullif($4,''), secret_blob=$5,
		keyring_owner=null, keyring_secret_name=null, keyring_provider=null, keyring_account=null, migrated_from=null, state='ok', state_reason=null`,
		kind, held.Username, held.HeaderName, blob)
	switch {
	case errors.Is(err, ErrGuardLost):
		return err
	case err != nil:
		r.fail(row, err)
		return nil
	}
	// The gateway's row decides again. A copy in the gateway's own space is
	// now nobody's and is removed. One in a person's keyring is theirs to
	// keep or delete: the command put it there, but it is their keyring.
	if !ref.HeldByGateway() {
		source += " (the secret stays in the user's keyring)"
	} else if err := r.Vault.RemoveSecret(ctx, ref, row.use()); err != nil {
		fmt.Fprintf(r.o.Out, "%-13s %-7s %-24s restored, but the keyring secret %q could not be deleted: %v\n", "note", kind, row.slug, row.secretName, err)
	}
	r.sum.Moved++
	r.say("restored", row, source)
	return nil
}

// mine reports whether a keyring reference is one the rollback may take
// back, and when it is not, why. held is the name the row refers to: the
// secret, or the OAuth provider.
//
// A row is the command's only when all of this holds:
//
//   - Its owner is the owner this run was given: the gateway's own space,
//     unless the operator named a person. A row that refers to anybody
//     else's keyring is a credential that person lent, whatever the row was
//     before. This matters because a server that was moved can afterwards
//     be pointed at a secret in somebody's keyring, and the row may still
//     say it was moved.
//   - It refers to the name the move writes, mcp-<slug>. Any other name is
//     a credential somebody chose.
//   - It was moved from one of the kinds in from; or, with IncludeNew, it
//     is held by the gateway and was stored in the keyring to begin with.
//     IncludeNew never reaches into a person's keyring.
func (r *runner) mine(row row, held string, from ...string) (bool, string) {
	if row.owner != r.o.Owner.String() {
		if row.owner == keyringref.GatewayOwner {
			return false, "held by the gateway, not by the owner this run was given"
		}
		return false, "lent from a user's keyring, which the command never takes from"
	}
	if held != keyringref.ServerCredentialName(row.slug) {
		return false, "not the keyring entry the move writes for this server; somebody chose it"
	}
	for _, f := range from {
		if row.migratedFrom == f {
			return true, ""
		}
	}
	if r.o.IncludeNew && row.owner == keyringref.GatewayOwner {
		return true, ""
	}
	return false, "not a credential this command moved"
}

// flip rewrites a server's credential row and its server's auth kind in one
// transaction. set is the assignment list, with $1 the server.
//
// The row must be exactly the row that was read: the same kind, and not
// written since (every write to a credential row moves updated_at). A
// credential somebody replaced in the meantime, a consent that landed, or a
// server that was deleted gives errChanged and changes nothing. The run must
// also still hold the refresh guard, or the switch is not made.
func (r *runner) flip(ctx context.Context, row row, newKind, set string, args ...any) error {
	if err := r.guarded(ctx); err != nil {
		return err
	}
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("switch the gateway's row: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	n := len(args) + 2
	tag, err := tx.Exec(ctx, fmt.Sprintf(`update credentials set %s, updated_at=now() where server_id=$1 and kind=$%d and updated_at=$%d`, set, n, n+1),
		append(append([]any{row.serverID}, args...), row.kind, row.updatedAt)...)
	if err != nil {
		return fmt.Errorf("switch the gateway's row: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errChanged
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

// otherSessions names the sessions on the gateway's database that are not a
// Bifrost of this version, or "" when there are none. Every pool this
// version opens names itself (db.ApplicationName and the keyring's); a
// gateway from before the refresh guard does not, and it is the one thing
// that must not be running while OAuth rows move, because it would refresh
// a token without waiting for the move.
func otherSessions(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	var names *string
	err := pool.QueryRow(ctx, `select string_agg(name || ' x' || n, ', ' order by name) from (
		select coalesce(nullif(application_name,''),'(unnamed)') as name, count(*) as n from pg_stat_activity
		where datname = current_database() and pid <> pg_backend_pid() and backend_type = 'client backend'
		  and coalesce(application_name,'') not like 'bifrost-%' group by 1) s`).Scan(&names)
	if err != nil {
		return "", fmt.Errorf("credmigrate: list database sessions: %w", err)
	}
	if names == nil {
		return "", nil
	}
	return *names, nil
}

// connection is a sealed OAuth row as the keyring takes it.
func (r *runner) connection(row row, s *secret) keyringref.ServerConnection {
	c := keyringref.ServerConnection{
		ServerURL: row.url, Name: row.name, Issuer: row.issuer, AuthorizationEndpoint: row.authzEndpoint, TokenEndpoint: row.tokenEndpoint,
		RegistrationEndpoint: row.registrationEndpoint, RegistrationKind: row.registrationKind, Resource: row.resource, Scopes: row.scopes,
		ClientID: row.clientID, ClientSecret: s.ClientSecret,
		Connected: s.AccessToken != "", TokenType: row.tokenType, AccessToken: s.AccessToken, RefreshToken: s.RefreshToken,
		ExpiresAt: row.expiresAt, LastRefreshedAt: row.refreshedAt,
	}
	if row.state == "broken" && c.Connected {
		c.Broken = row.stateReason
		if c.Broken == "" {
			c.Broken = "refresh refused"
		}
	}
	return c
}

func (r *runner) moveOAuth(ctx context.Context, row row) error {
	ref := keyringref.Ref{Owner: r.o.Owner.String(), Kind: keyringref.KindOAuth, Provider: keyringref.ServerCredentialName(row.slug), Account: keyringref.ServerAccount}
	target := fmt.Sprintf("-> keyring account %s/%s", ref.Provider, ref.Account)
	switch row.kind {
	case "oauth":
	case "keyring_oauth":
		if row.migratedFrom != "" {
			r.sum.Already++
			r.say("already", row, fmt.Sprintf("keyring account %s/%s (moved earlier)", row.provider, row.account))
			return nil
		}
		fallthrough
	default:
		r.sum.Skipped++
		r.say("skip", row, "not an OAuth connection in the gateway's store")
		return nil
	}
	// A person's keyring may already have a provider somebody wrote under
	// the server's name. The gateway's own space cannot.
	if taken, err := r.Vault.ProviderTaken(ctx, ref); err != nil {
		r.fail(row, fmt.Errorf("read the keyring: %w", err))
		return nil
	} else if taken {
		r.fail(row, fmt.Errorf("the owner's keyring already has a provider named %q that is not a server's connection; rename or delete it first", ref.Provider))
		return nil
	}
	target += map[string]string{"ok": "", "pending": " (consent never finished: the registration only)", "broken": " (broken: it stays broken until re-authorized)"}[row.state]
	if r.o.DryRun {
		r.sum.Moved++
		r.say("would move", row, target)
		return nil
	}

	sec, err := r.open(row)
	if err != nil {
		r.fail(row, err)
		return nil
	}
	if row.tokenEndpoint == "" || row.clientID == "" {
		r.fail(row, errors.New("the row has no token endpoint or client id; re-authorize the server instead"))
		return nil
	}
	in := r.connection(row, sec)
	// What the keyring holds for the server before the copy, so it can be
	// put back if the switch does not happen.
	prev, err := r.heldConnection(ctx, ref, row)
	if err != nil {
		r.fail(row, fmt.Errorf("read the keyring: %w", err))
		return nil
	}
	// 1. Copy. Refreshes are held off, so the tokens read above are the
	// current ones until this run ends. The gateway's row still decides
	// what is sent; a run that stops here is repeated from the top.
	if _, err := r.Vault.ImportOAuth(ctx, r.o.Owner, r.tenant, row.use(), in); err != nil {
		r.fail(row, fmt.Errorf("write the keyring: %w", err))
		return nil
	}
	// 2. Read the copy back and compare everything a refresh needs.
	back, err := r.Vault.ExportOAuth(ctx, ref, row.use())
	if err != nil {
		r.fail(row, fmt.Errorf("read the copy back: %w", err))
		return nil
	}
	if !sameConnection(back, in) {
		r.fail(row, errors.New("the keyring's copy does not match the original; the gateway's row is unchanged"))
		return nil
	}
	// 3. Switch the gateway's row to the reference and clear what was
	// copied, if the row is still the one that was copied. Its state is
	// kept: ok, awaiting consent, or broken.
	err = r.flip(ctx, row, "keyring_oauth", `kind='keyring_oauth', keyring_owner=$2, keyring_provider=$3, keyring_account=$4, keyring_secret_name=null,
		secret_blob=null, oauth_issuer=null, oauth_authorization_endpoint=null, oauth_token_endpoint=null, oauth_registration_endpoint=null,
		oauth_registration_kind=null, oauth_client_id=null, oauth_resource=null, oauth_scopes='{}', oauth_token_type=null, oauth_expires_at=null,
		oauth_has_refresh_token=false, last_refreshed_at=null, migrated_from='oauth'`,
		ref.Owner, ref.Provider, ref.Account)
	switch {
	case errors.Is(err, ErrGuardLost):
		_ = r.undoConnection(ctx, row, ref, in, prev)
		return err
	case errors.Is(err, errChanged):
		r.fail(row, r.undoConnection(ctx, row, ref, in, prev))
		return nil
	case err != nil:
		r.fail(row, err)
		return nil
	}
	r.sum.Moved++
	r.say("moved", row, target)
	return nil
}

// heldConnection is the server connection the keyring holds under ref, or
// nil when it holds none.
func (r *runner) heldConnection(ctx context.Context, ref keyringref.Ref, row row) (*keyringref.ServerConnection, error) {
	c, err := r.Vault.ExportOAuth(ctx, ref, row.use())
	if errors.Is(err, keyringref.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// sameConnection reports whether two connections are the same client with
// the same tokens.
func sameConnection(a, b keyringref.ServerConnection) bool {
	return a.TokenEndpoint == b.TokenEndpoint && a.Resource == b.Resource && a.RegistrationKind == b.RegistrationKind && a.ClientID == b.ClientID &&
		same(a.ClientSecret, b.ClientSecret) && a.Connected == b.Connected && same(a.AccessToken, b.AccessToken) && same(a.RefreshToken, b.RefreshToken)
}

// undoConnection is undoSecret for an OAuth connection: it tidies the
// keyring after a copy whose switch did not happen, and returns what to
// report.
//
// A row that changed and still holds the gateway's own sealed connection (a
// consent landed, a token was refreshed) refers to nothing in the keyring:
// the copy is removed and the next run copies the row as it is now. So is
// the copy of a server that was deleted. A row that now refers to the
// keyring's connection was re-authorized through the keyring while the old
// one was being copied; if the copy has overwritten that, what the keyring
// held before is put back, or the report says to authorize again.
func (r *runner) undoConnection(ctx context.Context, row row, ref keyringref.Ref, copied keyringref.ServerConnection, prev *keyringref.ServerConnection) error {
	now, err := r.reload(ctx, row)
	if err != nil {
		return fmt.Errorf("%w (the row could not be read again, so the keyring's copy %s was left: %v)", errChanged, ref.Provider, err)
	}
	if now == nil || now.kind != "keyring_oauth" || now.owner != ref.Owner || now.provider != ref.Provider {
		if err := r.Vault.RemoveOAuth(ctx, ref, row.use()); err != nil {
			return fmt.Errorf("%w (the keyring's copy %s could not be removed: %v)", errChanged, ref.Provider, err)
		}
		return errChanged
	}
	held, err := r.Vault.ExportOAuth(ctx, ref, row.use())
	if err != nil || !sameConnection(held, copied) {
		return errChanged // the keyring holds the server's new connection, not the copy
	}
	if prev != nil && sameConnection(*prev, copied) {
		return errChanged
	}
	if prev != nil {
		if _, err := r.Vault.ImportOAuth(ctx, r.o.Owner, r.tenant, row.use(), *prev); err == nil {
			return fmt.Errorf("%w (the keyring's connection was put back as it was before the copy)", errChanged)
		}
	}
	return fmt.Errorf("%w. The server was being authorized again while its old connection was copied, and the keyring may hold the old one: authorize the server again", errChanged)
}

func (r *runner) restoreOAuth(ctx context.Context, row row) error {
	ref := keyringref.Ref{Owner: row.owner, Kind: keyringref.KindOAuth, Provider: row.provider, Account: row.account}
	source := fmt.Sprintf("<- keyring account %s/%s", row.provider, row.account)
	switch {
	case row.kind == "oauth":
		r.sum.Already++
		r.say("already", row, "in the gateway's store")
		return nil
	case row.kind != "keyring_oauth":
		r.sum.Skipped++
		r.say("skip", row, "not a connection this command moved")
		return nil
	}
	if ok, why := r.mine(row, row.provider, "oauth"); !ok {
		r.sum.Skipped++
		r.say("skip", row, why)
		return nil
	}
	if row.account != keyringref.ServerAccount {
		r.sum.Skipped++
		r.say("skip", row, "not the account the move writes for this server; somebody chose it")
		return nil
	}
	if r.o.DryRun {
		r.sum.Moved++
		r.say("would restore", row, source)
		return nil
	}
	// The keyring's tokens are the truth: they have been refreshed since
	// the move, and refreshes are held off while this runs.
	held, err := r.Vault.ExportOAuth(ctx, ref, row.use())
	if err != nil {
		r.fail(row, fmt.Errorf("read the keyring: %w", err))
		return nil
	}
	blob, err := r.seal(row, &secret{AccessToken: held.AccessToken, RefreshToken: held.RefreshToken, ClientSecret: held.ClientSecret})
	if err != nil {
		r.fail(row, errors.New("seal the connection for the gateway's store"))
		return nil
	}
	state, reason := "ok", ""
	switch {
	case !held.Connected:
		state, reason = "pending", "awaiting consent"
	case held.Broken != "":
		state, reason = "broken", held.Broken
	case row.state == "broken":
		state, reason = "broken", row.stateReason
	}
	scopes := held.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	// Nothing has been written yet, so a row that changed is simply left.
	err = r.flip(ctx, row, "oauth", `kind='oauth', secret_blob=$2, oauth_issuer=nullif($3,''), oauth_authorization_endpoint=nullif($4,''),
		oauth_token_endpoint=nullif($5,''), oauth_registration_endpoint=nullif($6,''), oauth_registration_kind=nullif($7,''), oauth_client_id=nullif($8,''),
		oauth_resource=nullif($9,''), oauth_scopes=$10, oauth_token_type=nullif($11,''), oauth_expires_at=$12, oauth_has_refresh_token=$13,
		last_refreshed_at=$14, state=$15, state_reason=nullif($16,''),
		keyring_owner=null, keyring_provider=null, keyring_account=null, keyring_secret_name=null, migrated_from=null`,
		blob, held.Issuer, held.AuthorizationEndpoint, held.TokenEndpoint, held.RegistrationEndpoint, held.RegistrationKind, held.ClientID,
		held.Resource, scopes, held.TokenType, held.ExpiresAt, held.RefreshToken != "", held.LastRefreshedAt, state, reason)
	switch {
	case errors.Is(err, ErrGuardLost):
		return err
	case err != nil:
		r.fail(row, err)
		return nil
	}
	// The gateway's row decides again. A copy in the gateway's own space is
	// now nobody's and is removed; one in a person's keyring is left to them.
	if !ref.HeldByGateway() {
		source += " (the connection stays in the user's keyring)"
	} else if err := r.Vault.RemoveOAuth(ctx, ref, row.use()); err != nil {
		fmt.Fprintf(r.o.Out, "%-13s %-7s %-24s restored, but the keyring account %s could not be deleted: %v\n", "note", "oauth", row.slug, row.provider, err)
	}
	r.sum.Moved++
	r.say("restored", row, source)
	return nil
}

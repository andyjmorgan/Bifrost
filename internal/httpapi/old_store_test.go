package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/keyringref"
	"github.com/andyjmorgan/Bifrost/internal/store"
)

// An older gateway sealed a server's own credential in columns of its
// credentials table. The API neither reads nor writes them any more, so the
// tests reach them with SQL: to check nothing is left there, and to make a
// row as `gateway migrate-credentials` would find it.

// oldStore is what those columns hold for one server.
type oldStore struct {
	blob                                                    []byte
	username, header, clientID, tokenEndpoint, migratedFrom string
}

// any reports whether anything of a credential is left in the old columns.
func (o oldStore) any() bool {
	return len(o.blob) != 0 || o.username != "" || o.header != "" || o.clientID != "" || o.tokenEndpoint != "" || o.migratedFrom != ""
}

func (r *apiRig) oldStore(id string) oldStore {
	r.t.Helper()
	var o oldStore
	err := r.pool.QueryRow(context.Background(), `select secret_blob, coalesce(basic_username,''), coalesce(header_name,''),
		coalesce(oauth_client_id,''), coalesce(oauth_token_endpoint,''), coalesce(migrated_from,'') from credentials where server_id=$1`, uuid.MustParse(id)).
		Scan(&o.blob, &o.username, &o.header, &o.clientID, &o.tokenEndpoint, &o.migratedFrom)
	if err != nil {
		r.t.Fatal(err)
	}
	return o
}

// unmoved turns a server's credential into one of kind that an older gateway
// sealed itself and the migration has not reached. What it seals is what the
// stub MCP server answers to, so a gateway that sent it would get through.
func (r *apiRig) unmoved(id string, kind store.AuthKind) {
	r.t.Helper()
	ctx := context.Background()
	sid := uuid.MustParse(id)
	var (
		sealed                                    map[string]string
		username, header, clientID, tokenEndpoint string
	)
	switch kind {
	case store.AuthBasic:
		sealed, username = map[string]string{"password": stubSecret}, stubUser
	case store.AuthHeader:
		sealed, header = map[string]string{"headerValue": stubSecret}, stubHeader
	case store.AuthOAuth:
		sealed, clientID, tokenEndpoint = map[string]string{"accessToken": stubToken}, "stub-client", r.mcpURL+"/token"
	default:
		r.t.Fatalf("%s is not a kind the gateway sealed itself", kind)
	}
	plain, err := json.Marshal(sealed)
	if err != nil {
		r.t.Fatal(err)
	}
	blob, err := r.sealer.Seal(plain, sid[:])
	if err != nil {
		r.t.Fatal(err)
	}
	tag, err := r.pool.Exec(ctx, `update credentials set kind=$2, state='ok', state_reason=null, last_error=null, secret_blob=$3,
		basic_username=nullif($4,''), header_name=nullif($5,''), oauth_client_id=nullif($6,''), oauth_token_endpoint=nullif($7,''),
		keyring_owner=null, keyring_provider=null, keyring_account=null, keyring_secret_name=null where server_id=$1`,
		sid, string(kind), blob, username, header, clientID, tokenEndpoint)
	if err != nil || tag.RowsAffected() != 1 {
		r.t.Fatalf("seed an unmoved %s credential: %d rows, %v", kind, tag.RowsAffected(), err)
	}
	if _, err := r.pool.Exec(ctx, `update servers set auth_kind=$2 where id=$1`, sid, string(kind)); err != nil {
		r.t.Fatal(err)
	}
}

// A server whose credential is still in the gateway's old store is shown as
// what it is, broken, with the reason and the command that puts it right.
// The gateway does not open the sealed blob: a request through the server
// fails before anything is sent, whether the keyring is on or off.
func TestACredentialNotMovedToTheKeyringIsShownAndNotSent(t *testing.T) {
	for _, kind := range []store.AuthKind{store.AuthHeader, store.AuthBasic, store.AuthOAuth} {
		for name, kr := range map[string]keyringref.Holder{"keyring on": aliceHolds(), "keyring off": nil} {
			t.Run(string(kind)+", "+name, func(t *testing.T) {
				r := newAPIRig(t, kr)
				s := r.server()
				r.unmoved(s.ID, kind)

				for _, path := range []string{"/servers/" + s.ID, "/servers"} {
					status, body := r.do(http.MethodGet, path, alice, "", nil)
					if status != http.StatusOK {
						t.Fatalf("GET %s: %d %s", path, status, body)
					}
					noCredentialIn(t, "GET "+path, body)
				}
				_, body := r.do(http.MethodGet, "/servers/"+s.ID, alice, "", nil)
				var got ServerDTO
				if err := json.Unmarshal([]byte(body), &got); err != nil || got.Credential == nil {
					t.Fatalf("%v in %s", err, body)
				}
				c := got.Credential
				if got.AuthKind != kind || c.Kind != kind || c.Store != "gateway" {
					t.Fatalf("shown as %s/%s in %q, want %s in the gateway's store: %s", got.AuthKind, c.Kind, c.Store, kind, body)
				}
				if c.State != store.StateBroken || !strings.Contains(c.StateReason, "run gateway migrate-credentials") {
					t.Fatalf("shown as %s (%q), want broken with the reason: %s", c.State, c.StateReason, body)
				}
				// The row still says ok: what the API shows is not written back.
				if row, err := r.store.GetCredential(context.Background(), uuid.MustParse(s.ID)); err != nil || row.Kind != kind || row.State != store.StateOK {
					t.Fatalf("the row is %+v (%v)", row, err)
				}
				// Nothing is read out of the old columns, not even the names.
				if c.HeaderName != "" || c.BasicUsername != "" || c.OAuthClientID != "" {
					t.Fatalf("the API read the gateway's old columns: %s", body)
				}

				before := r.reached.Load()
				status, body := r.do(http.MethodPost, "/servers/"+s.ID+"/ping", alice, "", nil)
				var ping PingResult
				if err := json.Unmarshal([]byte(body), &ping); err != nil || status != http.StatusOK {
					t.Fatalf("ping: %d %s", status, body)
				}
				if ping.OK || !strings.Contains(ping.Error, "run `gateway migrate-credentials`") {
					t.Fatalf("the ping does not say what to do: %s", body)
				}
				if n := r.reached.Load() - before; n != 0 {
					t.Fatalf("the server received %d requests from a gateway with no credential to send", n)
				}
				noCredentialIn(t, "the ping result", body)
				if logs := r.logs.String(); !strings.Contains(logs, "migrate-credentials") || !strings.Contains(logs, s.Slug) {
					t.Fatal("the refusal was not logged with the server's slug")
				}
				noCredentialIn(t, "the gateway's logs", r.logs.String())

				// Consent cannot be restarted on a connection the gateway cannot read.
				if kind == store.AuthOAuth {
					status, body := r.do(http.MethodPost, "/servers/"+s.ID+"/oauth/start", alice, "", nil)
					if status != http.StatusBadRequest || !strings.Contains(body, "migrate-credentials") {
						t.Fatalf("oauth/start on an unmoved connection: %d %s", status, body)
					}
				}

				// The API changed nothing: the row waits for the command as it was.
				if o := r.oldStore(s.ID); len(o.blob) == 0 {
					t.Fatal("looking at an unmoved credential cleared its sealed blob")
				}
			})
		}
	}
}

// Giving such a server a new credential replaces the old one whole: the new
// one goes to the keyring and nothing sealed is left beside the reference.
func TestReplacingACredentialNotMovedToTheKeyring(t *testing.T) {
	kr := aliceHolds()
	r := newAPIRig(t, kr)
	s := r.server()
	r.unmoved(s.ID, store.AuthBasic)

	status, body := r.do(http.MethodPut, "/servers/"+s.ID+"/credential", alice, "", headerInput)
	if status != http.StatusOK {
		t.Fatalf("header: %d %s", status, body)
	}
	got := decodeServer(t, body)
	if got.AuthKind != store.AuthHeader || got.Credential.Store != "keyring" || got.Credential.State != store.StateOK || got.Credential.StateReason != "" {
		t.Fatalf("not a working header credential in the keyring: %s", body)
	}
	if r.oldStore(s.ID).any() {
		t.Fatal("the replaced credential left something in the gateway's own columns")
	}
	if !r.pings(s.ID) {
		t.Fatal("the server does not answer with its new credential")
	}
}

// With the keyring off the replacement is refused, and a refused change does
// not touch the row: the sealed credential is still there for the command.
func TestKeyringOffACredentialNotMovedIsLeftAlone(t *testing.T) {
	r := newAPIRig(t, nil)
	s := r.server()
	r.unmoved(s.ID, store.AuthHeader)
	before := r.oldStore(s.ID)

	status, body := r.do(http.MethodPut, "/servers/"+s.ID+"/credential", alice, "", basicInput)
	if status != http.StatusBadRequest || !strings.Contains(body, keyringref.ErrOff.Error()) {
		t.Fatalf("basic: %d %s", status, body)
	}
	after := r.oldStore(s.ID)
	if string(after.blob) != string(before.blob) || after.header != stubHeader {
		t.Fatal("a refused change altered the sealed credential")
	}
	if row, err := r.store.GetCredential(context.Background(), uuid.MustParse(s.ID)); err != nil || row.Kind != store.AuthHeader {
		t.Fatalf("a refused change switched the row: %+v (%v)", row, err)
	}
	// Renaming the server, or marking its credential, does not touch it either.
	if status, body := r.do(http.MethodPatch, "/servers/"+s.ID, alice, "", UpdateServerInput{Description: ptr("still waiting")}); status != http.StatusOK {
		t.Fatalf("patch: %d %s", status, body)
	}
	if err := r.store.SetCredentialState(context.Background(), uuid.MustParse(s.ID), store.StateBroken, "x"); err != nil {
		t.Fatal(err)
	}
	if after := r.oldStore(s.ID); string(after.blob) != string(before.blob) || after.header != stubHeader {
		t.Fatal("an edit that was not a new credential altered the sealed credential")
	}
}

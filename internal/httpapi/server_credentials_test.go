package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/keyringref"
	"github.com/andyjmorgan/Bifrost/internal/store"
)

var (
	headerInput = AuthInput{Kind: store.AuthHeader, HeaderName: stubHeader, HeaderValue: stubSecret}
	basicInput  = AuthInput{Kind: store.AuthBasic, Username: stubUser, Password: stubSecret}
)

// sealedBlob reads the column the gateway used to seal credentials in.
func (r *apiRig) sealedBlob(id string) []byte {
	r.t.Helper()
	c, err := r.store.GetCredential(context.Background(), uuid.MustParse(id))
	if err != nil {
		r.t.Fatal(err)
	}
	return c.SecretBlob
}

func (r *apiRig) pings(id string) bool {
	r.t.Helper()
	status, body := r.do(http.MethodPost, "/servers/"+id+"/ping", alice, "", nil)
	var ping PingResult
	if err := json.Unmarshal([]byte(body), &ping); err != nil || status != http.StatusOK {
		r.t.Fatalf("ping: %d %s", status, body)
	}
	return ping.OK
}

// With the keyring on, a Basic or header credential typed in for a server is
// stored in the gateway's own keyring space. The gateway keeps the reference
// and no secret, and the API goes on calling the credential what it is.
func TestAStaticCredentialIsStoredInTheKeyring(t *testing.T) {
	for name, in := range map[string]AuthInput{"header": headerInput, "basic": basicInput} {
		t.Run(name, func(t *testing.T) {
			kr := aliceHolds()
			r := newAPIRig(t, kr)
			slug := "k" + uuid.NewString()[:8]
			status, body := r.do(http.MethodPost, "/servers", alice, "", CreateServerInput{Name: "stub " + slug, Slug: slug, URL: r.mcpURL, Auth: in})
			if status != http.StatusCreated {
				t.Fatalf("create: %d %s", status, body)
			}
			noCredentialIn(t, "the response to creating the server", body)
			s := decodeServer(t, body)
			t.Cleanup(func() { _ = r.store.DeleteServer(context.Background(), uuid.MustParse(s.ID)) })

			// Shown as what the operator set, and said to be in the keyring.
			if s.AuthKind != in.Kind || s.Credential == nil || s.Credential.Kind != in.Kind || s.Credential.Store != "keyring" {
				t.Fatalf("the server is not shown as %s held in the keyring: %s", in.Kind, body)
			}
			if in.Kind == store.AuthHeader && s.Credential.HeaderName != stubHeader {
				t.Fatalf("the header name is not shown: %s", body)
			}
			if in.Kind == store.AuthBasic && s.Credential.BasicUsername != stubUser {
				t.Fatalf("the username is not shown: %s", body)
			}

			// Stored as a reference the gateway holds, with nothing sealed beside it.
			row, err := r.store.GetCredential(context.Background(), uuid.MustParse(s.ID))
			if err != nil {
				t.Fatal(err)
			}
			want := keyringref.Ref{Owner: keyringref.GatewayOwner, Kind: keyringref.KindSecret, Secret: keyringref.ServerCredentialName(slug)}
			if row.Kind != store.AuthKeyringSecret || row.KeyringRef() != want {
				t.Fatalf("stored as %s %+v, want a keyring secret %+v", row.Kind, row.KeyringRef(), want)
			}
			if len(row.SecretBlob) != 0 || row.BasicUsername != "" || row.HeaderName != "" {
				t.Fatal("the gateway kept part of the credential in its own store")
			}
			held, ok := kr.gateway.secret(want.Secret)
			if !ok || held.Value != stubSecret {
				t.Fatal("the keyring was not given the credential")
			}
			if use := kr.gateway.puts[0]; use.Slug != slug || use.ServerID != s.ID || use.Actor != alice {
				t.Fatalf("the keyring was told %+v, want the server and Alice", use)
			}

			// It authenticates, and no listing or log line carries it.
			if !r.pings(s.ID) {
				t.Fatal("the server does not answer with its keyring credential")
			}
			_, list := r.do(http.MethodGet, "/servers", alice, "", nil)
			noCredentialIn(t, "the server list", list)
			noCredentialIn(t, "the gateway's logs", r.logs.String())

			// Deleting the server deletes what the gateway held for it.
			if status, body := r.do(http.MethodDelete, "/servers/"+s.ID, alice, "", nil); status != http.StatusNoContent {
				t.Fatalf("delete: %d %s", status, body)
			}
			if _, ok := kr.gateway.secret(want.Secret); ok {
				t.Fatal("the credential outlived its server")
			}
		})
	}
}

// Replacing a server's static credential replaces the keyring secret under
// the same name; giving the server another kind of credential deletes it.
func TestReplacingAStaticCredential(t *testing.T) {
	kr := aliceHolds()
	r := newAPIRig(t, kr)
	s := r.server()
	name := keyringref.ServerCredentialName(s.Slug)

	if status, body := r.do(http.MethodPut, "/servers/"+s.ID+"/credential", alice, "", headerInput); status != http.StatusOK {
		t.Fatalf("header: %d %s", status, body)
	}
	status, body := r.do(http.MethodPut, "/servers/"+s.ID+"/credential", alice, "", basicInput)
	if status != http.StatusOK {
		t.Fatalf("basic: %d %s", status, body)
	}
	if got := decodeServer(t, body); got.AuthKind != store.AuthBasic || got.Credential.BasicUsername != stubUser || got.Credential.HeaderName != "" {
		t.Fatalf("not shown as Basic after the change: %s", body)
	}
	if held, _ := kr.gateway.secret(name); held.Scheme != keyringref.SchemeBasic {
		t.Fatalf("the keyring holds %q under the server's name, want basic", held.Scheme)
	}
	if len(kr.gateway.forgot) != 0 {
		t.Fatalf("a credential still in use was deleted: %v", kr.gateway.forgot)
	}
	if !r.pings(s.ID) {
		t.Fatal("the server does not answer with its Basic credential")
	}

	// Pointing the server at Alice's own secret makes the gateway's redundant.
	if status, body := r.do(http.MethodPut, "/servers/"+s.ID+"/credential", alice, "", secretInput); status != http.StatusOK {
		t.Fatalf("keyring secret: %d %s", status, body)
	}
	if _, ok := kr.gateway.secret(name); ok {
		t.Fatal("the gateway's credential was kept after the server was given another")
	}
	// And taking the credential away leaves Alice's alone.
	if status, body := r.do(http.MethodPut, "/servers/"+s.ID+"/credential", alice, "", AuthInput{Kind: store.AuthNone}); status != http.StatusOK {
		t.Fatalf("none: %d %s", status, body)
	}
	if got := kr.gateway.forgot; len(got) != 1 || got[0] != name {
		t.Fatalf("forgot %v, want only the server's own credential once", got)
	}
}

// A credential the gateway holds for a server is nobody's in particular:
// an API key may set one, and any operator may re-point the server, as both
// could when the gateway stored it itself.
func TestAServersOwnCredentialIsNotOnePersons(t *testing.T) {
	r := newAPIRig(t, aliceHolds())
	s := r.server()
	_, key, err := r.keys.Create(context.Background(), alice, "edit "+uuid.NewString()[:8], auth.AllScopes)
	if err != nil {
		t.Fatal(err)
	}
	status, body := r.do(http.MethodPut, "/servers/"+s.ID+"/credential", "", key, headerInput)
	if status != http.StatusOK {
		t.Fatalf("an API key could not set a header credential: %d %s", status, body)
	}
	if got := decodeServer(t, body); got.Credential.Store != "keyring" || got.AuthKind != store.AuthHeader {
		t.Fatalf("not a header credential in the keyring: %s", body)
	}
	if status, body := r.do(http.MethodPatch, "/servers/"+s.ID, bob, "", UpdateServerInput{URL: ptr(r.mcpURL + "/")}); status != http.StatusOK {
		t.Fatalf("Bob could not re-point a server that holds its own credential: %d %s", status, body)
	}
}

// With the keyring off a static credential is sealed in the gateway's own
// store, and the API says so.
func TestKeyringOffAStaticCredentialStaysInTheGateway(t *testing.T) {
	r := newAPIRig(t, nil)
	s := r.server()
	status, body := r.do(http.MethodPut, "/servers/"+s.ID+"/credential", alice, "", headerInput)
	if status != http.StatusOK {
		t.Fatalf("header: %d %s", status, body)
	}
	got := decodeServer(t, body)
	if got.AuthKind != store.AuthHeader || got.Credential.Store != "gateway" || got.Credential.HeaderName != stubHeader {
		t.Fatalf("not shown as a header credential in the gateway's store: %s", body)
	}
	if len(r.sealedBlob(s.ID)) == 0 {
		t.Fatal("nothing was sealed")
	}
	if strings.Contains(body, stubSecret) {
		t.Fatal("the response carries the credential")
	}
}

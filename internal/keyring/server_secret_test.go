package keyring

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/keyring/audit"
	"github.com/andyjmorgan/Bifrost/internal/keyring/contracts"
	"github.com/andyjmorgan/Bifrost/internal/keyring/service"
	"github.com/andyjmorgan/Bifrost/internal/keyringref"
)

var serverValue = strings.Join([]string{"server", "credential", "fixture"}, "-")

func gatewayRef(slug string) keyringref.Ref {
	return keyringref.Ref{Owner: keyringref.GatewayOwner, Kind: keyringref.KindSecret, Secret: keyringref.ServerCredentialName(slug)}
}

// A server's static credential is stored in the gateway's own space, sent as it was described, and
// deleted with the server. Each step is on record, with the server and the person who asked.
func TestSourceHoldsAServersStaticCredential(t *testing.T) {
	r := newSourceRig(t)
	ctx := context.Background()
	use := gatewayUse
	use.Actor = "user-ada"

	ref, err := r.src.PutServerSecret(ctx, use, keyringref.ServerSecret{Scheme: keyringref.SchemeHeader, HeaderName: "X-Api-Key", Value: serverValue, URL: "https://mcp.example/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if ref != gatewayRef(use.Slug) {
		t.Fatalf("stored as %+v, want %+v", ref, gatewayRef(use.Slug))
	}
	if err := r.src.Check(ctx, ref); err != nil {
		t.Fatalf("the stored credential does not check: %v", err)
	}
	if d, err := r.src.Describe(ctx, ref); err != nil || d.Scheme != keyringref.SchemeHeader || d.HeaderName != "X-Api-Key" {
		t.Fatalf("described as %+v (%v)", d, err)
	}
	h, err := r.src.SecretHeader(ctx, ref, gatewayUse)
	if err != nil || h.Name != "X-Api-Key" || h.Value != serverValue {
		t.Fatalf("sent as %q (%v)", h.Name, err)
	}

	// Replaced by a Basic credential under the same name.
	if _, err := r.src.PutServerSecret(ctx, use, keyringref.ServerSecret{Scheme: keyringref.SchemeBasic, Username: "svc", Value: serverValue}); err != nil {
		t.Fatal(err)
	}
	if d, err := r.src.Describe(ctx, ref); err != nil || d.Scheme != keyringref.SchemeBasic || d.Username != "svc" || d.HeaderName != "" {
		t.Fatalf("described as %+v (%v) after the change", d, err)
	}
	h, err = r.src.SecretHeader(ctx, ref, gatewayUse)
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("svc:"+serverValue)); err != nil || h.Name != "Authorization" || h.Value != want {
		t.Fatalf("Basic is not sent as Authorization: Basic (%v)", err)
	}

	if err := r.src.Forget(ctx, ref, use); err != nil {
		t.Fatal(err)
	}
	if err := r.src.Check(ctx, ref); !errors.Is(err, keyringref.ErrNotFound) {
		t.Fatalf("the credential outlived Forget: %v", err)
	}
	if err := r.src.Forget(ctx, ref, use); err != nil {
		t.Fatalf("forgetting what is gone: %v", err)
	}

	var got []string
	for _, e := range r.events() {
		if e.UserID.String() != keyringref.GatewayOwner || e.Transport != gatewayTransport {
			t.Fatalf("a %s event is not the gateway's own: user %s over %s", e.Type, e.UserID, e.Transport)
		}
		line := e.Type.String()
		if e.Type != audit.EventTokenAccessed {
			if e.Method == nil || !strings.Contains(*e.Method, use.Slug) || !strings.HasSuffix(*e.Method, " by user-ada") {
				t.Fatalf("a %s event does not name the server and who asked: %v", e.Type, e.Method)
			}
			line += " " + deref(e.TargetName)
		}
		got = append(got, line)
		if strings.Contains(fmt.Sprintf("%+v %s %s", e, deref(e.Detail), deref(e.Method)), serverValue) {
			t.Fatalf("a %s event carries the credential", e.Type)
		}
	}
	want := "CredentialCreated mcp-xmcp, TokenAccessed, CredentialChanged mcp-xmcp, TokenAccessed, CredentialRemoved mcp-xmcp"
	if strings.Join(got, ", ") != want {
		t.Fatalf("audit trail:\n got %s\nwant %s", strings.Join(got, ", "), want)
	}
}

// Forget deletes only what the gateway holds. A credential a person lent is theirs.
func TestForgetLeavesAPersonsCredentialAlone(t *testing.T) {
	r := newSourceRig(t)
	ada := newPerson()
	r.secret(ada, service.CreateAPIKeyParams{Name: "atlas", Secret: str(serverValue), Kind: contracts.KindHeaderAPIKey, Header: str("X-Api-Key")})
	ref := secretRefOf(ada, "atlas")
	if err := r.src.Forget(context.Background(), ref, gatewayUse); err != nil {
		t.Fatal(err)
	}
	if err := r.src.Check(context.Background(), ref); err != nil {
		t.Fatalf("Ada's secret was deleted: %v", err)
	}
}

// Nobody acts as the gateway's owner through the keyring's API: not with a token whose subject is
// that id, and not with an access key that somehow belongs to it.
func TestTheGatewaysSpaceIsNotReachableThroughTheAPI(t *testing.T) {
	k, h := newTestKeyring(t, testConfig())
	ctx := contracts.WithCaller(context.Background(), contracts.Caller{UserID: gatewayOwnerID})
	if _, err := k.source.PutServerSecret(context.Background(), gatewayUse, keyringref.ServerSecret{Scheme: keyringref.SchemeHeader, HeaderName: "X-Api-Key", Value: serverValue}); err != nil {
		t.Fatal(err)
	}
	// An access key minted straight into the gateway's space, as only a bug or a hand-edited
	// database could.
	_, key, err := service.NewAccessKeyService(k.source.store, k.auditLog).Create(ctx, "rogue", nil, []string{"vault:readwrite"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/api-keys", "/api/v1/api-keys/" + gatewayRef(gatewayUse.Slug).Secret + "/reveal"} {
		req := httptest.NewRequest(http.MethodGet, MountPrefix+path, nil)
		req.Header.Set("X-Api-Key", key)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("GET %s as the gateway's owner: %d, want 403", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), serverValue) {
			t.Fatal("the response carries a server's credential")
		}
	}
}

// The migration door writes to the owner it is told, reads the value back, and removes it.
func TestMigrationMovesASecretInAndOut(t *testing.T) {
	r := newSourceRig(t)
	m := r.k.Migration()
	ctx := context.Background()
	ada := newPerson()
	r.secret(ada, service.CreateAPIKeyParams{Name: "existing", Secret: str("x"), Kind: contracts.KindOpaque})

	if tenant, err := m.OwnerTenant(ctx, ada.id); err != nil || tenant != ada.tenant {
		t.Fatalf("Ada's tenant: %s (%v), want %s", tenant, err, ada.tenant)
	}
	if tenant, err := m.OwnerTenant(ctx, gatewayOwnerID); err != nil || tenant != uuid.Nil {
		t.Fatalf("the gateway's tenant: %s (%v)", tenant, err)
	}

	for _, owner := range []person{{id: gatewayOwnerID}, ada} {
		for _, in := range []keyringref.ServerSecret{
			{Scheme: keyringref.SchemeBasic, Username: "svc", Value: serverValue},
			{Scheme: keyringref.SchemeHeader, HeaderName: "X-Api-Key", Value: serverValue},
		} {
			ref, err := m.ImportSecret(ctx, owner.id, owner.tenant, gatewayUse, in)
			if err != nil {
				t.Fatal(err)
			}
			if ref.Owner != owner.id.String() || ref.Secret != "mcp-xmcp" {
				t.Fatalf("imported as %+v", ref)
			}
			if has, err := m.HasSecret(ctx, ref); err != nil || !has {
				t.Fatalf("HasSecret: %v %v", has, err)
			}
			out, err := m.ExportSecret(ctx, ref, gatewayUse)
			if err != nil || out.Scheme != in.Scheme || out.Username != in.Username || out.Value != in.Value {
				t.Fatalf("exported %s for %s (%v)", out.Scheme, in.Scheme, err)
			}
			if in.Scheme == keyringref.SchemeHeader && out.HeaderName != in.HeaderName {
				t.Fatalf("exported header %q", out.HeaderName)
			}
			if err := m.RemoveSecret(ctx, ref, gatewayUse); err != nil {
				t.Fatal(err)
			}
			if has, _ := m.HasSecret(ctx, ref); has {
				t.Fatal("the secret outlived RemoveSecret")
			}
		}
	}
	// A prefix is part of what is sent, so it comes out as part of the value.
	r.secret(ada, service.CreateAPIKeyParams{Name: "mcp-xmcp", Secret: str(serverValue), Kind: contracts.KindHeaderAPIKey, Prefix: str("Bearer ")})
	out, err := m.ExportSecret(ctx, secretRefOf(ada, "mcp-xmcp"), gatewayUse)
	if err != nil || out.HeaderName != "Authorization" || out.Value != "Bearer "+serverValue {
		t.Fatalf("a prefixed secret came out under %q (%v)", out.HeaderName, err)
	}
}

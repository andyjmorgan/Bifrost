package httpapi

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/keyring/audit"
	"github.com/andyjmorgan/Bifrost/internal/keyring/service"
)

// TestVaultScopesForExtraWebClients covers the gateway's SPA: a client of the same issuer that is
// accepted as a web caller beside Vault's own.
func TestVaultScopesForExtraWebClients(t *testing.T) {
	web, cli, gateway := "vault-web", "vault-cli", "gateway-web"
	cases := []struct {
		name      string
		clientID  string
		audiences []string
		extra     []string
		want      []string
	}{
		{"gateway client, its own audience", gateway, []string{gateway}, []string{gateway}, webVaultScopes},
		{"gateway client, any audience", gateway, []string{"account", "https://gw/mcp"}, []string{gateway}, webVaultScopes},
		{"gateway client not configured", gateway, []string{gateway}, nil, nil},
		{"vault web still full", web, []string{web}, []string{gateway}, webVaultScopes},
		{"cli still the subset", cli, []string{web}, []string{gateway}, cliVaultScopes},
		{"cli issued for the gateway audience only", cli, []string{gateway}, []string{gateway}, nil},
		{"unknown client", "other", []string{gateway}, []string{gateway}, nil},
		{"empty client id never matches", "", []string{gateway}, []string{gateway, ""}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := vaultScopesFor(c.clientID, c.audiences, web, cli, c.extra...); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}

// newGatewayJWTHarness is a JWT-authenticated server with the gateway's SPA client accepted beside
// Vault's, configured the way the keyring module configures it (through OIDCConfig).
func newGatewayJWTHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	auditLog := audit.NewLog(100, nil, nil)
	srv, err := NewServer(context.Background(), Deps{
		APIKeys:    service.NewAPIKeyService(h.ms, h.cipher, auditLog),
		AccessKeys: service.NewAccessKeyService(h.ms, auditLog),
		AuditLog:   auditLog, AuditQuery: audit.NewQueryService(h.ms, auditLog),
		IPResolver: audit.NewForwardedIPResolver(nil),
		OIDC:       OIDCConfig{WebClientID: "web", CliClientID: "cli", ExtraWebClientIDs: []string{"gateway-web"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.verifier.Store(oidc.NewVerifier(testIssuer, fakeKeySet{}, &oidc.Config{SkipClientIDCheck: true}))
	srv.authOn = true
	h.h = srv.Handler()
	return h
}

// TestJWTGatewayWebClient is the gateway's SPA calling the keyring: its token names the gateway's
// client in azp and carries the gateway's audiences, not Vault's.
func TestJWTGatewayWebClient(t *testing.T) {
	h := newGatewayJWTHarness(t)
	sub := uuid.NewString()
	claims := func(azp string, aud ...string) map[string]any {
		return map[string]any{"iss": testIssuer, "sub": sub, "aud": aud, "azp": azp, "exp": time.Now().Add(time.Hour).Unix()}
	}
	gateway := makeJWT(t, claims("gateway-web", "gateway-web", "account"))

	// A web caller: read, write and the audit trail.
	for _, path := range []string{"/api/v1/me", "/api/v1/api-keys", "/api/v1/audit"} {
		if rec := bearer(h, t, path, gateway); rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}

	// The same person through Vault's own client is the same user: rows follow the subject, not
	// the client that asked.
	vault := makeJWT(t, claims("web", "web"))
	viaGateway := decode[meResponse](t, bearer(h, t, "/api/v1/me", gateway))
	viaVault := decode[meResponse](t, bearer(h, t, "/api/v1/me", vault))
	if viaGateway.UserID == nil || viaVault.UserID == nil || *viaGateway.UserID != sub || *viaVault.UserID != sub {
		t.Fatalf("user ids: gateway %v, vault %v, want %s", viaGateway.UserID, viaVault.UserID, sub)
	}

	// The CLI's token is unchanged: the operational subset, no audit.
	cli := makeJWT(t, claims("cli", "web"))
	if rec := bearer(h, t, "/api/v1/api-keys", cli); rec.Code != 200 {
		t.Fatalf("cli read: %d", rec.Code)
	}
	if rec := bearer(h, t, "/api/v1/audit", cli); rec.Code != http.StatusForbidden {
		t.Fatalf("cli audit: %d, want 403", rec.Code)
	}

	// A client that is neither is authenticated but holds no scope.
	if rec := bearer(h, t, "/api/v1/api-keys", makeJWT(t, claims("some-other-app", "gateway-web"))); rec.Code != http.StatusForbidden {
		t.Fatalf("unknown client: %d, want 403", rec.Code)
	}
}

// TestAfterCallback covers the page the browser is sent to after an OAuth callback: the configured
// one, with and without a query of its own, and Vault's relative default.
func TestAfterCallback(t *testing.T) {
	for base, want := range map[string]string{
		"https://ui.test/connect":       "https://ui.test/connect?oauth_error=access+denied",
		"https://ui.test/connect?tab=x": "https://ui.test/connect?tab=x&oauth_error=access+denied",
		"":                              "/oauthconnect?oauth_error=access+denied",
	} {
		s := &Server{deps: Deps{PostCallbackURL: base}}
		if got := s.afterCallback("oauth_error", "access denied"); got != want {
			t.Errorf("base %q: %q, want %q", base, got, want)
		}
	}
}

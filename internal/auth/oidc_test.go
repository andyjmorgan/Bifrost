package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// issuer is a minimal OIDC provider: discovery, a JWKS and a signer.
type issuer struct {
	srv    *httptest.Server
	signer jose.Signer
}

func newIssuer(t *testing.T) *issuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "k1"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	iss := &issuer{signer: signer}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": iss.srv.URL, "jwks_uri": iss.srv.URL + "/jwks",
			"authorization_endpoint": iss.srv.URL + "/auth", "token_endpoint": iss.srv.URL + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	iss.srv = httptest.NewServer(mux)
	t.Cleanup(iss.srv.Close)
	return iss
}

func (i *issuer) token(t *testing.T, aud ...string) string {
	t.Helper()
	claims := map[string]any{
		"iss": i.srv.URL, "sub": "user-1", "aud": aud, "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix(),
		"email": "user@example.com", "realm_access": map[string]any{"roles": []string{"operator"}},
	}
	raw, err := jwt.Signed(i.signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A token another service obtained by token exchange, with this gateway's
// own name as its audience, is accepted once that name is among the
// configured audiences (MCPGW_OIDC_AUDIENCES), and refused otherwise.
func TestOIDCAcceptsConfiguredAudiences(t *testing.T) {
	iss := newIssuer(t)
	ctx := context.Background()
	without, err := NewOIDC(ctx, iss.srv.URL, []string{"web-client", "https://gw.example/mcp"}, "operator")
	if err != nil {
		t.Fatal(err)
	}
	with, err := NewOIDC(ctx, iss.srv.URL, []string{"web-client", "https://gw.example/mcp", "bifrost"}, "operator")
	if err != nil {
		t.Fatal(err)
	}
	exchanged := iss.token(t, "bifrost")
	if _, err := without.Verify(ctx, exchanged); err == nil {
		t.Fatal("a token for an audience that is not configured was accepted")
	}
	c, err := with.Verify(ctx, exchanged)
	if err != nil {
		t.Fatal(err)
	}
	if c.UserID != "user-1" || c.Type != ActorOIDC {
		t.Fatalf("caller: %+v", c)
	}
	if _, err := with.Verify(ctx, iss.token(t, "someone-else")); err == nil {
		t.Fatal("a token for another audience was accepted")
	}
}

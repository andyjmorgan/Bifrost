package oauthclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDiscoverAndRegister walks the full discovery chain against a fake
// resource server and authorization server, then checks both registration
// branches and the authorize URL.
func TestDiscoverAndRegister(t *testing.T) {
	var as *httptest.Server
	cimd := true
	as = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": as.URL, "authorization_endpoint": as.URL + "/auth", "token_endpoint": as.URL + "/token",
				"registration_endpoint": as.URL + "/register", "client_id_metadata_document_supported": cimd,
			})
		case "/register":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["scope"] != "mcp:x" {
				t.Errorf("register scope = %v", body["scope"])
			}
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "dcr-123"})
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("grant_type") == "refresh_token" && r.Form.Get("refresh_token") != "r1" {
				w.WriteHeader(400)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "stale"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "a2", "token_type": "Bearer", "expires_in": 300, "scope": "mcp:x"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer as.Close()

	var rs *httptest.Server
	rs = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mcp":
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+rs.URL+`/.well-known/oauth-protected-resource/mcp", scope="mcp:x"`)
			w.WriteHeader(401)
		case "/.well-known/oauth-protected-resource/mcp":
			_ = json.NewEncoder(w).Encode(map[string]any{"resource": rs.URL + "/mcp", "authorization_servers": []string{as.URL}, "scopes_supported": []string{"mcp:x", "mcp:y"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer rs.Close()

	c := New("https://gw.example", rs.Client())
	d, err := c.Discover(context.Background(), rs.URL+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if d.Resource != rs.URL+"/mcp" || d.AuthorizationServer != as.URL || d.TokenEndpoint != as.URL+"/token" {
		t.Fatalf("discovery = %+v", d)
	}
	if strings.Join(d.ChallengeScopes, " ") != "mcp:x" || len(d.ScopesSupported) != 2 || !d.CIMDSupported {
		t.Fatalf("scopes/cimd = %+v", d)
	}
	reg, err := c.Register(context.Background(), d, d.ChallengeScopes)
	if err != nil || reg.Kind != "cimd" || reg.ClientID != "https://gw.example/.well-known/mcp-client-metadata.json" {
		t.Fatalf("cimd registration = %+v, %v", reg, err)
	}
	cimd = false
	d, _ = c.Discover(context.Background(), rs.URL+"/mcp")
	reg, err = c.Register(context.Background(), d, d.ChallengeScopes)
	if err != nil || reg.Kind != "dcr" || reg.ClientID != "dcr-123" {
		t.Fatalf("dcr registration = %+v, %v", reg, err)
	}
	pk, _ := NewPKCE()
	u := c.AuthorizeURL(d, reg, []string{"mcp:x"}, "st", pk)
	for _, want := range []string{"client_id=dcr-123", "code_challenge_method=S256", "scope=mcp%3Ax", "resource=", "redirect_uri=https%3A%2F%2Fgw.example%2Foauth%2Fcallback"} {
		if !strings.Contains(u, want) {
			t.Errorf("authorize url lacks %s: %s", want, u)
		}
	}
	ts, err := c.Refresh(context.Background(), d.TokenEndpoint, reg, "r1", d.Resource, []string{"mcp:x"})
	if err != nil || ts.AccessToken != "a2" || ts.RefreshToken != "r1" || ts.ExpiresAt == nil {
		t.Fatalf("refresh = %+v, %v", ts, err)
	}
	_, err = c.Refresh(context.Background(), d.TokenEndpoint, reg, "bad", d.Resource, nil)
	var te *TokenError
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("expected invalid_grant, got %v", err)
	}
	_ = te
}

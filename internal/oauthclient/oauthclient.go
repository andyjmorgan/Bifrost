// Package oauthclient makes the gateway an OAuth client of downstream MCP
// servers: protected-resource discovery, CIMD or DCR registration, the
// PKCE authorization-code flow, and token refresh.
package oauthclient

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Discovery is what we learn about a server before registering.
type Discovery struct {
	Resource              string   // canonical resource URL (from PRM, else the MCP URL)
	ResourceMetadataURL   string   // where the PRM came from
	AuthorizationServer   string   // issuer
	ScopesSupported       []string // from PRM
	ChallengeScopes       []string // from WWW-Authenticate scope=
	AuthorizationEndpoint string
	TokenEndpoint         string
	RegistrationEndpoint  string
	CIMDSupported         bool
	Raw                   map[string]any
}

// Registration is the client identity to use with the authorization server.
type Registration struct {
	Kind         string // cimd | dcr
	ClientID     string
	ClientSecret string
}

// TokenSet is the result of a code exchange or refresh.
type TokenSet struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	Scope        []string
	ExpiresAt    *time.Time
}

// Client runs the flows. It is safe for concurrent use.
type Client struct {
	HTTP        *http.Client
	PublicBase  string // gateway base URL; hosts the CIMD document and callback
	ClientName  string
	RedirectURI string
}

// Errors.
var (
	ErrNoChallenge = errors.New("oauthclient: server did not issue an OAuth challenge")
	ErrNoAS        = errors.New("oauthclient: no authorization server advertised")
)

// New builds a client for a gateway at publicBase.
func New(publicBase string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{HTTP: httpClient, PublicBase: strings.TrimRight(publicBase, "/"), ClientName: "Bifrost", RedirectURI: strings.TrimRight(publicBase, "/") + "/oauth/callback"}
}

// CIMDURL is where the gateway serves its client metadata document.
func (c *Client) CIMDURL() string { return c.PublicBase + "/.well-known/mcp-client-metadata.json" }

// CIMDDocument is the hosted client metadata (RFC-style CIMD document).
func (c *Client) CIMDDocument() map[string]any {
	return map[string]any{
		"client_id":                  c.CIMDURL(),
		"client_name":                c.ClientName,
		"client_uri":                 c.PublicBase,
		"redirect_uris":              []string{c.RedirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}
}

var (
	paramRe = regexp.MustCompile(`([a-zA-Z_]+)="([^"]*)"`)
)

// Discover probes mcpURL without credentials, follows the WWW-Authenticate
// challenge to the protected resource metadata and then to the
// authorization server metadata.
func (c *Client) Discover(ctx context.Context, mcpURL string) (*Discovery, error) {
	d := &Discovery{Resource: mcpURL}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, mcpURL, strings.NewReader(`{"jsonrpc":"2.0","id":0,"method":"ping"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauthclient: probe: %w", err)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16)) //nolint:errcheck
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		return nil, fmt.Errorf("%w (status %d)", ErrNoChallenge, resp.StatusCode)
	}
	for _, m := range paramRe.FindAllStringSubmatch(resp.Header.Get("WWW-Authenticate"), -1) {
		switch m[1] {
		case "resource_metadata":
			d.ResourceMetadataURL = m[2]
		case "scope":
			d.ChallengeScopes = strings.Fields(m[2])
		}
	}
	// RFC 9728 well-known fallbacks when the challenge carries no pointer.
	candidates := []string{}
	if d.ResourceMetadataURL != "" {
		candidates = append(candidates, d.ResourceMetadataURL)
	}
	if u, err := url.Parse(mcpURL); err == nil {
		candidates = append(candidates,
			u.Scheme+"://"+u.Host+"/.well-known/oauth-protected-resource"+u.Path,
			u.Scheme+"://"+u.Host+"/.well-known/oauth-protected-resource")
	}
	var prm map[string]any
	for _, cand := range candidates {
		if err := c.getJSON(ctx, cand, &prm); err == nil && prm != nil {
			d.ResourceMetadataURL = cand
			break
		}
		prm = nil
	}
	if prm != nil {
		if r, ok := prm["resource"].(string); ok && r != "" {
			d.Resource = r
		}
		if as, ok := prm["authorization_servers"].([]any); ok && len(as) > 0 {
			d.AuthorizationServer, _ = as[0].(string)
		}
		if sc, ok := prm["scopes_supported"].([]any); ok {
			for _, s := range sc {
				if str, ok := s.(string); ok {
					d.ScopesSupported = append(d.ScopesSupported, str)
				}
			}
		}
	}
	if d.AuthorizationServer == "" {
		// Last resort: the MCP server's own origin may be the AS.
		if u, err := url.Parse(mcpURL); err == nil {
			d.AuthorizationServer = u.Scheme + "://" + u.Host
		}
	}
	if err := c.loadASMetadata(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

// loadASMetadata fills endpoints from the AS metadata, trying the OAuth
// well-known path, then the OIDC one (Keycloak serves both).
func (c *Client) loadASMetadata(ctx context.Context, d *Discovery) error {
	iss, err := url.Parse(d.AuthorizationServer)
	if err != nil || iss.Host == "" {
		return ErrNoAS
	}
	path := strings.TrimSuffix(iss.Path, "/")
	candidates := []string{
		iss.Scheme + "://" + iss.Host + "/.well-known/oauth-authorization-server" + path,
		strings.TrimRight(d.AuthorizationServer, "/") + "/.well-known/oauth-authorization-server",
		strings.TrimRight(d.AuthorizationServer, "/") + "/.well-known/openid-configuration",
		iss.Scheme + "://" + iss.Host + "/.well-known/openid-configuration" + path,
	}
	var md map[string]any
	for _, cand := range candidates {
		if err := c.getJSON(ctx, cand, &md); err == nil && md["token_endpoint"] != nil {
			break
		}
		md = nil
	}
	if md == nil {
		return fmt.Errorf("%w: no metadata at %s", ErrNoAS, d.AuthorizationServer)
	}
	d.Raw = md
	d.AuthorizationEndpoint, _ = md["authorization_endpoint"].(string)
	d.TokenEndpoint, _ = md["token_endpoint"].(string)
	d.RegistrationEndpoint, _ = md["registration_endpoint"].(string)
	d.CIMDSupported, _ = md["client_id_metadata_document_supported"].(bool)
	if d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" {
		return fmt.Errorf("%w: metadata lacks endpoints", ErrNoAS)
	}
	return nil
}

// Register picks CIMD when the AS supports it, otherwise performs RFC 7591
// dynamic registration.
func (c *Client) Register(ctx context.Context, d *Discovery, scopes []string) (*Registration, error) {
	if d.CIMDSupported {
		return &Registration{Kind: "cimd", ClientID: c.CIMDURL()}, nil
	}
	if d.RegistrationEndpoint == "" {
		return nil, errors.New("oauthclient: authorization server supports neither CIMD nor dynamic registration")
	}
	body := map[string]any{
		"client_name":                c.ClientName,
		"client_uri":                 c.PublicBase,
		"redirect_uris":              []string{c.RedirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}
	if len(scopes) > 0 {
		body["scope"] = strings.Join(scopes, " ")
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, d.RegistrationEndpoint, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauthclient: register: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("oauthclient: register: status %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var out struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.ClientID == "" {
		return nil, fmt.Errorf("oauthclient: register: bad response")
	}
	return &Registration{Kind: "dcr", ClientID: out.ClientID, ClientSecret: out.ClientSecret}, nil
}

// PKCE holds a verifier/challenge pair.
type PKCE struct {
	Verifier  string
	Challenge string
}

// NewPKCE generates an S256 pair.
func NewPKCE() (PKCE, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return PKCE{}, fmt.Errorf("oauthclient: %w", err)
	}
	v := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(v))
	return PKCE{Verifier: v, Challenge: base64.RawURLEncoding.EncodeToString(sum[:])}, nil
}

// NewState returns a random opaque state value.
func NewState() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("oauthclient: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// AuthorizeURL builds the browser redirect for the consent step.
func (c *Client) AuthorizeURL(d *Discovery, reg *Registration, scopes []string, state string, pkce PKCE) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", reg.ClientID)
	q.Set("redirect_uri", c.RedirectURI)
	q.Set("state", state)
	q.Set("code_challenge", pkce.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("resource", d.Resource)
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}
	sep := "?"
	if strings.Contains(d.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return d.AuthorizationEndpoint + sep + q.Encode()
}

// Exchange redeems an authorization code.
func (c *Client) Exchange(ctx context.Context, tokenEndpoint string, reg *Registration, code, verifier, resource string) (*TokenSet, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", c.RedirectURI)
	form.Set("client_id", reg.ClientID)
	form.Set("code_verifier", verifier)
	if resource != "" {
		form.Set("resource", resource)
	}
	return c.token(ctx, tokenEndpoint, reg, form)
}

// Refresh exchanges a refresh token for a new set. A missing refresh token in
// the response means the old one stays valid.
func (c *Client) Refresh(ctx context.Context, tokenEndpoint string, reg *Registration, refreshToken, resource string, scopes []string) (*TokenSet, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", reg.ClientID)
	if resource != "" {
		form.Set("resource", resource)
	}
	if len(scopes) > 0 {
		form.Set("scope", strings.Join(scopes, " "))
	}
	ts, err := c.token(ctx, tokenEndpoint, reg, form)
	if err != nil {
		return nil, err
	}
	if ts.RefreshToken == "" {
		ts.RefreshToken = refreshToken
	}
	return ts, nil
}

// TokenError is a structured OAuth error from the token endpoint.
type TokenError struct {
	Status      int
	Code        string
	Description string
}

func (e *TokenError) Error() string {
	return fmt.Sprintf("oauthclient: token endpoint %d %s: %s", e.Status, e.Code, e.Description)
}

func (c *Client) token(ctx context.Context, endpoint string, reg *Registration, form url.Values) (*TokenSet, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if reg.ClientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(reg.ClientID), url.QueryEscape(reg.ClientSecret))
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauthclient: token: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
			Desc  string `json:"error_description"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = "http_" + fmt.Sprint(resp.StatusCode)
			e.Desc = truncate(string(raw), 300)
		}
		return nil, &TokenError{Status: resp.StatusCode, Code: e.Error, Description: e.Desc}
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.AccessToken == "" {
		return nil, errors.New("oauthclient: token: response lacks access_token")
	}
	ts := &TokenSet{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken, TokenType: out.TokenType, Scope: strings.Fields(out.Scope)}
	if out.ExpiresIn > 0 {
		t := time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
		ts.ExpiresAt = &t
	}
	return ts, nil
}

func (c *Client) getJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

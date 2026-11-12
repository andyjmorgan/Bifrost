package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// ErrForbidden means the token verified but the caller lacks the operator role.
var ErrForbidden = errors.New("auth: caller lacks required role")

// OIDC verifies Keycloak access tokens for the front door and the API.
type OIDC struct {
	verifier     *oidc.IDTokenVerifier
	audiences    []string // any of these satisfies the audience check
	requiredRole string
}

// NewOIDC builds a verifier against issuer's JWKS. audiences lists the
// accepted aud values (SPA client id, MCP resource URLs).
func NewOIDC(ctx context.Context, issuer string, audiences []string, requiredRole string) (*OIDC, error) {
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("auth: oidc discovery: %w", err)
	}
	return &OIDC{
		verifier:     provider.Verifier(&oidc.Config{SkipClientIDCheck: true}),
		audiences:    audiences,
		requiredRole: requiredRole,
	}, nil
}

type keycloakClaims struct {
	Email             string `json:"email"`
	Name              string `json:"name"`
	PreferredUsername string `json:"preferred_username"`
	Scope             string `json:"scope"`
	AZP               string `json:"azp"`
	RealmAccess       struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
	ResourceAccess map[string]struct {
		Roles []string `json:"roles"`
	} `json:"resource_access"`
}

// Verify checks signature, issuer, expiry, audience (extraAudiences are
// accepted in addition to the configured ones, e.g. the exact resource URL
// of the request) and the role gate.
func (o *OIDC) Verify(ctx context.Context, raw string, extraAudiences ...string) (*Caller, error) {
	tok, err := o.verifier.Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	ok := false
	for _, a := range tok.Audience {
		if slices.Contains(o.audiences, a) || slices.Contains(extraAudiences, a) {
			ok = true
			break
		}
	}
	if !ok {
		return nil, fmt.Errorf("auth: audience %v not accepted", tok.Audience)
	}
	var c keycloakClaims
	if err := tok.Claims(&c); err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	if o.requiredRole != "" && !hasRole(c, o.requiredRole) {
		return nil, ErrForbidden
	}
	subject := c.Email
	if subject == "" {
		subject = c.PreferredUsername
	}
	return &Caller{
		UserID: tok.Subject, Subject: subject, Type: ActorOIDC,
		Scopes: strings.Fields(c.Scope), Email: c.Email, Name: c.Name,
	}, nil
}

func hasRole(c keycloakClaims, role string) bool {
	if slices.Contains(c.RealmAccess.Roles, role) {
		return true
	}
	for _, ra := range c.ResourceAccess {
		if slices.Contains(ra.Roles, role) {
			return true
		}
	}
	return false
}

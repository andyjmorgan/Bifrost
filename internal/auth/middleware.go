package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Authenticator turns bearer credentials into a Caller on the context.
type Authenticator struct {
	OIDC       *OIDC // nil in dev mode
	Keys       *Keys
	DevOwner   string
	PublicBase string
	MCPScope   string // advertised in MCP challenges
	Issuer     string
	// OwnerID, when set, is the tenant every OIDC caller is mapped to. The
	// gateway is single-tenant: whoever passes the role gate manages the
	// same servers regardless of which Keycloak identity they signed in with.
	OwnerID string
}

// Resource is the canonical resource URL for a request path.
func (a *Authenticator) Resource(path string) string { return a.PublicBase + path }

// Authenticate resolves the Authorization header. resource is accepted as an
// extra audience for OIDC tokens (MCP clients get tokens for the exact URL).
func (a *Authenticator) Authenticate(ctx context.Context, r *http.Request, resource string) (*Caller, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		if a.OIDC == nil {
			return &Caller{OwnerID: a.DevOwner, Subject: a.DevOwner, Type: ActorDev}, nil
		}
		return nil, errors.New("auth: missing bearer token")
	}
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok {
		return nil, errors.New("auth: unsupported authorization scheme")
	}
	tok = strings.TrimSpace(tok)
	if strings.HasPrefix(tok, KeyPrefix) {
		return a.Keys.Verify(ctx, tok)
	}
	if a.OIDC == nil {
		return &Caller{OwnerID: a.DevOwner, Subject: a.DevOwner, Type: ActorDev}, nil
	}
	c, err := a.OIDC.Verify(ctx, tok, resource)
	if err != nil {
		return nil, err
	}
	if a.OwnerID != "" {
		c.OwnerID = a.OwnerID
	}
	return c, nil
}

// RequireAPI guards REST routes: a valid caller holding scope (OIDC holds all).
func (a *Authenticator) RequireAPI(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := a.Authenticate(r.Context(), r, "")
			if err != nil {
				status := http.StatusUnauthorized
				if errors.Is(err, ErrForbidden) {
					status = http.StatusForbidden
				}
				http.Error(w, `{"error":"`+http.StatusText(status)+`"}`, status)
				return
			}
			if scope != "" && !c.Has(scope) {
				http.Error(w, `{"error":"insufficient scope"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithCaller(r.Context(), c)))
		})
	}
}

// RequireMCP guards /mcp routes with an RFC 9728 challenge on failure.
func (a *Authenticator) RequireMCP() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resource := a.Resource(r.URL.Path)
			c, err := a.Authenticate(r.Context(), r, resource)
			if err == nil && !c.Has(ScopeMCPExecute) {
				err = fmt.Errorf("%w: key lacks %s", ErrForbidden, ScopeMCPExecute)
			}
			if err != nil {
				prm := a.PublicBase + "/.well-known/oauth-protected-resource" + r.URL.Path
				challenge := fmt.Sprintf(`Bearer resource_metadata=%q`, prm)
				if a.MCPScope != "" {
					challenge += fmt.Sprintf(`, scope=%q`, a.MCPScope)
				}
				status := http.StatusUnauthorized
				if errors.Is(err, ErrForbidden) {
					status = http.StatusForbidden
					challenge += `, error="insufficient_scope"`
				} else if r.Header.Get("Authorization") != "" {
					challenge += `, error="invalid_token"`
				}
				w.Header().Set("WWW-Authenticate", challenge)
				http.Error(w, http.StatusText(status), status)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithCaller(r.Context(), c)))
		})
	}
}

// ProtectedResourceMetadata serves the PRM for any MCP path under the gateway.
func (a *Authenticator) ProtectedResourceMetadata() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		suffix := strings.TrimPrefix(r.URL.Path, "/.well-known/oauth-protected-resource")
		if suffix == "" {
			suffix = "/mcp"
		}
		var scopes string
		if a.MCPScope != "" {
			scopes = fmt.Sprintf(`"scopes_supported":[%q],`, a.MCPScope)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"resource":%q,"authorization_servers":[%q],%s"bearer_methods_supported":["header"],"resource_name":"mcp-gateway"}`,
			a.PublicBase+suffix, a.Issuer, scopes)
	})
}

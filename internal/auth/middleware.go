package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/andyjmorgan/Bifrost/internal/store"
)

// UserStore records sign-ins and resolves the users keys act as.
type UserStore interface {
	EnsureUser(ctx context.Context, id, email, name string) (*store.User, error)
	GetUser(ctx context.Context, id string) (*store.User, error)
}

// Authenticator turns bearer credentials into a Caller on the context.
// Everyone the identity provider lets in may run the gateway; what a person
// reaches through it is decided by the grants on each server.
type Authenticator struct {
	OIDC       *OIDC // nil in dev mode
	Keys       *Keys
	Users      UserStore
	DevOwner   string
	PublicBase string
	MCPScope   string // advertised in MCP challenges
	Issuer     string

	seen sync.Map // user id -> time the store last confirmed them
}

// seenTTL bounds how often a user's sign-in is written to the store, or a
// key's creator looked up, while requests keep coming.
const seenTTL = 15 * time.Second

func (a *Authenticator) fresh(id string) bool {
	if v, ok := a.seen.Load(id); ok && time.Since(v.(time.Time)) < seenTTL {
		return true
	}
	return false
}

// record notes a human's sign-in.
func (a *Authenticator) record(ctx context.Context, c *Caller) (*Caller, error) {
	if a.fresh(c.UserID) {
		return c, nil
	}
	if _, err := a.Users.EnsureUser(ctx, c.UserID, c.Email, c.Name); err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	a.seen.Store(c.UserID, time.Now())
	return c, nil
}

// dev is the dev-mode caller: the dev owner, or the local user an
// X-Dev-User header names, so two people can be tried without an identity
// provider.
func (a *Authenticator) dev(ctx context.Context, r *http.Request) (*Caller, error) {
	id := a.DevOwner
	if u := strings.TrimSpace(r.Header.Get("X-Dev-User")); u != "" {
		id = u
	}
	return a.record(ctx, &Caller{UserID: id, Subject: id, Type: ActorDev, Email: id + "@localhost", Name: strings.ToUpper(id[:1]) + id[1:]})
}

// bearerToken returns the token from an Authorization header value. The scheme
// name is case-insensitive (RFC 9110 §11.1), so "bearer" and "BEARER" count.
func bearerToken(h string) (string, bool) {
	scheme, tok, ok := strings.Cut(strings.TrimSpace(h), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	return strings.TrimSpace(tok), true
}

// Resource is the canonical resource URL for a request path.
func (a *Authenticator) Resource(path string) string { return a.PublicBase + path }

// Authenticate resolves the Authorization header. resource is accepted as an
// extra audience for OIDC tokens (MCP clients get tokens for the exact URL).
func (a *Authenticator) Authenticate(ctx context.Context, r *http.Request, resource string) (*Caller, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		if a.OIDC == nil {
			return a.dev(ctx, r)
		}
		return nil, errors.New("auth: missing bearer token")
	}
	tok, ok := bearerToken(h)
	if !ok {
		return nil, errors.New("auth: unsupported authorization scheme")
	}
	if strings.HasPrefix(tok, KeyPrefix) {
		c, err := a.Keys.Verify(ctx, tok)
		if err != nil {
			return nil, err
		}
		return a.keyCaller(ctx, c)
	}
	if a.OIDC == nil {
		return a.dev(ctx, r)
	}
	c, err := a.OIDC.Verify(ctx, tok, resource)
	if err != nil {
		return nil, err
	}
	return a.record(ctx, c)
}

// keyCaller checks that the user a key acts as still exists. A key acts as
// its creator and holds what they hold.
func (a *Authenticator) keyCaller(ctx context.Context, c *Caller) (*Caller, error) {
	if c.UserID == "" {
		return nil, ErrBadKey
	}
	if a.fresh(c.UserID) {
		return c, nil
	}
	_, err := a.Users.GetUser(ctx, c.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrBadKey
	}
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	a.seen.Store(c.UserID, time.Now())
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
		fmt.Fprintf(w, `{"resource":%q,"authorization_servers":[%q],%s"bearer_methods_supported":["header"],"resource_name":"Bifrost"}`,
			a.PublicBase+suffix, a.Issuer, scopes)
	})
}

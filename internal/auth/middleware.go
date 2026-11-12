package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/store"
)

// UserStore resolves sign-ins to an org and role.
type UserStore interface {
	EnsureUser(ctx context.Context, id, email, name, claimLegacy string) (*store.Standing, error)
	MemberRole(ctx context.Context, orgID uuid.UUID, userID string) (store.Role, error)
	GetOrg(ctx context.Context, orgID uuid.UUID) (*store.Org, error)
}

// Authenticator turns bearer credentials into a Caller on the context.
type Authenticator struct {
	OIDC       *OIDC // nil in dev mode
	Keys       *Keys
	Users      UserStore
	DevOwner   string
	PublicBase string
	MCPScope   string // advertised in MCP challenges
	Issuer     string
	// LegacyOwner is the pre-org owner id whose records were migrated into
	// the first org; AdminEmail names the human who claims that org at their
	// first sign-in. In dev mode the dev owner claims its own legacy org.
	LegacyOwner string
	AdminEmail  string

	standings sync.Map // user id -> cachedStanding
}

type cachedStanding struct {
	st  *store.Standing
	exp time.Time
}

// standingTTL bounds how long a sign-in's org and role are reused before
// the store is asked again. Join, leave and role changes call Forget.
const standingTTL = 15 * time.Second

// Forget drops a user's cached standing so the next request re-resolves it.
func (a *Authenticator) Forget(userID string) { a.standings.Delete(userID) }

// standing resolves (and briefly caches) a human's active org and role.
func (a *Authenticator) standing(ctx context.Context, id, email, name, claim string) (*store.Standing, error) {
	if v, ok := a.standings.Load(id); ok {
		if c := v.(cachedStanding); time.Now().Before(c.exp) {
			return c.st, nil
		}
	}
	st, err := a.Users.EnsureUser(ctx, id, email, name, claim)
	if err != nil {
		return nil, err
	}
	a.standings.Store(id, cachedStanding{st: st, exp: time.Now().Add(standingTTL)})
	return st, nil
}

// place fills a human caller's org fields from their standing.
func (a *Authenticator) place(ctx context.Context, c *Caller, claim string) (*Caller, error) {
	st, err := a.standing(ctx, c.UserID, c.Email, c.Name, claim)
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	c.OrgID = st.Org.ID.String()
	c.OrgName = st.Org.Name
	c.PersonalOrg = st.Org.PersonalFor == c.UserID
	c.Role = Role(st.Role)
	return c, nil
}

// dev is the dev-mode caller: the dev owner, admin of its migrated org. An
// X-Dev-User header names another local user, so the org flows can be
// exercised with two people without an identity provider.
func (a *Authenticator) dev(ctx context.Context, r *http.Request) (*Caller, error) {
	id := a.DevOwner
	if u := strings.TrimSpace(r.Header.Get("X-Dev-User")); u != "" {
		id = u
	}
	claim := ""
	if id == a.DevOwner {
		claim = a.DevOwner
	}
	return a.place(ctx, &Caller{UserID: id, Subject: id, Type: ActorDev, Email: id + "@localhost", Name: strings.ToUpper(id[:1]) + id[1:]}, claim)
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
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok {
		return nil, errors.New("auth: unsupported authorization scheme")
	}
	tok = strings.TrimSpace(tok)
	if strings.HasPrefix(tok, KeyPrefix) {
		c, err := a.Keys.Verify(ctx, tok)
		if err != nil {
			return nil, err
		}
		return a.placeKey(ctx, c)
	}
	if a.OIDC == nil {
		return a.dev(ctx, r)
	}
	c, err := a.OIDC.Verify(ctx, tok, resource)
	if err != nil {
		return nil, err
	}
	claim := ""
	if a.AdminEmail != "" && strings.EqualFold(c.Email, a.AdminEmail) {
		claim = a.LegacyOwner
	}
	return a.place(ctx, c, claim)
}

// placeKey gives a key caller the role of the user it acts as. A key whose
// creator has left the org is dead. A legacy key with no creator (the org
// not yet claimed) acts as an admin.
func (a *Authenticator) placeKey(ctx context.Context, c *Caller) (*Caller, error) {
	orgID, err := uuid.Parse(c.OrgID)
	if err != nil {
		return nil, ErrBadKey
	}
	if o, err := a.Users.GetOrg(ctx, orgID); err == nil {
		c.OrgName = o.Name
		c.PersonalOrg = c.UserID != "" && o.PersonalFor == c.UserID
	}
	if c.UserID == "" {
		c.Role = RoleAdmin
		return c, nil
	}
	role, err := a.Users.MemberRole(ctx, orgID, c.UserID)
	if errors.Is(err, store.ErrNotMember) {
		return nil, ErrBadKey
	}
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	c.Role = Role(role)
	return c, nil
}

// RequireAdmin guards routes that change the org: only admins pass. It runs
// after RequireAPI.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !CallerFrom(r.Context()).IsAdmin() {
			http.Error(w, `{"error":"admin role required"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
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

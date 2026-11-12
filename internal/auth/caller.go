// Package auth identifies front-door callers: Keycloak OIDC tokens with a
// role gate, or gateway API keys with scopes.
package auth

import (
	"context"
	"slices"
	"sync"

	"github.com/google/uuid"
)

// ActorType distinguishes how a caller authenticated.
type ActorType string

// Actor types.
const (
	ActorOIDC   ActorType = "oidc"
	ActorAPIKey ActorType = "apikey"
	ActorDev    ActorType = "dev"
	ActorSystem ActorType = "system"
)

// Scopes an API key may carry. OIDC callers implicitly hold all of them.
const (
	ScopeAPIRead    = "api:read"
	ScopeAPIEdit    = "api:edit"
	ScopeMCPExecute = "mcp:execute"
	ScopeMCPEdit    = "mcp:edit"
)

// AllScopes lists every valid key scope.
var AllScopes = []string{ScopeAPIRead, ScopeAPIEdit, ScopeMCPExecute, ScopeMCPEdit}

// Caller is the authenticated principal for a request.
type Caller struct {
	OrgID        string // the active org; the tenant key for every record
	OrgName      string
	PersonalOrg  bool   // the active org is the caller's own personal org
	UserID       string // Keycloak subject the caller acts as (a key acts as its creator)
	Role         Role   // standing in the active org
	Subject      string // human-readable: email or key name
	Type         ActorType
	Scopes       []string
	APIKeyID     *uuid.UUID
	APIKeyPrefix string
	Email        string
	Name         string
}

// Role is a member's standing in an org.
type Role string

// Roles.
const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

// IsAdmin reports whether the caller administers the active org. System
// callers (background workers) are unrestricted.
func (c *Caller) IsAdmin() bool {
	return c != nil && (c.Role == RoleAdmin || c.Type == ActorSystem)
}

// Has reports whether the caller holds scope. OIDC and dev callers hold all.
func (c *Caller) Has(scope string) bool {
	if c == nil {
		return false
	}
	if c.Type == ActorOIDC || c.Type == ActorDev || c.Type == ActorSystem {
		return true
	}
	return slices.Contains(c.Scopes, scope)
}

type callerKey struct{}

type callerSlotKey struct{}

// callerSlot lets a middleware that runs before authentication (the audit
// recorder) see the caller a later middleware attaches.
type callerSlot struct {
	mu sync.Mutex
	c  *Caller
}

// WithCallerSlot prepares ctx so WithCaller further down the chain is
// visible to holders of this ctx through CallerFrom.
func WithCallerSlot(ctx context.Context) context.Context {
	return context.WithValue(ctx, callerSlotKey{}, &callerSlot{})
}

// WithCaller stores the caller on the context (and in the slot, if any).
func WithCaller(ctx context.Context, c *Caller) context.Context {
	if s, ok := ctx.Value(callerSlotKey{}).(*callerSlot); ok {
		s.mu.Lock()
		s.c = c
		s.mu.Unlock()
	}
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom returns the caller, or nil when unauthenticated.
func CallerFrom(ctx context.Context) *Caller {
	if c, ok := ctx.Value(callerKey{}).(*Caller); ok && c != nil {
		return c
	}
	if s, ok := ctx.Value(callerSlotKey{}).(*callerSlot); ok {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.c
	}
	return nil
}

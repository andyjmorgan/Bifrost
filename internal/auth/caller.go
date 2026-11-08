// Package auth identifies front-door callers: Keycloak OIDC tokens with a
// role gate, or gateway API keys with scopes.
package auth

import (
	"context"
	"slices"

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
	OwnerID      string // Keycloak subject; the tenant key for every record
	Subject      string // human-readable: email or key name
	Type         ActorType
	Scopes       []string
	APIKeyID     *uuid.UUID
	APIKeyPrefix string
	Email        string
	Name         string
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

// WithCaller stores the caller on the context.
func WithCaller(ctx context.Context, c *Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom returns the caller, or nil when unauthenticated.
func CallerFrom(ctx context.Context) *Caller {
	c, _ := ctx.Value(callerKey{}).(*Caller)
	return c
}

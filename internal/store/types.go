// Package store is the persistence layer: typed records and the Postgres
// implementation behind the Store interface.
package store

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Sentinel errors.
var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
)

// AuthKind is how the gateway authenticates to a downstream server.
type AuthKind string

// Auth kinds.
const (
	AuthNone   AuthKind = "none"
	AuthOAuth  AuthKind = "oauth"
	AuthBasic  AuthKind = "basic"
	AuthHeader AuthKind = "header"
)

// CredentialState tracks whether a credential is usable.
type CredentialState string

// Credential states.
const (
	StateOK      CredentialState = "ok"
	StatePending CredentialState = "pending" // OAuth consent not completed yet
	StateBroken  CredentialState = "broken"  // refresh failed; needs re-auth
)

// Server is a registered downstream MCP server.
type Server struct {
	ID          uuid.UUID
	OwnerID     string
	Slug        string
	Name        string
	URL         string
	Description string
	AuthKind    AuthKind
	Enabled     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Credential is the non-secret view of a server's credential plus the
// sealed secret envelope.
type Credential struct {
	ServerID    uuid.UUID
	Kind        AuthKind
	State       CredentialState
	StateReason string

	BasicUsername string
	HeaderName    string

	OAuthIssuer                string
	OAuthAuthorizationEndpoint string
	OAuthTokenEndpoint         string
	OAuthRegistrationEndpoint  string
	OAuthRegistrationKind      string // cimd | dcr
	OAuthClientID              string
	OAuthResource              string
	OAuthScopes                []string
	OAuthTokenType             string
	OAuthExpiresAt             *time.Time
	OAuthHasRefreshToken       bool

	SecretBlob      []byte
	LastRefreshedAt *time.Time
	LastError       string
	UpdatedAt       time.Time
}

// Secret is the decrypted content of Credential.SecretBlob.
type Secret struct {
	Password     string `json:"password,omitempty"`
	HeaderValue  string `json:"headerValue,omitempty"`
	AccessToken  string `json:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
}

// OAuthState is a pending authorization-code round trip.
type OAuthState struct {
	State        string
	OwnerID      string
	ServerID     uuid.UUID
	CodeVerifier string
	ReturnTo     string
	ExpiresAt    time.Time
}

// APIKey is a gateway access key. The secret is kept hashed for lookup and
// sealed so the owner can drive end-to-end tests through the front door.
type APIKey struct {
	ID         uuid.UUID
	OwnerID    string
	Name       string
	Prefix     string
	SecretHash []byte
	SecretBlob []byte
	Scopes     []string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// Hop says which side of the gateway an audit event describes.
type Hop string

// Hops.
const (
	HopInbound  Hop = "inbound"
	HopUpstream Hop = "upstream"
)

// Outcome classifies an audited exchange.
type Outcome string

// Outcomes.
const (
	OutcomeSuccess Outcome = "success"
	OutcomeError   Outcome = "error"
	OutcomeDenied  Outcome = "denied"
	// OutcomePartial: the exchange succeeded but part of the fan-out did not (mux tools/list).
	OutcomePartial Outcome = "partial"
)

// AuditEvent is one HTTP exchange, inbound or upstream.
type AuditEvent struct {
	ID              uuid.UUID
	OwnerID         string
	CreatedAt       time.Time
	Hop             Hop
	ParentID        *uuid.UUID
	ActorType       string
	ActorSubject    string
	APIKeyID        *uuid.UUID
	APIKeyPrefix    string
	ServerID        *uuid.UUID
	Slug            string
	Route           string
	HTTPMethod      string
	Path            string
	McpMethod       string
	ToolName        string
	JSONRPCID       string
	ProtocolVersion string
	StatusCode      int
	Outcome         Outcome
	RequestBytes    int64
	ResponseBytes   int64
	DurationMS      int64
	RequestID       string
	ErrorType       string
	HasPayload      bool
}

// AuditPayload holds the redacted bodies and headers for an event.
type AuditPayload struct {
	EventID         uuid.UUID
	RequestHeaders  map[string]string
	RequestBody     string
	ResponseHeaders map[string]string
	ResponseBody    string
}

// AuditQuery filters a listing.
type AuditQuery struct {
	OwnerID   string
	Offset    int
	Limit     int
	ServerID  *uuid.UUID
	Slug      string
	Hop       Hop
	McpMethod string
	ToolName  string
	Outcome   Outcome
	RequestID string
}

// Page is a bounded listing.
type Page[T any] struct {
	Items  []T
	Total  int
	Offset int
	Limit  int
}

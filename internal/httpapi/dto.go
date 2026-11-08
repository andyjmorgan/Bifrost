package httpapi

import (
	"time"

	"github.com/andyjmorgan/mcp-gateway/internal/store"
)

// ServerDTO is a server with its credential summary (never the secret).
type ServerDTO struct {
	ID          string         `json:"id"`
	Slug        string         `json:"slug"`
	Name        string         `json:"name"`
	URL         string         `json:"url"`
	Description string         `json:"description"`
	AuthKind    store.AuthKind `json:"authKind"`
	Enabled     bool           `json:"enabled"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
	MCPURL      string         `json:"mcpUrl"`
	Credential  *CredentialDTO `json:"credential,omitempty"`
}

// CredentialDTO is the non-secret view of a credential.
type CredentialDTO struct {
	Kind                  store.AuthKind        `json:"kind"`
	State                 store.CredentialState `json:"state"`
	StateReason           string                `json:"stateReason,omitempty"`
	BasicUsername         string                `json:"basicUsername,omitempty"`
	HeaderName            string                `json:"headerName,omitempty"`
	OAuthIssuer           string                `json:"oauthIssuer,omitempty"`
	OAuthClientID         string                `json:"oauthClientId,omitempty"`
	OAuthRegistrationKind string                `json:"oauthRegistrationKind,omitempty"`
	OAuthResource         string                `json:"oauthResource,omitempty"`
	OAuthScopes           []string              `json:"oauthScopes"`
	OAuthExpiresAt        *time.Time            `json:"oauthExpiresAt,omitempty"`
	OAuthHasRefreshToken  bool                  `json:"oauthHasRefreshToken"`
	LastRefreshedAt       *time.Time            `json:"lastRefreshedAt,omitempty"`
	LastError             string                `json:"lastError,omitempty"`
	UpdatedAt             time.Time             `json:"updatedAt"`
}

// AuthInput is the credential part of a create/update request.
type AuthInput struct {
	Kind        store.AuthKind `json:"kind"`
	Username    string         `json:"username,omitempty"`
	Password    string         `json:"password,omitempty"`
	HeaderName  string         `json:"headerName,omitempty"`
	HeaderValue string         `json:"headerValue,omitempty"`
	Scopes      []string       `json:"scopes,omitempty"` // oauth: override discovered scopes
}

// CreateServerInput is the body of POST /servers.
type CreateServerInput struct {
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	Description string    `json:"description"`
	Auth        AuthInput `json:"auth"`
}

// UpdateServerInput is the body of PATCH /servers/{id}.
type UpdateServerInput struct {
	Name        *string `json:"name,omitempty"`
	URL         *string `json:"url,omitempty"`
	Description *string `json:"description,omitempty"`
	Enabled     *bool   `json:"enabled,omitempty"`
}

// CreateServerResult adds the consent URL when OAuth needs the browser.
type CreateServerResult struct {
	Server       ServerDTO `json:"server"`
	AuthorizeURL string    `json:"authorizeUrl,omitempty"`
}

// PingResult is the outcome of a connection test.
type PingResult struct {
	OK              bool   `json:"ok"`
	LatencyMS       int64  `json:"latencyMs"`
	ProtocolVersion string `json:"protocolVersion,omitempty"`
	ServerName      string `json:"serverName,omitempty"`
	ServerVersion   string `json:"serverVersion,omitempty"`
	ToolCount       int    `json:"toolCount"`
	Error           string `json:"error,omitempty"`
}

// APIKeyDTO is a key without its secret.
type APIKeyDTO struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
}

// CreateAPIKeyInput is the body of POST /api-keys.
type CreateAPIKeyInput struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}

// CreateAPIKeyResult carries the plaintext exactly once.
type CreateAPIKeyResult struct {
	Key    APIKeyDTO `json:"key"`
	Secret string    `json:"secret"`
}

// AuditEventDTO mirrors store.AuditEvent for the wire.
type AuditEventDTO struct {
	ID              string        `json:"id"`
	CreatedAt       time.Time     `json:"createdAt"`
	Hop             store.Hop     `json:"hop"`
	ParentID        *string       `json:"parentId,omitempty"`
	ActorType       string        `json:"actorType"`
	ActorSubject    string        `json:"actorSubject"`
	APIKeyID        *string       `json:"apiKeyId,omitempty"`
	APIKeyPrefix    string        `json:"apiKeyPrefix,omitempty"`
	ServerID        *string       `json:"serverId,omitempty"`
	Slug            string        `json:"slug,omitempty"`
	Route           string        `json:"route"`
	HTTPMethod      string        `json:"httpMethod"`
	Path            string        `json:"path"`
	McpMethod       string        `json:"mcpMethod,omitempty"`
	ToolName        string        `json:"toolName,omitempty"`
	JSONRPCID       string        `json:"jsonrpcId,omitempty"`
	ProtocolVersion string        `json:"protocolVersion,omitempty"`
	StatusCode      int           `json:"statusCode"`
	Outcome         store.Outcome `json:"outcome"`
	RequestBytes    int64         `json:"requestBytes"`
	ResponseBytes   int64         `json:"responseBytes"`
	DurationMS      int64         `json:"durationMs"`
	RequestID       string        `json:"requestId"`
	ErrorType       string        `json:"errorType,omitempty"`
	HasPayload      bool          `json:"hasPayload"`
}

// AuditPayloadDTO is the stored exchange.
type AuditPayloadDTO struct {
	RequestHeaders  map[string]string `json:"requestHeaders"`
	RequestBody     string            `json:"requestBody"`
	ResponseHeaders map[string]string `json:"responseHeaders"`
	ResponseBody    string            `json:"responseBody"`
}

// AuditDetailDTO is an event with payload.
type AuditDetailDTO struct {
	Event   AuditEventDTO    `json:"event"`
	Payload *AuditPayloadDTO `json:"payload,omitempty"`
}

// PageDTO is a bounded listing.
type PageDTO[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}

// E2EInput drives a tool listing or call through the front door as a key.
type E2EInput struct {
	APIKeyID  string         `json:"apiKeyId"`
	Name      string         `json:"name,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// E2EResult is the raw outcome plus timing.
type E2EResult struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latencyMs"`
	Endpoint  string `json:"endpoint"`
	Result    any    `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
	RequestID string `json:"requestId,omitempty"`
}

// MeDTO describes the caller.
type MeDTO struct {
	OwnerID string   `json:"ownerId"`
	Subject string   `json:"subject"`
	Type    string   `json:"type"`
	Email   string   `json:"email,omitempty"`
	Name    string   `json:"name,omitempty"`
	Scopes  []string `json:"scopes"`
}

// ConfigDTO is what the SPA needs before login.
type ConfigDTO struct {
	DevMode       bool     `json:"devMode"`
	OIDCIssuer    string   `json:"oidcIssuer,omitempty"`
	OIDCClientID  string   `json:"oidcClientId,omitempty"`
	PublicBaseURL string   `json:"publicBaseUrl"`
	Version       string   `json:"version"`
	MCPScope      string   `json:"mcpScope"`
	AllScopes     []string `json:"apiKeyScopes"`
}

func serverDTO(base string, s *store.Server, c *store.Credential) ServerDTO {
	d := ServerDTO{
		ID: s.ID.String(), Slug: s.Slug, Name: s.Name, URL: s.URL, Description: s.Description,
		AuthKind: s.AuthKind, Enabled: s.Enabled, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
		MCPURL: base + "/" + s.Slug + "/mcp",
	}
	if c != nil {
		d.Credential = credentialDTO(c)
	}
	return d
}

func credentialDTO(c *store.Credential) *CredentialDTO {
	scopes := c.OAuthScopes
	if scopes == nil {
		scopes = []string{}
	}
	return &CredentialDTO{
		Kind: c.Kind, State: c.State, StateReason: c.StateReason, BasicUsername: c.BasicUsername, HeaderName: c.HeaderName,
		OAuthIssuer: c.OAuthIssuer, OAuthClientID: c.OAuthClientID, OAuthRegistrationKind: c.OAuthRegistrationKind,
		OAuthResource: c.OAuthResource, OAuthScopes: scopes, OAuthExpiresAt: c.OAuthExpiresAt, OAuthHasRefreshToken: c.OAuthHasRefreshToken,
		LastRefreshedAt: c.LastRefreshedAt, LastError: c.LastError, UpdatedAt: c.UpdatedAt,
	}
}

func keyDTO(k *store.APIKey) APIKeyDTO {
	return APIKeyDTO{ID: k.ID.String(), Name: k.Name, Prefix: k.Prefix, Scopes: k.Scopes, CreatedAt: k.CreatedAt, LastUsedAt: k.LastUsedAt, RevokedAt: k.RevokedAt}
}

func auditDTO(e *store.AuditEvent) AuditEventDTO {
	d := AuditEventDTO{
		ID: e.ID.String(), CreatedAt: e.CreatedAt, Hop: e.Hop, ActorType: e.ActorType, ActorSubject: e.ActorSubject,
		APIKeyPrefix: e.APIKeyPrefix, Slug: e.Slug, Route: e.Route, HTTPMethod: e.HTTPMethod, Path: e.Path,
		McpMethod: e.McpMethod, ToolName: e.ToolName, JSONRPCID: e.JSONRPCID, ProtocolVersion: e.ProtocolVersion,
		StatusCode: e.StatusCode, Outcome: e.Outcome, RequestBytes: e.RequestBytes, ResponseBytes: e.ResponseBytes,
		DurationMS: e.DurationMS, RequestID: e.RequestID, ErrorType: e.ErrorType, HasPayload: e.HasPayload,
	}
	if e.ParentID != nil {
		s := e.ParentID.String()
		d.ParentID = &s
	}
	if e.APIKeyID != nil {
		s := e.APIKeyID.String()
		d.APIKeyID = &s
	}
	if e.ServerID != nil {
		s := e.ServerID.String()
		d.ServerID = &s
	}
	return d
}

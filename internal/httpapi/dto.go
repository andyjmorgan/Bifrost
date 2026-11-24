package httpapi

import (
	"encoding/json"
	"time"

	"github.com/andyjmorgan/Bifrost/internal/keyringref"
	"github.com/andyjmorgan/Bifrost/internal/ruletext"
	"github.com/andyjmorgan/Bifrost/internal/store"
)

// ServerDTO is a server with its rules, grants and credential summary
// (never the secret).
type ServerDTO struct {
	ID          string          `json:"id"`
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	URL         string          `json:"url"`
	Description string          `json:"description"`
	AuthKind    store.AuthKind  `json:"authKind"`
	Enabled     bool            `json:"enabled"`
	Seen        int             `json:"seen"` // capabilities the gateway has seen on it
	MCPURL      string          `json:"mcpUrl"`
	Selection   store.Selection `json:"selection"`
	// Hooks is the rules as written: the text of each hook's
	// rules, under the hook's property (see ruletext.Hooks). Every property
	// is present; null means the hook has no rules.
	Hooks ruletext.Hooks `json:"hooks"`
	// RulesError says why the stored rules cannot be used. While it is set
	// the server exposes nothing.
	RulesError string         `json:"rulesError,omitempty"`
	Generation int            `json:"generation"`
	Grants     []GrantDTO     `json:"grants"`
	Credential *CredentialDTO `json:"credential,omitempty"`
	CreatedAt  time.Time      `json:"createdAt"`
	UpdatedAt  time.Time      `json:"updatedAt"`
}

// CredentialDTO is the non-secret view of a credential.
type CredentialDTO struct {
	// Kind is the credential as the operator set it. A Basic or header
	// credential the gateway keeps in the keyring is still basic or header
	// here; Store says where it lives.
	Kind store.AuthKind `json:"kind"`
	// Store is where the secret is held: "keyring" for every credential that
	// works. "gateway" is a credential still sealed in the gateway's old
	// store, which `gateway migrate-credentials` has not moved: the gateway
	// cannot send it, so it is shown as broken, with the reason. Absent for
	// none.
	Store       string                `json:"store,omitempty"`
	State       store.CredentialState `json:"state"`
	StateReason string                `json:"stateReason,omitempty"`
	// BasicUsername, HeaderName, the OAuth fields and LastRefreshedAt are
	// what the keyring says of a credential the gateway holds there for the
	// server. They are empty for a reference into a person's keyring and for
	// a credential that has not been moved.
	BasicUsername         string     `json:"basicUsername,omitempty"`
	HeaderName            string     `json:"headerName,omitempty"`
	OAuthIssuer           string     `json:"oauthIssuer,omitempty"`
	OAuthClientID         string     `json:"oauthClientId,omitempty"`
	OAuthRegistrationKind string     `json:"oauthRegistrationKind,omitempty"`
	OAuthResource         string     `json:"oauthResource,omitempty"`
	OAuthScopes           []string   `json:"oauthScopes"`
	OAuthExpiresAt        *time.Time `json:"oauthExpiresAt,omitempty"`
	OAuthHasRefreshToken  bool       `json:"oauthHasRefreshToken"`
	// The keyring reference of a keyring_oauth or keyring_secret credential:
	// whose keyring, and the name of the entry. Names only; the gateway
	// holds no secret and never returns one.
	KeyringOwner      string     `json:"keyringOwner,omitempty"`
	KeyringProvider   string     `json:"keyringProvider,omitempty"`
	KeyringAccount    string     `json:"keyringAccount,omitempty"`
	KeyringSecretName string     `json:"keyringSecretName,omitempty"`
	LastRefreshedAt   *time.Time `json:"lastRefreshedAt,omitempty"`
	LastError         string     `json:"lastError,omitempty"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

// AuthInput is the credential part of a create/update request.
type AuthInput struct {
	Kind        store.AuthKind `json:"kind"`
	Username    string         `json:"username,omitempty"`
	Password    string         `json:"password,omitempty"`
	HeaderName  string         `json:"headerName,omitempty"`
	HeaderValue string         `json:"headerValue,omitempty"`
	Scopes      []string       `json:"scopes,omitempty"` // oauth: override discovered scopes
	// keyring_oauth: an OAuth account in the caller's keyring.
	KeyringProvider string `json:"keyringProvider,omitempty"`
	KeyringAccount  string `json:"keyringAccount,omitempty"`
	// keyring_secret: the name of a secret in the caller's keyring.
	KeyringSecretName string `json:"keyringSecretName,omitempty"`
}

// CreateServerInput is the body of POST /servers. Slug is derived from the
// name when empty, with its tail bumped until it is free; one that is given
// must be free.
type CreateServerInput struct {
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	URL         string    `json:"url"`
	Description string    `json:"description"`
	Auth        AuthInput `json:"auth"`
}

// UpdateServerInput is the body of PATCH /servers/{id}.
type UpdateServerInput struct {
	Name        *string          `json:"name,omitempty"`
	URL         *string          `json:"url,omitempty"`
	Description *string          `json:"description,omitempty"`
	Enabled     *bool            `json:"enabled,omitempty"`
	Selection   *store.Selection `json:"selection,omitempty"`
	// Hooks sets the rules: the text of each hook's rules, by hook property.
	// Nil leaves them as they are. What is sent replaces them all: a
	// property that is null or absent clears that hook's rules.
	Hooks *ruletext.Hooks `json:"hooks,omitempty"`
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

// DiscoverResult is what the server reported at initialize, verbatim:
// the negotiated protocol version, its implementation record, capabilities,
// instructions and any _meta. Capabilities and Meta are passed through as
// raw JSON so new spec fields show without a gateway change.
type DiscoverResult struct {
	OK              bool            `json:"ok"`
	LatencyMS       int64           `json:"latencyMs"`
	ProtocolVersion string          `json:"protocolVersion,omitempty"`
	ServerInfo      json.RawMessage `json:"serverInfo,omitempty"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
	Instructions    string          `json:"instructions,omitempty"`
	Meta            json.RawMessage `json:"_meta,omitempty"`
	Error           string          `json:"error,omitempty"`
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
	CreatedBy  string     `json:"createdBy,omitempty"`
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
	// RuleHitCount is how many rules applied to the request. Rules holds
	// them, on a single event only: a listing carries the count alone.
	RuleHitCount int             `json:"ruleHitCount,omitempty"`
	Rules        *store.RuleHits `json:"rules,omitempty"`
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
	URI       string         `json:"uri,omitempty"`    // resources/read
	Search    bool           `json:"search,omitempty"` // call the route in search mode (?search=true)
	// TaskID names the gateway's task for tasks/get, tasks/update and
	// tasks/cancel.
	TaskID string `json:"taskId,omitempty"`
	// InputResponses answers a task's questions on tasks/update, by key.
	InputResponses map[string]json.RawMessage `json:"inputResponses,omitempty"`
	// RequestState is the state a call that came back input_required
	// handed out; the retry sends it back with the answers.
	RequestState string `json:"requestState,omitempty"`
	// Task declares the tasks extension on a tools/call, so the server may
	// answer with a task.
	Task bool `json:"task,omitempty"`
}

// KeyringAuditDTO is the keyring's audit rows of one gateway request.
type KeyringAuditDTO struct {
	// RequestID is the id both trails carry for the request. The keyring's
	// own audit API filters on it too (?requestId=).
	RequestID string               `json:"requestId"`
	Items     []KeyringAuditRowDTO `json:"items"`
}

// KeyringAuditRowDTO is one keyring audit row: which credential was read or
// refreshed, for whom. Names only.
type KeyringAuditRowDTO struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Outcome string `json:"outcome"`
	// Owner is the keyring user the credential belongs to. HeldByGateway is
	// set when that is the gateway's own space: a credential the server
	// owns, which appears in no person's keyring trail.
	Owner          string    `json:"owner"`
	HeldByGateway  bool      `json:"heldByGateway"`
	TargetKind     string    `json:"targetKind,omitempty"`
	TargetProvider string    `json:"targetProvider,omitempty"`
	TargetAccount  string    `json:"targetAccount,omitempty"`
	TargetName     string    `json:"targetName,omitempty"`
	Method         string    `json:"method,omitempty"`
	Detail         string    `json:"detail,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

// AuditTraceDTO is one exchange as a trace: the inbound root (absent for
// API-initiated hops) and its upstream spans in start order.
type AuditTraceDTO struct {
	Root  *AuditEventDTO  `json:"root,omitempty"`
	Spans []AuditEventDTO `json:"spans"`
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
	UserID  string   `json:"userId"`
	Subject string   `json:"subject"`
	Type    string   `json:"type"`
	Email   string   `json:"email,omitempty"`
	Name    string   `json:"name,omitempty"`
	Scopes  []string `json:"scopes"`
}

// UserDTO is someone who has signed in.
type UserDTO struct {
	ID         string    `json:"id"`
	Email      string    `json:"email"`
	Name       string    `json:"name"`
	LastSeenAt time.Time `json:"lastSeenAt"`
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

// serverDTO is a server as the API returns it. held is what the keyring
// says about a credential it keeps for the server, or nil.
func serverDTO(base string, s *store.Server, c *store.Credential, held *keyringref.Description, users map[string]*store.User, seen int) ServerDTO {
	d := ServerDTO{
		ID: s.ID.String(), Slug: s.Slug, Name: s.Name, URL: s.URL, Description: s.Description,
		AuthKind: s.AuthKind, Enabled: s.Enabled, Seen: seen, MCPURL: base + "/" + s.Slug + "/mcp",
		Selection: s.Selection, Generation: s.Generation, Grants: grantDTOs(s.Grants, users),
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
	d.Hooks, d.RulesError = rulesOf(s.Hooks)
	if c != nil {
		d.Credential = credentialDTO(c, held)
		d.AuthKind = d.Credential.Kind
	}
	return d
}

// presentedKind is a credential's kind as the API shows it. A credential
// the gateway holds for a server in the keyring is shown as what the
// operator set, Basic or a header: that it lives in the keyring is where it
// is stored, not what it is. A reference into a person's keyring keeps its
// keyring kind, because whose it is matters there.
func presentedKind(c *store.Credential, held *keyringref.Description) store.AuthKind {
	if !c.Kind.FromKeyring() || !c.KeyringRef().HeldByGateway() {
		return c.Kind
	}
	// A server's own connection is OAuth whether or not the keyring holds
	// anything for it yet: discovery may have failed before it did.
	if c.Kind == store.AuthKeyringOAuth {
		return store.AuthOAuth
	}
	if held == nil {
		return c.Kind
	}
	switch held.Scheme {
	case keyringref.SchemeBasic:
		return store.AuthBasic
	case keyringref.SchemeHeader:
		return store.AuthHeader
	case keyringref.SchemeOAuth:
		return store.AuthOAuth
	}
	return c.Kind
}

// notMovedReason is why a credential still in the gateway's old store does
// not work, as the API says it.
const notMovedReason = "not moved to the keyring yet; run gateway migrate-credentials"

// credentialDTO is a credential as the API returns it. The row gives the
// kind, the state and the keyring reference. Everything that describes the
// credential itself (the user name, the header, the OAuth connection) comes
// from held, which is what the keyring says of a credential the gateway
// holds there, or nil.
func credentialDTO(c *store.Credential, held *keyringref.Description) *CredentialDTO {
	d := &CredentialDTO{
		Kind: presentedKind(c, held), State: c.State, StateReason: c.StateReason, OAuthScopes: []string{},
		KeyringOwner: c.KeyringOwner, KeyringProvider: c.KeyringProvider, KeyringAccount: c.KeyringAccount, KeyringSecretName: c.KeyringSecretName,
		LastError: c.LastError, UpdatedAt: c.UpdatedAt,
	}
	switch {
	case c.Kind.FromKeyring():
		d.Store = "keyring"
	case c.Kind.Unmigrated():
		// The gateway cannot send this credential whatever state its row is
		// in, and a request through the server fails. Say so where the
		// operator looks. What the row said of itself (awaiting consent, a
		// refused refresh) still holds once it is moved, so it follows.
		d.Store, d.State = "gateway", store.StateBroken
		if d.StateReason == "" {
			d.StateReason = notMovedReason
		} else {
			d.StateReason = notMovedReason + " (" + d.StateReason + ")"
		}
	}
	if held != nil {
		// How the secret is sent is the keyring's to say; these are names.
		d.BasicUsername, d.HeaderName = held.Username, held.HeaderName
	}
	if held != nil && held.Scheme == keyringref.SchemeOAuth {
		// A server's own connection: what the keyring discovered and holds.
		d.OAuthIssuer, d.OAuthClientID, d.OAuthRegistrationKind, d.OAuthResource = held.Issuer, held.ClientID, held.RegistrationKind, held.Resource
		d.OAuthExpiresAt, d.OAuthHasRefreshToken, d.LastRefreshedAt = held.ExpiresAt, held.HasRefreshToken, held.LastRefreshedAt
		if held.Scopes != nil {
			d.OAuthScopes = held.Scopes
		}
		// The keyring may have found the account broken before any request
		// through the gateway did.
		if held.Broken != "" && d.State == store.StateOK {
			d.State, d.StateReason = store.StateBroken, held.Broken
		}
	}
	return d
}

func keyDTO(k *store.APIKey) APIKeyDTO {
	return APIKeyDTO{ID: k.ID.String(), Name: k.Name, Prefix: k.Prefix, Scopes: k.Scopes, CreatedAt: k.CreatedAt, LastUsedAt: k.LastUsedAt, RevokedAt: k.RevokedAt, CreatedBy: k.CreatedBy}
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
	if e.Rules != nil {
		d.RuleHitCount = len(e.Rules.Hits) + e.Rules.Dropped
		d.Rules = e.Rules
	}
	return d
}

// auditRow is an event as a listing shows it: the count of its rules, not the rules.
func auditRow(e *store.AuditEvent) AuditEventDTO {
	d := auditDTO(e)
	d.Rules = nil
	return d
}

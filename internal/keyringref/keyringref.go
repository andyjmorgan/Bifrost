// Package keyringref is the one door between the gateway and the keyring.
//
// An MCP server may authenticate upstream with a credential that lives in
// the keyring instead of one the gateway stores itself. The gateway holds
// only a reference (Ref) and asks for the credential through Keyring each
// time it needs it. Nothing in the gateway imports the keyring's services,
// store or cipher: the keyring implements this interface (keyring.Source)
// and the gateway is handed it at start, or nil when the keyring is off.
//
// The package has no dependencies, so both sides can import it.
package keyringref

import (
	"context"
	"errors"
	"time"
)

// Kind says which sort of keyring credential a Ref names.
type Kind string

// Kinds of keyring credential a server can use.
const (
	// KindOAuth is a connected OAuth account: a provider and an account.
	KindOAuth Kind = "oauth"
	// KindSecret is a stored secret, by name.
	KindSecret Kind = "secret"
)

// GatewayOwner is the keyring user that holds the credentials an MCP server
// owns itself: the Basic password or header value typed in when the server
// was added, and the OAuth registration and tokens of a server the gateway
// connected to. It is a fixed id and no person: every keyring row keeps an
// explicit owner, and this is the owner of the rows that belong to a server
// rather than to somebody.
//
// Nobody reads this space through the keyring's API. The keyring refuses a
// caller that presents this id, so an MCP server's credential is handed to
// the gateway, in process, and to nothing else.
const GatewayOwner = "b1f405c7-0000-4000-8000-000000000001"

// ServerCredentialName is the name, in the keyring, of the credential a
// server owns itself: the name of its secret, and the key of its OAuth
// provider. A server's slug never changes, so neither does this.
func ServerCredentialName(slug string) string { return "mcp-" + slug }

// Ref names one credential in one user's keyring. Keyring rows belong to a
// user, so the owner is always part of the reference and a lookup never
// leaves that user's space.
type Ref struct {
	// Owner is the keyring user that holds the credential: the identity
	// provider subject of the person who pointed the server at it, or
	// GatewayOwner for a credential the server owns itself.
	Owner string
	Kind  Kind
	// Provider and Account name an OAuth account (KindOAuth).
	Provider string
	Account  string
	// Secret is the name of a stored secret (KindSecret).
	Secret string
}

// HeldByGateway reports whether the credential is one a server owns itself
// rather than one lent from a person's keyring.
func (r Ref) HeldByGateway() bool { return r.Owner == GatewayOwner }

// String is the reference as a log line or an error may show it: names
// only, never a value.
func (r Ref) String() string {
	if r.Kind == KindOAuth {
		return "OAuth account " + r.Provider + "/" + r.Account
	}
	return "secret " + r.Secret
}

// Use says which MCP server a credential is fetched or changed for. The
// keyring records it in its audit trail as the caller, beside the gateway
// itself.
type Use struct {
	ServerID string
	Slug     string
	// Actor is the gateway user who asked for a change to a credential the
	// gateway holds: the person who typed in a server's password, or
	// deleted the server. Empty for a fetch, which nobody asked for.
	Actor string
}

// Schemes a server's own static credential is sent by.
const (
	// SchemeBasic is HTTP Basic: a username and a password.
	SchemeBasic = "basic"
	// SchemeHeader is one header carrying a value.
	SchemeHeader = "header"
	// SchemeOAuth is a bearer token of an OAuth account. It is what
	// Describe says of an OAuth reference; a ServerSecret is never this.
	SchemeOAuth = "oauth"
)

// ServerSecret is a static credential a server owns itself, on its way into
// the keyring.
type ServerSecret struct {
	// Scheme is SchemeBasic or SchemeHeader.
	Scheme string
	// Username is the Basic user name.
	Username string
	// HeaderName is the header a SchemeHeader value is sent in.
	HeaderName string
	// Value is the password or the header value.
	Value string
	// URL is the server's address, kept beside the secret as a description
	// of what it is for. It is not used to send the secret anywhere.
	URL string
}

// Description is what the keyring says about a credential without
// decrypting its secret: enough for the servers API to show how a server
// authenticates.
type Description struct {
	// Scheme is SchemeBasic or SchemeHeader for a secret.
	Scheme string
	// Username is the Basic user name.
	Username string
	// HeaderName is the header the secret is sent in.
	HeaderName string

	// The rest describes an OAuth account (Scheme is SchemeOAuth).

	// Connected reports whether the account holds a token. A connection
	// whose consent has not finished does not.
	Connected bool
	// Broken is why the account needs authorizing again, or empty.
	Broken string
	// Issuer, Resource and RegistrationKind are what discovery and
	// registration found for a server's own connection. Empty for an
	// account of a provider somebody wrote.
	Issuer           string
	Resource         string
	RegistrationKind string
	// ClientID is the client the account was issued to. It is public.
	ClientID        string
	Scopes          []string
	ExpiresAt       *time.Time
	LastRefreshedAt *time.Time
	HasRefreshToken bool
}

// ServerAccount is the account name of a server's own OAuth connection. A
// server has one, so the name is fixed.
const ServerAccount = "default"

// ServerOAuth is an MCP server that wants OAuth, on its way to being
// connected through the keyring.
type ServerOAuth struct {
	// URL is the server's MCP endpoint, where discovery starts.
	URL string
	// Name is the server's display name.
	Name string
	// Scopes overrides the scopes discovery finds. Empty takes those.
	Scopes []string
	// ReturnTo is where the browser goes once consent has finished.
	ReturnTo string
}

// ServerConnection is a server's whole OAuth connection in the clear: what
// discovery found, the client that was registered, and the tokens. It exists
// only for `gateway migrate-credentials`, which carries a connection from
// one store to the other inside one process. Nothing else builds one, and
// nothing prints or logs one.
type ServerConnection struct {
	// ServerURL and Name are the server's, for the provider's own.
	ServerURL string
	Name      string

	Issuer                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	RegistrationEndpoint  string
	RegistrationKind      string // cimd | dcr
	Resource              string
	Scopes                []string

	ClientID     string
	ClientSecret string // empty for a public client

	// Connected reports whether tokens were ever issued. A connection whose
	// consent never finished has a client and no tokens.
	Connected       bool
	TokenType       string
	AccessToken     string
	RefreshToken    string
	ExpiresAt       *time.Time
	LastRefreshedAt *time.Time
	// Broken is why the connection needs authorizing again, or empty.
	Broken string
}

// OAuthState is what a consent round trip in flight is for.
type OAuthState struct {
	// Ref is the connection being authorized.
	Ref Ref
	// ReturnTo is where the round trip asked to end.
	ReturnTo string
}

// Token is a live OAuth access token.
type Token struct {
	// TokenType is the type the authorization server issued. Empty reads
	// as Bearer.
	TokenType   string
	AccessToken string
	// ExpiresAt is when the provider said the token ends; nil when it gave
	// no lifetime.
	ExpiresAt *time.Time
}

// Header is a credential in the form an HTTP request carries it.
type Header struct {
	Name  string
	Value string
}

// Errors a Keyring returns. Their text names the credential, never its value.
var (
	// ErrOff is the keyring not being enabled on this gateway.
	ErrOff = errors.New("the keyring is not enabled on this gateway")
	// ErrNotFound is a reference that names nothing in its owner's keyring:
	// never created, deleted since, or held by somebody else.
	ErrNotFound = errors.New("not found in the owner's keyring")
	// ErrUnusable is a credential that exists but cannot authenticate an
	// HTTP request, such as an SSH login.
	ErrUnusable = errors.New("cannot be sent as an HTTP header")
	// ErrNoRefresh is an OAuth account that cannot be refreshed: it holds
	// no refresh token, or its provider has no app credentials.
	ErrNoRefresh = errors.New("cannot be refreshed")
	// ErrBroken is an OAuth account whose refresh the token endpoint has
	// definitively refused. It stays unusable until it is authorized again.
	// Match it with errors.Is; the error itself is a *BrokenError.
	ErrBroken = errors.New("needs to be authorized again")
	// ErrNoState is a consent round trip the keyring does not know: never
	// started, already finished, expired, or not a server's.
	ErrNoState = errors.New("unknown or expired authorization state")
)

// BrokenError is ErrBroken with the token endpoint's reason.
type BrokenError struct{ Reason string }

func (e *BrokenError) Error() string { return ErrBroken.Error() + ": " + e.Reason }

// Is makes errors.Is(err, ErrBroken) hold.
func (e *BrokenError) Is(target error) bool { return target == ErrBroken }

// Keyring is what the gateway asks of the keyring to send a request: the
// credential a reference names. The connector is handed this much.
type Keyring interface {
	// Check reports whether ref names a credential the gateway could use,
	// without decrypting it. It is how a reference is validated when it is
	// set; it leaves no audit record.
	Check(ctx context.Context, ref Ref) error

	// OAuthToken returns a live access token for the OAuth account ref
	// names, refreshed by the keyring when it is close to expiry. A
	// non-empty rejected is the access token the upstream just refused: the
	// keyring then refreshes whatever the expiry says, unless the stored
	// token already differs from the rejected one.
	OAuthToken(ctx context.Context, ref Ref, use Use, rejected string) (Token, error)

	// SecretHeader decrypts the stored secret ref names and returns it as
	// the header the secret describes: its own header and prefix, or HTTP
	// Basic with its username.
	SecretHeader(ctx context.Context, ref Ref, use Use) (Header, error)
}

// Holder is everything the gateway may ask of the keyring: what Keyring
// lends, and the keeping of the credentials servers own themselves. The
// servers API is handed this.
type Holder interface {
	Keyring

	// Describe says how the credential ref names is sent, without
	// decrypting its secret. It leaves no audit record. An OAuth connection
	// whose consent has not finished is described too, as not connected.
	Describe(ctx context.Context, ref Ref) (Description, error)

	// BeginServerOAuth starts connecting the server use names to its own
	// OAuth authorization server: discovery from the MCP endpoint, client
	// registration (CIMD first, DCR otherwise) and PKCE. The provider, its
	// client and, later, its tokens are kept in the gateway's own keyring
	// space under ServerCredentialName of the slug. It returns the URL to
	// send the browser to and the reference the connection will have. A
	// connection the server already had is dropped.
	BeginServerOAuth(ctx context.Context, use Use, server ServerOAuth) (authorizeURL string, ref Ref, err error)

	// OAuthState says what the round trip state belongs to, without
	// consuming it. ErrNoState when it is not a server's pending consent.
	OAuthState(ctx context.Context, state string) (OAuthState, error)

	// CompleteServerOAuth redeems the code the authorization server sent
	// back and stores the tokens. The state is consumed whether or not the
	// exchange succeeds.
	CompleteServerOAuth(ctx context.Context, code, state string) error

	// AbandonServerOAuth ends a round trip the authorization server refused.
	AbandonServerOAuth(ctx context.Context, state, reason string) error

	// PutServerSecret stores the static credential of the server use names
	// in the gateway's own keyring space, replacing the one it had, and
	// returns the reference to it. The owner is GatewayOwner and the name
	// is ServerCredentialName of the slug.
	PutServerSecret(ctx context.Context, use Use, secret ServerSecret) (Ref, error)

	// Forget deletes a credential the gateway holds for a server, when the
	// server is deleted or given another credential. It does nothing to a
	// reference into a person's keyring: that credential is theirs.
	// Forgetting what is already gone is not an error.
	Forget(ctx context.Context, ref Ref, use Use) error
}

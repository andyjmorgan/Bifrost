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

// Ref names one credential in one user's keyring. Keyring rows belong to a
// user, so the owner is always part of the reference and a lookup never
// leaves that user's space.
type Ref struct {
	// Owner is the identity provider subject of the user whose keyring
	// holds the credential: the person who pointed the server at it.
	Owner string
	Kind  Kind
	// Provider and Account name an OAuth account (KindOAuth).
	Provider string
	Account  string
	// Secret is the name of a stored secret (KindSecret).
	Secret string
}

// String is the reference as a log line or an error may show it: names
// only, never a value.
func (r Ref) String() string {
	if r.Kind == KindOAuth {
		return "OAuth account " + r.Provider + "/" + r.Account
	}
	return "secret " + r.Secret
}

// Use says which MCP server a credential is fetched for. The keyring
// records it in its audit trail as the caller, beside the gateway itself.
type Use struct {
	ServerID string
	Slug     string
}

// Token is a live OAuth access token.
type Token struct {
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
)

// Keyring is everything the gateway may ask of the keyring.
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

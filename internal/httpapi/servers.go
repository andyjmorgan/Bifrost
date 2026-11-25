package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/cache"
	"github.com/andyjmorgan/Bifrost/internal/keyringref"
	"github.com/andyjmorgan/Bifrost/internal/slug"
	"github.com/andyjmorgan/Bifrost/internal/store"
	"github.com/andyjmorgan/Bifrost/internal/upstream"
)

func (a *API) withCredential(r *http.Request, s *store.Server) ServerDTO {
	return a.serverView(r, s, a.users(r))
}

// serverView is a server as the API returns it, with its grants' users
// named from users.
func (a *API) serverView(r *http.Request, s *store.Server, users map[string]*store.User) ServerDTO {
	ctx := r.Context()
	c, err := a.store.GetCredential(ctx, s.ID)
	if err != nil {
		c = nil
	}
	seen := 0
	if caps, err := a.store.ListCapabilities(ctx, s.ID); err == nil {
		seen = len(caps)
	}
	return serverDTO(a.publicBase, s, c, a.describe(ctx, s, c), users, seen)
}

// describe asks the keyring how a credential the gateway holds for a server
// is sent, so the API can show it as what the operator set: Basic with a
// username, or a header. Nil for every other credential, and when the
// keyring cannot say; the reference is then shown as it is stored.
func (a *API) describe(ctx context.Context, s *store.Server, c *store.Credential) *keyringref.Description {
	if c == nil || a.keyring == nil || !c.Kind.FromKeyring() {
		return nil
	}
	// A credential lent from a person's keyring is shown to everybody as the
	// reference it is: whose keyring, and the name. What the keyring says
	// about it beyond that (the user name of a Basic secret, the header it
	// travels in) is the owner's, and is shown to the owner alone, signed in
	// as themselves. Every operator sees every server; they do not thereby
	// see into each other's keyrings. A lent OAuth account has nothing more
	// to say and is never described.
	if !c.KeyringRef().HeldByGateway() {
		by := auth.CallerFrom(ctx)
		if c.Kind == store.AuthKeyringOAuth || !ownsKeyring(by, c.KeyringOwner) {
			return nil
		}
	}
	d, err := a.keyring.Describe(ctx, c.KeyringRef())
	if err != nil {
		// A connection whose discovery failed has nothing in the keyring yet;
		// its state already says so. A missing secret is worth a line.
		if c.KeyringRef().HeldByGateway() && c.Kind == store.AuthKeyringSecret {
			a.log.Warn("the keyring has no usable credential for a server", "slug", s.Slug, "credential", c.KeyringRef().String(), "err", err)
		}
		return nil
	}
	return &d
}

func (a *API) listServers(w http.ResponseWriter, r *http.Request) {
	servers, err := a.store.ListServers(r.Context())
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	users := a.users(r)
	out := make([]ServerDTO, 0, len(servers))
	for _, s := range servers {
		out = append(out, a.serverView(r, s, users))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getServer(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loadServer(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, a.withCredential(r, s))
}

func (a *API) loadServer(w http.ResponseWriter, r *http.Request) (*store.Server, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return nil, false
	}
	s, err := a.store.GetServer(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err)
		return nil, false
	}
	return s, true
}

func validURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("url must be an absolute http(s) URL")
	}
	return nil
}

func (a *API) createServer(w http.ResponseWriter, r *http.Request) {
	caller := auth.CallerFrom(r.Context())
	var in CreateServerInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.URL = strings.TrimSpace(in.URL)
	if in.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := validURL(in.URL); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Auth.Kind == "" {
		in.Auth.Kind = store.AuthNone
	}
	if err := validateAuth(in.Auth); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if status, err := a.checkKeyringAuth(r.Context(), caller, in.Auth); err != nil {
		writeErr(w, status, err.Error())
		return
	}
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	if in.Slug != "" && (!slug.Valid(in.Slug) || in.Slug == "mcp") {
		writeErr(w, http.StatusBadRequest, "slug must be 1-24 characters of a-z, 0-9 and -, and not mcp")
		return
	}
	now := time.Now()
	// A new server offers everything it has and is held by the person who
	// added it.
	s := &store.Server{ID: uuid.New(), Name: in.Name, URL: in.URL, Description: in.Description, AuthKind: storedKind(in.Auth.Kind), Enabled: true,
		Selection: store.Selection{}, Grants: []store.Grant{{UserID: caller.UserID}}, CreatedAt: now, UpdatedAt: now}
	if in.Slug != "" {
		s.Slug = in.Slug
		if err := a.store.CreateServer(r.Context(), s, caller.UserID); err != nil {
			writeStoreErr(w, err)
			return
		}
	} else {
		// The slug derives from the name; the tail is bumped until it is free.
		var err error
		slug.Candidates(slug.Base(in.Name), func(c string) bool {
			if c == "mcp" {
				return false
			}
			s.Slug = c
			err = a.store.CreateServer(r.Context(), s, caller.UserID)
			return !errors.Is(err, store.ErrConflict)
		})
		if err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	authorizeURL, err := a.applyAuth(r.Context(), s, in.Auth, caller)
	if err != nil {
		// Keep the server so the user can fix the credential, but report why.
		broken := a.credentialFor(s.Slug, s.ID, in.Auth, caller)
		broken.State, broken.StateReason = store.StateBroken, err.Error()
		_ = a.store.UpsertCredential(r.Context(), broken)
		writeJSON(w, http.StatusCreated, CreateServerResult{Server: a.withCredential(r, s)})
		return
	}
	writeJSON(w, http.StatusCreated, CreateServerResult{Server: a.withCredential(r, s), AuthorizeURL: authorizeURL})
}

func validateAuth(in AuthInput) error {
	switch in.Kind {
	case store.AuthNone, store.AuthOAuth:
		return nil
	case store.AuthKeyringOAuth:
		if strings.TrimSpace(in.KeyringProvider) == "" || strings.TrimSpace(in.KeyringAccount) == "" {
			return errors.New("a keyring OAuth account needs keyringProvider and keyringAccount")
		}
	case store.AuthKeyringSecret:
		if strings.TrimSpace(in.KeyringSecretName) == "" {
			return errors.New("a keyring secret needs keyringSecretName")
		}
	case store.AuthBasic:
		if in.Username == "" || in.Password == "" {
			return errors.New("basic auth needs username and password")
		}
	case store.AuthHeader:
		if in.HeaderName == "" || in.HeaderValue == "" {
			return errors.New("header auth needs headerName and headerValue")
		}
		if strings.ContainsAny(in.HeaderName, " :\r\n") {
			return errors.New("invalid header name")
		}
	default:
		return fmt.Errorf("unknown auth kind %q", in.Kind)
	}
	return nil
}

// storedKind is the kind a credential is stored as. A server's own
// credential lives in the gateway's keyring space and the gateway keeps the
// reference: a Basic or header credential as a secret, an OAuth connection
// as an account. The API goes on calling them Basic, header and OAuth (see
// presentedKind).
//
// The gateway has no store of its own for a credential, so this does not
// depend on whether the keyring is on. With the keyring off every kind but
// none is refused before anything is stored (checkKeyringAuth), and a server
// that already has a keyring credential fails to connect with
// upstream.ErrKeyringOff.
func storedKind(kind store.AuthKind) store.AuthKind {
	switch kind {
	case store.AuthBasic, store.AuthHeader:
		return store.AuthKeyringSecret
	case store.AuthOAuth:
		return store.AuthKeyringOAuth
	}
	return kind
}

// credentialFor is the credential row for in before any secret is stored:
// the kind and, for every kind but none, the keyring reference. A reference
// into a person's keyring is owned by the caller who is setting it; the
// credential a server owns itself is held by the gateway, under the
// server's name.
func (a *API) credentialFor(slug string, serverID uuid.UUID, in AuthInput, by *auth.Caller) *store.Credential {
	cred := &store.Credential{ServerID: serverID, Kind: storedKind(in.Kind), State: store.StateOK}
	switch in.Kind {
	case store.AuthKeyringOAuth:
		cred.KeyringOwner, cred.KeyringProvider, cred.KeyringAccount = by.UserID, strings.TrimSpace(in.KeyringProvider), strings.TrimSpace(in.KeyringAccount)
	case store.AuthKeyringSecret:
		cred.KeyringOwner, cred.KeyringSecretName = by.UserID, strings.TrimSpace(in.KeyringSecretName)
	case store.AuthBasic, store.AuthHeader:
		cred.KeyringOwner, cred.KeyringSecretName = keyringref.GatewayOwner, keyringref.ServerCredentialName(slug)
	case store.AuthOAuth:
		cred.KeyringOwner, cred.KeyringProvider, cred.KeyringAccount = keyringref.GatewayOwner, keyringref.ServerCredentialName(slug), keyringref.ServerAccount
	}
	return cred
}

// checkKeyringAuth refuses a credential the caller may not set, with the
// status to answer with. It runs before anything is stored, so a refused
// create leaves no server behind. No credential (none) always passes.
//
//   - The keyring must be on. Every credential is kept there: the ones a
//     server owns itself (basic, header, oauth) as much as the ones lent
//     from a person's keyring.
//
// The rest applies to a reference into a person's keyring only. A server's
// own credential belongs to the server, and an API key may set one.
//
//   - The caller must be a person. A gateway API key carries no right to
//     read the keyring, and pointing a server at a keyring credential makes
//     the gateway send that credential wherever the server's URL says.
//   - The reference must name something usable in the caller's own keyring.
//     It is resolved there and nowhere else, so somebody else's credential
//     is simply not found.
func (a *API) checkKeyringAuth(ctx context.Context, by *auth.Caller, in AuthInput) (int, error) {
	if in.Kind == store.AuthNone {
		return 0, nil
	}
	if a.keyring == nil {
		return http.StatusBadRequest, keyringref.ErrOff
	}
	if !in.Kind.FromKeyring() {
		return 0, nil
	}
	if by == nil || !ownsKeyring(by, by.UserID) {
		return http.StatusForbidden, errors.New("a keyring credential is set by its owner, signed in; an API key cannot set one")
	}
	if err := a.keyring.Check(ctx, a.credentialFor("", uuid.Nil, in, by).KeyringRef()); err != nil {
		if errors.Is(err, keyringref.ErrNotFound) || errors.Is(err, keyringref.ErrUnusable) {
			return http.StatusBadRequest, err
		}
		a.log.Error("keyring check failed", "err", err)
		return http.StatusBadGateway, errors.New("the keyring could not be read")
	}
	return 0, nil
}

// ownsKeyring reports whether the caller is the person owner names, signed
// in as themselves rather than acting through an API key.
func ownsKeyring(by *auth.Caller, owner string) bool {
	return by != nil && by.UserID != "" && by.UserID == owner && by.Type != auth.ActorAPIKey
}

// keyringOwnerOnly refuses a change that would send a server's keyring
// credential somewhere new, unless the caller is the credential's owner.
// Every operator may edit every server, but the credential is one person's:
// re-pointing the server would hand it to whoever answers at the new URL.
//
// A credential the gateway holds for the server is nobody's in particular.
// It was typed in for this server by an operator, and any operator may
// re-point the server, as they could when the gateway stored it itself.
func (a *API) keyringOwnerOnly(ctx context.Context, by *auth.Caller, s *store.Server) error {
	if !s.AuthKind.FromKeyring() {
		return nil
	}
	cred, err := a.store.GetCredential(ctx, s.ID)
	if err != nil || !cred.Kind.FromKeyring() || cred.KeyringRef().HeldByGateway() || ownsKeyring(by, cred.KeyringOwner) {
		return nil
	}
	return errors.New("this server uses a credential from another user's keyring; only its owner, signed in, can change the URL. Replace the credential first to take the server over")
}

// applyAuth gives a server the credential in: it stores a Basic or header
// secret in the gateway's own keyring space, or takes a reference into the
// caller's keyring, or starts an OAuth connection and returns the consent
// URL to open. The gateway's row is the reference in every case; no secret
// is kept beside it. by is the caller, who becomes the owner of a reference
// into their keyring.
//
// The credential the server had is replaced whole, including one still in
// the gateway's old store: its sealed columns are cleared with the row
// (store.UpsertCredential).
func (a *API) applyAuth(ctx context.Context, s *store.Server, in AuthInput, by *auth.Caller) (string, error) {
	// checkKeyringAuth has refused this already; nothing below works without
	// the keyring.
	if in.Kind != store.AuthNone && a.keyring == nil {
		return "", keyringref.ErrOff
	}
	cred := a.credentialFor(s.Slug, s.ID, in, by)
	previous, _ := a.store.GetCredential(ctx, s.ID)
	switch in.Kind {
	case store.AuthNone, store.AuthKeyringOAuth, store.AuthKeyringSecret:
	case store.AuthBasic:
		if _, err := a.keyring.PutServerSecret(ctx, a.use(s, by), keyringref.ServerSecret{Scheme: keyringref.SchemeBasic, Username: in.Username, Value: in.Password, URL: s.URL}); err != nil {
			return "", err
		}
	case store.AuthHeader:
		if _, err := a.keyring.PutServerSecret(ctx, a.use(s, by), keyringref.ServerSecret{Scheme: keyringref.SchemeHeader, HeaderName: in.HeaderName, Value: in.HeaderValue, URL: s.URL}); err != nil {
			return "", err
		}
	case store.AuthOAuth:
		return a.startOAuth(ctx, s, in.Scopes, "", by)
	}
	if err := a.store.UpsertCredential(ctx, cred); err != nil {
		return "", err
	}
	a.forget(ctx, s, previous, cred, by)
	a.warmTools(ctx, s.ID)
	return "", nil
}

// use names the server, and the caller acting on it, for the keyring's
// audit trail.
func (a *API) use(s *store.Server, by *auth.Caller) keyringref.Use {
	u := keyringref.Use{ServerID: s.ID.String(), Slug: s.Slug}
	if by != nil {
		u.Actor = by.UserID
	}
	return u
}

// forget deletes the credential the gateway held for a server in the
// keyring, once the server has been given another (now) or is gone (now is
// nil). A credential that is still the server's is left alone, and so is
// one from a person's keyring, which was only ever borrowed. A failure is
// logged and leaves a credential nothing refers to; it never fails the
// change that made it redundant.
func (a *API) forget(ctx context.Context, s *store.Server, was, now *store.Credential, by *auth.Caller) {
	if a.keyring == nil || was == nil || !was.Kind.FromKeyring() || !was.KeyringRef().HeldByGateway() {
		return
	}
	if now != nil && now.Kind.FromKeyring() && now.KeyringRef() == was.KeyringRef() {
		return
	}
	if err := a.keyring.Forget(context.WithoutCancel(ctx), was.KeyringRef(), a.use(s, by)); err != nil {
		a.log.Warn("a server's old keyring credential could not be deleted", "slug", s.Slug, "credential", was.KeyringRef().String(), "err", err)
	}
}

// warmTools lists a server's tools with its current configuration and
// rewrites the cache row. Called after anything that changes how the server
// is reached; a failure is logged and leaves the previous row (if any) in
// place, so an edit never empties the catalogue on its own. The server is
// re-read first so the write carries the new updated_at.
func (a *API) warmTools(ctx context.Context, id uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	s, err := a.store.GetServer(ctx, id)
	if err != nil || !s.Enabled {
		return
	}
	if _, err := a.gw.RefreshServer(ctx, s); err != nil {
		a.log.Warn("tools refresh after change failed", "server", s.Name, "err", err)
	}
}

// startOAuth discovers, registers and prepares the consent redirect. The
// credential is left pending until the callback lands.
//
// The keyring does all of it and keeps the result: the provider it
// discovered, the client it registered and, once consent has finished, the
// tokens. The gateway's row is a reference to that connection.
func (a *API) startOAuth(ctx context.Context, s *store.Server, scopes []string, returnTo string, by *auth.Caller) (string, error) {
	if a.keyring == nil {
		return "", keyringref.ErrOff
	}
	previous, _ := a.store.GetCredential(ctx, s.ID)
	authorizeURL, ref, err := a.keyring.BeginServerOAuth(ctx, a.use(s, by), keyringref.ServerOAuth{URL: s.URL, Name: s.Name, Scopes: scopes, ReturnTo: returnTo})
	if err != nil {
		return "", err
	}
	cred := &store.Credential{
		ServerID: s.ID, Kind: store.AuthKeyringOAuth, State: store.StatePending, StateReason: "awaiting consent",
		KeyringOwner: ref.Owner, KeyringProvider: ref.Provider, KeyringAccount: ref.Account,
	}
	if err := a.store.UpsertCredential(ctx, cred); err != nil {
		return "", err
	}
	a.forget(ctx, s, previous, cred, by)
	return authorizeURL, nil
}

// ownOAuth reports whether a server authenticates with an OAuth connection
// of its own: one the gateway holds for it in the keyring. An account lent
// from a person's keyring is not the server's to re-authorize, and a
// connection still in the gateway's old store has to be moved first.
func ownOAuth(c *store.Credential) bool {
	return c != nil && c.Kind == store.AuthKeyringOAuth && c.KeyringRef().HeldByGateway()
}

func (a *API) updateServer(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loadServer(w, r)
	if !ok {
		return
	}
	var in UpdateServerInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
		s.Name = strings.TrimSpace(*in.Name)
	}
	if in.URL != nil {
		if err := validURL(*in.URL); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if *in.URL != s.URL {
			if err := a.keyringOwnerOnly(r.Context(), auth.CallerFrom(r.Context()), s); err != nil {
				writeErr(w, http.StatusForbidden, err.Error())
				return
			}
		}
		s.URL = *in.URL
	}
	if in.Description != nil {
		s.Description = *in.Description
	}
	if in.Enabled != nil {
		s.Enabled = *in.Enabled
	}
	if in.Selection != nil || in.Hooks != nil {
		if in.Selection != nil {
			s.Selection = *in.Selection
		}
		if in.Hooks != nil {
			stored, ok := storedHooks(w, in.Hooks)
			if !ok {
				return
			}
			s.Hooks = stored
		}
		if err := a.store.UpdateRules(r.Context(), s); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	if in.Name != nil || in.URL != nil || in.Description != nil || in.Enabled != nil {
		if err := a.store.UpdateServer(r.Context(), s); err != nil {
			writeStoreErr(w, err)
			return
		}
		// Refetch with the new configuration rather than dropping the row: a
		// delete would send every concurrent caller upstream at once, and a
		// failed refetch should not take a working catalogue down with it.
		a.warmTools(r.Context(), s.ID)
	}
	writeJSON(w, http.StatusOK, a.withCredential(r, s))
}

func (a *API) deleteServer(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loadServer(w, r)
	if !ok {
		return
	}
	held, _ := a.store.GetCredential(r.Context(), s.ID)
	if err := a.store.DeleteServer(r.Context(), s.ID); err != nil {
		writeStoreErr(w, err)
		return
	}
	// The credential the gateway held for the server goes with it.
	a.forget(r.Context(), s, held, nil, auth.CallerFrom(r.Context()))
	w.WriteHeader(http.StatusNoContent)
}

// putCredential replaces the credential (and auth kind) of a server.
func (a *API) putCredential(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loadServer(w, r)
	if !ok {
		return
	}
	var in AuthInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Kind == "" {
		in.Kind = store.AuthNone
	}
	if err := validateAuth(in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	caller := auth.CallerFrom(r.Context())
	if status, err := a.checkKeyringAuth(r.Context(), caller, in); err != nil {
		writeErr(w, status, err.Error())
		return
	}
	if kind := storedKind(in.Kind); s.AuthKind != kind {
		s.AuthKind = kind
		if err := a.store.UpdateServer(r.Context(), s); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	authorizeURL, err := a.applyAuth(r.Context(), s, in, caller)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, CreateServerResult{Server: a.withCredential(r, s), AuthorizeURL: authorizeURL})
}

// oauthRefresh has the keyring replace the server's OAuth access token now,
// rather than when it next nears expiry or is rejected.
func (a *API) oauthRefresh(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loadServer(w, r)
	if !ok {
		return
	}
	cred, _ := a.store.GetCredential(r.Context(), s.ID)
	if !ownOAuth(cred) {
		writeErr(w, http.StatusBadRequest, "server does not hold its own oauth connection")
		return
	}
	err := a.conn.RefreshOAuth(r.Context(), s, cred)
	switch {
	case errors.Is(err, upstream.ErrKeyringOff):
		writeErr(w, http.StatusBadRequest, keyringref.ErrOff.Error())
		return
	case errors.Is(err, keyringref.ErrNoRefresh):
		writeErr(w, http.StatusConflict, "no refresh token is held for this server; re-authorize it instead")
		return
	case errors.Is(err, keyringref.ErrNotFound):
		writeErr(w, http.StatusConflict, "the keyring holds no token for this server yet; authorize it first")
		return
	case errors.Is(err, upstream.ErrCredentialBroken):
		writeErr(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.withCredential(r, s))
}

// oauthStart (re)starts consent for a server's own OAuth connection.
func (a *API) oauthStart(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loadServer(w, r)
	if !ok {
		return
	}
	cred, _ := a.store.GetCredential(r.Context(), s.ID)
	if cred != nil && cred.Kind == store.AuthOAuth {
		// Consent cannot be restarted on a row this gateway cannot read.
		writeErr(w, http.StatusBadRequest, "this server's OAuth connection is "+notMovedReason+", or replace the credential")
		return
	}
	if !ownOAuth(cred) {
		writeErr(w, http.StatusBadRequest, "server does not use oauth")
		return
	}
	if a.keyring == nil {
		writeErr(w, http.StatusBadRequest, keyringref.ErrOff.Error())
		return
	}
	var in struct {
		Scopes   []string `json:"scopes,omitempty"`
		ReturnTo string   `json:"returnTo,omitempty"`
	}
	if r.ContentLength > 0 {
		if err := decode(r, &in); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if len(in.Scopes) == 0 {
		// Ask again for what was granted last time.
		if d := a.describe(r.Context(), s, cred); d != nil {
			in.Scopes = d.Scopes
		}
	}
	u, err := a.startOAuth(r.Context(), s, in.Scopes, in.ReturnTo, auth.CallerFrom(r.Context()))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"authorizeUrl": u})
}

// keyringOAuthCallback finishes a consent round trip the keyring started for
// a server. The keyring redeems the code and keeps the tokens; the gateway
// marks its reference usable, or broken with the reason, and sends the
// browser back.
func (a *API) keyringOAuthCallback(w http.ResponseWriter, r *http.Request, st keyringref.OAuthState) {
	ctx, q := r.Context(), r.URL.Query()
	state := q.Get("state")
	serverID, err := a.store.FindServerByKeyringOAuth(ctx, st.Ref.Owner, st.Ref.Provider)
	if err != nil {
		// The server was deleted, or given another credential, while its
		// consent was open. Nothing is left to authorize.
		_ = a.keyring.AbandonServerOAuth(ctx, state, "the server no longer uses this connection")
		http.Redirect(w, r, a.publicBase+"/servers?oauth=error&reason="+url.QueryEscape("the server no longer uses this connection"), http.StatusFound)
		return
	}
	redirect := func(status, reason string) {
		http.Redirect(w, r, consentReturn(a.publicBase, st.ReturnTo, serverID, status, reason), http.StatusFound)
	}
	if e := q.Get("error"); e != "" {
		_ = a.keyring.AbandonServerOAuth(ctx, state, e)
		_ = a.store.SetCredentialState(ctx, serverID, store.StateBroken, e+": "+q.Get("error_description"))
		redirect("error", e)
		return
	}
	if err := a.keyring.CompleteServerOAuth(ctx, q.Get("code"), state); err != nil {
		_ = a.store.SetCredentialState(ctx, serverID, store.StateBroken, err.Error())
		redirect("error", err.Error())
		return
	}
	if err := a.store.SetCredentialState(ctx, serverID, store.StateOK, ""); err != nil {
		redirect("error", err.Error())
		return
	}
	a.warmTools(ctx, serverID)
	redirect("ok", "")
}

// consentReturn is where the browser goes after a consent round trip: the
// page that started it, or the server's own, with the outcome appended.
func consentReturn(publicBase, returnTo string, serverID uuid.UUID, status, reason string) string {
	to := returnTo
	if to == "" {
		to = publicBase + "/servers/" + serverID.String()
	}
	sep := "?"
	if strings.Contains(to, "?") {
		sep = "&"
	}
	to += sep + "oauth=" + status
	if reason != "" {
		to += "&reason=" + url.QueryEscape(reason)
	}
	return to
}

// oauthCallback finishes the consent round trip and redirects to the UI.
// The path is the redirect URI every MCP authorization server has on file
// for the gateway, so it stays where it is, but the round trip is the
// keyring's: it started it, and it redeems the code and keeps the tokens.
// A state the keyring does not know, and any state at all with the keyring
// off, is answered as unknown or expired.
func (a *API) oauthCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if a.keyring != nil {
		st, err := a.keyring.OAuthState(r.Context(), state)
		if err == nil {
			a.keyringOAuthCallback(w, r, st)
			return
		}
		if !errors.Is(err, keyringref.ErrNoState) {
			a.log.Warn("the keyring could not look up an authorization state", "err", err)
		}
	}
	http.Redirect(w, r, a.publicBase+"/servers?oauth=error&reason="+url.QueryEscape("unknown or expired state"), http.StatusFound)
}

// discover opens a session and returns the initialize result as the server
// sent it. Opening also refreshes the init cache (OnInit), so this is the
// operator's way to re-read instructions and capabilities after an upgrade.
func (a *API) discover(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loadServer(w, r)
	if !ok {
		return
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	sess, err := a.conn.Open(ctx, s)
	if err != nil {
		writeJSON(w, http.StatusOK, DiscoverResult{OK: false, LatencyMS: time.Since(start).Milliseconds(), Error: err.Error()})
		return
	}
	defer sess.Close()
	res := DiscoverResult{OK: true, LatencyMS: time.Since(start).Milliseconds()}
	if ir := sess.InitializeResult(); ir != nil {
		res.ProtocolVersion, res.Instructions = ir.ProtocolVersion, ir.Instructions
		res.ServerInfo, _ = json.Marshal(ir.ServerInfo)
		res.Capabilities, _ = json.Marshal(ir.Capabilities)
		if len(ir.Meta) > 0 {
			res.Meta, _ = json.Marshal(ir.Meta)
		}
	}
	writeJSON(w, http.StatusOK, res)
}

// ping opens a session to the server with its credential and reports.
func (a *API) ping(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loadServer(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, a.doPing(r.Context(), s))
}

func (a *API) doPing(ctx context.Context, s *store.Server) PingResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	sess, err := a.conn.Open(ctx, s)
	if err != nil {
		return PingResult{OK: false, LatencyMS: time.Since(start).Milliseconds(), Error: err.Error()}
	}
	defer sess.Close()
	res := PingResult{OK: true}
	if ir := sess.InitializeResult(); ir != nil {
		res.ProtocolVersion = ir.ProtocolVersion
		if ir.ServerInfo != nil {
			res.ServerName, res.ServerVersion = ir.ServerInfo.Name, ir.ServerInfo.Version
		}
	}
	if tools, err := sess.ListTools(sess.Ctx, &mcp.ListToolsParams{}); err == nil {
		res.ToolCount = len(tools.Tools)
		// A ping is the operator asking "what is this server now": refresh the cache.
		id := s.ID
		a.cache.Put(cache.NSTools, id.String(), &id, s.UpdatedAt, tools.Tools, a.toolsTTL)
	} else {
		res.OK, res.Error = false, err.Error()
	}
	res.LatencyMS = time.Since(start).Milliseconds()
	return res
}

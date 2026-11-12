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
	"github.com/andyjmorgan/Bifrost/internal/oauthclient"
	"github.com/andyjmorgan/Bifrost/internal/slug"
	"github.com/andyjmorgan/Bifrost/internal/store"
)

func (a *API) withCredential(ctx context.Context, s *store.Server) ServerDTO {
	c, err := a.store.GetCredential(ctx, s.ID)
	if err != nil {
		c = nil
	}
	ents, _ := a.store.ListEntitlements(ctx, s.OrgID, &s.ID)
	seen := 0
	if caps, err := a.store.ListCapabilities(ctx, s.OrgID, s.ID); err == nil {
		seen = len(caps)
	}
	return serverDTO(a.publicBase, s, c, ents, seen)
}

func (a *API) listServers(w http.ResponseWriter, r *http.Request) {
	caller := auth.CallerFrom(r.Context())
	servers, err := a.store.ListServers(r.Context(), caller.OrgID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := make([]ServerDTO, 0, len(servers))
	for _, s := range servers {
		out = append(out, a.withCredential(r.Context(), s))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getServer(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loadServer(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, a.withCredential(r.Context(), s))
}

func (a *API) loadServer(w http.ResponseWriter, r *http.Request) (*store.Server, bool) {
	caller := auth.CallerFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return nil, false
	}
	s, err := a.store.GetServer(r.Context(), caller.OrgID, id)
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
	now := time.Now()
	s := &store.Server{ID: uuid.New(), OrgID: caller.OrgID, Name: in.Name, URL: in.URL, Description: in.Description, AuthKind: in.Auth.Kind, Enabled: true, CreatedAt: now, UpdatedAt: now}

	if err := a.store.CreateServer(r.Context(), s); err != nil {
		writeStoreErr(w, err)
		return
	}
	// A server is only reachable through an entitlement, so every new server
	// gets a default one, everything it offers, held by the org's admins.
	// The slug derives from the name; the tail is bumped until it is free.
	ent := &store.Entitlement{ID: uuid.New(), OrgID: s.OrgID, ServerID: s.ID, Name: in.Name, Description: "Everything the server offers", Selection: store.Selection{All: true}}
	var created bool
	slug.Candidates(slug.Base(in.Name), func(c string) bool {
		ent.Slug = c
		err := a.store.CreateEntitlement(r.Context(), ent)
		if errors.Is(err, store.ErrConflict) {
			return false
		}
		created = err == nil
		if err != nil {
			a.log.Error("create default entitlement", "err", err)
		}
		return true
	})
	if created {
		_ = a.store.SetGrants(r.Context(), s.OrgID, ent.ID, []store.Grant{{SubjectType: "role", SubjectID: "admin"}}, caller.UserID)
	}
	authorizeURL, err := a.applyAuth(r.Context(), s, in.Auth)
	if err != nil {
		// Keep the server so the user can fix the credential, but report why.
		_ = a.store.UpsertCredential(r.Context(), &store.Credential{ServerID: s.ID, Kind: in.Auth.Kind, State: store.StateBroken, StateReason: err.Error(), OAuthScopes: []string{}})
		writeJSON(w, http.StatusCreated, CreateServerResult{Server: a.withCredential(r.Context(), s)})
		return
	}
	writeJSON(w, http.StatusCreated, CreateServerResult{Server: a.withCredential(r.Context(), s), AuthorizeURL: authorizeURL})
}

func validateAuth(in AuthInput) error {
	switch in.Kind {
	case store.AuthNone, store.AuthOAuth:
		return nil
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

// applyAuth stores a basic/header secret, or runs OAuth discovery and
// registration and returns the consent URL to open.
func (a *API) applyAuth(ctx context.Context, s *store.Server, in AuthInput) (string, error) {
	cred := &store.Credential{ServerID: s.ID, Kind: in.Kind, State: store.StateOK, OAuthScopes: []string{}}
	switch in.Kind {
	case store.AuthNone:
	case store.AuthBasic:
		blob, err := a.conn.SealSecret(s.ID, &store.Secret{Password: in.Password})
		if err != nil {
			return "", err
		}
		cred.BasicUsername, cred.SecretBlob = in.Username, blob
	case store.AuthHeader:
		blob, err := a.conn.SealSecret(s.ID, &store.Secret{HeaderValue: in.HeaderValue})
		if err != nil {
			return "", err
		}
		cred.HeaderName, cred.SecretBlob = in.HeaderName, blob
	case store.AuthOAuth:
		return a.startOAuth(ctx, s, in.Scopes, "")
	}
	if err := a.store.UpsertCredential(ctx, cred); err != nil {
		return "", err
	}
	a.warmTools(ctx, s.OrgID, s.ID)
	return "", nil
}

// warmTools lists a server's tools with its current configuration and
// rewrites the cache row. Called after anything that changes how the server
// is reached; a failure is logged and leaves the previous row (if any) in
// place, so an edit never empties the catalogue on its own. The server is
// re-read first so the write carries the new updated_at.
func (a *API) warmTools(ctx context.Context, owner string, id uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	s, err := a.store.GetServer(ctx, owner, id)
	if err != nil || !s.Enabled {
		return
	}
	if _, err := a.gw.RefreshServer(ctx, s); err != nil {
		a.log.Warn("tools refresh after change failed", "server", s.Name, "err", err)
	}
}

// startOAuth discovers, registers and prepares the consent redirect. The
// credential is left pending until the callback lands.
func (a *API) startOAuth(ctx context.Context, s *store.Server, scopeOverride []string, returnTo string) (string, error) {
	d, err := a.oauth.Discover(ctx, s.URL)
	if err != nil {
		return "", err
	}
	scopes := scopeOverride
	if len(scopes) == 0 {
		scopes = d.ChallengeScopes
	}
	if len(scopes) == 0 {
		scopes = d.ScopesSupported
	}
	reg, err := a.oauth.Register(ctx, d, scopes)
	if err != nil {
		return "", err
	}
	pkce, err := oauthclient.NewPKCE()
	if err != nil {
		return "", err
	}
	state, err := oauthclient.NewState()
	if err != nil {
		return "", err
	}
	blob, err := a.conn.SealSecret(s.ID, &store.Secret{ClientSecret: reg.ClientSecret})
	if err != nil {
		return "", err
	}
	cred := &store.Credential{
		ServerID: s.ID, Kind: store.AuthOAuth, State: store.StatePending, StateReason: "awaiting consent",
		OAuthIssuer: d.AuthorizationServer, OAuthAuthorizationEndpoint: d.AuthorizationEndpoint, OAuthTokenEndpoint: d.TokenEndpoint,
		OAuthRegistrationEndpoint: d.RegistrationEndpoint, OAuthRegistrationKind: reg.Kind, OAuthClientID: reg.ClientID,
		OAuthResource: d.Resource, OAuthScopes: scopes, SecretBlob: blob,
	}
	if cred.OAuthScopes == nil {
		cred.OAuthScopes = []string{}
	}
	if err := a.store.UpsertCredential(ctx, cred); err != nil {
		return "", err
	}
	if err := a.store.PutOAuthState(ctx, &store.OAuthState{State: state, OrgID: s.OrgID, ServerID: s.ID, CodeVerifier: pkce.Verifier, ReturnTo: returnTo, ExpiresAt: time.Now().Add(15 * time.Minute)}); err != nil {
		return "", err
	}
	return a.oauth.AuthorizeURL(d, reg, scopes, state, pkce), nil
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
		s.URL = *in.URL
	}
	if in.Description != nil {
		s.Description = *in.Description
	}
	if in.Exclusive != nil {
		s.Exclusive = *in.Exclusive
	}
	if in.Enabled != nil {
		s.Enabled = *in.Enabled
	}
	if err := a.store.UpdateServer(r.Context(), s); err != nil {
		writeStoreErr(w, err)
		return
	}
	// Refetch with the new configuration rather than dropping the row: a
	// delete would send every concurrent caller upstream at once, and a
	// failed refetch should not take a working catalogue down with it.
	a.warmTools(r.Context(), s.OrgID, s.ID)
	writeJSON(w, http.StatusOK, a.withCredential(r.Context(), s))
}

func (a *API) deleteServer(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loadServer(w, r)
	if !ok {
		return
	}
	if err := a.store.DeleteServer(r.Context(), s.OrgID, s.ID); err != nil {
		writeStoreErr(w, err)
		return
	}
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
	if s.AuthKind != in.Kind {
		s.AuthKind = in.Kind
		if err := a.store.UpdateServer(r.Context(), s); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	authorizeURL, err := a.applyAuth(r.Context(), s, in)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, CreateServerResult{Server: a.withCredential(r.Context(), s), AuthorizeURL: authorizeURL})
}

// oauthStart (re)starts consent for an existing OAuth server.
func (a *API) oauthStart(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loadServer(w, r)
	if !ok {
		return
	}
	if s.AuthKind != store.AuthOAuth {
		writeErr(w, http.StatusBadRequest, "server does not use oauth")
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
		if c, err := a.store.GetCredential(r.Context(), s.ID); err == nil {
			in.Scopes = c.OAuthScopes
		}
	}
	u, err := a.startOAuth(r.Context(), s, in.Scopes, in.ReturnTo)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"authorizeUrl": u})
}

// oauthCallback finishes the consent round trip and redirects to the UI.
func (a *API) oauthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	st, err := a.store.TakeOAuthState(r.Context(), q.Get("state"))
	if err != nil {
		http.Redirect(w, r, a.publicBase+"/servers?oauth=error&reason="+url.QueryEscape("unknown or expired state"), http.StatusFound)
		return
	}
	redirect := func(status, reason string) {
		to := st.ReturnTo
		if to == "" {
			to = a.publicBase + "/servers/" + st.ServerID.String()
		}
		sep := "?"
		if strings.Contains(to, "?") {
			sep = "&"
		}
		to += sep + "oauth=" + status
		if reason != "" {
			to += "&reason=" + url.QueryEscape(reason)
		}
		http.Redirect(w, r, to, http.StatusFound)
	}
	if e := q.Get("error"); e != "" {
		_ = a.store.SetCredentialState(r.Context(), st.ServerID, store.StateBroken, e+": "+q.Get("error_description"))
		redirect("error", e)
		return
	}
	cred, err := a.store.GetCredential(r.Context(), st.ServerID)
	if err != nil {
		redirect("error", "credential missing")
		return
	}
	sec, err := a.conn.OpenSecret(cred)
	if err != nil {
		redirect("error", err.Error())
		return
	}
	reg := &oauthclient.Registration{Kind: cred.OAuthRegistrationKind, ClientID: cred.OAuthClientID, ClientSecret: sec.ClientSecret}
	ts, err := a.oauth.Exchange(r.Context(), cred.OAuthTokenEndpoint, reg, q.Get("code"), st.CodeVerifier, cred.OAuthResource)
	if err != nil {
		_ = a.store.SetCredentialState(r.Context(), st.ServerID, store.StateBroken, err.Error())
		redirect("error", err.Error())
		return
	}
	blob, err := a.conn.SealSecret(st.ServerID, &store.Secret{AccessToken: ts.AccessToken, RefreshToken: ts.RefreshToken, ClientSecret: sec.ClientSecret})
	if err != nil {
		redirect("error", err.Error())
		return
	}
	if err := a.store.UpdateOAuthTokens(r.Context(), st.ServerID, blob, ts.TokenType, ts.ExpiresAt, ts.RefreshToken != "", ts.Scope); err != nil {
		redirect("error", err.Error())
		return
	}
	a.warmTools(r.Context(), st.OrgID, st.ServerID)
	redirect("ok", "")
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

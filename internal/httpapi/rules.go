package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/gateway"
	"github.com/andyjmorgan/Bifrost/internal/portal"
	"github.com/andyjmorgan/Bifrost/internal/ruletext"
	"github.com/andyjmorgan/Bifrost/internal/store"
	"github.com/andyjmorgan/Bifrost/pkg/hooks"
)

// A server's rules, selection and grants are edited on the server itself
// (servers.go). This file has what the rule editor and the member portal
// need besides: checking rules, the hook reference, the catalog a server
// has been seen to offer, and the servers a member holds.

// GrantDTO gives one user the server.
type GrantDTO struct {
	UserID    string    `json:"userId"`
	Email     string    `json:"email,omitempty"`
	Name      string    `json:"name,omitempty"`
	GrantedAt time.Time `json:"grantedAt"`
}

// RuleProblems is the 400 body when rules do not compile.
type RuleProblems struct {
	Error    string             `json:"error"`
	Problems []ruletext.Problem `json:"problems"`
}

// storedHooks turns the hooks of an update into what is stored. It writes
// the 400 itself and returns false when their rules do not compile.
func storedHooks(w http.ResponseWriter, hs *ruletext.Hooks) (json.RawMessage, bool) {
	if _, probs := ruletext.Compile(hs.ByName()); len(probs) > 0 {
		writeJSON(w, http.StatusBadRequest, RuleProblems{Error: "rules do not compile", Problems: probs})
		return nil, false
	}
	return hs.Stored(), true
}

// CapabilityDTO is one thing a server has been seen to offer, with the
// definition as last sent (a tool, prompt, resource or template object).
type CapabilityDTO struct {
	Kind        string          `json:"kind"`
	Name        string          `json:"name"`
	Fingerprint string          `json:"fingerprint,omitempty"`
	Definition  json.RawMessage `json:"definition,omitempty"`
	FirstSeenAt time.Time       `json:"firstSeenAt"`
	LastSeenAt  time.Time       `json:"lastSeenAt"`
}

// PortalServerDTO is one server the caller holds: what the member portal
// shows. The Connect API lists the same thing (internal/portal).
type PortalServerDTO = portal.Server

// rulesOf reads a server's stored hooks for the API: the text of each
// hook's rules, and why they cannot be used if they cannot.
func rulesOf(raw json.RawMessage) (ruletext.Hooks, string) {
	hs, err := ruletext.ParseStored(raw)
	if err != nil {
		return hs, err.Error()
	}
	if _, probs := ruletext.Compile(hs.ByName()); len(probs) > 0 {
		return hs, ruletext.ProblemsError(probs).Error()
	}
	return hs, ""
}

// grantDTOs names the users of a server's grants.
func grantDTOs(grants []store.Grant, users map[string]*store.User) []GrantDTO {
	out := make([]GrantDTO, 0, len(grants))
	for _, g := range grants {
		gd := GrantDTO{UserID: g.UserID, GrantedAt: g.GrantedAt}
		if u := users[g.UserID]; u != nil {
			gd.Email, gd.Name = u.Email, u.Name
		}
		out = append(out, gd)
	}
	return out
}

// users returns everyone who has signed in, by id, for naming grants.
func (a *API) users(r *http.Request) map[string]*store.User {
	out := map[string]*store.User{}
	if us, err := a.store.ListUsers(r.Context()); err == nil {
		for _, u := range us {
			out[u.ID] = u
		}
	}
	return out
}

// validateRules checks rules without saving them. The text is compiled by
// the engine's compiler, which is what decides whether it can be saved, and
// then looked over for rules that compile but look wrong; those come back
// as warnings.
func (a *API) validateRules(w http.ResponseWriter, r *http.Request) {
	var in struct {
		// Hooks is the text to check: each hook's rules, by hook property.
		Hooks ruletext.Hooks `json:"hooks"`
		// Catalog is the names the server is known to offer, by kind. With
		// it, a rule that names something the server does not have is
		// warned about.
		Catalog map[string][]string `json:"catalog"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	text := in.Hooks.ByName()
	rules, probs := ruletext.Compile(text)
	if probs == nil {
		probs = []ruletext.Problem{}
	}
	warnings := ruletext.Lint(text, in.Catalog)
	if warnings == nil {
		warnings = []ruletext.Problem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"problems": probs, "warnings": warnings, "hasRules": len(rules) > 0})
}

// hooksSchema describes every hook to someone writing rules on it: what it
// controls, what a rule there may read and set, and a worked example. It
// comes from the engine's own registry, so an editor built on it cannot
// drift from what the engine compiles. Beside the hooks are the request
// flags and what this gateway puts in `caller`.
func (a *API) hooksSchema(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"flags": hooks.FlagDocs, "caller": gateway.CallerKeys, "hooks": ruletext.List()})
}

// putGrants replaces who holds a server.
func (a *API) putGrants(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	s, ok := a.loadServer(w, r)
	if !ok {
		return
	}
	var in struct {
		Grants []GrantDTO `json:"grants"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	users := a.users(r)
	grants := make([]store.Grant, 0, len(in.Grants))
	for _, g := range in.Grants {
		if users[g.UserID] == nil {
			writeErr(w, http.StatusBadRequest, "no user "+g.UserID+": a grant names someone who has signed in")
			return
		}
		grants = append(grants, store.Grant{UserID: g.UserID})
	}
	if err := a.store.SetGrants(r.Context(), s.ID, grants, c.UserID); err != nil {
		writeStoreErr(w, err)
		return
	}
	a.getServer(w, r)
}

// ---- catalog --------------------------------------------------------------------

// listCapabilities returns what a server has been seen to offer.
func (a *API) listCapabilities(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loadServer(w, r)
	if !ok {
		return
	}
	caps, err := a.store.ListCapabilities(r.Context(), s.ID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := make([]CapabilityDTO, 0, len(caps))
	for _, cp := range caps {
		out = append(out, CapabilityDTO{Kind: cp.Kind, Name: cp.Name, Fingerprint: cp.Fingerprint, Definition: cp.Definition, FirstSeenAt: cp.FirstSeenAt, LastSeenAt: cp.LastSeenAt})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- the member portal ----------------------------------------------------------

// listPortal returns the servers the caller holds, with what each exposes.
func (a *API) listPortal(w http.ResponseWriter, r *http.Request) {
	out, err := portal.List(r.Context(), a.store, a.publicBase, auth.CallerFrom(r.Context()).UserID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

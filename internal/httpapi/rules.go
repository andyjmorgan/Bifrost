package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/gateway"
	"github.com/andyjmorgan/Bifrost/internal/ruletext"
	"github.com/andyjmorgan/Bifrost/internal/store"
	"github.com/andyjmorgan/Bifrost/pkg/hooks"
)

// A server's rules, selection and grants are edited on the server itself
// (servers.go). This file has what the rule editor and the member portal
// need besides: checking rules, the hook reference, the catalog a server
// has been seen to offer, and the servers a member holds.

// GrantDTO gives a user or a role the server.
type GrantDTO struct {
	SubjectType string    `json:"subjectType"` // "user" | "role"
	SubjectID   string    `json:"subjectId"`   // user id, or "admin" | "member"
	Email       string    `json:"email,omitempty"`
	Name        string    `json:"name,omitempty"`
	GrantedAt   time.Time `json:"grantedAt"`
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
// shows.
type PortalServerDTO struct {
	ID           string          `json:"id"`
	Slug         string          `json:"slug"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	MCPURL       string          `json:"mcpUrl"`
	URL          string          `json:"url"`
	Enabled      bool            `json:"enabled"`
	Selection    store.Selection `json:"selection"`
	Via          []string        `json:"via"`          // "user" or "role:member"
	Capabilities []string        `json:"capabilities"` // "kind:name" seen on the server and in the allow lists (rules not applied here)
	HasRules     bool            `json:"hasRules"`     // the server has rules on some hook
}

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

// grantDTOs names the users among a server's grants.
func grantDTOs(grants []store.Grant, members map[string]*store.Member) []GrantDTO {
	out := make([]GrantDTO, 0, len(grants))
	for _, g := range grants {
		gd := GrantDTO{SubjectType: g.SubjectType, SubjectID: g.SubjectID, GrantedAt: g.GrantedAt}
		if m := members[g.SubjectID]; g.SubjectType == "user" && m != nil {
			gd.Email, gd.Name = m.Email, m.Name
		}
		out = append(out, gd)
	}
	return out
}

// members returns the org's members by user id, for naming grants.
func (a *API) members(r *http.Request, org string) map[string]*store.Member {
	out := map[string]*store.Member{}
	id, err := uuid.Parse(org)
	if err != nil {
		return out
	}
	if ms, err := a.store.ListMembers(r.Context(), id); err == nil {
		for _, m := range ms {
			out[m.UserID] = m
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
	grants := make([]store.Grant, 0, len(in.Grants))
	for _, g := range in.Grants {
		switch {
		case g.SubjectType == "role" && (g.SubjectID == "admin" || g.SubjectID == "member"):
		case g.SubjectType == "user" && g.SubjectID != "":
		default:
			writeErr(w, http.StatusBadRequest, "grant must be role admin|member or a user id")
			return
		}
		grants = append(grants, store.Grant{SubjectType: g.SubjectType, SubjectID: g.SubjectID})
	}
	if err := a.store.SetGrants(r.Context(), c.OrgID, s.ID, grants, c.UserID); err != nil {
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
	caps, err := a.store.ListCapabilities(r.Context(), s.OrgID, s.ID)
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
	c := auth.CallerFrom(r.Context())
	held, _, err := a.store.ListGrantedWithCache(r.Context(), c.OrgID, c.UserID, string(c.Role))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	all, err := a.store.ListServers(r.Context(), c.OrgID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	grants := map[uuid.UUID][]store.Grant{}
	for _, s := range all {
		grants[s.ID] = s.Grants
	}
	out := make([]PortalServerDTO, 0, len(held))
	for _, s := range held {
		d := PortalServerDTO{ID: s.ID.String(), Slug: s.Slug, Name: s.Name, Description: s.Description, MCPURL: a.publicBase + "/" + s.Slug + "/mcp", URL: s.URL,
			Enabled: s.Enabled, Selection: s.Selection, Via: []string{}, Capabilities: []string{}}
		if hs, err := ruletext.ParseStored(s.Hooks); err != nil || !hs.Empty() {
			d.HasRules = true
		}
		for _, g := range grants[s.ID] {
			if g.SubjectType == "user" && g.SubjectID == c.UserID {
				d.Via = append(d.Via, "user")
			}
			if g.SubjectType == "role" && g.SubjectID == string(c.Role) {
				d.Via = append(d.Via, "role:"+g.SubjectID)
			}
		}
		if caps, err := a.store.ListCapabilities(r.Context(), c.OrgID, s.ID); err == nil {
			for _, cp := range caps {
				if allowsKind(s, cp.Kind, cp.Name) {
					d.Capabilities = append(d.Capabilities, cp.Kind+":"+cp.Name)
				}
			}
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, out)
}

// allowsKind mirrors the gateway's cut for the portal listing (resources
// by exact pattern match only; the gateway also matches globs).
func allowsKind(s *store.Server, kind, name string) bool {
	list, ok := s.Selection[store.SelectionKey(kind)]
	if !ok {
		return true
	}
	for _, x := range list {
		if x == name {
			return true
		}
	}
	return false
}

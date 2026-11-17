package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/gateway"
	"github.com/andyjmorgan/Bifrost/internal/ruletext"
	"github.com/andyjmorgan/Bifrost/internal/slug"
	"github.com/andyjmorgan/Bifrost/internal/store"
	"github.com/andyjmorgan/Bifrost/pkg/hooks"
)

// Entitlements: the grantable views of a server, each under its own slug.
// Admins create them, pick what they expose and grant them; members see
// what they hold through /exposures.

// EntitlementDTO is one entitlement with its grants.
type EntitlementDTO struct {
	ID          string          `json:"id"`
	ServerID    string          `json:"serverId"`
	ServerName  string          `json:"serverName"`
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	MCPURL      string          `json:"mcpUrl"`
	Selection   store.Selection `json:"selection"`
	// Rules is the rules as the admin writes them: the text of each hook,
	// under the hook's property (see ruletext.Rules). Every property is
	// present; null means the hook has no rules.
	Rules ruletext.Rules `json:"rules"`
	// RulesError says why the stored rules cannot be used. While it is set
	// the entitlement exposes nothing.
	RulesError string     `json:"rulesError,omitempty"`
	Generation int        `json:"generation"`
	Grants     []GrantDTO `json:"grants"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// GrantDTO gives a user or a role the entitlement.
type GrantDTO struct {
	SubjectType string    `json:"subjectType"` // "user" | "role"
	SubjectID   string    `json:"subjectId"`   // user id, or "admin" | "member"
	Email       string    `json:"email,omitempty"`
	Name        string    `json:"name,omitempty"`
	GrantedAt   time.Time `json:"grantedAt"`
}

// EntitlementInput creates or updates an entitlement. Slug is only read on
// create; when empty one is derived from the name. Rules nil leaves them.
type EntitlementInput struct {
	Name        string           `json:"name"`
	Slug        string           `json:"slug"`
	Description string           `json:"description"`
	Selection   *store.Selection `json:"selection"`
	// Rules sets the rules: text by hook property. Nil leaves them as they
	// are; a null property clears that hook's rules.
	Rules *ruletext.Rules `json:"rules"`
}

// TextProblems is the 400 body when rules do not compile.
type TextProblems struct {
	Error    string             `json:"error"`
	Problems []ruletext.Problem `json:"textProblems"`
}

// rulesFrom turns the rules of a create or update into what is stored. It
// writes the 400 itself and returns false when they do not compile.
func rulesFrom(w http.ResponseWriter, in *EntitlementInput) (json.RawMessage, bool) {
	if _, probs := ruletext.Compile(in.Rules.ByHook()); len(probs) > 0 {
		writeJSON(w, http.StatusBadRequest, TextProblems{Error: "rules do not compile", Problems: probs})
		return nil, false
	}
	return in.Rules.Stored(), true
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

// ExposureDTO is one slug the caller holds: what the member portal shows.
type ExposureDTO struct {
	Slug         string          `json:"slug"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	MCPURL       string          `json:"mcpUrl"`
	ServerID     string          `json:"serverId"`
	ServerName   string          `json:"serverName"`
	ServerURL    string          `json:"serverUrl"`
	Enabled      bool            `json:"enabled"`
	Selection    store.Selection `json:"selection"`
	Via          []string        `json:"via"` // "user" or "role:member"
	Conflict     string          `json:"conflict,omitempty"`
	Capabilities []string        `json:"capabilities"` // "kind:name" seen on the server and in the allow lists (rules not applied here)
	HasRules     bool            `json:"hasRules"`     // the entitlement has rules on some hook
}

func (a *API) entitlementDTO(r *http.Request, e *store.Entitlement, serverName string) EntitlementDTO {
	d := EntitlementDTO{ID: e.ID.String(), ServerID: e.ServerID.String(), ServerName: serverName, Slug: e.Slug, Name: e.Name, Description: e.Description,
		MCPURL: a.publicBase + "/" + e.Slug + "/mcp", Selection: e.Selection, Generation: e.Generation, Grants: []GrantDTO{}, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt}
	if rs, err := ruletext.ParseStored(e.Rules); err != nil {
		d.RulesError = err.Error()
	} else {
		d.Rules = rs
		if _, probs := ruletext.Compile(rs.ByHook()); len(probs) > 0 {
			d.RulesError = ruletext.ProblemsError(probs).Error()
		}
	}
	members := map[string]*store.Member{}
	if ms, err := a.store.ListMembers(r.Context(), uuid.MustParse(e.OrgID)); err == nil {
		for _, m := range ms {
			members[m.UserID] = m
		}
	}
	for _, g := range e.Grants {
		gd := GrantDTO{SubjectType: g.SubjectType, SubjectID: g.SubjectID, GrantedAt: g.GrantedAt}
		if m := members[g.SubjectID]; g.SubjectType == "user" && m != nil {
			gd.Email, gd.Name = m.Email, m.Name
		}
		d.Grants = append(d.Grants, gd)
	}
	return d
}

func (a *API) serverNames(r *http.Request, org string) map[uuid.UUID]string {
	out := map[uuid.UUID]string{}
	if servers, err := a.store.ListServers(r.Context(), org); err == nil {
		for _, s := range servers {
			out[s.ID] = s.Name
		}
	}
	return out
}

func (a *API) listEntitlements(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	ents, err := a.store.ListEntitlements(r.Context(), c.OrgID, nil)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	names := a.serverNames(r, c.OrgID)
	out := make([]EntitlementDTO, 0, len(ents))
	for _, e := range ents {
		out = append(out, a.entitlementDTO(r, e, names[e.ServerID]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) listServerEntitlements(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loadServer(w, r)
	if !ok {
		return
	}
	ents, err := a.store.ListEntitlements(r.Context(), s.OrgID, &s.ID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := make([]EntitlementDTO, 0, len(ents))
	for _, e := range ents {
		out = append(out, a.entitlementDTO(r, e, s.Name))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getEntitlement(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	e, err := a.store.GetEntitlement(r.Context(), c.OrgID, id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.entitlementDTO(r, e, a.serverNames(r, c.OrgID)[e.ServerID]))
}

func (a *API) createEntitlement(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	s, ok := a.loadServer(w, r)
	if !ok {
		return
	}
	var in EntitlementInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	e := &store.Entitlement{ID: uuid.New(), OrgID: c.OrgID, ServerID: s.ID, Name: in.Name, Description: in.Description, Selection: store.Selection{}}
	if in.Selection != nil {
		e.Selection = *in.Selection
	}
	if in.Rules != nil {
		stored, ok := rulesFrom(w, &in)
		if !ok {
			return
		}
		e.Rules = stored
	}
	if in.Slug != "" {
		e.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
		if !slug.Valid(e.Slug) || e.Slug == "mcp" {
			writeErr(w, http.StatusBadRequest, "slug must be 1-24 lowercase letters, digits or hyphens")
			return
		}
		if err := a.store.CreateEntitlement(r.Context(), e); err != nil {
			if errors.Is(err, store.ErrConflict) {
				writeErr(w, http.StatusConflict, "slug already in use")
				return
			}
			writeStoreErr(w, err)
			return
		}
	} else {
		var created bool
		slug.Candidates(slug.Base(in.Name), func(cand string) bool {
			e.Slug = cand
			err := a.store.CreateEntitlement(r.Context(), e)
			if errors.Is(err, store.ErrConflict) {
				return false
			}
			created = err == nil
			return true
		})
		if !created {
			writeErr(w, http.StatusInternalServerError, "could not allocate a slug")
			return
		}
	}
	e.Grants = []store.Grant{}
	writeJSON(w, http.StatusCreated, a.entitlementDTO(r, e, s.Name))
}

func (a *API) updateEntitlement(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	e, err := a.store.GetEntitlement(r.Context(), c.OrgID, id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	var in EntitlementInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if n := strings.TrimSpace(in.Name); n != "" {
		e.Name = n
	}
	e.Description = in.Description
	if in.Selection != nil {
		e.Selection = *in.Selection
	}
	if in.Rules != nil {
		stored, ok := rulesFrom(w, &in)
		if !ok {
			return
		}
		e.Rules = stored
	}
	if err := a.store.UpdateEntitlement(r.Context(), e); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.entitlementDTO(r, e, a.serverNames(r, c.OrgID)[e.ServerID]))
}

// validateRules checks rules without saving them: text by stage, or a rule
// list. Text is compiled by the engine's compiler, which is what decides
// whether it can be saved, and then looked over for rules that compile but
// look wrong; those come back as warnings.
func (a *API) validateRules(w http.ResponseWriter, r *http.Request) {
	var in struct {
		// Rules is the text to check, by hook property.
		Rules ruletext.Rules `json:"rules"`
		// Catalog is the names the server is known to offer, by kind. With
		// it, a rule that names something the server does not have is
		// warned about.
		Catalog map[string][]string `json:"catalog"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	text := in.Rules.ByHook()
	rules, probs := ruletext.Compile(text)
	if probs == nil {
		probs = []ruletext.Problem{}
	}
	warnings := ruletext.Lint(text, in.Catalog)
	if warnings == nil {
		warnings = []ruletext.Problem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"textProblems": probs, "warnings": warnings, "hasRules": len(rules) > 0})
}

// rulesSchema describes, for every hook, the variables a rule can read and
// the fields it can set, with the request flags. The rule editor builds its
// reference and its completions from this, so it cannot drift from the
// engine.
func (a *API) rulesSchema(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"flags": hooks.FlagDocs, "caller": gateway.CallerKeys, "hooks": hooks.Schema(), "stages": ruletext.Stages()})
}

func (a *API) deleteEntitlement(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := a.store.DeleteEntitlement(r.Context(), c.OrgID, id); err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) putGrants(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
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
	if err := a.store.SetGrants(r.Context(), c.OrgID, id, grants, c.UserID); err != nil {
		writeStoreErr(w, err)
		return
	}
	a.getEntitlement(w, r)
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

// listExposures returns the slugs the caller holds, with what each exposes.
func (a *API) listExposures(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	servers, _, err := a.store.ListExposuresWithCache(r.Context(), c.OrgID, c.UserID, string(c.Role))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	count := map[uuid.UUID][]string{}
	for _, s := range servers {
		if s.Exclusive {
			count[s.ID] = append(count[s.ID], s.Slug)
		}
	}
	out := make([]ExposureDTO, 0, len(servers))
	for _, s := range servers {
		d := ExposureDTO{Slug: s.Slug, Name: s.EntitlementName, MCPURL: a.publicBase + "/" + s.Slug + "/mcp", ServerID: s.ID.String(), ServerName: s.Name, ServerURL: s.URL,
			Enabled: s.Enabled, Selection: s.Selection, Via: []string{}, Capabilities: []string{}}
		if e, err := a.store.GetEntitlement(r.Context(), c.OrgID, s.EntitlementID); err == nil {
			d.Description = e.Description
			if rs, err := ruletext.ParseStored(e.Rules); err != nil || !rs.Empty() {
				d.HasRules = true
			}
			for _, g := range e.Grants {
				if g.SubjectType == "user" && g.SubjectID == c.UserID {
					d.Via = append(d.Via, "user")
				}
				if g.SubjectType == "role" && g.SubjectID == string(c.Role) {
					d.Via = append(d.Via, "role:"+g.SubjectID)
				}
			}
		}
		if slugs := count[s.ID]; len(slugs) > 1 {
			d.Conflict = "entitlements conflict: " + strings.Join(slugs, ", ") + " on an exclusive server"
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

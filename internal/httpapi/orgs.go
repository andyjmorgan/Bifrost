package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/store"
)

// Orgs: the caller's active org, its members and invites, and the join and
// leave flows. Every route here is for humans; API keys act inside an org
// but cannot move between them.

// OrgDTO is the caller's active org as the SPA sees it.
type OrgDTO struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Personal   bool        `json:"personal"` // the caller's own personal org
	Role       string      `json:"role"`
	Members    []MemberDTO `json:"members"`
	Orgs       []OrgRefDTO `json:"orgs"` // every org the caller belongs to
	InviteBase string      `json:"inviteBase"`
}

// OrgRefDTO names one of the caller's orgs.
type OrgRefDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Personal bool   `json:"personal"`
	Active   bool   `json:"active"`
}

// MemberDTO is one member of the active org.
type MemberDTO struct {
	UserID   string    `json:"userId"`
	Email    string    `json:"email"`
	Name     string    `json:"name"`
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joinedAt"`
	Self     bool      `json:"self"`
}

// InviteDTO is a magic link. URL is only present on the create response.
type InviteDTO struct {
	ID        string     `json:"id"`
	Role      string     `json:"role"`
	CreatedBy string     `json:"createdBy"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt time.Time  `json:"expiresAt"`
	UsedAt    *time.Time `json:"usedAt,omitempty"`
	UsedBy    string     `json:"usedBy,omitempty"`
	URL       string     `json:"url,omitempty"`
}

// InvitePreviewDTO is what a visitor sees before accepting.
type InvitePreviewDTO struct {
	OrgName   string    `json:"orgName"`
	Role      string    `json:"role"`
	ExpiresAt time.Time `json:"expiresAt"`
	Member    bool      `json:"member"` // the caller already belongs to this org
}

const inviteTTL = 7 * 24 * time.Hour

func (a *API) orgDTO(r *http.Request, c *auth.Caller) (*OrgDTO, error) {
	orgID, err := uuid.Parse(c.OrgID)
	if err != nil {
		return nil, err
	}
	out := &OrgDTO{ID: c.OrgID, Name: c.OrgName, Personal: c.PersonalOrg, Role: string(c.Role), Members: []MemberDTO{}, Orgs: []OrgRefDTO{}, InviteBase: a.publicBase + "/join/"}
	// The member list is the admin's; a member sees only themselves.
	members, err := a.store.ListMembers(r.Context(), orgID)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		if !c.IsAdmin() && m.UserID != c.UserID {
			continue
		}
		out.Members = append(out.Members, MemberDTO{UserID: m.UserID, Email: m.Email, Name: m.Name, Role: string(m.Role), JoinedAt: m.JoinedAt, Self: m.UserID == c.UserID})
	}
	if c.UserID != "" {
		ms, orgs, err := a.store.ListUserOrgs(r.Context(), c.UserID)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			o := orgs[m.OrgID]
			out.Orgs = append(out.Orgs, OrgRefDTO{ID: o.ID.String(), Name: o.Name, Role: string(m.Role), Personal: o.PersonalFor == c.UserID, Active: o.ID == orgID})
		}
	}
	return out, nil
}

func (a *API) getOrg(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	dto, err := a.orgDTO(r, c)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (a *API) renameOrg(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 80 {
		writeErr(w, http.StatusBadRequest, "name must be 1-80 characters")
		return
	}
	if err := a.store.RenameOrg(r.Context(), uuid.MustParse(c.OrgID), in.Name); err != nil {
		writeStoreErr(w, err)
		return
	}
	a.forgetOrg(r, c)
	c.OrgName = in.Name
	dto, err := a.orgDTO(r, c)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// forgetOrg drops every member's cached standing so a change is seen at once.
func (a *API) forgetOrg(r *http.Request, c *auth.Caller) {
	members, err := a.store.ListMembers(r.Context(), uuid.MustParse(c.OrgID))
	if err != nil {
		return
	}
	for _, m := range members {
		a.authn.Forget(m.UserID)
	}
	a.authn.Forget(c.UserID)
}

func (a *API) setMemberRole(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	var in struct {
		Role string `json:"role"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	role := store.Role(in.Role)
	if role != store.RoleAdmin && role != store.RoleMember {
		writeErr(w, http.StatusBadRequest, "role must be admin or member")
		return
	}
	userID := chi.URLParam(r, "userId")
	if err := a.store.SetMemberRole(r.Context(), uuid.MustParse(c.OrgID), userID, role); err != nil {
		writeOrgErr(w, err)
		return
	}
	a.authn.Forget(userID)
	a.getOrg(w, r)
}

func (a *API) removeMember(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	userID := chi.URLParam(r, "userId")
	if _, err := a.store.RemoveMember(r.Context(), uuid.MustParse(c.OrgID), userID); err != nil {
		writeOrgErr(w, err)
		return
	}
	a.authn.Forget(userID)
	if userID == c.UserID {
		// An admin removed themselves: they are now in their personal org.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	a.getOrg(w, r)
}

// leaveOrg drops the caller from the active org and lands them in their
// personal org.
func (a *API) leaveOrg(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	if c.Type == auth.ActorAPIKey {
		writeErr(w, http.StatusForbidden, "keys cannot leave an org")
		return
	}
	st, err := a.store.RemoveMember(r.Context(), uuid.MustParse(c.OrgID), c.UserID)
	if err != nil {
		writeOrgErr(w, err)
		return
	}
	a.authn.Forget(c.UserID)
	writeJSON(w, http.StatusOK, a.standingDTO(r, c, st))
}

// switchOrg makes another of the caller's orgs active.
func (a *API) switchOrg(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	if c.Type == auth.ActorAPIKey {
		writeErr(w, http.StatusForbidden, "keys cannot switch org")
		return
	}
	var in struct {
		OrgID string `json:"orgId"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := uuid.Parse(in.OrgID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "orgId must be a uuid")
		return
	}
	st, err := a.store.SwitchOrg(r.Context(), c.UserID, id)
	if err != nil {
		writeOrgErr(w, err)
		return
	}
	a.authn.Forget(c.UserID)
	writeJSON(w, http.StatusOK, a.standingDTO(r, c, st))
}

func (a *API) standingDTO(r *http.Request, c *auth.Caller, st *store.Standing) *OrgDTO {
	cc := *c
	cc.OrgID = st.Org.ID.String()
	cc.OrgName = st.Org.Name
	cc.PersonalOrg = st.Org.PersonalFor == c.UserID
	cc.Role = auth.Role(st.Role)
	dto, err := a.orgDTO(r, &cc)
	if err != nil {
		return &OrgDTO{ID: cc.OrgID, Name: cc.OrgName, Personal: cc.PersonalOrg, Role: string(cc.Role), Members: []MemberDTO{}, Orgs: []OrgRefDTO{}}
	}
	return dto
}

// ---- invites ----------------------------------------------------------------

func (a *API) listInvites(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	invs, err := a.store.ListInvites(r.Context(), uuid.MustParse(c.OrgID))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := make([]InviteDTO, 0, len(invs))
	for _, i := range invs {
		out = append(out, inviteDTO(i))
	}
	writeJSON(w, http.StatusOK, out)
}

func inviteDTO(i *store.Invite) InviteDTO {
	return InviteDTO{ID: i.ID.String(), Role: string(i.Role), CreatedBy: i.CreatedBy, CreatedAt: i.CreatedAt, ExpiresAt: i.ExpiresAt, UsedAt: i.UsedAt, UsedBy: i.UsedBy}
}

func (a *API) createInvite(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	var in struct {
		Role string `json:"role"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	role := store.RoleMember
	if in.Role == string(store.RoleAdmin) {
		role = store.RoleAdmin
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	inv := &store.Invite{ID: uuid.New(), OrgID: uuid.MustParse(c.OrgID), Role: role, CreatedBy: c.UserID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(inviteTTL)}
	if err := a.store.CreateInvite(r.Context(), inv, auth.Hash(token)); err != nil {
		writeStoreErr(w, err)
		return
	}
	dto := inviteDTO(inv)
	dto.URL = a.publicBase + "/join/" + token
	writeJSON(w, http.StatusCreated, dto)
}

func (a *API) deleteInvite(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := a.store.DeleteInvite(r.Context(), uuid.MustParse(c.OrgID), id); err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// previewInvite describes the org behind a token so the join page can ask
// before switching the caller over.
func (a *API) previewInvite(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	token := chi.URLParam(r, "token")
	inv, org, err := a.store.PeekInvite(r.Context(), auth.Hash(token))
	if err != nil {
		writeOrgErr(w, err)
		return
	}
	member := false
	if _, err := a.store.MemberRole(r.Context(), inv.OrgID, c.UserID); err == nil {
		member = true
	}
	writeJSON(w, http.StatusOK, InvitePreviewDTO{OrgName: org.Name, Role: string(inv.Role), ExpiresAt: inv.ExpiresAt, Member: member})
}

func (a *API) joinOrg(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	if c.Type == auth.ActorAPIKey {
		writeErr(w, http.StatusForbidden, "keys cannot join an org")
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Token == "" {
		writeErr(w, http.StatusBadRequest, "token is required")
		return
	}
	st, err := a.store.AcceptInvite(r.Context(), auth.Hash(in.Token), c.UserID)
	if err != nil {
		writeOrgErr(w, err)
		return
	}
	a.authn.Forget(c.UserID)
	writeJSON(w, http.StatusOK, a.standingDTO(r, c, st))
}

func writeOrgErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInviteInvalid):
		writeErr(w, http.StatusNotFound, "this invite is invalid, used or expired")
	case errors.Is(err, store.ErrLastAdmin):
		writeErr(w, http.StatusConflict, "the org needs at least one admin")
	case errors.Is(err, store.ErrPersonalOrg):
		writeErr(w, http.StatusConflict, "you cannot leave your personal org")
	case errors.Is(err, store.ErrNotMember):
		writeErr(w, http.StatusNotFound, "not a member of this org")
	default:
		writeStoreErr(w, err)
	}
}

package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/store"
)

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	scopes := c.Scopes
	if c.Type != auth.ActorAPIKey {
		scopes = auth.AllScopes
	}
	writeJSON(w, http.StatusOK, MeDTO{UserID: c.UserID, OrgID: c.OrgID, OrgName: c.OrgName, PersonalOrg: c.PersonalOrg, Role: string(c.Role), Subject: c.Subject, Type: string(c.Type), Email: c.Email, Name: c.Name, Scopes: scopes})
}

func (a *API) config(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.cfgDTO)
}

func (a *API) listKeys(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	keys, err := a.store.ListAPIKeys(r.Context(), c.OrgID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := make([]APIKeyDTO, 0, len(keys))
	for _, k := range keys {
		if !c.IsAdmin() && k.CreatedBy != c.UserID {
			continue
		}
		out = append(out, keyDTO(k))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) createKey(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	var in CreateAPIKeyInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	rec, secret, err := a.keys.Create(r.Context(), c.OrgID, c.UserID, in.Name, in.Scopes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, CreateAPIKeyResult{Key: keyDTO(rec), Secret: secret})
}

func (a *API) revokeKey(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if !c.IsAdmin() {
		k, err := a.store.GetAPIKey(r.Context(), c.OrgID, id)
		if err != nil || k.CreatedBy != c.UserID {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
	}
	if err := a.store.RevokeAPIKey(r.Context(), c.OrgID, id); err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listAudit(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	q := r.URL.Query()
	query := store.AuditQuery{OrgID: c.OrgID, Slug: q.Get("slug"), Hop: store.Hop(q.Get("hop")), McpMethod: q.Get("method"),
		ToolName: q.Get("tool"), Outcome: store.Outcome(q.Get("outcome")), RequestID: q.Get("requestId")}
	query.Offset, _ = strconv.Atoi(q.Get("offset"))
	query.Limit, _ = strconv.Atoi(q.Get("limit"))
	if sid := q.Get("serverId"); sid != "" {
		if id, err := uuid.Parse(sid); err == nil {
			query.ServerID = &id
		}
	}
	page, err := a.store.ListAuditEvents(r.Context(), query)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := PageDTO[AuditEventDTO]{Items: make([]AuditEventDTO, 0, len(page.Items)), Total: page.Total, Offset: page.Offset, Limit: page.Limit}
	for _, e := range page.Items {
		out.Items = append(out.Items, auditDTO(e))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getAuditTrace(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	root, spans, err := a.store.GetAuditTrace(r.Context(), c.OrgID, id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := AuditTraceDTO{Spans: make([]AuditEventDTO, 0, len(spans))}
	if root != nil {
		d := auditDTO(root)
		out.Root = &d
	}
	for _, s := range spans {
		out.Spans = append(out.Spans, auditDTO(s))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getAudit(w http.ResponseWriter, r *http.Request) {
	c := auth.CallerFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	e, p, err := a.store.GetAuditEvent(r.Context(), c.OrgID, id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := AuditDetailDTO{Event: auditDTO(e)}
	if p != nil {
		out.Payload = &AuditPayloadDTO{RequestHeaders: p.RequestHeaders, RequestBody: p.RequestBody, ResponseHeaders: p.ResponseHeaders, ResponseBody: p.ResponseBody}
	}
	writeJSON(w, http.StatusOK, out)
}

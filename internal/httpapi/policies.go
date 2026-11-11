package httpapi

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/policy"
	"github.com/andyjmorgan/Bifrost/internal/store"
)

// ---- DTOs -------------------------------------------------------------------

type PolicyBindingRef struct {
	Route      string `json:"route"`
	ServerID   string `json:"serverId,omitempty"`
	ServerName string `json:"serverName,omitempty"`
}

type PolicyDTO struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Invert      bool               `json:"invert"`
	Rules       []policy.Rule      `json:"rules"`
	CreatedAt   time.Time          `json:"createdAt"`
	UpdatedAt   time.Time          `json:"updatedAt"`
	Bindings    []PolicyBindingRef `json:"bindings"`
}

type PolicyInput struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Invert      bool          `json:"invert"`
	Rules       []policy.Rule `json:"rules"`
}

type PolicyPreviewInput struct {
	Policy PolicyInput `json:"policy"`
	Route  string      `json:"route"`
	Extra  bool        `json:"extra"`
}

type PolicyPreviewTool struct {
	Server         string       `json:"server"`
	Upstream       string       `json:"upstream"`
	Name           string       `json:"name"`
	Visible        bool         `json:"visible"`
	Confirm        bool         `json:"confirm"`
	ConfirmMessage string       `json:"confirmMessage,omitempty"`
	Changed        []string     `json:"changed"`
	Hits           []policy.Hit `json:"hits"`
	Tool           *mcp.Tool    `json:"tool"`
}

type PolicyPreviewResult struct {
	Route       string              `json:"route"`
	Tools       []PolicyPreviewTool `json:"tools"`
	Unavailable []unavailableDTO    `json:"unavailable,omitempty"`
}

type unavailableDTO struct {
	Server string `json:"server"`
	Error  string `json:"error"`
}

type RouteBindingsDTO struct {
	Route      string      `json:"route"`
	ServerID   string      `json:"serverId,omitempty"`
	ServerName string      `json:"serverName,omitempty"`
	Policies   []policyRef `json:"policies"`
}

type policyRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ---- helpers ----------------------------------------------------------------

// routeBindings maps every route (mux + each slug) to its ordered policies.
func (a *API) routeBindings(r *http.Request, owner string) ([]RouteBindingsDTO, map[uuid.UUID][]PolicyBindingRef, error) {
	bindings, err := a.store.ListBindings(r.Context(), owner)
	if err != nil {
		return nil, nil, err
	}
	servers, err := a.store.ListServers(r.Context(), owner)
	if err != nil {
		return nil, nil, err
	}
	byID := map[uuid.UUID]*store.Server{}
	for _, s := range servers {
		byID[s.ID] = s
	}
	routes := map[string]*RouteBindingsDTO{}
	perPolicy := map[uuid.UUID][]PolicyBindingRef{}
	for _, b := range bindings {
		ref := PolicyBindingRef{Route: "mux"}
		if b.ServerID != nil {
			s := byID[*b.ServerID]
			if s == nil {
				continue
			}
			ref = PolicyBindingRef{Route: s.Slug, ServerID: s.ID.String(), ServerName: s.Name}
		}
		rt := routes[ref.Route]
		if rt == nil {
			rt = &RouteBindingsDTO{Route: ref.Route, ServerID: ref.ServerID, ServerName: ref.ServerName, Policies: []policyRef{}}
			routes[ref.Route] = rt
		}
		rt.Policies = append(rt.Policies, policyRef{ID: b.Policy.ID.String(), Name: b.Policy.Name})
		perPolicy[b.Policy.ID] = append(perPolicy[b.Policy.ID], ref)
	}
	out := make([]RouteBindingsDTO, 0, len(routes))
	for _, rt := range routes {
		out = append(out, *rt)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Route == "mux" || out[j].Route == "mux" {
			return out[i].Route == "mux"
		}
		return out[i].Route < out[j].Route
	})
	return out, perPolicy, nil
}

func policyDTO(p *policy.Policy, refs []PolicyBindingRef) PolicyDTO {
	if refs == nil {
		refs = []PolicyBindingRef{}
	}
	rules := p.Rules
	if rules == nil {
		rules = []policy.Rule{}
	}
	return PolicyDTO{ID: p.ID.String(), Name: p.Name, Description: p.Description, Invert: p.Invert, Rules: rules,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, Bindings: refs}
}

// writeProblems answers a policy that does not compile: 400 with the
// problems located to rule and field.
func writeProblems(w http.ResponseWriter, err error) bool {
	var ps policy.Problems
	if !errors.As(err, &ps) {
		return false
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": "policy does not compile", "problems": ps})
	return true
}

func (in PolicyInput) apply(p *policy.Policy) error {
	p.Name = strings.TrimSpace(in.Name)
	if p.Name == "" {
		return errors.New("name is required")
	}
	p.Description = strings.TrimSpace(in.Description)
	p.Invert = in.Invert
	p.Rules = in.Rules
	if p.Rules == nil {
		p.Rules = []policy.Rule{}
	}
	for i := range p.Rules {
		if p.Rules[i].ID == "" {
			p.Rules[i].ID = uuid.NewString()
		}
		if p.Rules[i].Then == nil {
			p.Rules[i].Then = map[string]string{}
		}
	}
	return nil
}

func (a *API) loadPolicy(w http.ResponseWriter, r *http.Request) (*policy.Policy, bool) {
	caller := auth.CallerFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return nil, false
	}
	p, err := a.store.GetPolicy(r.Context(), caller.OwnerID, id)
	if err != nil {
		writeStoreErr(w, err)
		return nil, false
	}
	return p, true
}

// ---- handlers ---------------------------------------------------------------

func (a *API) listPolicies(w http.ResponseWriter, r *http.Request) {
	caller := auth.CallerFrom(r.Context())
	ps, err := a.store.ListPolicies(r.Context(), caller.OwnerID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	_, refs, err := a.routeBindings(r, caller.OwnerID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := make([]PolicyDTO, 0, len(ps))
	for _, p := range ps {
		out = append(out, policyDTO(p, refs[p.ID]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getPolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := a.loadPolicy(w, r)
	if !ok {
		return
	}
	_, refs, err := a.routeBindings(r, p.OwnerID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, policyDTO(p, refs[p.ID]))
}

func (a *API) policyFields(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, policy.Fields)
}

func (a *API) createPolicy(w http.ResponseWriter, r *http.Request) {
	caller := auth.CallerFrom(r.Context())
	var in PolicyInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	p := &policy.Policy{OwnerID: caller.OwnerID}
	if err := in.apply(p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := policy.Compile(p); err != nil {
		if !writeProblems(w, err) {
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if err := a.store.CreatePolicy(r.Context(), p); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, http.StatusConflict, "a policy with that name exists")
			return
		}
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, policyDTO(p, nil))
}

func (a *API) updatePolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := a.loadPolicy(w, r)
	if !ok {
		return
	}
	var in PolicyInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := in.apply(p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := policy.Compile(p); err != nil {
		if !writeProblems(w, err) {
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if err := a.store.UpdatePolicy(r.Context(), p); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, http.StatusConflict, "a policy with that name exists")
			return
		}
		writeStoreErr(w, err)
		return
	}
	_, refs, err := a.routeBindings(r, p.OwnerID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, policyDTO(p, refs[p.ID]))
}

func (a *API) deletePolicy(w http.ResponseWriter, r *http.Request) {
	p, ok := a.loadPolicy(w, r)
	if !ok {
		return
	}
	if err := a.store.DeletePolicy(r.Context(), p.OwnerID, p.ID); err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validatePolicy compiles rules without saving; always 200 with problems.
func (a *API) validatePolicy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Rules []policy.Rule `json:"rules"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	problems := policy.Problems{}
	if _, err := policy.Compile(&policy.Policy{Rules: in.Rules}); err != nil {
		var ps policy.Problems
		if errors.As(err, &ps) {
			problems = ps
		} else {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"problems": problems})
}

// previewPolicy evaluates an unsaved policy against a route's tools.
func (a *API) previewPolicy(w http.ResponseWriter, r *http.Request) {
	caller := auth.CallerFrom(r.Context())
	var in PolicyPreviewInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	route := strings.ToLower(strings.TrimSpace(in.Route))
	if route == "" {
		route = "mux"
	}
	p := &policy.Policy{OwnerID: caller.OwnerID, Name: in.Policy.Name}
	if p.Name == "" {
		p.Name = "preview"
	}
	p.Description, p.Invert, p.Rules = in.Policy.Description, in.Policy.Invert, in.Policy.Rules
	servers, err := a.gw.Preview(r.Context(), caller, route, p, in.Extra)
	if err != nil {
		if writeProblems(w, err) {
			return
		}
		writeStoreErr(w, err)
		return
	}
	out := PolicyPreviewResult{Route: route, Tools: []PolicyPreviewTool{}}
	for _, s := range servers {
		if s.Err != nil {
			out.Unavailable = append(out.Unavailable, unavailableDTO{Server: s.Slug, Error: s.Err.Error()})
			continue
		}
		for _, d := range s.Decisions {
			name := d.Tool.Name
			if route == "mux" {
				name = s.Slug + "__" + name
			}
			changed := map[string]bool{}
			for _, h := range d.Hits {
				for _, f := range h.Set {
					changed[f] = true
				}
			}
			ch := make([]string, 0, len(changed))
			for f := range changed {
				ch = append(ch, f)
			}
			sort.Strings(ch)
			hits := d.Hits
			if hits == nil {
				hits = []policy.Hit{}
			}
			out.Tools = append(out.Tools, PolicyPreviewTool{Server: s.Slug, Upstream: d.Upstream, Name: name, Visible: d.Visible, Confirm: d.Confirm,
				ConfirmMessage: d.ConfirmMessage, Changed: ch, Hits: hits, Tool: d.Tool})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) listBindings(w http.ResponseWriter, r *http.Request) {
	caller := auth.CallerFrom(r.Context())
	routes, _, err := a.routeBindings(r, caller.OwnerID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, routes)
}

// resolveRoute turns "mux" or a slug into the binding key.
func (a *API) resolveRoute(w http.ResponseWriter, r *http.Request) (route string, srv *store.Server, ok bool) {
	caller := auth.CallerFrom(r.Context())
	route = strings.ToLower(chi.URLParam(r, "route"))
	if route == "mux" {
		return route, nil, true
	}
	s, err := a.store.GetServerBySlug(r.Context(), caller.OwnerID, route)
	if err != nil {
		writeStoreErr(w, err)
		return "", nil, false
	}
	return route, s, true
}

func (a *API) getBindings(w http.ResponseWriter, r *http.Request) {
	caller := auth.CallerFrom(r.Context())
	route, srv, ok := a.resolveRoute(w, r)
	if !ok {
		return
	}
	routes, _, err := a.routeBindings(r, caller.OwnerID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	for _, rt := range routes {
		if rt.Route == route {
			writeJSON(w, http.StatusOK, rt)
			return
		}
	}
	out := RouteBindingsDTO{Route: route, Policies: []policyRef{}}
	if srv != nil {
		out.ServerID, out.ServerName = srv.ID.String(), srv.Name
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) putBindings(w http.ResponseWriter, r *http.Request) {
	caller := auth.CallerFrom(r.Context())
	_, srv, ok := a.resolveRoute(w, r)
	if !ok {
		return
	}
	var in struct {
		PolicyIDs []string `json:"policyIds"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ids := make([]uuid.UUID, 0, len(in.PolicyIDs))
	for _, s := range in.PolicyIDs {
		id, err := uuid.Parse(s)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad policy id "+s)
			return
		}
		ids = append(ids, id)
	}
	var serverID *uuid.UUID
	if srv != nil {
		id := srv.ID
		serverID = &id
	}
	if err := a.store.SetBindings(r.Context(), caller.OwnerID, serverID, ids); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusBadRequest, "unknown policy id")
			return
		}
		writeStoreErr(w, err)
		return
	}
	a.getBindings(w, r)
}

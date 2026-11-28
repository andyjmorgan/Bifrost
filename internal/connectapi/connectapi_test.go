package connectapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/crypto"
	"github.com/andyjmorgan/Bifrost/internal/store"
	bifrostv1 "github.com/andyjmorgan/Bifrost/pkg/api/bifrost/v1"
	"github.com/andyjmorgan/Bifrost/pkg/api/bifrost/v1/bifrostv1connect"
)

// fakeStore holds users, keys, servers, their grants and catalogs in memory:
// enough for the Authenticator and the portal listing.
type fakeStore struct {
	mu      sync.Mutex
	users   map[string]*store.User
	keys    map[string]*store.APIKey // by hex of the hash
	servers []*store.Server
	grants  map[uuid.UUID][]string
	caps    map[uuid.UUID][]*store.Capability
	listErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{users: map[string]*store.User{}, keys: map[string]*store.APIKey{}, grants: map[uuid.UUID][]string{}, caps: map[uuid.UUID][]*store.Capability{}}
}

func (f *fakeStore) CreateAPIKey(_ context.Context, k *store.APIKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[string(k.SecretHash)] = k
	return nil
}

func (f *fakeStore) GetAPIKeyByHash(_ context.Context, h []byte) (*store.APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if k, ok := f.keys[string(h)]; ok && k.RevokedAt == nil {
		return k, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) GetAPIKey(_ context.Context, id uuid.UUID) (*store.APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.keys {
		if k.ID == id {
			return k, nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) EnsureUser(_ context.Context, id, email, name string) (*store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := &store.User{ID: id, Email: email, Name: name}
	f.users[id] = u
	return u, nil
}

func (f *fakeStore) GetUser(_ context.Context, id string) (*store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.users[id]; ok {
		return u, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) ListGrantedWithCache(_ context.Context, userID string, _ ...string) ([]*store.Server, map[uuid.UUID]store.CacheBundle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, nil, f.listErr
	}
	var out []*store.Server
	for _, s := range f.servers {
		for _, u := range f.grants[s.ID] {
			if u == userID {
				out = append(out, s)
			}
		}
	}
	return out, map[uuid.UUID]store.CacheBundle{}, nil
}

func (f *fakeStore) ListCapabilities(_ context.Context, id uuid.UUID) ([]*store.Capability, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.caps[id], nil
}

func (f *fakeStore) addServer(slug string, sel store.Selection, hooks string, users ...string) *store.Server {
	s := &store.Server{ID: uuid.New(), Slug: slug, Name: slug + " server", Description: "about " + slug, URL: "https://" + slug + ".example/mcp", Enabled: true, Selection: sel}
	if hooks != "" {
		s.Hooks = []byte(hooks)
	}
	f.servers = append(f.servers, s)
	f.grants[s.ID] = users
	return s
}

const (
	alice = "alice"
	bob   = "bob"
)

type rig struct {
	t    *testing.T
	st   *fakeStore
	keys *auth.Keys
	url  string
	logs *bytes.Buffer
}

// newRig serves the Connect API on a real loopback listener with an
// Authenticator that is not in dev mode: a request needs a credential.
func newRig(t *testing.T) *rig {
	t.Helper()
	st := newFakeStore()
	sealer, err := crypto.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	keys := auth.NewKeys(st, sealer)
	// A non-nil OIDC turns dev mode off. These tests present API keys only;
	// the OIDC verification itself is tested in internal/auth.
	authn := &auth.Authenticator{OIDC: &auth.OIDC{}, Keys: keys, Users: st, PublicBase: "https://gw.example"}
	logs := &bytes.Buffer{}
	api := New(Deps{Authn: authn, Store: st, PublicBase: "https://gw.example", Log: slog.New(slog.NewTextHandler(logs, nil))})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	addr, err := Serve(ctx, "127.0.0.1:0", api.Handler(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{alice, bob} {
		if _, err := st.EnsureUser(ctx, u, u+"@example.com", u); err != nil {
			t.Fatal(err)
		}
	}
	return &rig{t: t, st: st, keys: keys, url: "http://" + addr.String(), logs: logs}
}

func (r *rig) key(user string, scopes ...string) string {
	r.t.Helper()
	_, secret, err := r.keys.Create(context.Background(), user, user+"-key", scopes)
	if err != nil {
		r.t.Fatal(err)
	}
	return secret
}

func (r *rig) list(token string, opts ...connect.ClientOption) (*bifrostv1.ListServersResponse, error) {
	hc := http.DefaultClient
	if len(opts) > 0 {
		// gRPC needs HTTP/2; the listener speaks it in cleartext.
		var p http.Protocols
		p.SetUnencryptedHTTP2(true)
		hc = &http.Client{Transport: &http.Transport{Protocols: &p}}
	}
	c := bifrostv1connect.NewPortalServiceClient(hc, r.url, opts...)
	req := connect.NewRequest(&bifrostv1.ListServersRequest{})
	if token != "" {
		req.Header().Set("Authorization", "Bearer "+token)
	}
	res, err := c.ListServers(context.Background(), req)
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func wantCode(t *testing.T, err error, code connect.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %v, got success", code)
	}
	if got := connect.CodeOf(err); got != code {
		t.Fatalf("want %v, got %v (%v)", code, got, err)
	}
}

func TestAuthRequired(t *testing.T) {
	r := newRig(t)
	r.st.addServer("search", nil, "", alice)

	_, err := r.list("")
	wantCode(t, err, connect.CodeUnauthenticated)
	_, err = r.list("mcpg_notakey")
	wantCode(t, err, connect.CodeUnauthenticated)

	// A key whose user is gone no longer authenticates.
	ghost := r.key("ghost", auth.ScopeAPIRead)
	_, err = r.list(ghost)
	wantCode(t, err, connect.CodeUnauthenticated)

	// The same scope as GET /api/v1/portal: api:read.
	_, err = r.list(r.key(alice, auth.ScopeMCPExecute))
	wantCode(t, err, connect.CodePermissionDenied)

	if _, err := r.list(r.key(alice, auth.ScopeAPIRead)); err != nil {
		t.Fatal(err)
	}
}

func TestCallerSeesOnlyTheirServers(t *testing.T) {
	r := newRig(t)
	search := r.st.addServer("search", store.Selection{store.SelTools: {"find"}}, "", alice, bob)
	r.st.caps[search.ID] = []*store.Capability{
		{ServerID: search.ID, Kind: store.KindTool, Name: "find"},
		{ServerID: search.ID, Kind: store.KindTool, Name: "delete_index"}, // outside the allow list
		{ServerID: search.ID, Kind: store.KindResource, Name: "doc:a:b"},  // no resource list: allowed
	}
	r.st.addServer("notes", nil, `{"toolCall":"deny if true"}`, alice)
	r.st.addServer("billing", nil, "", bob)

	for _, tc := range []struct {
		user  string
		slugs []string
	}{{alice, []string{"search", "notes"}}, {bob, []string{"search", "billing"}}} {
		got, err := r.list(r.key(tc.user, auth.ScopeAPIRead))
		if err != nil {
			t.Fatal(err)
		}
		var slugs []string
		for _, s := range got.Servers {
			slugs = append(slugs, s.Slug)
		}
		if len(slugs) != len(tc.slugs) || slugs[0] != tc.slugs[0] || slugs[1] != tc.slugs[1] {
			t.Fatalf("%s sees %v, want %v", tc.user, slugs, tc.slugs)
		}
	}

	got, err := r.list(r.key(alice, auth.ScopeAPIRead))
	if err != nil {
		t.Fatal(err)
	}
	s := got.Servers[0]
	if s.Id != search.ID.String() || s.McpUrl != "https://gw.example/search/mcp" || s.Url != "https://search.example/mcp" || !s.Enabled || s.HasRules {
		t.Fatalf("search server: %+v", s)
	}
	if s.Selection.GetTools() == nil || len(s.Selection.Tools.Names) != 1 || s.Selection.Tools.Names[0] != "find" {
		t.Fatalf("tools allow list: %+v", s.Selection.GetTools())
	}
	if s.Selection.GetPrompts() != nil || s.Selection.GetResources() != nil || s.Selection.GetTemplates() != nil {
		t.Fatalf("kinds without a list must be unset (every name allowed): %+v", s.Selection)
	}
	if len(s.Capabilities) != 2 || s.Capabilities[0].Kind != "tool" || s.Capabilities[0].Name != "find" ||
		s.Capabilities[1].Kind != "resource" || s.Capabilities[1].Name != "doc:a:b" {
		t.Fatalf("capabilities: %+v", s.Capabilities)
	}
	if !got.Servers[1].HasRules {
		t.Fatalf("notes has rules: %+v", got.Servers[1])
	}

	// The same answer over gRPC, which needs the listener's cleartext HTTP/2.
	viaGRPC, err := r.list(r.key(alice, auth.ScopeAPIRead), connect.WithGRPC())
	if err != nil {
		t.Fatal(err)
	}
	if len(viaGRPC.Servers) != 2 {
		t.Fatalf("over gRPC: %v", viaGRPC.Servers)
	}
	_, err = r.list("", connect.WithGRPC())
	wantCode(t, err, connect.CodeUnauthenticated)
}

func TestStoreErrorIsInternalAndNotLeaked(t *testing.T) {
	r := newRig(t)
	r.st.listErr = errors.New("connection to 10.0.0.5 refused")
	_, err := r.list(r.key(alice, auth.ScopeAPIRead))
	wantCode(t, err, connect.CodeInternal)
	if bytes.Contains([]byte(err.Error()), []byte("10.0.0.5")) {
		t.Fatalf("store error leaked to the caller: %v", err)
	}
	if !bytes.Contains(r.logs.Bytes(), []byte("10.0.0.5")) {
		t.Fatalf("store error not logged: %s", r.logs)
	}
}

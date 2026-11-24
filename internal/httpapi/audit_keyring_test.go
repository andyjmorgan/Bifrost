package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/keyring"
	"github.com/andyjmorgan/Bifrost/internal/keyring/store/memstore"
)

// eventually polls until fn reports true. Both audit trails are written by
// background workers, a moment after the request they describe.
func eventually(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A credential's use by the gateway can be traced both ways: the gateway's
// audit event of a request leads to the keyring's rows for the credential it
// fetched, and the keyring's row carries the request id the gateway's trail
// is filtered by. The two trails stay two tables; the request id is on both.
func TestACredentialsUseIsTracedBetweenTheTwoAuditTrails(t *testing.T) {
	ms := memstore.New()
	kr, err := keyring.New(context.Background(), &keyring.Config{
		ActiveKekID: "local:v1", Keks: map[string]string{"local:v1": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
		OAuthRedirectBaseURL: "http://gateway.test/keyring", UIBaseURL: "http://gateway.test", GatewayBaseURL: "http://gateway.test",
		AuditChannelCap: 10000, AuditFlushInterval: 20 * time.Millisecond,
	}, ms, nil)
	if err != nil {
		t.Fatal(err)
	}
	kr.Start() // the keyring's audit writer
	t.Cleanup(kr.Shutdown)
	r := newAPIRig(t, kr.Source())

	slug := "a" + uuid.NewString()[:8]
	status, body := r.do(http.MethodPost, "/servers", alice, "", CreateServerInput{Name: "audited " + slug, Slug: slug, URL: r.mcpURL, Auth: headerInput})
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	s := decodeServer(t, body)
	t.Cleanup(func() { _ = r.store.DeleteServer(context.Background(), uuid.MustParse(s.ID)) })
	if !r.pings(s.ID) {
		t.Fatal("the server does not answer")
	}

	// The gateway's trail: the upstream hops of the ping, under one request id.
	var events PageDTO[AuditEventDTO]
	eventually(t, "the gateway's audit events of the ping", func() bool {
		_, body := r.do(http.MethodGet, "/audit?slug="+slug, alice, "", nil)
		return json.Unmarshal([]byte(body), &events) == nil && len(events.Items) > 0
	})
	event := events.Items[0]
	if event.RequestID == "" {
		t.Fatal("the gateway's audit event has no request id")
	}

	// From the gateway's event to the keyring's rows.
	var traced KeyringAuditDTO
	eventually(t, "the keyring's audit row of the ping", func() bool {
		status, body := r.do(http.MethodGet, "/audit/"+event.ID+"/keyring", alice, "", nil)
		if status != http.StatusOK || json.Unmarshal([]byte(body), &traced) != nil {
			return false
		}
		for _, row := range traced.Items {
			if row.Type == "TokenAccessed" {
				return true
			}
		}
		return false
	})
	if traced.RequestID != event.RequestID {
		t.Fatalf("traced under request %q, the event is %q", traced.RequestID, event.RequestID)
	}
	var read *KeyringAuditRowDTO
	for i := range traced.Items {
		if traced.Items[i].Type == "TokenAccessed" {
			read = &traced.Items[i]
		}
	}
	if read.Outcome != "Success" || !read.HeldByGateway || read.TargetKind != "api_key" || read.TargetName != "mcp-"+slug || !strings.Contains(read.Method, slug) {
		t.Fatalf("the keyring's row is %+v", *read)
	}
	// The row of creating the secret was not written for a forwarded request and is not listed.
	for _, row := range traced.Items {
		if row.Type == "CredentialCreated" {
			t.Fatal("a row that was not written for the request is listed under it")
		}
	}
	if strings.Contains(body, stubSecret) {
		t.Fatal("the trace carries the credential")
	}

	// And back: the keyring's row carries the id, and the gateway's trail filters by it.
	rows, err := ms.AuditByRequestID(context.Background(), event.RequestID, 10)
	if err != nil || len(rows) == 0 || rows[0].RequestID == nil || *rows[0].RequestID != event.RequestID {
		t.Fatalf("the keyring's stored row does not carry the request id: %v", err)
	}
	status, body = r.do(http.MethodGet, "/audit?requestId="+*rows[0].RequestID, alice, "", nil)
	var back PageDTO[AuditEventDTO]
	if err := json.Unmarshal([]byte(body), &back); err != nil || status != http.StatusOK || len(back.Items) == 0 {
		t.Fatalf("the gateway's trail has nothing under the keyring row's request id: %d %s", status, body)
	}
	found := false
	for _, e := range back.Items {
		found = found || e.ID == event.ID
	}
	if !found {
		t.Fatal("the request id of the keyring's row does not lead back to the gateway's event")
	}

	// An event of a request that used no keyring credential lists nothing.
	if status, body := r.do(http.MethodGet, "/audit/"+uuid.NewString()+"/keyring", alice, "", nil); status != http.StatusNotFound {
		t.Fatalf("an unknown event: %d %s", status, body)
	}
}

// With the keyring off the lookup answers with an empty list, not an error.
func TestKeyringOffAnAuditEventListsNoKeyringRows(t *testing.T) {
	r := newAPIRig(t, nil)
	s := r.server()
	r.pings(s.ID)
	var events PageDTO[AuditEventDTO]
	eventually(t, "the gateway's audit events of the ping", func() bool {
		_, body := r.do(http.MethodGet, "/audit?slug="+s.Slug, alice, "", nil)
		return json.Unmarshal([]byte(body), &events) == nil && len(events.Items) > 0
	})
	status, body := r.do(http.MethodGet, "/audit/"+events.Items[0].ID+"/keyring", alice, "", nil)
	var traced KeyringAuditDTO
	if err := json.Unmarshal([]byte(body), &traced); err != nil || status != http.StatusOK || traced.Items == nil || len(traced.Items) != 0 {
		t.Fatalf("%d %s", status, body)
	}
}

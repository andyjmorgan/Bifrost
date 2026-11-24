package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestValidRequestID(t *testing.T) {
	for id, want := range map[string]bool{
		"trace-1.a:b_C9":                   true,
		strings.Repeat("a", 64):            true,
		"":                                 false,
		strings.Repeat("a", 65):            false,
		"has space":                        false,
		"tab\there":                        false,
		"new\nline":                        false,
		"nul\x00byte":                      false,
		"\xff\xfe":                         false, // not UTF-8
		"café":                             false, // UTF-8, but not ASCII
		"semi;colon":                       false,
		"quote'":                           false,
		"slash/":                           false,
		"00000000-0000-4000-8000-00000000": true,
	} {
		if got := validRequestID(id); got != want {
			t.Errorf("validRequestID(%q) = %v, want %v", id, got, want)
		}
	}
}

// pingAs pings a server with the given X-Request-Id and returns the request
// id the gateway's audit trail recorded for the upstream call.
func (r *apiRig) pingAs(t *testing.T, serverID, slug, requestID string) string {
	t.Helper()
	// Registering the server already called it once, under a request id of
	// its own. Let that settle, and remember what is there.
	seen := map[string]bool{}
	list := func() []AuditEventDTO {
		var events PageDTO[AuditEventDTO]
		_, body := r.do(http.MethodGet, "/audit?slug="+slug, alice, "", nil)
		if json.Unmarshal([]byte(body), &events) != nil {
			return nil
		}
		return events.Items
	}
	eventually(t, "the audit events of registering the server", func() bool { return len(list()) >= 2 })
	for _, e := range list() {
		seen[e.ID] = true
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/servers/"+serverID+"/ping", nil)
	req.Header.Set("X-Dev-User", alice)
	req.Header["X-Request-Id"] = []string{requestID}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ping: %d %s", rec.Code, rec.Body.String())
	}
	got := ""
	eventually(t, "the audit events of the ping", func() bool {
		for _, e := range list() {
			if !seen[e.ID] {
				got = e.RequestID
				return true
			}
		}
		return false
	})
	return got
}

// A caller's request id goes into both audit trails, so it is taken only
// when it is a plain token. Bytes that are not text would be refused by the
// keyring's database and take a whole batch of audit rows with them.
func TestACallersRequestIDIsTakenOnlyWhenItIsAPlainToken(t *testing.T) {
	for name, given := range map[string]string{"not UTF-8": "\xff\xfe\xfd", "a NUL": "a\x00b", "spaces": "two words", "too long": strings.Repeat("x", 200)} {
		t.Run(name, func(t *testing.T) {
			r := newAPIRig(t, nil)
			s := r.server()
			got := r.pingAs(t, s.ID, s.Slug, given)
			if got == given {
				t.Fatal("the caller's bytes were recorded as the request id")
			}
			if _, err := uuid.Parse(got); err != nil {
				t.Fatalf("the replacement is not an id the gateway made: %q", got)
			}
		})
	}
	// A plain token is the caller's to choose: that is what lets them find
	// their request in the trail.
	r := newAPIRig(t, nil)
	s := r.server()
	if got := r.pingAs(t, s.ID, s.Slug, "trace-42.step:a_b"); got != "trace-42.step:a_b" {
		t.Fatalf("a plain token was replaced with %q", got)
	}
}

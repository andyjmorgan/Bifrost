package gateway

import (
	"net/http"
	"strings"
	"testing"

	"github.com/andyjmorgan/Bifrost/internal/auth"
)

// CallerKeys describes exactly what a rule can read from request.caller,
// and a field the caller does not have is left out, not given as "".
func TestCallerKeysMatchTheCallerMap(t *testing.T) {
	got := callerMap(&auth.Caller{Email: "a@example.com", Name: "A", Type: auth.ActorAPIKey, Subject: "ci"})
	if len(got) != len(CallerKeys) {
		t.Errorf("callerMap has %d keys, CallerKeys describes %d", len(got), len(CallerKeys))
	}
	for k := range got {
		if CallerKeys[k] == "" {
			t.Errorf("caller.%s has no description in CallerKeys", k)
		}
	}
	bare := callerMap(&auth.Caller{})
	for _, k := range []string{"email", "name", "keyName"} {
		if _, has := bare[k]; has {
			t.Errorf("caller.%s is filled in for a caller that has none", k)
		}
	}
}

// A rule sees no HTTP header unless the operator names it, and never one
// that carries a credential, even when it is named.
func TestRuleHeadersStartEmptyAndNeverShowCredentials(t *testing.T) {
	sent := http.Header{}
	sent.Set("Authorization", "Bearer secret")
	sent.Set("Cookie", "session=secret")
	sent.Set("X-Api-Key", "secret")
	sent.Set("X-Team", "sre")
	sent.Add("X-Tag", "a")
	sent.Add("X-Tag", "b")

	g := &Gateway{}
	if got := g.headersFor(sent); len(got) != 0 {
		t.Fatalf("with no allow-list a rule sees %v", got)
	}
	g.SetRuleHeaders([]string{"X-Team", " x-tag ", "Authorization", "cookie", "X-API-Key", "Proxy-Authorization", "X-Absent", "",
		"X-Auth-Token", "X-Amz-Security-Token", "X-Client-Secret", "X-Session-Id", "X-Hub-Signature"})
	got := g.headersFor(sent)
	if len(got) != 2 || got["x-team"] != "sre" || got["x-tag"] != "a, b" {
		t.Fatalf("a rule sees %v, want x-team and x-tag only", got)
	}
	for k, v := range got {
		if strings.Contains(v, "secret") {
			t.Errorf("%s carries a credential", k)
		}
	}
}

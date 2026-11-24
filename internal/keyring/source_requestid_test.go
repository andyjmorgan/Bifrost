package keyring

import (
	"context"
	"strings"
	"testing"

	"github.com/andyjmorgan/Bifrost/internal/keyring/audit"
	"github.com/andyjmorgan/Bifrost/internal/keyringref"
)

// The keyring writes the gateway's request id on its audit row, and only an id its column can hold:
// text, without a NUL, at most 64 bytes. The gateway is meant to send nothing else, but the keyring
// does not stake a batch of audit rows on that.
func TestTheKeyringKeepsOnlyARequestIDItCanStore(t *testing.T) {
	r := newSourceRig(t)
	ctx := context.Background()
	ref, err := r.src.PutServerSecret(ctx, gatewayUse, keyringref.ServerSecret{Scheme: keyringref.SchemeHeader, HeaderName: "X-Api-Key", Value: serverValue})
	if err != nil {
		t.Fatal(err)
	}
	r.events()

	for name, c := range map[string]struct {
		id   string
		kept bool
	}{
		"a plain token": {"trace-42.step:a_b", true},
		"not UTF-8":     {"\xff\xfe", false},
		"a NUL":         {"a\x00b", false},
		"too long":      {strings.Repeat("x", 65), false},
		"none":          {"", false},
	} {
		use := gatewayUse
		use.RequestID = c.id
		if _, err := r.src.SecretHeader(ctx, ref, use); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		events := r.events()
		if len(events) != 1 || events[0].Type != audit.EventTokenAccessed {
			t.Fatalf("%s: %d events", name, len(events))
		}
		switch got := events[0].RequestID; {
		case c.kept && (got == nil || *got != c.id):
			t.Errorf("%s: the request id was not written on the row", name)
		case !c.kept && got != nil:
			t.Errorf("%s: a request id the column cannot hold was written on the row", name)
		}
	}
}

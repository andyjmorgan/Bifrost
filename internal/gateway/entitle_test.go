package gateway

import (
	"testing"

	"github.com/andyjmorgan/Bifrost/internal/auth"
)

// CallerKeys describes exactly what a rule can read from `caller`.
func TestCallerKeysMatchTheCallerMap(t *testing.T) {
	got := callerMap(&auth.Caller{})
	if len(got) != len(CallerKeys) {
		t.Errorf("callerMap has %d keys, CallerKeys describes %d", len(got), len(CallerKeys))
	}
	for k := range got {
		if CallerKeys[k] == "" {
			t.Errorf("caller.%s has no description in CallerKeys", k)
		}
	}
}

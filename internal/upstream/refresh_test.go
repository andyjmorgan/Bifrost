package upstream

import (
	"testing"
	"time"

	"github.com/andyjmorgan/Bifrost/internal/store"
)

func TestNeedsRefreshScalesLeewayToLifetime(t *testing.T) {
	c := &Connector{leeway: 10 * time.Minute}
	now := time.Now()
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }

	// 5-minute token refreshed just now: fresh for its first half.
	cred := &store.Credential{LastRefreshedAt: at(0), OAuthExpiresAt: at(5 * time.Minute)}
	if c.needsRefresh(cred) {
		t.Fatal("fresh short-lived token should not refresh")
	}
	cred = &store.Credential{LastRefreshedAt: at(-3 * time.Minute), OAuthExpiresAt: at(2 * time.Minute)}
	if !c.needsRefresh(cred) {
		t.Fatal("short-lived token past half-life should refresh")
	}
	// Long-lived token: plain leeway applies.
	cred = &store.Credential{LastRefreshedAt: at(-50 * time.Minute), OAuthExpiresAt: at(9 * time.Minute)}
	if !c.needsRefresh(cred) {
		t.Fatal("token inside the leeway window should refresh")
	}
	cred = &store.Credential{LastRefreshedAt: at(-50 * time.Minute), OAuthExpiresAt: at(11 * time.Minute)}
	if c.needsRefresh(cred) {
		t.Fatal("token outside the leeway window should not refresh")
	}
}

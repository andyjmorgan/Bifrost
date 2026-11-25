package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestForceRefreshReplacesALiveToken(t *testing.T) {
	f := newOAuthFixture(t)
	res, _ := f.flow.Begin(f.ctx, "acme", []string{"openid"}, "https://v")
	if _, err := f.flow.Complete(context.Background(), "code", res.State); err != nil {
		t.Fatal(err)
	}
	list, _ := f.tokens.List(f.ctx)

	f.tokenResp = `{"access_token":"access-2","expires_in":3600}`
	before := time.Now()
	got, err := f.tokens.Refresh(f.ctx, list[0].ID)
	if err != nil || got == nil {
		t.Fatalf("refresh: %+v %v", got, err)
	}
	if got.LastRefreshedAt == nil || got.LastRefreshedAt.Before(before) {
		t.Fatalf("lastRefreshedAt not moved: %v", got.LastRefreshedAt)
	}
	tok, err := f.tokens.GetAccessToken(f.ctx, "acme", "")
	if err != nil || tok.AccessToken != "access-2" {
		t.Fatalf("expected the refreshed token, got %+v %v", tok, err)
	}
}

func TestForceRefreshWithoutRefreshToken(t *testing.T) {
	f := newOAuthFixture(t)
	f.tokenResp = `{"access_token":"access-1","expires_in":3600}`
	res, _ := f.flow.Begin(f.ctx, "acme", []string{"openid"}, "https://v")
	if _, err := f.flow.Complete(context.Background(), "code", res.State); err != nil {
		t.Fatal(err)
	}
	list, _ := f.tokens.List(f.ctx)
	if _, err := f.tokens.Refresh(f.ctx, list[0].ID); !errors.Is(err, ErrNotRefreshable) {
		t.Fatalf("expected ErrNotRefreshable, got %v", err)
	}
}

func TestForceRefreshUnknownAccount(t *testing.T) {
	f := newOAuthFixture(t)
	got, err := f.tokens.Refresh(f.ctx, uuid.New())
	if err != nil || got != nil {
		t.Fatalf("expected nil, got %+v %v", got, err)
	}
}

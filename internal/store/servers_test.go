package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

// newTestServer inserts a server with a slug no other test uses and removes
// it when the test ends.
func newTestServer(t *testing.T, p *Postgres, kind AuthKind) *Server {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	id := uuid.New()
	s := &Server{ID: id, Slug: "t" + id.String()[:8], Name: "before", URL: "https://before.example/mcp", Description: "d",
		AuthKind: kind, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := p.CreateServer(ctx, s, "tester"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.DeleteServer(ctx, s.ID) })
	return s
}

// UpdateServer writes each connection field to its own column. Its
// placeholders once pointed one argument off, and Postgres refused to
// prepare the statement at all.
func TestUpdateServerWritesEachField(t *testing.T) {
	p := testStore(t)
	ctx := context.Background()
	s := newTestServer(t, p, AuthNone)

	s.Name, s.URL, s.Description, s.Enabled, s.AuthKind = "after", "https://after.example/mcp", "changed", false, AuthHeader
	if err := p.UpdateServer(ctx, s); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := p.GetServer(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "after" || got.URL != "https://after.example/mcp" || got.Description != "changed" || got.Enabled || got.AuthKind != AuthHeader {
		t.Fatalf("stored %+v", got)
	}
}

func TestServerIconsSurviveCacheExpiryAndRejectStaleDiscovery(t *testing.T) {
	p := testStore(t)
	ctx := context.Background()
	s := newTestServer(t, p, AuthNone)
	// Read back Postgres' timestamp precision, as the connector does.
	s, err := p.GetServer(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	icons := json.RawMessage(`[{"src":"https://example.com/icon.svg","mimeType":"image/svg+xml","sizes":["any"],"theme":"dark"}]`)
	check := func(want json.RawMessage) {
		t.Helper()
		got, err := p.GetServer(ctx, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		var a, b any
		if json.Unmarshal(got.Icons, &a) != nil || json.Unmarshal(want, &b) != nil || !reflect.DeepEqual(a, b) {
			t.Fatalf("icons = %s, want %s", got.Icons, want)
		}
		if !got.UpdatedAt.Equal(s.UpdatedAt) || got.Generation != s.Generation {
			t.Fatal("observing icons changed the configuration version")
		}
	}
	if err := p.ObserveServerIcons(ctx, s, icons); err != nil {
		t.Fatal(err)
	}
	if err := p.CacheInvalidateServer(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	check(icons)

	// Re-discovery with no icons removes an old icon.
	if err := p.ObserveServerIcons(ctx, s, nil); err != nil {
		t.Fatal(err)
	}
	check(json.RawMessage(`[]`))
	if err := p.ObserveServerIcons(ctx, s, icons); err != nil {
		t.Fatal(err)
	}
	// Renaming retains the display metadata, changing endpoints clears it.
	s.Name = "renamed"
	if err := p.UpdateServer(ctx, s); err != nil {
		t.Fatal(err)
	}
	check(icons)
	old := *s
	s.URL = "https://other.example/mcp"
	if err := p.UpdateServer(ctx, s); err != nil {
		t.Fatal(err)
	}
	check(json.RawMessage(`[]`))
	if err := p.ObserveServerIcons(ctx, &old, icons); err != nil {
		t.Fatal(err)
	}
	check(json.RawMessage(`[]`))
}

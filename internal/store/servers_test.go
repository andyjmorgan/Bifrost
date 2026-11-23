package store

import (
	"context"
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

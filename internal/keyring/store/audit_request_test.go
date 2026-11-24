package store_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/keyring/store"
)

// TestAuditRowsCarryTheRequestID pins the column a gateway request's id is stored in: it is written
// with the row, returned by a listing, filtered on, and looked up across owners.
func TestAuditRowsCarryTheRequestID(t *testing.T) {
	alice, bob, tenant := uuid.New(), uuid.New(), uuid.New()
	req := "req-" + uuid.NewString()
	now := time.Now().UTC()
	rows := []store.AuditEntry{
		{EventType: 1, UserID: alice, TenantID: tenant, Transport: "gateway", Headers: map[string]string{}, CreatedAt: now.Add(-2 * time.Second), RequestID: &req},
		{EventType: 2, UserID: bob, TenantID: tenant, Transport: "gateway", Headers: map[string]string{}, CreatedAt: now.Add(-time.Second), RequestID: &req},
		{EventType: 1, UserID: alice, TenantID: tenant, Transport: "http", Headers: map[string]string{}, CreatedAt: now},
	}
	if err := pg.InsertAuditBatch(ctx(), rows); err != nil {
		t.Fatal(err)
	}
	// Alice's own listing: both her rows, the request id on the one that has it.
	items, total, err := pg.QueryAudit(ctx(), store.AuditFilter{UserID: alice, TenantID: tenant, Limit: 10})
	if err != nil || total != 2 || items[0].RequestID != nil || items[1].RequestID == nil || *items[1].RequestID != req {
		t.Fatalf("listing: total %d err %v", total, err)
	}
	// Filtered to the request: hers only, never Bob's.
	items, total, err = pg.QueryAudit(ctx(), store.AuditFilter{UserID: alice, TenantID: tenant, Limit: 10, RequestID: &req})
	if err != nil || total != 1 || items[0].UserID != alice {
		t.Fatalf("filtered listing: total %d err %v", total, err)
	}
	// The gateway's lookup: every row of the request, oldest first, whoever owns it.
	all, err := pg.AuditByRequestID(ctx(), req, 10)
	if err != nil || len(all) != 2 || all[0].UserID != alice || all[1].UserID != bob {
		t.Fatalf("by request: %d rows, err %v", len(all), err)
	}
	if one, err := pg.AuditByRequestID(ctx(), req, 1); err != nil || len(one) != 1 {
		t.Fatalf("limited: %d rows, err %v", len(one), err)
	}
	if none, err := pg.AuditByRequestID(ctx(), "", 10); err != nil || len(none) != 0 {
		t.Fatalf("an empty id matched %d rows, err %v", len(none), err)
	}
}

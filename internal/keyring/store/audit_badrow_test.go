package store_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/keyring/audit"
	"github.com/andyjmorgan/Bifrost/internal/keyring/store"
)

// notText is a value Postgres refuses to store in a text column.
const notText = "\xff\xfe not text"

// TestABadRowFailsItsBatch pins the fault the writer has to live with: one row the database
// refuses fails the whole insert.
func TestABadRowFailsItsBatch(t *testing.T) {
	u, tenant := uuid.New(), uuid.New()
	bad := notText
	rows := []store.AuditEntry{
		{EventType: 1, UserID: u, TenantID: tenant, Transport: "http", Headers: map[string]string{}, CreatedAt: time.Now()},
		{EventType: 1, UserID: u, TenantID: tenant, Transport: "gateway", Headers: map[string]string{}, CreatedAt: time.Now(), RequestID: &bad},
	}
	if err := pg.InsertAuditBatch(ctx(), rows); err == nil {
		t.Fatal("the database stored a request id that is not text")
	}
	if _, total, err := pg.QueryAudit(ctx(), store.AuditFilter{UserID: u, TenantID: tenant, Limit: 10}); err != nil || total != 0 {
		t.Fatalf("part of a failed batch was stored: %d rows, err %v", total, err)
	}
}

// TestOneBadRowDoesNotSinkTheWritersBatch runs the audit writer against the real database with a
// batch in which one event cannot be stored. The other events must still be written: an audit trail
// that loses every row queued beside a bad one can be blinded on purpose.
func TestOneBadRowDoesNotSinkTheWritersBatch(t *testing.T) {
	u, tenant := uuid.New(), uuid.New()
	bad := notText
	log := audit.NewLog(100, nil, nil)
	w := audit.NewWriter(log, pg, nil, nil, audit.WriterOptions{BatchSize: 10, FlushInterval: time.Hour})
	done := make(chan struct{})
	go func() { w.Run(ctx()); close(done) }()

	event := func(detail string, requestID *string) audit.Event {
		return audit.Event{Type: audit.EventTokenAccessed, UserID: u, TenantID: tenant, Transport: "gateway", Headers: map[string]string{},
			Detail: &detail, RequestID: requestID, CreatedAt: time.Now().UTC()}
	}
	log.Enqueue(event("before", nil))
	log.Enqueue(event("the bad one", &bad))
	log.Enqueue(event("after", nil))
	log.Complete() // flushes the three as one batch
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the writer did not finish")
	}

	items, total, err := pg.QueryAudit(ctx(), store.AuditFilter{UserID: u, TenantID: tenant, Limit: 10})
	if err != nil || total != 2 {
		t.Fatalf("%d rows written, want the two good ones (err %v)", total, err)
	}
	for _, e := range items {
		if e.Detail == nil || (*e.Detail != "before" && *e.Detail != "after") {
			t.Fatalf("an unexpected row was written: %v", e.Detail)
		}
	}
}

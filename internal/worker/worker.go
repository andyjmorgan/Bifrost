// Package worker runs the periodic jobs: OAuth token refresh and audit
// retention.
package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/store"
	"github.com/andyjmorgan/Bifrost/internal/upstream"
)

// Store is the slice of persistence the worker uses.
type Store interface {
	ExpiringOAuthCredentials(ctx context.Context, before time.Time) ([]*store.Credential, error)
	ServerOwner(ctx context.Context, id uuid.UUID) (*store.Server, error)
	PurgeAuditBefore(ctx context.Context, t time.Time) (int64, error)
	PurgeExpiredOAuthStates(ctx context.Context) error
	PurgeExpiredCache(ctx context.Context) (int64, error)
}

// Worker owns the tickers.
type Worker struct {
	store     Store
	conn      *upstream.Connector
	log       *slog.Logger
	interval  time.Duration
	leeway    time.Duration
	retention time.Duration
}

// New builds a worker.
func New(s Store, conn *upstream.Connector, log *slog.Logger, interval, leeway, retention time.Duration) *Worker {
	return &Worker{store: s, conn: conn, log: log, interval: interval, leeway: leeway, retention: retention}
}

// Run blocks until ctx ends. The first pass runs shortly after start so a
// restart never leaves tokens stale for a whole interval.
func (w *Worker) Run(ctx context.Context) {
	first := time.NewTimer(30 * time.Second)
	defer first.Stop()
	select {
	case <-ctx.Done():
		return
	case <-first.C:
		w.Tick(ctx)
	}
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Tick(ctx)
		}
	}
}

// Tick performs one pass of every job.
func (w *Worker) Tick(ctx context.Context) {
	w.refreshTokens(ctx)
	w.purge(ctx)
}

// refreshTokens renews anything that would expire before the next pass.
func (w *Worker) refreshTokens(ctx context.Context) {
	deadline := time.Now().Add(w.interval + w.leeway)
	creds, err := w.store.ExpiringOAuthCredentials(ctx, deadline)
	if err != nil {
		w.log.Error("worker: list expiring credentials", "err", err)
		return
	}
	for _, c := range creds {
		srv, err := w.store.ServerOwner(ctx, c.ServerID)
		if err != nil {
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := w.conn.Refresh(rctx, srv, c); err != nil {
			w.log.Warn("worker: refresh failed", "slug", srv.Slug, "err", err)
		}
		cancel()
	}
	if len(creds) > 0 {
		w.log.Info("worker: refresh pass", "candidates", len(creds))
	}
}

func (w *Worker) purge(ctx context.Context) {
	if n, err := w.store.PurgeExpiredCache(ctx); err != nil {
		w.log.Error("worker: cache sweep", "err", err)
	} else if n > 0 {
		w.log.Info("worker: cache sweep", "deleted", n)
	}
	if n, err := w.store.PurgeAuditBefore(ctx, time.Now().Add(-w.retention)); err != nil {
		w.log.Error("worker: audit purge", "err", err)
	} else if n > 0 {
		w.log.Info("worker: audit purged", "rows", n)
	}
	if err := w.store.PurgeExpiredOAuthStates(ctx); err != nil {
		w.log.Error("worker: oauth state purge", "err", err)
	}
}

// Package worker runs the gateway's periodic jobs: the cache sweep and audit
// retention.
//
// It refreshes no tokens. Every OAuth token a server uses lives in the
// keyring, which refreshes one when it is asked for it close to its expiry.
package worker

import (
	"context"
	"log/slog"
	"time"
)

// Store is the slice of persistence the worker uses.
type Store interface {
	PurgeAuditBefore(ctx context.Context, t time.Time) (int64, error)
	PurgeExpiredCache(ctx context.Context) (int64, error)
}

// Worker owns the ticker.
type Worker struct {
	store     Store
	log       *slog.Logger
	interval  time.Duration
	retention time.Duration
}

// New builds a worker that sweeps every interval and keeps audit events for
// retention.
func New(s Store, log *slog.Logger, interval, retention time.Duration) *Worker {
	return &Worker{store: s, log: log, interval: interval, retention: retention}
}

// Run blocks until ctx ends. The first pass runs shortly after start so a
// restart never leaves expired rows lying for a whole interval.
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
	w.purge(ctx)
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
}

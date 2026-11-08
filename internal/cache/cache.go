// Package cache is the in-process writer for the Postgres TTL cache: callers
// enqueue and return, a goroutine does the insert, like the audit recorder.
package cache

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// Namespaces (the ns column).
const (
	NSTools = "tools" // a server's full tools/list, keyed by server id
)

// Store is the slice of the Postgres store the writer needs.
type Store interface {
	CacheSet(ctx context.Context, ns, key string, serverID *uuid.UUID, value json.RawMessage, ttl time.Duration) error
	CacheInvalidateServer(ctx context.Context, serverID uuid.UUID) error
}

type job struct {
	ns, key    string
	serverID   *uuid.UUID
	value      json.RawMessage
	ttl        time.Duration
	invalidate *uuid.UUID
}

// Writer applies cache writes off the request path.
type Writer struct {
	s     Store
	log   *slog.Logger
	queue chan job
}

// NewWriter starts the background writer; it stops when ctx ends.
func NewWriter(ctx context.Context, s Store, log *slog.Logger) *Writer {
	w := &Writer{s: s, log: log, queue: make(chan job, 256)}
	go w.run(ctx)
	return w
}

func (w *Writer) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-w.queue:
			wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			var err error
			if j.invalidate != nil {
				err = w.s.CacheInvalidateServer(wctx, *j.invalidate)
			} else {
				err = w.s.CacheSet(wctx, j.ns, j.key, j.serverID, j.value, j.ttl)
			}
			cancel()
			if err != nil {
				w.log.Warn("cache write failed", "ns", j.ns, "key", j.key, "err", err)
			}
		}
	}
}

func (w *Writer) enqueue(j job) {
	if w == nil {
		return
	}
	select {
	case w.queue <- j:
	default:
		w.log.Warn("cache queue full, dropping write", "ns", j.ns, "key", j.key)
	}
}

// Put stores v (JSON-marshalled) under (ns, key) for ttl. A ttl <= 0 is a no-op.
func (w *Writer) Put(ns, key string, serverID *uuid.UUID, v any, ttl time.Duration) {
	if w == nil || ttl <= 0 {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		w.log.Warn("cache marshal failed", "ns", ns, "key", key, "err", err)
		return
	}
	w.enqueue(job{ns: ns, key: key, serverID: serverID, value: b, ttl: ttl})
}

// Invalidate drops every cached entry tied to a server (edit, new credential).
func (w *Writer) Invalidate(serverID uuid.UUID) {
	if w == nil {
		return
	}
	id := serverID
	w.enqueue(job{invalidate: &id})
}

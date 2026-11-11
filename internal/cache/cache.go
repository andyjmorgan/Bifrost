// Package cache is the in-process writer for the Postgres TTL cache: callers
// enqueue and return, a goroutine does the insert, like the audit recorder.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/store"
)

// Namespaces (the ns column).
const (
	NSTools = "tools" // a server's full tools/list, keyed by server id
	NSInit  = "init"  // what the server reported at initialize (Init), keyed by server id
)

// Init is the cached initialize result: the negotiated protocol version lets
// the next connect start there (no discover probe on legacy servers, no
// initialize on 2026-07-28 ones). It expires like any cache row, so an
// upgraded server is re-discovered with the latest version at the next miss.
type Init struct {
	ProtocolVersion string `json:"protocolVersion"`
	ServerName      string `json:"serverName,omitempty"`
	ServerVersion   string `json:"serverVersion,omitempty"`
	Title           string `json:"title,omitempty"`
	Instructions    string `json:"instructions,omitempty"`
}

// Store is the slice of the Postgres store the writer needs.
type Store interface {
	CacheSet(ctx context.Context, ns, key string, serverID *uuid.UUID, ifUpdatedAt time.Time, value json.RawMessage, ttl time.Duration) error
}

type job struct {
	ns, key   string
	serverID  *uuid.UUID
	updatedAt time.Time
	value     json.RawMessage
	ttl       time.Duration
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
			err := w.s.CacheSet(wctx, j.ns, j.key, j.serverID, j.updatedAt, j.value, j.ttl)
			cancel()
			switch {
			case errors.Is(err, store.ErrStale):
				w.log.Debug("cache write skipped: server changed since fetch", "ns", j.ns, "key", j.key)
			case err != nil:
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

// Put stores v (JSON-marshalled) under (ns, key) for ttl. A ttl <= 0 is a
// no-op. updatedAt is the server's updated_at when v was fetched; the write
// is dropped if the server changed since (see store.CacheSet).
func (w *Writer) Put(ns, key string, serverID *uuid.UUID, updatedAt time.Time, v any, ttl time.Duration) {
	if w == nil || ttl <= 0 {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		w.log.Warn("cache marshal failed", "ns", ns, "key", key, "err", err)
		return
	}
	w.enqueue(job{ns: ns, key: key, serverID: serverID, updatedAt: updatedAt, value: b, ttl: ttl})
}

package hooks

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// StateStore keeps a request's state between the round that asks a question
// and the retry that answers it, whether the gateway asked or the server
// did. The client holds only the key, which says nothing about the request
// and cannot be altered.
//
// A gateway with more than one instance must supply a store they all share:
// a cache with expiring keys, such as a Redis key with a TTL. Put stores a
// state for ttl. Take returns a state and removes it in one step, so a
// question can be answered once; across instances that step must be atomic,
// as Redis GETDEL is.
type StateStore interface {
	Put(key string, st State, ttl time.Duration)
	Take(key string) (State, bool)
}

// stateKeyPrefix marks a request state the gateway issued.
const stateKeyPrefix = "c_"

func newStateKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("no randomness for a confirmation key: " + err.Error())
	}
	return stateKeyPrefix + hex.EncodeToString(b[:])
}

func isStateKey(token string) bool { return strings.HasPrefix(token, stateKeyPrefix) }

// MemoryStates is a StateStore in this process's memory. It is what the
// engine uses when a gateway supplies none, and it is right for tests and a
// single instance only: a retry that lands on another instance does not
// find its state, and a restart forgets every request that was waiting.
type MemoryStates struct {
	mu      sync.Mutex
	entries map[string]memoryState
	swept   time.Time
}

type memoryState struct {
	st      State
	expires time.Time
}

// Put stores a challenge for ttl.
func (m *MemoryStates) Put(key string, st State, ttl time.Duration) {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = map[string]memoryState{}
	}
	// Unanswered challenges are dropped once a minute, so they cannot pile up.
	if now.Sub(m.swept) > time.Minute {
		for k, e := range m.entries {
			if now.After(e.expires) {
				delete(m.entries, k)
			}
		}
		m.swept = now
	}
	m.entries[key] = memoryState{st: st, expires: now.Add(ttl)}
}

// Take returns a challenge and removes it. It reports false for a key that
// was never stored, was already taken or has expired.
func (m *MemoryStates) Take(key string) (State, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok {
		return State{}, false
	}
	delete(m.entries, key)
	if time.Now().After(e.expires) {
		return State{}, false
	}
	return e.st, true
}

// Len reports how many challenges are held, expired ones included.
func (m *MemoryStates) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// states returns the configured store, or the engine's own in-memory one.
func (e *Engine) states() StateStore {
	if e.States != nil {
		return e.States
	}
	e.memOnce.Do(func() { e.mem = &MemoryStates{} })
	return e.mem
}

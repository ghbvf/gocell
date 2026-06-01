package reconcile

import (
	"container/list"
	"math"
	"sync"
	"time"
)

// defaultBackoffBase is the base delay mirroring client-go
// ItemExponentialFailureRateLimiter (5ms).
//
// ref: kubernetes/client-go util/workqueue/default_rate_limiters.go
const defaultBackoffBase = 5 * time.Millisecond

// defaultBackoffMax is the maximum delay mirroring client-go
// ItemExponentialFailureRateLimiter (1000s).
//
// ref: kubernetes/client-go util/workqueue/default_rate_limiters.go
const defaultBackoffMax = 1000 * time.Second

// maxBackoffEntries bounds the per-entity failure tracker. client-go's
// ItemExponentialFailureRateLimiter holds an unbounded map and relies on the
// caller's key set being bounded; GoCell keeps that contract in
// Request.EntityID godoc (S1) AND enforces this hard cap so a high-cardinality
// or hostile EntityID stream cannot grow the tracker without limit (DoS guard).
//
// When the tracker is full, the least-recently-used entity is evicted; an
// evicted entity simply restarts at base on its next failure. This is
// correctness-preserving: the exponential backoff is a throttling optimization,
// not a guarantee, so losing one entity's accumulated count only resets its
// delay to base — it never drops a reconcile.
const maxBackoffEntries = 4096

// backoffEntry is one LRU node: the entity key plus its current failure count.
type backoffEntry struct {
	entityID string
	failures int
}

// entityBackoff tracks per-entity failure counts and computes an exponential
// backoff delay for each entity. It mirrors client-go's
// ItemExponentialFailureRateLimiter: base·2^n, capped at max, no jitter.
// The first When call for an entity returns base (n=0 before increment).
//
// Unlike client-go, the per-entity state is a bounded LRU (cap
// maxBackoffEntries): see that const for the rationale. All mutating methods
// hold mu.
//
// ref: kubernetes/client-go util/workqueue/default_rate_limiters.go
type entityBackoff struct {
	mu         sync.Mutex
	ll         *list.List               // LRU recency list; front = most recently used, holds *backoffEntry
	items      map[string]*list.Element // entityID → its list element
	base       time.Duration
	max        time.Duration
	maxEntries int
}

// newEntityBackoff creates an entityBackoff with the given parameters.
// base <= 0 defaults to 5ms; max <= 0 defaults to 1000s. A base greater than
// max is normalized to max (defensive: Start fail-fasts on this misconfig
// before constructing, so direct callers — tests/internal — still get a
// coherent [base, max] interval rather than a base that exceeds the cap).
//
// ref: kubernetes/client-go util/workqueue/default_rate_limiters.go
func newEntityBackoff(base, max time.Duration) *entityBackoff {
	if base <= 0 {
		base = defaultBackoffBase
	}
	if max <= 0 {
		max = defaultBackoffMax
	}
	if base > max {
		base = max
	}
	return &entityBackoff{
		ll:         list.New(),
		items:      make(map[string]*list.Element),
		base:       base,
		max:        max,
		maxEntries: maxBackoffEntries,
	}
}

// When returns the backoff delay for entityID and increments its failure count.
// The first call returns base; subsequent calls double each time, capped at max.
// The result is always in [base, max] — never negative or zero. Touching an
// entity marks it most-recently-used; a never-seen entity is inserted and, when
// the tracker is at capacity, the least-recently-used entity is evicted first.
func (b *entityBackoff) When(entityID string) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	var n int
	if el, ok := b.items[entityID]; ok {
		b.ll.MoveToFront(el)
		ent := el.Value.(*backoffEntry)
		n = ent.failures
		ent.failures++
	} else {
		b.items[entityID] = b.ll.PushFront(&backoffEntry{entityID: entityID, failures: 1})
		n = 0
		if b.ll.Len() > b.maxEntries {
			b.evictOldest()
		}
	}

	// Compute base · 2^n using float64 to avoid integer overflow.
	ns := float64(b.base.Nanoseconds()) * math.Pow(2, float64(n))
	if ns > math.MaxInt64 || time.Duration(ns) > b.max {
		return b.max
	}
	return time.Duration(ns)
}

// evictOldest removes the least-recently-used entry. Caller holds mu.
func (b *entityBackoff) evictOldest() {
	el := b.ll.Back()
	if el == nil {
		return
	}
	b.ll.Remove(el)
	delete(b.items, el.Value.(*backoffEntry).entityID)
}

// Forget clears the failure count for entityID so the next When call
// returns base again. It is a no-op for unknown entities.
func (b *entityBackoff) Forget(entityID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if el, ok := b.items[entityID]; ok {
		b.ll.Remove(el)
		delete(b.items, entityID)
	}
}

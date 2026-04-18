package idempotency

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// InMemClaimer is a process-local Claimer backed by a map + mutex. It is safe
// for single-process deployments (demo / single-pod production) but does NOT
// coordinate across replicas — use adapters/redis NewIdempotencyClaimer for
// multi-pod setups.
//
// Purpose: Keep ConsumerBase wired on every GoCell deployment (including the
// in-process EventBus path used by demos and unit tests) so consumer comments
// that promise Claimer semantics stay true regardless of broker choice. Without
// this, the in-mem path would silently skip the middleware and the "Claimer
// (two-phase Claim/Commit/Release)" declarations on every L2 consumer would be
// false advertising.
//
// Lease TTL and done TTL are honoured via wall-clock expiry checked lazily on
// each Claim; no background goroutines so shutdown is trivial.
type InMemClaimer struct {
	mu      sync.Mutex
	entries map[string]*inMemEntry
	// now is indirected for tests; production uses time.Now.
	now func() time.Time
}

type inMemEntry struct {
	state ClaimState // ClaimAcquired (in-flight) or ClaimDone (committed)
	token string     // guards stale Commit/Release after Release+reclaim
	// expiresAt is lease expiry for in-flight entries, or done-key expiry for
	// committed entries. When wall-clock passes expiresAt the entry is dropped
	// and a fresh Claim acquires.
	expiresAt time.Time
}

// NewInMemClaimer creates a new in-memory Claimer with default wall-clock.
func NewInMemClaimer() *InMemClaimer {
	return &InMemClaimer{
		entries: make(map[string]*inMemEntry),
		now:     time.Now,
	}
}

// Claim acquires a processing lease. See Claimer.Claim for semantics.
func (c *InMemClaimer) Claim(_ context.Context, key string, leaseTTL, doneTTL time.Duration) (ClaimState, Receipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if existing, ok := c.entries[key]; ok {
		if now.Before(existing.expiresAt) {
			if existing.state == ClaimDone {
				return ClaimDone, nil, nil
			}
			return ClaimBusy, nil, nil
		}
		// Expired entry — drop and fall through to fresh acquisition.
		delete(c.entries, key)
	}

	token := newToken()
	c.entries[key] = &inMemEntry{
		state:     ClaimAcquired,
		token:     token,
		expiresAt: now.Add(leaseTTL),
	}
	return ClaimAcquired, &inMemReceipt{
		claimer: c,
		key:     key,
		token:   token,
		doneTTL: doneTTL,
	}, nil
}

// inMemReceipt is the Receipt returned by InMemClaimer.Claim.
type inMemReceipt struct {
	claimer *InMemClaimer
	key     string
	token   string
	doneTTL time.Duration
	used    sync.Once
	err     error
}

// Commit marks the claim as done and sets doneTTL. Stale commits (after
// Release + reclaim by another goroutine) return an error and do not clobber
// the newer claim's state.
func (r *inMemReceipt) Commit(_ context.Context) error {
	r.used.Do(func() {
		r.claimer.mu.Lock()
		defer r.claimer.mu.Unlock()
		entry, ok := r.claimer.entries[r.key]
		if !ok || entry.token != r.token {
			r.err = errStaleReceipt
			return
		}
		entry.state = ClaimDone
		entry.expiresAt = r.claimer.now().Add(r.doneTTL)
	})
	return r.err
}

// Release drops the in-flight lease so another consumer can re-claim.
func (r *inMemReceipt) Release(_ context.Context) error {
	r.used.Do(func() {
		r.claimer.mu.Lock()
		defer r.claimer.mu.Unlock()
		entry, ok := r.claimer.entries[r.key]
		if !ok || entry.token != r.token {
			r.err = errStaleReceipt
			return
		}
		delete(r.claimer.entries, r.key)
	})
	return r.err
}

// Extend resets the processing-lease TTL. Returns ErrLeaseExpired if the lease
// was not held (token mismatch or entry evicted by TTL).
func (r *inMemReceipt) Extend(_ context.Context, ttl time.Duration) error {
	r.claimer.mu.Lock()
	defer r.claimer.mu.Unlock()
	entry, ok := r.claimer.entries[r.key]
	if !ok || entry.token != r.token {
		return ErrLeaseExpired
	}
	entry.expiresAt = r.claimer.now().Add(ttl)
	return nil
}

var errStaleReceipt = errors.New("idempotency: receipt is stale (claim was released or expired)")

func newToken() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Entropy failure is non-recoverable; match stdlib uuid panic behaviour.
		panic("idempotency: crypto/rand.Read failed: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// Compile-time check that InMemClaimer satisfies Claimer.
var _ Claimer = (*InMemClaimer)(nil)

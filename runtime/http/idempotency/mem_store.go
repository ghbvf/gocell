package idempotency

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
)

// MemStore is an in-process Store implementation backed by a mutex-guarded map.
// It is safe for use in unit tests and single-pod demo deployments; it does NOT
// coordinate idempotency across replicas.
//
// Use a Redis-backed Store for multi-replica deployments.
type MemStore struct {
	mu      sync.Mutex
	entries map[string]*memEntry
	clk     clock.Clock
	rand    io.Reader
}

type memEntry struct {
	// leaseToken is the fencing token for the current in-flight lease.
	// Empty when the entry is in Done state.
	leaseToken string
	// leaseExpiry is when the in-flight lease expires.
	// Only meaningful when leaseToken != "".
	leaseExpiry time.Time
	// recorded is the response stored on Record; non-nil means Done state.
	recorded *RecordedResponse
	// doneExpiry is when the Done entry should be evicted.
	doneExpiry time.Time
}

// NewMemStore creates a new MemStore using the given clock.
// clock.MustHaveClock panics on nil clock (programmer-error guard).
func NewMemStore(clk clock.Clock) *MemStore {
	clock.MustHaveClock(clk, "idempotency.NewMemStore")
	return &MemStore{
		entries: make(map[string]*memEntry),
		clk:     clk,
		rand:    rand.Reader,
	}
}

// Claim implements Store.
func (ms *MemStore) Claim(ctx context.Context, ns, key string, leaseTTL time.Duration) (idempotency.ClaimState, *RecordedResponse, Receipt, error) { //nolint:lll // Store.Claim signature mirrors the interface; cannot shorten without breaking the interface contract
	ms.mu.Lock()
	defer ms.mu.Unlock()

	composed := ns + "\x00" + key
	now := ms.clk.Now()

	if e, ok := ms.entries[composed]; ok {
		// Done state: if not expired, replay.
		if e.recorded != nil && now.Before(e.doneExpiry) {
			return idempotency.ClaimDone, e.recorded, noopReceipt{}, nil
		}
		// In-flight lease: if not expired, busy.
		// nil *RecordedResponse here is intentional per Store.Claim contract (ClaimBusy has no response).
		if e.leaseToken != "" && now.Before(e.leaseExpiry) {
			// nil *RecordedResponse per Store.Claim contract: ClaimBusy has no response to replay.
			return idempotency.ClaimBusy, nil, noopReceipt{}, nil //nolint:nilnil // by design
		}
		// Expired entry — evict and fall through to fresh acquisition.
		delete(ms.entries, composed)
	}

	token, err := ms.generateToken()
	if err != nil {
		return idempotency.ClaimBusy, nil, noopReceipt{}, fmt.Errorf("idempotency.MemStore: generate token: %w", err)
	}

	ms.entries[composed] = &memEntry{
		leaseToken:  token,
		leaseExpiry: now.Add(leaseTTL),
	}
	// nil *RecordedResponse is intentional per Store.Claim contract (ClaimAcquired has no prior response).
	// nil *RecordedResponse per Store.Claim contract: ClaimAcquired has no prior response.
	return idempotency.ClaimAcquired, nil, &memReceipt{ //nolint:nilnil // by design
		store:    ms,
		composed: composed,
		token:    token,
	}, nil
}

func (ms *MemStore) generateToken() (string, error) {
	var b [16]byte // 128-bit fencing token; matches Redis adapter (idempotency.go claimToken)
	if _, err := io.ReadFull(ms.rand, b[:]); err != nil {
		return "", fmt.Errorf("rand read: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// memReceipt is the Receipt returned by MemStore.Claim for ClaimAcquired.
// Record and Release are each idempotent via their own bool guard: the first
// call performs the state transition and any subsequent calls are no-ops.
type memReceipt struct {
	store    *MemStore
	composed string
	token    string

	mu      sync.Mutex
	settled bool // true after Record or Release has completed
}

// Record persists resp and transitions the entry to Done state.
// A stale receipt (token no longer matches, or already settled) returns ErrLeaseExpired.
func (r *memReceipt) Record(ctx context.Context, resp *RecordedResponse, doneTTL time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.settled {
		return idempotency.ErrLeaseExpired
	}
	r.settled = true

	r.store.mu.Lock()
	defer r.store.mu.Unlock()

	e, ok := r.store.entries[r.composed]
	if !ok || e.leaseToken != r.token {
		return idempotency.ErrLeaseExpired
	}
	now := r.store.clk.Now()
	// Clone the response to prevent external mutation of the stored copy.
	body := resp.Body()
	hdr := resp.Header()
	stored := newRecordedResponse(r.store.clk, resp.Status(), body, hdr)
	r.store.entries[r.composed] = &memEntry{
		recorded:   &stored,
		doneExpiry: now.Add(doneTTL),
	}
	return nil
}

// Release drops the in-flight lease so a subsequent request may re-claim.
// A stale receipt (already settled) is a safe no-op that returns nil,
// because the most common Release path is a defer that always fires.
func (r *memReceipt) Release(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.settled {
		return nil
	}
	r.settled = true

	r.store.mu.Lock()
	defer r.store.mu.Unlock()

	e, ok := r.store.entries[r.composed]
	if !ok || e.leaseToken != r.token {
		return idempotency.ErrLeaseExpired
	}
	delete(r.store.entries, r.composed)
	return nil
}

// noopReceipt is the Receipt returned for ClaimDone and ClaimBusy states.
// Its methods return ErrNoClaimLease because no lease was acquired.
type noopReceipt struct{}

func (noopReceipt) Record(_ context.Context, _ *RecordedResponse, _ time.Duration) error {
	return idempotency.ErrNoClaimLease
}

func (noopReceipt) Release(_ context.Context) error {
	return idempotency.ErrNoClaimLease
}

// Compile-time check: MemStore satisfies Store.
var _ Store = (*MemStore)(nil)

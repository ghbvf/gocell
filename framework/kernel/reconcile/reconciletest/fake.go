// Package reconciletest provides public test-support fakes and conformance
// suites for kernel/reconcile consumers and LeaderElector adapters. It mirrors
// the kernel/outbox/outboxtest and kernel/cell/celltest test-support packages:
// adapters import it from their _test.go to run RunLeaderConformance /
// RunFencingConformance against their real implementation.
package reconciletest

import (
	"context"
	"sync"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/reconcile"
)

// Compile-time assertions that the fakes satisfy the kernel seams.
var (
	_ reconcile.LeaderElector    = (*FakeLeaderElector)(nil)
	_ reconcile.FencedRepository = (*FakeFencedRepository)(nil)
	_ reconcile.Reconciler       = FakeReconciler{}
	_ reconcile.Trigger          = FakeTrigger{}
)

// defaultFakeLeaseTTL is the lease TTL a FakeLeaderElector uses when constructed
// via Elector. Large enough that nothing expires mid-test unless Expire() is
// called or ElectorWithTTL picks a short TTL deliberately.
const defaultFakeLeaseTTL = 30 * time.Second

// FakeLeaseBackend is the shared in-memory lease store behind FakeLeaderElector.
// Two electors built from the SAME backend with the same reconcilerID contend
// for one lease — that is how the conformance suite simulates a multi-replica
// handoff. The monotonic epoch increments on every holder change (acquire of a
// free/expired lease) and is kept on an idempotent same-holder re-acquire of a
// still-live lease, mirroring the redis SETNX+INCR and postgres advisory-lock
// adapters.
type FakeLeaseBackend struct {
	mu     sync.Mutex
	clk    clock.Clock
	leases map[string]*fakeLeaseState
}

type fakeLeaseState struct {
	holder     string
	epoch      uint64
	acquiredAt time.Time
	expiresAt  time.Time
}

// NewFakeLeaseBackend returns an empty shared lease backend. clk stamps the lease
// window (callers pass clock.Real() or a test clock).
func NewFakeLeaseBackend(clk clock.Clock) *FakeLeaseBackend {
	clock.MustHaveClock(clk, "reconciletest.FakeLeaseBackend")
	return &FakeLeaseBackend{clk: clk, leases: make(map[string]*fakeLeaseState)}
}

// Elector returns a FakeLeaderElector for holderID backed by b with the default
// TTL. Build two with distinct holderIDs from one backend to test handoff.
func (b *FakeLeaseBackend) Elector(holderID string) *FakeLeaderElector {
	return b.ElectorWithTTL(holderID, defaultFakeLeaseTTL)
}

// ElectorWithTTL returns a FakeLeaderElector with an explicit lease TTL (used by
// expiry/takeover tests that need a short TTL).
func (b *FakeLeaseBackend) ElectorWithTTL(holderID string, ttl time.Duration) *FakeLeaderElector {
	return &FakeLeaderElector{backend: b, holderID: holderID, ttl: ttl}
}

// Expire forces the current lease for reconcilerID to be expired (test helper for
// "follower takes over on expiry" — deterministic, no wall-clock wait).
func (b *FakeLeaseBackend) Expire(reconcilerID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if st := b.leases[reconcilerID]; st != nil {
		st.expiresAt = b.clk.Now().Add(-time.Second)
	}
}

// FakeLeaderElector is an in-memory reconcile.LeaderElector for one holder.
type FakeLeaderElector struct {
	backend  *FakeLeaseBackend
	holderID string
	ttl      time.Duration
}

// AcquireLease implements reconcile.LeaderElector. It returns ErrLeaseHeld when a
// different holder owns a still-live lease (contention), bumps the monotonic
// epoch on a takeover of a free/expired lease, and keeps the epoch on an
// idempotent same-holder re-acquire of a live lease.
func (e *FakeLeaderElector) AcquireLease(ctx context.Context, reconcilerID string) (reconcile.LeaseToken, error) {
	if err := ctx.Err(); err != nil {
		return reconcile.LeaseToken{}, err
	}
	b := e.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clk.Now()
	st := b.leases[reconcilerID]
	live := st != nil && st.expiresAt.After(now)
	switch {
	case live && st.holder != e.holderID:
		return reconcile.LeaseToken{}, reconcile.ErrLeaseHeld
	case live && st.holder == e.holderID:
		// idempotent re-acquire of our own live lease: keep epoch
	case st == nil:
		st = &fakeLeaseState{holder: e.holderID, epoch: 1}
	default:
		// free / expired: takeover bumps the monotonic epoch
		st.holder = e.holderID
		st.epoch++
	}
	st.acquiredAt = now
	st.expiresAt = now.Add(e.ttl)
	b.leases[reconcilerID] = st
	return reconcile.LeaseToken{
		ReconcilerID: reconcilerID,
		HolderID:     e.holderID,
		Epoch:        st.epoch,
		AcquiredAt:   st.acquiredAt,
		ExpiresAt:    st.expiresAt,
	}, nil
}

// ReleaseLease implements reconcile.LeaderElector. It frees our lease (idempotent;
// releasing a lease we no longer hold is a no-op). The next AcquireLease by any
// holder takes over and bumps the epoch (a release is a holder-change gap).
func (e *FakeLeaderElector) ReleaseLease(ctx context.Context, token reconcile.LeaseToken) error {
	b := e.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	if st := b.leases[token.ReconcilerID]; st != nil && st.holder == e.holderID {
		st.expiresAt = b.clk.Now().Add(-time.Second) // mark free
	}
	return nil
}

// RenewLease implements reconcile.LeaderElector. It extends our lease (keeping the
// epoch) or returns ErrReconcileLeaseLost when the lease is no longer ours.
func (e *FakeLeaderElector) RenewLease(ctx context.Context, token reconcile.LeaseToken) error {
	b := e.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clk.Now()
	st := b.leases[token.ReconcilerID]
	if st == nil || st.holder != e.holderID || !st.expiresAt.After(now) {
		return reconcile.ErrReconcileLeaseLost
	}
	st.expiresAt = now.Add(e.ttl)
	return nil
}

// FencedEffect records one accepted ApplyFenced write for assertions.
type FencedEffect struct {
	EntityID string
	Epoch    uint64
	Mutation any
}

// FakeFencedRepository is an in-memory reconcile.FencedRepository implementing the
// monotonic-epoch CAS: it rejects (accepted=false) any write whose epoch is
// strictly older than the highest epoch already seen for that entity, and records
// each accepted write so a test can assert no duplicate effect from a stale replay.
type FakeFencedRepository struct {
	mu        sync.Mutex
	lastEpoch map[string]uint64
	effects   []FencedEffect
}

// NewFakeFencedRepository returns an empty fenced repository.
func NewFakeFencedRepository() *FakeFencedRepository {
	return &FakeFencedRepository{lastEpoch: make(map[string]uint64)}
}

// ApplyFenced implements reconcile.FencedRepository (monotonic CAS).
func (r *FakeFencedRepository) ApplyFenced(ctx context.Context, entityID string, epoch uint64, mutation any) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if epoch < r.lastEpoch[entityID] {
		return false, nil // stale epoch — reject
	}
	r.lastEpoch[entityID] = epoch
	r.effects = append(r.effects, FencedEffect{EntityID: entityID, Epoch: epoch, Mutation: mutation})
	return true, nil
}

// LastEpoch returns the highest accepted epoch for entityID (0 if none).
func (r *FakeFencedRepository) LastEpoch(entityID string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastEpoch[entityID]
}

// Effects returns a copy of all accepted writes in order.
func (r *FakeFencedRepository) Effects() []FencedEffect {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]FencedEffect, len(r.effects))
	copy(out, r.effects)
	return out
}

// FakeReconciler is a reconcile.Reconciler driven by a func (nil → no-op success).
type FakeReconciler struct {
	Fn func(ctx context.Context, req reconcile.Request) (reconcile.Result, error)
}

// Reconcile implements reconcile.Reconciler.
func (f FakeReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	if f.Fn != nil {
		return f.Fn(ctx, req)
	}
	return reconcile.Result{}, nil
}

// FakeTrigger forwards Requests from In into the Loop's queue until ctx is
// canceled or In is closed. It satisfies reconcile.Trigger.
type FakeTrigger struct {
	In <-chan reconcile.Request
}

// Start implements reconcile.Trigger (non-blocking; spawns one forwarder).
func (t FakeTrigger) Start(ctx context.Context, queue chan<- reconcile.Request) error {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case req, ok := <-t.In:
				if !ok {
					return
				}
				select {
				case queue <- req:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return nil
}

// NewFakeTrigger creates a FakeTrigger backed by a fresh buffered channel and
// returns a submit function that injects a Request into that channel. The
// submit function blocks briefly if the buffer is full; the buffer (size 64)
// is large enough for any conformance use-case. This is the standard
// Wiring.NewTrigger implementation for RunConformance.
func NewFakeTrigger() (trigger FakeTrigger, submit func(reconcile.Request)) {
	ch := make(chan reconcile.Request, 64)
	submit = func(req reconcile.Request) { ch <- req }
	return FakeTrigger{In: ch}, submit
}

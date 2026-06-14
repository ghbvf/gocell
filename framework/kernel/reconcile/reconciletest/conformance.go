package reconciletest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/reconcile"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
)

const (
	holderA        = "holder-A"
	holderB        = "holder-B"
	acquireA       = "acquire A"
	releaseA       = "release A"
	acquireB       = "acquire B"
	basicEntityID  = "basic-entity-1"
	leaderEntityID = "leader-entity-1"
)

// ElectorFactory builds a LeaderElector for the given holderID, all sharing one
// backend (same redis client / pg pool / FakeLeaseBackend) so two holders
// contend for the same lease. Distinct holderIDs simulate distinct replicas.
type ElectorFactory func(holderID string) reconcile.LeaderElector

// RunLeaderConformance exercises a LeaderElector implementation across the
// deterministic, wall-clock-free parts of the contract: mutual exclusion, graceful
// release handoff, renew keeping ownership + epoch, and monotonic epoch bump on
// holder change. Both adapters (redis miniredis, postgres integration) and the
// fake run it. TTL-expiry takeover is wall-clock-dependent and is covered by
// implementation-specific tests (the fake's Expire helper, miniredis FastForward).
//
// Plain testing.T assertions (no testify): this is a kernel test-support package
// and the kernel-isolation depguard bans testify outside *_test.go.
func RunLeaderConformance(t *testing.T, newElector ElectorFactory) {
	t.Helper()
	t.Run("AcquireExclusive", func(t *testing.T) { confAcquireExclusive(t, newElector) })
	t.Run("ReleaseUnblocksFollower", func(t *testing.T) { confReleaseUnblocksFollower(t, newElector) })
	t.Run("RenewKeepsLeaseAndEpoch", func(t *testing.T) { confRenewKeepsLeaseAndEpoch(t, newElector) })
	t.Run("HandoffBumpsEpoch", func(t *testing.T) { confHandoffBumpsEpoch(t, newElector) })
	t.Run("ReacquireAfterReleaseBumpsEpoch", func(t *testing.T) { confReacquireAfterReleaseBumps(t, newElector) })
	t.Run("RenewAfterTakeoverIsLost", func(t *testing.T) { confRenewAfterTakeoverIsLost(t, newElector) })
	t.Run("ReleaseAfterTakeoverIsNoop", func(t *testing.T) { confReleaseAfterTakeoverIsNoop(t, newElector) })
}

// confReacquireAfterReleaseBumps pins the fencing contract for a SAME-holder
// release→reacquire: it MUST bump the epoch (a release frees the lease — a gap —
// so the reacquire is a takeover of a free lease, conservatively fencing the
// holder's own pre-release in-flight writes). Uses one elector instance so the
// holderID is stable across the release/reacquire.
func confReacquireAfterReleaseBumps(t *testing.T, newElector ElectorFactory) {
	ctx := context.Background()
	const rid = "conf-reacquire"
	a := newElector(holderA)

	tok1, err := a.AcquireLease(ctx, rid)
	mustNoErr(t, err, "first acquire")
	mustNoErr(t, a.ReleaseLease(ctx, tok1), "release")

	tok2, err := a.AcquireLease(ctx, rid)
	mustNoErr(t, err, "reacquire by same holder")
	defer func() { _ = a.ReleaseLease(ctx, tok2) }()
	if tok2.Epoch <= tok1.Epoch {
		t.Fatalf("same-holder reacquire after release must bump the epoch: got %d, want > %d", tok2.Epoch, tok1.Epoch)
	}
}

func confAcquireExclusive(t *testing.T, newElector ElectorFactory) {
	ctx := context.Background()
	const rid = "conf-exclusive"
	a, b := newElector(holderA), newElector(holderB)

	tokA, err := a.AcquireLease(ctx, rid)
	mustNoErr(t, err, "first holder must acquire")
	defer func() { _ = a.ReleaseLease(ctx, tokA) }() // release held resources (PG session conn)

	if _, err = b.AcquireLease(ctx, rid); !errors.Is(err, reconcile.ErrLeaseHeld) {
		t.Fatalf("contention must report ErrLeaseHeld (logged at Debug); got %v", err)
	}
}

func confReleaseUnblocksFollower(t *testing.T, newElector ElectorFactory) {
	ctx := context.Background()
	const rid = "conf-release"
	a, b := newElector(holderA), newElector(holderB)

	tokA, err := a.AcquireLease(ctx, rid)
	mustNoErr(t, err, acquireA)
	mustNoErr(t, a.ReleaseLease(ctx, tokA), releaseA)

	tokB, err := b.AcquireLease(ctx, rid)
	mustNoErr(t, err, "follower must acquire after the leader releases")
	_ = b.ReleaseLease(ctx, tokB)
}

func confRenewKeepsLeaseAndEpoch(t *testing.T, newElector ElectorFactory) {
	ctx := context.Background()
	const rid = "conf-renew"
	a, b := newElector(holderA), newElector(holderB)

	tokA, err := a.AcquireLease(ctx, rid)
	mustNoErr(t, err, acquireA)
	defer func() { _ = a.ReleaseLease(ctx, tokA) }()
	mustNoErr(t, a.RenewLease(ctx, tokA), "renew must succeed while held")

	if _, err = b.AcquireLease(ctx, rid); !errors.Is(err, reconcile.ErrLeaseHeld) {
		t.Fatalf("renew must keep the follower locked out; got %v", err)
	}

	// Idempotent same-holder re-acquire of a live lease keeps the epoch.
	tokA2, err := a.AcquireLease(ctx, rid)
	mustNoErr(t, err, "idempotent re-acquire")
	if tokA2.Epoch != tokA.Epoch {
		t.Fatalf("same-holder re-acquire of a live lease must keep the epoch: got %d, want %d", tokA2.Epoch, tokA.Epoch)
	}
}

func confHandoffBumpsEpoch(t *testing.T, newElector ElectorFactory) {
	ctx := context.Background()
	const rid = "conf-handoff"
	a, b := newElector(holderA), newElector(holderB)

	tokA, err := a.AcquireLease(ctx, rid)
	mustNoErr(t, err, acquireA)
	mustNoErr(t, a.ReleaseLease(ctx, tokA), releaseA)

	tokB, err := b.AcquireLease(ctx, rid)
	mustNoErr(t, err, acquireB)
	defer func() { _ = b.ReleaseLease(ctx, tokB) }()
	if tokB.Epoch <= tokA.Epoch {
		t.Fatalf("a holder change must bump the monotonic epoch: B=%d not > A=%d", tokB.Epoch, tokA.Epoch)
	}
}

func confRenewAfterTakeoverIsLost(t *testing.T, newElector ElectorFactory) {
	ctx := context.Background()
	const rid = "conf-renew-lost"
	a, b := newElector(holderA), newElector(holderB)

	tokA, err := a.AcquireLease(ctx, rid)
	mustNoErr(t, err, acquireA)
	mustNoErr(t, a.ReleaseLease(ctx, tokA), releaseA)
	tokB, err := b.AcquireLease(ctx, rid)
	mustNoErr(t, err, acquireB)
	defer func() { _ = b.ReleaseLease(ctx, tokB) }()

	// A's renew must now report the lease lost (B owns it).
	if err := a.RenewLease(ctx, tokA); !errors.Is(err, reconcile.ErrReconcileLeaseLost) {
		t.Fatalf("renew after takeover must report ErrReconcileLeaseLost (drives lost-lease ctx cancel); got %v", err)
	}
}

// confReleaseAfterTakeoverIsNoop pins fencing safety: a stale holder releasing its
// OLD token AFTER another holder has taken over must NOT free the new holder's
// lease. Otherwise a laggard's delayed ReleaseLease could hand the lease to a third
// contender while the legitimate holder still believes it leads (split-brain).
func confReleaseAfterTakeoverIsNoop(t *testing.T, newElector ElectorFactory) {
	ctx := context.Background()
	const rid = "conf-release-after-takeover"
	a, b, c := newElector(holderA), newElector(holderB), newElector("holder-C")

	tokA, err := a.AcquireLease(ctx, rid)
	mustNoErr(t, err, acquireA)
	mustNoErr(t, a.ReleaseLease(ctx, tokA), "A releases")

	tokB, err := b.AcquireLease(ctx, rid)
	mustNoErr(t, err, "B takes over")
	defer func() { _ = b.ReleaseLease(ctx, tokB) }()

	// Stale release: A releases its OLD token AGAIN, now that B owns the lease.
	// Must be a no-op (not an error, and must not free B's lease).
	mustNoErr(t, a.ReleaseLease(ctx, tokA), "stale release by old holder must be a no-op")

	// C must still be locked out — B's lease was NOT freed by A's stale release.
	if _, err := c.AcquireLease(ctx, rid); !errors.Is(err, reconcile.ErrLeaseHeld) {
		t.Fatalf("stale release by old holder must NOT free the new holder's lease; C got err=%v", err)
	}
	// And B must still hold (its renew succeeds).
	mustNoErr(t, b.RenewLease(ctx, tokB), "new holder must still hold after a stale release")
}

// RunFencingConformance is the real-failure-injection: a zombie old leader's write
// at epoch N, replayed AFTER a follower took over at epoch N+1, MUST be rejected by
// the monotonic-epoch CAS, and MUST leave no duplicate effect. It pairs the real
// elector (which must produce a strictly higher epoch on handoff) with a
// FakeFencedRepository standing in for a consumer's epoch-aware store (no real
// consumer ships in PR-A6). This proves "leader election ≠ fencing": even though
// both leaders briefly believed they led, only the higher-epoch write lands.
//
// Uses plain testing.T (no testify) per the kernel test-support isolation rule.
//
// This drives ApplyFenced directly to validate elector epoch monotonicity + CAS;
// the Loop end-to-end FencedWriter path is covered by loop_leader_test.go.
func RunFencingConformance(t *testing.T, newElector ElectorFactory) {
	t.Helper()
	ctx := context.Background()
	const (
		rid    = "fence-rid"
		entity = "device-1"
	)

	a := newElector(holderA)
	tokA, err := a.AcquireLease(ctx, rid)
	mustNoErr(t, err, acquireA)
	mustNoErr(t, a.ReleaseLease(ctx, tokA), "old leader hands off")

	b := newElector(holderB)
	tokB, err := b.AcquireLease(ctx, rid)
	mustNoErr(t, err, acquireB)
	defer func() { _ = b.ReleaseLease(ctx, tokB) }()
	if tokB.Epoch <= tokA.Epoch {
		t.Fatalf("takeover must bump the fencing epoch: B=%d not > A=%d", tokB.Epoch, tokA.Epoch)
	}

	repo := NewFakeFencedRepository()

	// New leader (epoch N+1) applies its write — accepted.
	accepted, err := repo.ApplyFenced(ctx, entity, tokB.Epoch, "command-from-new-leader")
	mustNoErr(t, err, "new-leader write")
	if !accepted {
		t.Fatal("the current leader's write must be accepted")
	}

	// Zombie old leader (epoch N) replays its in-flight write AFTER the handoff —
	// the monotonic CAS must reject it.
	accepted, err = repo.ApplyFenced(ctx, entity, tokA.Epoch, "command-from-zombie-leader")
	mustNoErr(t, err, "zombie-leader write")
	if accepted {
		t.Fatal("stale-epoch zombie write must be rejected by the fencing CAS")
	}

	// No duplicate command: exactly one effect, from the new leader.
	if got := repo.LastEpoch(entity); got != tokB.Epoch {
		t.Fatalf("last accepted epoch = %d, want %d (the new leader's)", got, tokB.Epoch)
	}
	effects := repo.Effects()
	if len(effects) != 1 {
		t.Fatalf("the stale replay must not produce a duplicate effect: got %d effects", len(effects))
	}
	if effects[0].Mutation != "command-from-new-leader" {
		t.Fatalf("the surviving effect must be the new leader's: got %v", effects[0].Mutation)
	}
}

// mustNoErr fails the test fatally if err is non-nil (testify-free helper).
func mustNoErr(t *testing.T, err error, msg string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", msg, err)
	}
}

// ── Loop end-to-end conformance harness (FR-013) ────────────────────────────
//
// RunConformance exercises the Loop's scheduling contracts end-to-end.
// It mirrors commandtest.RunQueueConformance: each subtest calls newHarness
// fresh, constructs its own Loop via the public Builder (reconcile.New), and
// asserts the Loop's behavior under controlled conditions.
//
// The existing RunLeaderConformance / RunFencingConformance / ElectorFactory
// (elector-level adapter contract) are untouched; RunConformance tests the
// Loop scheduling layer, not the LeaderElector adapter.
//
// Plain testing.T assertions (no testify): this is a kernel test-support
// package and the kernel-isolation depguard bans testify outside *_test.go.

// Wiring is the consumer-supplied plug-in set the conformance suite drives a
// Loop with. Each subtest calls HarnessFactory fresh so all state is isolated.
type Wiring struct {
	// NewTrigger returns a fresh Trigger plus a submit func that injects a
	// Request into that trigger's source so the suite can drive work
	// deterministically.
	// Standard impl: func() (reconcile.Trigger, func(reconcile.Request)) { return reconciletest.NewFakeTrigger() }
	NewTrigger func() (trigger reconcile.Trigger, submit func(reconcile.Request))
	// Leader is the LeaderElector for the loop under test; nil when
	// Features.Leader is false.
	Leader reconcile.LeaderElector
	// Fenced is the consumer's epoch-aware repo; nil when Features.Fencing
	// is false.
	Fenced reconcile.FencedRepository
	// Cleanup releases any resources (nil ok — called with defer after each
	// subtest).
	Cleanup func()
}

// HarnessFactory builds a fresh Wiring for one subtest invocation.
// It mirrors commandtest.QueueFactory.
type HarnessFactory func(t *testing.T) Wiring

// Features captures which optional Loop contracts the harness supports.
// Fencing requires Leader=true; RunConformance fatals immediately when
// Fencing=true and Leader=false.
type Features struct {
	Leader  bool // exercise LeaderFlow subtest
	Fencing bool // exercise Fencing subtest (requires Leader)
}

// conformance timing constants (TEST-TIME-LITERAL-01: no inline literals).
const (
	confShortInterval = 20 * time.Millisecond  // loop interval for fast requeue
	confEventualWait  = 3 * time.Second        // budget for require.Eventually-style polls
	confPollTick      = 5 * time.Millisecond   // polling frequency inside wait loops
	confQuietPeriod   = 80 * time.Millisecond  // quiet-period check for PermanentError
	confLeaderRenew   = 10 * time.Millisecond  // fast renew cadence for leader tests
	confBarrierWait   = 2 * time.Second        // barrier wait for concurrency test
	confBackoffMax    = confShortInterval * 10 // panic-recovery backoff cap (TEST-TIME-LITERAL-01)
)

// confStop stops l with a bounded drain budget and FAILS the test if the drain
// does not complete in time. Discarding the Stop error would mask a stuck drain
// (leaked goroutines / hung reconcile) as a false green — the goleak guard is a
// backstop, but a timed-out Stop is a direct contract violation worth a failure.
func confStop(t *testing.T, l *reconcile.Loop) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), confEventualWait)
	defer cancel()
	if err := l.Stop(ctx); err != nil {
		t.Errorf("Loop.Stop: drain did not complete within %s: %v", confEventualWait, err)
	}
}

// RunConformance runs the Loop scheduling conformance suite.
// Subtests are skipped (not failed) when the corresponding feature is off.
//
// Features.Fencing requires Features.Leader=true; RunConformance fatals immediately
// if Fencing=true and Leader=false.
func RunConformance(t *testing.T, newHarness HarnessFactory, features Features) {
	t.Helper()
	if features.Fencing && !features.Leader {
		t.Fatal("reconciletest.RunConformance: Features.Fencing requires Features.Leader=true")
	}
	t.Run("BasicReconcile", func(t *testing.T) {
		confBasicReconcile(t, newHarness)
	})
	t.Run("RequeueAfter", func(t *testing.T) {
		confRequeueAfter(t, newHarness)
	})
	t.Run("PermanentError", func(t *testing.T) {
		confPermanentError(t, newHarness)
	})
	t.Run("PanicRecovery", func(t *testing.T) {
		confPanicRecovery(t, newHarness)
	})
	t.Run("MaxConcurrentReconciles", func(t *testing.T) {
		confMaxConcurrent(t, newHarness)
	})
	t.Run("LeaderFlow", func(t *testing.T) {
		if !features.Leader {
			t.Skip("LeaderFlow requires Features.Leader=true")
		}
		confLeaderFlow(t, newHarness)
	})
	t.Run("Fencing", func(t *testing.T) {
		if !features.Fencing {
			t.Skip("Fencing requires Features.Fencing=true")
		}
		confFencing(t, newHarness)
	})
}

// confBasicReconcile verifies the happy path: a submitted Request is
// dispatched to the Reconciler exactly once (on the first observation).
func confBasicReconcile(t *testing.T, newHarness HarnessFactory) {
	t.Helper()
	w := newHarness(t)
	if w.Cleanup != nil {
		defer w.Cleanup()
	}

	trigger, submit := w.NewTrigger()
	invoked := make(chan reconcile.Request, 1)
	rec := FakeReconciler{Fn: func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
		select {
		case invoked <- req:
		default:
		}
		return reconcile.Result{RequeueAfter: time.Hour}, nil // suppress fast requeue
	}}

	l, err := reconcile.New(rec, reconcile.SingleTenant()).
		WithTrigger(trigger).
		WithInterval(confShortInterval).
		Build()
	mustNoErr(t, err, "Build")

	ownerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mustNoErr(t, l.Start(ownerCtx), "Start")
	defer confStop(t, l)

	submit(reconcile.Request{EntityID: basicEntityID})

	select {
	case got := <-invoked:
		if got.EntityID != basicEntityID {
			t.Fatalf("BasicReconcile: got entity %q, want %q", got.EntityID, basicEntityID)
		}
	case <-time.After(confEventualWait):
		t.Fatal("BasicReconcile: Reconcile never called within budget")
	}
}

// confRequeueAfter verifies that returning Result{RequeueAfter: d>0} causes
// the entity to be re-invoked at least twice within the wait budget.
func confRequeueAfter(t *testing.T, newHarness HarnessFactory) {
	t.Helper()
	w := newHarness(t)
	if w.Cleanup != nil {
		defer w.Cleanup()
	}

	trigger, submit := w.NewTrigger()
	var count atomic.Int64
	rec := FakeReconciler{Fn: func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
		if count.Add(1) == 1 {
			return reconcile.Result{RequeueAfter: confShortInterval}, nil
		}
		return reconcile.Result{RequeueAfter: time.Hour}, nil // stop after 2nd
	}}

	l, err := reconcile.New(rec, reconcile.SingleTenant()).
		WithTrigger(trigger).
		WithInterval(confShortInterval).
		Build()
	mustNoErr(t, err, "Build")

	ownerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mustNoErr(t, l.Start(ownerCtx), "Start")
	defer confStop(t, l)

	submit(reconcile.Request{EntityID: "requeue-entity-1"})

	testwait.External(t, "reconcile-requeued", func() bool {
		return count.Load() >= 2
	}, confEventualWait, confPollTick, "RequeueAfter: entity not re-invoked ≥ 2 times")
}

// confPermanentError verifies that a PermanentError response causes exactly one
// Reconcile invocation (dead-letter: no requeue).
func confPermanentError(t *testing.T, newHarness HarnessFactory) {
	t.Helper()
	w := newHarness(t)
	if w.Cleanup != nil {
		defer w.Cleanup()
	}

	trigger, submit := w.NewTrigger()
	var count atomic.Int64
	rec := FakeReconciler{Fn: func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
		count.Add(1)
		return reconcile.Result{}, reconcile.PermanentError(errors.New("unrecoverable"))
	}}

	l, err := reconcile.New(rec, reconcile.SingleTenant()).
		WithTrigger(trigger).
		WithInterval(confShortInterval).
		Build()
	mustNoErr(t, err, "Build")

	ownerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mustNoErr(t, l.Start(ownerCtx), "Start")
	defer confStop(t, l)

	submit(reconcile.Request{EntityID: "perm-entity-1"})

	// Wait until invoked at least once.
	testwait.External(t, "reconcile-invoked-once", func() bool {
		return count.Load() >= 1
	}, confEventualWait, confPollTick, "PermanentError: Reconcile never called")

	// Quiet period: no further invocation expected after dead-letter. This is a
	// genuine sleep (asserting the ABSENCE of an event cannot be polled-for) —
	// not synchronous polling, so testwait does not apply.
	snapshot := count.Load()
	time.Sleep(confQuietPeriod) //archtest:allow:test-sleep quiet-period asserts no re-run after dead-letter (absence cannot be polled)
	if after := count.Load(); after != snapshot {
		t.Fatalf("PermanentError: invoked %d more time(s) after dead-letter (expected 0 re-runs)", after-snapshot)
	}
}

// confPanicRecovery verifies that a panic in the Reconciler is recovered by
// the Loop (no crash) and the entity is retried (transient path), resulting
// in at least 2 total invocations.
func confPanicRecovery(t *testing.T, newHarness HarnessFactory) {
	t.Helper()
	w := newHarness(t)
	if w.Cleanup != nil {
		defer w.Cleanup()
	}

	trigger, submit := w.NewTrigger()
	var count atomic.Int64
	rec := FakeReconciler{Fn: func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
		n := count.Add(1)
		if n == 1 {
			// Simulated reconciler panic to exercise the Loop's recover→transient
			// path. conformance.go is a production (non-_test.go) test-support file,
			// so PANIC-REGISTERED-01 requires the panicregister.Approved wrap.
			panic(panicregister.Approved("reconcile-conformance-panic-probe",
				errcode.Assertion("conformance: simulated reconciler panic for panic-recovery test")))
		}
		return reconcile.Result{RequeueAfter: time.Hour}, nil
	}}

	l, err := reconcile.New(rec, reconcile.SingleTenant()).
		WithTrigger(trigger).
		WithInterval(confShortInterval).
		WithBackoff(confShortInterval, confBackoffMax).
		Build()
	mustNoErr(t, err, "Build")

	ownerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mustNoErr(t, l.Start(ownerCtx), "Start")
	defer confStop(t, l)

	submit(reconcile.Request{EntityID: "panic-entity-1"})

	testwait.External(t, "reconcile-retried-after-panic", func() bool {
		return count.Load() >= 2
	}, confEventualWait, confPollTick, "PanicRecovery: panic not recovered and retried")
}

// confMaxConcurrent verifies two properties of MaxConcurrentReconciles > 1:
//  1. Cross-entity concurrency: multiple distinct entities can be reconciled
//     concurrently (in-flight count reaches > 1).
//  2. Same-entity serialization: two submits of the same entity never overlap.
func confMaxConcurrent(t *testing.T, newHarness HarnessFactory) {
	t.Helper()
	w := newHarness(t)
	if w.Cleanup != nil {
		defer w.Cleanup()
	}
	confMaxConcurrentCrossEntity(t, w)
	confMaxConcurrentSameEntity(t, w)
}

// confMaxConcurrentCrossEntity checks that multiple distinct entities are
// reconciled concurrently (in-flight count reaches the worker limit).
func confMaxConcurrentCrossEntity(t *testing.T, w Wiring) {
	t.Helper()
	const workers = 3
	trigger, submit := w.NewTrigger()

	var (
		mu       sync.Mutex
		inflight int
		maxSeen  int
	)
	barrier := make(chan struct{})
	var barrierOnce sync.Once

	rec := FakeReconciler{Fn: func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
		mu.Lock()
		inflight++
		if inflight > maxSeen {
			maxSeen = inflight
		}
		if inflight >= workers {
			barrierOnce.Do(func() { close(barrier) })
		}
		mu.Unlock()

		select {
		case <-barrier:
		case <-ctx.Done():
		case <-time.After(confBarrierWait):
		}

		mu.Lock()
		inflight--
		mu.Unlock()
		return reconcile.Result{RequeueAfter: time.Hour}, nil
	}}

	l, err := reconcile.New(rec, reconcile.SingleTenant()).
		WithTrigger(trigger).
		WithConcurrency(workers).
		WithInterval(confShortInterval).
		Build()
	mustNoErr(t, err, "Build cross-entity")

	ownerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mustNoErr(t, l.Start(ownerCtx), "Start cross-entity")
	defer confStop(t, l)

	for i := 0; i < workers; i++ {
		submit(reconcile.Request{EntityID: string(rune('A' + i))})
	}

	select {
	case <-barrier:
	case <-time.After(confEventualWait):
		mu.Lock()
		seen := maxSeen
		mu.Unlock()
		t.Fatalf("MaxConcurrentReconciles cross-entity: expected %d concurrent workers; only reached %d", workers, seen)
	}
}

// confMaxConcurrentSameEntity checks that two submits of the same entity never
// run concurrently (the Loop's dirty/processing serialization guarantee) AND
// that the coalesced second submit actually triggers a re-run (same-entity
// serialization contract = coalesce-into-dirty-rerun, not drop).
func confMaxConcurrentSameEntity(t *testing.T, w Wiring) {
	t.Helper()
	trigger, submit := w.NewTrigger()

	var (
		calls           atomic.Int64
		overlapDetected bool
		sameInflight    int
		sameEntityMu    sync.Mutex
		sameBarrier     = make(chan struct{})
		sameBarrierOnce sync.Once
		sameDone        = make(chan struct{})
	)

	rec := FakeReconciler{Fn: func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
		calls.Add(1)

		sameEntityMu.Lock()
		sameInflight++
		if sameInflight > 1 {
			overlapDetected = true
		}
		sameEntityMu.Unlock()

		sameBarrierOnce.Do(func() { close(sameBarrier) })
		select {
		case <-sameDone:
		case <-ctx.Done():
		case <-time.After(confBarrierWait):
		}

		sameEntityMu.Lock()
		sameInflight--
		sameEntityMu.Unlock()
		return reconcile.Result{RequeueAfter: time.Hour}, nil
	}}

	l, err := reconcile.New(rec, reconcile.SingleTenant()).
		WithTrigger(trigger).
		WithConcurrency(2).
		WithInterval(confShortInterval).
		Build()
	mustNoErr(t, err, "Build same-entity")

	ownerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mustNoErr(t, l.Start(ownerCtx), "Start same-entity")
	defer confStop(t, l)

	submit(reconcile.Request{EntityID: "same-entity"})
	submit(reconcile.Request{EntityID: "same-entity"})

	select {
	case <-sameBarrier:
	case <-time.After(confEventualWait):
		t.Fatal("MaxConcurrentReconciles same-entity: first invocation never started")
	}
	close(sameDone)

	// The coalesced second submit must actually re-run (dirty re-run guarantee):
	// verify ≥2 invocations before checking the overlap flag.
	testwait.External(t, "same-entity-rerun", func() bool { return calls.Load() >= 2 },
		confEventualWait, confPollTick,
		"same-entity coalesced re-run never executed")

	sameEntityMu.Lock()
	detected := overlapDetected
	sameEntityMu.Unlock()
	if detected {
		t.Fatal("MaxConcurrentReconciles same-entity: two reconciles ran concurrently for the same entity")
	}
}

// confLeaderFlow verifies that a Loop with a LeaderElector acquires leadership
// and dispatches submitted work end-to-end.
func confLeaderFlow(t *testing.T, newHarness HarnessFactory) {
	t.Helper()
	w := newHarness(t)
	if w.Cleanup != nil {
		defer w.Cleanup()
	}
	if w.Leader == nil {
		t.Fatal("LeaderFlow: Wiring.Leader must be non-nil when Features.Leader=true")
	}

	trigger, submit := w.NewTrigger()
	dispatched := make(chan reconcile.Request, 1)
	rec := FakeReconciler{Fn: func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
		select {
		case dispatched <- req:
		default:
		}
		return reconcile.Result{RequeueAfter: time.Hour}, nil
	}}

	l, err := reconcile.New(rec, reconcile.SingleTenant()).
		WithTrigger(trigger).
		WithLeader(w.Leader).
		WithRenewInterval(confLeaderRenew).
		WithInterval(confShortInterval).
		Build()
	mustNoErr(t, err, "Build")

	ownerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mustNoErr(t, l.Start(ownerCtx), "Start")
	defer confStop(t, l)

	submit(reconcile.Request{EntityID: leaderEntityID})

	select {
	case got := <-dispatched:
		if got.EntityID != leaderEntityID {
			t.Fatalf("LeaderFlow: got entity %q, want %q", got.EntityID, leaderEntityID)
		}
	case <-time.After(confEventualWait):
		t.Fatal("LeaderFlow: work not dispatched within budget — Loop may not have acquired leadership")
	}
}

// confFencing verifies the end-to-end FencedWriter path: a Reconciler that uses
// FencedWriterFrom(ctx) to apply a write sees it accepted at the live epoch, and
// the FencedRepository records the effect.
func confFencing(t *testing.T, newHarness HarnessFactory) {
	t.Helper()
	w := newHarness(t)
	if w.Cleanup != nil {
		defer w.Cleanup()
	}
	if w.Leader == nil {
		t.Fatal("Fencing: Wiring.Leader must be non-nil when Features.Fencing=true")
	}
	if w.Fenced == nil {
		t.Fatal("Fencing: Wiring.Fenced must be non-nil when Features.Fencing=true")
	}

	trigger, submit := w.NewTrigger()
	written := make(chan uint64, 1)
	rec := FakeReconciler{Fn: func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
		fw, ok := reconcile.FencedWriterFrom(ctx)
		if !ok {
			// Not yet leader — skip silently (leaderManage may still be acquiring).
			return reconcile.Result{RequeueAfter: confShortInterval}, nil
		}
		if err := fw.Write(ctx, req.EntityID, "mutation"); err != nil {
			return reconcile.Result{}, err
		}
		select {
		case written <- fw.Epoch():
		default:
		}
		return reconcile.Result{RequeueAfter: time.Hour}, nil
	}}

	l, err := reconcile.New(rec, reconcile.SingleTenant()).
		WithTrigger(trigger).
		WithLeader(w.Leader).
		WithFencedRepo(w.Fenced).
		WithRenewInterval(confLeaderRenew).
		WithInterval(confShortInterval).
		Build()
	mustNoErr(t, err, "Build")

	ownerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mustNoErr(t, l.Start(ownerCtx), "Start")
	defer confStop(t, l)

	submit(reconcile.Request{EntityID: "fence-entity-1"})

	var epoch uint64
	select {
	case epoch = <-written:
	case <-time.After(confEventualWait):
		t.Fatal("Fencing: FencedWriter.Write never succeeded within budget")
	}
	if epoch == 0 {
		t.Fatal("Fencing: FencedWriter must carry a non-zero epoch")
	}

	// Verify the FencedRepository recorded the effect.
	repo, ok := w.Fenced.(*FakeFencedRepository)
	if !ok {
		// Non-fake implementations: at minimum check a non-zero last epoch via
		// the interface (we only have ApplyFenced, so skip the extra check for
		// non-fake repos).
		return
	}
	if got := repo.LastEpoch("fence-entity-1"); got != epoch {
		t.Fatalf("Fencing: repo.LastEpoch=%d, want %d (the live lease epoch)", got, epoch)
	}
	if effects := repo.Effects(); len(effects) == 0 {
		t.Fatal("Fencing: no effect recorded in FakeFencedRepository")
	}
}

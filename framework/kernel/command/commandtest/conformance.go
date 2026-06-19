// Package commandtest provides shared conformance assertions for
// command.Queue + command.ActiveScanner implementations. Both the in-memory
// InMemQueue and the PostgreSQL command_queue adapter (adapters/postgres)
// enroll against the same suite so behavior stays in lock-step.
//
// ref: runtime/audit/ledger/storetest (kernel-side conformance shape)
// ref: corecells/accesscore/internal/ports/conformance (Factory + Features pattern)
package commandtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/command"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// TxRunner is the minimal transactional runner contract the conformance suite
// depends on. It mirrors kernel/persistence.TxRunner structurally — any
// implementation of that public interface satisfies this one — but is
// declared locally so kernel/command/commandtest does not import
// kernel/persistence (KERNEL-INTERNAL-DAG-01: command package would otherwise
// gain a cross-owner edge to persistence).
//
// The Go idiom is "accept interfaces, return concrete types"; the conformance
// suite is the accept-interface site, and a one-method local definition keeps
// the kernel-internal DAG flat without adding a runtime dependency.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// fifoSeedSpacing is the gap between successive seeded entries in FIFO-ordering
// sub-tests. The exact value is irrelevant — only the strict-monotonic-ascent
// property matters — but extracting it to a const satisfies the
// TEST-TIME-LITERAL-01 archtest and documents the intent at a single location.
const fifoSeedSpacing = 2 * time.Second

// Conformance test format strings and fixture IDs used across multiple
// sub-tests. Extracted as constants to satisfy go:S1192 (duplicate literal
// threshold ≥ 3).
const (
	fmtGetCommand = "GetCommand: %v"
	fmtDequeue    = "Dequeue: %v"
	fmtAck        = "Ack: %v"
	fmtScanActive = "ScanActive: %v"

	fixtureRepIdem1 = "rep-idem-1"
	fixtureAckMM    = "ack-mm"
	fixtureCancelP  = "cancel-p"
	fixtureCancelT  = "cancel-t"
)

// QueueFactory builds a fresh Queue + ActiveScanner pair (typically the same
// concrete type) plus the matching TxRunner. The returned cleanup MUST be
// idempotent and tolerate being called even if no resources were acquired.
//
// The returned clock is the time source the implementation must obey when the
// suite drives state transitions. Implementations that use wall time should
// return time.Now-equivalent here so the conformance "now" stays consistent.
type QueueFactory func(t *testing.T) (
	q command.Queue,
	scanner command.ActiveScanner,
	txRunner TxRunner,
	now func() time.Time,
	cleanup func(),
)

// Features captures behavioral differences between in-memory and durable
// implementations that the conformance suite must accommodate without
// resorting to t.Skip. Features should describe *what* the implementation
// offers, never *whether to skip*.
type Features struct {
	// RequiresAmbientTx indicates writes must run inside a txRunner.RunInTx
	// scope (true for PG; false for mem). When true, the suite wraps each
	// mutating call in RunInTx; when false, it calls the Queue methods directly.
	RequiresAmbientTx bool

	// SupportsLeaseRenewal indicates ExtendLease is honored (true for both
	// today; the flag exists so future read-only mirrors can opt out without
	// silently passing).
	SupportsLeaseRenewal bool
}

// RunQueueConformance drives the shared state-machine assertions against the
// implementation produced by factory. Test names are scoped t.Run sub-tests so
// failures point at a single transition rather than a soup of assertions.
func RunQueueConformance(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()

	t.Run("Enqueue/HappyPath", func(t *testing.T) { runEnqueueHappy(t, factory, features) })
	t.Run("Enqueue/DuplicateID", func(t *testing.T) { runEnqueueDuplicateID(t, factory, features) })
	t.Run("Enqueue/IdempotencyKeyDedup", func(t *testing.T) { runEnqueueIdempotencyKey(t, factory, features) })
	t.Run("Enqueue/MaxPendingPerDeviceCap", func(t *testing.T) { runEnqueueMaxPendingCap(t, factory, features) })
	t.Run("Enqueue/ActiveKeyBlocksAcrossNonTerminal", func(t *testing.T) { runEnqueueActiveKeyBlocks(t, factory, features) })
	t.Run("Enqueue/KeyReleasedOnSucceeded", func(t *testing.T) {
		runEnqueueKeyReleasedAfterAck(t, factory, features, "rel-ok", command.AckSuccess)
	})
	t.Run("Enqueue/KeyReleasedOnFailed", func(t *testing.T) {
		runEnqueueKeyReleasedAfterAck(t, factory, features, "rel-fail", command.AckFailed)
	})
	t.Run("Enqueue/KeyReleasedOnExpired", func(t *testing.T) {
		runEnqueueKeyReleasedAfterAck(t, factory, features, "rel-exp", command.AckTimeout)
	})
	t.Run("Enqueue/KeyReleasedOnRejected", func(t *testing.T) {
		runEnqueueKeyReleasedAfterAck(t, factory, features, "rel-rej", command.AckRejected)
	})
	t.Run("Enqueue/KeyReleasedOnCanceled", func(t *testing.T) { runEnqueueKeyReleasedAfterCancel(t, factory, features) })
	t.Run("Enqueue/KeyReleasedViaDeliveredOnSucceeded", func(t *testing.T) {
		runEnqueueKeyReleasedAfterAckViaDelivered(t, factory, features, "rel-del-ok", command.AckSuccess)
	})
	t.Run("Enqueue/KeyReleasedViaDeliveredOnFailed", func(t *testing.T) {
		runEnqueueKeyReleasedAfterAckViaDelivered(t, factory, features, "rel-del-fail", command.AckFailed)
	})
	t.Run("Enqueue/KeyReleasedViaDeliveredOnExpired", func(t *testing.T) {
		runEnqueueKeyReleasedAfterAckViaDelivered(t, factory, features, "rel-del-exp", command.AckTimeout)
	})
	t.Run("Enqueue/AuthzReject", func(t *testing.T) { runEnqueueAuthzReject(t, factory, features) })
	t.Run("Enqueue/InvalidEntry", func(t *testing.T) { runEnqueueInvalidEntry(t, factory, features) })

	t.Run("Dequeue/FIFO", func(t *testing.T) { runDequeueFIFO(t, factory, features) })
	t.Run("Dequeue/EmptyQueue", func(t *testing.T) { runDequeueEmpty(t, factory, features) })
	t.Run("Dequeue/NGreaterThanAvailable", func(t *testing.T) { runDequeueNTooLarge(t, factory, features) })
	t.Run("Dequeue/AdvancesToSent", func(t *testing.T) { runDequeueAdvancesToSent(t, factory, features) })

	t.Run("Report/SentToDelivered", func(t *testing.T) { runReportSentToDelivered(t, factory, features) })
	t.Run("Report/AlreadyDeliveredIsIdempotent", func(t *testing.T) { runReportIdempotent(t, factory, features) })
	t.Run("Report/NotFound", func(t *testing.T) { runReportNotFound(t, factory, features) })

	t.Run("Ack/SentToSucceeded", func(t *testing.T) { runAckSentToSucceeded(t, factory, features) })
	t.Run("Ack/SentToFailed", func(t *testing.T) { runAckSentToFailed(t, factory, features) })
	t.Run("Ack/SentToExpiredViaTimeout", func(t *testing.T) { runAckTimeout(t, factory, features) })
	t.Run("Ack/SentToCanceledViaRejected", func(t *testing.T) { runAckRejected(t, factory, features) })
	t.Run("Ack/DeliveredToSucceeded", func(t *testing.T) { runAckDeliveredToSucceeded(t, factory, features) })
	t.Run("Ack/DeliveredToFailed", func(t *testing.T) { runAckDeliveredToFailed(t, factory, features) })
	t.Run("Ack/DeliveredToExpired", func(t *testing.T) { runAckDeliveredToExpired(t, factory, features) })
	t.Run("Ack/IdempotentSameTarget", func(t *testing.T) { runAckIdempotentSame(t, factory, features) })
	t.Run("Ack/MismatchTerminalRejected", func(t *testing.T) { runAckMismatchTerminal(t, factory, features) })
	t.Run("Ack/InvalidReason", func(t *testing.T) { runAckInvalidReason(t, factory, features) })
	t.Run("Ack/NotFound", func(t *testing.T) { runAckNotFound(t, factory, features) })

	if features.SupportsLeaseRenewal {
		t.Run("ExtendLease/NotFound", func(t *testing.T) { runExtendLeaseNotFound(t, factory, features) })
		t.Run("ExtendLease/NoLeaseRejected", func(t *testing.T) { runExtendLeaseNoLease(t, factory, features) })
		t.Run("ExtendLease/Renews", func(t *testing.T) { runExtendLeaseRenews(t, factory, features) })
	}

	t.Run("Cancel/PendingToCanceled", func(t *testing.T) { runCancelPending(t, factory, features) })
	t.Run("Cancel/TerminalRejected", func(t *testing.T) { runCancelTerminal(t, factory, features) })
	t.Run("Cancel/NotFound", func(t *testing.T) { runCancelNotFound(t, factory, features) })

	t.Run("ScanActive/FilterByDevice", func(t *testing.T) { runScanActiveByDevice(t, factory, features) })
	t.Run("ScanActive/FilterByStatus", func(t *testing.T) { runScanActiveByStatus(t, factory, features) })
	t.Run("ScanActive/ExcludesTerminal", func(t *testing.T) { runScanActiveExcludesTerminal(t, factory, features) })
	t.Run("ScanActive/SortedByCreatedAt", func(t *testing.T) { runScanActiveSorted(t, factory, features) })

	t.Run("GetCommand/HappyPath", func(t *testing.T) { runGetCommandHappy(t, factory, features) })
	t.Run("GetCommand/NotFound", func(t *testing.T) { runGetCommandNotFound(t, factory, features) })
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// inTx runs fn either inside RunInTx (when ambient-tx is required) or directly.
// Returns the error fn produced; conformance assertions wrap the outcome.
func inTx(t *testing.T, ctx context.Context, txRunner TxRunner, features Features, fn func(ctx context.Context) error) error {
	t.Helper()
	if !features.RequiresAmbientTx {
		return fn(ctx)
	}
	return txRunner.RunInTx(ctx, fn)
}

func seedEntry(t *testing.T, ctx context.Context, q command.Queue, txRunner TxRunner, features Features, e command.Entry) {
	t.Helper()
	if err := inTx(t, ctx, txRunner, features, func(c context.Context) error {
		return q.Enqueue(c, e, command.EnqueueOptions{})
	}); err != nil {
		t.Fatalf("seedEntry: Enqueue %q: %v", e.ID, err)
	}
}

func dequeueOne(t *testing.T, ctx context.Context, q command.Queue, txRunner TxRunner, features Features, deviceID string) command.Entry {
	t.Helper()
	var got []command.Entry
	if err := inTx(t, ctx, txRunner, features, func(c context.Context) error {
		var derr error
		got, derr = q.Dequeue(c, deviceID, 1, command.DefaultLeaseDuration)
		return derr
	}); err != nil {
		t.Fatalf("dequeueOne: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("dequeueOne: expected 1, got %d", len(got))
	}
	return got[0]
}

func makeEntry(id, deviceID string, now time.Time) command.Entry {
	return command.NewEntry(id, deviceID, "test.cmd", []byte(`{"k":"v"}`), command.Timeouts{}, now)
}

func errorCode(err error) errcode.Code {
	var ec *errcode.Error
	if errors.As(err, &ec) {
		return ec.Code
	}
	return ""
}

func requireErrCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %q, got nil", want)
	}
	if got := errorCode(err); got != want {
		t.Fatalf("expected errcode %q, got %q (err=%v)", want, got, err)
	}
}

// ---------------------------------------------------------------------------
// Enqueue cases
// ---------------------------------------------------------------------------

func runEnqueueHappy(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, scanner, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	e := makeEntry("enq-1", "dev-a", now())
	seedEntry(t, ctx, q, tx, features, e)

	got, err := scanner.GetCommand(ctx, "enq-1")
	if err != nil {
		t.Fatalf(fmtGetCommand, err)
	}
	if got.Status != command.StatusPending {
		t.Fatalf("expected Pending after Enqueue, got %s", got.Status)
	}
}

func runEnqueueDuplicateID(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, _, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	e := makeEntry("dup-1", "dev-a", now())
	seedEntry(t, ctx, q, tx, features, e)

	err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Enqueue(c, e, command.EnqueueOptions{})
	})
	requireErrCode(t, err, errcode.ErrConflict)
}

func runEnqueueIdempotencyKey(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, scanner, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	e := makeEntry("idem-1", "dev-a", now())
	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Enqueue(c, e, command.EnqueueOptions{IdempotencyKey: "k-1"})
	}); err != nil {
		t.Fatalf("first Enqueue: %v", err)
	}

	// Second Enqueue with the same key must be a no-op (nil error) even
	// though we hand it a fresh ID.
	e2 := makeEntry("idem-2", "dev-a", now())
	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Enqueue(c, e2, command.EnqueueOptions{IdempotencyKey: "k-1"})
	}); err != nil {
		t.Fatalf("second Enqueue (idempotent): %v", err)
	}

	// Only the first ID should exist; the second ID should not.
	if _, err := scanner.GetCommand(ctx, "idem-2"); err == nil {
		t.Fatal("expected idem-2 to not exist (idempotency key collapsed)")
	}
}

// runEnqueueMaxPendingCap pins the atomic per-device Pending cap
// (EnqueueOptions.MaxPendingPerDevice, F-S-005 #822): the queue admits at most
// `limit` Pending commands per device, the cap is per-device, only Pending counts
// (a drained command frees a slot), and an idempotency-coalesced re-enqueue never
// trips it. The count+insert run at the write boundary under one lock/tx, so the
// cap is a hard invariant — the concurrency proof is the in-mem -race test
// (TestInMemQueue_MaxPendingPerDevice_Concurrent); this case pins the semantics
// for both the in-mem and PG implementations.
func runEnqueueMaxPendingCap(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, scanner, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	const limit = 3
	capped := command.EnqueueOptions{MaxPendingPerDevice: limit}
	enqueue := func(id, deviceID string, opts command.EnqueueOptions) error {
		return inTx(t, ctx, tx, features, func(c context.Context) error {
			return q.Enqueue(c, makeEntry(id, deviceID, now()), opts)
		})
	}

	// Fill dev-a to the cap — all admitted.
	for _, id := range []string{"cap-a-1", "cap-a-2", "cap-a-3"} {
		if err := enqueue(id, "dev-a", capped); err != nil {
			t.Fatalf("Enqueue %s under cap: %v", id, err)
		}
	}
	// One past the cap → rejected with rate-limited at the write boundary (the
	// queue owns the invariant; no service-side pre-check).
	requireErrCode(t, enqueue("cap-a-over", "dev-a", capped), errcode.ErrRateLimited)

	// The cap is per-device: dev-b has its own budget.
	if err := enqueue("cap-b-1", "dev-b", capped); err != nil {
		t.Fatalf("Enqueue dev-b under its own cap: %v", err)
	}

	// An idempotency-coalesced re-enqueue adds no row, so it is a no-op even at the
	// cap — NOT a rate-limit (dedup is decided before the cap).
	seedEntryWithKey(t, ctx, q, tx, features, makeEntry("cap-ak-1", "dev-x", now()), "cap-key")
	for _, id := range []string{"cap-ak-2", "cap-ak-3"} {
		if err := enqueue(id, "dev-x", capped); err != nil {
			t.Fatalf("Enqueue %s under cap: %v", id, err)
		}
	}
	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Enqueue(c, makeEntry("cap-ak-dup", "dev-x", now()),
			command.EnqueueOptions{IdempotencyKey: "cap-key", MaxPendingPerDevice: limit})
	}); err != nil {
		t.Fatalf("idempotency-coalesced re-enqueue at cap must be a no-op, got: %v", err)
	}

	// Draining one Pending → Sent frees a slot (only Pending counts), so dev-a
	// admits one more and ends holding exactly `limit` Pending.
	dequeueOne(t, ctx, q, tx, features, "dev-a")
	if err := enqueue("cap-a-after-drain", "dev-a", capped); err != nil {
		t.Fatalf("Enqueue dev-a after a slot freed: %v", err)
	}
	pendingA, err := scanner.ScanActive(ctx, command.ScanFilter{
		DeviceID: "dev-a", Statuses: []command.Status{command.StatusPending},
	})
	if err != nil {
		t.Fatalf(fmtScanActive, err)
	}
	if len(pendingA) != limit {
		t.Fatalf("dev-a Pending = %d, want exactly %d (hard cap)", len(pendingA), limit)
	}
}

// idemKeySuffix is appended to a sub-test's id base to build its idempotency
// key, keeping key and id distinct without repeating a literal across cases.
const idemKeySuffix = ":idem"

// seedEntryWithKey enqueues e under idempotencyKey, failing the test on error.
func seedEntryWithKey(
	t *testing.T, ctx context.Context,
	q command.Queue, txRunner TxRunner, features Features,
	e command.Entry, key string,
) {
	t.Helper()
	if err := inTx(t, ctx, txRunner, features, func(c context.Context) error {
		return q.Enqueue(c, e, command.EnqueueOptions{IdempotencyKey: key})
	}); err != nil {
		t.Fatalf("seedEntryWithKey: Enqueue %q (key %q): %v", e.ID, key, err)
	}
}

// runEnqueueActiveKeyBlocks asserts the idempotency key is state-aware ACTIVE
// uniqueness: a re-enqueue under the same key is coalesced for as long as the
// holding command stays non-terminal — across Pending, Sent, AND Delivered.
// (A permanent-uniqueness implementation also passes this; the release cases
// below are what distinguish state-aware from permanent.)
func runEnqueueActiveKeyBlocks(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, scanner, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()
	key := "active" + idemKeySuffix

	seedEntryWithKey(t, ctx, q, tx, features, makeEntry("active-1", "dev-a", now()), key)

	// While Pending: same-key re-enqueue collapses.
	seedEntryWithKey(t, ctx, q, tx, features, makeEntry("active-2", "dev-a", now()), key)
	if _, err := scanner.GetCommand(ctx, "active-2"); err == nil {
		t.Fatal("expected active-2 to collapse while holder is Pending")
	}

	// Advance holder Pending→Sent, then same-key re-enqueue still collapses.
	dequeueOne(t, ctx, q, tx, features, "dev-a")
	seedEntryWithKey(t, ctx, q, tx, features, makeEntry("active-3", "dev-a", now()), key)
	if _, err := scanner.GetCommand(ctx, "active-3"); err == nil {
		t.Fatal("expected active-3 to collapse while holder is Sent")
	}

	// Advance holder Sent→Delivered, then same-key re-enqueue still collapses.
	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Report(c, "active-1", now())
	}); err != nil {
		t.Fatalf("Report: %v", err)
	}
	seedEntryWithKey(t, ctx, q, tx, features, makeEntry("active-4", "dev-a", now()), key)
	if _, err := scanner.GetCommand(ctx, "active-4"); err == nil {
		t.Fatal("expected active-4 to collapse while holder is Delivered")
	}
}

// runEnqueueKeyReleasedAfterAck asserts state-aware release: once the holding
// command reaches a TERMINAL status via Ack(reason), the idempotency key is
// freed and a fresh command with the same key enqueues successfully. A
// permanent-uniqueness implementation FAILS this (the second id never exists).
func runEnqueueKeyReleasedAfterAck(t *testing.T, factory QueueFactory, features Features, idBase string, reason command.AckReason) {
	t.Helper()
	q, scanner, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()
	key := idBase + idemKeySuffix

	seedEntryWithKey(t, ctx, q, tx, features, makeEntry(idBase+"-1", "dev-a", now()), key)
	dequeueOne(t, ctx, q, tx, features, "dev-a")
	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Ack(c, idBase+"-1", reason, now())
	}); err != nil {
		t.Fatalf("Ack(%s): %v", reason, err)
	}

	// Key is now released — a fresh command with the same key must enqueue.
	seedEntryWithKey(t, ctx, q, tx, features, makeEntry(idBase+"-2", "dev-a", now()), key)
	if _, err := scanner.GetCommand(ctx, idBase+"-2"); err != nil {
		t.Fatalf("expected %s-2 to exist after key released by terminal %s: %v", idBase, reason, err)
	}
}

// runEnqueueKeyReleasedAfterAckViaDelivered asserts state-aware release on the
// Delivered→terminal path. Unlike runEnqueueKeyReleasedAfterAck (which ACKs from
// Sent), this helper advances the holder to Delivered via Report before ACKing,
// proving the key release fires on the Delivered→terminal arc as well.
func runEnqueueKeyReleasedAfterAckViaDelivered(
	t *testing.T, factory QueueFactory, features Features,
	idBase string, reason command.AckReason,
) {
	t.Helper()
	q, scanner, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()
	key := idBase + idemKeySuffix

	seedEntryWithKey(t, ctx, q, tx, features, makeEntry(idBase+"-1", "dev-a", now()), key)
	dequeueOne(t, ctx, q, tx, features, "dev-a")

	// Advance Sent→Delivered.
	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Report(c, idBase+"-1", now())
	}); err != nil {
		t.Fatalf("Report: %v", err)
	}

	// ACK from Delivered — this must release the idempotency key.
	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Ack(c, idBase+"-1", reason, now())
	}); err != nil {
		t.Fatalf("Ack(%s) from Delivered: %v", reason, err)
	}

	// Key is now released — a fresh command with the same key must enqueue.
	seedEntryWithKey(t, ctx, q, tx, features, makeEntry(idBase+"-2", "dev-a", now()), key)
	if _, err := scanner.GetCommand(ctx, idBase+"-2"); err != nil {
		t.Fatalf("expected %s-2 to exist after key released by Delivered→terminal %s: %v", idBase, reason, err)
	}
}

// runEnqueueKeyReleasedAfterCancel is the Cancel-path analog of
// runEnqueueKeyReleasedAfterAck: a Pending holder Canceled releases its key.
func runEnqueueKeyReleasedAfterCancel(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, scanner, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()
	key := "rel-cancel" + idemKeySuffix

	seedEntryWithKey(t, ctx, q, tx, features, makeEntry("rel-cancel-1", "dev-a", now()), key)
	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Cancel(c, "rel-cancel-1", now())
	}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	seedEntryWithKey(t, ctx, q, tx, features, makeEntry("rel-cancel-2", "dev-a", now()), key)
	if _, err := scanner.GetCommand(ctx, "rel-cancel-2"); err != nil {
		t.Fatalf("expected rel-cancel-2 to exist after key released by Cancel: %v", err)
	}
}

func runEnqueueAuthzReject(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, scanner, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	rejectErr := errors.New("not authorized")
	e := makeEntry("authz-1", "dev-a", now())
	err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Enqueue(c, e, command.EnqueueOptions{
			Authz: func(context.Context) error { return rejectErr },
		})
	})
	if err == nil || !errors.Is(err, rejectErr) {
		t.Fatalf("expected wrapped rejectErr, got %v", err)
	}
	if _, err := scanner.GetCommand(ctx, "authz-1"); err == nil {
		t.Fatal("expected authz-1 to not exist after Authz reject")
	}
}

func runEnqueueInvalidEntry(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, _, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	e := makeEntry("inv-1", "dev-a", now())
	e.CommandType = "" // ValidateNew will reject

	err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Enqueue(c, e, command.EnqueueOptions{})
	})
	requireErrCode(t, err, errcode.ErrValidationFailed)
}

// ---------------------------------------------------------------------------
// Dequeue cases
// ---------------------------------------------------------------------------

func runDequeueFIFO(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, _, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	base := now()
	seedEntry(t, ctx, q, tx, features, makeEntry("fifo-1", "dev-x", base))
	seedEntry(t, ctx, q, tx, features, makeEntry("fifo-2", "dev-x", base.Add(time.Second)))
	seedEntry(t, ctx, q, tx, features, makeEntry("fifo-3", "dev-x", base.Add(fifoSeedSpacing)))

	var got []command.Entry
	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		var derr error
		got, derr = q.Dequeue(c, "dev-x", 2, command.DefaultLeaseDuration)
		return derr
	}); err != nil {
		t.Fatalf(fmtDequeue, err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if got[0].ID != "fifo-1" || got[1].ID != "fifo-2" {
		t.Fatalf("expected FIFO order [fifo-1, fifo-2], got [%s, %s]", got[0].ID, got[1].ID)
	}
}

func runDequeueEmpty(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, _, tx, _, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	var got []command.Entry
	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		var derr error
		got, derr = q.Dequeue(c, "missing-device", 5, command.DefaultLeaseDuration)
		return derr
	}); err != nil {
		t.Fatalf(fmtDequeue, err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 entries from empty queue, got %d", len(got))
	}
}

func runDequeueNTooLarge(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, _, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	seedEntry(t, ctx, q, tx, features, makeEntry("over-1", "dev-y", now()))

	var got []command.Entry
	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		var derr error
		got, derr = q.Dequeue(c, "dev-y", 10, command.DefaultLeaseDuration)
		return derr
	}); err != nil {
		t.Fatalf(fmtDequeue, err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entry (n>available), got %d", len(got))
	}
}

func runDequeueAdvancesToSent(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, scanner, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	seedEntry(t, ctx, q, tx, features, makeEntry("adv-1", "dev-z", now()))
	got := dequeueOne(t, ctx, q, tx, features, "dev-z")
	if got.Status != command.StatusSent {
		t.Fatalf("expected Sent after Dequeue, got %s", got.Status)
	}
	if got.SentAt == nil {
		t.Fatal("expected SentAt to be set after Dequeue")
	}
	if got.Attempt != 1 {
		t.Fatalf("expected Attempt=1 after first Dequeue, got %d", got.Attempt)
	}

	stored, err := scanner.GetCommand(ctx, "adv-1")
	if err != nil {
		t.Fatalf(fmtGetCommand, err)
	}
	if stored.Status != command.StatusSent {
		t.Fatalf("expected persisted Status=Sent, got %s", stored.Status)
	}
}

// ---------------------------------------------------------------------------
// Report cases
// ---------------------------------------------------------------------------

func runReportSentToDelivered(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, scanner, tx, n, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	seedEntry(t, ctx, q, tx, features, makeEntry("rep-1", "dev-a", n()))
	dequeueOne(t, ctx, q, tx, features, "dev-a")

	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Report(c, "rep-1", n())
	}); err != nil {
		t.Fatalf("Report: %v", err)
	}

	got, err := scanner.GetCommand(ctx, "rep-1")
	if err != nil {
		t.Fatalf(fmtGetCommand, err)
	}
	if got.Status != command.StatusDelivered {
		t.Fatalf("expected Delivered, got %s", got.Status)
	}
	if got.DeliveredAt == nil {
		t.Fatal("expected DeliveredAt to be set")
	}
}

func runReportIdempotent(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, scanner, tx, n, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	seedEntry(t, ctx, q, tx, features, makeEntry(fixtureRepIdem1, "dev-a", n()))
	dequeueOne(t, ctx, q, tx, features, "dev-a")

	// First Report — advances Sent→Delivered and sets delivered_at.
	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Report(c, fixtureRepIdem1, n())
	}); err != nil {
		t.Fatalf("Report first call: %v", err)
	}

	first, err := scanner.GetCommand(ctx, fixtureRepIdem1)
	if err != nil {
		t.Fatalf("GetCommand after first Report: %v", err)
	}
	if first.DeliveredAt == nil {
		t.Fatal("expected DeliveredAt set after first Report")
	}
	firstDeliveredAt := *first.DeliveredAt

	// Second Report — must be idempotent: no error and delivered_at unchanged.
	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Report(c, fixtureRepIdem1, n())
	}); err != nil {
		t.Fatalf("Report second call: %v", err)
	}

	second, err := scanner.GetCommand(ctx, fixtureRepIdem1)
	if err != nil {
		t.Fatalf("GetCommand after second Report: %v", err)
	}
	if second.DeliveredAt == nil {
		t.Fatal("expected DeliveredAt still set after second Report")
	}
	if !second.DeliveredAt.Equal(firstDeliveredAt) {
		t.Fatalf("delivered_at must not change on idempotent Report: first=%v second=%v",
			firstDeliveredAt, *second.DeliveredAt)
	}
}

func runReportNotFound(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, _, tx, n, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Report(c, "does-not-exist", n())
	})
	requireErrCode(t, err, errcode.ErrCommandNotFound)
}

// ---------------------------------------------------------------------------
// Ack cases
// ---------------------------------------------------------------------------

func runAckSentToSucceeded(t *testing.T, factory QueueFactory, features Features) {
	runAckTerminal(t, factory, features, "ack-ok", command.AckSuccess, command.StatusSucceeded)
}

func runAckSentToFailed(t *testing.T, factory QueueFactory, features Features) {
	runAckTerminal(t, factory, features, "ack-fail", command.AckFailed, command.StatusFailed)
}

func runAckTimeout(t *testing.T, factory QueueFactory, features Features) {
	runAckTerminal(t, factory, features, "ack-to", command.AckTimeout, command.StatusExpired)
}

func runAckRejected(t *testing.T, factory QueueFactory, features Features) {
	runAckTerminal(t, factory, features, "ack-rj", command.AckRejected, command.StatusCanceled)
}

func runAckDeliveredToSucceeded(t *testing.T, factory QueueFactory, features Features) {
	runAckTerminalViaDelivered(t, factory, features, "ack-del-ok", command.AckSuccess, command.StatusSucceeded)
}

func runAckDeliveredToFailed(t *testing.T, factory QueueFactory, features Features) {
	runAckTerminalViaDelivered(t, factory, features, "ack-del-fail", command.AckFailed, command.StatusFailed)
}

func runAckDeliveredToExpired(t *testing.T, factory QueueFactory, features Features) {
	runAckTerminalViaDelivered(t, factory, features, "ack-del-exp", command.AckTimeout, command.StatusExpired)
}

// runAckTerminalViaDelivered seeds → dequeues → Reports (Sent→Delivered) →
// Acks and verifies the terminal status. This covers the Delivered→terminal
// path that runAckTerminal (Sent→terminal) does not exercise.
func runAckTerminalViaDelivered(
	t *testing.T, factory QueueFactory, features Features,
	id string, reason command.AckReason, want command.Status,
) {
	t.Helper()
	q, scanner, tx, n, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	seedEntry(t, ctx, q, tx, features, makeEntry(id, "dev-a", n()))
	dequeueOne(t, ctx, q, tx, features, "dev-a")

	// Advance to Delivered first.
	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Report(c, id, n())
	}); err != nil {
		t.Fatalf("Report: %v", err)
	}

	// Now Ack from Delivered.
	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Ack(c, id, reason, n())
	}); err != nil {
		t.Fatalf("Ack from Delivered: %v", err)
	}
	got, err := scanner.GetCommand(ctx, id)
	if err != nil {
		t.Fatalf(fmtGetCommand, err)
	}
	if got.Status != want {
		t.Fatalf("expected %s after Delivered→Ack, got %s", want, got.Status)
	}
	if got.CompletedAt == nil {
		t.Fatal("expected CompletedAt to be set on terminal ack from Delivered")
	}
}

func runAckTerminal(t *testing.T, factory QueueFactory, features Features, id string, reason command.AckReason, want command.Status) {
	t.Helper()
	q, scanner, tx, n, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	seedEntry(t, ctx, q, tx, features, makeEntry(id, "dev-a", n()))
	dequeueOne(t, ctx, q, tx, features, "dev-a")

	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Ack(c, id, reason, n())
	}); err != nil {
		t.Fatalf(fmtAck, err)
	}
	got, err := scanner.GetCommand(ctx, id)
	if err != nil {
		t.Fatalf(fmtGetCommand, err)
	}
	if got.Status != want {
		t.Fatalf("expected %s, got %s", want, got.Status)
	}
	if got.CompletedAt == nil {
		t.Fatal("expected CompletedAt to be set on terminal ack")
	}
}

func runAckIdempotentSame(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, _, tx, n, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	seedEntry(t, ctx, q, tx, features, makeEntry("ack-idem", "dev-a", n()))
	dequeueOne(t, ctx, q, tx, features, "dev-a")

	for i := 0; i < 2; i++ {
		if err := inTx(t, ctx, tx, features, func(c context.Context) error {
			return q.Ack(c, "ack-idem", command.AckSuccess, n())
		}); err != nil {
			t.Fatalf("Ack iter=%d: %v", i, err)
		}
	}
}

func runAckMismatchTerminal(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, _, tx, n, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	seedEntry(t, ctx, q, tx, features, makeEntry(fixtureAckMM, "dev-a", n()))
	dequeueOne(t, ctx, q, tx, features, "dev-a")

	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Ack(c, fixtureAckMM, command.AckSuccess, n())
	}); err != nil {
		t.Fatalf("first Ack: %v", err)
	}
	err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Ack(c, fixtureAckMM, command.AckFailed, n())
	})
	requireErrCode(t, err, errcode.ErrValidationFailed)
}

func runAckInvalidReason(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, _, tx, n, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	seedEntry(t, ctx, q, tx, features, makeEntry("ack-inv", "dev-a", n()))
	err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Ack(c, "ack-inv", command.AckReason(0), n())
	})
	requireErrCode(t, err, errcode.ErrValidationFailed)
}

func runAckNotFound(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, _, tx, n, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Ack(c, "missing", command.AckSuccess, n())
	})
	requireErrCode(t, err, errcode.ErrCommandNotFound)
}

// ---------------------------------------------------------------------------
// ExtendLease cases
// ---------------------------------------------------------------------------

func runExtendLeaseNotFound(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, _, tx, n, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.ExtendLease(c, "missing", time.Minute, n())
	})
	requireErrCode(t, err, errcode.ErrCommandNotFound)
}

func runExtendLeaseNoLease(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, _, tx, n, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	// Pending entries have no lease — ExtendLease must reject.
	seedEntry(t, ctx, q, tx, features, makeEntry("ext-pending", "dev-a", n()))
	err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.ExtendLease(c, "ext-pending", time.Minute, n())
	})
	requireErrCode(t, err, errcode.ErrValidationFailed)
}

func runExtendLeaseRenews(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, _, tx, n, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	seedEntry(t, ctx, q, tx, features, makeEntry("ext-ok", "dev-a", n()))
	dequeueOne(t, ctx, q, tx, features, "dev-a")

	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.ExtendLease(c, "ext-ok", time.Hour, n())
	}); err != nil {
		t.Fatalf("ExtendLease: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Cancel cases
// ---------------------------------------------------------------------------

func runCancelPending(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, scanner, tx, n, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	seedEntry(t, ctx, q, tx, features, makeEntry(fixtureCancelP, "dev-a", n()))

	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Cancel(c, fixtureCancelP, n())
	}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	got, err := scanner.GetCommand(ctx, fixtureCancelP)
	if err != nil {
		t.Fatalf(fmtGetCommand, err)
	}
	if got.Status != command.StatusCanceled {
		t.Fatalf("expected Canceled, got %s", got.Status)
	}
}

func runCancelTerminal(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, _, tx, n, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	seedEntry(t, ctx, q, tx, features, makeEntry(fixtureCancelT, "dev-a", n()))
	dequeueOne(t, ctx, q, tx, features, "dev-a")
	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Ack(c, fixtureCancelT, command.AckSuccess, n())
	}); err != nil {
		t.Fatalf(fmtAck, err)
	}

	err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Cancel(c, fixtureCancelT, n())
	})
	requireErrCode(t, err, errcode.ErrValidationFailed)
}

func runCancelNotFound(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, _, tx, n, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()
	err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Cancel(c, "cancel-nonexistent", n())
	})
	requireErrCode(t, err, errcode.ErrCommandNotFound)
}

// ---------------------------------------------------------------------------
// ScanActive cases
// ---------------------------------------------------------------------------

func runScanActiveByDevice(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, scanner, tx, n, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	base := n()
	seedEntry(t, ctx, q, tx, features, makeEntry("sa-1", "dev-a", base))
	seedEntry(t, ctx, q, tx, features, makeEntry("sa-2", "dev-b", base.Add(time.Second)))

	got, err := scanner.ScanActive(ctx, command.ScanFilter{DeviceID: "dev-a"})
	if err != nil {
		t.Fatalf(fmtScanActive, err)
	}
	if len(got) != 1 || got[0].ID != "sa-1" {
		t.Fatalf("expected only sa-1, got %#v", got)
	}
}

func runScanActiveByStatus(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, scanner, tx, n, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	base := n()
	seedEntry(t, ctx, q, tx, features, makeEntry("st-pend", "dev-a", base))
	seedEntry(t, ctx, q, tx, features, makeEntry("st-sent", "dev-a", base.Add(time.Second)))
	dequeueOne(t, ctx, q, tx, features, "dev-a") // advances st-pend to Sent

	got, err := scanner.ScanActive(ctx, command.ScanFilter{Statuses: []command.Status{command.StatusPending}})
	if err != nil {
		t.Fatalf(fmtScanActive, err)
	}
	// FIFO dequeue advances st-pend (oldest) to Sent; st-sent remains Pending.
	// Filtering by StatusPending must return only the still-Pending entry st-sent.
	if len(got) != 1 || got[0].ID != "st-sent" {
		t.Fatalf("expected only st-sent (still Pending after FIFO dequeue of st-pend), got %#v", got)
	}
}

func runScanActiveExcludesTerminal(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, scanner, tx, n, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	base := n()
	seedEntry(t, ctx, q, tx, features, makeEntry("term-1", "dev-a", base))
	dequeueOne(t, ctx, q, tx, features, "dev-a")
	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return q.Ack(c, "term-1", command.AckSuccess, n())
	}); err != nil {
		t.Fatalf(fmtAck, err)
	}

	got, err := scanner.ScanActive(ctx, command.ScanFilter{})
	if err != nil {
		t.Fatalf(fmtScanActive, err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no active entries (only one terminal), got %d", len(got))
	}
}

func runScanActiveSorted(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, scanner, tx, n, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	base := n()
	seedEntry(t, ctx, q, tx, features, makeEntry("ord-3", "dev-a", base.Add(fifoSeedSpacing)))
	seedEntry(t, ctx, q, tx, features, makeEntry("ord-1", "dev-a", base))
	seedEntry(t, ctx, q, tx, features, makeEntry("ord-2", "dev-a", base.Add(time.Second)))

	got, err := scanner.ScanActive(ctx, command.ScanFilter{})
	if err != nil {
		t.Fatalf(fmtScanActive, err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}
	if got[0].ID != "ord-1" || got[1].ID != "ord-2" || got[2].ID != "ord-3" {
		t.Fatalf("expected sorted [ord-1, ord-2, ord-3], got [%s, %s, %s]", got[0].ID, got[1].ID, got[2].ID)
	}
}

// ---------------------------------------------------------------------------
// GetCommand cases
// ---------------------------------------------------------------------------

func runGetCommandHappy(t *testing.T, factory QueueFactory, features Features) {
	t.Helper()
	q, scanner, tx, n, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	seedEntry(t, ctx, q, tx, features, makeEntry("get-1", "dev-a", n()))
	got, err := scanner.GetCommand(ctx, "get-1")
	if err != nil {
		t.Fatalf(fmtGetCommand, err)
	}
	if got.ID != "get-1" {
		t.Fatalf("expected ID get-1, got %s", got.ID)
	}
}

func runGetCommandNotFound(t *testing.T, factory QueueFactory, _ Features) {
	t.Helper()
	_, scanner, _, _, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	_, err := scanner.GetCommand(ctx, "nope")
	requireErrCode(t, err, errcode.ErrCommandNotFound)
}

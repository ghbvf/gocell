package reconcile

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// FencedRepository is the consumer-implemented, epoch-aware persistence seam that
// closes the cross-replica correctness gap leader election alone cannot (see
// LeaderElector — "leader election is NOT fencing"). A consumer that performs
// device writes / command emission from inside Reconcile wires its repository as
// a FencedRepository on the Loop; the Loop then injects a per-Reconcile
// epoch-bound FencedWriter into ctx, and that writer is the reconciler's ONLY
// write surface.
//
// ApplyFenced MUST implement a monotonic-epoch compare-and-swap: the resource row
// records the highest epoch it has ever seen, and the write is applied only when
// epoch >= row.last_epoch (and last_epoch advanced to epoch). It returns
// accepted=false (NOT an error) when epoch is stale (< highest-seen) so a zombie
// leader's late write is rejected structurally rather than corrupting state. This
// is Kleppmann (DDIA §8.4) monotonic fencing, NOT the kernel/outbox UUID
// identity-fencing (which only rejects "not equal to current"): a device write
// may land after several L1→L2→L3 handoffs and only "older than highest-seen"
// can reject an out-of-order late write.
//
// mutation is `any` deliberately. The reconciler reaches FencedWriter through a
// non-generic context value (Reconciler.Reconcile is frozen single-method and
// takes no extra param), so a generic FencedRepository[M] would have to be boxed
// to `any` at the ctx seam anyway — the type parameter buys nothing while making
// the Loop (which mints the writer without knowing M) impossible to keep
// monomorphic. The consumer's ApplyFenced does the single type-assert at its own
// write boundary. The fencing guarantee depends on the epoch being unforgeable
// and the CAS being applied — not on the payload being statically typed.
type FencedRepository interface {
	ApplyFenced(ctx context.Context, entityID string, epoch uint64, mutation any) (accepted bool, err error)
}

// FencedWriter is the reconciler's sole write surface for one Reconcile call. It
// is a STRUCT with unexported fields and an unexported constructor
// (newFencedWriter), so a consumer in another package can RECEIVE one (from
// FencedWriterFrom) but can never COMPOSE one with an attacker-chosen epoch —
// reconcile.FencedWriter{epoch: 999} is a compile error outside this package.
// The epoch is therefore always whatever the Loop pulled from the live
// LeaseToken; forging a higher/lower epoch is unrepresentable in Go's type
// system. This is the upstream-Hard half of RECONCILE-FENCED-WRITE-FUNNEL-01
// (sole write surface + sealed construction).
type FencedWriter struct {
	repo  FencedRepository
	epoch uint64
}

// newFencedWriter is the SEALED constructor (unexported). The Loop is the only
// caller, and it passes the epoch from the live lease — never a consumer-chosen
// value. RECONCILE-FENCED-WRITE-FUNNEL-01 locks this callsite to loop.go and
// reflect-locks the unexported field set so the seal cannot drift (e.g. epoch
// becoming an exported field).
func newFencedWriter(repo FencedRepository, epoch uint64) FencedWriter {
	return FencedWriter{repo: repo, epoch: epoch}
}

// Epoch returns the bound fencing token (read-only, for logging/metrics). There
// is no setter — the field is unexported and there is no With/SetEpoch method.
func (w FencedWriter) Epoch() uint64 { return w.epoch }

// Write applies mutation under the bound epoch's CAS via the consumer's
// FencedRepository. A stale-epoch rejection surfaces as ErrFencedWriteStale (the
// reconcile lost a fencing race; this is NOT a transient retry — a fresh lease /
// trigger re-observes the entity). A zero-valued (unbound) writer being written
// through is a programmer error (the writer was not minted by the Loop) and
// surfaces as ErrFencedWriterUnbound.
func (w FencedWriter) Write(ctx context.Context, entityID string, mutation any) error {
	if w.repo == nil {
		return ErrFencedWriterUnbound
	}
	accepted, err := w.repo.ApplyFenced(ctx, entityID, w.epoch, mutation)
	if err != nil {
		return fmt.Errorf("reconcile: fenced write (entity=%q epoch=%d): %w", entityID, w.epoch, err)
	}
	if !accepted {
		return ErrFencedWriteStale
	}
	return nil
}

// ErrFencedWriteStale is the sentinel Write returns (errors.Is-matchable) when
// the consumer's ApplyFenced rejected the write as stale-epoch.
var ErrFencedWriteStale = errcode.New(errcode.KindConflict, errcode.ErrFencedWriteStale,
	"reconcile: fenced write rejected (stale epoch)")

// ErrFencedWriterUnbound is the sentinel Write returns when invoked on a
// zero-valued FencedWriter (no bound repository — programmer error).
var ErrFencedWriterUnbound = errcode.New(errcode.KindInternal, errcode.ErrFencedWriterUnbound,
	"reconcile: FencedWriter has no bound repository (not minted by the Loop)")

// fencedWriterCtxKey is an unexported, zero-field ctx key — no package outside
// kernel/reconcile can name it, so no one can inject or overwrite the writer in
// the ctx. (Putting a handle in ctx is generally an anti-pattern, but it is
// forced here: Reconciler.Reconcile is frozen single-method and cannot take an
// extra param, and the writer is genuinely reconcile-scoped — the same shape as
// persistence's tx-in-ctx.)
type fencedWriterCtxKey struct{}

// withFencedWriter seeds the per-Reconcile epoch-bound writer. Unexported: only
// the Loop calls it (locked by RECONCILE-FENCED-WRITE-FUNNEL-01).
func withFencedWriter(ctx context.Context, w FencedWriter) context.Context {
	return context.WithValue(ctx, fencedWriterCtxKey{}, w)
}

// FencedWriterFrom returns the epoch-bound writer the Loop injected for this
// Reconcile call. ok is false when the Loop runs without a FencedRepository
// (single-process / no-fencing mode, or pre-2027 consumers): a reconciler that
// needs fenced writes MUST treat !ok as "no lease-scoped write surface" and skip
// the side effect (or be wired only under a FencedRepository + LeaderElector).
func FencedWriterFrom(ctx context.Context) (FencedWriter, bool) {
	w, ok := ctx.Value(fencedWriterCtxKey{}).(FencedWriter)
	return w, ok
}

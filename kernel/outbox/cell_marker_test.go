package outbox_test

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
)

type fakePublisher struct{ publishes int }

func (f *fakePublisher) Publish(_ context.Context, _ string, _ []byte) error {
	f.publishes++
	return nil
}

func (f *fakePublisher) Close(_ context.Context) error { return nil }

type fakeCellWriter struct{ writes int }

func (f *fakeCellWriter) Write(_ context.Context, _ outbox.Entry) error {
	f.writes++
	return nil
}

// TestCellPublisher_WrapNilReturnsNil — see kernel/persistence
// TestCellTxManager_WrapNilReturnsNil for rationale.
func TestCellPublisher_WrapNilReturnsNil(t *testing.T) {
	t.Parallel()
	if outbox.WrapPublisherForCell(nil) != nil {
		t.Fatal("WrapPublisherForCell(nil) must return nil interface")
	}
}

func TestCellWriter_WrapNilReturnsNil(t *testing.T) {
	t.Parallel()
	if outbox.WrapWriterForCell(nil) != nil {
		t.Fatal("WrapWriterForCell(nil) must return nil interface")
	}
}

// TestCellPublisher_WrapTypedNilReturnsNil — see kernel/persistence
// TestCellTxManager_WrapTypedNilReturnsNil for the typed-nil interface
// pitfall rationale. Publisher path mirrors TxRunner path because composition
// roots commonly write `var p *amqpPublisher; outbox.WrapPublisherForCell(p)`
// and the bare nil check would silently return a non-nil sealed wrapper.
func TestCellPublisher_WrapTypedNilReturnsNil(t *testing.T) {
	t.Parallel()
	var p *fakePublisher
	var pub outbox.Publisher = p
	if outbox.WrapPublisherForCell(pub) != nil {
		t.Fatal("WrapPublisherForCell(typed-nil) must return nil interface, not a sealed wrapper hiding nil pointer")
	}
}

func TestCellWriter_WrapTypedNilReturnsNil(t *testing.T) {
	t.Parallel()
	var w *fakeCellWriter
	var writer outbox.Writer = w
	if outbox.WrapWriterForCell(writer) != nil {
		t.Fatal("WrapWriterForCell(typed-nil) must return nil interface, not a sealed wrapper hiding nil pointer")
	}
}

// TestCellPublisher_WrapDelegates pins the transparent-proxy invariant
// for the publisher path (DirectEmitter consumes raw outbox.Publisher).
func TestCellPublisher_WrapDelegates(t *testing.T) {
	t.Parallel()
	fp := &fakePublisher{}
	wrapped := outbox.WrapPublisherForCell(fp)
	if wrapped == nil {
		t.Fatal("WrapPublisherForCell(non-nil) must not return nil")
	}
	if err := wrapped.Publish(context.Background(), "topic", []byte("payload")); err != nil {
		t.Fatalf("Publish err: %v", err)
	}
	if fp.publishes != 1 {
		t.Fatalf("delegate failed: publishes=%d", fp.publishes)
	}
}

func TestCellWriter_WrapDelegates(t *testing.T) {
	t.Parallel()
	fw := &fakeCellWriter{}
	wrapped := outbox.WrapWriterForCell(fw)
	if wrapped == nil {
		t.Fatal("WrapWriterForCell(non-nil) must not return nil")
	}
	if err := wrapped.Write(context.Background(), outbox.Entry{}); err != nil {
		t.Fatalf("Write err: %v", err)
	}
	if fw.writes != 1 {
		t.Fatalf("delegate failed: writes=%d", fw.writes)
	}
}

func TestCellPublisher_SatisfiesPublisher(t *testing.T) {
	t.Parallel()
	var _ outbox.Publisher = outbox.WrapPublisherForCell(&fakePublisher{})
}

func TestCellWriter_SatisfiesWriter(t *testing.T) {
	t.Parallel()
	var _ outbox.Writer = outbox.WrapWriterForCell(&fakeCellWriter{})
}

// outbox.NoopWriter and outbox.DiscardPublisher both implement Nooper
// (kernel/outbox/outbox.go::NoopWriter.Noop() / DiscardPublisher.Noop()).
// The wrapper's Noop() pass-through preserves that signal so
// cell.CheckNotNoop / mode_resolver.isNooperDep / emitter.ReportDurable
// all see the inner Nooper status.

// TestWrapPublisherForCell_PreservesNooperPassThrough is the end-to-end
// regression for the publisher Nooper pass-through: removing
// internalCellPublisher.Noop() makes this test fail (type assertion
// returns ok=false), before reaching cell-level integration.
func TestWrapPublisherForCell_PreservesNooperPassThrough(t *testing.T) {
	t.Parallel()
	wrapped := outbox.WrapPublisherForCell(&outbox.DiscardPublisher{})
	type nooper interface{ Noop() bool }
	n, ok := wrapped.(nooper)
	if !ok {
		t.Fatal("CellPublisher wrap must expose inner Nooper interface")
	}
	if !n.Noop() {
		t.Fatal("wrapped DiscardPublisher.Noop() must return true (passthrough)")
	}
}

// TestWrapWriterForCell_PreservesNooperPassThrough mirrors the publisher
// case for outbox.NoopWriter — durable mode rejects NoopWriter via
// CheckNotNoop / isNooperDep, both of which depend on this pass-through.
func TestWrapWriterForCell_PreservesNooperPassThrough(t *testing.T) {
	t.Parallel()
	wrapped := outbox.WrapWriterForCell(outbox.NoopWriter{})
	type nooper interface{ Noop() bool }
	n, ok := wrapped.(nooper)
	if !ok {
		t.Fatal("CellWriter wrap must expose inner Nooper interface")
	}
	if !n.Noop() {
		t.Fatal("wrapped NoopWriter.Noop() must return true (passthrough)")
	}
}

// TestWrapPublisherForCell_NonNooperReturnsFalse confirms the default:
// when the inner Publisher does not implement Nooper, the wrapper's Noop()
// returns false (durable mode accepts the publisher as a real impl).
func TestWrapPublisherForCell_NonNooperReturnsFalse(t *testing.T) {
	t.Parallel()
	wrapped := outbox.WrapPublisherForCell(&fakePublisher{})
	type nooper interface{ Noop() bool }
	n, ok := wrapped.(nooper)
	if !ok {
		t.Fatal("CellPublisher always implements Noop() by structure")
	}
	if n.Noop() {
		t.Fatal("non-Nooper inner Publisher must produce Noop()==false")
	}
}

// TestWrapWriterForCell_NonNooperReturnsFalse mirrors the publisher case
// for the writer side.
func TestWrapWriterForCell_NonNooperReturnsFalse(t *testing.T) {
	t.Parallel()
	wrapped := outbox.WrapWriterForCell(&fakeCellWriter{})
	type nooper interface{ Noop() bool }
	n, ok := wrapped.(nooper)
	if !ok {
		t.Fatal("CellWriter always implements Noop() by structure")
	}
	if n.Noop() {
		t.Fatal("non-Nooper inner Writer must produce Noop()==false")
	}
}

// --- CellEmitter sealed marker (PR-A23 / #618 A.1) ---

// fakeEmitter is a minimal outbox.Emitter for wrap tests.
type fakeEmitter struct{ emits int }

func (f *fakeEmitter) Emit(_ context.Context, _ outbox.Entry) error {
	f.emits++
	return nil
}

// nooperEmitter implements both Emitter and Nooper (Noop()==true). No
// production Emitter implements Nooper today, but the sealed wrapper must
// forward the signal uniformly with the other internalCell* markers
// (SEALED-MARKER-NOOP-TRANSPARENCY-01) so a future Nooper-capable Emitter is
// not silently hidden.
type nooperEmitter struct{}

func (nooperEmitter) Emit(_ context.Context, _ outbox.Entry) error { return nil }
func (nooperEmitter) Noop() bool                                   { return true }

func TestCellEmitter_WrapNilReturnsNil(t *testing.T) {
	t.Parallel()
	if outbox.WrapEmitterForCell(nil) != nil {
		t.Fatal("WrapEmitterForCell(nil) must return nil interface")
	}
}

// TestCellEmitter_WrapTypedNilReturnsNil — see kernel/persistence
// TestCellTxManager_WrapTypedNilReturnsNil for the typed-nil interface
// pitfall rationale.
func TestCellEmitter_WrapTypedNilReturnsNil(t *testing.T) {
	t.Parallel()
	var e *fakeEmitter
	var em outbox.Emitter = e
	if outbox.WrapEmitterForCell(em) != nil {
		t.Fatal("WrapEmitterForCell(typed-nil) must return nil interface, not a sealed wrapper hiding nil pointer")
	}
}

// TestCellEmitter_WrapDelegatesEmit pins the transparent-proxy invariant for
// the emitter path: cells store CellEmitter but services still call Emit.
func TestCellEmitter_WrapDelegatesEmit(t *testing.T) {
	t.Parallel()
	fe := &fakeEmitter{}
	wrapped := outbox.WrapEmitterForCell(fe)
	if wrapped == nil {
		t.Fatal("WrapEmitterForCell(non-nil) must not return nil")
	}
	if err := wrapped.Emit(context.Background(), outbox.Entry{}); err != nil {
		t.Fatalf("Emit err: %v", err)
	}
	if fe.emits != 1 {
		t.Fatalf("delegate failed: emits=%d", fe.emits)
	}
}

func TestCellEmitter_SatisfiesEmitter(t *testing.T) {
	t.Parallel()
	// WrapEmitterForCell's return type is outbox.CellEmitter, so the full sealed
	// contract (Emitter + DurabilityReporter + healthz.ProbeSet + sealed marker)
	// is already compile-enforced at its `return internalCellEmitter{...}` site —
	// dropping an embedded interface or a forwarding method breaks the build there.
	// This assertion pins the widening to the base Emitter (mirrors the
	// Publisher/Writer SatisfiesX tests).
	var _ outbox.Emitter = outbox.WrapEmitterForCell(&fakeEmitter{})
}

// TestWrapEmitterForCell_PreservesNooperPassThrough mirrors the publisher /
// writer Nooper regression for the emitter leg.
func TestWrapEmitterForCell_PreservesNooperPassThrough(t *testing.T) {
	t.Parallel()
	wrapped := outbox.WrapEmitterForCell(nooperEmitter{})
	type nooper interface{ Noop() bool }
	n, ok := wrapped.(nooper)
	if !ok {
		t.Fatal("CellEmitter wrap must expose inner Nooper interface")
	}
	if !n.Noop() {
		t.Fatal("wrapped nooperEmitter.Noop() must return true (passthrough)")
	}
}

func TestWrapEmitterForCell_NonNooperReturnsFalse(t *testing.T) {
	t.Parallel()
	wrapped := outbox.WrapEmitterForCell(&fakeEmitter{})
	type nooper interface{ Noop() bool }
	n, ok := wrapped.(nooper)
	if !ok {
		t.Fatal("CellEmitter always implements Noop() by structure")
	}
	if n.Noop() {
		t.Fatal("non-Nooper inner Emitter must produce Noop()==false")
	}
}

// TestWrapEmitterForCell_PreservesDurablePassThrough is the durability
// regression guard: embedding the Emitter interface in internalCellEmitter
// drops the inner *WriterEmitter / *DirectEmitter Durable() method, so the
// wrapper MUST re-expose DurabilityReporter. Without the forwarding,
// ResolveCellEmitter's durable-mode check and L2 atomicity decisions would
// silently see a non-durable emitter. The CellEmitter interface embeds
// DurabilityReporter, so a missing Durable() is a compile error (Hard).
func TestWrapEmitterForCell_PreservesDurablePassThrough(t *testing.T) {
	t.Parallel()
	durableInner, err := outbox.NewWriterEmitter(fakeWriter{})
	if err != nil {
		t.Fatalf("NewWriterEmitter: %v", err)
	}
	if !outbox.WrapEmitterForCell(durableInner).Durable() {
		t.Fatal("CellEmitter wrapping a durable WriterEmitter must report Durable()==true")
	}
	if outbox.WrapEmitterForCell(outbox.NewNoopEmitter()).Durable() {
		t.Fatal("CellEmitter wrapping a NoopWriter emitter must report Durable()==false")
	}
}

// TestWrapEmitterForCell_PreservesProbesPassThrough is the health-probe
// regression guard: the sealed wrapper must re-expose healthz.ProbeSet so
// cell.RegisterEmitterHealthProbes still registers the DirectEmitter fail-open
// probe through the wrap. The CellEmitter interface embeds healthz.ProbeSet,
// so a missing Probes() is a compile error (Hard).
func TestWrapEmitterForCell_PreservesProbesPassThrough(t *testing.T) {
	t.Parallel()
	de, err := outbox.NewDirectEmitter(&fakePublisher{}, outbox.DirectPublishFailClosed,
		metrics.NopProvider{}, clock.Real(), "testcell")
	if err != nil {
		t.Fatalf("NewDirectEmitter: %v", err)
	}
	if got := len(outbox.WrapEmitterForCell(de).Probes()); got != 1 {
		t.Fatalf("CellEmitter wrapping a DirectEmitter must forward its 1 probe, got %d", got)
	}
	if got := len(outbox.WrapEmitterForCell(outbox.NewNoopEmitter()).Probes()); got != 0 {
		t.Fatalf("CellEmitter wrapping a WriterEmitter (no probes) must forward zero probes, got %d", got)
	}
}

// TestNewDirectCellEmitter covers the L4 direct-publish funnel: it composes
// NewDirectEmitter + WrapEmitterForCell into a sealed, non-durable CellEmitter
// that still forwards the DirectEmitter fail-open probe — and never reaches the
// ResolveCellEmitter L2 atomicity Warn path.
func TestNewDirectCellEmitter(t *testing.T) {
	t.Parallel()

	em, err := outbox.NewDirectCellEmitter(&fakePublisher{}, outbox.DirectPublishFailOpen,
		metrics.NopProvider{}, clock.Real(), "devicecell")
	if err != nil {
		t.Fatalf("NewDirectCellEmitter: %v", err)
	}
	if em == nil {
		t.Fatal("NewDirectCellEmitter must return a non-nil CellEmitter")
	}
	if em.Durable() {
		t.Fatal("NewDirectCellEmitter (DirectEmitter-backed) must report Durable()==false")
	}
	if got := len(em.Probes()); got != 1 {
		t.Fatalf("NewDirectCellEmitter must forward the DirectEmitter fail-open probe, got %d probes", got)
	}

	// Error path: NewDirectEmitter rejects empty cellID — the funnel propagates
	// the error and returns a nil CellEmitter (no partial sealed value).
	emErr, err := outbox.NewDirectCellEmitter(&fakePublisher{}, outbox.DirectPublishFailOpen,
		metrics.NopProvider{}, clock.Real(), "")
	if err == nil {
		t.Fatal("NewDirectCellEmitter with empty cellID must return an error")
	}
	if emErr != nil {
		t.Fatal("NewDirectCellEmitter must return a nil CellEmitter on error")
	}
}

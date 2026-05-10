package outbox_test

import (
	"context"
	"testing"

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

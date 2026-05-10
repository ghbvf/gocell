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

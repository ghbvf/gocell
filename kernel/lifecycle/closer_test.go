package lifecycle_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/ghbvf/gocell/kernel/lifecycle"
)

// TestContextCloser_InterfaceShape verifies the ContextCloser interface has
// exactly Close(ctx context.Context) error and no other method.
func TestContextCloser_InterfaceShape(t *testing.T) {
	t.Parallel()
	// A plain function that satisfies ContextCloser via an anonymous struct.
	var closed bool
	impl := &mockContextCloser{fn: func(_ context.Context) error {
		closed = true
		return nil
	}}

	var cc lifecycle.ContextCloser = impl
	if err := cc.Close(context.Background()); err != nil {
		t.Fatalf("Close returned unexpected error: %v", err)
	}
	if !closed {
		t.Fatal("Close was not called")
	}
}

// TestIgnoreCtx_AdaptsIoCloser verifies that IgnoreCtx wraps an io.Closer
// and that calling Close ignores the context but invokes the underlying Close.
func TestIgnoreCtx_AdaptsIoCloser(t *testing.T) {
	t.Parallel()
	var closeCalled bool
	plain := &mockIoCloser{fn: func() error {
		closeCalled = true
		return nil
	}}

	cc := lifecycle.IgnoreCtx(plain)
	if cc == nil {
		t.Fatal("IgnoreCtx returned nil for a non-nil io.Closer")
	}

	// cc is already lifecycle.ContextCloser by virtue of IgnoreCtx's return type.

	// Call with a cancelled context — the underlying close MUST still run.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cc.Close(cancelledCtx); err != nil {
		t.Fatalf("unexpected error from IgnoreCtx close: %v", err)
	}
	if !closeCalled {
		t.Fatal("underlying io.Closer.Close was not invoked")
	}
}

// TestIgnoreCtx_NilReceiverSafe verifies that IgnoreCtx(nil) returns nil
// rather than panicking.
func TestIgnoreCtx_NilReceiverSafe(t *testing.T) {
	t.Parallel()
	cc := lifecycle.IgnoreCtx(nil)
	if cc != nil {
		t.Fatalf("IgnoreCtx(nil) expected nil, got %v", cc)
	}
}

// TestContextCloser_Chain verifies that wrapping an IgnoreCtx-adapted closer
// inside another ContextCloser-aware middleware correctly propagates calls.
func TestContextCloser_Chain(t *testing.T) {
	t.Parallel()
	var order []string

	inner := lifecycle.IgnoreCtx(&mockIoCloser{fn: func() error {
		order = append(order, "inner")
		return nil
	}})

	outer := &loggingContextCloser{
		inner: inner,
		onClose: func() {
			order = append(order, "outer")
		},
	}

	var cc lifecycle.ContextCloser = outer
	if err := cc.Close(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 2 || order[0] != "outer" || order[1] != "inner" {
		t.Fatalf("unexpected call order: %v", order)
	}
}

// TestIgnoreCtx_PropagatesError verifies that errors from the underlying
// io.Closer are surfaced by the ContextCloser wrapper.
func TestIgnoreCtx_PropagatesError(t *testing.T) {
	t.Parallel()
	want := errors.New("close failed")
	plain := &mockIoCloser{fn: func() error { return want }}

	cc := lifecycle.IgnoreCtx(plain)
	if err := cc.Close(context.Background()); !errors.Is(err, want) {
		t.Fatalf("expected %v, got %v", want, err)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

type mockContextCloser struct {
	fn func(context.Context) error
}

func (m *mockContextCloser) Close(ctx context.Context) error { return m.fn(ctx) }

type mockIoCloser struct {
	fn func() error
}

func (m *mockIoCloser) Close() error { return m.fn() }

// Ensure mockIoCloser satisfies io.Closer.
var _ io.Closer = (*mockIoCloser)(nil)

// loggingContextCloser calls onClose then delegates to inner.
type loggingContextCloser struct {
	inner   lifecycle.ContextCloser
	onClose func()
}

func (l *loggingContextCloser) Close(ctx context.Context) error {
	l.onClose()
	return l.inner.Close(ctx)
}

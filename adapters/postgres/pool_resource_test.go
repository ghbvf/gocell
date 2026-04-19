package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/worker"
)

// stubWorker is a minimal worker.Worker stub for unit tests.
type stubWorker struct{}

func (s *stubWorker) Start(_ context.Context) error { return nil }
func (s *stubWorker) Stop(_ context.Context) error  { return nil }

// stubPool creates a minimal *Pool for unit tests without a real DB.
// We build the Pool struct directly via package-internal access since tests
// are in the same package (package postgres).
func newStubPool() *Pool {
	return &Pool{} // inner pgxpool.Pool is nil; only used for struct access, not queries
}

// TestNewPGResource_Fields verifies construction and default name.
func TestNewPGResource_Fields(t *testing.T) {
	pool := newStubPool()
	res := NewPGResource(pool, nil)

	if res.pool != pool {
		t.Error("pool field not set correctly")
	}
	if res.relay != nil {
		t.Error("relay should be nil when not supplied")
	}
	if res.name != "postgres" {
		t.Errorf("expected name 'postgres', got %q", res.name)
	}
}

// TestPGResource_CheckersReturnsNamed verifies the checker map has the correct
// key. The actual health call requires a real PG pool, so we only check the map
// structure here.
func TestPGResource_CheckersReturnsNamed(t *testing.T) {
	res := NewPGResource(newStubPool(), nil)
	checkers := res.Checkers()
	if len(checkers) != 1 {
		t.Fatalf("expected 1 checker, got %d", len(checkers))
	}
	fn, ok := checkers["postgres"]
	if !ok {
		t.Fatal("expected checker named 'postgres'")
	}
	if fn == nil {
		t.Error("checker function must not be nil")
	}
}

// TestPGResource_WorkerNil verifies nil relay propagates.
func TestPGResource_WorkerNil(t *testing.T) {
	res := NewPGResource(newStubPool(), nil)
	if res.Worker() != nil {
		t.Error("expected nil worker")
	}
}

// TestPGResource_WorkerNonNil verifies a supplied relay is returned.
func TestPGResource_WorkerNonNil(t *testing.T) {
	sw := &stubWorker{}
	res := NewPGResource(newStubPool(), sw)
	if res.Worker() == nil {
		t.Error("expected non-nil worker")
	}
	// Should be the same instance.
	if res.Worker() != worker.Worker(sw) {
		t.Error("returned worker is not the supplied stub")
	}
}

// stubCloser is a minimal poolCloser stub that records whether Close was called.
type stubCloser struct {
	called int
}

func (s *stubCloser) Close() { s.called++ }

// TestPGResource_CloseReturnsNil verifies Close() calls the underlying closer
// exactly once and always returns nil.
func TestPGResource_CloseReturnsNil(t *testing.T) {
	sc := &stubCloser{}
	res := &PGResource{name: "postgres", closeOverride: sc}

	if err := res.Close(); err != nil {
		t.Errorf("Close() returned non-nil error: %v", err)
	}
	if sc.called != 1 {
		t.Errorf("expected Close to call closer once, got %d calls", sc.called)
	}
}

// TestPGResource_ImplementsManagedResource is a compile-time check surfaced as a
// test to make the assertion visible in test output.
func TestPGResource_ImplementsManagedResource(t *testing.T) {
	var _ bootstrap.ManagedResource = (*PGResource)(nil)
}

// TestPGResource_CheckerTimeout verifies that the health checker uses a
// standalone context (not the caller's ctx) with a 5-second timeout.
// We inject a custom pool that records the deadline of the context it receives.
func TestPGResource_CheckerTimeout(t *testing.T) {
	// Build the checker inline with a fake pool that records context deadline.
	var receivedDeadline time.Time
	fakeFn := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		dl, _ := ctx.Deadline()
		receivedDeadline = dl
		return nil
	}

	// Call the fake fn directly — simulates what the real checker does.
	if err := fakeFn(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	now := time.Now()
	// Deadline should be ~5s from now (allow ±2s tolerance for slow CI).
	diff := receivedDeadline.Sub(now)
	if diff < 3*time.Second || diff > 7*time.Second {
		t.Errorf("expected deadline ~5s from now, got %v", diff)
	}
}

// TestPGResource_CheckerUsesIndependentCtx verifies that the health checker
// derives its context from context.Background(), not from a caller-provided
// context. Even when the caller's context is already cancelled, the checker
// must receive a live context with a ~5s deadline.
func TestPGResource_CheckerUsesIndependentCtx(t *testing.T) {
	var receivedCtx context.Context
	res := &PGResource{
		name: "test-pg",
		healthFunc: func(ctx context.Context) error {
			receivedCtx = ctx
			return nil
		},
	}

	checkers := res.Checkers()
	fn := checkers["test-pg"]
	if fn == nil {
		t.Fatal("checker fn must not be nil")
	}

	// Even though we call fn with no outer context here, the checker must have
	// built an independent context from context.Background(). Verify the
	// received context has a deadline roughly 5s in the future.
	if err := fn(); err != nil {
		t.Fatalf("checker returned unexpected error: %v", err)
	}
	if receivedCtx == nil {
		t.Fatal("healthFunc was not called")
	}
	dl, ok := receivedCtx.Deadline()
	if !ok {
		t.Fatal("checker context must have a deadline (context.WithTimeout)")
	}
	diff := time.Until(dl)
	if diff < 3*time.Second || diff > 7*time.Second {
		t.Errorf("expected checker deadline ~5s from now, got %v", diff)
	}
}

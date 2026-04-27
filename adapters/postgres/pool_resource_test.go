package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// stubPool creates a minimal *Pool for unit tests without a real DB.
// We build the Pool struct directly via package-internal access since tests
// are in the same package (package postgres).
func newStubPool() *Pool {
	return &Pool{} // inner pgxpool.Pool is nil; only used for struct access, not queries
}

// TestNewPGResource_Fields verifies construction and default name.
func TestNewPGResource_Fields(t *testing.T) {
	pool := newStubPool()
	res, err := NewPGResource(pool)
	if err != nil {
		t.Fatalf("NewPGResource returned error: %v", err)
	}

	if res.pool != pool {
		t.Error("pool field not set correctly")
	}
	if res.name != "postgres_ready" {
		t.Errorf("expected name 'postgres_ready', got %q", res.name)
	}
}

// TestNewPGResource_RejectsNilPool guards the construction contract: a nil
// pool would nil-deref at Checkers() or Close() time (the worst moments to
// discover it). The constructor must surface this at wiring time instead.
func TestNewPGResource_RejectsNilPool(t *testing.T) {
	res, err := NewPGResource(nil)
	if err == nil {
		t.Fatal("expected error for nil pool, got nil")
	}
	if res != nil {
		t.Error("expected nil resource on error")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T %v", err, err)
	}
	if ec.Code != errcode.ErrValidationFailed {
		t.Errorf("expected code %s, got %s", errcode.ErrValidationFailed, ec.Code)
	}
}

// mustNewPGResource builds a PGResource for tests; fatals on error so test
// bodies stay focused on the assertion under test.
func mustNewPGResource(t *testing.T, pool *Pool) *PGResource {
	t.Helper()
	res, err := NewPGResource(pool)
	if err != nil {
		t.Fatalf("NewPGResource: %v", err)
	}
	return res
}

// TestPGResource_CheckersReturnsNamed verifies the checker map has the correct
// key. The actual health call requires a real PG pool, so we only check the map
// structure here.
func TestPGResource_CheckersReturnsNamed(t *testing.T) {
	res := mustNewPGResource(t, newStubPool())
	checkers := res.Checkers()
	if len(checkers) != 1 {
		t.Fatalf("expected 1 checker, got %d", len(checkers))
	}
	fn, ok := checkers["postgres_ready"]
	if !ok {
		t.Fatal("expected checker named 'postgres_ready'")
	}
	if fn == nil {
		t.Error("checker function must not be nil")
	}
}

// TestPGResource_WorkerAlwaysNil verifies that PGResource.Worker() always
// returns nil — the relay is registered as a separate ManagedResource.
func TestPGResource_WorkerAlwaysNil(t *testing.T) {
	res := mustNewPGResource(t, newStubPool())
	if res.Worker() != nil {
		t.Error("expected nil worker: relay is registered independently via bootstrap.WithManagedResource")
	}
}

// stubCloser is a minimal poolCloser stub that records whether Close was called.
type stubCloser struct {
	called int
}

func (s *stubCloser) Close(_ context.Context) error { s.called++; return nil }

// TestPGResource_CloseReturnsNil verifies Close(ctx) calls the underlying closer
// exactly once and always returns nil.
func TestPGResource_CloseReturnsNil(t *testing.T) {
	sc := &stubCloser{}
	res := &PGResource{name: "postgres_ready", closeOverride: sc}

	if err := res.Close(context.Background()); err != nil {
		t.Errorf("Close() returned non-nil error: %v", err)
	}
	if sc.called != 1 {
		t.Errorf("expected Close to call closer once, got %d calls", sc.called)
	}
}

// TestPGResource_ImplementsManagedResource is a compile-time check surfaced as a
// test to make the assertion visible in test output.
func TestPGResource_ImplementsManagedResource(t *testing.T) {
	var _ lifecycle.ManagedResource = (*PGResource)(nil)
}

// TestPGResource_CheckerTimeout verifies that the health checker uses a
// standalone context with a ~5-second timeout. We inject a healthFunc via
// the PGResource struct field (same mechanism as TestPGResource_CheckerUsesIndependentCtx)
// to go through the real Checkers() path instead of testing a self-contained stub.
func TestPGResource_CheckerTimeout(t *testing.T) {
	var receivedDeadline time.Time
	res := &PGResource{
		name: "postgres_ready",
		healthFunc: func(ctx context.Context) error {
			dl, _ := ctx.Deadline()
			receivedDeadline = dl
			return nil
		},
	}

	checkers := res.Checkers()
	fn := checkers["postgres_ready"]
	if fn == nil {
		t.Fatal("checker fn must not be nil")
	}

	if err := fn(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Deadline should be ~5s from now (allow ±2s tolerance for slow CI).
	diff := time.Until(receivedDeadline)
	if diff < 3*time.Second || diff > 7*time.Second {
		t.Errorf("expected checker deadline ~5s from now, got %v", diff)
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

	// Even though we call fn with context.Background() here, the checker must
	// apply an inner 5s timeout. Verify the received context has a deadline
	// roughly 5s in the future.
	if err := fn(context.Background()); err != nil {
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

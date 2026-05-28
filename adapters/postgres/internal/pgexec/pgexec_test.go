package pgexec

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/pgrepoapproved"
)

// fakeTx is a minimal stub satisfying pgx.Tx for ambient-tx dispatch tests.
// Only Exec, Query, and QueryRow are overridden; all other methods are
// forwarded to the embedded pgx.Tx zero value (never called in these tests).
type fakeTx struct {
	pgx.Tx
	execCalled     bool
	queryCalled    bool
	queryRowCalled bool
}

func (f *fakeTx) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	f.execCalled = true
	return pgconn.CommandTag{}, nil
}

func (f *fakeTx) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	f.queryCalled = true
	return &fakeRows{}, nil
}

// fakeRows is a minimal pgx.Rows stub that does nothing and is immediately closed.
type fakeRows struct{ pgx.Rows }

func (r *fakeRows) Close() {}

func (f *fakeTx) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	f.queryRowCalled = true
	return &fakeRow{}
}

// fakeRow satisfies pgx.Row with a no-op Scan.
type fakeRow struct{}

func (r *fakeRow) Scan(_ ...any) error { return nil }

// TestExec_RoutesThroughAmbientTx verifies that Exec dispatches to the ambient
// tx when ctx carries one, not to the pool.
func TestExec_RoutesThroughAmbientTx(t *testing.T) {
	tx := &fakeTx{}
	ctx := context.WithValue(context.Background(), persistence.TxCtxKey, tx)
	// pool is nil; ambient-tx branch must fire before any pool dereference.
	e := New(nil)
	_, err := e.Exec(ctx, "SELECT 1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !tx.execCalled {
		t.Fatal("expected ambient tx Exec to be called, but it was not")
	}
}

// TestQuery_RoutesThroughAmbientTx verifies that Query dispatches to the
// ambient tx when ctx carries one, not to the pool.
func TestQuery_RoutesThroughAmbientTx(t *testing.T) {
	tx := &fakeTx{}
	ctx := context.WithValue(context.Background(), persistence.TxCtxKey, tx)
	// pool is nil; ambient-tx branch must fire before any pool dereference.
	e := New(nil)
	rows, err := e.Query(ctx, "SELECT 1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rows != nil {
		rows.Close()
	}
	if !tx.queryCalled {
		t.Fatal("expected ambient tx Query to be called, but it was not")
	}
}

// TestQueryRow_RoutesThroughAmbientTx verifies that QueryRow dispatches to the
// ambient tx when ctx carries one, not to the pool.
func TestQueryRow_RoutesThroughAmbientTx(t *testing.T) {
	tx := &fakeTx{}
	ctx := context.WithValue(context.Background(), persistence.TxCtxKey, tx)
	// pool is nil; ambient-tx branch must fire before any pool dereference.
	e := New(nil)
	row := e.QueryRow(ctx, "SELECT 1")
	if row == nil {
		t.Fatal("expected non-nil row from ambient tx QueryRow")
	}
	if !tx.queryRowCalled {
		t.Fatal("expected ambient tx QueryRow to be called, but it was not")
	}
}

// mockExec is an in-package non-*pgExecutor PGExecutor impl used to exercise
// ExecDirect's type-assertion guard. PGExecutor is sealed (sealPGExecutor is
// unexported), so this mock can only exist inside package pgexec — which is
// exactly the residual surface the guard covers: an in-package value that
// satisfies PGExecutor but is not the sanctioned *pgExecutor.
type mockExec struct{}

func (mockExec) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (mockExec) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	return nil, errors.ErrUnsupported
}

func (mockExec) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row { return nil }

func (mockExec) sealPGExecutor() {}

// TestExecDirect_PanicsOnNonSealedExecutor verifies pgexec.ExecDirect's
// type-assertion guard panics (A-class assertion) when given a PGExecutor
// whose dynamic type isn't *pgExecutor. In production this branch is
// unreachable — the sealed interface guarantees every value originates from
// New and is *pgExecutor; the in-package mock is the only way to reach it,
// confirming the guard rather than a silent ambient-tx bypass.
func TestExecDirect_PanicsOnNonSealedExecutor(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected ExecDirect on non-*pgExecutor to panic")
		}
		err, ok := r.(error)
		if !ok {
			t.Fatalf("expected panic value to be an error, got %T", r)
		}
		var ec *errcode.Error
		if !errors.As(err, &ec) {
			t.Fatalf("expected A-class *errcode.Error panic payload (errcode.Assertion), got %T: %v", err, err)
		}
		if !strings.Contains(err.Error(), "must originate from pgexec.New") {
			t.Fatalf("unexpected panic error: %v", err)
		}
	}()
	// The Approve token here only satisfies the compiler (ExecDirect's first
	// param is Approval); this is a test-only mock path, not an ADR bypass —
	// the !ok branch is unreachable in production (sealed interface).
	_, _ = ExecDirect(pgrepoapproved.Approve(pgrepoapproved.RevokeSessionCascade), mockExec{}, context.Background(), "SELECT 1")
}

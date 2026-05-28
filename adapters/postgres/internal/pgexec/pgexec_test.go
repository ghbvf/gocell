package pgexec

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ghbvf/gocell/kernel/persistence"
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

// mockExec is a non-sealed PGExecutor impl used to exercise ExecDirect's
// type-assertion guard against external impls (security boundary check).
type mockExec struct{}

func (m mockExec) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (m mockExec) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	return nil, errors.ErrUnsupported
}

func (m mockExec) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row { return nil }

// TestExecDirect_RejectsNonSealedExecutor verifies that pgexec.ExecDirect's
// type-assertion guard fires when given a PGExecutor whose dynamic type isn't
// *pgExecutor (e.g., an external mock). This blocks mock-mediated bypass of
// ambient-tx routing — even if an external party implements the PGExecutor
// interface, ExecDirect refuses to invoke pool.Exec on it.
func TestExecDirect_RejectsNonSealedExecutor(t *testing.T) {
	_, err := ExecDirect(mockExec{}, context.Background(), "SELECT 1")
	if err == nil {
		t.Fatal("expected ExecDirect on non-sealed executor to return error")
	}
	if !errors.Is(err, errExecDirectOnNonSealedExecutor) {
		t.Fatalf("expected errExecDirectOnNonSealedExecutor, got %v", err)
	}
}

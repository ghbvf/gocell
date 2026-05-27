package pgexec

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/pgrepoapproved"
)

// fakeTx is a minimal stub satisfying pgx.Tx for ambient-tx dispatch tests.
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

type fakeRows struct{ pgx.Rows }

func (r *fakeRows) Close() {}

func (f *fakeTx) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	f.queryRowCalled = true
	return &fakeRow{}
}

type fakeRow struct{}

func (r *fakeRow) Scan(_ ...any) error { return nil }

func TestExec_RoutesThroughAmbientTx(t *testing.T) {
	tx := &fakeTx{}
	ctx := context.WithValue(context.Background(), persistence.TxCtxKey, tx)
	e := New(nil)
	if _, err := e.Exec(ctx, "SELECT 1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !tx.execCalled {
		t.Fatal("expected ambient tx Exec to be called")
	}
}

func TestQuery_RoutesThroughAmbientTx(t *testing.T) {
	tx := &fakeTx{}
	ctx := context.WithValue(context.Background(), persistence.TxCtxKey, tx)
	e := New(nil)
	rows, err := e.Query(ctx, "SELECT 1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rows != nil {
		rows.Close()
	}
	if !tx.queryCalled {
		t.Fatal("expected ambient tx Query to be called")
	}
}

func TestQueryRow_RoutesThroughAmbientTx(t *testing.T) {
	tx := &fakeTx{}
	ctx := context.WithValue(context.Background(), persistence.TxCtxKey, tx)
	e := New(nil)
	row := e.QueryRow(ctx, "SELECT 1")
	if row == nil {
		t.Fatal("expected non-nil row")
	}
	if !tx.queryRowCalled {
		t.Fatal("expected ambient tx QueryRow to be called")
	}
}

// mockExec is an in-package non-*pgExecutor PGExecutor impl used to exercise
// ExecDirect's type-assertion guard. PGExecutor is sealed (sealPGExecutor is
// unexported), so this mock can only exist inside package pgexec — exactly the
// residual surface the guard covers.
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
// unreachable; the in-package mock is the only way to reach it.
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
		if !strings.Contains(err.Error(), "must originate from pgexec.New") {
			t.Fatalf("unexpected panic error: %v", err)
		}
	}()
	_, _ = ExecDirect(pgrepoapproved.Approve("test-non-sealed"), mockExec{}, context.Background(), "SELECT 1")
}

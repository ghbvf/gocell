package pgexec

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ghbvf/gocell/framework/kernel/persistence"
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

// TestAcquireTx_ReturnsAmbientWhenCtxCarriesTx verifies that AcquireTx returns
// (ambient, owned=false, nil) when ctx carries a pgx.Tx, so the caller skips
// Commit/Rollback. This is the L2 OutboxFact invariant for saga: joining the
// caller's ambient tx atomically with the outbox row.
func TestAcquireTx_ReturnsAmbientWhenCtxCarriesTx(t *testing.T) {
	ambient := &fakeTx{}
	ctx := context.WithValue(context.Background(), persistence.TxCtxKey, ambient)
	e := New(nil)
	tx, owned, err := e.AcquireTx(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owned {
		t.Fatal("expected owned=false when ctx carries ambient tx")
	}
	if tx != ambient {
		t.Fatal("expected returned tx to be the ambient one")
	}
}

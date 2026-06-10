//go:build integration

// exec_direct_integration_test.go covers ExecDirect's residual sealed-impl
// guard. Lives under the same `integration` build tag as ExecDirect itself
// (exec_direct_integration.go) so the test-only mock + panic-recovery path
// stays out of the production-build symbol table.

package pgexec

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/pgrepoapproved"
)

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
	_, _ = ExecDirect(pgrepoapproved.Approve(pgrepoapproved.IntegrationTestDeleteUser), mockExec{}, context.Background(), "SELECT 1")
}

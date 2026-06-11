// Package pgexec is the sealed PostgreSQL executor funnel for adapters/postgres.
// It is the ONLY location in this adapter's package tree permitted to hold a
// *pgxpool.Pool. The sub-package boundary makes the pool field compile-time
// unreachable from the parent package; stores consume the pool exclusively
// through the exported PGExecutor interface.
//
// ref: ent/ent dialect/sql/driver.go (unexported impl + exported interface)
// ref: sqlc DBTX (`db DBTX` unexported field in generated Queries)
//
// Enforced by archtest PG-REPO-AMBIENT-TX-01 R1 (sealed holder: any
// *pgxpool.Pool field must live in a package whose import path ends with
// /internal/pgexec) and R2 (cross-package wrap funnel: New*-constructors with
// a *pgxpool.Pool parameter must call pgexec.New(pool)).
//
// Usage: call pgexec.New(pool) once inside your New*-prefixed constructor
// and store the returned PGExecutor as an unexported field on your repo /
// store struct. All SQL goes through that field; the raw pool stays sealed
// in this sub-package.
package pgexec

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/pkg/pgrepoapproved"
)

// PGExecutor is the sealed read/write surface routed through the ambient
// transaction (when ctx carries one) or directly against the pool. The
// concrete implementation is unexported; the pool field is unreachable from
// outside this package by Go visibility.
//
// The interface is SEALED via the unexported sealPGExecutor marker method:
// only *pgExecutor (in this package) can implement it, so a parent package
// cannot declare a parallel PGExecutor implementation that holds its own raw
// pool and skips ambient-tx routing. Every PGExecutor value therefore
// originates from New — a type-system guarantee that backstops the R1
// file-extension archtest scope. Regression-guarded by archtest
// PG-REPO-AMBIENT-TX-01 InterfaceSealed (asserts exactly one unexported marker
// method, implemented only by *pgExecutor).
//
// ExecDirect is intentionally NOT a method on this interface — it is a
// top-level function pgexec.ExecDirect(approval, e, ctx, sql, args...). This
// closes the subset-interface bypass vector (ai-robust.md §Hard 范本 #2 typed
// marker funnel): a caller cannot declare a local interface re-shape with the
// same method set and call ExecDirect through it, because ExecDirect simply
// isn't a method anywhere. The only sanctioned callsite form is
// pgexec.ExecDirect(pgrepoapproved.Approve(pgrepoapproved.<CatalogConst>),
// s.db, ctx, ...), identified by archtest R3 via callee-identity resolution
// + the call-bound typed ApprovalReason catalog token.
type PGExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	sealPGExecutor()
}

// pgExecutor is the unexported impl; outside this package the only way to
// obtain a PGExecutor is via New.
type pgExecutor struct {
	pool *pgxpool.Pool
}

// New wraps a *pgxpool.Pool in the sealed PGExecutor funnel. This is the ONLY
// sanctioned constructor; archtest R2 enforces that any New*-prefixed function
// in any package receiving a *pgxpool.Pool parameter must call pgexec.New on
// that parameter.
//
// Passing a nil pool is permitted in unit tests that exercise logic firing
// before any SQL method is called (e.g. ambient-tx guard tests in
// corecells/accesscore/internal/adapters/postgres/tx_assert_test.go). Any SQL
// method invocation on a New(nil) instance will panic at the pool dereference.
func New(pool *pgxpool.Pool) PGExecutor {
	return &pgExecutor{pool: pool}
}

func (*pgExecutor) sealPGExecutor() {}

func (e *pgExecutor) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if tx, ok := persistence.TxFromContext[pgx.Tx](ctx); ok {
		return tx.Exec(ctx, sql, args...)
	}
	return e.pool.Exec(ctx, sql, args...)
}

func (e *pgExecutor) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if tx, ok := persistence.TxFromContext[pgx.Tx](ctx); ok {
		return tx.QueryRow(ctx, sql, args...)
	}
	return e.pool.QueryRow(ctx, sql, args...)
}

func (e *pgExecutor) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if tx, ok := persistence.TxFromContext[pgx.Tx](ctx); ok {
		return tx.Query(ctx, sql, args...)
	}
	return e.pool.Query(ctx, sql, args...)
}

// ExecDirect bypasses the ambient transaction and executes directly against
// the underlying pool. It is the explicit compensation-path bypass reserved
// for ADR-approved cascade-revoke and similar independent-commit semantics.
//
// The first parameter is a pgrepoapproved.Approval token that MUST be minted
// inline as pgrepoapproved.Approve(pgrepoapproved.<CatalogConst>) at the
// callsite: the authorization is bound to the call expression itself, so a
// bypass cannot exist without a documented reason and a reason cannot be
// stranded away from its call. Form is a top-level function (not a method) so
// no local interface re-shape can invoke it.
//
// archtest R3 (global scope) enforces:
//  1. callee identity is pgexec.ExecDirect (Pkg().Path() ending /internal/pgexec)
//  2. arg[0] is an inline CallExpr to pgrepoapproved.Approve whose own arg[0]
//     resolves via *types.Info.Uses to a *types.Const declared in
//     pkg/pgrepoapproved with type pgrepoapproved.ApprovalReason (rejects
//     locally-declared ApprovalReason consts, ApprovalReason("…") type
//     conversion, string literals, BinaryExpr concat, fmt.Sprintf, and any
//     reused/pre-constructed Approval variable)
//
// archtest R4 (reverse, global scope) enforces every pgrepoapproved.Approve
// callsite appears exactly as Args[0] of a pgexec.ExecDirect call — closing
// the orphan-Approve dead-code form.
//
// Currently exactly one production callsite exists:
// adapters/postgres/refresh_store.go::revokeSessionDetachedAt.
func ExecDirect(_ pgrepoapproved.Approval, e PGExecutor, ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	impl, ok := e.(*pgExecutor)
	if !ok {
		// Unreachable in production: PGExecutor is sealed (sealPGExecutor marker),
		// so every value originates from New and is *pgExecutor. A non-*pgExecutor
		// here is a programmer error reachable only from in-package test mocks —
		// an A-class assertion panic (state-machine unreachable branch), not a
		// recoverable error return.
		panic(panicregister.Approved("pgexec-execdirect-non-sealed",
			errcode.Assertion("pgexec.ExecDirect: PGExecutor must originate from pgexec.New")))
	}
	return impl.pool.Exec(ctx, sql, args...)
}

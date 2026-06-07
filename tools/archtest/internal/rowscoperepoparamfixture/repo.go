//go:build archtest_fixture

// Package rowscoperepoparamfixture provides deliberate RED fixtures for the
// ROWSCOPE-REPO-PARAM-FUNNEL-01 archtest. Loaded only when the archtest_fixture
// build tag is set (the tag literal must agree with the unexported fixtureBuildTag
// const in tools/archtest/fixture.go; Go build directives cannot reference a Go const).
//
// The build tag excludes this package from `go build ./...` / `go test ./...`,
// so it never pollutes real-repo scans.
//
// # RED fixtures covered
//
//   - FakeRowScopedRepo.GetByThing — a read method with NO tenant.RowVisibility
//     obligation parameter at all; ROWSCOPE-REPO-PARAM-FUNNEL-01 MUST flag this.
//   - FakeRowScopedRepo.WrongPosition — tenant.RowVisibility present but at the
//     wrong position (param[2] after a non-ctx, non-tenant param); the obligation
//     slot here is param[1] (no positional TenantID), so the position assertion
//     MUST flag this.
//   - FakeRowScopedRepo.GoodNoTenant — tenant.RowVisibility at param[1] (after ctx,
//     no positional tenant); MUST NOT flag.
//   - FakeRowScopedRepo.GoodWithTenant — tenant.RowVisibility at param[2] (after
//     ctx + tenant.TenantID); MUST NOT flag (covers the future accesscore-style
//     enrollment where a positional TenantID precedes the obligation).
package rowscoperepoparamfixture

import (
	"context"

	"github.com/ghbvf/gocell/pkg/tenant"
)

// FakeRowScopedRepo mirrors the shape of a tenant-scoped list/get repo interface.
type FakeRowScopedRepo interface {
	// VIOLATION (missing obligation): a read with no tenant.RowVisibility
	// parameter. A row-scoped read that omits the obligation reopens the
	// "no row-visibility predicate" hole — the scanner MUST flag this.
	//
	//nolint:all // intentional violation for archtest RED fixture
	GetByThing(ctx context.Context, seq int64) (string, error)

	// VIOLATION (wrong position): tenant.RowVisibility is present but at position 2
	// behind a non-ctx, non-tenant param. The obligation slot for this signature
	// (no positional TenantID) is param[1]; the position assertion MUST flag this.
	//
	//nolint:all // intentional position-violation for archtest RED fixture
	WrongPosition(ctx context.Context, seq int64, vis tenant.RowVisibility) error

	// OK: tenant.RowVisibility at param[1] (after ctx, no positional tenant) — the
	// sanctioned shape for ledger.Store-style repos. MUST NOT flag.
	GoodNoTenant(ctx context.Context, vis tenant.RowVisibility, seq int64) error

	// OK: tenant.RowVisibility at param[2] (after ctx + tenant.TenantID) — the
	// sanctioned shape for accesscore-style repos that carry a positional tenant.
	// MUST NOT flag.
	GoodWithTenant(ctx context.Context, t tenant.TenantID, vis tenant.RowVisibility, id string) error
}

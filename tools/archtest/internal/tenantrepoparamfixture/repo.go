//go:build archtest_fixture

// Package tenantrepoparamfixture provides deliberate RED fixtures for the
// TENANT-REPO-PARAM-FUNNEL-01 archtest. Loaded only when the archtest_fixture
// build tag is set (the tag literal must agree with the unexported fixtureBuildTag
// const in tools/archtest/fixture.go; Go build directives cannot reference a Go const).
//
// The build tag excludes this package from `go build ./...` / `go test ./...`,
// so it never pollutes real-repo scans.
//
// # RED fixtures covered
//
//   - FakeRepo.GetByThing — tenant slot typed as plain string (not TenantID);
//     TENANT-REPO-PARAM-FUNNEL-01 MUST flag this.
//   - FakeRepo.WrongPosition — tenant.TenantID present but at position 2 (after a
//     non-ctx string param), not position 1; TENANT-REPO-PARAM-FUNNEL-01 position
//     assertion MUST flag this.
//   - FakeRepo.GoodMethod — tenant.TenantID at position 1 (after ctx); MUST NOT flag.
//
// Note: TENANT-REPO-CALLSITE-FUNNEL-01 was retired in PR-3b (#1617) — the
// tenant-less UserRepository.GetByID was deleted (all user-repo methods now have
// a typed tenant.TenantID position param), so there is no tenant-less method left
// to lock. FakeUserRepository and FakeGetByIDCaller have been removed accordingly.
package tenantrepoparamfixture

import (
	"context"

	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// FakeRepo mirrors the shape of accesscore's tenant-scoped repo interfaces.
type FakeRepo interface {
	// VIOLATION (string-typed slot): the tenant slot is a plain string, not
	// tenant.TenantID. A repo method scoping by a string "tenant" sidesteps the
	// typed funnel — the scanner MUST flag this.
	//
	//nolint:all // intentional violation for archtest RED fixture
	GetByThing(ctx context.Context, tenantID string, id string) (string, error)

	// VIOLATION (wrong position): tenant.TenantID is present but at position 2
	// (after a non-ctx extra string), not at position 1. The position assertion
	// MUST flag this: ctx=param[0], extraParam=param[1], t=param[2].
	//
	//nolint:all // intentional position-violation for archtest RED fixture — F12
	WrongPosition(ctx context.Context, extra string, t tenant.TenantID) error

	// OK: a real tenant.TenantID positional parameter at position 1. The scanner
	// MUST NOT flag this — it is the sanctioned shape.
	GoodMethod(ctx context.Context, t tenant.TenantID, id string) error
}

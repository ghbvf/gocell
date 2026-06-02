//go:build archtest_fixture

// Package tenantrepoparamfixture provides deliberate RED fixtures for
// TENANT-REPO-PARAM-FUNNEL-01 and TENANT-REPO-CALLSITE-FUNNEL-01 archtests.
// Loaded only when the archtest_fixture build tag is set (the tag literal must
// agree with the unexported fixtureBuildTag const in tools/archtest/fixture.go;
// Go build directives cannot reference a Go const).
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
//   - FakeUserRepository / FakeGetByIDCaller — callsite violation fixture: a fake
//     slice package calling the tenant-less GetByID-shaped method from outside the
//     sanctioned allowlist; TENANT-REPO-CALLSITE-FUNNEL-01 MUST flag this.
package tenantrepoparamfixture

import (
	"context"

	"github.com/ghbvf/gocell/pkg/tenant"
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

// FakeUserRepository is a minimal interface that mimics the tenant-less GetByID
// carve-out shape — used by the TENANT-REPO-CALLSITE-FUNNEL-01 callsite fixture.
type FakeUserRepository interface {
	// GetByID is the tenant-less by-PK carve-out shape.
	GetByID(ctx context.Context, id string) (string, error)
}

// FakeGetByIDCaller is an unsanctioned caller of the tenant-less GetByID. This
// is the RED fixture for TENANT-REPO-CALLSITE-FUNNEL-01: a call from a package
// outside the sanctioned allowlist MUST be flagged.
//
//nolint:all // intentional violation for archtest RED fixture — F3
func FakeGetByIDCaller(ctx context.Context, repo FakeUserRepository) (string, error) {
	// VIOLATION: calling the tenant-less GetByID from an unsanctioned package.
	// In production, only the explicit allowlist packages (sessionrefresh /
	// sessionvalidate / rbacassign + adapter-internal self-calls) are permitted.
	return repo.GetByID(ctx, "some-id") //nolint:all // RED fixture
}

//go:build archtest_fixture

// Package tenantrepoparamfixture is a deliberate TENANT-REPO-PARAM-FUNNEL-01
// negative fixture loaded only when the archtest_fixture build tag is set (the
// tag literal must agree with the unexported fixtureBuildTag const in
// tools/archtest/fixture.go; Go build directives cannot reference a Go const).
//
// The build tag excludes this package from `go build ./...` / `go test ./...`,
// so it never pollutes real-repo scans. It is loaded explicitly by
// TestTenantRepoParamFunnel01_ScannerCatchesViolation via RunTypedFixture, which
// runs the SAME scanner the production test uses against this fixture interface.
//
// It proves the scanner is not vacuous: a repo method whose tenant slot is typed
// as a plain string (instead of tenant.TenantID) MUST be reported, while a method
// carrying a real tenant.TenantID positional parameter MUST NOT be.
package tenantrepoparamfixture

import (
	"context"

	"github.com/ghbvf/gocell/pkg/tenant"
)

// FakeRepo mirrors the shape of accesscore's tenant-scoped repo interfaces.
type FakeRepo interface {
	// VIOLATION: the tenant slot is a plain string, not tenant.TenantID. A repo
	// method scoping by a string "tenant" sidesteps the typed funnel — the
	// scanner MUST flag this.
	//
	//nolint:all // intentional violation for archtest RED fixture
	GetByThing(ctx context.Context, tenantID string, id string) (string, error)

	// OK: a real tenant.TenantID positional parameter. The scanner MUST NOT flag
	// this — it is the sanctioned shape.
	GoodMethod(ctx context.Context, t tenant.TenantID, id string) error
}

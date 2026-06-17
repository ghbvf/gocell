//go:build archtest_fixture

// Package auditquerytenantparamfixture provides deliberate RED/GREEN fixtures for
// the AUDIT-QUERY-TENANT-PARAM-01 archtest. Loaded only when the archtest_fixture
// build tag is set (the tag literal must agree with the unexported fixtureBuildTag
// const in tools/archtest/fixture.go; Go build directives cannot reference a Go const).
//
// The build tag excludes this package from `go build ./...` / `go test ./...`, so
// it never pollutes real-repo scans.
//
// # Fixtures covered
//
//   - FakeGoodQueryStore.Query — carries tenant.TenantID at param[1] (after ctx),
//     the sanctioned shape; AUDIT-QUERY-TENANT-PARAM-01 MUST NOT flag it.
//   - FakeBadQueryStore.Query — OMITS the typed tenant param (the row-visibility
//     obligation sits at param[1] instead), exactly the drift the ROWSCOPE funnel
//     does NOT catch (its GoodNoTenant form treats a no-tenant Query as valid);
//     AUDIT-QUERY-TENANT-PARAM-01 MUST flag it.
package auditquerytenantparamfixture

import (
	"context"

	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// FakeGoodQueryStore is the sanctioned audit-read shape: ctx, then the typed
// tenant.TenantID scope at param[1], then the row-visibility obligation — for BOTH
// the list path (Query) and the single-entry path (GetByID, #1852).
type FakeGoodQueryStore interface {
	Query(ctx context.Context, t tenant.TenantID, vis tenant.RowVisibility, limit int64) error
	GetByID(ctx context.Context, t tenant.TenantID, vis tenant.RowVisibility, id string) error
}

// FakeBadQueryStore drops the typed tenant param on BOTH read methods. Each
// type-checks fine and PASSES ROWSCOPE-REPO-PARAM-FUNNEL-01 (obligation at param[1]
// is its GoodNoTenant form), so only AUDIT-QUERY-TENANT-PARAM-01 closes this
// tenant-axis-removal drift — for Query and GetByID alike.
//
//nolint:all // intentional violation for archtest RED fixture
type FakeBadQueryStore interface {
	Query(ctx context.Context, vis tenant.RowVisibility, limit int64) error
	GetByID(ctx context.Context, vis tenant.RowVisibility, id string) error
}

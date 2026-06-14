//go:build archtest_fixture

// Package tenantscopefixture is an archtest RED fixture for
// TENANT-TXSCOPE-WRITE-CALLER-01. It dot-imports pkg/tenant and calls WithScope
// as a BARE identifier (not the package-qualified tenant.WithScope selector). The
// pre-#1622 SelectorExpr-only detector would have missed this form; the Use-based
// detector (info.Uses) resolves the bare ident to the same *types.Func, so the
// caller-allowlist scan must flag this file (it is outside the allowlist). The
// fixture proves dot-import is genuinely covered, not just documented.
//
// (The file is gated behind the archtest_fixture build tag so the dot-import does
// not trip the repo-wide revive `dot-imports` lint nor enter normal builds.)
package tenantscopefixture

import (
	"context"

	. "github.com/ghbvf/gocell/framework/pkg/tenant" //nolint:revive,staticcheck // RED fixture: dot-import is the form under test
)

// dotImportScopeWrite writes a tx tenant scope via a dot-imported bare WithScope
// identifier. Outside the allowlist, the detector must report it.
func dotImportScopeWrite(ctx context.Context) context.Context {
	return WithScope(ctx, TenantID("00000000-0000-0000-0000-000000000000"))
}

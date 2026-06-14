//go:build archtest_fixture

// This file adds the alias-bypass RED forms for
// RECONCILE-BUILDER-FUNNEL-01 (#1292 r2 F2/F3): an import-aliased
// selector and a Go 1.23 type alias. The type-alias form is the one a bare
// *types.Named assertion (without types.Unalias) would miss, so it proves the
// Unalias fix is load-bearing. Gated by the archtest_fixture build tag.
package reconcileloopredfixture

import rc "github.com/ghbvf/gocell/framework/kernel/reconcile"

// loopTypeAlias is a Go 1.23 type alias to reconcile.Loop. Under
// gotypesalias=1, a composite literal of it denotes a *types.Alias, so the
// scanner must types.Unalias before the *types.Named assertion.
type loopTypeAlias = rc.Loop

//nolint:all // intentional violations for archtest RED fixture
var (
	// import-aliased selector form: rc.Loop{} (resolves to reconcile.Loop).
	redLoopViaImportAlias = &rc.Loop{} //nolint:unused
	// Go 1.23 type-alias form: needs types.Unalias to be flagged.
	redLoopViaTypeAlias = &loopTypeAlias{} //nolint:unused
)

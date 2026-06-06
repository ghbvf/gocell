//go:build archtest_fixture

// Package systemprincipalinstallfixture is an archtest RED fixture for
// PROJECTION-SYSTEM-PRINCIPAL-INSTALL-CALLER-01. It dot-imports kernel/projection
// and calls InstallSystemPrincipal as a BARE identifier (not the package-qualified
// projection.InstallSystemPrincipal selector) from a function that is NOT the
// sanctioned saga-carrier RestoreContext.
//
// The pre-fix SelectorExpr-only scanner would have missed this bare-ident form
// (and the same-package bare-ident form it stands in for); the Use-based scanner
// (info.Uses) resolves the bare ident to the same *types.Func and binds it to its
// enclosing caller, so the callsite-allowlist scan must flag this file. The
// fixture is the negative control proving the funnel is symbol-and-callsite level,
// not syntax/file level.
//
// (Gated behind the archtest_fixture build tag so the dot-import neither trips the
// repo-wide revive dot-imports lint nor enters normal builds / the live scan.)
package systemprincipalinstallfixture

import (
	"context"

	. "github.com/ghbvf/gocell/kernel/projection" //nolint:revive,staticcheck // RED fixture: dot-import bare-ident is the form under test
)

// bypassInstallFromUnsanctionedCaller installs the system principal from a caller
// that is NOT (*sagaProjectionEvent).RestoreContext. Outside the callsite
// allowlist, the scanner must report this single reference.
func bypassInstallFromUnsanctionedCaller(ctx context.Context) context.Context {
	return InstallSystemPrincipal(ctx)
}

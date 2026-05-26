//go:build archtest_fixture

// Negative case: an unrelated package whose local alias is the same string
// "auth" the prior AST matcher keyed on. The decoy package (notauth) defines
// identifiers with the same names as the banned auth symbols, but lives at a
// different import path. The typed scanner must NOT flag these references —
// they are the false-positive surface the AST-only matcher conflated.
//
// Expected hits: 0.

package nodeletedauthsymbolsfixture

import auth "github.com/ghbvf/gocell/tools/archtest/internal/nodeletedauthsymbolsfixture/notauth"

// NegativeOtherPkgReferences references same-named identifiers under a
// different package path. The scanner must produce zero diagnostics for this
// function.
func NegativeOtherPkgReferences() {
	_ = auth.RoleInternalAdmin
	_ = auth.ServiceNameInternal
	_ = auth.BuiltinServiceRoles("svc")
}

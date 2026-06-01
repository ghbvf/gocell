//go:build archtest_fixture

// Package principalkindifchainfixture is the PRINCIPAL-KIND-EXHAUSTIVE-SWITCH-01
// blind-spot reverse self-check (charter §"强制盲区自检"). The rule deliberately
// scopes itself to `switch` statements; if/else-if chains comparing PrincipalKind
// are OUT of scope. This fixture contains ONLY a non-exhaustive if/else-if chain
// over auth.PrincipalKind (no switch). The detector scanPrincipalKindSwitches,
// pointed at this package, MUST produce ZERO diagnostics — proving the if-chain
// blind spot is a true non-false-positive, not an accidental gap.
//
// DO NOT use this package in production code.
package principalkindifchainfixture

import "github.com/ghbvf/gocell/runtime/auth"

// ifChain handles only some PrincipalKind values via an if/else-if chain (no
// PrincipalDevice, no switch). The exhaustiveness rule must NOT flag this.
func ifChain(k auth.PrincipalKind) string {
	if k == auth.PrincipalUser {
		return "user"
	} else if k == auth.PrincipalService {
		return "service"
	}
	return "other"
}

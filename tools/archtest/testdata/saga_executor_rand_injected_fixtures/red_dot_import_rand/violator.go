//go:build archtest_fixture

package reddotimportrand

import (
	// nosemgrep: go.lang.security.audit.crypto.math_random.math-random-used // archtest red fixture; SAGA-EXECUTOR-RAND-INJECTED-01 dot-import detector self-test
	. "math/rand/v2"
)

// jitter computes a jitter value using the dot-imported package-level global
// Int64N. The call is a bare Ident (no SelectorExpr), exercising the detector's
// ast.Ident walk branch. Violates SAGA-EXECUTOR-RAND-INJECTED-01.
func jitter(base int64) int64 {
	// Violation: dot-imported package-level global Int64N call.
	return Int64N(base)
}

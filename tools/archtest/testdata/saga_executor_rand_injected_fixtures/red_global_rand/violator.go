//go:build archtest_fixture

package redexecutorglobalrand

import (
	"math/rand/v2"
)

// jitter computes a jitter duration using the package-level global rand.Int64N.
// This directly calls the global random source instead of using an injected
// rand.Rand — violating SAGA-EXECUTOR-RAND-INJECTED-01.
func jitter(base int64) int64 {
	// Violation: package-level global rand.Int64N call.
	return rand.Int64N(base)
}

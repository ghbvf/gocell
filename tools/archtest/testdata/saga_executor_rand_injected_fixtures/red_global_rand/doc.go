//go:build archtest_fixture

// Package redexecutorglobalrand is a RED fixture for
// SAGA-EXECUTOR-RAND-INJECTED-01: a function in the package directly calls
// math/rand/v2's package-level global function rand.Int64N, bypassing the
// injected-random-source requirement. Expect one diagnostic.
package redexecutorglobalrand

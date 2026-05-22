//go:build archtest_fixture

// Package indirect_funcarg_red is a RED fixture for the blind-spot reverse
// self-test in TestEventuallyFunnel_NoIndirectReferences. It passes
// require.Eventually as a function argument — an indirect reference that
// bypasses the CallExpr-driven main scan.
package indirect_funcarg_red

import (
	"time"

	"github.com/stretchr/testify/require"
)

// eventuallyFn is the signature of require.Eventually.
type eventuallyFn func(require.TestingT, func() bool, time.Duration, time.Duration, ...interface{})

// passThrough receives a function value matching Eventually's signature.
func passThrough(_ eventuallyFn) {}

// UseRequireEventually passes require.Eventually as a function argument — indirect.
func UseRequireEventually() {
	passThrough(require.Eventually)
}

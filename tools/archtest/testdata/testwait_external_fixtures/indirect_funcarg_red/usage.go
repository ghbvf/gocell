//go:build archtest_fixture

// Package indirect_funcarg_red is a RED fixture for the blind-spot reverse
// self-test in TestExternalReasonLiteral_NoIndirectReferences. It passes
// testwait.External as a function argument — an indirect reference that
// bypasses the (callee, arg) form-uniqueness check of the main rule.
package indirect_funcarg_red

import (
	"time"

	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// externalFn is the signature of testwait.External.
type externalFn func(testwait.TB, string, func() bool, time.Duration, time.Duration, ...any)

// passThrough receives a function value matching External's signature.
func passThrough(_ externalFn) {}

// UseExternal passes testwait.External as a function argument — indirect reference.
func UseExternal() {
	passThrough(testwait.External)
}

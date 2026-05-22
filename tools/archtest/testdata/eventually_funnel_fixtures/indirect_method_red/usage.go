//go:build archtest_fixture

// Package indirect_method_red is a RED fixture for the blind-spot reverse
// self-test (TestEventuallyFunnel_NoIndirectReferences) covering the
// info.Selections path (MethodVal + MethodExpr):
//
//   - methodValRef: var bound to require.New(t).Eventually — MethodVal
//     (function value of a method on a specific receiver value).
//   - methodExprRef: var bound to (*assert.Assertions).EventuallyWithT —
//     MethodExpr (function value of a method expression with the receiver
//     as the first parameter).
//
// Both shapes are absent from info.Uses; the Selections-pass in
// scanFileForIndirectEventuallyReferences is what catches them.
package indirect_method_red

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// methodVal binds a *require.Assertions method to a function value — MethodVal.
func methodVal(t *testing.T) {
	methodValRef := require.New(t).Eventually // violation on this line
	_ = methodValRef
}

// methodExprRef holds (*assert.Assertions).EventuallyWithT — MethodExpr.
var methodExprRef = (*assert.Assertions).EventuallyWithT

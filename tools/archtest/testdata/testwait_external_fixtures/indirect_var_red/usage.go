//go:build archtest_fixture

// Package indirect_var_red is a RED fixture for the blind-spot reverse
// self-test in TestExternalReasonLiteral_NoIndirectReferences. It assigns
// testwait.External to a package-level variable — an indirect reference that
// bypasses the (callee, arg) form-uniqueness check of the main rule.
package indirect_var_red

import (
	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
)

// varRef holds testwait.External as a function value — indirect reference.
var varRef = testwait.External

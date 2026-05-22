//go:build archtest_fixture

// Package indirect_var_red is a RED fixture for the blind-spot reverse
// self-test in TestEventuallyFunnel_NoIndirectReferences. It assigns
// require.Eventually to a package-level variable — an indirect reference
// that would bypass the CallExpr-driven main scan if the reverse
// self-test were missing.
package indirect_var_red

import (
	"github.com/stretchr/testify/require"
)

// varRef holds require.Eventually as a function value — indirect reference.
var varRef = require.Eventually

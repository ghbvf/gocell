//go:build archtest_fixture

// Package indirect_var_f_red is a RED fixture for the blind-spot reverse
// self-test (TestEventuallyFunnel_NoIndirectReferences): a package-level
// var bound to require.Eventuallyf — an indirect reference to a *f variant
// that the prefix predicate must also cover on the existing Uses path.
package indirect_var_f_red

import (
	"github.com/stretchr/testify/require"
)

// varRef holds require.Eventuallyf as a function value — indirect *f reference.
var varRef = require.Eventuallyf

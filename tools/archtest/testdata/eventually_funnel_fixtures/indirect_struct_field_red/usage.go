//go:build archtest_fixture

// Package indirect_struct_field_red is a RED fixture for the blind-spot
// reverse self-test in TestEventuallyFunnel_NoIndirectReferences. It stores
// require.Eventually in a struct field — an indirect reference that
// bypasses the CallExpr-driven main scan.
package indirect_struct_field_red

import (
	"time"

	"github.com/stretchr/testify/require"
)

// wrapper holds a function value matching Eventually's signature.
type wrapper struct {
	F func(require.TestingT, func() bool, time.Duration, time.Duration, ...interface{})
}

// fieldRef stores require.Eventually in a struct field — indirect reference.
var fieldRef = wrapper{F: require.Eventually}

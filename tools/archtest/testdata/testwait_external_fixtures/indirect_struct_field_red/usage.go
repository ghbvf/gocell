//go:build archtest_fixture

// Package indirect_struct_field_red is a RED fixture for the blind-spot
// reverse self-test in TestExternalReasonLiteral_NoIndirectReferences. It
// stores testwait.External in a struct field — an indirect reference that
// bypasses the (callee, arg) form-uniqueness check of the main rule.
package indirect_struct_field_red

import (
	"time"

	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
)

// wrapper holds a function value matching External's signature.
type wrapper struct {
	F func(testwait.TB, string, func() bool, time.Duration, time.Duration, ...any)
}

// fieldRef stores testwait.External in a struct field — indirect reference.
var fieldRef = wrapper{F: testwait.External}

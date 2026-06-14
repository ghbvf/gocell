//go:build archtest_fixture

// Package indirect_reflect_red is a RED fixture for the blind-spot reverse
// self-test in TestExternalReasonLiteral_NoIndirectReferences. It wraps
// testwait.External in reflect.ValueOf — an indirect reference that bypasses
// the (callee, arg) form-uniqueness check of the main rule.
package indirect_reflect_red

import (
	"reflect"

	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
)

// reflectRef wraps testwait.External using reflect.ValueOf — indirect reference.
var reflectRef = reflect.ValueOf(testwait.External)

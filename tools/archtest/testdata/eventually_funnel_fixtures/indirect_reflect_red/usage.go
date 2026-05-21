//go:build archtest_fixture

// Package indirect_reflect_red is a RED fixture for the blind-spot reverse
// self-test in TestEventuallyFunnel_NoIndirectReferences. It wraps
// require.Eventually in reflect.ValueOf — an indirect reference that
// bypasses the CallExpr-driven main scan.
package indirect_reflect_red

import (
	"reflect"

	"github.com/stretchr/testify/require"
)

// reflectRef wraps require.Eventually using reflect.ValueOf — indirect.
var reflectRef = reflect.ValueOf(require.Eventually)

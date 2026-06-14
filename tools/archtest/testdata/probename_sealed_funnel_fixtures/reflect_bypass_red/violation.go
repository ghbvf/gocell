// Package reflect_bypass_red is a synthetic violation fixture for
// PROBENAME-SEALED-FUNNEL-01/B1.
//
// It uses reflect.MethodByName("RegisterReadiness") to call the registration
// method indirectly, bypassing the type-safe funnel. The archtest B1 blind-spot
// scanner must detect this pattern.
//
// DO NOT use this package in production code.
package reflect_bypass_red

import (
	"reflect"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/healthz"
)

// bypassViaReflect demonstrates the B1 violation: using reflect.MethodByName
// to call RegisterReadiness instead of the typed funnel.
func bypassViaReflect(reg cell.Registrar, name healthz.ProbeName, prober healthz.Prober) {
	// VIOLATION B1: reflect bypass of RegisterReadiness funnel.
	// The correct pattern is: reg.RegisterReadiness(name, prober).
	reflect.ValueOf(reg).MethodByName("RegisterReadiness").Call([]reflect.Value{
		reflect.ValueOf(name),
		reflect.ValueOf(prober),
	})
}

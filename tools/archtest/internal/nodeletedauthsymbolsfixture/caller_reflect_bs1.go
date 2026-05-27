//go:build archtest_fixture

// BS-1 positive fixture: chained reflect.Value.{Field,Method}ByName calls
// whose constant string argument names a banned symbol. The BS-1 scanner
// delegates to the shared scanReflectStringArgCalls
// (REFLECT-STRING-ARG-SCANNER-01), which type-checks the receiver to
// reflect.Value and folds the constant string argument — so chained
// `reflect.ValueOf(x).MethodByName("...")` (the realistic reflective
// lookup form) is covered, not just direct `reflect.X("...")` calls.
//
// Companion test TestNO_DELETED_AUTH_SYMBOLS_01_BS1_FixtureCatchesReflect
// asserts both forms (FieldByName + MethodByName) produce exactly 1 hit
// each — total 2 — pinning the scanner-logic-verified contract.
//
// Expected hits: 2.

package nodeletedauthsymbolsfixture

import "reflect"

// reflectBS1Holder exposes a field with the same name as a banned const so
// FieldByName has a valid argument shape; the call is for AST/type analysis
// only, the result is discarded.
type reflectBS1Holder struct {
	ServiceNameInternal string
}

// ReflectBS1Bypass demonstrates the chained reflect.Value pattern that BS-1
// guards. Both calls are never invoked at runtime.
func ReflectBS1Bypass() {
	_ = reflect.ValueOf(reflectBS1Holder{}).FieldByName("ServiceNameInternal")
	_ = reflect.ValueOf(reflectBS1Holder{}).MethodByName("BuiltinServiceRoles")
}

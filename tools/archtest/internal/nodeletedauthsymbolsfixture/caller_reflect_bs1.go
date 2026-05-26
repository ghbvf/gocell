//go:build archtest_fixture

// BS-1 positive fixture: one direct reflect.<X>(...) call site whose
// string-literal argument contains a banned symbol name. Companion test
// TestNO_DELETED_AUTH_SYMBOLS_01_BS1_FixtureCatchesReflect asserts the
// scanner reports exactly 1 hit on this file — converts BS-1 from
// "absence-asserted-in-production" to "scanner-logic-verified".
//
// The direct-call form (sel.X is *ast.Ident "reflect") is the BS-1
// scanner's covered shape. Chained `reflect.TypeOf(...).MethodByName(...)`
// is an accepted BS-1a residual; documented in the main test file's
// package godoc.
//
// Expected hits: 1 (the reflect.ValueOf call below).

package nodeletedauthsymbolsfixture

import "reflect"

// ReflectBS1Bypass demonstrates the reflect direct-call pattern that BS-1
// guards. The call value is discarded — the fixture exists for AST/type
// analysis only.
func ReflectBS1Bypass() {
	_ = reflect.ValueOf("BuiltinServiceRoles")
}

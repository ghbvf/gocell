package typesutil

import "go/types"

// ImplementsInterface reports whether t — as a value OR via its pointer —
// satisfies iface. It mirrors the go/types.Implements(V, T) argument shape
// and adds the value-or-pointer convenience: a value type may satisfy iface
// only through its pointer method set, so when the value form does not
// implement and t is not already a pointer, the *t form is tried. The
// pointer wrap is skipped when t is already a *types.Pointer (a **T method
// set adds nothing over *T).
//
// Use ImplementsInterfaceExact when the no-pointer-fallback semantic is
// intentional — e.g. t may itself be an interface type and a synthetic
// pointer-to-interface check would be meaningless.
//
// Signature note: the backlog-described (typesInfo, expr, ifaceObj) shape
// was deliberately rejected — none of the consolidated call sites hold an
// ast.Expr at the point of check (they take a struct field, obj.Type(), or
// a parameter type). This shape mirrors stdlib go/types.Implements(V, T).
//
// AI-rebust note: the ImplementsInterface vs ImplementsInterfaceExact
// choice is a name-level (Medium) distinction — both share one signature,
// so picking the wrong one is not a compile error. The fixture-level guard
// is the discriminating test case in implements_interface_test.go. The
// Hard layer (banning raw go/types.Implements outside this file) is
// TYPESUTIL-IMPLEMENTS-FUNNEL-01, landing in PR-b (see below).
//
// Edge cases (covered by implements_interface_test.go):
//   - value-receiver impl / pointer-receiver-only impl via value / via *T
//   - t already *types.Pointer (true path, no double-wrap)
//   - t already *types.Pointer and non-impl (no **T fallback)
//   - promoted (embedded) interface methods
//   - generic instantiated named types
//   - non-impl / nil t / nil iface
//
// ref: golang.org/x/tools go/analysis/passes/copylock (types.Implements +
//
//	pointer-receiver method-set idiom)
func ImplementsInterface(t types.Type, iface *types.Interface) bool {
	if t == nil || iface == nil {
		return false
	}
	if types.Implements(t, iface) {
		return true
	}
	if _, isPtr := t.(*types.Pointer); !isPtr {
		return types.Implements(types.NewPointer(t), iface)
	}
	return false
}

// ImplementsInterfaceExact is the strict, value-only satisfaction check —
// the deliberate no-pointer-fallback counterpart of ImplementsInterface.
// Use it where t may itself be an interface type and a synthetic
// pointer-to-interface check would be semantically meaningless (e.g. the
// CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01 anonymous-interface bypass
// detector, where t is frequently a *types.Interface).
//
// Once TYPESUTIL-IMPLEMENTS-FUNNEL-01 lands (PR-b,
// tools/archtest/implements_funnel_test.go), this file will be the sole
// sanctioned site for raw go/types.Implements; CI will then reject the raw
// call anywhere else. Until PR-b merges that ban is NOT yet enforced — do
// not add new raw go/types.Implements calls outside this file in the
// interim.
func ImplementsInterfaceExact(t types.Type, iface *types.Interface) bool {
	if t == nil || iface == nil {
		return false
	}
	return types.Implements(t, iface)
}

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
// anonymous-interface bypass detector in cell_public_option_param_test).
//
// It is also the SOLE sanctioned raw go/types.Implements call site outside
// ImplementsInterface — TYPESUTIL-IMPLEMENTS-FUNNEL-01 bans the raw call
// everywhere else.
func ImplementsInterfaceExact(t types.Type, iface *types.Interface) bool {
	if t == nil || iface == nil {
		return false
	}
	return types.Implements(t, iface)
}

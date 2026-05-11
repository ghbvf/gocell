// Package typesutil exposes small, dependency-light helpers around go/types
// for test and tool code in this module.
//
// Boundary with tools/archtest/internal/typeseval:
//   - typeseval is archtest-internal (SharedResolver, build-tag matrix,
//     generated-path skip, etc.) and lives under internal/ so it cannot be
//     imported from outside tools/archtest.
//   - typesutil is non-internal and importable from any _test.go in the
//     module (archtest rules, kernel/* tests).
//
// Constraints:
//   - stdlib + golang.org/x/tools only.
//   - No runtime/cells/adapters imports.
//   - Each helper does one go/types operation.
//
// ref: golang.org/x/tools/go/types/typeutil StaticCallee
// ref: golang.org/x/tools/internal/typesinternal ReceiverNamed (pattern source; the upstream package is internal and unimportable)
package typesutil

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/types/typeutil"
)

// ResolveReceiverType returns the named receiver type for a method call,
// e.g. (*scanner.Scanner) for s.EachInSubtree(...). It returns ok=false for
// any call that cannot be statically resolved to a method on a named type:
//
//   - builtins (StaticCallee returns nil)
//   - interface dispatch (StaticCallee filters interfaceMethod)
//   - package-level functions (Signature.Recv() is nil)
//   - method values (the call expression's Fun is a variable, not a Selector)
//   - methods on anonymous/unnamed types (no *types.Named to return)
//
// The isPtr return distinguishes pointer- and value-receiver methods.
// Callers that only care about the named type may discard isPtr.
//
// Edge cases:
//   - Promoted (embedded) methods return the embedded type's *types.Named,
//     not the outer type. This mirrors what StaticCallee resolves to.
//   - Generic methods return the generic base *types.Named, not the
//     instantiation. Callers needing instantiation info call TypeArgs() on
//     the returned named type.
//   - Alias-pointer chains (`type P = *T; func (P) M()`) unalias before the
//     pointer cast, matching upstream typesinternal.ReceiverNamed.
func ResolveReceiverType(typesInfo *types.Info, call *ast.CallExpr) (named *types.Named, isPtr bool, ok bool) {
	if typesInfo == nil || call == nil {
		return nil, false, false
	}
	fn := typeutil.StaticCallee(typesInfo, call)
	if fn == nil {
		return nil, false, false
	}
	sig, sigOK := fn.Type().(*types.Signature)
	if !sigOK || sig.Recv() == nil {
		return nil, false, false
	}
	isPtr, named = receiverNamed(sig.Recv())
	return named, isPtr, named != nil
}

// receiverNamed inlines golang.org/x/tools/internal/typesinternal.ReceiverNamed,
// which is unimportable due to Go's internal/ visibility rule. Behaviorally
// equivalent: unalias before the pointer cast so alias-pointer chains
// (`type P = *T`) resolve correctly.
func receiverNamed(recv *types.Var) (isPtr bool, named *types.Named) {
	t := recv.Type()
	if ptr, ok := types.Unalias(t).(*types.Pointer); ok {
		isPtr = true
		t = ptr.Elem()
	}
	named, _ = types.Unalias(t).(*types.Named)
	return
}

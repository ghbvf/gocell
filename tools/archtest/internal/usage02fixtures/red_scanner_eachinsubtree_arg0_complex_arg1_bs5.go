package usage02fixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// arg0ComplexBS5Helper is the named helper at arg[1] — BS5 form (named
// function value, not inline FuncLit). The fixture deliberately fills the
// helper body with content so its byte-size is non-trivial; this is
// irrelevant to the detector (BS5 form-bans the call shape regardless of
// helper body content) but pins the contract for future readers.
func arg0ComplexBS5Helper(*ast.CallExpr) {
	// body content irrelevant — BS5 fires on the call shape, not body.
	_ = 0
}

// Regression fixture for F1 (PR #1252 round-3 Review A): asserts the
// explicit `call.Args[1].(*ast.FuncLit)` anchor is robust against
// complex arg[0] subtree shapes. The old detector's implicit-position
// `FindFirstChild[ast.FuncLit](call, ...)` would walk depth=1 children
// in order (Fun, arg[0], arg[1], ...) and pick the FIRST FuncLit it
// finds. In Go source code, arg[0] cannot directly be a literal FuncLit
// (the walker's first parameter is `ast.Node`, and a literal `func() {...}`
// has function type, not `ast.Node` — type system rejects). But arg[0]
// CAN be a CallExpr that itself contains a FuncLit grandchild (a type
// conversion or immediate FuncLit invocation). The old detector still
// worked here because the grandchild FuncLit is at depth=2 not depth=1,
// outside `FindFirstChild`'s reach — but that is a coincidence of the
// depth-1 lookup, not a structural guarantee.
//
// This fixture pins the contract structurally: arg[0] is the result of
// calling an inline FuncLit (so the inline FuncLit appears in arg[0]'s
// subtree at depth=2), arg[1] is a NAMED callback (BS5). The detector
// must anchor to arg[1] explicitly and report BS5 — failing to do so
// would mean the implementation regressed back to implicit-position
// lookup.
//
// Expected: 1 main-detector hit (BS5).
func _(file *ast.File) {
	scanner.EachInSubtree[ast.CallExpr](
		// arg[0]: result of calling an inline FuncLit. The inline FuncLit
		// is a grandchild of the outer CallExpr (it is the Fun of arg[0]'s
		// CallExpr), so it lives at depth=2 of the outer CallExpr. Both
		// old (depth=1 FindFirstChild) and new (explicit Args[1]) detector
		// implementations skip this FuncLit correctly; the new one does so
		// by construction (positional anchor), the old one by accident
		// (depth=1 doesn't reach grandchildren).
		func() ast.Node { return file }(),
		// arg[1]: named callback — BS5 form. Detector MUST report this.
		arg0ComplexBS5Helper,
	)
}

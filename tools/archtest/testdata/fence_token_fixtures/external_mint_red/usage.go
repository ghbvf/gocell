// Package external_mint_red is a RED fixture for FENCE-TOKEN-MINT-FUNNEL-01.
// It references credentialfence.Mint from a non-allowlisted path in five
// distinct AST shapes. The form-complete scanner must detect EVERY one of them
// (call / var-decl value-capture / short-var value-capture / pass-through arg /
// reflect arg) — proving form-uniqueness across all reference shapes, not just
// the direct CallExpr. A CallExpr-only scanner would miss the four capture
// forms; the exact-count self-check in the archtest pins this.
//
// The reflect import is deliberately aliased to exercise the alias-immune
// resolution path (ResolvePackageRef resolves via types.Info, not the local
// identifier name).
package external_mint_red

import (
	r "reflect"

	"github.com/ghbvf/gocell/runtime/auth/credentialfence"
)

// 1. direct call.
func directCall() credentialfence.FenceToken {
	return credentialfence.Mint()
}

// 2. var-decl function-value capture (GenDecl ValueSpec — NOT an AssignStmt).
var capturedMint = credentialfence.Mint

// 3. short-var function-value capture (AssignStmt) + deferred invocation.
func assignCapture() credentialfence.FenceToken {
	fn := credentialfence.Mint
	return fn()
}

// 4. pass-through as a function argument.
func passThrough() {
	consume(credentialfence.Mint)
}

func consume(func() credentialfence.FenceToken) {}

// 5. reflect arg via aliased reflect import (alias-immunity check).
func reflectArg() r.Value {
	return r.ValueOf(credentialfence.Mint)
}

// Keep capturedMint referenced so the fixture mirrors realistic capture-then-use
// flow without tripping unused warnings under stricter loaders.
func useCaptured() credentialfence.FenceToken { return capturedMint() }

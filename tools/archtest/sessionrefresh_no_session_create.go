package archtest

// sessionrefresh_no_session_create.go — importable rule logic for
// SESSIONREFRESH-NO-SESSION-CREATE-01 (#1302 M3).
//
// Detection logic lives here (non-test) so it can be compiled by external
// Cell repositories. GoCell's own TestSessionrefreshNoSessionStoreMutation_01
// in sessionrefresh_no_session_create_test.go calls CheckSessionrefreshNoSessionCreate01
// directly — single source, no parallel rule body.
//
// receiverNamedType is also defined here because refresh_invariants.go
// (matchRefreshGuardedMethod) uses it — both files are in the same package,
// and the helper must live in a non-test .go to be visible from another
// non-test .go.
//
// Platform-symbol paths are anchored to [PlatformModulePath]; the scan SCOPE
// is the running module, supplied by the driver. See external.go.

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"
	"testing"
)

// ─── rule ID constant ─────────────────────────────────────────────────────────

const ruleSessionrefreshNoSessionCreate01 = "SESSIONREFRESH-NO-SESSION-CREATE-01"

// ─── detection data ────────────────────────────────────────────────────────────
//
// sessionrefreshPkg and sessionStorePkg are declared in
// credential_invalidate_funnel_invariants.go (same package).

// sessionStoreType ("Store") is declared in credential_invalidate_funnel_invariants.go (same package).

// bannedSessionStoreMethods is the closed set of mutating method names on
// runtime/auth/session.Store. Refresh may call Get; everything that flips
// or appends state is banned in the refresh path.
var bannedSessionStoreMethods = map[string]struct{}{
	"Create":           {},
	"Revoke":           {},
	"RevokeForSubject": {},
}

// ─── SESSIONREFRESH-NO-SESSION-CREATE-01 ─────────────────────────────────────

// CheckSessionrefreshNoSessionCreate01 runs SESSIONREFRESH-NO-SESSION-CREATE-01
// over the running module and returns its diagnostics. GoCell's own
// TestSessionrefreshNoSessionStoreMutation_01 calls it directly — single source.
func CheckSessionrefreshNoSessionCreate01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	patterns := []string{"./corecells/accesscore/slices/sessionrefresh/..."}
	return Run(t, WorkspaceTyped(TypedOpts{Tests: false}, patterns), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		if p.Pkg.Path() != sessionrefreshPkg {
			return nil
		}
		var ds []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			ds = append(ds, scanSessionrefreshFile(p, file, rel)...)
		}
		return ds
	})
}

// scanSessionrefreshFile walks file's AST for CallExpr nodes whose method
// receiver resolves to runtime/auth/session.Store and whose method name is
// in bannedSessionStoreMethods. EachInSubtree[ast.CallExpr] traverses the
// full file tree — nested function literals and closures are covered.
func scanSessionrefreshFile(
	p *Pass,
	file *ast.File,
	rel string,
) []Diagnostic {
	var ds []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if d, ok := checkSessionStoreBannedCall(p, call, rel); ok {
			ds = append(ds, d)
		}
	})
	return ds
}

// checkSessionStoreBannedCall inspects a single CallExpr and returns a
// Diagnostic when the call targets a banned method on runtime/auth/session.Store.
// Returns (Diagnostic, true) on a violation; (Diagnostic{}, false) otherwise.
func checkSessionStoreBannedCall(p *Pass, call *ast.CallExpr, rel string) (Diagnostic, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return Diagnostic{}, false
	}
	methodName := sel.Sel.Name
	if _, banned := bannedSessionStoreMethods[methodName]; !banned {
		return Diagnostic{}, false
	}
	fn, ok := ResolveMethodCall(p.TypesInfo, sel)
	if !ok {
		return Diagnostic{}, false
	}
	// Filter by owning package = runtime/auth/session and that the
	// receiver interface is named Store. Receiver inspection guards
	// against shadowing the method name on an unrelated type.
	if fn.Pkg() == nil || fn.Pkg().Path() != sessionStorePkg {
		return Diagnostic{}, false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return Diagnostic{}, false
	}
	named, ok := receiverNamedType(sig.Recv().Type())
	if !ok || named.Obj().Name() != sessionStoreType {
		return Diagnostic{}, false
	}
	line := p.Fset.Position(call.Pos()).Line
	return Diagnostic{
		Rel:  rel,
		Line: line,
		Message: fmt.Sprintf(
			"%s:%d: SESSIONREFRESH-NO-SESSION-CREATE-01: forbidden session.Store.%s call from refresh path",
			rel, line, methodName,
		),
	}, true
}

// receiverNamedType unwraps pointer / alias layers to recover the *types.Named
// the method is attached to. Method receivers on session.Store (an interface)
// are interface-named, so the *types.Named lookup is straightforward.
//
// Defined here (non-test) rather than in sessionrefresh_no_session_create_test.go
// because refresh_invariants.go (matchRefreshGuardedMethod) also uses it, and a
// non-test .go may only reference helpers in other non-test .go files.
func receiverNamedType(t types.Type) (*types.Named, bool) {
	switch v := t.(type) {
	case *types.Pointer:
		return receiverNamedType(v.Elem())
	case *types.Named:
		return v, true
	}
	return nil, false
}

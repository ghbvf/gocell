package typeseval

import (
	"go/ast"
	"go/types"
)

// ResolveEnclosingFunc returns the OUTERMOST top-level *ast.FuncDecl that
// encloses node in file, mapped to its *types.Func identity via typesInfo.Defs.
// The identity form (callers usually call fn.FullName(), which is the go/types
// canonical form — parentheses around the receiver type for both pointer and
// value methods):
//
//   - Top-level function:   "pkg/path.FuncName"             (e.g. "fixture.DoThing")
//   - Pointer method:       "(*pkg/path.Recv).MethodName"   (e.g. "(*fixture.Service).Run")
//   - Value method:         "(pkg/path.Recv).MethodName"    (e.g. "(fixture.Counter).Inc")
//   - init() function:      "pkg/path.init"                 (Go reflection form)
//
// Returns (nil, false) when node is NOT inside any top-level FuncDecl:
//
//   - Package-level var / const init expression (e.g. `var X = helper()`)
//   - Package-level var = FuncLit() invocation (e.g. `var _ = func(){...}()`)
//   - Import block / file-level CommentGroup
//   - nil typesInfo, nil file, or nil node
//
// Callers funneling callsite-level allowlists treat (nil, false) as an
// automatic violation: a setter call at package scope has no enclosing
// FuncDecl to allowlist and would otherwise slip through.
//
// FuncLit semantics (deliberate): a nested *ast.FuncLit inside a FuncDecl is
// NOT a distinct callsite — its identity is the OUTERMOST containing FuncDecl.
// Rationale: the FuncLit and the FuncDecl share the same author; allowlisting
// the outer FuncDecl implicitly trusts any FuncLit inside it. Multi-level
// FuncLit nesting collapses to the same outer FuncDecl.
//
// Implementation: iterate file.Decls for *ast.FuncDecl whose Pos ≤ node.Pos()
// < End, then resolve fd.Name to *types.Func via typesInfo.Defs. O(F) lookup
// where F = FuncDecl count per file. No binary search: F is small (tens at
// most) and the scan is dominated by typesInfo.Defs map lookup.
//
// ref: golang/tools go/types: Info.Defs[*ast.FuncDecl.Name] is the canonical
// way to obtain *types.Func for a FuncDecl. fn.FullName() returns the same
// canonical form Go reflection uses for method names.
func ResolveEnclosingFunc(typesInfo *types.Info, file *ast.File, node ast.Node) (*types.Func, bool) {
	if typesInfo == nil || file == nil || node == nil {
		return nil, false
	}
	pos := node.Pos()
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Body == nil {
			// External / forward-declared func with no body: cannot enclose a CallExpr.
			continue
		}
		if pos < fd.Pos() || pos >= fd.End() {
			continue
		}
		// Resolve fd.Name to *types.Func via Defs. Generic methods produce a
		// *types.Func with a non-nil Pkg(); shouldn't fail in practice.
		obj := typesInfo.Defs[fd.Name]
		fn, ok := obj.(*types.Func)
		if !ok || fn == nil {
			return nil, false
		}
		return fn, true
	}
	return nil, false
}

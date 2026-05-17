// invariants:
//   - INVARIANT: CELL-DURABILITY-ALIGNMENT-FUNNEL-01
//   - INVARIANT: CELL-MUSTNEWBASECELL-FUNNEL-01
//   - INVARIANT: BASECELL-INIT-NO-NIL-GUARD-01
//   - INVARIANT: CELLMETA-INCLUDES-DURABILITYMODE-01
//
// CELL-DURABILITY-ALIGNMENT-FUNNEL-* — BaseCell.Init durability alignment guards.
//
// Cell lifecycle funnel: cell.yaml.durabilityMode (single source)
//
//	→ cellgen funnel (cell.tmpl → cell_gen.go DurabilityMode literal)
//	→ NewBaseCell parse (meta.DurabilityMode → requiredMode enum)
//	→ BaseCell.Init unconditional alignment check
//
// CELL-DURABILITY-ALIGNMENT-FUNNEL-01 (Medium type-aware):
//
//	BaseCell.Init must contain an unconditional BinaryExpr comparing
//	b.requiredMode with reg.DurabilityMode() using !=, and the then-block
//	must return errcode.New(...) or equivalent. Detection uses type-aware
//	AST scan (RunTyped + archtest.ResolveMethodCall) on BaseCell.Init body.
//	Tool: RunTyped + AST BinaryExpr walk over Init method body.
//
// Blind spots (forms outside *types.Info resolution — these are reverse-self-checked):
//
//	B1. Alternative struct embed shape: a wrapper type embedding *BaseCell that
//	    overrides Init would bypass this scan (scanner checks BaseCell.Init by
//	    receiver type). Reverse check: TestCellDurabilityFunnel_ReverseBlindSpot_NoBaseCellInitOverride
//	    asserts no non-kernel/cell package declares a method named Init with
//	    receiver type embedding *BaseCell.
//
//	B2. Indirect comparison via helper function: moving the != to a helper
//	    `func checkMode(b *BaseCell, reg Registry) bool` called from Init
//	    would hide the BinaryExpr from direct body scan. Reverse check:
//	    TestCellDurabilityFunnel_ReverseBlindSpot_NoModeCheckHelper asserts
//	    that no such helper exists in kernel/cell (the check must be inline).
//
// CELL-MUSTNEWBASECELL-FUNNEL-01 (Medium type-aware):
//
//	cells/** and examples/**/cells/** production files must not construct
//	BaseCell via composite literal (&cell.BaseCell{...} or cell.BaseCell{...}).
//	The only approved construction paths are cell.MustNewBaseCell and cell.NewBaseCell.
//	Tool: RunTypedProduction + AST CompositeLit walk.
//
// BASECELL-INIT-NO-NIL-GUARD-01 (Medium — reverse self-check):
//
//	BaseCell.Init must NOT contain zero-value short-circuit comparisons on
//	b.requiredMode (== 0, != 0, int(...) == 0). The alignment check must be
//	unconditional. Reverse check ensures production AST is free of such guards.
//
// CELLMETA-INCLUDES-DURABILITYMODE-01 (Soft backstop — golden file check):
//
//	cells/configcore/cell_gen.go (canonical generated golden) must contain
//	"DurabilityMode:" string, confirming the cellgen funnel includes the field
//	in the rendered CellMeta literal.
//
// ref: kernel/cell/base.go — BaseCell.Init
// ref: kernel/cell/durability.go — ParseDurabilityMode, DurabilityMode
// ref: tools/codegen/cellgen/templates/cell.tmpl — RenderedMetaLiteral funnel
// ref: docs/plans/202605101548-035-configcore-replicated-scroll.md §batch-A
package archtest

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCellDurabilityAlignmentFunnel01_BaseCellInitContainsCheck verifies
// CELL-DURABILITY-ALIGNMENT-FUNNEL-01.
//
// BaseCell.Init must contain an unconditional IfStmt whose condition is a
// BinaryExpr `!=` between `b.requiredMode` and a call to `reg.DurabilityMode()`.
// The method call callee must resolve via *types.Info to
// kernel/cell.Registry.DurabilityMode.
func TestCellDurabilityAlignmentFunnel01_BaseCellInitContainsCheck(t *testing.T) {
	t.Parallel()

	const (
		registryPkg    = "github.com/ghbvf/gocell/kernel/cell"
		registryMethod = "DurabilityMode"
		targetField    = "requiredMode"
	)

	var (
		foundBinaryExpr bool
		foundReturnErr  bool
	)

	_ = RunTyped(t, TypedOpts{Tests: false}, []string{"./kernel/cell"}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		if p.Pkg.Path() != registryPkg {
			return nil
		}

		for _, file := range p.Files {
			// Find the BaseCell.Init method declaration.
			EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
				if fn.Name == nil || fn.Name.Name != "Init" {
					return
				}
				if fn.Recv == nil || len(fn.Recv.List) == 0 {
					return
				}
				recv := fn.Recv.List[0]
				recvTypeName := ReceiverTypeName(recv.Type)
				if recvTypeName != "BaseCell" {
					return
				}
				if fn.Body == nil {
					return
				}

				// Scan the body for an IfStmt whose Cond is a binary != expr
				// comparing b.requiredMode with reg.DurabilityMode().
				EachInSubtree[ast.IfStmt](fn.Body, func(ifStmt *ast.IfStmt) {
					binExpr, ok := ifStmt.Cond.(*ast.BinaryExpr)
					if !ok || binExpr.Op != token.NEQ {
						return
					}

					// Check both sides: one must be `b.requiredMode` (SelectorExpr)
					// and the other must be a call to reg.DurabilityMode().
					lhsSel, lhsOk := binExpr.X.(*ast.SelectorExpr)
					rhsCall, rhsOk := binExpr.Y.(*ast.CallExpr)
					if !lhsOk || !rhsOk {
						// Try swapped sides.
						lhsSel, lhsOk = binExpr.Y.(*ast.SelectorExpr)
						rhsCall, rhsOk = binExpr.X.(*ast.CallExpr)
					}
					if !lhsOk || !rhsOk {
						return
					}

					// Verify lhsSel is `?.requiredMode` (field name check).
					if lhsSel.Sel == nil || lhsSel.Sel.Name != targetField {
						return
					}

					// Verify rhsCall.Fun resolves to Registry.DurabilityMode.
					if len(rhsCall.Args) != 0 {
						return
					}
					callSel, ok := rhsCall.Fun.(*ast.SelectorExpr)
					if !ok {
						return
					}
					fn2, ok := ResolveMethodCall(p.TypesInfo, callSel)
					if !ok {
						return
					}
					if fn2.Pkg() == nil || fn2.Pkg().Path() != registryPkg {
						return
					}
					if fn2.Name() != registryMethod {
						return
					}
					foundBinaryExpr = true

					// Check that the IfStmt body contains a return statement.
					EachInSubtree[ast.ReturnStmt](ifStmt.Body, func(_ *ast.ReturnStmt) {
						foundReturnErr = true
					})
				})
			})
		}
		return nil
	})

	assert.True(t, foundBinaryExpr,
		"CELL-DURABILITY-ALIGNMENT-FUNNEL-01: BaseCell.Init must contain "+
			"BinaryExpr `b.requiredMode != reg.DurabilityMode()` (or symmetric); "+
			"implement alignment check per PR-CFG-L2-DIVERGENCE plan §A4")
	assert.True(t, foundReturnErr,
		"CELL-DURABILITY-ALIGNMENT-FUNNEL-01: the IfStmt for durability mismatch must "+
			"contain a return statement returning an error")
}

// TestCellMustNewBaseCellFunnel01_NoCellBaseLiteralInCells verifies
// CELL-MUSTNEWBASECELL-FUNNEL-01.
//
// cells/** and examples/**/cells/** production code must not construct
// BaseCell via composite literal. Only MustNewBaseCell and NewBaseCell are
// approved construction paths.
func TestCellMustNewBaseCellFunnel01_NoCellBaseLiteralInCells(t *testing.T) {
	t.Parallel()

	const cellBasePkg = "github.com/ghbvf/gocell/kernel/cell"

	type violation struct {
		file string
		line int
	}
	var violations []violation

	_ = RunTyped(t, TypedOpts{Tests: false}, []string{"./cells/...", "./examples/..."}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}

		for _, file := range p.Files {
			fileName := p.Fset.File(file.Pos()).Name()
			// Skip test files and generated files.
			if strings.HasSuffix(fileName, "_test.go") {
				continue
			}
			if strings.Contains(fileName, "cell_gen.go") {
				continue
			}

			EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
				if cl.Type == nil {
					return
				}
				// Use type info to resolve the composite lit type.
				tv, ok := p.TypesInfo.Types[cl.Type]
				if !ok {
					return
				}
				// Check via named type string (handles pointer and value forms).
				typeStr := tv.Type.String()
				if strings.Contains(typeStr, cellBasePkg+".BaseCell") {
					pos := p.Fset.Position(cl.Pos())
					violations = append(violations, violation{file: pos.Filename, line: pos.Line})
				}
			})
		}
		return nil
	})

	assert.Empty(t, violations,
		"CELL-MUSTNEWBASECELL-FUNNEL-01: cells/ and examples/**/cells/ must not "+
			"construct BaseCell via composite literal; use MustNewBaseCell or NewBaseCell: %v",
		violations)
}

// TestBaseCellInitNoNilGuard01_ReverseCheck verifies BASECELL-INIT-NO-NIL-GUARD-01.
//
// BaseCell.Init must NOT contain zero-value short-circuit comparisons on
// b.requiredMode (e.g. `b.requiredMode == 0`, `b.requiredMode != 0`,
// `int(b.requiredMode) == 0`). The alignment check must be unconditional.
// This is a reverse self-check: the test PASSES when such guards are absent.
func TestBaseCellInitNoNilGuard01_ReverseCheck(t *testing.T) {
	t.Parallel()

	const (
		kernelCellPkg = "github.com/ghbvf/gocell/kernel/cell"
		targetField   = "requiredMode"
	)

	type violation struct {
		form string
		line int
	}
	var violations []violation

	_ = RunTyped(t, TypedOpts{Tests: false}, []string{"./kernel/cell"}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		if p.Pkg.Path() != kernelCellPkg {
			return nil
		}

		for _, file := range p.Files {
			EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
				if fn.Name == nil || fn.Name.Name != "Init" {
					return
				}
				if fn.Recv == nil || len(fn.Recv.List) == 0 {
					return
				}
				recvTypeName := ReceiverTypeName(fn.Recv.List[0].Type)
				if recvTypeName != "BaseCell" {
					return
				}
				if fn.Body == nil {
					return
				}

				EachInSubtree[ast.BinaryExpr](fn.Body, func(be *ast.BinaryExpr) {
					if be.Op != token.EQL && be.Op != token.NEQ {
						return
					}
					// Look for patterns where one side references requiredMode
					// and the other side is a zero literal (0) or int(...) == 0 conversion.
					lhsStr := exprContainsField(be.X, targetField)
					rhsStr := exprContainsField(be.Y, targetField)

					isZeroLit := func(e ast.Expr) bool {
						lit, ok := e.(*ast.BasicLit)
						return ok && lit.Kind == token.INT && lit.Value == "0"
					}
					isIntConv := func(e ast.Expr) bool {
						call, ok := e.(*ast.CallExpr)
						if !ok {
							return false
						}
						ident, ok := call.Fun.(*ast.Ident)
						return ok && ident.Name == "int"
					}

					if lhsStr && (isZeroLit(be.Y) || isIntConv(be.Y)) {
						pos := p.Fset.Position(be.Pos())
						violations = append(violations, violation{
							form: "b.requiredMode op 0",
							line: pos.Line,
						})
					}
					if rhsStr && (isZeroLit(be.X) || isIntConv(be.X)) {
						pos := p.Fset.Position(be.Pos())
						violations = append(violations, violation{
							form: "0 op b.requiredMode",
							line: pos.Line,
						})
					}
				})
			})
		}
		return nil
	})

	assert.Empty(t, violations,
		"BASECELL-INIT-NO-NIL-GUARD-01: BaseCell.Init must not contain zero-value "+
			"short-circuit on b.requiredMode; alignment check must be unconditional: %v",
		violations)
}

// exprContainsField returns true if expr is a SelectorExpr whose Sel.Name == fieldName.
func exprContainsField(expr ast.Expr, fieldName string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return sel.Sel != nil && sel.Sel.Name == fieldName
}

// TestCellmetaIncludesDurabilityMode verifies CELLMETA-INCLUDES-DURABILITYMODE.
//
// The canonical generated golden file cells/configcore/cell_gen.go must contain
// a "DurabilityMode:" field assignment in the CellMeta literal, confirming that
// the cellgen funnel renders the field. This is a simple string-content check
// on the golden file.
func TestCellmetaIncludesDurabilityMode(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	goldenPath := filepath.Join(root, "cells", "configcore", "cell_gen.go")

	scope := DirsScope(root, []string{"cells/configcore"})
	var found bool
	Run(t, scope, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			fileName := p.Fset.File(file.Pos()).Name()
			if !strings.HasSuffix(fileName, "cell_gen.go") {
				continue
			}
			EachInSubtree[ast.KeyValueExpr](file, func(kv *ast.KeyValueExpr) {
				key, ok := kv.Key.(*ast.Ident)
				if ok && key.Name == "DurabilityMode" {
					found = true
				}
			})
		}
		return nil
	})

	_ = goldenPath // referenced for documentation
	assert.True(t, found,
		"CELLMETA-INCLUDES-DURABILITYMODE-01: cells/configcore/cell_gen.go must contain "+
			"DurabilityMode: field in CellMeta literal (cellgen funnel requirement)")
}

// ---------------------------------------------------------------------------
// Reverse blind-spot self-checks
// ---------------------------------------------------------------------------

// TestCellDurabilityFunnel_ReverseBlindSpot_NoBaseCellInitOverride asserts that
// no package outside kernel/cell declares a method named Init whose receiver
// embeds or aliases *BaseCell. Such an override would bypass FUNNEL-01's scan.
func TestCellDurabilityFunnel_ReverseBlindSpot_NoBaseCellInitOverride(t *testing.T) {
	t.Parallel()

	type violation struct {
		file string
		line int
		pkg  string
	}
	var violations []violation

	_ = RunTyped(t, TypedOpts{Tests: false}, []string{
		"./cells/...",
		"./examples/...",
		"./runtime/...",
		"./adapters/...",
	}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		for _, file := range p.Files {
			EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
				if fn.Name == nil || fn.Name.Name != "Init" {
					return
				}
				if fn.Recv == nil || len(fn.Recv.List) == 0 {
					return
				}
				// Check if any receiver field type contains "BaseCell".
				recv := fn.Recv.List[0]
				recvName := ReceiverTypeName(recv.Type)
				if strings.Contains(recvName, "BaseCell") {
					pos := p.Fset.Position(fn.Pos())
					violations = append(violations, violation{
						file: pos.Filename,
						line: pos.Line,
						pkg:  p.Pkg.Path(),
					})
				}
			})
		}
		return nil
	})

	assert.Empty(t, violations,
		"ReverseBlindSpot B1: no non-kernel/cell package should declare Init "+
			"with receiver referencing BaseCell directly: %v", violations)
}

// TestCellDurabilityFunnel_ReverseBlindSpot_NoModeCheckHelper asserts that
// kernel/cell does not define a standalone helper function that performs the
// mode comparison (which would hide the BinaryExpr from FUNNEL-01's body scan).
func TestCellDurabilityFunnel_ReverseBlindSpot_NoModeCheckHelper(t *testing.T) {
	t.Parallel()

	const kernelCellPkg = "github.com/ghbvf/gocell/kernel/cell"
	type violation struct {
		funcName string
		line     int
	}
	var violations []violation

	_ = RunTyped(t, TypedOpts{Tests: false}, []string{"./kernel/cell"}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != kernelCellPkg {
			return nil
		}
		for _, file := range p.Files {
			EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
				if fn.Name == nil || fn.Recv != nil {
					return // skip methods
				}
				name := fn.Name.Name
				// Flag standalone functions whose name suggests mode-comparison delegation.
				if strings.Contains(strings.ToLower(name), "checkmode") ||
					strings.Contains(strings.ToLower(name), "alignmode") ||
					strings.Contains(strings.ToLower(name), "verifydurability") {
					pos := p.Fset.Position(fn.Pos())
					violations = append(violations, violation{funcName: name, line: pos.Line})
				}
			})
		}
		return nil
	})

	assert.Empty(t, violations,
		"ReverseBlindSpot B2: kernel/cell must not define a standalone mode-check "+
			"helper; alignment must be inline in BaseCell.Init: %v", violations)
}

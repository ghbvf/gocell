package archtest

// cell_init_checknotnoop.go — importable CELL-L2-INIT-CHECKNOTNOOP-CALLED-01
// rule logic.
//
// This is the non-test home of the detector so it can be compiled and run by
// an external Cell repository through CheckCellL2InitCheckNotNoop /
// StandardCellRules (Go never compiles a dependency's _test.go, so rule logic
// that external repos must run cannot live in a _test.go file). GoCell's own
// TestCELL_L2_INIT_CHECKNOTNOOP_CALLED_01 (cell_init_checknotnoop_test.go)
// calls the same CheckCellL2InitCheckNotNoop — single source, no parallel
// rule body.
//
// Platform-symbol paths (kernel/outbox.CheckNotNoop) are anchored to
// PlatformModulePath (fixed: external repos import these packages as a GoCell
// dependency at that path). The scan SCOPE is the running module, supplied by
// Run(t, Production(...)). See external.go for the design rationale.
//
// The _test.go dogfood tests (TestCELL_L2_INIT_CHECKNOTNOOP_CALLED_01, etc.)
// use the helpers declared here; they also add phase-A test helpers, fixture
// loading, and reverse self-checks that are _test.go-only. The two files share
// the archtest package namespace during `go test`, but only this file is
// visible to external importers.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const ruleCellL2InitCheckNotNoop01 = "CELL-L2-INIT-CHECKNOTNOOP-CALLED-01"

// checkNotNoopFullName is the fully-qualified callee name produced by
// (*types.Func).FullName() for kernel/outbox.CheckNotNoop. Anchored to
// PlatformModulePath — not a bare string literal.
const checkNotNoopFullName = PlatformModulePath + "/kernel/outbox.CheckNotNoop"

// CheckCellL2InitCheckNotNoop runs CELL-L2-INIT-CHECKNOTNOOP-CALLED-01 over
// the running module and returns its diagnostics. It is the importable
// CellRule body wrapped by StandardCellRules; GoCell's
// TestCELL_L2_INIT_CHECKNOTNOOP_CALLED_01 calls it directly so the gate has a
// single source. The scan SCOPE is the running module (resolved from its go.mod
// by Run(t, Production(...))).
//
// Phase A walks cell.yaml files under cells/** to find L2+ targets; Phase B
// scans production packages for each target's Init method. See the file-level
// godoc in cell_init_checknotnoop_test.go for the full algorithm, blind-spot
// inventory, and phase descriptions.
func CheckCellL2InitCheckNotNoop(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()

	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("%s: read module path: %v", ruleCellL2InitCheckNotNoop01, err)
	}

	scope := ModuleScope(root)
	targets, missingDiags := collectCNNL2PlusTargets(t, scope, modPath)

	var diags []Diagnostic
	diags = append(diags, missingDiags...)

	if len(targets) > 0 {
		scanDiags := Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
			return scanPassForCNN(p, targets)
		})
		diags = append(diags, scanDiags...)
	}
	return diags
}

// cnnL2Target captures the minimal data Phase A collects from cell.yaml to
// drive Phase B. ("cnn" prefix = CheckNotNoop, to avoid name collision with
// the _test.go's l2TargetCell which is declared in the same package.)
type cnnL2Target struct {
	cellID       string
	goStructName string
	yamlPath     string
	pkgPath      string
}

// cnnCellYAML is the minimal YAML projection for Phase A.
type cnnCellYAML struct {
	ID               string `yaml:"id"`
	ConsistencyLevel string `yaml:"consistencyLevel"`
	GoStructName     string `yaml:"goStructName"`
}

// cnnLevelAtLeastL2 reports whether level is L2, L3, or L4.
// Mirrors consistencyLevelAtLeastL2 in the _test.go with the same logic.
func cnnLevelAtLeastL2(level string) bool {
	return level >= "L2" && level <= "L4"
}

// collectCNNL2PlusTargets walks cell.yaml files under cells/ and returns L2+
// targets for Phase B. Only cell.yaml files directly under cells/<id>/ are
// considered; examples/** and other paths are skipped.
func collectCNNL2PlusTargets(t *testing.T, scope Scope, modPath string) ([]cnnL2Target, []Diagnostic) {
	t.Helper()
	var (
		targets    []cnnL2Target
		missingGSN []Diagnostic
	)
	EachContentFile(t, scope, []string{".yaml"}, func(t *testing.T, fc ContentContext) {
		rel := filepath.ToSlash(fc.Rel)
		if filepath.Base(rel) != "cell.yaml" {
			return
		}
		if !strings.HasPrefix(rel, "cells/") {
			return
		}
		var meta cnnCellYAML
		if err := yaml.Unmarshal(fc.Bytes, &meta); err != nil {
			t.Fatalf("cell.yaml parse %s: %v", rel, err)
		}
		if !cnnLevelAtLeastL2(meta.ConsistencyLevel) {
			return
		}
		if meta.GoStructName == "" {
			missingGSN = append(missingGSN, Diagnostic{
				Rel:  rel,
				Line: 1,
				Message: "L2+ cell " + meta.ID +
					": cell.yaml is missing goStructName — K#04 codegen convention" +
					" requires L2+ cells to declare goStructName so this archtest" +
					" can locate the Init method receiver",
			})
			return
		}
		cellDir := filepath.Dir(rel)
		targets = append(targets, cnnL2Target{
			cellID:       meta.ID,
			goStructName: meta.GoStructName,
			yamlPath:     rel,
			pkgPath:      modPath + "/" + cellDir,
		})
	})
	return targets, missingGSN
}

// scanPassForCNN runs Phase B on a single Pass against the provided targets.
func scanPassForCNN(p *Pass, targets []cnnL2Target) []Diagnostic {
	if p == nil || p.Pkg == nil {
		return nil
	}
	pkgPath := p.Pkg.Path()
	var target *cnnL2Target
	for i := range targets {
		if targets[i].pkgPath == pkgPath {
			target = &targets[i]
			break
		}
	}
	if target == nil {
		return nil
	}
	initFn := cnnFindInitFuncDecl(p, target.goStructName)
	if initFn == nil {
		return []Diagnostic{{
			Rel:  target.yamlPath,
			Line: 1,
			Message: "L2+ cell " + target.cellID +
				": missing Init method on *" + target.goStructName +
				" — every L2+ cell needs an Init that calls kernel/outbox.CheckNotNoop",
		}}
	}
	if !cnnInitReaches(p, initFn) {
		initPos := p.Fset.Position(initFn.Pos())
		initSite := fmt.Sprintf("%s:%d", initPos.Filename, initPos.Line)
		return []Diagnostic{{
			Rel:  target.yamlPath,
			Line: 1,
			Message: "L2+ cell " + target.cellID +
				": Init (same-package callees of *" + target.goStructName +
				".Init) does not call kernel/outbox.CheckNotNoop;" +
				" add the call in Init or in a hand-written same-package hook" +
				" (e.g. initInternal) to guard durable-mode wiring [Init at " +
				initSite + "]",
		}}
	}
	return nil
}

// cnnFindInitFuncDecl returns the FuncDecl named "Init" whose receiver is
// *goStructName (or goStructName). Equivalent to initFuncDecl in the _test.go.
func cnnFindInitFuncDecl(p *Pass, goStructName string) *ast.FuncDecl {
	var found *ast.FuncDecl
	for _, f := range p.Files {
		EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
			if found != nil {
				return
			}
			if fd.Recv == nil || fd.Name == nil || fd.Name.Name != "Init" {
				return
			}
			if cnnReceiverTypeName(fd) == goStructName {
				found = fd
			}
		})
		if found != nil {
			return found
		}
	}
	return nil
}

// cnnReceiverTypeName extracts the base receiver type name from a FuncDecl,
// stripping a leading '*'. Equivalent to receiverTypeName in pg_repo_ambient_tx_test.go
// but declared here so the non-test file does not depend on a _test.go symbol.
func cnnReceiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// cnnFuncLitSpan records the position span of a FuncLit node.
type cnnFuncLitSpan struct {
	start, end token.Pos
}

// cnnCollectFuncLitSpans returns the spans of every FuncLit reachable in root.
func cnnCollectFuncLitSpans(root ast.Node) []cnnFuncLitSpan {
	var spans []cnnFuncLitSpan
	EachInSubtree[ast.FuncLit](root, func(fl *ast.FuncLit) {
		spans = append(spans, cnnFuncLitSpan{start: fl.Pos(), end: fl.End()})
	})
	return spans
}

// cnnPosInsideSpan reports whether pos falls strictly inside any span.
func cnnPosInsideSpan(pos token.Pos, spans []cnnFuncLitSpan) bool {
	for _, s := range spans {
		if pos > s.start && pos < s.end {
			return true
		}
	}
	return false
}

// cnnResolveCallee maps a CallExpr.Fun AST node to its *types.Func via
// TypesInfo.Uses. Returns nil for non-function callees.
func cnnResolveCallee(p *Pass, fun ast.Expr) *types.Func {
	if p == nil || p.TypesInfo == nil {
		return nil
	}
	var ident *ast.Ident
	switch e := fun.(type) {
	case *ast.Ident:
		ident = e
	case *ast.SelectorExpr:
		ident = e.Sel
	default:
		return nil
	}
	obj, ok := p.TypesInfo.Uses[ident]
	if !ok {
		return nil
	}
	fn, _ := obj.(*types.Func)
	return fn
}

// cnnBuildSameSrcMap builds a map from *types.Func → its *ast.FuncDecl for all
// functions declared in the package source files.
func cnnBuildSameSrcMap(p *Pass) map[*types.Func]*ast.FuncDecl {
	m := map[*types.Func]*ast.FuncDecl{}
	for _, f := range p.Files {
		EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
			if fd.Name == nil {
				return
			}
			obj := p.TypesInfo.Defs[fd.Name]
			fn, ok := obj.(*types.Func)
			if !ok {
				return
			}
			m[fn] = fd
		})
	}
	return m
}

// cnnScanFuncDecl searches fd.Body for a direct call to checkNotNoopFullName
// (ignoring nested FuncLit bodies) and collects any same-package callees that
// should be enqueued for BFS. Returns (found, callees).
func cnnScanFuncDecl(p *Pass, fd *ast.FuncDecl, sameSrc map[*types.Func]*ast.FuncDecl) (bool, []*ast.FuncDecl) {
	if fd.Body == nil {
		return false, nil
	}
	spans := cnnCollectFuncLitSpans(fd.Body)
	var enqueue []*ast.FuncDecl
	_, found := FindFirstInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) bool {
		if cnnPosInsideSpan(call.Pos(), spans) {
			return false
		}
		callee := cnnResolveCallee(p, call.Fun)
		if callee == nil {
			return false
		}
		if callee.FullName() == checkNotNoopFullName {
			return true
		}
		if next, ok := sameSrc[callee]; ok {
			enqueue = append(enqueue, next)
		}
		return false
	})
	return found, enqueue
}

// cnnInitReaches returns true if the init FuncDecl (or any same-package callee
// transitively reachable from it) calls checkNotNoopFullName outside any nested
// FuncLit. Equivalent to initReachesCheckNotNoop in the _test.go.
func cnnInitReaches(p *Pass, init *ast.FuncDecl) bool {
	if init == nil || p == nil || p.TypesInfo == nil {
		return false
	}
	sameSrc := cnnBuildSameSrcMap(p)
	visited := map[*ast.FuncDecl]struct{}{}
	queue := []*ast.FuncDecl{init}
	for len(queue) > 0 {
		fd := queue[0]
		queue = queue[1:]
		if _, seen := visited[fd]; seen {
			continue
		}
		visited[fd] = struct{}{}
		found, callees := cnnScanFuncDecl(p, fd, sameSrc)
		if found {
			return true
		}
		queue = append(queue, callees...)
	}
	return false
}

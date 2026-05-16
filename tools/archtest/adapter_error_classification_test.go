// INVARIANT: ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01
//
// Hard funnel double-lock for adapter transient-error classification
// (ai-collab.md §"Funnel 双向锁" + §"Hard 范本" typed-function-call form).
//
//   - Downstream Hard: the private errcode.Error.transient marker — the single
//     signal errcode.IsTransient keys on for *Error — is WRITTEN only inside
//     func WrapInfra. The field is unexported (Go type system forbids any
//     package outside pkg/errcode from setting it at all); this archtest adds
//     the in-package lock so no future New/Wrap/Assertion/Option sets it.
//   - Upstream Hard: each in-scope adapter (postgres / redis / s3) routes its
//     transient branch through errcode.WrapInfra — verified by a type-aware
//     call-resolution presence check, so dropping an adapter's classifier
//     fails CI.
//
// Tool: archtest.RunTypedProduction (040 Pass-Driver) + *types.Info call /
// field resolution. NOT registered in internal/archtestmeta.LegacyAllowlist.
//
// Declared blind spots (ai-collab.md §"工具选定后强制盲区自检"), each with a
// compensating argument and/or reverse self-check fixture:
//
//  1. Reflective field set (reflect.Value.SetBool on transient): impossible —
//     transient is unexported, reflect cannot set an unexported field of a
//     type defined in another package, and within pkg/errcode no reflect path
//     touches it. Compensation: Go type system (not archtest-bound).
//  2. Cross-package Error literal with transient set: a compile error outside
//     pkg/errcode (unexported field). Compensation: Go type system.
//  3. WrapInfra reached via a func-value indirection (var f = errcode.WrapInfra;
//     f(...)): info.Uses still resolves the selector to the *types.Func, so the
//     upstream presence check still counts it. Covered, not a gap.
//  4. "Every adapter error site calls classify…" is intentionally NOT enforced
//     (declared blind spot — see plan §"Fail-closed rationale" + ADR
//     202605161800): an unclassified adapter error carries no marker →
//     IsTransient false → consumer Requeues on the retry-then-budget-DLX path
//     (fail-closed toward not losing an event). The broad no-bare-error sweep
//     covering oidc/websocket is a separate, larger rule out of this PR's Cx3.
//
// Reverse self-check: TestAdapterErrorClassificationTransient01_FixturePattern
// loads a real build-tag-gated package whose transient-marker writes outside
// "WrapInfra" MUST be reported; bypassing requires editing real source.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// transientMarkerWriterFunc is the single function name allowed to write the
// errcode.Error.transient marker. Form-uniqueness anchor (ai-collab Hard 范本).
const transientMarkerWriterFunc = "WrapInfra"

// transientMarkerField is the unexported marker field name on errcode.Error.
const transientMarkerField = "transient"

func TestAdapterErrorClassificationTransient01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")

	errcodePkgPath := modPath + "/pkg/errcode"

	// Downstream Hard: scan pkg/errcode; every write to Error.transient must
	// be lexically inside func WrapInfra.
	downstream := RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != errcodePkgPath {
			return nil
		}
		return scanTransientMarkerWrites(p, errcodePkgPath, transientMarkerWriterFunc)
	})
	Report(t, "ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01", downstream)

	// Upstream Hard: each in-scope adapter must route through errcode.WrapInfra.
	wantAdapters := map[string]bool{
		modPath + "/adapters/postgres": false,
		modPath + "/adapters/redis":    false,
		modPath + "/adapters/s3":       false,
	}
	_ = RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		if _, tracked := wantAdapters[p.Pkg.Path()]; !tracked {
			return nil
		}
		if packageCallsWrapInfra(p, errcodePkgPath) {
			wantAdapters[p.Pkg.Path()] = true
		}
		return nil
	})
	for pkg, present := range wantAdapters {
		assert.Truef(t, present,
			"adapter %s must route its transient branch through errcode.WrapInfra "+
				"(ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01 upstream Hard)", pkg)
	}
}

// TestAdapterErrorClassificationTransient01_FixturePattern is the reverse
// self-check (ai-collab.md §"工具选定后强制盲区自检"): a real build-tag-gated
// package whose two non-WrapInfra transient-marker writes (assignment +
// composite literal) MUST be reported, while the WrapInfra writer and a
// read-only access MUST NOT. Asserts the exact count to catch both
// false-negative drift (detector goes blind) and false-positive drift
// (detector flags reads / the allowed writer).
func TestAdapterErrorClassificationTransient01_FixturePattern(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")

	fixturePkgPath := modPath + "/tools/archtest/internal/transientmarkerfixture"
	fixturePattern := "./tools/archtest/internal/transientmarkerfixture/..."

	diags := RunTyped(t, TypedOpts{Tests: false, Tags: []string{"archtest_fixture"}},
		[]string{fixturePattern},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != fixturePkgPath {
				return nil
			}
			return scanTransientMarkerWrites(p, fixturePkgPath, transientMarkerWriterFunc)
		})

	for _, d := range diags {
		t.Log(d.Message)
	}
	require.Len(t, diags, 2,
		"fixture must yield exactly 2 RED writes (badAssign assignment + "+
			"badLiteral composite literal); WrapInfra writer and readOnly read "+
			"must not be flagged")

	joined := ""
	for _, d := range diags {
		joined += d.Message + "\n"
	}
	assert.Contains(t, joined, "in badAssign (assignment)")
	assert.Contains(t, joined, "in badLiteral (composite literal)")
	assert.NotContains(t, joined, "in WrapInfra")
	assert.NotContains(t, joined, "in readOnly")
}

// scanTransientMarkerWrites reports every write to the errcodePkgPath.Error
// field named transient whose enclosing top-level func is not writerFunc.
// Write sites: AssignStmt LHS selector `.transient`, and CompositeLit
// `Error{transient: …}` keyed element. Reads (e.g. `if ec.transient` in
// IsTransient) are not write sites and are not reported.
func scanTransientMarkerWrites(p *Pass, errcodePkgPath, writerFunc string) []Diagnostic {
	if p.TypesInfo == nil {
		return nil
	}
	var ds []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		report := func(node ast.Node, form string) {
			fn := enclosingFuncName(file, node.Pos())
			if fn == writerFunc {
				return
			}
			where := fn
			if where == "" {
				where = "<package scope>"
			}
			ds = append(ds, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(node.Pos()).Line,
				Message: fmt.Sprintf(
					"errcode.Error.%s written in %s (%s); only func %s may set the "+
						"transient marker (ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01)",
					transientMarkerField, where, form, writerFunc),
			})
		}
		EachInSubtree[ast.AssignStmt](file, func(as *ast.AssignStmt) {
			// Direct-child selectors of an AssignStmt cover the Lhs write
			// targets (`e.transient = …`). Reads of the marker live in
			// IfStmt/BinaryExpr conditions (IsTransient), never as a direct
			// AssignStmt child, so they are not visited here.
			EachInChildren[ast.SelectorExpr](as, func(sel *ast.SelectorExpr) {
				if sel.Sel == nil || sel.Sel.Name != transientMarkerField {
					return
				}
				if isErrcodeTransientField(p.TypesInfo, sel.Sel, errcodePkgPath) {
					report(sel, "assignment")
				}
			})
		})
		EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
			EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != transientMarkerField {
					return
				}
				if isErrcodeTransientField(p.TypesInfo, key, errcodePkgPath) {
					report(kv, "composite literal")
				}
			})
		})
	}
	return ds
}

// isErrcodeTransientField reports whether ident resolves (via info.Uses) to a
// struct field named transient declared in package errcodePkgPath.
func isErrcodeTransientField(info *types.Info, ident *ast.Ident, errcodePkgPath string) bool {
	obj := info.Uses[ident]
	if obj == nil {
		obj = info.Defs[ident]
	}
	v, ok := obj.(*types.Var)
	if !ok || !v.IsField() || v.Name() != transientMarkerField {
		return false
	}
	return v.Pkg() != nil && v.Pkg().Path() == errcodePkgPath
}

// packageCallsWrapInfra reports whether p contains at least one call whose
// callee resolves to errcodePkgPath.WrapInfra (qualified or dot-imported).
func packageCallsWrapInfra(p *Pass, errcodePkgPath string) bool {
	if p.TypesInfo == nil {
		return false
	}
	found := false
	for _, file := range p.Files {
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			if found {
				return
			}
			pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
			if ok && pkgPath == errcodePkgPath && name == transientMarkerWriterFunc {
				found = true
			}
		})
	}
	return found
}

// enclosingFuncName returns the name of the top-level *ast.FuncDecl whose
// source range contains pos, or "" when pos is at package scope (no
// containing top-level func).
func enclosingFuncName(file *ast.File, pos token.Pos) string {
	name := ""
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if name != "" {
			return
		}
		if fd.Pos() <= pos && pos <= fd.End() {
			name = fd.Name.Name
		}
	})
	return name
}

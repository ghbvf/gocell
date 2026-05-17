// invariants:
//   - INVARIANT: HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01
//   - INVARIANT: HEALTH-REDACTED-ERROR-MSG-FUNNEL-01
//   - INVARIANT: HEALTH-VERBOSE-SCAN-COVERAGE-01
//
// HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01 — runtime/http/health.verboseDependencyEntry
//
//	struct field set is exactly {Status, DurationMs}. Adding error text to the
//	wire payload requires extending this allowlist deliberately + amending ADR
//	docs/architecture/202605171200-adr-readyz-verbose-four-channel-redaction.md
//	§3 (channel mapping) and §6 (enforcement funnel matrix).
//
// HEALTH-REDACTED-ERROR-MSG-FUNNEL-01 — Production runtime/http/health/ code may
//
//	construct redactedErrorMsg values only via newRedactedErrorMsg. Any other
//	type conversion `redactedErrorMsg(x)` outside newRedactedErrorMsg's function
//	body fails this gate. Hard funnel: combined with the type being unexported,
//	no external package can construct a redactedErrorMsg by any shape.
//
// HEALTH-VERBOSE-SCAN-COVERAGE-01 — sanity gate: asserts the archtest scope
//
//	used by the two Hard rules above enumerates the canonical files where the
//	target types live (verbose_shape.go + health.go), so a future file move
//	that relocates the types outside this scope is surfaced before silently
//	dropping the gates.
//
// Blind-spot inventory (charter §3 mandatory) for HEALTH-REDACTED-ERROR-MSG-FUNNEL-01:
//
//	(a) untyped string literal conversion via assignment to ErrorMsg field
//	    (e.g. `SlogDependencyEntry{ErrorMsg: "raw"}`) — empty literal "" is
//	    the documented nil sentinel, any non-empty literal indicates bypass.
//	    Reverse-checked by TestHealthRedactedErrorMsgFunnelLiteralReverse.
//	(b) reflect-based construction — requires reflect.Value.Convert on the
//	    redactedErrorMsg type; the type being unexported makes this impossible
//	    to reach from outside the package, and using reflect within the
//	    package would itself be the bug being investigated. No additional
//	    archtest needed — the package boundary closes the upstream side.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	ruleHealthVerboseWireShapeFrozen     = "HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01"
	ruleHealthRedactedErrorMsgFunnel     = "HEALTH-REDACTED-ERROR-MSG-FUNNEL-01"
	ruleHealthVerboseScanCoverage        = "HEALTH-VERBOSE-SCAN-COVERAGE-01"
	healthPackageRelativeRoot            = "runtime/http/health"
	healthVerboseShapeName               = "verboseDependencyEntry"
	healthRedactedErrorMsgTypeName       = "redactedErrorMsg"
	healthRedactedErrorMsgFunnelFuncName = "newRedactedErrorMsg"
	healthSlogEntryTypeName              = "SlogDependencyEntry"
)

// healthVerboseWireAllowedFields is the verbatim field set of
// runtime/http/health.verboseDependencyEntry. Adding a field requires
// extending this allowlist deliberately and amending ADR
// 202605171200-adr-readyz-verbose-four-channel-redaction.md §3 to declare
// which channel the new field belongs to.
var healthVerboseWireAllowedFields = map[string]struct{}{
	"Status":     {},
	"DurationMs": {},
}

// healthScope returns the DirsScope used by all three HEALTH-VERBOSE-* gates.
// Single source of truth: the SCAN-COVERAGE test verifies this scope resolves
// the canonical files where the target types live.
func healthScope(t *testing.T) Scope {
	t.Helper()
	return DirsScope(findModuleRoot(t), []string{healthPackageRelativeRoot})
}

// TestHealthVerboseWireShapeFrozen enforces HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01.
func TestHealthVerboseWireShapeFrozen(t *testing.T) {
	t.Parallel()

	var (
		found bool
		seen  = make(map[string]struct{})
	)
	diags := Run(t, healthScope(t), func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, f := range p.Files {
			EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
				if ts.Name == nil || ts.Name.Name != healthVerboseShapeName {
					return
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					return
				}
				found = true
				for _, field := range st.Fields.List {
					if len(field.Names) == 0 {
						ds = append(ds, Diagnostic{
							Rel:     p.Rel(f),
							Line:    p.Fset.Position(field.Type.Pos()).Line,
							Message: "<embedded field> — wire shape forbids embedded fields (channel d owns error text)",
						})
						continue
					}
					for _, name := range field.Names {
						seen[name.Name] = struct{}{}
						if _, ok := healthVerboseWireAllowedFields[name.Name]; !ok {
							ds = append(ds, Diagnostic{
								Rel:  p.Rel(f),
								Line: p.Fset.Position(name.Pos()).Line,
								Message: fmt.Sprintf("%s — field not in allowlist; the wire shape carries no error text by design "+
									"(channel d ops-diagnostics owns it). Adding a field requires updating "+
									"healthVerboseWireAllowedFields and amending ADR "+
									"docs/architecture/202605171200-adr-readyz-verbose-four-channel-redaction.md §3+§6", name.Name),
							})
						}
					}
				}
			})
		}
		return ds
	})

	Report(t, ruleHealthVerboseWireShapeFrozen, diags)

	if !found {
		t.Fatalf("%s: %s struct definition not found under %s — if the type was relocated, "+
			"update this test's hardcoded type name + relative root along with the move",
			ruleHealthVerboseWireShapeFrozen, healthVerboseShapeName, healthPackageRelativeRoot)
	}

	for k := range healthVerboseWireAllowedFields {
		if _, ok := seen[k]; !ok {
			t.Errorf("%s: required field %s missing from %s.%s — removing a field changes the wire payload; "+
				"review ADR 202605171200",
				ruleHealthVerboseWireShapeFrozen, k, healthPackageRelativeRoot, healthVerboseShapeName)
		}
	}
}

// TestHealthRedactedErrorMsgFunnel enforces HEALTH-REDACTED-ERROR-MSG-FUNNEL-01.
//
// Detection (pure AST, no go/types — scope is one directory, the type being
// unexported closes the package boundary):
//  1. Walk every non-test .go file under runtime/http/health/ recursively via
//     archtest.Run + DirsScope.
//  2. For each file, traverse top-level FuncDecls (direct children of *ast.File).
//     Inside each function body, find every *ast.CallExpr whose Fun is
//     *ast.Ident{Name: "redactedErrorMsg"}.
//  3. Assert the enclosing FuncDecl.Name is "newRedactedErrorMsg".
func TestHealthRedactedErrorMsgFunnel(t *testing.T) {
	t.Parallel()

	diags := Run(t, healthScope(t), func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, f := range p.Files {
			EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
				if fd.Body == nil {
					return
				}
				fnName := ""
				if fd.Name != nil {
					fnName = fd.Name.Name
				}
				EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
					ident, ok := call.Fun.(*ast.Ident)
					if !ok || ident.Name != healthRedactedErrorMsgTypeName {
						return
					}
					if fnName != healthRedactedErrorMsgFunnelFuncName {
						ds = append(ds, Diagnostic{
							Rel:  p.Rel(f),
							Line: p.Fset.Position(call.Pos()).Line,
							Message: fmt.Sprintf(
								"redactedErrorMsg(...) conversion inside %s; only %s may construct redactedErrorMsg values",
								fnName, healthRedactedErrorMsgFunnelFuncName,
							),
						})
					}
				})
			})
		}
		return ds
	})

	Report(t, ruleHealthRedactedErrorMsgFunnel, diags)
}

// TestHealthRedactedErrorMsgFunnelLiteralReverse is the blind-spot reverse
// self-check for HEALTH-REDACTED-ERROR-MSG-FUNNEL-01 case (a): asserts that
// no SlogDependencyEntry composite literal in runtime/http/health/ sets
// ErrorMsg to a non-empty string literal (which would bypass the funnel via
// untyped const conversion). Empty literal "" is allowed as the documented
// nil sentinel.
func TestHealthRedactedErrorMsgFunnelLiteralReverse(t *testing.T) {
	t.Parallel()

	diags := Run(t, healthScope(t), func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, f := range p.Files {
			EachInSubtree[ast.CompositeLit](f, func(cl *ast.CompositeLit) {
				ident, ok := cl.Type.(*ast.Ident)
				if !ok || ident.Name != healthSlogEntryTypeName {
					return
				}
				EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
					keyIdent, ok := kv.Key.(*ast.Ident)
					if !ok || keyIdent.Name != "ErrorMsg" {
						return
					}
					lit, ok := kv.Value.(*ast.BasicLit)
					if !ok {
						// Not a literal — typed call result (newRedactedErrorMsg etc).
						// The forward TestHealthRedactedErrorMsgFunnel covers the
						// conversion-side rule; here we only watch literal bypass.
						return
					}
					if lit.Kind == token.STRING && lit.Value != `""` {
						ds = append(ds, Diagnostic{
							Rel:  p.Rel(f),
							Line: p.Fset.Position(lit.Pos()).Line,
							Message: fmt.Sprintf(
								"(literal reverse self-check): SlogDependencyEntry.ErrorMsg literal %s — only "+
									"\"\" sentinel or newRedactedErrorMsg(...) result is allowed",
								lit.Value,
							),
						})
					}
				})
			})
		}
		return ds
	})

	Report(t, ruleHealthRedactedErrorMsgFunnel, diags)
}

// TestHealthVerboseScanCoverage enforces HEALTH-VERBOSE-SCAN-COVERAGE-01.
//
// Sanity gate: the DirsScope built by healthScope must enumerate the canonical
// files where the target types live (verbose_shape.go declares
// verboseDependencyEntry + redactedErrorMsg + SlogDependencyEntry + the funnel
// function; health.go is the typical caller). If any of these moves out of
// runtime/http/health/ silently, the Hard rules above would still pass
// vacuously — this test catches that class of regression.
func TestHealthVerboseScanCoverage(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{})
	_ = Run(t, healthScope(t), func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			seen[p.Rel(f)] = struct{}{}
		}
		return nil
	})

	required := []string{
		filepath.ToSlash(filepath.Join(healthPackageRelativeRoot, "verbose_shape.go")),
		filepath.ToSlash(filepath.Join(healthPackageRelativeRoot, "health.go")),
	}
	for _, want := range required {
		_, ok := seen[want]
		assert.True(t, ok,
			"%s: archtest DirsScope must enumerate %s; missing files would let HEALTH-VERBOSE-* gates pass vacuously",
			ruleHealthVerboseScanCoverage, want)
	}
}

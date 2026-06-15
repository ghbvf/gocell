//go:build archtest

// INVARIANT: SPAN-RECORD-ERROR-SEAL-01
//
// SPAN-RECORD-ERROR-SEAL-01 — RecordError on any oteltrace.Span within
// adapters/otel is funneled through otelSpan.RecordError, and that single
// implementation wraps its argument with pkg/redaction.RedactError before
// forwarding to the OTel SDK. Two downstream Hard assertions seal the funnel:
//
//	A (Callsite locality — downstream Hard): every RecordError call on a
//	  field receiver (SelectorExpr) inside adapters/otel must appear inside
//	  the body of otelSpan.RecordError. Any call outside that body bypasses
//	  the redaction step and writes unredacted error text to the collector.
//
//	B (Argument form — downstream Hard): the sole call to inner.RecordError
//	  inside otelSpan.RecordError's body must pass redaction.RedactError(x)
//	  where x is the formal parameter of otelSpan.RecordError. A swap like
//	  inner.RecordError(redaction.RedactError(otherVar)) satisfies a loose
//	  *ast.Ident check but fails identity binding (SPAN-SETATTR-REDACT-01
//	  A3b technique, applied here).
//
// Upstream (holder seal): otelSpan is the only struct in adapters/otel that
// holds an oteltrace.Span field (inner, unexported). This is enforced by the
// sibling SPAN-SETATTR-REDACT-01 A1 holder-uniqueness assertion
// (tools/archtest/span_setattr_redact_test.go). The inner field is
// package-private, so no package-external type can hold it — Go compiler
// Hard gate. Package-internal bypass (a new struct in the same package
// acquiring an oteltrace.Span field) is caught by A1 at archtest time
// (Medium). Both are documented under SPAN-SETATTR-REDACT-01; this file does
// not reimplement holder-seal to avoid dual maintenance.
//
// AI-robust rating — Funnel double-lock:
//   - Upstream (package-external): Hard — `inner` unexported, packages
//     outside adapters/otel cannot construct a holder; Go compiler gate.
//   - Upstream (package-internal): Medium — SPAN-SETATTR-REDACT-01 A1
//     archtest holder-uniqueness. Hard path tracked at gh #851.
//   - Downstream A: Hard — RecordError callsite ⊆ otelSpan.RecordError
//     body, verified by impl body range gate.
//   - Downstream B: Hard — argument identity binding to formal parameter,
//     closes swap-args blindspot (mirrors SPAN-SETATTR-REDACT-01 A3b).
//
// BLIND SPOTS (AST forms outside the coverage of SPAN-RECORD-ERROR-SEAL-01):
//
//   - A fires only on RecordError calls where the receiver is a
//     *ast.SelectorExpr (field access like s.inner). A call on a plain
//     *ast.Ident receiver (local variable: var s Span; s.RecordError(err))
//     is not detected by A. Reverse self-check
//     TestSpanRecordErrorSeal_NoBlindspotsInProduction asserts this shape
//     is absent from production adapters/otel code.
//   - B uses syntactic parameter-name binding. A rename of the formal
//     parameter in otelSpan.RecordError (e.g. from "err" to "e") would
//     require the call argument to also be renamed consistently for B to
//     pass. TestSpanRecordErrorSeal_B_DetectsViolation covers the
//     wrong-identifier shape.
//   - Method-value expressions on a FIELD receiver (f := s.inner.RecordError;
//     f(err)) ARE now detected by sealAMethodValueViolationsInFile (#1432):
//     RecordError SelectorExprs not in CallExpr.Fun position, outside the sink
//     body, on a field receiver are flagged. A method-value on a plain Ident
//     receiver remains the same Ident blind spot as the call form (see above).
//     TestSpanRecordErrorSeal_MethodValue_DetectsViolation is the RED proof.
//
// ref: tools/archtest/span_setattr_redact_test.go (sibling INVARIANT, upstream
// holder seal + A3b parameter-binding technique)
// ref: .claude/rules/gocell/observability.md "Span Error Redaction"
// ref: .claude/rules/gocell/ai-robust.md §"Hard 范本目录"
// ref: ADR docs/architecture/202604242030-adr-kernel-wrapper-contract-observability.md §8
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Shared helpers (also used by span_setattr_redact_test.go and
// saga_invariants_test.go — keep in sync if function signatures change).
// ---------------------------------------------------------------------------

const redactionImportPath = `"github.com/ghbvf/gocell/framework/pkg/redaction"`

// redactionLocalName returns the local identifier used in file to refer to
// the pkg/redaction package (default "redaction"; alias otherwise).
// Returns "" when the file does not import pkg/redaction at all — in that
// case any RedactError call shape is automatically not present, which
// triggers a B violation.
func redactionLocalName(file *ast.File) string {
	for _, imp := range file.Imports {
		if imp.Path == nil {
			continue
		}
		if imp.Path.Value != redactionImportPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "redaction"
	}
	return ""
}

// recordErrorSinkFile is the module-relative path to the adapter file whose
// otelSpan.RecordError body is the single sanctioned funnel for all
// oteltrace.Span.RecordError calls in adapters/otel.
const recordErrorSinkFile = "adapters/otel/span.go"

// violSealA is the diagnostic for SPAN-RECORD-ERROR-SEAL-01 A.
const violSealA = "RecordError call on field receiver outside otelSpan.RecordError body" +
	" — bypass of sink funnel (SPAN-RECORD-ERROR-SEAL-01 A)"

// violSealAMethodValue is the diagnostic for the A method-value sub-check: a
// RecordError method-value reference (f := s.inner.RecordError) on a field
// receiver outside otelSpan.RecordError body. Taking the method as a value lets
// it be invoked later with a raw error, bypassing the sink redaction.
const violSealAMethodValue = "RecordError taken as a method-value on a field receiver" +
	" outside otelSpan.RecordError body — bypass of sink funnel via deferred" +
	" invocation (SPAN-RECORD-ERROR-SEAL-01 A method-value)"

// violSealB is the diagnostic for SPAN-RECORD-ERROR-SEAL-01 B.
const violSealB = "otelSpan.RecordError body: inner.RecordError arg must be" +
	" redaction.RedactError(<formalParam>) — argument identity-bound to formal" +
	" parameter (SPAN-RECORD-ERROR-SEAL-01 B)"

// collectOtelSpanRecordErrorBodyRanges returns the body Pos/End pairs for the
// FuncDecl named "RecordError" with receiver type *otelSpan or otelSpan.
// Pairs are flat: even indices are start positions, odd are end positions.
//
// Scoped to the otelSpan receiver so that assertion A can precisely
// distinguish "inside the sanctioned implementation" vs "outside it".
func collectOtelSpanRecordErrorBodyRanges(file *ast.File) []token.Pos {
	var ranges []token.Pos
	EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Body == nil || fn.Name == nil || fn.Recv == nil {
			return
		}
		if fn.Name.Name != "RecordError" {
			return
		}
		for _, field := range fn.Recv.List {
			if receiverBaseNameMatches(field.Type, "otelSpan") {
				ranges = append(ranges, fn.Body.Lbrace, fn.Body.Rbrace)
				return
			}
		}
	})
	return ranges
}

// receiverBaseNameMatches reports whether t is *name or name.
func receiverBaseNameMatches(t ast.Expr, name string) bool {
	switch expr := t.(type) {
	case *ast.Ident:
		return expr.Name == name
	case *ast.StarExpr:
		id, ok := expr.X.(*ast.Ident)
		return ok && id.Name == name
	}
	return false
}

// sealAViolationsInFile reports RecordError calls on SelectorExpr receivers
// that appear outside the otelSpan.RecordError implementation body.
// Returns violations as a slice of (line, message) pairs encoded in
// Diagnostic. Used both by production enforcement and negative tests.
func sealAViolationsInFile(fset *token.FileSet, file *ast.File, relPath string) []Diagnostic {
	implRanges := collectOtelSpanRecordErrorBodyRanges(file)
	var ds []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "RecordError" {
			return
		}
		if posInRanges(call.Pos(), implRanges) {
			return
		}
		// Only flag calls where the receiver is a SelectorExpr (field access,
		// e.g. s.inner.RecordError). Plain Ident receivers (interface variable)
		// are the declared blind spot — see godoc above.
		if _, isSel := sel.X.(*ast.SelectorExpr); isSel {
			pos := fset.Position(call.Pos())
			ds = append(ds, Diagnostic{Rel: relPath, Line: pos.Line, Message: violSealA})
		}
	})
	return ds
}

// sealAMethodValueViolationsInFile closes the A method-value blind spot: a
// RecordError SelectorExpr on a field receiver (e.g. s.inner.RecordError) that
// is NOT the Fun of a CallExpr — i.e. taken as a method-value (f :=
// s.inner.RecordError) — outside otelSpan.RecordError's body. sealAViolationsInFile
// only scans CallExpr.Fun, so a method-value reference would slip past it and
// could later be invoked with a raw (unredacted) error. The receiver constraint
// (field selector, not plain Ident) mirrors sealAViolationsInFile; an Ident
// receiver method-value remains the same declared Ident blind spot as the call
// form.
func sealAMethodValueViolationsInFile(fset *token.FileSet, file *ast.File, relPath string) []Diagnostic {
	implRanges := collectOtelSpanRecordErrorBodyRanges(file)
	// Collect the SelectorExpr nodes that are the Fun of a CallExpr — those are
	// invocations (covered by sealAViolationsInFile), not method-values.
	calledFuns := make(map[*ast.SelectorExpr]bool)
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			calledFuns[sel] = true
		}
	})
	var ds []Diagnostic
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if sel.Sel == nil || sel.Sel.Name != "RecordError" {
			return
		}
		if calledFuns[sel] {
			return // invocation, not a method-value — covered by the call check
		}
		if posInRanges(sel.Pos(), implRanges) {
			return // inside the sanctioned sink body
		}
		if _, isSel := sel.X.(*ast.SelectorExpr); !isSel {
			return // Ident receiver — declared blind spot, same as call form
		}
		pos := fset.Position(sel.Pos())
		ds = append(ds, Diagnostic{Rel: relPath, Line: pos.Line, Message: violSealAMethodValue})
	})
	return ds
}

// sealBViolationsInFile reports whether otelSpan.RecordError's body contains
// an inner.RecordError call whose argument is not redaction.RedactError(<param>)
// where <param> is otelSpan.RecordError's formal parameter.
// Returns violations as Diagnostic slice. Used both by production enforcement
// and negative tests.
func sealBViolationsInFile(fset *token.FileSet, file *ast.File, redactionLocal, relPath string) []Diagnostic {
	fn, ok := findOtelSpanRecordErrorDecl(file)
	if !ok {
		return []Diagnostic{{Rel: relPath, Line: fset.Position(file.Package).Line, Message: violSealB}}
	}
	if redactionLocal == "" {
		return []Diagnostic{{Rel: relPath, Line: fset.Position(fn.Pos()).Line, Message: violSealB}}
	}
	paramName := recordErrorFormalParamName(fn)
	if paramName == "" {
		return []Diagnostic{{Rel: relPath, Line: fset.Position(fn.Pos()).Line, Message: violSealB}}
	}
	if fn.Body == nil {
		return []Diagnostic{{Rel: relPath, Line: fset.Position(fn.Pos()).Line, Message: violSealB}}
	}
	var ds []Diagnostic
	EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "RecordError" {
			return
		}
		if len(call.Args) != 1 {
			pos := fset.Position(call.Pos())
			ds = append(ds, Diagnostic{Rel: relPath, Line: pos.Line, Message: violSealB})
			return
		}
		if !isRedactErrorCallWithParam(call.Args[0], redactionLocal, paramName) {
			pos := fset.Position(call.Pos())
			ds = append(ds, Diagnostic{Rel: relPath, Line: pos.Line, Message: violSealB})
		}
	})
	return ds
}

// findOtelSpanRecordErrorDecl finds the FuncDecl for otelSpan.RecordError
// in file. Returns (decl, true) if found.
func findOtelSpanRecordErrorDecl(file *ast.File) (*ast.FuncDecl, bool) {
	var found *ast.FuncDecl
	EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Body == nil || fn.Name == nil || fn.Recv == nil {
			return
		}
		if fn.Name.Name != "RecordError" {
			return
		}
		for _, field := range fn.Recv.List {
			if receiverBaseNameMatches(field.Type, "otelSpan") {
				found = fn
				return
			}
		}
	})
	return found, found != nil
}

// recordErrorFormalParamName returns the name of the first named parameter
// of fn. For otelSpan.RecordError(err error), this returns "err".
func recordErrorFormalParamName(fn *ast.FuncDecl) string {
	if fn == nil || fn.Type == nil || fn.Type.Params == nil {
		return ""
	}
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			if name != nil {
				return name.Name
			}
		}
	}
	return ""
}

// isRedactErrorCallWithParam reports whether expr is
// <redactionLocal>.RedactError(<paramName>).
// Argument identifier is bound by name to paramName, closing the swap-args
// blindspot (same technique as SPAN-SETATTR-REDACT-01 A3b).
func isRedactErrorCallWithParam(expr ast.Expr, redactionLocal, paramName string) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	if !callMatches(call, redactionLocal, "RedactError") {
		return false
	}
	if len(call.Args) != 1 {
		return false
	}
	arg, ok := call.Args[0].(*ast.Ident)
	if !ok {
		return false
	}
	return arg.Name == paramName
}

// TestSpanRecordErrorSeal enforces SPAN-RECORD-ERROR-SEAL-01 (A + B) across
// adapters/otel production files.
func TestSpanRecordErrorSeal(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"adapters/otel"}, IncludeGenerated())
	diags := Run(t, AST(scope), func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))

			ds = append(ds, sealAViolationsInFile(p.Fset, file, rel)...)

			ds = append(ds, sealAMethodValueViolationsInFile(p.Fset, file, rel)...)

			if rel == recordErrorSinkFile {
				redactionLocal := redactionLocalName(file)
				ds = append(ds, sealBViolationsInFile(p.Fset, file, redactionLocal, rel)...)
			}
		}
		return ds
	})

	Report(t, "SPAN-RECORD-ERROR-SEAL-01", diags)
}

// TestSpanRecordErrorSeal_NoBlindspotsInProduction asserts the declared blind
// spots of SPAN-RECORD-ERROR-SEAL-01 do not appear in production adapters/otel
// code, per .claude/rules/gocell/ai-robust.md §"工具选定后强制盲区自检".
//
// Checked blind spot:
//  1. RecordError on plain *ast.Ident receiver (local variable, not a field
//     access): `var s Span; s.RecordError(err)`. This is the sole remaining
//     A blind spot.
//
// The method-value form on a FIELD receiver (f := s.inner.RecordError) is NO
// LONGER a blind spot (#1432): sealAMethodValueViolationsInFile flags it, since
// sealAViolationsInFile alone only scans CallExpr.Fun and would miss the deferred
// invocation. Only the Ident-receiver form (1) remains uncovered.
func TestSpanRecordErrorSeal_NoBlindspotsInProduction(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"adapters/otel"}, IncludeGenerated())
	var ds []Diagnostic
	Run(t, AST(scope), func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			implRanges := collectOtelSpanRecordErrorBodyRanges(file)
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				if posInRanges(call.Pos(), implRanges) {
					return
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil || sel.Sel.Name != "RecordError" {
					return
				}

				if _, isIdent := sel.X.(*ast.Ident); isIdent {
					pos := p.Fset.Position(call.Pos())
					ds = append(ds, Diagnostic{
						Rel:  filepath.ToSlash(p.Rel(file)),
						Line: pos.Line,
						Message: "RecordError on Ident receiver is a blind spot of" +
							" SPAN-RECORD-ERROR-SEAL-01 A — route through otelSpan.RecordError",
					})
				}
			})
		}
		return ds
	})

	Report(t, "SPAN-RECORD-ERROR-SEAL-01-BLINDSPOT", ds)
}

// TestSpanRecordErrorSeal_A_DetectsViolation is the reverse-fixture for
// assertion A. It constructs a synthetic AST where a helper function outside
// otelSpan.RecordError calls s.inner.RecordError directly, and asserts
// sealAViolationsInFile detects it.
//
// This satisfies .claude/rules/gocell/ai-robust.md §"工具选定后强制盲区自检":
// blind-spot claims must be backed by a failing negative test.
func TestSpanRecordErrorSeal_A_DetectsViolation(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		src     string
		wantVio bool
		desc    string
	}{
		"bypass_helper": {
			src: `package otel
import oteltrace "go.opentelemetry.io/otel/trace"
type otelSpan struct { inner oteltrace.Span }
func (s *otelSpan) RecordError(err error) {
	s.inner.RecordError(err)  // impl body — must NOT be flagged
}
// VIOLATION: RecordError call outside impl body, on a field receiver
func leakHelper(s *otelSpan, err error) {
	s.inner.RecordError(err)
}
`,
			wantVio: true,
			desc:    "field.RecordError call outside otelSpan body",
		},
		"impl_only": {
			src: `package otel
import oteltrace "go.opentelemetry.io/otel/trace"
type otelSpan struct { inner oteltrace.Span }
func (s *otelSpan) RecordError(err error) {
	s.inner.RecordError(err)  // impl body only — compliant
}
`,
			wantVio: false,
			desc:    "compliant: only call is inside impl body",
		},
		"ident_receiver_not_flagged": {
			// Blind spot: Ident receiver is NOT detected by A.
			// This confirms the blind spot behavior is consistent.
			src: `package otel
type Span interface{ RecordError(error) }
func leakViaInterface(s Span, err error) {
	s.RecordError(err)  // Ident receiver — blind spot, not flagged
}
`,
			wantVio: false,
			desc:    "Ident receiver is declared blind spot — not flagged by A",
		},
	}

	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "span.go", tc.src, parser.SkipObjectResolution)
			require.NoError(t, err, "fixture must parse")

			diags := sealAViolationsInFile(fset, file, "span.go")
			if tc.wantVio {
				assert.NotEmpty(t, diags,
					"A must detect violation for %q: %s; got no diags", name, tc.desc)
			} else {
				assert.Empty(t, diags,
					"A must not flag %q: %s; got %v", name, tc.desc, diags)
			}
		})
	}
}

// TestSpanRecordErrorSeal_MethodValue_DetectsViolation is the reverse-fixture for
// the A method-value sub-check (#1432): sealAMethodValueViolationsInFile must
// flag a RecordError method-value taken on a field receiver outside the sink
// body, must NOT flag a normal call (covered by sealAViolationsInFile), must NOT
// flag a method-value taken inside the sink body, and must NOT flag an
// Ident-receiver method-value (declared blind spot).
func TestSpanRecordErrorSeal_MethodValue_DetectsViolation(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		src     string
		wantVio bool
		desc    string
	}{
		"method_value_outside_body": {
			src: `package otel
import oteltrace "go.opentelemetry.io/otel/trace"
type otelSpan struct { inner oteltrace.Span }
func (s *otelSpan) RecordError(err error) {
	s.inner.RecordError(err)
}
// VIOLATION: RecordError taken as a method-value on a field receiver, outside body
func leakViaMethodValue(s *otelSpan) func(error) {
	f := s.inner.RecordError
	return f
}
`,
			wantVio: true,
			desc:    "field.RecordError method-value outside otelSpan body",
		},
		"call_not_method_value": {
			src: `package otel
import oteltrace "go.opentelemetry.io/otel/trace"
type otelSpan struct { inner oteltrace.Span }
func (s *otelSpan) RecordError(err error) {
	s.inner.RecordError(err)
}
func helper(s *otelSpan, err error) {
	s.inner.RecordError(err)  // a CALL, not a method-value — not this check's concern
}
`,
			wantVio: false,
			desc:    "plain call is covered by sealAViolationsInFile, not the method-value check",
		},
		"method_value_inside_body": {
			src: `package otel
import oteltrace "go.opentelemetry.io/otel/trace"
type otelSpan struct { inner oteltrace.Span }
func (s *otelSpan) RecordError(err error) {
	f := s.inner.RecordError  // inside sink body — compliant
	f(err)
}
`,
			wantVio: false,
			desc:    "method-value inside the sink body is sanctioned",
		},
		"ident_receiver_method_value": {
			src: `package otel
type Span interface{ RecordError(error) }
func leak(s Span) func(error) {
	return s.RecordError  // Ident receiver method-value — declared blind spot
}
`,
			wantVio: false,
			desc:    "Ident-receiver method-value is the declared blind spot",
		},
	}

	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "span.go", tc.src, parser.SkipObjectResolution)
			require.NoError(t, err, "fixture must parse")

			diags := sealAMethodValueViolationsInFile(fset, file, "span.go")
			if tc.wantVio {
				assert.NotEmpty(t, diags,
					"method-value check must detect violation for %q: %s; got no diags", name, tc.desc)
			} else {
				assert.Empty(t, diags,
					"method-value check must not flag %q: %s; got %v", name, tc.desc, diags)
			}
		})
	}
}

// TestSpanRecordErrorSeal_B_DetectsViolation is the reverse-fixture for
// assertion B. It asserts sealBViolationsInFile detects the wrong-argument
// and missing-redaction cases.
func TestSpanRecordErrorSeal_B_DetectsViolation(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		src     string
		wantVio bool
		desc    string
	}{
		"no_redact_wrap": {
			src: `package otel
import (
	oteltrace "go.opentelemetry.io/otel/trace"
	"github.com/ghbvf/gocell/framework/pkg/redaction"
)
var _ = redaction.Mask
type otelSpan struct{ inner oteltrace.Span }
func (s *otelSpan) RecordError(err error) {
	s.inner.RecordError(err)  // VIOLATION: no RedactError wrap
}
`,
			wantVio: true,
			desc:    "no RedactError wrapper — direct error pass",
		},
		"wrong_var_bound": {
			src: `package otel
import (
	oteltrace "go.opentelemetry.io/otel/trace"
	"github.com/ghbvf/gocell/framework/pkg/redaction"
)
var sentinel error
type otelSpan struct{ inner oteltrace.Span }
func (s *otelSpan) RecordError(err error) {
	s.inner.RecordError(redaction.RedactError(sentinel))  // VIOLATION: wrong var
}
`,
			wantVio: true,
			desc:    "RedactError wraps wrong variable (not formal param)",
		},
		"compliant": {
			src: `package otel
import (
	oteltrace "go.opentelemetry.io/otel/trace"
	"github.com/ghbvf/gocell/framework/pkg/redaction"
)
type otelSpan struct{ inner oteltrace.Span }
func (s *otelSpan) RecordError(err error) {
	s.inner.RecordError(redaction.RedactError(err))  // compliant
}
`,
			wantVio: false,
			desc:    "formal param wrapped with RedactError — compliant",
		},
	}

	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "span.go", tc.src, parser.SkipObjectResolution)
			require.NoError(t, err, "fixture must parse")

			redLocal := redactionLocalName(file)
			diags := sealBViolationsInFile(fset, file, redLocal, "span.go")
			if tc.wantVio {
				assert.NotEmpty(t, diags,
					"B must detect violation for %q: %s; got no diags", name, tc.desc)
			} else {
				assert.Empty(t, diags,
					"B must not flag %q: %s; got %v", name, tc.desc, diags)
			}
		})
	}
}

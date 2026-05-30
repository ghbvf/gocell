// INVARIANT: WEBHOOK-RECEIVER-PIPELINE-01
//
// WEBHOOK-RECEIVER-PIPELINE-01 — runtime/webhook receive-pipeline token
// sealed-construction + handler callsite allowlist (KERNEL-WEBHOOK-01 PR-3).
//
// # AI-robust rating
//
// Both A1 and A2 are downstream Hard (callsite allowlist + types.Info form
// uniqueness). Upstream is Medium — Go's package visibility guarantees that
// package-external code cannot construct or reference the unexported verified /
// claimed structs or the unexported handler field, but inside the package Go
// cannot compile-time forbid a new function from constructing those types.
// This Medium upstream ceiling is the permanent Go-language limit, the same
// shape as OUTBOX-ENTRY-SEALED-CONSTRUCTION-01 (tracked in gh #1282, won't-do)
// and SPAN-SETATTR-HOLDER-SEAL-01 (#851). The external axis is Hard by the
// type system (unexported struct → package-external construction is a compile
// error); the internal axis is Medium archtest. Both axes are documented here
// as the authoritative source (ai-robust.md §"落地实例与符号清单活在代码 godoc").
//
// # Sub-rules
//
//   - A1 (downstream Hard, sealed construction): every composite literal of
//     type runtime/webhook.verified must be in an enclosing function named
//     "verify"; every composite literal of type runtime/webhook.claimed must be
//     in an enclosing function named "claim". types.Info.TypeOf(cl.Type) resolves
//     the composite literal's declared type to *types.Named; obj.Pkg().Path() and
//     obj.Name() provide exact package-path + type-name identity, defeating any
//     import alias or type alias re-shape. Only production files are scanned
//     (_test.go excluded), so white-box test helpers in package webhook are not
//     incorrectly flagged.
//
//   - A2 (downstream Hard, handler callsite): every call expression of the form
//     `r.handler(...)` where the SelectorExpr resolves via types.Info.Selections
//     to a FieldVal selection of the "handler" field on *Receiver must occur in
//     an enclosing function named "invokeHandler". A new function that calls
//     r.handler directly without going through invokeHandler is flagged.
//
//   - A3 (blind-spot reverse self-check): a synthetic fixture module
//     (testdata/webhook_pipeline_violate/) defines its own verified / claimed /
//     Receiver types with identical names and produces both A1 and A2 violations.
//     TestWebhookReceiverPipeline_ReverseFixture asserts each check fires, proving
//     the scanner does not vacuously pass.
//
// # Blind spots (ai-robust mandatory reverse self-check)
//
// B1 — reflect-based construction: `reflect.New(reflect.TypeOf(verified{}))` can
// create a zero verified value at runtime without a composite literal. This
// blind spot is structural (AST scan cannot see reflect call intent); bounded
// response: reflect.New produces a zero-value token that the handler chain would
// reject at claim-stage (Claimer.Claim would fail on an empty delivery ID), so
// the security impact is limited to a nil-delivery invoke, not bypass of HMAC
// verify. No reverse self-test added because the reflect path is untypeable at
// AST level with the current tooling.
//
// B2 — handler stored in a local variable before call: `h := r.handler; h(ctx, d)`
// extracts the handler field into a local, then calls the variable. The A2 scan
// detects only direct `r.handler(...)` SelectorExpr call expressions in call
// position; a local variable capture breaks the SelectorExpr form. Bounded
// response: the local-var form requires two statements and is visually distinct;
// any future function that does this will be caught in code review, and the
// existing invokeHandler is the only caller today (locked by A2 on the
// SelectorExpr form). A3 does not exercise this form because the fixture would
// need to also detect the AssignStmt + CallExpr combo, which is a separate rule.
// No reverse self-test added for B2 because the local-variable-capture form is
// not expressible as an AST-level SelectorExpr with types.Info.Selections — the
// same Go-language ceiling that makes package-internal upstream Hard-ization
// impossible for this pattern (same precedent as gh #1282 won't-do and #851).
//
// B3 — alias type re-shape: if verified or claimed were re-exported under an
// alias in another package inside runtime/webhook (impossible today because the
// package has no subdirectories), a composite literal of the alias type would
// resolve to a different *types.Named with the alias package path and name, not
// matching the allowlist. Bounded response: runtime/webhook has no
// subdirectories; layout enforced by repository structure (LAYER-07).
//
// B4 — handler passed as function value then called: `fn := r.handler` followed
// by `fn(ctx, d)` in another package — impossible because handler is unexported;
// package-external callers cannot form the SelectorExpr `r.handler` at all
// (compile error). This vector is therefore Hard-closed by the type system even
// though B2 exists for the within-package form.
//
// ref: runtime/webhook/receiver.go (production pipeline)
// ref: tools/archtest/webhook_hmac_funnel_test.go (callsite-allowlist template)
// ref: gh #1282 (OUTBOX-ENTRY-SEALED upstream Medium permanent ceiling)
package archtest

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	runtimeWebhookPkgPath = PlatformModulePath + "/runtime/webhook"
	runtimeWebhookPattern = "./runtime/webhook/..."

	// allowedVerifiedConstructor is the only function in runtime/webhook
	// that may construct a verified{} composite literal.
	allowedVerifiedConstructor = "verify"
	// allowedClaimedConstructor is the only function in runtime/webhook
	// that may construct a claimed{} composite literal.
	allowedClaimedConstructor = "claim"
	// allowedHandlerCaller is the only function in runtime/webhook
	// that may call the r.handler field.
	allowedHandlerCaller = "invokeHandler"

	// token type names in runtime/webhook — must match receiver.go exactly.
	verifiedTypeName = "verified"
	claimedTypeName  = "claimed"

	// receiverHandlerField is the field name on *Receiver that holds the handler.
	receiverHandlerField = "handler"
	// webhookReceiverStructName is the Receiver struct name in runtime/webhook.
	webhookReceiverStructName = "Receiver"
)

// scanWebhookTokenConstruction implements A1: composite literals of type
// verified / claimed may only appear in the enclosing functions "verify" /
// "claim" respectively.
//
// Detection: EachInSubtree[ast.CompositeLit] walks every composite literal.
// For each one, info.TypeOf(cl.Type) resolves the declared type (handles bare
// Ident "verified{}" and qualified "webhook.verified{}" shapes alike). The
// resulting *types.Named is checked against (pkgPath, typeName). The enclosing
// function is resolved with ResolveEnclosingFunc; if the function name is not
// in the per-type allowlist, a diagnostic is emitted.
//
// pkgPath is the Go package import path of the package being scanned. In
// production it is runtimeWebhookPkgPath; in the A3 reverse fixture it is the
// fixture package path. This parameterisation allows the same scanner to serve
// both contexts.
func scanWebhookTokenConstruction(fset *token.FileSet, file *ast.File, rel string, info *types.Info, pkgPath string) []Diagnostic {
	var out []Diagnostic

	// allowedConstructors maps type name → the single function name that may
	// construct a composite literal of that type.
	allowedConstructors := map[string]string{
		verifiedTypeName: allowedVerifiedConstructor,
		claimedTypeName:  allowedClaimedConstructor,
	}

	EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
		if cl.Type == nil {
			// Implicit-type composite literals (e.g., array element shorthand)
			// cannot be verified/claimed because those types have no exported
			// constructors and no parent composite that could infer them.
			return
		}
		t := info.TypeOf(cl.Type)
		if t == nil {
			return
		}
		named, ok := t.(*types.Named)
		if !ok {
			return
		}
		obj := named.Obj()
		if obj == nil || obj.Pkg() == nil {
			return
		}
		if obj.Pkg().Path() != pkgPath {
			return
		}
		allowedFunc, isToken := allowedConstructors[obj.Name()]
		if !isToken {
			return
		}
		// This is a verified{} or claimed{} composite literal. Verify it is
		// inside the single allowed enclosing function.
		fn, ok := ResolveEnclosingFunc(info, file, cl)
		if !ok || fn == nil {
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: fset.Position(cl.Pos()).Line,
				Message: obj.Name() + "{} constructed outside any function in " + pkgPath +
					"; must be inside " + allowedFunc + " (WEBHOOK-RECEIVER-PIPELINE-01/A1)",
			})
			return
		}
		if fn.Name() != allowedFunc {
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: fset.Position(cl.Pos()).Line,
				Message: obj.Name() + "{} constructed in " + fn.Name() + " in " + pkgPath +
					"; only " + allowedFunc + " may construct " + obj.Name() +
					" (WEBHOOK-RECEIVER-PIPELINE-01/A1)",
			})
		}
	})
	return out
}

// scanWebhookHandlerCallsite implements A2: `r.handler(...)` (a field-value
// call on *Receiver.handler) may only appear inside invokeHandler.
//
// Detection: EachInSubtree[ast.CallExpr] walks every call expression. For each
// call whose Fun is a *ast.SelectorExpr with selector name == "handler",
// info.Selections[sel] is consulted. A FieldVal selection whose Obj().Name()
// == "handler" and whose Recv() (the receiver type of the selection) is
// *Receiver (the pointer-to-named whose obj.Pkg().Path() == pkgPath and
// obj.Name() == "Receiver") confirms this is the target field. The enclosing
// function must be "invokeHandler".
//
// pkgPath is parameterised for the same reason as scanWebhookTokenConstruction.
func scanWebhookHandlerCallsite(fset *token.FileSet, file *ast.File, rel string, info *types.Info, pkgPath string) []Diagnostic {
	var out []Diagnostic

	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		if sel.Sel.Name != receiverHandlerField {
			return
		}
		// Confirm this is a FieldVal selection of *Receiver.handler in pkgPath.
		selection, ok := info.Selections[sel]
		if !ok {
			// Not a selection (e.g., a package-level function named "handler").
			return
		}
		if selection.Kind() != types.FieldVal {
			// Method, not field — not our target.
			return
		}
		// Check that the selected field belongs to pkgPath.Receiver.
		// selection.Recv() is the type of the receiver (the base type, possibly pointer).
		recv := selection.Recv()
		// Unwrap pointer.
		if ptr, ok := recv.(*types.Pointer); ok {
			recv = ptr.Elem()
		}
		named, ok := recv.(*types.Named)
		if !ok {
			return
		}
		obj := named.Obj()
		if obj == nil || obj.Pkg() == nil {
			return
		}
		if obj.Pkg().Path() != pkgPath || obj.Name() != webhookReceiverStructName {
			return
		}
		// This is genuinely *Receiver.handler in pkgPath. Check enclosing func.
		fn, ok := ResolveEnclosingFunc(info, file, call)
		if !ok || fn == nil {
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: fset.Position(call.Pos()).Line,
				Message: "r.handler called outside any function in " + pkgPath +
					"; must be inside invokeHandler (WEBHOOK-RECEIVER-PIPELINE-01/A2)",
			})
			return
		}
		if fn.Name() != allowedHandlerCaller {
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: fset.Position(call.Pos()).Line,
				Message: "r.handler called in " + fn.Name() + " in " + pkgPath +
					"; only invokeHandler may call r.handler (WEBHOOK-RECEIVER-PIPELINE-01/A2)",
			})
		}
	})
	return out
}

// TestWebhookReceiverPipeline verifies the WEBHOOK-RECEIVER-PIPELINE-01
// invariant against the production runtime/webhook package:
//
//   - A1: verified{}/claimed{} composite literals only in verify/claim.
//   - A2: r.handler(...) calls only in invokeHandler.
//
// _test.go files are excluded so that white-box test helpers in package webhook
// are not incorrectly flagged (token types are unexported; external test package
// cannot reach them; internal _test.go helpers could legitimately construct
// them for testing purposes).
func TestWebhookReceiverPipeline(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var a1, a2 []Diagnostic

	_ = RunTyped(t, TypedOpts{Tests: false}, []string{runtimeWebhookPattern},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != runtimeWebhookPkgPath {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				a1 = append(a1, scanWebhookTokenConstruction(p.Fset, f, rel, p.TypesInfo, runtimeWebhookPkgPath)...)
				a2 = append(a2, scanWebhookHandlerCallsite(p.Fset, f, rel, p.TypesInfo, runtimeWebhookPkgPath)...)
			}
			return nil
		})

	Report(t, "WEBHOOK-RECEIVER-PIPELINE-01/A1", a1)
	Report(t, "WEBHOOK-RECEIVER-PIPELINE-01/A2", a2)
}

// TestWebhookReceiverPipeline_ReverseFixture loads the synthetic violation
// fixture and asserts A1 (token construction outside allowed functions) and A2
// (handler call outside invokeHandler) fire — guards against the rule logic
// silently regressing to a vacuous pass.
//
// The fixture (testdata/webhook_pipeline_violate/) is a standalone Go module
// that defines its own verified / claimed / Receiver types with the same names
// as the production runtime/webhook types, then violates the construction and
// callsite rules. The scanner is parameterised with the fixture package path so
// the types.Info resolution points at the fixture's named types.
func TestWebhookReceiverPipeline_ReverseFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "webhook_pipeline_violate")

	const fixturePkgPath = "fixturetest/webhook_pipeline_violate"

	var a1, a2 []Diagnostic
	_ = RunTypedDir(t, fixtureDir, TypedOpts{Tests: false}, []string{"./..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				a1 = append(a1, scanWebhookTokenConstruction(p.Fset, f, rel, p.TypesInfo, fixturePkgPath)...)
				a2 = append(a2, scanWebhookHandlerCallsite(p.Fset, f, rel, p.TypesInfo, fixturePkgPath)...)
			}
			return nil
		})

	// A1: both verified{} (forgeBadVerified) and claimed{} (forgeBadClaimed)
	// construction outside the allowed functions must fire — exactly 2 diagnostics
	// matching the two known violation sites in the fixture.
	assert.Equal(t, 2, len(a1),
		"A1 reverse fixture: expected exactly 2 diagnostics (verified{} outside verify + claimed{} outside claim)")
	// A2: r.handler called in callHandlerDirectly (outside invokeHandler) must fire —
	// exactly 1 diagnostic matching the one known violation site in the fixture.
	// Also asserts that the legitimate invokeHandler callsite does NOT produce a diagnostic.
	assert.Equal(t, 1, len(a2),
		"A2 reverse fixture: expected exactly 1 diagnostic (r.handler called outside invokeHandler)")
}

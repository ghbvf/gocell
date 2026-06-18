//go:build archtest

// enrollment_credential_mint_caller_test.go — the in-package backstop for the
// SOLE sanctioned device first-enrollment credential mint path (#2303, epic
// #2299 G4, FR-012).
//
// INVARIANT: ENROLLMENT-CREDENTIAL-MINT-CALLER-01
//
// # What this guards
//
// runtime/auth.EnrollmentCredentialIssuer.Issue (enrollment_credential.go) is the
// only sanctioned producer of a TokenIntentEnrollment credential. It is the sole
// place that calls JWTIssuer.Issue(TokenIntentEnrollment, ...) and the sole place
// that constructs a field-bearing EnrollmentIdentity — so every enrollment
// credential flows through one policy-enforcing path (principal_kind=device +
// short TTL + random jti + non-empty tenant/subject), and every verified
// EnrollmentIdentity carries proof it came through the verifier.
//
// Two arms, one allowlist (enrollment_credential.go):
//
//   - Arm ① (main, externally expressible) — every production callsite of
//     (*auth.JWTIssuer).Issue whose first argument const-evaluates to the
//     TokenIntentEnrollment value. Issue and TokenIntentEnrollment are necessarily
//     exported (the verifier needs the const), so any package holding a *JWTIssuer
//     could mint an enrollment credential directly, bypassing the wrapper's policy.
//     The RED fixture exercises this arm.
//   - Arm ② (in-package backstop) — every production EnrollmentIdentity composite
//     literal that SETS a field. EnrollmentIdentity's fields are unexported, so an
//     out-of-package literal can only be the inert zero value (never flagged, and
//     a field-setting literal there does not even compile). A field-bearing literal
//     can therefore only appear inside runtime/auth — this arm pins it to
//     enrollment_credential.go so a sibling file cannot forge a usable identity
//     bypassing newEnrollmentIdentity's fail-closed validation.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream (a consumer trusting an EnrollmentIdentity): HARD — type system.
//     A usable (field-bearing) EnrollmentIdentity cannot be constructed
//     out-of-package (unexported fields + unexported constructor).
//   - Upstream (only enrollment_credential.go mints): MEDIUM, this archtest being
//     the file-discipline backstop. Issue / TokenIntentEnrollment are exported, so
//     Go visibility cannot express "only EnrollmentCredentialIssuer may pass the
//     enrollment intent". This is the same documented Go ceiling as
//     COMMAND-ASYNC-EMIT-CALLER-01 / DEVICE-PRINCIPAL-MINT-CALLER-01; no low-cost
//     Hard path exists and no backlog issue is opened for this split.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - Arm ① matches the first argument either as a direct compile-time constant
//     (EvaluateConstString) OR as a same-package identifier whose single assignment
//     const-folds to the enrollment intent (collectEnrollmentValuedIdents — closes
//     the variable-relay bypass `intent := TokenIntentEnrollment; iss.Issue(intent, ...)`,
//     #2382 review F1). RESIDUAL (still uncaught, intentionally — keeps this a
//     lightweight typed-AST scan, not full value-flow): the intent relayed through a
//     CROSS-FUNCTION param/return, or a variable REASSIGNED after an enrollment
//     assignment. The Hard close of these is the SSA value-flow scan OR the larger
//     "make a bare Issue(TokenIntentEnrollment, ...) unexpressible via a dedicated
//     typed mint API" refactor (codex's 重构 option) — a deliberate Cx3 deferral,
//     not adopted here. Either residual still leaves the credential bounded: it must
//     pass EnrollmentCredentialVerifier (device assert + non-empty tenant/subject/jti).
//   - Arm ② is composite-literal based; an identity assembled field-by-field after
//     a zero-value construction would evade it — but only the sealed constructor
//     produces a non-empty identity any consumer honors.
//   - The anti-vacuity guard requires the allowlisted file to host BOTH a live
//     enrollment Issue call and a field-bearing EnrollmentIdentity literal, so a
//     stale allowlist entry cannot become a silent bypass slot.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
)

// wantEnrollmentIntent is the const-folded wire value of TokenIntentEnrollment.
// Sourced from the real kernel const so the scanner cannot drift from the value
// it guards.
const wantEnrollmentIntent = string(kauth.TokenIntentEnrollment)

// enrollmentCredentialMintAllowlist is the set of module-relative production files
// allowed to mint an enrollment credential (call Issue with the enrollment intent)
// or construct a field-bearing EnrollmentIdentity. See the file godoc.
var enrollmentCredentialMintAllowlist = map[string]struct{}{
	"runtime/auth/enrollment_credential.go": {}, // EnrollmentCredentialIssuer.Issue + newEnrollmentIdentity
}

// enrollmentCredentialMintFixturePkg is the build-tagged RED fixture exercised by
// the reverse self-check.
const enrollmentCredentialMintFixturePkg = "./tools/archtest/internal/enrollmentcredentialfixture"

// TestEnrollmentCredentialMintCaller01 asserts every production enrollment-intent
// Issue callsite and every field-bearing EnrollmentIdentity construction sits in
// enrollmentCredentialMintAllowlist, and that the allowlist entry is live
// (anti-vacuity reverse check for both arms).
func TestEnrollmentCredentialMintCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observedIssue := map[string]struct{}{}
	observedIdentity := map[string]struct{}{}

	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		d, oi, oid := checkEnrollmentCredentialMint(p, enrollmentCredentialMintAllowlist)
		for f := range oi {
			observedIssue[f] = struct{}{}
		}
		for f := range oid {
			observedIdentity[f] = struct{}{}
		}
		return d
	})

	// Anti-vacuity: the sole allowlisted file must host BOTH a live enrollment
	// Issue call and a field-bearing EnrollmentIdentity construction, else an arm
	// guards nothing.
	const allowed = "runtime/auth/enrollment_credential.go"
	if _, seen := observedIssue[allowed]; !seen {
		diags = append(diags, Diagnostic{
			Message: "ENROLLMENT-CREDENTIAL-MINT-CALLER-01 anti-vacuity: no live (*auth.JWTIssuer).Issue(" +
				"TokenIntentEnrollment, ...) call observed in " + allowed + " — EnrollmentCredentialIssuer.Issue " +
				"was removed/renamed or the scanner regressed; the mint funnel guards nothing.",
		})
	}
	if _, seen := observedIdentity[allowed]; !seen {
		diags = append(diags, Diagnostic{
			Message: "ENROLLMENT-CREDENTIAL-MINT-CALLER-01 anti-vacuity: no live field-bearing " +
				"auth.EnrollmentIdentity construction observed in " + allowed + " — newEnrollmentIdentity " +
				"was removed/renamed or the scanner regressed; the identity backstop guards nothing.",
		})
	}

	Report(t, "ENROLLMENT-CREDENTIAL-MINT-CALLER-01", diags)
}

// TestEnrollmentCredentialMintCaller01_ScannerCatchesViolation is the reverse
// self-check: it runs the SAME production detector against the RED fixture,
// asserting it flags EXACTLY the fixture's bare enrollment Issue call and that an
// allowlisted run suppresses it.
func TestEnrollmentCredentialMintCaller01_ScannerCatchesViolation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{enrollmentCredentialMintFixturePkg}), func(p *Pass) []Diagnostic {
		d, _, _ := checkEnrollmentCredentialMint(p, nil)
		return d
	})

	// Two RED cases: the direct enrollment Issue call + the variable-relay one
	// (intent := TokenIntentEnrollment; iss.Issue(intent, ...)). The two access
	// controls (direct + via var) are NOT flagged.
	const wantFlagged = 2
	if len(diags) != wantFlagged {
		t.Fatalf("ENROLLMENT-CREDENTIAL-MINT-CALLER-01 scanner self-check: expected the production detector to "+
			"flag exactly %d enrollment Issue callsites in the fixture (direct + variable-relay; and NOT the two "+
			"access-intent controls), got %d: %+v",
			wantFlagged, len(diags), diags)
	}
	if len(diags) > 0 {
		// Same detector, fixture file IN the allowlist → ZERO diagnostics.
		allowFixture := map[string]struct{}{diags[0].Rel: {}}
		suppressed := Run(t, Fixture(FixtureOpts{Tests: false}, []string{enrollmentCredentialMintFixturePkg}), func(p *Pass) []Diagnostic {
			d, _, _ := checkEnrollmentCredentialMint(p, allowFixture)
			return d
		})
		assert.Empty(t, suppressed,
			"ENROLLMENT-CREDENTIAL-MINT-CALLER-01 scanner self-check: allowlisting the fixture file must suppress "+
				"all diagnostics on the identical detector path")
	}
}

// checkEnrollmentCredentialMint scans one typed Pass for (a) (*auth.JWTIssuer).Issue
// callsites whose first argument const-evaluates to TokenIntentEnrollment, and
// (b) field-bearing auth.EnrollmentIdentity composite literals; it flags any not in
// allowlist. It returns the diagnostics plus the sets of module-relative files in
// which each kind of mint was observed (anti-vacuity). SINGLE detection path shared
// by the production test and the fixture self-check.
func checkEnrollmentCredentialMint(p *Pass, allowlist map[string]struct{}) (diags []Diagnostic, issueObs, identityObs map[string]struct{}) {
	issueObs = map[string]struct{}{}
	identityObs = map[string]struct{}{}
	if !p.Typed() {
		return nil, issueObs, identityObs
	}
	// Local def-use: vars/consts whose single assignment const-folds to the
	// enrollment intent, so a relayed `intent := TokenIntentEnrollment; iss.Issue(intent, ...)`
	// is caught, not just a direct const argument (#2382 review F1).
	enrollVars := collectEnrollmentValuedIdents(p)
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			if !isEnrollmentIssueCall(p, call, enrollVars) {
				return
			}
			issueObs[rel] = struct{}{}
			if _, allowed := allowlist[rel]; allowed {
				return
			}
			pos := p.Fset.Position(call.Pos())
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"ENROLLMENT-CREDENTIAL-MINT-CALLER-01: %s calls (*auth.JWTIssuer).Issue with TokenIntentEnrollment "+
						"outside the sole sanctioned enrollment-credential issuer. A device first-enrollment credential "+
						"MUST be minted by runtime/auth.EnrollmentCredentialIssuer.Issue (enrollment_credential.go), which "+
						"forces principal_kind=device + short TTL + jti + non-empty tenant/subject. Route through it; or, if "+
						"this is a genuinely new sanctioned issuer, add it to enrollmentCredentialMintAllowlist with a rationale.",
					rel),
			})
		})
		EachInSubtree[ast.CompositeLit](file, func(lit *ast.CompositeLit) {
			if !isFieldBearingEnrollmentIdentityLiteral(p.TypesInfo, lit) {
				return
			}
			identityObs[rel] = struct{}{}
			if _, allowed := allowlist[rel]; allowed {
				return
			}
			pos := p.Fset.Position(lit.Pos())
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"ENROLLMENT-CREDENTIAL-MINT-CALLER-01: %s constructs a field-bearing auth.EnrollmentIdentity outside "+
						"the sole sanctioned constructor. A usable enrollment identity MUST come from "+
						"runtime/auth.newEnrollmentIdentity (enrollment_credential.go), which fail-closes on empty "+
						"tenant/subject so a verified identity proves it came through EnrollmentCredentialVerifier.",
					rel),
			})
		})
	}
	return diags, issueObs, identityObs
}

// isEnrollmentIssueCall reports whether call is (*auth.JWTIssuer).Issue(...) whose
// first argument resolves to TokenIntentEnrollment — either as a direct
// compile-time constant OR as a same-package identifier whose single assignment
// const-folds to it (local def-use, enrollVars). The latter closes the
// variable-relay bypass (#2382 review F1); cross-function relay and reassignment
// remain the documented residual (see file godoc blind spots).
func isEnrollmentIssueCall(p *Pass, call *ast.CallExpr, enrollVars map[types.Object]struct{}) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "Issue" {
		return false
	}
	selection := p.TypesInfo.Selections[sel]
	if selection == nil {
		return false
	}
	fn, ok := selection.Obj().(*types.Func)
	if !ok || !isAuthJWTIssuerRecv(fn) {
		return false
	}
	if len(call.Args) < 1 {
		return false
	}
	if val, isConst := EvaluateConstString(p.TypesInfo, call.Args[0]); isConst {
		return val == wantEnrollmentIntent
	}
	// Variable relay: the first arg is a non-const identifier — match it against
	// the set of identifiers whose assignment const-folds to the enrollment intent.
	if id, isID := call.Args[0].(*ast.Ident); isID {
		if obj := p.TypesInfo.ObjectOf(id); obj != nil {
			_, relayed := enrollVars[obj]
			return relayed
		}
	}
	return false
}

// collectEnrollmentValuedIdents returns the set of var/const objects in the Pass
// whose single-value assignment (`x := <const>`, `x = <const>`, `var x = <const>`,
// `const x = <const>`) const-folds to the enrollment intent. It is the local
// def-use backing for isEnrollmentIssueCall's variable-relay arm. Multi-assignment
// and later reassignment are intentionally out of scope (the documented residual):
// over-approximating to "any ident ever assigned enrollment" only ever flags MORE
// Issue callsites, which is fail-closed-safe for a mint funnel.
func collectEnrollmentValuedIdents(p *Pass) map[types.Object]struct{} {
	out := map[types.Object]struct{}{}
	add := func(lhs ast.Expr, rhs ast.Expr) {
		id, isID := lhs.(*ast.Ident)
		if !isID || id.Name == "_" {
			return
		}
		if val, isConst := EvaluateConstString(p.TypesInfo, rhs); !isConst || val != wantEnrollmentIntent {
			return
		}
		if obj := p.TypesInfo.ObjectOf(id); obj != nil {
			out[obj] = struct{}{}
		}
	}
	for _, file := range p.Files {
		EachInSubtree[ast.AssignStmt](file, func(as *ast.AssignStmt) {
			if len(as.Lhs) == 1 && len(as.Rhs) == 1 {
				add(as.Lhs[0], as.Rhs[0])
			}
		})
		EachInSubtree[ast.ValueSpec](file, func(vs *ast.ValueSpec) {
			if len(vs.Names) == 1 && len(vs.Values) == 1 {
				add(vs.Names[0], vs.Values[0])
			}
		})
	}
	return out
}

// isAuthJWTIssuerRecv reports whether fn is a method whose receiver base type is
// runtime/auth.JWTIssuer (pointer or value), alias-proof.
func isAuthJWTIssuerRecv(fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	t := sig.Recv().Type()
	if ptr, isPtr := types.Unalias(t).(*types.Pointer); isPtr {
		t = ptr.Elem()
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Pkg() != nil &&
		obj.Pkg().Path() == authRuntimePkgPath && obj.Name() == "JWTIssuer"
}

// isFieldBearingEnrollmentIdentityLiteral reports whether lit is an
// auth.EnrollmentIdentity composite literal that sets at least one field. A bare
// EnrollmentIdentity{} (the inert zero, used as an error-path return) is NOT
// flagged; out-of-package code cannot set the unexported fields, so a
// field-bearing literal can only appear inside runtime/auth.
func isFieldBearingEnrollmentIdentityLiteral(info *types.Info, lit *ast.CompositeLit) bool {
	if info == nil || lit.Type == nil || len(lit.Elts) == 0 {
		return false
	}
	tv, ok := info.Types[lit.Type]
	if !ok {
		return false
	}
	named, ok := types.Unalias(tv.Type).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Pkg() != nil &&
		obj.Pkg().Path() == authRuntimePkgPath && obj.Name() == "EnrollmentIdentity"
}

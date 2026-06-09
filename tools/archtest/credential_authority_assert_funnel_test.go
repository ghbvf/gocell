package archtest

// credential_authority_assert_funnel_test.go — Hard double-prong funnel
// for the read-side user-bound credential-authority decision.
//
// INVARIANT: CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01
//
// Funnel 适用域（ADR §A11 重写后）:
//
//   user-bound credential checks only —
//     domain.(*User).CanAuthenticate() + domain.User.PasswordVersion.
//
//   session-state checks（session.{Session,ValidateView}.RevokedAt 等）由
//     **独立 archtest** SESSION-REVOKED-FIELD-ACCESS-01 接管。两条 funnel 各自
//     单语义、各自 Hard，不再在同一 Check 接口下混合（消除 apply(_ *User)
//     underscore 形态的建模错位）。详见 ADR §A11 重写 + §A12 wire-uniformity。
//
// Funnel 双向锁评级 (ai-robust.md §"Funnel 双向锁评级"):
//
//   Downstream Hard (caller allowlist):
//     credentialauthority.Assert is resolved via typeseval.ResolvePackageRef
//     to its exact *types.Func identity. Any production call from a file
//     outside the slice-prefix allowlist fails archtest in CI. Honest caveat:
//     Go does not block the call at compile time; enforcement is archtest-
//     bound. This is the highest Hard grade reachable for exported-function
//     caller restriction.
//
//   Upstream Hard (mandatory funnel):
//     1. Production code under sessionlogin/, sessionrefresh/, sessionvalidate/
//        must NOT directly call domain.(*User).CanAuthenticate or read
//        domain.User.PasswordVersion. Each resolves to a specific *types.Func
//        / *types.Var identity via *types.Info, so any direct dependency is
//        detectable.
//     2. Concrete Check struct types defined in credentialauthority/ MUST be
//        unexported, so package-external callers cannot zero-value-construct
//        a Check skipping the factory function (sealed-by-name funnel,
//        complementary to the sealed-interface ``checkOK()'' marker).
//     3. Slice-package files MUST NOT capture credentialauthority.Assert or
//        domain.(*User).CanAuthenticate as a function value (var fn = ...,
//        fn := ..., or pass as call argument). The funnel guarantee only
//        holds for direct CallExpr; function-value capture would defer the
//        actual call to an Ident site where ResolveMethodCall cannot resolve
//        the receiver.
//
// Two-prong Hard closes the loop with write-side authzmutate:
//
//   write-side (authzmutate.Mutator.Apply): DOMAIN-AUTHZ-FIELD-PRIVATE-01 +
//                                            AUTHZ-MUTATION-APPLY-FUNNEL-01
//   read-side  user-bound (credentialauthority.Assert): this file
//   read-side  session-state (RevokedAt allowlist):    SESSION-REVOKED-FIELD-ACCESS-01
//
// Together: write-side / read-side bidirectional closure + session-state
// independent funnel for "who decides whether a credential is authoritative."
//
// Scanning tools:
//   - Downstream: typeseval.ResolvePackageRef + EachInSubtree[ast.CallExpr]
//   - Upstream method:        typeseval.ResolveMethodCall + EachInSubtree[ast.CallExpr]
//   - Upstream field:         *types.Info.Selections lookup over EachInSubtree[ast.SelectorExpr]
//   - Upstream sealed-by-name: AST scan of struct type names that implement Check
//   - Upstream value-capture:  AST scan of AssignStmt + ValueSpec + CallExpr-arg
//                              with typed Ident / Selector resolution
//
// Blind-spot self-checks (ai-robust.md §"工具选定后强制盲区自检"):
//
//  1. Method-value assignment: `fn := u.CanAuthenticate; fn()`
//     The deferred fn() CallExpr's Fun is *ast.Ident, not *ast.SelectorExpr,
//     so ResolveMethodCall would miss the second call. Captured by:
//     TestCredentialAuthorityAssertFunnel_BlindSpot_MethodValueAssignment
//     (uses EachInSubtree on the file to cover chained-call shapes such as
//     fn := o.GetUser().CanAuthenticate).
//
//  2. reflect.Value.MethodByName("CanAuthenticate"): AST-invisible at the
//     bytes that actually invoke the method. Captured by:
//     TestCredentialAuthorityAssertFunnel_BlindSpot_ReflectMethodByName via the
//     shared scanReflectStringArgCalls (REFLECT-STRING-ARG-SCANNER-01).
//
//  3. reflect.Value.FieldByName("PasswordVersion"): bypasses SelectorExpr
//     resolution; field name is a string argument. Captured by:
//     TestCredentialAuthorityAssertFunnel_BlindSpot_ReflectFieldByName via the
//     shared scanReflectStringArgCalls — type-aware receiver gate +
//     EvaluateConstString cover raw-string / const / concat forms (runtime-value
//     names remain the irreducible reflect caveat).
//     (RevokedAt blind-spot is handled by SESSION-REVOKED-FIELD-ACCESS-01.)
//
//  4. unsafe.Pointer offset read of a User field: bypasses Go field
//     visibility. Captured by:
//     TestCredentialAuthorityAssertFunnel_BlindSpot_UnsafePointerRead.
//
//  5. Slice-internal helper indirection: a helper function inside the same
//     slice package that wraps CanAuthenticate / reads the protected fields.
//     The upstream prong scans the full slice directory (not just service.go),
//     so the helper's CallExpr / SelectorExpr is still resolved and flagged.
//     Captured implicitly by the upstream production scan (no separate
//     fixture needed) and documented for review traceability.
//
// Known caveats (archtest CANNOT close these; documented for review):
//   a. Cross-package helper wrappers (e.g., a new pkg/authcheck.X(user) that
//      reads CanAuthenticate internally). AST scope is the slice prefix; a
//      helper sitting outside this scope is invisible to the upstream prong.
//      Mitigation: PR review must verify any new external helper that takes
//      *domain.User is itself routed through credentialauthority.Assert.
//   b. Reading fields via an interface abstraction over *domain.User. The
//      slice currently holds *domain.User directly (no interface); if that
//      changes, the SelectorExpr resolves to the interface's *types.Func and
//      we must extend the upstream prong to interface origin lookup.
//
// RED fixtures (must self-fire ≥ 1 violation in each per-detector bucket):
//   - corecells/accesscore/internal/credentialauthority/testdata/outside_caller_red:
//     non-allowlisted caller invokes credentialauthority.Assert.
//   - corecells/accesscore/internal/credentialauthority/testdata/direct_canauth_skip_red:
//     slice file reads user.CanAuthenticate() AND user.PasswordVersion
//     directly without routing through Assert (must produce ≥ 1 violation
//     per detector category, not just ≥ 1 aggregate).
//   - corecells/accesscore/internal/credentialauthority/testdata/value_capture_red:
//     three forms of function-value capture (AssignStmt / ValueSpec /
//     CallExpr-arg) that bypass the direct-CallExpr scan.

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/tools/typesutil"
)

// ─── Dogfood test: full rule (uses ruleCredentialAuthorityAssertFunnel01) ────

// TestCredentialAuthorityAssertFunnel exercises the full
// CheckCredentialAuthorityAssertFunnel01 rule in one shot, mirroring the
// dogfood pattern used by other platform CellRules. It must produce zero
// diagnostics on GoCell production code.
func TestCredentialAuthorityAssertFunnel(t *testing.T) {
	t.Parallel()
	Report(t, ruleCredentialAuthorityAssertFunnel01,
		CheckCredentialAuthorityAssertFunnel01(t, ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))
}

// ─── Downstream prong: caller allowlist ──────────────────────────────────

// TestCredentialAuthorityAssertFunnel_DownstreamCaller_01 enforces that
// credentialauthority.Assert is called only from the three slice prefixes
// (sessionlogin/, sessionrefresh/, sessionvalidate/) or the funnel package
// itself. Any other production call site is a violation.
//
// RED fixture: testdata/outside_caller_red simulates an outside caller.
func TestCredentialAuthorityAssertFunnel_DownstreamCaller_01(t *testing.T) {
	t.Parallel()

	var violations []Diagnostic
	_ = Run(t, WorkspaceTyped(TypedOpts{}, []string{
		"./corecells/...",
		"./cmd/...",
	}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if isAssertCallerAllowlisted(rel) {
					continue
				}
				violations = append(violations, scanAssertCallSites(p, file, rel)...)
			}
			return nil
		})

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Rel != violations[j].Rel {
			return violations[i].Rel < violations[j].Rel
		}
		return violations[i].Line < violations[j].Line
	})
	for _, d := range violations {
		t.Logf("%s:%d: %s", d.Rel, d.Line, d.Message)
	}
	assert.Empty(t, violations,
		"CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 (downstream): credentialauthority.Assert "+
			"must only be called from sessionlogin/, sessionrefresh/, sessionvalidate/, "+
			"or the funnel package itself. Any other call site is a funnel breach.")

	verifyAssertCallerRedFixtureDetected(
		t,
		"./corecells/accesscore/internal/credentialauthority/testdata/outside_caller_red",
		"CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 downstream RED fixture",
	)
}

func verifyAssertCallerRedFixtureDetected(t *testing.T, pattern, label string) {
	t.Helper()
	var found int
	_ = Run(t, WorkspaceTyped(TypedOpts{}, []string{pattern}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			found += len(scanAssertCallSites(p, file, label))
		}
		return nil
	})

	assert.GreaterOrEqual(t, found, 1,
		"RED fixture self-check FAILED: %s — expected ≥ 1 violation, got 0. "+
			"Check that the fixture calls credentialauthority.Assert from a non-allowlisted file.",
		label)
}

// ─── Upstream prong: mandatory funnel ────────────────────────────────────

// TestCredentialAuthorityAssertFunnel_UpstreamMandatory_02 enforces that
// production code under the three slice prefixes does NOT directly call
// domain.(*User).CanAuthenticate or read domain.User.PasswordVersion. Such
// reads must route through credentialauthority.Assert.
// (Session-state checks — RevokedAt — are handled by the independent
// SESSION-REVOKED-FIELD-ACCESS-01 archtest.)
//
// RED fixture: testdata/direct_canauth_skip_red simulates a slice file that
// reads these directly without going through Assert. Per-detector bucket
// counting requires the fixture to produce ≥ 1 violation for EACH detector
// (CanAuthenticate + PasswordVersion), not just ≥ 1 aggregate.
func TestCredentialAuthorityAssertFunnel_UpstreamMandatory_02(t *testing.T) {
	t.Parallel()

	var violations []Diagnostic
	_ = Run(t, WorkspaceTyped(TypedOpts{}, []string{
		"./corecells/accesscore/slices/sessionlogin/...",
		"./corecells/accesscore/slices/sessionrefresh/...",
		"./corecells/accesscore/slices/sessionvalidate/...",
	}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				if !isInSliceFunnelScope(rel) {
					continue
				}
				violations = append(violations, scanDirectCanAuthCalls(p, file, rel)...)
				violations = append(violations, scanDirectFieldReads(p, file, rel)...)
			}
			return nil
		})

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Rel != violations[j].Rel {
			return violations[i].Rel < violations[j].Rel
		}
		return violations[i].Line < violations[j].Line
	})
	for _, d := range violations {
		t.Logf("%s:%d: %s", d.Rel, d.Line, d.Message)
	}
	assert.Empty(t, violations,
		"CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 (upstream): slice production "+
			"code (sessionlogin/sessionrefresh/sessionvalidate) must not directly "+
			"call user.CanAuthenticate() or read user.PasswordVersion outside "+
			"Assert. Route through credentialauthority.Assert with the appropriate "+
			"Check. (RevokedAt is governed by SESSION-REVOKED-FIELD-ACCESS-01.)")

	verifyDirectReadRedFixtureDetectedPerBucket(
		t,
		"./corecells/accesscore/internal/credentialauthority/testdata/direct_canauth_skip_red",
		"CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 upstream RED fixture",
	)
}

// verifyDirectReadRedFixtureDetectedPerBucket asserts that the fixture
// produces ≥ 1 violation in EACH detector category (CanAuthenticate +
// PasswordVersion). An aggregate ≥ 1 count would let a fixture cover only
// one detector and silently pass — exactly the regression that motivated
// P2-A bucket counting (review finding: "RED fixture only asserts >=1,
// cannot prove every detector is live").
func verifyDirectReadRedFixtureDetectedPerBucket(t *testing.T, pattern, label string) {
	t.Helper()
	var canAuthHits, passwordVerHits int
	_ = Run(t, WorkspaceTyped(TypedOpts{}, []string{pattern}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			canAuthHits += len(scanDirectCanAuthCalls(p, file, label))
			passwordVerHits += len(scanDirectFieldReads(p, file, label))
		}
		return nil
	})

	assert.GreaterOrEqual(t, canAuthHits, 1,
		"RED fixture self-check FAILED (CanAuthenticate bucket): %s — "+
			"expected ≥ 1 violation in CanAuthenticate detector, got 0. "+
			"Add a direct user.CanAuthenticate() call to the fixture.",
		label)
	assert.GreaterOrEqual(t, passwordVerHits, 1,
		"RED fixture self-check FAILED (PasswordVersion bucket): %s — "+
			"expected ≥ 1 violation in PasswordVersion detector, got 0. "+
			"Add a direct user.PasswordVersion read to the fixture.",
		label)
}

// ─── Blind-spot self-check tests ─────────────────────────────────────────

// TestCredentialAuthorityAssertFunnel_BlindSpot_MethodValueAssignment asserts
// that the method-value-assignment pattern (e.g.
// `fn := user.CanAuthenticate; fn()`) does NOT appear in non-allowlisted
// production code. If it did, the upstream prong would miss the deferred
// fn() CallExpr because Fun would be *ast.Ident, not *ast.SelectorExpr.
//
// Detection is typed (ResolveMethodCall + isFunnelMethod), not a name anchor:
// only a selector that resolves to domain.(*User).CanAuthenticate is flagged,
// so an unrelated type exposing a CanAuthenticate method does not false-positive
// (Soft→Medium, issue #948 (b)).
func TestCredentialAuthorityAssertFunnel_BlindSpot_MethodValueAssignment(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = Run(t, WorkspaceTyped(TypedOpts{}, []string{
		"./corecells/accesscore/...",
		"./cmd/...",
	}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.AssignStmt](file, func(assign *ast.AssignStmt) {
					EachInSubtree[ast.SelectorExpr](assign, func(sel *ast.SelectorExpr) {
						fn, ok := ResolveMethodCall(p.TypesInfo, sel)
						if !ok || !isFunnelMethod(fn) {
							return
						}
						line := p.Fset.Position(assign.Pos()).Line
						violations = append(violations, fmt.Sprintf(
							"%s:%d: method-value assignment of CanAuthenticate "+
								"blind spot detected — archtest would miss the deferred call",
							rel, line,
						))
					})
				})
			}
			return nil
		})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"credentialauthority blind-spot: method-value assignment of CanAuthenticate "+
			"found in production code — the archtest would miss the deferred fn() call.")
}

// TestCredentialAuthorityAssertFunnel_BlindSpot_ReflectMethodByName asserts
// that reflect.Value.MethodByName("CanAuthenticate") does NOT appear in
// production code, confirming the reflect blind spot is not exercised.
func TestCredentialAuthorityAssertFunnel_BlindSpot_ReflectMethodByName(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = Run(t, WorkspaceTyped(TypedOpts{}, []string{
		"./corecells/accesscore/...",
		"./cmd/...",
	}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				for _, hit := range scanReflectStringArgCalls(p, file, reflectMethodByName,
					func(n string) bool { return n == credCanAuthenticate }) {
					violations = append(violations, fmt.Sprintf(
						"%s:%d: CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01: reflect.MethodByName(%q) blind "+
							"spot detected — archtest cannot see reflect-based invocations",
						rel, hit.Line, hit.Name,
					))
				}
			}
			return nil
		})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"credentialauthority blind-spot: reflect.MethodByName of CanAuthenticate "+
			"found in production code — the archtest cannot see reflect-based invocations.")
}

// TestCredentialAuthorityAssertFunnel_BlindSpot_ReflectFieldByName asserts
// that reflect.Value.FieldByName with PasswordVersion does NOT appear in
// production code, confirming the reflect field-read blind spot is not
// exercised. (RevokedAt is covered by SESSION-REVOKED-FIELD-ACCESS-01's
// own reflect blind-spot test.)
func TestCredentialAuthorityAssertFunnel_BlindSpot_ReflectFieldByName(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = Run(t, WorkspaceTyped(TypedOpts{}, []string{
		"./corecells/accesscore/...",
		"./cmd/...",
	}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				for _, hit := range scanReflectStringArgCalls(p, file, reflectFieldByName,
					func(n string) bool { return n == credPasswordVersion }) {
					violations = append(violations, fmt.Sprintf(
						"%s:%d: CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01: reflect.FieldByName(%q) blind "+
							"spot detected — archtest cannot see reflect-based field reads",
						rel, hit.Line, hit.Name,
					))
				}
			}
			return nil
		})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"credentialauthority blind-spot: reflect.FieldByName of PasswordVersion "+
			"found in production code — the archtest cannot see reflect field reads.")
}

// TestCredentialAuthorityAssertFunnel_BlindSpot_UnsafePointerRead asserts
// that no slice file imports "unsafe", which would let unsafe.Pointer offset
// reads bypass field visibility entirely. Scoped to corecells/accesscore/... and
// cmd/... — adapters/postgres legitimately uses unsafe for pgx and is out of
// scope here.
func TestCredentialAuthorityAssertFunnel_BlindSpot_UnsafePointerRead(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = Run(t, WorkspaceTyped(TypedOpts{}, []string{
		"./corecells/accesscore/...",
		"./cmd/...",
	}),
		func(p *Pass) []Diagnostic {
			if p.Fset == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				for _, imp := range file.Imports {
					if imp.Path == nil {
						continue
					}
					impPath := strings.Trim(imp.Path.Value, `"`)
					if impPath == "unsafe" {
						line := p.Fset.Position(imp.Pos()).Line
						violations = append(violations, fmt.Sprintf(
							"%s:%d: imports \"unsafe\" — potential offset read of "+
								"domain.User / session.{Session,ValidateView} could bypass "+
								"credentialauthority funnel",
							rel, line,
						))
					}
				}
			}
			return nil
		})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"credentialauthority blind-spot: unsafe import found in corecells/accesscore "+
			"or cmd/ — verify no unsafe.Pointer reads target funnel-protected fields.")
}

// ─── Upstream prong: concrete Check sealed-by-name (P1-B) ────────────────

// TestCredentialAuthorityAssertFunnel_UpstreamSealed_03 asserts that every
// concrete struct type defined inside the credentialauthority package that
// implements Check (i.e., has a `checkOK()` method) is **unexported** (name
// starts with a lowercase letter).
//
// Why this matters: Check is a sealed interface (the unexported `checkOK()`
// marker prevents external packages from declaring new variants), but
// nothing in the type system prevents an external caller from zero-value
// constructing an exported concrete Check struct (e.g.
// `credentialauthority.WithPasswordVersionPin{}`) and passing it to Assert
// — bypassing the factory function's intended initialization. Forcing
// concrete types to be unexported closes that bypass at the package
// boundary: external callers can only obtain a Check through the factory
// (SnapshotPasswordVersion, etc.), which controls field initialization.
//
// Hard rating: type identity is resolved through *types.Info; exported-name
// detection uses ast.IsExported (token.IsExported → unicode.IsUpper on the
// first rune), identical to Go's own export rule — so a Unicode-uppercase
// exported name (which the prior ASCII-only check missed) is still flagged.
// Picking any exported name shape is a CI failure.
func TestCredentialAuthorityAssertFunnel_UpstreamSealed_03(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = Run(t, WorkspaceTyped(TypedOpts{}, []string{
		"./corecells/accesscore/internal/credentialauthority/...",
	}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}

			if p.Pkg.Path() != credAuthorityPkgPath {
				return nil
			}

			checkObj := p.Pkg.Scope().Lookup("Check")
			if checkObj == nil {
				return nil
			}
			checkIface, ok := checkObj.Type().Underlying().(*types.Interface)
			if !ok {
				return nil
			}

			scope := p.Pkg.Scope()
			for _, name := range scope.Names() {
				obj := scope.Lookup(name)
				tn, ok := obj.(*types.TypeName)
				if !ok {
					continue
				}
				named, ok := tn.Type().(*types.Named)
				if !ok {
					continue
				}
				if _, ok := named.Underlying().(*types.Struct); !ok {
					continue
				}

				if !typesutil.ImplementsInterface(named, checkIface) {
					continue
				}
				if !ast.IsExported(name) {
					continue
				}
				pos := p.Fset.Position(tn.Pos())
				violations = append(violations, fmt.Sprintf(
					"%s:%d: CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 (sealed-by-name): "+
						"concrete Check struct %q is exported — external callers can "+
						"zero-value construct it and bypass the factory function. "+
						"Rename to lowercase and expose only the factory.",
					stripModuleRoot(pos.Filename), pos.Line, name,
				))
			}
			return nil
		})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 (sealed-by-name): every "+
			"concrete struct in credentialauthority/ that implements Check "+
			"must be unexported, so package-external callers can only obtain "+
			"a Check through the factory function.")
}

// ─── Upstream prong: typed callee reference (P2-B Hard) ─────────────────

// TestCredentialAuthorityAssertFunnel_UpstreamCalleeReference_04 enforces
// that funnel-protected callees (credentialauthority.Assert and
// domain.(*User).CanAuthenticate) are NEVER referenced at any position
// other than the direct CallExpr.Fun slot. Function-value capture in any
// expression context (AssignStmt RHS / ValueSpec / CallExpr argument /
// ReturnStmt / SendStmt / IndexExpr / CompositeLit element / ...) defeats
// the downstream caller-allowlist funnel: a saved function value can be
// passed to a callee outside the allowlist and invoked via an Ident, where
// caller identity is no longer recoverable.
//
// Hard rating (ai-robust.md §"Hard 范本：typed function call as Hard funnel
// for unbounded operations"):
//   - Form uniqueness: any SelectorExpr typed-resolved to the funnel
//     callee that is NOT at CallExpr.Fun position is reported. No
//     syntactic-context enumeration — every Go expression position is
//     covered by the single typed-parent check, so adding new syntactic
//     contexts in future Go versions cannot reopen the bypass.
//   - Full scope: cells/ + cmd/ + runtime/. Identical to the downstream
//     caller allowlist scope, so the two prongs cover the same surface
//     from complementary directions (Upstream_01 sees direct calls,
//     Upstream_04 sees every other reference).
//
// Why a single typed-parent check replaces the previous three
// syntactic-context scans (AssignStmt/ValueSpec/CallArg) AND the missing
// ReturnStmt/CompositeLit/SendStmt cases: Go's first-class function
// values can appear in any expression slot. Enumerating syntactic
// contexts can never close the form-uniqueness gap. The CallExpr.Fun
// positions are pre-collected into a set; every SelectorExpr resolving
// to a funnel callee that is NOT in that set is a violation, regardless
// of syntactic context.
//
// RED fixture: testdata/value_capture_red (multiple forms; ≥ 1 total).
func TestCredentialAuthorityAssertFunnel_UpstreamCalleeReference_04(t *testing.T) {
	t.Parallel()

	var violations []Diagnostic
	_ = Run(t, WorkspaceTyped(TypedOpts{}, []string{
		"./corecells/...",
		"./cmd/...",
		"./runtime/...",
	}),
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				violations = append(violations, scanFunnelCalleeReferences(p, file, rel)...)
			}
			return nil
		})

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Rel != violations[j].Rel {
			return violations[i].Rel < violations[j].Rel
		}
		return violations[i].Line < violations[j].Line
	})
	for _, d := range violations {
		t.Logf("%s:%d: %s", d.Rel, d.Line, d.Message)
	}
	assert.Empty(t, violations,
		"CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 (typed callee reference): "+
			"production code referenced credentialauthority.Assert or "+
			"domain.(*User).CanAuthenticate at a non-direct-call position "+
			"(function-value capture), defeating the caller-allowlist funnel. "+
			"Call the function directly at the use site; do not save it as a "+
			"value or pass it as an argument.")

	verifyFunnelCalleeReferenceRedFixtureDetected(
		t,
		"./corecells/accesscore/internal/credentialauthority/testdata/value_capture_red",
		"CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 callee-reference RED fixture",
	)
}

// verifyFunnelCalleeReferenceRedFixtureDetected asserts the RED fixture fires
// ≥ 1 violation in EACH callee bucket. The two callees travel DIFFERENT typed
// resolution paths — credentialauthority.Assert via *types.Info.Uses, and
// domain.(*User).CanAuthenticate via *types.Info.Selections — so an aggregate
// ≥ 1 count could silently pass while one resolution path is dead. Per-callee
// counting proves both paths live (mirrors the Upstream_02 per-bucket rationale;
// issue #948 (a)).
func verifyFunnelCalleeReferenceRedFixtureDetected(t *testing.T, pattern, label string) {
	t.Helper()
	byCallee := map[string]int{}
	_ = Run(t, WorkspaceTyped(TypedOpts{}, []string{pattern}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			for _, hit := range collectFunnelCalleeReferenceHits(p, file) {
				byCallee[hit.Callee]++
			}
		}
		return nil
	})

	assert.GreaterOrEqual(t, byCallee[funnelCalleeAssert], 1,
		"RED fixture self-check FAILED (Assert via info.Uses): %s — expected ≥ 1 "+
			"value-capture of credentialauthority.Assert, got 0.", label)
	assert.GreaterOrEqual(t, byCallee[funnelCalleeCanAuth], 1,
		"RED fixture self-check FAILED (CanAuthenticate via info.Selections): %s — "+
			"expected ≥ 1 value-capture of domain.(*User).CanAuthenticate, got 0.", label)
}

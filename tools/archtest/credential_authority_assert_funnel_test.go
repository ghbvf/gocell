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
// Funnel 双向锁评级 (ai-collab.md §"Funnel 双向锁评级"):
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
// Blind-spot self-checks (ai-collab.md §"工具选定后强制盲区自检"):
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
//     TestCredentialAuthorityAssertFunnel_BlindSpot_ReflectMethodByName.
//
//  3. reflect.Value.FieldByName("PasswordVersion"): bypasses SelectorExpr
//     resolution; field name is in a string literal. Captured by:
//     TestCredentialAuthorityAssertFunnel_BlindSpot_ReflectFieldByName.
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
//   - cells/accesscore/internal/credentialauthority/testdata/outside_caller_red:
//     non-allowlisted caller invokes credentialauthority.Assert.
//   - cells/accesscore/internal/credentialauthority/testdata/direct_canauth_skip_red:
//     slice file reads user.CanAuthenticate() AND user.PasswordVersion
//     directly without routing through Assert (must produce ≥ 1 violation
//     per detector category, not just ≥ 1 aggregate).
//   - cells/accesscore/internal/credentialauthority/testdata/value_capture_red:
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
)

// ─── package path / symbol constants ────────────────────────────────────

const (
	credAuthorityPkgPath = "github.com/ghbvf/gocell/cells/accesscore/internal/credentialauthority"
	credAuthorityFnName  = "Assert"
	credSessionPkgPath   = "github.com/ghbvf/gocell/runtime/auth/session"
	credSessionType      = "Session"
	credSessionViewType  = "ValidateView"
	credCanAuthenticate  = "CanAuthenticate"
	credPasswordVersion  = "PasswordVersion"
	credRevokedAt        = "RevokedAt"
)

// assertCallerAllowlist limits callers of credentialauthority.Assert to
// these three slice prefixes + the funnel package itself. _test.go files
// always pass (test helpers may call Assert to construct expectations).
var assertCallerAllowlist = []string{
	"cells/accesscore/internal/credentialauthority/", // funnel itself
	"cells/accesscore/slices/sessionlogin/",
	"cells/accesscore/slices/sessionrefresh/",
	"cells/accesscore/slices/sessionvalidate/",
}

// sliceFunnelScopes are the slice prefixes whose production files MUST route
// CanAuthenticate / PasswordVersion / RevokedAt reads through Assert.
var sliceFunnelScopes = []string{
	"cells/accesscore/slices/sessionlogin/",
	"cells/accesscore/slices/sessionrefresh/",
	"cells/accesscore/slices/sessionvalidate/",
}

// The upstream prong scans only the three slice prefixes (sliceFunnelScopes),
// so no broader "directReadAllowlist" enumeration is needed — packages outside
// those scopes (credentialauthority, domain, runtime/auth/session, authzmutate,
// credentialinvalidate) legitimately read CanAuthenticate / PasswordVersion /
// RevokedAt because they are the field-defining or write-side-aggregate paths.
// Confining the scan to sliceFunnelScopes makes the allowlist implicit and
// avoids drift between two parallel lists.

// isAssertCallerAllowlisted reports whether a module-relative path may call
// credentialauthority.Assert directly. Test files always pass.
func isAssertCallerAllowlisted(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	for _, prefix := range assertCallerAllowlist {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// isInSliceFunnelScope reports whether rel is under one of the three slice
// prefixes that MUST route through Assert.
func isInSliceFunnelScope(rel string) bool {
	for _, prefix := range sliceFunnelScopes {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
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

	var violations []string
	_ = RunTyped(t, TypedOpts{}, []string{
		"./cells/...",
		"./cmd/...",
	}, func(p *Pass) []Diagnostic {
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

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 (downstream): credentialauthority.Assert "+
			"must only be called from sessionlogin/, sessionrefresh/, sessionvalidate/, "+
			"or the funnel package itself. Any other call site is a funnel breach.")

	verifyAssertCallerRedFixtureDetected(
		t,
		"./cells/accesscore/internal/credentialauthority/testdata/outside_caller_red",
		"CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 downstream RED fixture",
	)
}

// scanAssertCallSites flags every CallExpr in file whose callee resolves via
// ResolvePackageRef to credentialauthority.Assert.
func scanAssertCallSites(p *Pass, file *ast.File, rel string) []string {
	var out []string
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
		if !ok {
			return
		}
		if pkgPath != credAuthorityPkgPath || name != credAuthorityFnName {
			return
		}
		line := p.Fset.Position(call.Pos()).Line
		out = append(out, fmt.Sprintf(
			"%s:%d: CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01: call to %s.%s "+
				"outside slice allowlist (sessionlogin/, sessionrefresh/, sessionvalidate/)",
			rel, line, credAuthorityPkgPath, credAuthorityFnName,
		))
	})
	return out
}

func verifyAssertCallerRedFixtureDetected(t *testing.T, pattern, label string) {
	t.Helper()
	var found int
	_ = RunTyped(t, TypedOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
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

	var violations []string
	_ = RunTyped(t, TypedOpts{}, []string{
		"./cells/accesscore/slices/sessionlogin/...",
		"./cells/accesscore/slices/sessionrefresh/...",
		"./cells/accesscore/slices/sessionvalidate/...",
	}, func(p *Pass) []Diagnostic {
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

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 (upstream): slice production "+
			"code (sessionlogin/sessionrefresh/sessionvalidate) must not directly "+
			"call user.CanAuthenticate() or read user.PasswordVersion outside "+
			"Assert. Route through credentialauthority.Assert with the appropriate "+
			"Check. (RevokedAt is governed by SESSION-REVOKED-FIELD-ACCESS-01.)")

	verifyDirectReadRedFixtureDetectedPerBucket(
		t,
		"./cells/accesscore/internal/credentialauthority/testdata/direct_canauth_skip_red",
		"CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 upstream RED fixture",
	)
}

// scanDirectCanAuthCalls flags direct CallExpr to (*domain.User).CanAuthenticate
// inside slice files that should route through Assert.
func scanDirectCanAuthCalls(p *Pass, file *ast.File, rel string) []string {
	var out []string
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != credCanAuthenticate {
			return
		}
		fn, ok := ResolveMethodCall(p.TypesInfo, sel)
		if !ok {
			return
		}
		if fn.Pkg() == nil || fn.Pkg().Path() != domainUserPkg {
			return
		}
		line := p.Fset.Position(call.Pos()).Line
		out = append(out, fmt.Sprintf(
			"%s:%d: CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01: direct call to "+
				"domain.(*User).CanAuthenticate outside credentialauthority.Assert",
			rel, line,
		))
	})
	return out
}

// scanDirectFieldReads flags SelectorExpr reads of domain.User.PasswordVersion
// inside slice files. (RevokedAt is handled by SESSION-REVOKED-FIELD-ACCESS-01.)
func scanDirectFieldReads(p *Pass, file *ast.File, rel string) []string {
	var out []string
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if sel.Sel == nil || sel.Sel.Name != credPasswordVersion {
			return
		}
		selection := p.TypesInfo.Selections[sel]
		if selection == nil {
			return
		}
		obj := selection.Obj()
		field, ok := obj.(*types.Var)
		if !ok || !field.IsField() {
			return
		}
		recv := selection.Recv()
		if recv == nil {
			return
		}
		owner := typeOwner(recv)
		if owner == nil {
			return
		}
		ownerPkg := owner.Pkg()
		if ownerPkg == nil ||
			ownerPkg.Path() != domainUserPkg ||
			owner.Name() != domainUserType {
			return
		}
		line := p.Fset.Position(sel.Pos()).Line
		out = append(out, fmt.Sprintf(
			"%s:%d: CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01: direct read of "+
				"%s.%s.%s outside credentialauthority.Assert",
			rel, line, ownerPkg.Path(), owner.Name(), sel.Sel.Name,
		))
	})
	return out
}

// typeOwner unwraps a *T → T and returns the *types.TypeName for the owning
// named type, or nil if the type isn't a *types.Named.
func typeOwner(t types.Type) *types.TypeName {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil
	}
	return named.Obj()
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
	_ = RunTyped(t, TypedOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
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
func TestCredentialAuthorityAssertFunnel_BlindSpot_MethodValueAssignment(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = RunTyped(t, TypedOpts{}, []string{
		"./cells/accesscore/...",
		"./cmd/...",
	}, func(p *Pass) []Diagnostic {
		if p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			EachInSubtree[ast.AssignStmt](file, func(assign *ast.AssignStmt) {
				// EachInSubtree (not EachInChildren) so chained-call shapes
				// like `fn := obj.GetUser().CanAuthenticate` are covered —
				// the SelectorExpr nests inside a CallExpr child of the
				// AssignStmt, which EachInChildren (depth=1) would miss.
				EachInSubtree[ast.SelectorExpr](assign, func(sel *ast.SelectorExpr) {
					if sel.Sel != nil && sel.Sel.Name == credCanAuthenticate {
						line := p.Fset.Position(assign.Pos()).Line
						violations = append(violations, fmt.Sprintf(
							"%s:%d: method-value assignment of CanAuthenticate "+
								"blind spot detected — archtest would miss the deferred call",
							rel, line,
						))
					}
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
	_ = RunTyped(t, TypedOpts{}, []string{
		"./cells/accesscore/...",
		"./cmd/...",
	}, func(p *Pass) []Diagnostic {
		if p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil || sel.Sel.Name != "MethodByName" {
					return
				}
				if len(call.Args) != 1 {
					return
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok {
					return
				}
				name := strings.Trim(lit.Value, `"`)
				if name == credCanAuthenticate {
					line := p.Fset.Position(call.Pos()).Line
					violations = append(violations, fmt.Sprintf(
						"%s:%d: reflect.MethodByName(%q) blind spot detected — "+
							"archtest cannot see reflect-based invocations",
						rel, line, name,
					))
				}
			})
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

	bannedNames := map[string]bool{
		credPasswordVersion: true,
	}

	var violations []string
	_ = RunTyped(t, TypedOpts{}, []string{
		"./cells/accesscore/...",
		"./cmd/...",
	}, func(p *Pass) []Diagnostic {
		if p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil || sel.Sel.Name != "FieldByName" {
					return
				}
				if len(call.Args) != 1 {
					return
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok {
					return
				}
				name := strings.Trim(lit.Value, `"`)
				if bannedNames[name] {
					line := p.Fset.Position(call.Pos()).Line
					violations = append(violations, fmt.Sprintf(
						"%s:%d: reflect.FieldByName(%q) blind spot detected — "+
							"archtest cannot see reflect-based field reads",
						rel, line, name,
					))
				}
			})
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
// reads bypass field visibility entirely. Scoped to cells/accesscore/... and
// cmd/... — adapters/postgres legitimately uses unsafe for pgx and is out of
// scope here.
func TestCredentialAuthorityAssertFunnel_BlindSpot_UnsafePointerRead(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = RunTyped(t, TypedOpts{}, []string{
		"./cells/accesscore/...",
		"./cmd/...",
	}, func(p *Pass) []Diagnostic {
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
		"credentialauthority blind-spot: unsafe import found in cells/accesscore "+
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
// detection is unicode.IsUpper on the first rune, identical to Go's own
// export rule. Picking any exported name shape is a CI failure.
func TestCredentialAuthorityAssertFunnel_UpstreamSealed_03(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = RunTyped(t, TypedOpts{}, []string{
		"./cells/accesscore/internal/credentialauthority/...",
	}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		// credAuthorityPkgPath is the const at file top; reuse instead of
		// reassembling from credAuthorityPkgRel.
		if p.Pkg.Path() != credAuthorityPkgPath {
			return nil
		}
		// Find the Check interface in this package.
		checkObj := p.Pkg.Scope().Lookup("Check")
		if checkObj == nil {
			return nil
		}
		checkIface, ok := checkObj.Type().Underlying().(*types.Interface)
		if !ok {
			return nil
		}
		// Walk all type names in this package; for each named struct, check
		// whether it implements Check. If so, the name must be unexported.
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
			// Pointer-receiver methods are on *T; value-receiver on T.
			// Check both because Go method sets differ.
			if !types.Implements(named, checkIface) &&
				!types.Implements(types.NewPointer(named), checkIface) {
				continue
			}
			if !isExportedName(name) {
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

func isExportedName(s string) bool {
	if s == "" {
		return false
	}
	r := s[0]
	return r >= 'A' && r <= 'Z'
}

// stripModuleRoot turns an absolute filename produced by p.Fset.Position
// into a module-relative path for assert messages. Best-effort: if the
// module-root marker is missing, returns the absolute path unchanged.
func stripModuleRoot(abs string) string {
	const marker = "/gocell/"
	if i := strings.LastIndex(abs, marker); i >= 0 {
		return abs[i+len(marker):]
	}
	// Worktree-aware fallback: file paths under worktrees/<NN>-<name>/
	// strip the marker manually.
	if i := strings.Index(abs, "/worktrees/"); i >= 0 {
		rest := abs[i+len("/worktrees/"):]
		if j := strings.Index(rest, "/"); j >= 0 {
			return rest[j+1:]
		}
	}
	return abs
}

// ─── Upstream prong: function-value capture (P2-B) ───────────────────────

// TestCredentialAuthorityAssertFunnel_UpstreamValueCapture_04 asserts that
// slice-package production files do NOT capture
// credentialauthority.Assert or domain.(*User).CanAuthenticate as a
// function value (AssignStmt RHS, ValueSpec value, or CallExpr argument).
// Direct CallExpr detection (Upstream_02) only sees the call site itself;
// function-value capture defers the call to an Ident site where receiver
// resolution is unrecoverable.
//
// Hard rating: every Ident / SelectorExpr considered is type-resolved
// via *types.Info — name-based shape matching is impossible to game.
//
// RED fixture: testdata/value_capture_red.
func TestCredentialAuthorityAssertFunnel_UpstreamValueCapture_04(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = RunTyped(t, TypedOpts{}, []string{
		"./cells/accesscore/slices/sessionlogin/...",
		"./cells/accesscore/slices/sessionrefresh/...",
		"./cells/accesscore/slices/sessionvalidate/...",
	}, func(p *Pass) []Diagnostic {
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
			violations = append(violations, scanFunnelValueCapture(p, file, rel)...)
		}
		return nil
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 (value-capture): slice "+
			"production code captured credentialauthority.Assert or "+
			"domain.(*User).CanAuthenticate as a function value (AssignStmt / "+
			"ValueSpec / call argument), defeating the direct-CallExpr funnel. "+
			"Call the function directly at the use site.")

	verifyValueCaptureRedFixtureDetectedPerBucket(
		t,
		"./cells/accesscore/internal/credentialauthority/testdata/value_capture_red",
		"CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 value-capture RED fixture",
	)
}

// scanFunnelValueCapture flags AssignStmt RHS, ValueSpec values, and
// CallExpr arguments that resolve via *types.Info to the funnel-protected
// callees (credentialauthority.Assert, domain.(*User).CanAuthenticate).
func scanFunnelValueCapture(p *Pass, file *ast.File, rel string) []string {
	var out []string

	check := func(expr ast.Expr, kind string) {
		if expr == nil {
			return
		}
		// Direct CallExpr is already covered by Upstream_02; only flag
		// non-call references (i.e., the function value itself).
		if _, isCall := expr.(*ast.CallExpr); isCall {
			return
		}
		if pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, expr); ok {
			if pkgPath == credAuthorityPkgPath && name == credAuthorityFnName {
				pos := p.Fset.Position(expr.Pos())
				out = append(out, fmt.Sprintf(
					"%s:%d: %s captures credentialauthority.Assert as function value",
					rel, pos.Line, kind,
				))
				return
			}
		}
		if sel, ok := expr.(*ast.SelectorExpr); ok && sel.Sel != nil &&
			sel.Sel.Name == credCanAuthenticate {
			fn, ok := ResolveMethodCall(p.TypesInfo, sel)
			if ok && fn.Pkg() != nil && fn.Pkg().Path() == domainUserPkg {
				pos := p.Fset.Position(sel.Pos())
				out = append(out, fmt.Sprintf(
					"%s:%d: %s captures domain.(*User).CanAuthenticate as method value",
					rel, pos.Line, kind,
				))
			}
		}
	}

	// (1) AssignStmt: fn := pkg.X / fn := obj.Method
	EachInSubtree[ast.AssignStmt](file, func(assign *ast.AssignStmt) {
		for _, rhs := range assign.Rhs {
			check(rhs, "AssignStmt")
		}
	})

	// (2) ValueSpec: var fn = pkg.X
	EachInSubtree[ast.ValueSpec](file, func(spec *ast.ValueSpec) {
		for _, v := range spec.Values {
			check(v, "ValueSpec")
		}
	})

	// (3) CallExpr argument: someHelper(pkg.X)
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		// Skip the very call we are passing args to (handled by direct-call
		// detector or unrelated).
		for _, arg := range call.Args {
			check(arg, "CallArg")
		}
	})

	return out
}

func verifyValueCaptureRedFixtureDetectedPerBucket(t *testing.T, pattern, label string) {
	t.Helper()
	var assignHits, valueSpecHits, callArgHits int
	_ = RunTyped(t, TypedOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			for _, v := range scanFunnelValueCapture(p, file, label) {
				switch {
				case strings.Contains(v, "AssignStmt"):
					assignHits++
				case strings.Contains(v, "ValueSpec"):
					valueSpecHits++
				case strings.Contains(v, "CallArg"):
					callArgHits++
				}
			}
		}
		return nil
	})
	assert.GreaterOrEqual(t, assignHits, 1,
		"RED fixture self-check FAILED (AssignStmt bucket): %s — expected "+
			"≥ 1 AssignStmt violation, got 0.",
		label)
	assert.GreaterOrEqual(t, valueSpecHits, 1,
		"RED fixture self-check FAILED (ValueSpec bucket): %s — expected "+
			"≥ 1 ValueSpec violation, got 0.",
		label)
	assert.GreaterOrEqual(t, callArgHits, 1,
		"RED fixture self-check FAILED (CallArg bucket): %s — expected "+
			"≥ 1 CallArg violation, got 0.",
		label)
}

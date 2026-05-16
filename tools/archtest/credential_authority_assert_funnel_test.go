package archtest

// credential_authority_assert_funnel_test.go — Hard double-prong funnel
// for the read-side credential-authority decision (S-next).
//
// INVARIANT: CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01
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
//     Production code under sessionlogin/, sessionrefresh/, sessionvalidate/
//     must NOT directly call domain.(*User).CanAuthenticate, read
//     domain.User.PasswordVersion, or read
//     session.{Session,ValidateView}.RevokedAt. Each of these resolves to a
//     specific *types.Func / *types.Var identity via *types.Info, so any
//     direct dependency is detectable. Combined with the blind-spot
//     self-checks (method-value / reflect.Method / reflect.Field / unsafe
//     / slice-internal helper), upstream is also Hard.
//
// Two-prong Hard closes the loop with write-side authzmutate:
//
//   write-side (authzmutate.Mutator.Apply): DOMAIN-AUTHZ-FIELD-PRIVATE-01 +
//                                            AUTHZ-MUTATION-APPLY-FUNNEL-01
//   read-side  (credentialauthority.Assert): this file
//
// Together: write-side / read-side bidirectional closure for "who decides
// whether a credential is authoritative."
//
// Scanning tools:
//   - Downstream: typeseval.ResolvePackageRef + EachInSubtree[ast.CallExpr]
//   - Upstream method:    typeseval.ResolveMethodCall + EachInSubtree[ast.CallExpr]
//   - Upstream field:     *types.Info.Selections lookup over EachInSubtree[ast.SelectorExpr]
//
// Blind-spot self-checks (ai-collab.md §"工具选定后强制盲区自检"):
//
//  1. Method-value assignment: `fn := u.CanAuthenticate; fn()`
//     The deferred fn() CallExpr's Fun is *ast.Ident, not *ast.SelectorExpr,
//     so ResolveMethodCall would miss the second call. Captured by:
//     TestCredentialAuthorityAssertFunnel_BlindSpot_MethodValueAssignment.
//
//  2. reflect.Value.MethodByName("CanAuthenticate"): AST-invisible at the
//     bytes that actually invoke the method. Captured by:
//     TestCredentialAuthorityAssertFunnel_BlindSpot_ReflectMethodByName.
//
//  3. reflect.Value.FieldByName("PasswordVersion"/"RevokedAt"): bypasses
//     SelectorExpr resolution; field name is in a string literal. Captured
//     by: TestCredentialAuthorityAssertFunnel_BlindSpot_ReflectFieldByName.
//
//  4. unsafe.Pointer offset read of a User / Session / ValidateView field:
//     bypasses Go field visibility. Captured by:
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
// RED fixtures (must self-fire ≥ 1 violation each):
//   - cells/accesscore/internal/credentialauthority/testdata/outside_caller_red:
//     non-allowlisted caller invokes credentialauthority.Assert.
//   - cells/accesscore/internal/credentialauthority/testdata/direct_canauth_skip_red:
//     simulated slice file reads user.CanAuthenticate() and user.PasswordVersion
//     directly without routing through Assert.

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
// domain.(*User).CanAuthenticate, read domain.User.PasswordVersion, or
// read session.{Session,ValidateView}.RevokedAt. Such reads must route
// through credentialauthority.Assert.
//
// RED fixture: testdata/direct_canauth_skip_red simulates a slice file that
// reads these directly without going through Assert.
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
			"call user.CanAuthenticate() or read user.PasswordVersion / "+
			"session.{Session,ValidateView}.RevokedAt outside Assert. Route "+
			"through credentialauthority.Assert with the appropriate Check.")

	verifyDirectReadRedFixtureDetected(
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
// and session.{Session,ValidateView}.RevokedAt inside slice files.
func scanDirectFieldReads(p *Pass, file *ast.File, rel string) []string {
	var out []string
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if sel.Sel == nil {
			return
		}
		name := sel.Sel.Name
		if name != credPasswordVersion && name != credRevokedAt {
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
		// Owner is the struct that declares the field.
		recv := selection.Recv()
		if recv == nil {
			return
		}
		owner := typeOwner(recv)
		if owner == nil {
			return
		}
		// Match domain.User.PasswordVersion OR
		// runtime/auth/session.{Session,ValidateView}.RevokedAt.
		ownerPkg := owner.Pkg()
		if ownerPkg == nil {
			return
		}
		switch {
		case name == credPasswordVersion &&
			ownerPkg.Path() == domainUserPkg && owner.Name() == domainUserType:
			// match
		case name == credRevokedAt &&
			ownerPkg.Path() == credSessionPkgPath &&
			(owner.Name() == credSessionType || owner.Name() == credSessionViewType):
			// match
		default:
			return
		}
		line := p.Fset.Position(sel.Pos()).Line
		out = append(out, fmt.Sprintf(
			"%s:%d: CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01: direct read of "+
				"%s.%s.%s outside credentialauthority.Assert",
			rel, line, ownerPkg.Path(), owner.Name(), name,
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

func verifyDirectReadRedFixtureDetected(t *testing.T, pattern, label string) {
	t.Helper()
	var found int
	_ = RunTyped(t, TypedOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			found += len(scanDirectCanAuthCalls(p, file, label))
			found += len(scanDirectFieldReads(p, file, label))
		}
		return nil
	})
	assert.GreaterOrEqual(t, found, 1,
		"RED fixture self-check FAILED: %s — expected ≥ 1 violation, got 0. "+
			"Check that the fixture directly calls user.CanAuthenticate / reads "+
			"PasswordVersion / RevokedAt and is type-checkable.",
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
				EachInChildren[ast.SelectorExpr](assign, func(sel *ast.SelectorExpr) {
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
// that reflect.Value.FieldByName with PasswordVersion / RevokedAt does NOT
// appear in production code, confirming the reflect field-write blind spot
// is not exercised.
func TestCredentialAuthorityAssertFunnel_BlindSpot_ReflectFieldByName(t *testing.T) {
	t.Parallel()

	bannedNames := map[string]bool{
		credPasswordVersion: true,
		credRevokedAt:       true,
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
		"credentialauthority blind-spot: reflect.FieldByName of PasswordVersion / "+
			"RevokedAt found in production code — the archtest cannot see reflect "+
			"field reads.")
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

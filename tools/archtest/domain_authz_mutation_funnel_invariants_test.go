package archtest

// domain_authz_mutation_funnel_invariants_test.go — two invariants completing
// the S4e authz-mutation funnel closure.
//
// INVARIANT: DOMAIN-AUTHZ-FIELD-PRIVATE-01
// INVARIANT: AUTHZ-MUTATION-APPLY-FUNNEL-01
//
// Funnel 双向锁评级 (ai-collab.md §"Funnel 双向锁评级"):
//
//   Downstream Hard (DOMAIN-AUTHZ-FIELD-PRIVATE-01):
//     Cross-package write of domain.User authz fields (status,
//     passwordResetRequired, authzEpoch) is a compile error because the fields
//     are unexported — this is the compile-time privatization Hard guarantee.
//     This archtest is the regression net: it fires before a re-export ever
//     reaches a build. Addition of a new public setter beyond the two
//     sanctioned ones, or a reflection-based write, would be caught here.
//
//   Downstream Hard (AUTHZ-MUTATION-APPLY-FUNNEL-01, Rule a — SetStatus /
//   SetPasswordResetRequired caller set):
//     "Form uniqueness" = "call resolves to this exact *types.Func identity via
//     typeseval.ResolveMethodCall". Any call site outside the narrowed file-level
//     allowlist fails archtest in CI with no gray zone. Honest caveat: Go does
//     not prevent the calls at compile time (the methods are exported); enforcement
//     is archtest-bound. This is the highest Hard grade reachable in Go for
//     exported-method caller restriction.
//
//   Upstream Medium-by-necessity (caller-set upper-bound):
//     The upstream guarantee — that all live-aggregate authz mutations MUST go
//     through authzmutate.Mutator.Apply — is Medium, not Hard, because:
//     (a) identitymanage/service.go and adminprovision/provisioner.go legitimately
//     call SetPasswordResetRequired at creation time (no live sessions yet);
//     routing through authzmutate would be semantically wrong.
//     (b) sealed interfaces or codegen cannot express "creation-time-only" as a
//     compile-time invariant without redesigning the domain model.
//     The Medium ceiling is an accepted architectural trade-off; the file-level
//     allowlist (not package-level) is the tightest achievable restriction:
//     only the exact files that contain creation-time calls are allowlisted.
//     See backlog item AUTHZ-MUTATION-FUNNEL-UPSTREAM-HARD-01 for future Hard
//     upgrade path via domain model redesign.
//
// Relationship to CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01 (Rule b):
//   The Hard closure of the P1.2 / P1-#1 regression class is Rule (a) above —
//   field privatization + SetStatus/SetPasswordResetRequired funnel. Narrowing
//   the credentialinvalidate.Invalidator.Apply caller set (Rule b) is a
//   secondary tightening implemented in-file by modifying upstreamCallerAllowlistPrefixes
//   in credential_invalidate_funnel_invariants_test.go (setup/ and adminprovision/
//   removed — they do not call Apply in production code).
//
//   The ADR §A10 idealization "{authzmutate, sessionrefresh}" is NOT achievable:
//   Delete and changePasswordInTx in identitymanage need Invalidator.Apply co-tx
//   with another write for atomicity; routing through authzmutate would split
//   their transaction. Similarly rbacassign needs role-row write + revoke in one
//   tx. The actual S4e legitimate caller set is {authzmutate/, identitymanage/,
//   rbacassign/, sessionrefresh/} + the funnel package itself.
//   Wave 3 ADR author: the P1 regression class is closed at Rule (a), not at
//   Rule (b). §A10 should be updated to reflect the actual caller set.
//
// Allowlist precision (D1 RC-D hardening):
//   The allowlist was narrowed from package-level prefixes to file-level paths
//   for the two creation-time call sites (identitymanage/service.go and
//   adminprovision/provisioner.go). Any new file in those packages that adds a
//   direct setter call will fail the archtest immediately, without requiring a
//   separate allowlist entry. This is the tightest achievable restriction given
//   the Medium upstream ceiling.
//
// Scanning tool: typeseval.SharedResolver + typeseval.ResolveMethodCall +
// scanner.EachInSubtree[ast.CallExpr] for Rule (a);
// go/types struct field and method set inspection for DOMAIN-AUTHZ-FIELD-PRIVATE-01.
//
// Blind-spot self-check (ai-collab.md §"工具选定后强制盲区自检"):
//
// For AUTHZ-MUTATION-APPLY-FUNNEL-01 — ResolveMethodCall resolves via
// info.Selections. AST forms NOT covered:
//
//  1. Method-value store + call: `fn := u.SetStatus; fn(domain.StatusLocked, t)`
//     The second `fn(...)` CallExpr's Fun is *ast.Ident, not *ast.SelectorExpr,
//     so info.Selections is not consulted. Captured by:
//     TestDomainAuthzMutation_BlindSpot_MethodValueAssignment (asserts absence
//     in production code — if this pattern appeared, the scanner would miss it).
//
//  2. Method expression (qualified): `(*domain.User).SetStatus(u, s, t)`
//     Fun is *ast.SelectorExpr resolving via info.Selections as MethodExpr.
//     ResolveMethodCall explicitly accepts types.MethodExpr — this IS covered.
//     Documented for completeness; no self-check needed.
//
//  3. reflect.Value.MethodByName("SetStatus").Call(...): fully AST-invisible.
//     Captured by:
//     TestDomainAuthzMutation_BlindSpot_ReflectMethodByName (asserts absence).
//
//  4. Dot-import: `import . "...domain"` followed by a bare call. SetStatus is
//     a method, not a package-level function, so dot-import does not affect
//     method calls on a receiver. Not applicable; no self-check needed.
//
//  5. Embedded promotion: `type W struct { *domain.User }; w.SetStatus(...)`
//     resolves via info.Selections to the same *types.Func (promoted method
//     Obj() is the original). This IS covered. Documented for completeness.
//
// For DOMAIN-AUTHZ-FIELD-PRIVATE-01 — go/types struct/method inspection.
// AST forms NOT covered by the type definition check:
//
//  6. unsafe.Pointer offset write bypasses Go field visibility:
//     (*domain.UserStatus)(unsafe.Pointer(uintptr(unsafe.Pointer(u)) + offset))
//     Captured by:
//     TestDomainAuthzMutation_BlindSpot_UnsafePointerWrite (asserts absence).
//
//  7. reflect.ValueOf(u).Elem().FieldByName("status").Set(...):
//     Call-site reflection bypasses type checking. Captured by:
//     TestDomainAuthzMutation_BlindSpot_ReflectFieldByName (asserts absence).

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ─── package path / name constants ───────────────────────────────────────

const (
	domainUserPkg  = "github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	domainUserType = "User"

	domainSetStatusMethod                = "SetStatus"
	domainSetPasswordResetRequiredMethod = "SetPasswordResetRequired"
)

// authzFieldNames are the three authz-sensitive field names that must remain
// private in production domain.User.
var authzFieldNames = map[string]bool{
	"Status":                true,
	"PasswordResetRequired": true,
	"AuthzEpoch":            true,
}

// sanctionedSetters are the two exported mutator methods that ARE permitted on
// domain.User. Any other exported method whose name matches a setter-concept
// prefix and is not in this map is a violation.
var sanctionedSetters = map[string]bool{
	domainSetStatusMethod:                true,
	domainSetPasswordResetRequiredMethod: true,
}

// authzSetterPrefixes are method name prefixes that indicate a setter for
// authz-sensitive state. Methods with these prefixes that are not in
// sanctionedSetters are flagged.
var authzSetterPrefixes = []string{"Set", "Mark", "Clear", "Lock", "Unlock"}

// setMutatorAllowlist lists the module-relative paths whose production code is
// permitted to call domain.User.SetStatus or
// domain.User.SetPasswordResetRequired directly.
//
// Entries ending in "/" are package-level prefixes (all files in the package).
// Entries ending in ".go" are exact file paths (only that file).
//
// Allowlist rationale:
//   - cells/accesscore/internal/authzmutate/ — the primary funnel (package
//     prefix). All live-aggregate authz mutations route through Mutator.Apply
//     which calls mutation.apply() → SetStatus / SetPasswordResetRequired.
//     The entire package is allowlisted because any future mutation type
//     added here is legitimate funnel code.
//   - cells/accesscore/internal/adminprovision/provisioner.go — creation-time
//     only (exact file). No live sessions exist for a brand-new user (epoch=1).
//     SetPasswordResetRequired is called on a freshly constructed aggregate
//     before any session exists. authzmutate.Apply is for mutating existing
//     principals, not initial construction. Any NEW file in adminprovision/
//     that adds a direct setter call MUST be reviewed and explicitly added here.
//   - cells/accesscore/internal/domain/ — the methods' own package (package
//     prefix). SetStatus and SetPasswordResetRequired are defined here.
//   - cells/accesscore/slices/identitymanage/service.go — creation-time only
//     (exact file). service.go calls SetPasswordResetRequired on a freshly
//     constructed user aggregate (identitymanage create path). Any NEW file in
//     identitymanage/ that adds a direct setter call MUST be reviewed and
//     explicitly added here.
//
// _test.go files are always allowed.
var setMutatorAllowlist = []string{
	"cells/accesscore/internal/authzmutate/",
	"cells/accesscore/internal/adminprovision/provisioner.go",
	"cells/accesscore/internal/domain/",
	"cells/accesscore/slices/identitymanage/service.go",
}

// isSetMutatorAllowlisted reports whether a module-relative path is in the
// set-mutator allowlist. Test files (*_test.go) always pass.
//
// Entries ending in "/" match any file under that directory prefix.
// Entries ending in ".go" match only that exact file.
func isSetMutatorAllowlisted(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	for _, entry := range setMutatorAllowlist {
		if strings.HasSuffix(entry, "/") {
			// Package-level prefix: match any file under this directory.
			if strings.HasPrefix(rel, entry) {
				return true
			}
		} else {
			// Exact file path.
			if rel == entry {
				return true
			}
		}
	}
	return false
}

// ─── Rule 1: DOMAIN-AUTHZ-FIELD-PRIVATE-01 ─────────────────────────────

// TestDomainAuthzFieldPrivate_01 enforces DOMAIN-AUTHZ-FIELD-PRIVATE-01:
// domain.User must NOT expose exported fields named Status,
// PasswordResetRequired, or AuthzEpoch, and must NOT have exported setter
// methods matching the Set*/Mark*/Clear*/Lock*/Unlock* pattern beyond the two
// sanctioned ones (SetStatus / SetPasswordResetRequired).
//
// Primary guarantee: Go field privatization makes cross-package writes of
// status/passwordResetRequired/authzEpoch a compile error. This test is the
// regression net that fires before a re-export ever reaches a build.
//
// Implementation: load the domain package via typeseval.SharedResolver, look
// up the User type via pkg.Types.Scope().Lookup("User"), then:
//  1. Inspect every struct field: exported field name in authzFieldNames → violation.
//  2. Inspect the pointer receiver method set: any exported method whose name
//     starts with a setter prefix and is NOT in sanctionedSetters → violation.
//
// RED fixture: testdata/authz_mutation_fixtures/domain_exported_authz_field_red
// contains a synthetic User struct with exported Status, PasswordResetRequired,
// AuthzEpoch fields and a SetStatusPublic method. The scanner must flag ≥ 1.
func TestDomainAuthzFieldPrivate_01(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = RunTyped(t, TypedOpts{}, []string{"./cells/accesscore/internal/domain"}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != domainUserPkg {
			return nil
		}
		violations = append(violations, scanDomainUserViolations(p.Pkg)...)
		return nil
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"DOMAIN-AUTHZ-FIELD-PRIVATE-01: domain.User must not expose exported authz fields "+
			"(Status, PasswordResetRequired, AuthzEpoch) or unauthorized exported setters "+
			"(beyond SetStatus / SetPasswordResetRequired). Keep these fields private; "+
			"mutate only through authzmutate.Mutator.Apply.")

	// RED fixture verification.
	root := findModuleRoot(t)
	verifyDomainFieldRedFixtureDetected(t, root,
		"./tools/archtest/testdata/authz_mutation_fixtures/domain_exported_authz_field_red",
		"DOMAIN-AUTHZ-FIELD-PRIVATE-01 RED fixture",
	)
}

// scanDomainUserViolations inspects the User named type in pkg for exported
// authz fields and unauthorized exported setter methods.
func scanDomainUserViolations(pkg *types.Package) []string {
	obj := pkg.Scope().Lookup(domainUserType)
	if obj == nil {
		return []string{fmt.Sprintf(
			"DOMAIN-AUTHZ-FIELD-PRIVATE-01: type %s not found in package %s",
			domainUserType, pkg.Path())}
	}
	named, ok := obj.Type().(*types.Named)
	if !ok {
		return []string{fmt.Sprintf(
			"DOMAIN-AUTHZ-FIELD-PRIVATE-01: %s is not a named type in %s",
			domainUserType, pkg.Path())}
	}

	var out []string

	// Check struct fields.
	if strct, ok := named.Underlying().(*types.Struct); ok {
		for i := 0; i < strct.NumFields(); i++ {
			f := strct.Field(i)
			if f.Exported() && authzFieldNames[f.Name()] {
				out = append(out, fmt.Sprintf(
					"DOMAIN-AUTHZ-FIELD-PRIVATE-01: %s.%s has exported authz field %q — must be private",
					domainUserType, pkg.Path(), f.Name()))
			}
		}
	}

	// Check pointer-receiver method set (all public mutations use *User).
	mset := types.NewMethodSet(types.NewPointer(named))
	for i := 0; i < mset.Len(); i++ {
		name := mset.At(i).Obj().Name()
		if !token.IsExported(name) {
			continue
		}
		if sanctionedSetters[name] {
			continue
		}
		for _, prefix := range authzSetterPrefixes {
			if strings.HasPrefix(name, prefix) {
				out = append(out, fmt.Sprintf(
					"DOMAIN-AUTHZ-FIELD-PRIVATE-01: %s.%s has unauthorized exported setter %q "+
						"(prefix %q); only SetStatus and SetPasswordResetRequired are sanctioned",
					domainUserType, pkg.Path(), name, prefix))
				break
			}
		}
	}

	return out
}

// verifyDomainFieldRedFixtureDetected loads the RED fixture package and asserts
// that the domain-field scanner finds ≥ 1 violation — proving the rule is not
// permanently GREEN.
func verifyDomainFieldRedFixtureDetected(t *testing.T, root, fixturePattern, label string) {
	t.Helper()
	_ = root // root is the module root; RunTyped resolves it via findModuleRoot internally
	var found int
	_ = RunTyped(t, TypedOpts{}, []string{fixturePattern}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		found += len(scanDomainUserViolations(p.Pkg))
		return nil
	})
	assert.GreaterOrEqual(t, found, 1,
		"RED fixture self-check FAILED: %s — expected ≥ 1 violation, got 0. "+
			"Check that the fixture actually exports authz fields or unauthorized setters.",
		label)
}

// ─── Rule 2: AUTHZ-MUTATION-APPLY-FUNNEL-01 ────────────────────────────

// TestAuthzMutationApplyFunnel_SetStatus_01 enforces Rule (a) of
// AUTHZ-MUTATION-APPLY-FUNNEL-01: every call to domain.User.SetStatus or
// domain.User.SetPasswordResetRequired in non-test production code must
// originate from an entry in setMutatorAllowlist.
//
// Allowlist (see setMutatorAllowlist for rationale):
//   - cells/accesscore/internal/authzmutate/ — primary funnel (package prefix)
//   - cells/accesscore/internal/adminprovision/provisioner.go — creation-time only (exact file)
//   - cells/accesscore/internal/domain/ — methods' own package (package prefix)
//   - cells/accesscore/slices/identitymanage/service.go — creation-time only (exact file)
//
// RED fixture: cells/accesscore/internal/domain/testdata/rbacassign_direct_setstatus_red
// simulates an rbacassign caller invoking SetStatus directly — must detect ≥ 1.
func TestAuthzMutationApplyFunnel_SetStatus_01(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = RunTyped(t, TypedOpts{}, []string{
		"./cells/accesscore/...",
		"./cmd/...",
	}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if isSetMutatorAllowlisted(rel) {
				continue
			}
			violations = append(violations,
				scanSetMutatorViolationsPass(p, file, rel, domainSetStatusMethod)...)
			violations = append(violations,
				scanSetMutatorViolationsPass(p, file, rel, domainSetPasswordResetRequiredMethod)...)
		}
		return nil
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"AUTHZ-MUTATION-APPLY-FUNNEL-01 (Rule a): domain.User.SetStatus and "+
			"domain.User.SetPasswordResetRequired must only be called from the allowlisted "+
			"packages (authzmutate/, adminprovision/, domain/, identitymanage/). "+
			"Route new live-aggregate mutations through authzmutate.Mutator.Apply.")

	// RED fixture: rbacassign caller directly invoking SetStatus.
	// LOCATION: cells/accesscore/internal/domain/testdata/ because domain is an
	// internal package; the fixture must live under cells/accesscore/ to satisfy
	// Go's internal-import rule, and testdata/ keeps it out of go build ./...
	verifySetMutatorRedFixtureDetected(t,
		"./cells/accesscore/internal/domain/testdata/rbacassign_direct_setstatus_red",
		domainSetStatusMethod,
		"AUTHZ-MUTATION-APPLY-FUNNEL-01 Rule (a) RED fixture",
	)
}

// scanSetMutatorViolationsPass walks a single file's AST for CallExpr nodes where
// the method receiver resolves to domain.User.SetStatus or
// domain.User.SetPasswordResetRequired. It returns a slice of violation strings.
//
// This reuses the same ResolveMethodCall + EachInSubtree[ast.CallExpr] pattern
// as scanFunnelViolations in credential_invalidate_funnel_invariants_test.go,
// but targets domain.User methods rather than store interface methods.
func scanSetMutatorViolationsPass(
	p *Pass,
	file *ast.File,
	rel string,
	targetMethod string,
) []string {
	var out []string
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return
		}
		if sel.Sel.Name != targetMethod {
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
			"%s:%d: AUTHZ-MUTATION-APPLY-FUNNEL-01: direct call to domain.User.%s "+
				"outside allowed funnel packages",
			rel, line, targetMethod))
	})
	return out
}

// verifySetMutatorRedFixtureDetected loads the given RED fixture and asserts
// that the scanner finds ≥ 1 violation — proving the rule is not permanently GREEN.
func verifySetMutatorRedFixtureDetected(
	t *testing.T,
	fixturePattern, targetMethod, label string,
) {
	t.Helper()
	var found int
	_ = RunTyped(t, TypedOpts{}, []string{fixturePattern}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			found += len(scanSetMutatorViolationsPass(p, file, label, targetMethod))
		}
		return nil
	})
	assert.GreaterOrEqual(t, found, 1,
		"RED fixture self-check FAILED: %s — expected ≥ 1 violation, got 0. "+
			"Check that the fixture calls the banned method and is type-checkable.",
		label)
}

// ─── Blind-spot self-check tests ─────────────────────────────────────────

// TestDomainAuthzMutation_BlindSpot_MethodValueAssignment asserts that the
// method-value-assignment blind spot (e.g. `fn := u.SetStatus; fn(...)`) does
// NOT appear in production code outside the allowlist. If it did, the scanner
// would miss the second CallExpr because fn(...)  has Fun=*ast.Ident, not
// *ast.SelectorExpr.
//
// Scanner: EachInSubtree[ast.AssignStmt] + right-hand-side SelectorExpr name
// matching. AST-only (no type info), but the method names are distinct enough
// to avoid false positives.
func TestDomainAuthzMutation_BlindSpot_MethodValueAssignment(t *testing.T) {
	t.Parallel()

	bannedNames := map[string]bool{
		domainSetStatusMethod:                true,
		domainSetPasswordResetRequiredMethod: true,
	}

	var violations []string
	_ = RunTyped(t, TypedOpts{}, []string{
		"./cells/accesscore/...", "./cmd/...",
	}, func(p *Pass) []Diagnostic {
		if p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			if isSetMutatorAllowlisted(rel) {
				continue
			}
			EachInSubtree[ast.AssignStmt](file, func(assign *ast.AssignStmt) {
				EachInChildren[ast.SelectorExpr](assign, func(sel *ast.SelectorExpr) {
					if bannedNames[sel.Sel.Name] {
						line := p.Fset.Position(assign.Pos()).Line
						violations = append(violations, fmt.Sprintf(
							"%s:%d: method-value assignment of %s blind spot detected — "+
								"archtest would miss the second call site",
							rel, line, sel.Sel.Name))
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
		"authz-mutation blind-spot: method-value assignment of SetStatus / "+
			"SetPasswordResetRequired found in non-allowlisted production code — "+
			"the archtest would miss the deferred call. Refactor to call authzmutate.Mutator.Apply.")
}

// TestDomainAuthzMutation_BlindSpot_ReflectMethodByName asserts that
// reflect.Value.MethodByName("SetStatus") / ("SetPasswordResetRequired") does
// NOT appear in non-allowlisted production code, confirming the reflect blind
// spot is not exercised.
func TestDomainAuthzMutation_BlindSpot_ReflectMethodByName(t *testing.T) {
	t.Parallel()

	bannedNames := map[string]bool{
		domainSetStatusMethod:                true,
		domainSetPasswordResetRequiredMethod: true,
	}

	var violations []string
	_ = RunTyped(t, TypedOpts{}, []string{
		"./cells/accesscore/...", "./cmd/...",
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
				if !ok || sel.Sel.Name != "MethodByName" {
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
						"%s:%d: reflect.MethodByName(%q) blind spot detected — "+
							"archtest cannot see reflect-based invocations of authz setters",
						rel, line, name))
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
		"authz-mutation blind-spot: reflect.MethodByName of SetStatus / "+
			"SetPasswordResetRequired found in production code — the archtest cannot "+
			"see reflect-based invocations. Refactor to use authzmutate.Mutator.Apply.")
}

// TestDomainAuthzMutation_BlindSpot_UnsafePointerWrite asserts that
// unsafe.Pointer-based writes to domain.User authz fields do NOT appear in
// non-allowlisted production code. Such writes bypass Go's field visibility
// entirely and would be invisible to the type-definition check in
// DOMAIN-AUTHZ-FIELD-PRIVATE-01.
//
// Scanner: AST-only search for import of "unsafe" outside the allowlist.
// The unsafe package is legitimately used in adapters/postgres for pgx scanning;
// this check is scoped to cells/accesscore/... and cmd/... and specifically
// flags packages that import "unsafe" outside the allowlist.
func TestDomainAuthzMutation_BlindSpot_UnsafePointerWrite(t *testing.T) {
	t.Parallel()

	var violations []string
	_ = RunTyped(t, TypedOpts{}, []string{
		"./cells/accesscore/...", "./cmd/...",
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
						"%s:%d: imports \"unsafe\" — potential unsafe.Pointer write "+
							"could bypass domain.User authz field privatization "+
							"(blind spot for DOMAIN-AUTHZ-FIELD-PRIVATE-01)",
						rel, line))
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
		"authz-mutation blind-spot: unsafe import found in cells/accesscore or cmd/ — "+
			"verify no unsafe.Pointer writes target domain.User private fields.")
}

// TestDomainAuthzMutation_BlindSpot_ReflectFieldByName asserts that
// reflect.Value.FieldByName with authz field names does NOT appear in
// non-allowlisted production code. Such calls would bypass field privatization
// and be invisible to the type-definition check.
func TestDomainAuthzMutation_BlindSpot_ReflectFieldByName(t *testing.T) {
	t.Parallel()

	// Check both private field names (actual names) and potential exported regressions.
	bannedFieldNames := map[string]bool{
		"status":                true,
		"passwordResetRequired": true,
		"authzEpoch":            true,
		"Status":                true,
		"PasswordResetRequired": true,
		"AuthzEpoch":            true,
	}

	var violations []string
	_ = RunTyped(t, TypedOpts{}, []string{
		"./cells/accesscore/...", "./cmd/...",
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
				if !ok || sel.Sel.Name != "FieldByName" {
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
				if bannedFieldNames[name] {
					line := p.Fset.Position(call.Pos()).Line
					violations = append(violations, fmt.Sprintf(
						"%s:%d: reflect.FieldByName(%q) blind spot detected — "+
							"archtest cannot see reflect-based writes to domain.User authz fields",
						rel, line, name))
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
		"authz-mutation blind-spot: reflect.FieldByName of authz field names found "+
			"in production code — the archtest cannot see reflect-based field writes. "+
			"Refactor to use authzmutate.Mutator.Apply.")
}

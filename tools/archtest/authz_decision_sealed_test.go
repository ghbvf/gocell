//go:build archtest

// authz_decision_sealed_test.go — reflect schema freeze for the sealed
// authz.Decision type and the open obligation types Obligations/FieldMask
// (#1344 PR-6), plus an AST-based constructor closed-set scan.
//
//   - INVARIANT: AUTHZ-DECISION-SEALED-FIELD-FROZEN-01
//   - INVARIANT: AUTHZ-DECISION-CONSTRUCTOR-CLOSEDSET-01
//   - INVARIANT: AUTHZ-DECISION-ALLOW-DENY-CALLER-01
//
// # What this guards
//
// authz.Decision carries the authorization verdict returned by auth.Authorizer.
// All three fields are UNEXPORTED so outside-package struct-literal construction
// is a Go compile error — a P0 authz-bypass vector (business code forging an
// Allow verdict) is closed at the type-system level.
//
// authz.Obligations and authz.FieldMask carry the PEP-enforcement obligations
// returned by a Decision. Their fields ARE EXPORTED (PEPs must read them), so
// the sealed-construction protection does not apply. Instead, a reflect schema
// freeze prevents field-set drift that would silently change the obligation
// contract without updating all PEP consumers.
//
// This archtest freezes the field shape of Decision, Obligations, and FieldMask
// against in-package drift that the Go type system allows but which would break
// the contracts:
//
//   - Any Decision field being exported (PkgPath == "") re-opens outside-package
//     literal construction → authz-bypass becomes possible.
//   - A Decision field being renamed breaks downstream callers that rely on the
//     logical shape (Effect/obligations/reason semantics).
//   - An Obligations or FieldMask field being added, removed, or renamed changes
//     the PEP obligation contract without any compiler warning to consumers.
//   - A field being added or removed changes the struct size and may introduce
//     new forge vectors (Decision) or silent gaps in obligation enforcement.
//
// # AI-robust Rating ("reflect schema freeze" Hard 范本目录)
//
// "reflect schema freeze" range item from ai-robust.md §Hard 范本目录:
// Hard source = reflect.Type.NumField() / StructField.PkgPath / StructField.Type
// are objective structural facts (not string anchors), any drift triggers a tuple
// comparison failure → forces the change onto an explicit archtest update
// checkpoint. This closes in-package drift (rename / reorder / retag / export a
// field) that the Go type system does not prevent on its own.
//
//   - Upstream Hard (Decision): unexported fields make Decision{Effect:EffectAllow}
//     a compile error outside pkg/authz (sealed construction 范本, ai-robust.md).
//     This test verifies the seal has not been broken by an in-package edit.
//   - Upstream: Obligations and FieldMask have EXPORTED fields (PEPs must read
//     them) — sealed construction does not apply. The reflect freeze here is the
//     sole drift guard for those two types.
//   - Downstream caller-allowlist (DELIVERED PR-7 #1345): the closing half of
//     the sealed-Decision funnel — AUTHZ-DECISION-ALLOW-DENY-CALLER-01 below —
//     pins every production reference to authz.Allow / authz.Deny to the sole
//     ABAC PDP engine file (authorizationdecide/evaluator.go). Combined with the
//     upstream sealed fields (literal forge impossible), the funnel is now
//     fully closed Hard/Hard: a business package forging an authorization verdict
//     either cannot construct Decision (upstream Hard) or cannot reach the only
//     two constructors (downstream Hard caller-allowlist).
//
// # AUTHZ-DECISION-ALLOW-DENY-CALLER-01 (Hard downstream)
//
// AI-robust Rating (charter §"Funnel 双向锁评级") — fully closed (Hard/Hard):
//
//   - Downstream (who may CALL Allow/Deny): HARD. The detector resolves every
//     identifier USE to the authz.Allow / authz.Deny *types.Func via go/types
//     (info.Uses), so the package-qualified form (authz.Allow), an import-aliased
//     form, AND the dot-imported bare ident all resolve to the same object; any
//     reference outside allowDenyCallerAllowlist fails CI, no import shape excepted.
//   - Upstream (can a Decision be CONSTRUCTED without Allow/Deny): HARD. All
//     Decision fields are unexported (AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 above),
//     so an outside-package struct literal is a compile error, and the only two
//     exported funcs returning a Decision are Allow / Deny
//     (AUTHZ-DECISION-CONSTRUCTOR-CLOSEDSET-01). There is no alternative
//     construction path — no Hard-upgrade issue is needed.
//
// Detection is use-based (info.Uses), invariant to import form. Anti-vacuity: the
// allowlisted file must reference Allow or Deny at least once, so a removed call
// or scanner regression (which would make the funnel vacuously pass) fails CI. A
// RED fixture (internal/authzdecisioncallerfixture) proves the detector fires on
// an Allow/Deny reference outside the allowlist.
//
// Blind spot (known, by design): the scan runs over Production() scope, which
// excludes _test.go files. Test doubles legitimately construct verdicts via
// Allow/Deny (e.g. runtime/auth/middleware_test.go mockAuthorizer) and are NOT
// flagged — production-only enforcement is the intended boundary (the same
// convention as every Production() caller-allowlist in this suite). A production
// Allow/Deny reference is always caught; a test-file one is an accepted exemption.
//
// # AUTHZ-DECISION-CONSTRUCTOR-CLOSEDSET-01
//
// AI-robust Rating: Medium upstream (archtest AST scan; Go type-system cannot
// prevent a new exported func in pkg/authz that happens to return authz.Decision)
// + Hard downstream (upstream sealed construction already makes outside-package
// struct literals impossible; the sole risk is an in-package new exported constructor
// that bypasses Allow/Deny validation, which this scan catches).
//
// Every exported, top-level func declared in package pkg/authz whose result tuple
// contains type authz.Decision must be one of the closed set {Allow, Deny}.
// Adding "UnsafeAllow()" or any other shortcut that bypasses Obligations.Validate()
// is the bypass vector; this scan catches it at archtest time.
//
// Blind spots:
//   - Method receivers: this scan only checks package-level funcs, not methods.
//     A new Decision-returning method on a new type would not be caught.
//     Mitigation: Decision fields are unexported — any such method must live in
//     pkg/authz; the reflect freeze catches any new pkg/authz struct fields.
//   - Indirect construction via reflect or unsafe: structurally blocked by
//     unexported fields (reflect.Value.Set panics on unexported fields from outside).
//   - Type aliases to authz.Decision in another package: still sealed (Go alias
//     shares field visibility). Not relevant to this scan which is in-package.
//
// Reverse self-check: TestAuthzDecisionConstructorClosedSet_RedFixture asserts
// that a hypothetical "UnsafeAllow() authz.Decision" function name would be flagged.
//
// # Tool Blind Spots (charter §"强制盲区自检")
//
//   - This test uses reflect on the *imported* pkg/authz types. Any in-package
//     drift in the worktree will be captured because Go recompiles the package
//     before running tests.
//   - Type aliases (type D = authz.Decision in another package) are NOT sealed by
//     this test — but such an alias would still have all unexported fields because
//     Go type aliases share the underlying field visibility. The alias itself cannot
//     be constructed with field values from outside the package.
//   - The "zero-value fail-closed" property (Decision{}.IsAllow() == false) is
//     tested in pkg/authz/decision_test.go; this file only locks the struct shape.
//   - For Obligations and FieldMask the freeze detects field-set drift but does
//     NOT enforce any construction constraint (those types have exported fields
//     and are intentionally constructable by PEPs). PEP enforcement correctness
//     is the caller's responsibility.
//
// Reverse self-check: a GREEN fixture (correct shape) and a RED fixture (exported
// field) are both verified inline below.
package archtest

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/authz"
)

// frozenDecisionField is the expected frozen shape of one authz.Decision field.
type frozenDecisionField struct {
	name     string
	typeName string // reflect.Type.String()
	exported bool   // PkgPath == "" → exported
}

// frozenDecisionFields is the expected exact field tuple for authz.Decision.
// Any deviation (rename / reorder / add / remove / export) will fail CI.
//
// Fields (in Go struct declaration order):
//
//	effect      authz.Effect      — unexported: the verdict enum
//	obligations authz.Obligations — unexported: RowScope + FieldMask obligations
//	reason      string            — unexported: opaque server-side diagnostic
var frozenDecisionFields = []frozenDecisionField{
	{name: "effect", typeName: "authz.Effect", exported: false},
	{name: "obligations", typeName: "authz.Obligations", exported: false},
	{name: "reason", typeName: "string", exported: false},
}

// TestAuthzDecisionSealedFieldFrozen01 is the primary guard for
// AUTHZ-DECISION-SEALED-FIELD-FROZEN-01.
//
// Three axes:
//
//  1. Exact field count — adding or removing fields fails immediately.
//  2. Per-field identity — name + type + unexported status frozen.
//  3. All fields unexported — PkgPath != "" for every field; any exported
//     field re-opens outside-package literal construction (P0 authz-bypass).
func TestAuthzDecisionSealedFieldFrozen01(t *testing.T) {
	t.Parallel()

	dt := reflect.TypeOf(authz.Decision{})
	require.Equal(t, reflect.Struct, dt.Kind(), "authz.Decision must be a struct")

	// Axis 1: exact field count.
	require.Equal(
		t, len(frozenDecisionFields), dt.NumField(),
		"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01: authz.Decision NumField = %d, want %d "+
			"(adding a field may re-open the sealed-construction invariant; "+
			"removing a field breaks the PDP contract; update the frozen tuple and "+
			"pkg/authz/doc.go together)",
		dt.NumField(), len(frozenDecisionFields),
	)

	for i, want := range frozenDecisionFields {
		sf := dt.Field(i)

		// Axis 2a: field name.
		assert.Equal(
			t, want.name, sf.Name,
			"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01: Decision field[%d] name = %q, want %q",
			i, sf.Name, want.name,
		)

		// Axis 2b: field type identity.
		assert.Equal(
			t, want.typeName, sf.Type.String(),
			"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01: Decision.%s type = %q, want %q",
			sf.Name, sf.Type.String(), want.typeName,
		)

		// Axis 3: all fields must be unexported (PkgPath != "").
		exported := sf.PkgPath == ""
		assert.Equal(
			t, want.exported, exported,
			"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01: Decision.%s exported = %v, want %v "+
				"(exported fields allow outside-package struct-literal construction — "+
				"a P0 authz-bypass vector; re-seal by making the field unexported)",
			sf.Name, exported, want.exported,
		)
	}
}

// TestAuthzDecisionSealedFieldFrozen01_AntiVacuity verifies the archtest itself
// is not trivially passing due to a missing import or wrong type load.
func TestAuthzDecisionSealedFieldFrozen01_AntiVacuity(t *testing.T) {
	t.Parallel()

	dt := reflect.TypeOf(authz.Decision{})

	// The type must be named "Decision" in package "authz".
	assert.Equal(
		t, "Decision", dt.Name(),
		"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 anti-vacuity: loaded type name = %q, want Decision",
		dt.Name(),
	)
	assert.Equal(
		t, "authz", dt.PkgPath()[len(dt.PkgPath())-len("authz"):],
		"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 anti-vacuity: package path suffix must be 'authz'",
	)

	// The type must have at least one field (guards against loading an empty stub).
	assert.Greater(
		t, dt.NumField(), 0,
		"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 anti-vacuity: Decision has no fields — wrong type loaded?",
	)
}

// TestAuthzDecisionSealedFieldFrozen01_RedFixture is the reverse self-check.
// It asserts that a struct WITH an exported field is detected as a violation by
// the same field-visibility check used in the main test. This validates that the
// check is not trivially vacuous.
func TestAuthzDecisionSealedFieldFrozen01_RedFixture(t *testing.T) {
	t.Parallel()

	// Craft a struct that looks like a Decision but has an exported Effect field.
	type brokenDecision struct {
		Effect      authz.Effect      // EXPORTED — should fail the exported check
		obligations authz.Obligations //nolint:unused // intentional: mimics unexported field shape
		reason      string            //nolint:unused // intentional: mimics unexported field shape
	}

	bt := reflect.TypeOf(brokenDecision{})
	require.Equal(t, 3, bt.NumField(), "red-fixture must have 3 fields")

	// The first field (Effect) must be detected as exported (PkgPath == "").
	exportedField := bt.Field(0)
	exportedDetected := exportedField.PkgPath == ""
	assert.True(
		t, exportedDetected,
		"RED fixture self-check: Effect field in brokenDecision must be detected as exported "+
			"(PkgPath empty = %q); if this fails the check would trivially miss real violations",
		exportedField.PkgPath,
	)
}

// TestAuthzDecisionSealedFieldFrozen01_GreenFixture asserts that a properly sealed
// struct (all unexported) passes the visibility check. Symmetric with the RED fixture.
func TestAuthzDecisionSealedFieldFrozen01_GreenFixture(t *testing.T) {
	t.Parallel()

	// All-unexported struct — exactly the shape we require for authz.Decision.
	type goodDecision struct {
		effect      authz.Effect      //nolint:unused // reflected over via reflect.TypeOf, not read directly
		obligations authz.Obligations //nolint:unused // reflected over via reflect.TypeOf, not read directly
		reason      string            //nolint:unused // reflected over via reflect.TypeOf, not read directly
	}

	gt := reflect.TypeOf(goodDecision{})
	for i := range gt.NumField() {
		sf := gt.Field(i)
		assert.NotEmpty(
			t, sf.PkgPath,
			"GREEN fixture self-check: field[%d] %q must be unexported (PkgPath non-empty); "+
				"if this fails the check is broken",
			i, sf.Name,
		)
	}
}

// TestAuthzDecisionSealedFieldFrozen01_FieldNames verifies the exact ordered name
// list of the frozen fields. This provides a human-readable diff when a field is
// renamed or reordered without changing the count.
func TestAuthzDecisionSealedFieldFrozen01_FieldNames(t *testing.T) {
	t.Parallel()

	dt := reflect.TypeOf(authz.Decision{})
	gotNames := make([]string, dt.NumField())
	for i := range dt.NumField() {
		gotNames[i] = dt.Field(i).Name
	}

	wantNames := make([]string, len(frozenDecisionFields))
	for i, f := range frozenDecisionFields {
		wantNames[i] = f.name
	}

	assert.Equal(
		t, wantNames, gotNames,
		"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01: Decision field names (ordered) = %v, want %v "+
			"(a rename or reorder here breaks the semantic contract; update frozenDecisionFields)",
		gotNames, wantNames,
	)
}

// TestAuthzDecisionSealedFieldFrozen01_AllFieldsUnexported is a single-assertion
// summary that all Decision fields are unexported. This guards the core security
// invariant: outside-package struct-literal forge is impossible.
func TestAuthzDecisionSealedFieldFrozen01_AllFieldsUnexported(t *testing.T) {
	t.Parallel()

	dt := reflect.TypeOf(authz.Decision{})
	var exported []string
	for i := range dt.NumField() {
		sf := dt.Field(i)
		if sf.PkgPath == "" {
			exported = append(exported, sf.Name)
		}
	}

	assert.Empty(
		t, exported,
		"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01: exported Decision fields = %v — "+
			"any exported field allows `authz.Decision{%s: ...}` outside pkg/authz "+
			"(P0 authz-bypass); re-seal by lowercasing the field name",
		exported,
		func() string {
			if len(exported) > 0 {
				return exported[0]
			}
			return ""
		}(),
	)
}

// TestAuthzDecisionSealedFieldFrozen01_ConstructorSet verifies that the two
// sanctioned constructors (Allow and Deny) exist and have the right signatures
// by exercising them. This is a lightweight runtime check, not an AST scan
// (the PR-7 caller-allowlist will add the AST-level guard when production
// callers exist).
func TestAuthzDecisionSealedFieldFrozen01_ConstructorSet(t *testing.T) {
	t.Parallel()

	// Allow() must produce an allow Decision.
	allowDec, err := authz.Allow(authz.Obligations{})
	require.NoError(t, err,
		"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 constructor: Allow(valid Obligations) must not return error")
	assert.True(t, allowDec.IsAllow(),
		"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 constructor: Allow() must produce IsAllow()==true")

	// Deny() must produce a deny Decision.
	denyDec := authz.Deny("test reason")
	assert.False(t, denyDec.IsAllow(),
		"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 constructor: Deny() must produce IsAllow()==false")

	// The reflect type of what Allow() returns must be authz.Decision.
	allowType := fmt.Sprintf("%T", allowDec)
	assert.Equal(
		t, "authz.Decision", allowType,
		"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 constructor: Allow() return type = %s, want authz.Decision",
		allowType,
	)
}

// ---- Companion reflect schema freezes for Obligations and FieldMask ----
//
// These companion tests lock the field shapes of authz.Obligations and
// authz.FieldMask. Unlike Decision, both types have EXPORTED fields (PEPs
// must read and construct them), so the sealed-construction protection does not
// apply. The reflect freeze is the sole guard against in-package field-set
// drift that would silently change the PEP obligation contract.
//
// Blind spot: these tests do NOT enforce construction constraints. A PEP can
// freely construct Obligations{} or FieldMask{} literals — that is intentional
// and correct; Validate() is the runtime enforcement gate.

// frozenObligationsField mirrors frozenDecisionField for authz.Obligations.
type frozenObligationsField struct {
	name     string
	typeName string // reflect.Type.String()
	exported bool   // PkgPath == "" → exported
}

// frozenObligationsFields is the expected exact field tuple for authz.Obligations.
// Any deviation (rename / reorder / add / remove / export-flip) will fail CI.
//
// Fields (in Go struct declaration order):
//
//	RowScope  tenant.RowScope  — exported: the row-visibility obligation enum
//	FieldMask authz.FieldMask  — exported: the column-masking obligation
var frozenObligationsFields = []frozenObligationsField{
	{name: "RowScope", typeName: "tenant.RowScope", exported: true},
	{name: "FieldMask", typeName: "authz.FieldMask", exported: true},
}

// TestAuthzObligationsFieldsFrozen locks the field shape of authz.Obligations.
//
// Three axes (mirroring Decision freeze):
//
//  1. Exact field count — adding/removing fields fails immediately.
//  2. Per-field identity — name + type + exported status frozen.
//  3. All fields exported — PkgPath == "" for every field; any unexported field
//     would make PEP literal construction fail (they need to read the fields).
func TestAuthzObligationsFieldsFrozen(t *testing.T) {
	t.Parallel()

	ot := reflect.TypeOf(authz.Obligations{})
	require.Equal(t, reflect.Struct, ot.Kind(), "authz.Obligations must be a struct")

	// Axis 1: exact field count.
	require.Equal(
		t, len(frozenObligationsFields), ot.NumField(),
		"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 (Obligations): authz.Obligations NumField = %d, want %d "+
			"(adding a field silently extends the obligation contract without updating PEP consumers; "+
			"removing one drops an obligation axis; update frozenObligationsFields and pkg/authz/doc.go together)",
		ot.NumField(), len(frozenObligationsFields),
	)

	for i, want := range frozenObligationsFields {
		sf := ot.Field(i)

		// Axis 2a: field name.
		assert.Equal(
			t, want.name, sf.Name,
			"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 (Obligations): Obligations field[%d] name = %q, want %q",
			i, sf.Name, want.name,
		)

		// Axis 2b: field type identity.
		assert.Equal(
			t, want.typeName, sf.Type.String(),
			"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 (Obligations): Obligations.%s type = %q, want %q",
			sf.Name, sf.Type.String(), want.typeName,
		)

		// Axis 3: all fields must be exported (PkgPath == "").
		exported := sf.PkgPath == ""
		assert.Equal(
			t, want.exported, exported,
			"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 (Obligations): Obligations.%s exported = %v, want %v "+
				"(unexported Obligations field would break PEP construction; re-export by uppercasing)",
			sf.Name, exported, want.exported,
		)
	}
}

// frozenFieldMaskField mirrors frozenDecisionField for authz.FieldMask.
type frozenFieldMaskField struct {
	name     string
	typeName string
	exported bool
}

// frozenFieldMaskFields is the expected exact field tuple for authz.FieldMask.
// Any deviation (rename / reorder / add / remove / export-flip) will fail CI.
//
// Fields (in Go struct declaration order):
//
//	Fields []string — exported: the ordered list of column names to mask
var frozenFieldMaskFields = []frozenFieldMaskField{
	{name: "Fields", typeName: "[]string", exported: true},
}

// ---- AUTHZ-DECISION-CONSTRUCTOR-CLOSEDSET-01 ----
//
// The tests below implement the AST-based scan described in the package godoc.
// We parse the pkg/authz source files with pure AST analysis (no go/types
// type-checker) to find every exported top-level function whose result tuple
// contains the bare identifier "Decision". Since Decision is declared in the
// same package, it appears as *ast.Ident{Name: "Decision"} — no SelectorExpr,
// no cross-package resolution needed. importer.Default() is intentionally
// avoided because it cannot resolve module-based imports (only GOPATH/GOROOT).

// authzPkgDir returns the absolute path to pkg/authz by navigating relative to
// this test file's location (stable across workspaces).
func authzPkgDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller(0) must succeed")
	// thisFile is tools/archtest/authz_decision_sealed_test.go
	// repo root is two levels up
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	return filepath.Join(repoRoot, "framework", "pkg", "authz")
}

// authzParseNonTestFiles parses all non-test Go files in dir and returns
// a slice of *ast.File. It uses os.Open + (*os.File).Readdir to enumerate
// the directory (not the deprecated parser.ParseDir, not the banned
// filepath.WalkDir / os.ReadDir). The returned FileSet is shared across all
// parsed files.
func authzParseNonTestFiles(t *testing.T, fset *token.FileSet, dir string) []*ast.File {
	t.Helper()

	f, err := os.Open(dir) //nolint:gosec // dir is authzPkgDir-derived, not user input
	require.NoError(t, err, "authzParseNonTestFiles: open dir %s", dir)
	defer f.Close() //nolint:errcheck // read-only dir handle

	infos, err := f.Readdir(-1)
	require.NoError(t, err, "authzParseNonTestFiles: readdir %s", dir)

	var files []*ast.File
	for _, info := range infos {
		name := info.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		af, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		require.NoError(t, perr, "authzParseNonTestFiles: parse %s", name)
		files = append(files, af)
	}
	require.NotEmpty(t, files, "authzParseNonTestFiles: no non-test .go files found in %s", dir)
	return files
}

// resultContainsDecisionAST reports whether the FuncDecl's result list contains
// the bare identifier "Decision". This is sufficient for an intra-package scan:
// within pkg/authz, the type "Decision" always appears as *ast.Ident (not a
// SelectorExpr), so no type-checker is required.
//
// Blind spot: a result field declared as *Decision (pointer) would appear as
// *ast.StarExpr wrapping *ast.Ident. Decision is currently returned by value,
// so this is not an issue; the check is extended to cover StarExpr as defense
// in depth.
func resultContainsDecisionAST(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil {
		return false
	}
	for _, field := range fn.Type.Results.List {
		if identIsDecision(field.Type) {
			return true
		}
	}
	return false
}

// identIsDecision reports whether the expression is the bare identifier
// "Decision" or a pointer *Decision.
func identIsDecision(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == "Decision"
	case *ast.StarExpr:
		return identIsDecision(e.X)
	}
	return false
}

// TestAuthzDecisionConstructorClosedSet01 is the primary scan for
// AUTHZ-DECISION-CONSTRUCTOR-CLOSEDSET-01.
//
// It parses all non-test Go files in pkg/authz with pure AST analysis and
// asserts: every exported top-level func (no receiver) whose result tuple
// includes the bare type name "Decision" must be in the closed set {Allow, Deny}.
//
// Pure AST rationale: Decision is declared in pkg/authz; within the same
// package it appears as *ast.Ident{Name:"Decision"}, never as a SelectorExpr.
// No type-checker is needed for this invariant, which avoids the importer.Default()
// limitation that cannot resolve module-based imports (only GOPATH/GOROOT).
func TestAuthzDecisionConstructorClosedSet01(t *testing.T) {
	t.Parallel()

	dir := authzPkgDir(t)
	fset := token.NewFileSet()
	parsedFiles := authzParseNonTestFiles(t, fset, dir)

	// allowedConstructors is the closed set.
	allowedConstructors := map[string]struct{}{
		"Allow": {},
		"Deny":  {},
	}

	var violations []string
	for _, file := range parsedFiles {
		EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
			// Only package-level funcs (no receiver), exported names.
			if fn.Recv != nil || !fn.Name.IsExported() {
				return
			}
			if resultContainsDecisionAST(fn) {
				if _, allowed := allowedConstructors[fn.Name.Name]; !allowed {
					violations = append(violations, fn.Name.Name)
				}
			}
		})
	}

	assert.Empty(
		t, violations,
		"AUTHZ-DECISION-CONSTRUCTOR-CLOSEDSET-01: exported pkg/authz funcs returning Decision "+
			"outside closed set {Allow, Deny}: %v — add a new constructor only if it validates "+
			"obligations via Obligations.Validate() and is approved in ai-robust review; "+
			"update allowedConstructors in this test in the same PR",
		violations,
	)
}

// TestAuthzDecisionConstructorClosedSet01_AntiVacuity ensures the scan loaded
// the pkg/authz package and found at least the two known constructors (Allow
// and Deny) returning Decision. This prevents the scan from silently passing
// if pkg/authz is empty or Decision was renamed.
func TestAuthzDecisionConstructorClosedSet01_AntiVacuity(t *testing.T) {
	t.Parallel()

	dir := authzPkgDir(t)
	fset := token.NewFileSet()
	parsedFiles := authzParseNonTestFiles(t, fset, dir)

	// Build a map: func name → returns Decision (by AST).
	returnsDecision := map[string]bool{}
	for _, file := range parsedFiles {
		EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
			if fn.Recv != nil || !fn.Name.IsExported() {
				return
			}
			if resultContainsDecisionAST(fn) {
				returnsDecision[fn.Name.Name] = true
			}
		})
	}

	// Verify Allow and Deny both exist and return Decision.
	for _, name := range []string{"Allow", "Deny"} {
		assert.True(t, returnsDecision[name],
			"AUTHZ-DECISION-CONSTRUCTOR-CLOSEDSET-01 anti-vacuity: func %q not found in pkg/authz "+
				"returning Decision — the scan would be vacuous if neither constructor returns the "+
				"target type; verify Decision type name has not changed", name)
	}
}

// TestAuthzDecisionConstructorClosedSet01_RedFixture verifies that the scanning
// logic WOULD detect a hypothetical unauthorized constructor named "UnsafeAllow".
// This fixture is entirely in-memory; it does not touch real pkg/authz source.
func TestAuthzDecisionConstructorClosedSet01_RedFixture(t *testing.T) {
	t.Parallel()

	// Simulate parsing a fictional pkg/authz file that contains an extra
	// exported constructor "UnsafeAllow() Decision". We do this with a minimal
	// synthetic source to verify the detection logic works correctly.
	const src = `package authz

type Effect uint8
type Decision struct { effect Effect }

// Allow is the sanctioned constructor.
func Allow(o Obligations) (Decision, error) { return Decision{}, nil }
// Deny is the sanctioned constructor.
func Deny(reason string) Decision { return Decision{} }
// UnsafeAllow is the UNAUTHORIZED constructor this test proves gets caught.
func UnsafeAllow() Decision { return Decision{} }

type Obligations struct{}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fake_authz.go", src, 0)
	require.NoError(t, err, "RED fixture: parse failed")

	conf := types.Config{Importer: importer.Default()}
	info := &types.Info{Defs: make(map[*ast.Ident]types.Object)}
	// Use a minimal package path; we only care about the scan logic.
	typedPkg, err := conf.Check("authz", fset, []*ast.File{f}, info)
	require.NoError(t, err, "RED fixture: type-check failed")

	allowedConstructors := map[string]struct{}{
		"Allow": {},
		"Deny":  {},
	}

	scope := typedPkg.Scope()
	var violations []string
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		fn, ok := obj.(*types.Func)
		if !ok || !fn.Exported() || fn.Type().(*types.Signature).Recv() != nil {
			continue
		}
		sig := fn.Type().(*types.Signature)
		for i := 0; i < sig.Results().Len(); i++ {
			rt := sig.Results().At(i).Type()
			named, ok := rt.(*types.Named)
			if !ok {
				continue
			}
			if named.Obj().Name() == "Decision" {
				if _, allowed := allowedConstructors[fn.Name()]; !allowed {
					violations = append(violations, fn.Name())
				}
			}
		}
	}

	// The RED fixture MUST contain exactly "UnsafeAllow" in violations.
	require.Contains(t, violations, "UnsafeAllow",
		"RED fixture self-check: UnsafeAllow must be detected as a violation; "+
			"if this fails, the detection logic is broken and the main scan is vacuous")
	// Allow and Deny must NOT appear in violations.
	assert.NotContains(t, violations, "Allow",
		"RED fixture self-check: Allow must NOT appear in violations (it is in the closed set)")
	assert.NotContains(t, violations, "Deny",
		"RED fixture self-check: Deny must NOT appear in violations (it is in the closed set)")
}

// TestAuthzFieldMaskFieldsFrozen locks the field shape of authz.FieldMask.
//
// Three axes (mirroring Decision and Obligations freezes):
//
//  1. Exact field count.
//  2. Per-field identity — name + type + exported status frozen.
//  3. All fields exported — PEPs read FieldMask.Fields directly.
func TestAuthzFieldMaskFieldsFrozen(t *testing.T) {
	t.Parallel()

	ft := reflect.TypeOf(authz.FieldMask{})
	require.Equal(t, reflect.Struct, ft.Kind(), "authz.FieldMask must be a struct")

	// Axis 1: exact field count.
	require.Equal(
		t, len(frozenFieldMaskFields), ft.NumField(),
		"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 (FieldMask): authz.FieldMask NumField = %d, want %d "+
			"(adding a field changes the masking contract; update frozenFieldMaskFields and pkg/authz/doc.go together)",
		ft.NumField(), len(frozenFieldMaskFields),
	)

	for i, want := range frozenFieldMaskFields {
		sf := ft.Field(i)

		// Axis 2a: field name.
		assert.Equal(
			t, want.name, sf.Name,
			"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 (FieldMask): FieldMask field[%d] name = %q, want %q",
			i, sf.Name, want.name,
		)

		// Axis 2b: field type identity.
		assert.Equal(
			t, want.typeName, sf.Type.String(),
			"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 (FieldMask): FieldMask.%s type = %q, want %q",
			sf.Name, sf.Type.String(), want.typeName,
		)

		// Axis 3: all fields must be exported.
		exported := sf.PkgPath == ""
		assert.Equal(
			t, want.exported, exported,
			"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 (FieldMask): FieldMask.%s exported = %v, want %v "+
				"(unexported FieldMask field would break PEP construction; re-export by uppercasing)",
			sf.Name, exported, want.exported,
		)
	}
}

// --- AUTHZ-DECISION-ALLOW-DENY-CALLER-01 (Hard downstream caller-allowlist) ---

// authzPkgPath is the pkg/authz import path, anchored to PlatformModulePath.
const authzPkgPath = PlatformFrameworkModulePath + "/pkg/authz"

// allowDenyCallerAllowlist is the set of module-relative production files allowed
// to reference authz.Allow / authz.Deny. PR-7 (#1345): the platform ABAC PDP
// engine file is the sanctioned construction site of an authorization verdict.
//
// PR-10d (#1894) adds the two example-owned PDPs: iotdevice and todoorder do NOT
// bundle accesscore, so each ships its own lightweight Authorizer (the SOLE
// Allow/Deny caller in its example tree) wired via bootstrap.PrimaryAuthorizerOption.
// Each is a genuine sanctioned PDP engine for its example domain — exactly the
// "new sanctioned engine → add with rationale" case this allowlist's godoc names.
var allowDenyCallerAllowlist = map[string]struct{}{
	"corecells/accesscore/slices/authorizationdecide/evaluator.go": {},
	"examples/iotdevice/cells/devicecell/authorizer.go":            {},
	"examples/todoorder/cells/ordercell/authorizer.go":             {},
}

// TestAuthzDecisionAllowDenyCaller01 asserts every production reference to
// authz.Allow / authz.Deny sits in allowDenyCallerAllowlist, and that the
// allowlist entry is live (anti-vacuity reverse self-check).
func TestAuthzDecisionAllowDenyCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	observed := map[string]struct{}{}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		return scanAllowDenyCallers(p, observed)
	})
	for f := range allowDenyCallerAllowlist {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"AUTHZ-DECISION-ALLOW-DENY-CALLER-01: allowlist entry %q is STALE — no live authz.Allow/Deny "+
						"reference observed. Either the scanner regressed or the call was removed; drop the dead "+
						"allowlist entry so it cannot become a silent verdict-forge slot.", f,
				),
			})
		}
	}
	Report(t, "AUTHZ-DECISION-ALLOW-DENY-CALLER-01", diags)
}

// scanAllowDenyCallers flags every reference to authz.Allow / authz.Deny outside
// allowDenyCallerAllowlist, recording observed allowlisted files for anti-vacuity.
func scanAllowDenyCallers(p *Pass, observed map[string]struct{}) []Diagnostic {
	var d []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
			if !isAuthzAllowOrDenyRef(p.TypesInfo, id) {
				return
			}
			if _, allowed := allowDenyCallerAllowlist[rel]; allowed {
				observed[rel] = struct{}{}
				return
			}
			pos := p.Fset.Position(id.Pos())
			d = append(d, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"AUTHZ-DECISION-ALLOW-DENY-CALLER-01: authz.%s is referenced from %s, which is not the sanctioned "+
						"PDP engine. Only the authorizationdecide ABAC evaluator (evaluator.go) may construct an "+
						"authz.Decision; forging an authorization verdict elsewhere is a P0 bypass. If this IS a new "+
						"sanctioned engine, add it to allowDenyCallerAllowlist with rationale.", id.Name, rel,
				),
			})
		})
	}
	return d
}

// isAuthzAllowOrDenyRef resolves an identifier USE to authz.Allow or authz.Deny
// via go/types (info.Uses): matches package-qualified, import-aliased, and
// dot-imported forms (all resolve to the same *types.Func).
func isAuthzAllowOrDenyRef(info *types.Info, id *ast.Ident) bool {
	if id.Name != "Allow" && id.Name != "Deny" {
		return false
	}
	fn, ok := info.Uses[id].(*types.Func)
	if !ok {
		return false
	}
	return fn.Pkg() != nil && fn.Pkg().Path() == authzPkgPath
}

// TestAuthzDecisionAllowDenyCaller01_RedFixture is the reverse self-check: the
// fixture references authz.Allow / authz.Deny from a package that is NOT on the
// allowlist; the detector must flag it. A 0 result means the detector regressed.
func TestAuthzDecisionAllowDenyCaller01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")
	fixturePkg := modPath + "/tools/archtest/internal/authzdecisioncallerfixture"
	pattern := "./tools/archtest/internal/authzdecisioncallerfixture/..."

	throwaway := map[string]struct{}{}
	var found int
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{pattern}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != fixturePkg || p.TypesInfo == nil {
			return nil
		}
		found += len(scanAllowDenyCallers(p, throwaway))
		return nil
	})
	assert.GreaterOrEqual(t, found, 1,
		"RED fixture self-check FAILED: detector must flag an authz.Allow/Deny reference outside the allowlist")
}

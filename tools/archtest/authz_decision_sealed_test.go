// authz_decision_sealed_test.go — reflect schema freeze for the sealed
// authz.Decision type and the open obligation types Obligations/FieldMask
// (#1344 PR-6).
//
//   - INVARIANT: AUTHZ-DECISION-SEALED-FIELD-FROZEN-01
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
//   - Downstream caller-allowlist: Allow()/Deny() today have zero production
//     callers; the allowlist lands in PR-7 (#1345, gh issue tracking). This is
//     explicitly deferred as stated in pkg/authz/doc.go per ai-robust.md
//     §"Funnel 双向锁评级": the Medium-upstream → Hard-downstream transitional
//     funnel must name its Hard-ization tracker, which is gh #1345 (PR-7).
//     Once PR-7 ships a production Allow()/Deny() caller, the companion archtest
//     must be extended with an AST caller-allowlist guard (only the
//     authorizationdecide engine may call Allow() or Deny()).
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
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/authz"
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
	require.Equal(t, len(frozenDecisionFields), dt.NumField(),
		"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01: authz.Decision NumField = %d, want %d "+
			"(adding a field may re-open the sealed-construction invariant; "+
			"removing a field breaks the PDP contract; update the frozen tuple and "+
			"pkg/authz/doc.go together)",
		dt.NumField(), len(frozenDecisionFields),
	)

	for i, want := range frozenDecisionFields {
		sf := dt.Field(i)

		// Axis 2a: field name.
		assert.Equal(t, want.name, sf.Name,
			"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01: Decision field[%d] name = %q, want %q",
			i, sf.Name, want.name,
		)

		// Axis 2b: field type identity.
		assert.Equal(t, want.typeName, sf.Type.String(),
			"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01: Decision.%s type = %q, want %q",
			sf.Name, sf.Type.String(), want.typeName,
		)

		// Axis 3: all fields must be unexported (PkgPath != "").
		exported := sf.PkgPath == ""
		assert.Equal(t, want.exported, exported,
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
	assert.Equal(t, "Decision", dt.Name(),
		"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 anti-vacuity: loaded type name = %q, want Decision",
		dt.Name(),
	)
	assert.Equal(t, "authz", dt.PkgPath()[len(dt.PkgPath())-len("authz"):],
		"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 anti-vacuity: package path suffix must be 'authz'",
	)

	// The type must have at least one field (guards against loading an empty stub).
	assert.Greater(t, dt.NumField(), 0,
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
	assert.True(t, exportedDetected,
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
		assert.NotEmpty(t, sf.PkgPath,
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

	assert.Equal(t, wantNames, gotNames,
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

	assert.Empty(t, exported,
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
	allowDec := authz.Allow(authz.Obligations{})
	assert.True(t, allowDec.IsAllow(),
		"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 constructor: Allow() must produce IsAllow()==true")

	// Deny() must produce a deny Decision.
	denyDec := authz.Deny("test reason")
	assert.False(t, denyDec.IsAllow(),
		"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 constructor: Deny() must produce IsAllow()==false")

	// The reflect type of what Allow() returns must be authz.Decision.
	allowType := fmt.Sprintf("%T", allowDec)
	assert.Equal(t, "authz.Decision", allowType,
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
	require.Equal(t, len(frozenObligationsFields), ot.NumField(),
		"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 (Obligations): authz.Obligations NumField = %d, want %d "+
			"(adding a field silently extends the obligation contract without updating PEP consumers; "+
			"removing one drops an obligation axis; update frozenObligationsFields and pkg/authz/doc.go together)",
		ot.NumField(), len(frozenObligationsFields),
	)

	for i, want := range frozenObligationsFields {
		sf := ot.Field(i)

		// Axis 2a: field name.
		assert.Equal(t, want.name, sf.Name,
			"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 (Obligations): Obligations field[%d] name = %q, want %q",
			i, sf.Name, want.name,
		)

		// Axis 2b: field type identity.
		assert.Equal(t, want.typeName, sf.Type.String(),
			"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 (Obligations): Obligations.%s type = %q, want %q",
			sf.Name, sf.Type.String(), want.typeName,
		)

		// Axis 3: all fields must be exported (PkgPath == "").
		exported := sf.PkgPath == ""
		assert.Equal(t, want.exported, exported,
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
	require.Equal(t, len(frozenFieldMaskFields), ft.NumField(),
		"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 (FieldMask): authz.FieldMask NumField = %d, want %d "+
			"(adding a field changes the masking contract; update frozenFieldMaskFields and pkg/authz/doc.go together)",
		ft.NumField(), len(frozenFieldMaskFields),
	)

	for i, want := range frozenFieldMaskFields {
		sf := ft.Field(i)

		// Axis 2a: field name.
		assert.Equal(t, want.name, sf.Name,
			"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 (FieldMask): FieldMask field[%d] name = %q, want %q",
			i, sf.Name, want.name,
		)

		// Axis 2b: field type identity.
		assert.Equal(t, want.typeName, sf.Type.String(),
			"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 (FieldMask): FieldMask.%s type = %q, want %q",
			sf.Name, sf.Type.String(), want.typeName,
		)

		// Axis 3: all fields must be exported.
		exported := sf.PkgPath == ""
		assert.Equal(t, want.exported, exported,
			"AUTHZ-DECISION-SEALED-FIELD-FROZEN-01 (FieldMask): FieldMask.%s exported = %v, want %v "+
				"(unexported FieldMask field would break PEP construction; re-export by uppercasing)",
			sf.Name, exported, want.exported,
		)
	}
}

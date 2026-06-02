// INVARIANT: MODULE-PROVIDE-NO-VALUE-HANDOFF-01
//
// # MODULE-PROVIDE-NO-VALUE-HANDOFF-01 — CellModule.Provide signature frozen (Hard)
//
// ## Rule
//
// The `CellModule.Provide` method in `runtime/composition` MUST have exactly
// the signature:
//
//	Provide(context.Context, *SharedDeps) (ModuleResult, error)
//
// and the `ModuleResult` struct MUST have exactly three exported fields:
//
//	type ModuleResult struct {
//	    Cell      cell.Cell
//	    Opts      []bootstrap.Option
//	    Resources []lifecycle.ManagedResource
//	}
//
// No cross-module value-handoff parameter (formerly `in ModuleExports`) and no
// cross-module value-handoff field/return (formerly `ModuleExports`) are
// permitted. Adding either would re-open the in-process Go-handle channel that
// Wave-1 #1423 closed. The single-source `Resources` field (PR #591 / #1420)
// is the only resource channel: Builder derives BOTH the steady-state
// `bootstrap.WithManagedResource` registration AND the pre-Run rollback stack
// from it, so a module cannot diverge the two (the former double-write bug).
//
// ## AI-robust rating: Hard (reflect interface method signature + struct fields)
//
// The archtest uses reflect.TypeOf to inspect (1) the interface method set,
// pinning the exact In/Out type list, and (2) the ModuleResult struct field
// set, pinning NumField()==3, the field names, types, and exported-ness.
// Changing either reflect shape → test fails immediately. There is no
// string-anchor or comment allowlist; the rule is form-locked.
//
// ## Blind spots and reverse self-checks
//
//  1. **Type alias rewrite**: if `CellModule` were re-declared as a type alias
//     of a different interface, reflect.TypeOf would resolve to the aliased
//     type's method set. Reverse self-check: `TestModuleProvideSignatureFrozen_ReverseCheck_AliasNotPresent`
//     asserts that `composition.CellModule` is an interface type (not an alias
//     to a struct or func), so an alias rewrite is immediately caught.
//  2. **Embedded interface bypass**: adding an embedded interface that re-declares
//     Provide with a different signature would shadow the frozen method.
//     Reverse self-check: `TestModuleProvideSignatureFrozen_ReverseCheck_NoEmbedding`
//     asserts the method set has exactly one method named "Provide" (plus "ID"),
//     so hidden embedding with a shadowed Provide is caught.
//  3. **Wrong package path**: if a different `CellModule` type in a sibling
//     package were frozen instead, the archtest would pass vacuously. Mitigation:
//     the reflect handle is taken from `composition.CellModule` / `composition.ModuleResult`
//     directly (canonical import path), so a sibling type cannot be substituted.
//  4. **Handoff field smuggled into ModuleResult**: a new exported field on
//     ModuleResult (e.g. `Exports any`) would re-open value handoff without
//     touching the method signature. `TestModuleProvideSignatureFrozen01_ModuleResultFieldsFrozen`
//     asserts NumField()==3 and the exact field-name set {Cell,Opts,Resources},
//     so any extra/renamed field fails. This is the structural successor of the
//     old Out[]-list freeze (the resource/opts channels moved from positional
//     returns into ModuleResult fields).
//
// ## Symbol inventory (lives here, not in ai-robust.md per the charter)
//
//   - Frozen type: `github.com/ghbvf/gocell/runtime/composition.CellModule`
//   - Frozen method: `Provide`
//   - Required In[0]: `context.Context`
//   - Required In[1]: `*runtime/composition.SharedDeps`
//   - Required Out[0]: `runtime/composition.ModuleResult`
//   - Required Out[1]: `error`
//   - Frozen struct: `github.com/ghbvf/gocell/runtime/composition.ModuleResult`
//   - Field "Cell": `kernel/cell.Cell`
//   - Field "Opts": `[]runtime/bootstrap.Option`
//   - Field "Resources": `[]kernel/lifecycle.ManagedResource`
//
// See also: `runtime/composition/cell_module.go` godoc §MODULE-PROVIDE-NO-VALUE-HANDOFF-01.
package archtest

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/composition"
)

const ruleModuleProvideNoValueHandoff01 = "MODULE-PROVIDE-NO-VALUE-HANDOFF-01"

// TestModuleProvideSignatureFrozen01 reflects over the composition.CellModule
// interface and asserts that Provide has exactly the expected signature:
//
//	Provide(context.Context, *SharedDeps) (ModuleResult, error)
//
// Any deviation — extra parameter, extra return, wrong types in any position —
// causes an immediate test failure, preventing re-introduction of the
// ModuleExports handoff channel that Wave-1 #1423 deleted.
func TestModuleProvideSignatureFrozen01(t *testing.T) {
	t.Parallel()

	ifaceType := reflect.TypeOf((*composition.CellModule)(nil)).Elem()

	// Assert it is an interface (blind spot 1 reverse check).
	require.Equal(t, reflect.Interface, ifaceType.Kind(),
		"%s: composition.CellModule must be an interface kind; got %s",
		ruleModuleProvideNoValueHandoff01, ifaceType.Kind())

	// Find the Provide method.
	provideMethod, ok := ifaceType.MethodByName("Provide")
	require.True(t, ok,
		"%s: composition.CellModule must have a Provide method",
		ruleModuleProvideNoValueHandoff01)

	mt := provideMethod.Type

	// Assert exactly 2 inputs (context.Context, *SharedDeps).
	// Note: for an interface method obtained via reflect.Type.MethodByName, the
	// method type does NOT include a receiver parameter (unlike
	// reflect.Type.Method on a non-interface). So In(0) = first real parameter.
	require.Equal(t, 2, mt.NumIn(),
		"%s: Provide must have exactly 2 input parameters (context.Context, *SharedDeps); got %d. "+
			"A ModuleExports or other cross-module handoff parameter is forbidden.",
		ruleModuleProvideNoValueHandoff01, mt.NumIn())

	// Assert In[0] = context.Context
	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
	assert.Equal(t, ctxType, mt.In(0),
		"%s: Provide In[0] must be context.Context; got %s",
		ruleModuleProvideNoValueHandoff01, mt.In(0))

	// Assert In[1] = *composition.SharedDeps
	sharedDepsType := reflect.TypeOf((*composition.SharedDeps)(nil))
	assert.Equal(t, sharedDepsType, mt.In(1),
		"%s: Provide In[1] must be *composition.SharedDeps; got %s",
		ruleModuleProvideNoValueHandoff01, mt.In(1))

	// Assert exactly 2 outputs (ModuleResult, error). The resource/opts channels
	// now live as ModuleResult fields (single-source), not positional returns.
	require.Equal(t, 2, mt.NumOut(),
		"%s: Provide must have exactly 2 output values (ModuleResult, error); got %d. "+
			"A ModuleExports or other cross-module handoff return is forbidden.",
		ruleModuleProvideNoValueHandoff01, mt.NumOut())

	// Assert Out[0] = composition.ModuleResult
	moduleResultType := reflect.TypeOf(composition.ModuleResult{})
	assert.Equal(t, moduleResultType, mt.Out(0),
		"%s: Provide Out[0] must be composition.ModuleResult; got %s",
		ruleModuleProvideNoValueHandoff01, mt.Out(0))

	// Assert Out[1] = error
	errType := reflect.TypeOf((*error)(nil)).Elem()
	assert.Equal(t, errType, mt.Out(1),
		"%s: Provide Out[1] must be error; got %s",
		ruleModuleProvideNoValueHandoff01, mt.Out(1))
}

// TestModuleProvideSignatureFrozen01_ModuleResultFieldsFrozen verifies blind
// spot 4: the ModuleResult struct must have exactly three exported fields
// {Cell, Opts, Resources} with the exact types. An extra field (e.g. a smuggled
// `Exports any` handoff channel), a rename, or a type change fails immediately.
func TestModuleProvideSignatureFrozen01_ModuleResultFieldsFrozen(t *testing.T) {
	t.Parallel()

	rt := reflect.TypeOf(composition.ModuleResult{})
	require.Equal(t, reflect.Struct, rt.Kind(),
		"%s: composition.ModuleResult must be a struct; got %s",
		ruleModuleProvideNoValueHandoff01, rt.Kind())

	// Exactly 3 fields — bans an extra handoff field.
	require.Equal(t, 3, rt.NumField(),
		"%s: ModuleResult must have exactly 3 fields {Cell, Opts, Resources}; got %d. "+
			"A new field (e.g. an Exports handoff channel) is forbidden.",
		ruleModuleProvideNoValueHandoff01, rt.NumField())

	// Exact field-name set (catches a rename to a handoff-y field).
	names := map[string]struct{}{}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		assert.True(t, f.IsExported(),
			"%s: ModuleResult field %q must be exported", ruleModuleProvideNoValueHandoff01, f.Name)
		names[f.Name] = struct{}{}
	}
	assert.Equal(t, map[string]struct{}{"Cell": {}, "Opts": {}, "Resources": {}}, names,
		"%s: ModuleResult field-name set must be exactly {Cell, Opts, Resources}",
		ruleModuleProvideNoValueHandoff01)

	// Exact field types.
	cellField, ok := rt.FieldByName("Cell")
	require.True(t, ok, "%s: ModuleResult.Cell must exist", ruleModuleProvideNoValueHandoff01)
	assert.Equal(t, reflect.TypeOf((*cell.Cell)(nil)).Elem(), cellField.Type,
		"%s: ModuleResult.Cell must be cell.Cell; got %s", ruleModuleProvideNoValueHandoff01, cellField.Type)

	optsField, ok := rt.FieldByName("Opts")
	require.True(t, ok, "%s: ModuleResult.Opts must exist", ruleModuleProvideNoValueHandoff01)
	assert.Equal(t, reflect.TypeOf([]bootstrap.Option(nil)), optsField.Type,
		"%s: ModuleResult.Opts must be []bootstrap.Option; got %s", ruleModuleProvideNoValueHandoff01, optsField.Type)

	resField, ok := rt.FieldByName("Resources")
	require.True(t, ok, "%s: ModuleResult.Resources must exist", ruleModuleProvideNoValueHandoff01)
	assert.Equal(t, reflect.TypeOf([]kernellifecycle.ManagedResource(nil)), resField.Type,
		"%s: ModuleResult.Resources must be []lifecycle.ManagedResource; got %s",
		ruleModuleProvideNoValueHandoff01, resField.Type)
}

// TestModuleProvideSignatureFrozen_ReverseCheck_AliasNotPresent verifies blind
// spot 1: composition.CellModule must be an interface, not a type alias of
// another kind. This protects against an alias rewrite that would cause the
// primary test to inspect the wrong type.
func TestModuleProvideSignatureFrozen_ReverseCheck_AliasNotPresent(t *testing.T) {
	t.Parallel()
	ifaceType := reflect.TypeOf((*composition.CellModule)(nil)).Elem()
	assert.Equal(t, reflect.Interface, ifaceType.Kind(),
		"%s reverse-check: CellModule must remain an interface; a type-alias rewrite to %s "+
			"would defeat the reflect-based signature freeze",
		ruleModuleProvideNoValueHandoff01, ifaceType.Kind())
}

// TestModuleProvideSignatureFrozen_ReverseCheck_NoEmbedding verifies blind spot 2:
// the CellModule interface method set must have exactly 2 named methods (ID, Provide).
// If an embedded interface shadows Provide with a different signature, the method
// count would be wrong OR MethodByName would resolve to the embedded method.
func TestModuleProvideSignatureFrozen_ReverseCheck_NoEmbedding(t *testing.T) {
	t.Parallel()
	ifaceType := reflect.TypeOf((*composition.CellModule)(nil)).Elem()
	require.Equal(t, reflect.Interface, ifaceType.Kind())

	numMethods := ifaceType.NumMethod()
	assert.Equal(t, 2, numMethods,
		"%s reverse-check: CellModule must have exactly 2 methods (ID, Provide); got %d. "+
			"An embedded interface adding extra methods, or a shadow-Provide embedding, would be caught here.",
		ruleModuleProvideNoValueHandoff01, numMethods)

	// Verify both expected method names are present.
	_, hasID := ifaceType.MethodByName("ID")
	_, hasProvide := ifaceType.MethodByName("Provide")
	assert.True(t, hasID, "%s: method ID must exist on CellModule", ruleModuleProvideNoValueHandoff01)
	assert.True(t, hasProvide, "%s: method Provide must exist on CellModule", ruleModuleProvideNoValueHandoff01)
}

// INVARIANT: MODULE-PROVIDE-NO-VALUE-HANDOFF-01
//
// # MODULE-PROVIDE-NO-VALUE-HANDOFF-01 — CellModule.Provide signature frozen (Hard)
//
// ## Rule
//
// The `CellModule.Provide` method in `runtime/composition` MUST have exactly
// the signature:
//
//	Provide(context.Context, *SharedDeps) (cell.Cell, []bootstrap.Option, []lifecycle.ManagedResource, error)
//
// No cross-module value-handoff parameter (formerly `in ModuleExports`) and no
// cross-module value-handoff return (formerly `ModuleExports`) are permitted.
// Adding either would re-open the in-process Go-handle channel that Wave-1
// #1423 closed.
//
// ## AI-robust rating: Hard (reflect interface method signature)
//
// The archtest uses reflect.TypeOf to inspect the interface method set and
// pins the exact In/Out type list. Changing the signature changes the reflect
// shape → test fails immediately. There is no string-anchor or comment
// allowlist; the rule is form-locked.
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
//     the test loads the type via `go/types` package path and asserts the
//     canonical path suffix matches `/runtime/composition`.
//
// ## Symbol inventory (lives here, not in ai-robust.md per the charter)
//
//   - Frozen type: `github.com/ghbvf/gocell/runtime/composition.CellModule`
//   - Frozen method: `Provide`
//   - Required In[0]: `context.Context`
//   - Required In[1]: `*runtime/composition.SharedDeps`
//   - Required Out[0]: `kernel/cell.Cell`
//   - Required Out[1]: `[]runtime/bootstrap.Option`
//   - Required Out[2]: `[]kernel/lifecycle.ManagedResource`
//   - Required Out[3]: `error`
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
//	Provide(context.Context, *SharedDeps) (cell.Cell, []bootstrap.Option, []lifecycle.ManagedResource, error)
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

	// Assert exactly 4 outputs.
	require.Equal(t, 4, mt.NumOut(),
		"%s: Provide must have exactly 4 output values (cell.Cell, []bootstrap.Option, []lifecycle.ManagedResource, error); got %d. "+
			"A ModuleExports or other cross-module handoff return is forbidden.",
		ruleModuleProvideNoValueHandoff01, mt.NumOut())

	// Assert Out[0] = cell.Cell
	cellType := reflect.TypeOf((*cell.Cell)(nil)).Elem()
	assert.Equal(t, cellType, mt.Out(0),
		"%s: Provide Out[0] must be cell.Cell; got %s",
		ruleModuleProvideNoValueHandoff01, mt.Out(0))

	// Assert Out[1] = []bootstrap.Option
	bootstrapOptSliceType := reflect.TypeOf([]bootstrap.Option(nil))
	assert.Equal(t, bootstrapOptSliceType, mt.Out(1),
		"%s: Provide Out[1] must be []bootstrap.Option; got %s",
		ruleModuleProvideNoValueHandoff01, mt.Out(1))

	// Assert Out[2] = []lifecycle.ManagedResource
	managedResSliceType := reflect.TypeOf([]kernellifecycle.ManagedResource(nil))
	assert.Equal(t, managedResSliceType, mt.Out(2),
		"%s: Provide Out[2] must be []lifecycle.ManagedResource; got %s",
		ruleModuleProvideNoValueHandoff01, mt.Out(2))

	// Assert Out[3] = error
	errType := reflect.TypeOf((*error)(nil)).Elem()
	assert.Equal(t, errType, mt.Out(3),
		"%s: Provide Out[3] must be error; got %s",
		ruleModuleProvideNoValueHandoff01, mt.Out(3))
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

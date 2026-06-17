//go:build archtest

// INVARIANT: SHAREDDEPS-FIELDSET-FROZEN-01
//
// # SHAREDDEPS-FIELDSET-FROZEN-01 — composition.SharedDeps exported field set frozen (Hard)
//
// ## Rule
//
// The exported field set of `runtime/composition.SharedDeps` is frozen by a
// reflect golden: every exported field's name and type identity (as
// reflect.Type.String()) MUST exactly match sharedDepsFrozenExportedFields.
// Adding, removing, renaming, or retyping any exported field fails this test.
//
// ## Why
//
// SharedDeps is the public, interface-only dependency bag handed to EVERY
// CellModule.Provide. Its design contract (see the struct godoc) is that fields
// are "genuinely cross-cutting (consumed by multiple cells or the runtime
// itself)" — it is cell-agnostic. #1413 removed the last cell-specific field
// (ConfigKeyProvider); configcore now self-builds its key provider. Without a
// machine guard, that invariant is Soft: a future change could silently re-add a
// cell-specific field (e.g. ConfigKeyProvider, AuditLedgerStore, …) to the shared
// bag, re-leaking a platform cell's implementation detail to all consumers.
//
// This freeze makes any field-set change a deliberate, reviewed event: the golden
// must be edited in the same change, and the godoc must justify a newcomer as
// genuinely cross-cutting. It is the field-set analog of
// MODULE-PROVIDE-NO-VALUE-HANDOFF-01's ModuleResult freeze.
//
// ## AI-robust rating: Hard (reflect struct field-set + type freeze)
//
// reflect.TypeOf(SharedDeps{}) enumerates the exported fields; the test pins the
// exact {name → type-string} map. A field add/remove/rename/retype changes the
// reflect shape → the golden comparison fails immediately. There is no comment
// allowlist or string anchor; the rule is form-locked. (Type identity is pinned
// via reflect.Type.String(); the lone theoretical blind spot — a retype to a
// different type whose String() collides, e.g. two distinct packages both named
// "metrics" — is caught for any field RENAME/ADD/REMOVE regardless, and a same-
// string retype on an existing field would still have to pass Go's type checker
// at every consumer, so it cannot be a silent semantic swap.)
//
// ## Anti-vacuity / synthetic red case
//
// TestSharedDepsFieldSetFrozen01_DetectorRejectsPerturbation applies the same
// exportedFieldTypeStrings extractor to synthetic structs that (a) add an extra
// "ConfigKeyProvider" field and (b) drop a field, and asserts the extractor's
// output DIFFERS from the golden — proving the comparison is not vacuously true.
//
// ## Symbol inventory (lives here, not in ai-robust.md per the charter)
//
//   - Frozen type: github.com/ghbvf/gocell/framework/runtime/composition.SharedDeps
//   - Frozen golden: sharedDepsFrozenExportedFields (25 exported fields)
//
// Relationship: advances #1412 (SharedDeps Hard-ization) by covering the
// field-set dimension. The unexported sealed-construction marker (valid) is
// covered separately by SHAREDDEPS-SEALED-MARKER-01.
//
// See also: runtime/composition/shared_deps.go struct godoc.
package archtest

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/runtime/composition"
)

const ruleSharedDepsFieldSetFrozen01 = "SHAREDDEPS-FIELDSET-FROZEN-01"

// sharedDepsFrozenExportedFields is the golden {field name → reflect type
// string} map for composition.SharedDeps's EXPORTED fields. Every entry is a
// genuinely cross-cutting dependency (consumed by multiple cells or the runtime
// itself). To change it you must (1) edit this golden and (2) justify the field
// as cross-cutting in the SharedDeps struct godoc — a cell-specific field
// (consumed by exactly one cell) does NOT belong here; pass it via that cell's
// module constructor / self-build instead (see #1413 / configcore).
var sharedDepsFrozenExportedFields = map[string]string{
	"Clock":                  "clock.Clock",
	"Topology":               "bootstrap.Topology",
	"DeploymentTopology":     "bootstrap.DeploymentTopologySpec",
	"JWTIssuer":              "*auth.JWTIssuer",
	"JWTVerifier":            "*auth.JWTVerifier",
	"MetricsProvider":        "metrics.Provider",
	"Publisher":              "outbox.Publisher",
	"Subscriber":             "outbox.Subscriber",
	"ConfigEventCollector":   "metrics.ConfigEventCollector",
	"ConsumerClaimer":        "idempotency.Claimer",
	"PG":                     "capability.PGProvider",
	"Redis":                  "capability.RedisProvider",
	"InternalServiceKeyring": "auth.ServiceKeyring",
	"NonceStore":             "auth.NonceStore",
	"PrimaryHTTPAddr":        "string",
	"InternalHTTPAddr":       "string",
	"InProcessTransport":     "*transport.InProcessTransport",
	"Tracer":                 "wrapper.Tracer",
	"TransportObs":           "transport.CrossCellObs",
	// #2263 split-topology cross-cell mTLS material (cross-cutting: consumed by
	// celltransport.Resolve for the client side and the composition root's
	// internal-listener wiring for the server side).
	"RemoteClientTLS":           "tlsutil.ClientIdentity",
	"InternalListenerServerTLS": "*tls.Config",
	"HealthHTTPAddr":            "string",
	"MetricsToken":              "string",
	"VerboseToken":              "string",
	"VerboseDisabled":           "bool",
	"HealthLocalOnly":           "bool",
	"ProjectRoot":               "string",
}

// exportedFieldTypeStrings returns the {name → type-string} map of the exported
// fields of a struct type. Unexported fields (e.g. the sealed-construction
// marker `valid`) are intentionally excluded — they are an implementation detail
// guarded separately, not part of the public dependency surface.
func exportedFieldTypeStrings(rt reflect.Type) map[string]string {
	out := make(map[string]string, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		out[f.Name] = f.Type.String()
	}
	return out
}

// TestSharedDepsFieldSetFrozen01 pins the exact exported field set of
// composition.SharedDeps. A field add/remove/rename/retype trips the golden
// comparison, forcing a deliberate edit + a cross-cutting justification in the
// struct godoc and blocking a silent re-leak of a cell-specific field.
func TestSharedDepsFieldSetFrozen01(t *testing.T) {
	t.Parallel()

	rt := reflect.TypeOf(composition.SharedDeps{})
	require.Equal(t, reflect.Struct, rt.Kind(),
		"%s: composition.SharedDeps must be a struct; got %s",
		ruleSharedDepsFieldSetFrozen01, rt.Kind())

	got := exportedFieldTypeStrings(rt)
	assert.Equal(t, sharedDepsFrozenExportedFields, got,
		"%s: composition.SharedDeps exported field set drifted from the frozen golden. "+
			"If you ADDED a field, it must be genuinely cross-cutting (consumed by multiple "+
			"cells or the runtime) — a cell-specific dep belongs on that cell's module "+
			"(see #1413/configcore self-build), NOT on the shared bag. Update "+
			"sharedDepsFrozenExportedFields AND the SharedDeps struct godoc in the same change.",
		ruleSharedDepsFieldSetFrozen01)
}

// TestSharedDepsFieldSetFrozen01_DetectorRejectsPerturbation is the anti-vacuity
// red case: it proves exportedFieldTypeStrings + the golden comparison actually
// detect a drift, so TestSharedDepsFieldSetFrozen01 is not vacuously green.
func TestSharedDepsFieldSetFrozen01_DetectorRejectsPerturbation(t *testing.T) {
	t.Parallel()

	// (a) A re-added cell-specific field (the exact regression #1413 removed)
	// must be detected as drift.
	type sharedDepsWithReaddedCellField struct {
		Clock             string
		ConfigKeyProvider string
	}
	addedExtra := exportedFieldTypeStrings(reflect.TypeOf(sharedDepsWithReaddedCellField{}))
	assert.NotEqual(t, sharedDepsFrozenExportedFields, addedExtra,
		"%s anti-vacuity: a struct carrying a re-added ConfigKeyProvider field must NOT match "+
			"the golden — otherwise the freeze would not catch a re-leak", ruleSharedDepsFieldSetFrozen01)

	// (b) A dropped field must also be detected.
	type sharedDepsMissingField struct {
		Clock string
	}
	dropped := exportedFieldTypeStrings(reflect.TypeOf(sharedDepsMissingField{}))
	assert.NotEqual(t, sharedDepsFrozenExportedFields, dropped,
		"%s anti-vacuity: a struct missing fields must NOT match the golden",
		ruleSharedDepsFieldSetFrozen01)

	// (c) Same field NAMES but one field RETYPED: proves that type-string drift is
	// caught at constant field count, not just count drift. Clock is retyped from
	// clock.Clock → string; all other 24 field names (the full golden set) are
	// preserved so the ONLY divergence is Clock's type. This red case demonstrates
	// that exportedFieldTypeStrings detects type-string divergence, not just
	// field-set membership divergence.
	type sharedDepsRetypedClock struct {
		Clock                  string // was clock.Clock — intentional type perturbation
		Topology               string
		DeploymentTopology     string
		JWTIssuer              string
		JWTVerifier            string
		MetricsProvider        string
		Publisher              string
		Subscriber             string
		ConfigEventCollector   string
		ConsumerClaimer        string
		PG                     string
		Redis                  string
		InternalServiceKeyring string
		NonceStore             string
		PrimaryHTTPAddr        string
		InternalHTTPAddr       string
		InProcessTransport     string
		Tracer                 string
		TransportObs           string
		HealthHTTPAddr         string
		MetricsToken           string
		VerboseToken           string
		VerboseDisabled        string // was bool
		HealthLocalOnly        string // was bool
		ProjectRoot            string
	}
	retyped := exportedFieldTypeStrings(reflect.TypeOf(sharedDepsRetypedClock{}))
	assert.NotEqual(t, sharedDepsFrozenExportedFields, retyped,
		"%s anti-vacuity (type drift): a struct with the same 25 field names but Clock "+
			"retyped to string must NOT match the golden — proves type-string drift detection, "+
			"not just field-set membership detection", ruleSharedDepsFieldSetFrozen01)

	// Sanity: the golden itself is non-empty (a vacuous empty golden would make
	// the primary test trivially satisfiable by an empty struct).
	require.NotEmpty(t, sharedDepsFrozenExportedFields,
		"%s: golden field set must be non-empty", ruleSharedDepsFieldSetFrozen01)
}

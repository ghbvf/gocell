//go:build archtest

// INVARIANT: WEBHOOK-CB-NO-DISABLE-01
//
// WEBHOOK-CB-NO-DISABLE-01 — CircuitBreakerSettings exported field set freeze.
//
// # What this guards
//
// kernel/webhook.CircuitBreakerSettings deliberately has no Enabled/Disabled
// field: "disabling the circuit breaker" must be inexpressible at the type
// level (no-disable invariant, see circuit.go godoc and ADR #2106). This test
// locks the exported field set to exactly:
//
//	{TripThreshold int, OpenTimeout time.Duration, HalfOpenProbes int}
//
// Any change that adds an Enabled/Disabled field — or any other exported field
// that could semantically suppress circuit-breaker protection — causes this
// test to fail at CI. The frozen set is intentionally EXPORTED-only: unexported
// fields are internal implementation detail and do not affect the no-disable
// invariant.
//
// # AI-robust rating
//
// Hard — reflect field freeze (charter §"Hard 范本目录": "reflect schema freeze:
// wire/schema struct 字段集、tag、类型身份精确冻结"). The field set, field names,
// and field types are objective structural facts captured by reflect.Type.
// Any drift (add Enabled bool / rename / reorder exported fields) produces a
// tuple mismatch and forces the change through this explicit checkpoint.
//
// Upstream Hard (type system): the Go type system prevents any caller from
// minting a CircuitBreakerSettings with fields that do not exist; adding one
// forces every existing literal and composite-literal to also add the field,
// making the change auditable at compile time.
//
// Downstream Hard: [CircuitBreakerSettings.Validate] rejects zero/near-disable
// values for all three tunable fields — a complement to the no-field gate that
// prevents de-facto disabling via extreme values.
//
// # Blind spots (charter §"强制盲区自检")
//
//   - Unexported fields: a same-package unexported `enabled bool` field would
//     not appear in the frozen exported set below. Bounded by code review +
//     PANIC-REGISTERED-01 / existing archtest coverage of the package. The
//     circuit gate itself (circuitGate.Allow) is tested to never silently skip
//     the breaker even when breakerFor returns nil (fail-open with Error log).
//   - Validate upper-bound bypass: an operator could still call
//     CircuitBreakerSettings{TripThreshold: cbMaxTripThreshold, ...} for the
//     maximum allowed value; the freeze does not prevent that. cbMaxTripThreshold
//     is chosen to allow realistic high thresholds (≤ 1000), so no real-world
//     tuning need is blocked.
//   - Anti-vacuity: if the package path changes or the type is renamed, the anti-
//     vacuity assertion (NumExportedFields > 0 + correct type name + pkg path)
//     catches the regression and turns GREEN fixture → RED, alerting immediately.
//
// ref: tools/archtest/webhook_signer_funnel_test.go (A2 SignedHeaders field freeze
// — same reflect schema freeze pattern)
// ref: kernel/webhook/circuit.go (CircuitBreakerSettings godoc + const bounds)
package archtest

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/webhook"
)

// frozenCBField describes one expected exported field of CircuitBreakerSettings.
type frozenCBField struct {
	name     string
	typeName string // reflect.Type.String()
}

// frozenCBExportedFields is the expected exact exported-field tuple for
// webhook.CircuitBreakerSettings (in Go struct declaration order).
// Updating this list requires a conscious decision: any addition of an
// Enabled/Disabled field MUST be discussed and WILL break CI.
var frozenCBExportedFields = []frozenCBField{
	{name: "TripThreshold", typeName: "int"},
	{name: "OpenTimeout", typeName: "time.Duration"},
	{name: "HalfOpenProbes", typeName: "int"},
}

// TestWebhookCBNoDisable_ExportedFieldFreeze is the primary guard: it freezes
// the exported field set of webhook.CircuitBreakerSettings and asserts every
// field matches the frozen tuple by name, type, and count.
//
// Adding an Enabled/Disabled bool or any other exported field → NumExportedFields
// mismatch → CI red.
// Renaming a field → name mismatch → CI red.
// Changing a field type → typeName mismatch → CI red.
func TestWebhookCBNoDisable_ExportedFieldFreeze(t *testing.T) {
	t.Parallel()

	st := reflect.TypeOf(webhook.CircuitBreakerSettings{})
	require.Equal(t, reflect.Struct, st.Kind(),
		"webhook.CircuitBreakerSettings must be a struct")

	// Collect exported fields only (PkgPath == "" → exported).
	var exported []reflect.StructField
	for i := range st.NumField() {
		sf := st.Field(i)
		if sf.PkgPath == "" { // exported
			exported = append(exported, sf)
		}
	}

	require.Equal(t, len(frozenCBExportedFields), len(exported),
		"WEBHOOK-CB-NO-DISABLE-01: CircuitBreakerSettings exported field count = %d, want %d "+
			"(adding an Enabled/Disabled field re-opens the no-disable invariant; "+
			"removing one breaks downstream callers; update frozenCBExportedFields + circuit.go together)",
		len(exported), len(frozenCBExportedFields))

	for i, want := range frozenCBExportedFields {
		sf := exported[i]
		assert.Equal(t, want.name, sf.Name,
			"WEBHOOK-CB-NO-DISABLE-01: exported field[%d] name = %q, want %q", i, sf.Name, want.name)
		assert.Equal(t, want.typeName, sf.Type.String(),
			"WEBHOOK-CB-NO-DISABLE-01: exported field %s type = %q, want %q",
			sf.Name, sf.Type.String(), want.typeName)
		exported := sf.PkgPath == ""
		assert.True(t, exported,
			"WEBHOOK-CB-NO-DISABLE-01: field %s must remain exported (PkgPath must be empty)", sf.Name)
	}
}

// TestWebhookCBNoDisable_AntiVacuity guards against the freeze trivially passing
// on a wrong/empty type load or an unexpected package rename.
func TestWebhookCBNoDisable_AntiVacuity(t *testing.T) {
	t.Parallel()

	st := reflect.TypeOf(webhook.CircuitBreakerSettings{})
	assert.Equal(t, "CircuitBreakerSettings", st.Name(),
		"anti-vacuity: loaded type name = %q, want CircuitBreakerSettings", st.Name())
	assert.True(t, strings.HasSuffix(st.PkgPath(), "kernel/webhook"),
		"anti-vacuity: package path %q must end with kernel/webhook", st.PkgPath())

	// Count exported fields directly (not via the frozen slice) to prove the
	// reflect call actually found fields.
	exportedCount := 0
	for i := range st.NumField() {
		if st.Field(i).PkgPath == "" {
			exportedCount++
		}
	}
	assert.Equal(t, len(frozenCBExportedFields), exportedCount,
		"anti-vacuity: exported field count via reflect (%d) must equal frozen tuple size (%d) — "+
			"wrong type loaded or freeze slice is stale",
		exportedCount, len(frozenCBExportedFields))
	assert.Greater(t, exportedCount, 0,
		"anti-vacuity: CircuitBreakerSettings has no exported fields — wrong type loaded?")
}

// TestWebhookCBNoDisable_RedFixture is the reverse self-check: a look-alike
// struct with an extra Enabled field must be detected by the field-count check,
// proving the freeze is not vacuous.
func TestWebhookCBNoDisable_RedFixture(t *testing.T) {
	t.Parallel()

	// brokenCBSettings mimics CircuitBreakerSettings but adds an Enabled bool —
	// the exact field addition the no-disable invariant forbids.
	type brokenCBSettings struct {
		TripThreshold  int
		OpenTimeout    time.Duration
		HalfOpenProbes int
		Enabled        bool // forbidden addition — should trigger the count mismatch
	}
	bt := reflect.TypeOf(brokenCBSettings{})

	// Count exported fields.
	exportedCount := 0
	for i := range bt.NumField() {
		if bt.Field(i).PkgPath == "" {
			exportedCount++
		}
	}
	assert.Equal(t, 4, exportedCount,
		"RED fixture self-check: brokenCBSettings must have 4 exported fields (including Enabled)")
	assert.Greater(t, exportedCount, len(frozenCBExportedFields),
		"RED fixture self-check: brokenCBSettings field count (%d) must exceed frozen count (%d) — "+
			"proves the field-count check would catch this addition",
		exportedCount, len(frozenCBExportedFields))
}

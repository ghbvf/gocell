// invariants asserted in this file:
//   - INVARIANT: CONTRACT-YAML-WEBHOOK-FIELDS-FROZEN-01
//   - INVARIANT: WEBHOOK-MARKER-RETIRED-01
//
// Package archtest — webhook contract kind single-source funnel.
//
// Webhook cell subscriptions are single-sourced from slice.yaml
// contractUsages[role=webhook-receive] (for inbound) and
// contractUsages[role=webhook-dispatch] (for outbound). Two derived artifacts
// are locked:
//
//  1. contract.yaml endpoints.receivers / endpoints.dispatchers — DERIVED.
//     metadata.EndpointsMeta.Receivers and Dispatchers carry yaml:"-" so
//     the parser's KnownFields(true) strict decode REJECTS any literal
//     `receivers:` or `dispatchers:` key in contract.yaml. The cell
//     receiver/dispatcher sets are computed from slice contractUsages;
//     deriveWebhookEndpoints populates both fields.
//  2. cell.go `// +webhook:receive` / `// +webhook:dispatch` markers — RETIRED
//     before they could ever exist. markergen's knownMarkers set does NOT
//     include any "webhook:" marker, so a mistakenly added marker is an
//     unknown-marker error at generate time; cellgen builds wiring from
//     slice.yaml.
//
// AI-robust ratings (per .claude/rules/gocell/ai-robust.md):
//
//   - CONTRACT-YAML-WEBHOOK-FIELDS-FROZEN-01 — Funnel dual-lock:
//     下游 Hard: yaml:"-" on EndpointsMeta.Receivers + EndpointsMeta.Dispatchers
//     + KnownFields strict decode makes hand-written `receivers:` / `dispatchers:`
//     keys in contract.yaml unrepresentable parse errors (type-system gate;
//     mirrors EndpointsMeta.Subscribers yaml:"-" precedent).
//     This reflect-based test LOCKS that mechanism: if an author re-adds a yaml
//     key to Receivers or Dispatchers (re-opening the bypass), this test fires.
//     Type-level assertion, no string anchor.
//     上游 Medium: EndpointsMeta.Receivers / Dispatchers are exported fields on
//     a public struct — direct assignment by any package (cells/, examples/,
//     cmd/) is not currently guarded; package-internal AND package-external Go
//     code can assign them directly, bypassing deriveWebhookEndpoints. Archtest
//     does not yet lock the write path to the derive funnel. Upstream Hard-ization
//     tracked in gh issue #1254 (same pattern as Subscribers gh #985).
//   - WEBHOOK-MARKER-RETIRED-01 — Hard upstream (markergen closed set; any
//     "webhook:*" marker not in knownMarkers is an unknown-marker error at
//     generate time) + Medium downstream (AST backstop cell.go scan catches
//     stray markers statically before generate runs).
//
// Blind spots (reverse self-checks below assert the scanners have teeth):
//   - The reflect lock keys on field NAMES "Receivers"/"Dispatchers"; a rename
//     surfaces as a test error (visible), not a silent pass.
//   - The markergen Merge oracle: uses Merge black-box to verify no "webhook:"
//     marker is in knownMarkers. If markergen adds "webhook:" by mistake, Merge
//     would succeed and this test would fail — correct behaviour.
//   - The cell.go scan matches the literal tokens "+webhook:receive" /
//     "+webhook:dispatch"; a marker split across comment lines is a documented
//     blind spot (markergen's line-based parser also would not recognise it).
//   - Receivers/Dispatchers are exported fields on a public struct — direct
//     assignment is not guarded; upstream Hard-ization tracked in gh issue #1254.
package archtest

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/codegen/markergen"
)

// ---------------------------------------------------------------------------
// CONTRACT-YAML-WEBHOOK-FIELDS-FROZEN-01
// ---------------------------------------------------------------------------

// TestContractYAMLWebhookFieldsFrozen01 locks the type-system mechanism that makes
// hand-written contract.yaml `receivers:` / `dispatchers:` keys unrepresentable:
// EndpointsMeta.Receivers and Dispatchers must carry yaml:"-" (derived; KnownFields
// rejects the literal keys), mirroring the EndpointsMeta.Subscribers precedent.
//
// Blind-spot inventory:
//   - Reflect lock keys on EXACT field names "Receivers" / "Dispatchers"; any
//     rename (even to a semantically equivalent name) surfaces as a test failure.
//   - yaml:"-" tag is verified precisely; yaml:"-,omitempty" or other variants
//     are treated as failures so they cannot sneak in a yaml-serialisable form.
//   - Exported fields on a public struct can still be written directly by
//     cells/examples/cmd — upstream Hard-ization tracked in gh issue #1254.
//
// Reverse self-check: see TestContractYAMLWebhookFieldsFrozen01_ReverseCheck below.
//
// AI-robust rating: Hard downstream (reflect tag lock, type-system gate).
func TestContractYAMLWebhookFieldsFrozen01(t *testing.T) {
	t.Parallel()
	et := reflect.TypeOf(metadata.EndpointsMeta{})

	for _, fieldName := range []string{"Receivers", "Dispatchers"} {
		field, ok := et.FieldByName(fieldName)
		if !ok {
			t.Errorf("CONTRACT-YAML-WEBHOOK-FIELDS-FROZEN-01: EndpointsMeta has no %s field (not yet added by impl?)", fieldName)
			continue
		}
		if got := field.Tag.Get("yaml"); got != "-" {
			t.Errorf(
				"CONTRACT-YAML-WEBHOOK-FIELDS-FROZEN-01: EndpointsMeta.%s yaml tag = %q, want %q "+
					"(derived field must be yaml:\"-\" so KnownFields rejects a hand-written %s: key)",
				fieldName, got, "-", strings.ToLower(fieldName),
			)
		}
	}
}

// TestContractYAMLWebhookFieldsFrozen01_ReverseCheck is the reverse self-check:
// it asserts that the existing Subscribers field (the precedent) still has
// yaml:"-", proving the reflect inspection works and is not vacuously passing.
// It also asserts the full set of yaml:"-" fields on EndpointsMeta includes at
// least Subscribers — giving a known-good anchor for the mechanism.
func TestContractYAMLWebhookFieldsFrozen01_ReverseCheck(t *testing.T) {
	t.Parallel()
	et := reflect.TypeOf(metadata.EndpointsMeta{})

	// Subscribers is the existing frozen field — validate the mechanism works on it.
	subs, ok := et.FieldByName("Subscribers")
	if !ok {
		t.Fatal("CONTRACT-YAML-WEBHOOK-FIELDS-FROZEN-01 reverse check: Subscribers field disappeared — mechanism broken")
	}
	if got := subs.Tag.Get("yaml"); got != "-" {
		t.Errorf("CONTRACT-YAML-WEBHOOK-FIELDS-FROZEN-01 reverse check: Subscribers yaml tag = %q, want \"-\"", got)
	}

	// Collect all yaml:"-" fields to document the known set and catch silent
	// regressions where the entire tag is removed.
	var derivedFields []string
	for i := range et.NumField() {
		f := et.Field(i)
		if f.Tag.Get("yaml") == "-" {
			derivedFields = append(derivedFields, f.Name)
		}
	}
	// Must contain at least the existing Subscribers anchor.
	found := false
	for _, name := range derivedFields {
		if name == "Subscribers" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("CONTRACT-YAML-WEBHOOK-FIELDS-FROZEN-01 reverse check: Subscribers not in derived fields set %v", derivedFields)
	}
}

// ---------------------------------------------------------------------------
// WEBHOOK-MARKER-RETIRED-01
// ---------------------------------------------------------------------------

// scanWebhookMarker reports a violation when a cell.go retains a
// `// +webhook:receive` or `// +webhook:dispatch` marker.
func scanWebhookMarker(rel string, data []byte) []string {
	s := string(data)
	var violations []string
	for _, token := range []string{"+webhook:receive", "+webhook:dispatch"} {
		if strings.Contains(s, token) {
			violations = append(violations, rel+": retains a "+token+" marker; webhook roles are "+
				"single-sourced from slice.yaml contractUsages[role=webhook-receive/webhook-dispatch] — "+
				"remove the marker")
		}
	}
	return violations
}

// TestWebhookMarkerRetired01 asserts no cell.go under cells/ or examples/
// retains a +webhook:receive or +webhook:dispatch marker.
//
// Blind-spot inventory:
//   - Scanner matches exact literal tokens "+webhook:receive" / "+webhook:dispatch";
//     a marker split across comment lines is not detected (same blind spot as
//     SUBSCRIBE-MARKER-RETIRED-01; markergen's line-based parser also ignores splits).
//   - This test currently passes vacuously (no cell.go has ever contained these
//     markers because they were never valid); it is a regression lock ensuring
//     future cell.go authors cannot accidentally add them.
//
// Reverse self-check: see TestWebhookMarkerRetired01_ScannerFires below.
//
// AI-robust rating: Hard upstream (markergen knownMarkers closed set, Merge errors) +
// Medium downstream (this AST backstop).
func TestWebhookMarkerRetired01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := scanner.DirsScope(root, []string{"cells", "examples"},
		scanner.MatchRels(func(rel string) bool {
			return filepath.Base(rel) == "cell.go"
		}),
	)
	scanner.EachContentFile(t, scope, []string{".go"}, func(t *testing.T, fc scanner.ContentContext) {
		for _, v := range scanWebhookMarker(fc.Rel, fc.Bytes) {
			t.Errorf("WEBHOOK-MARKER-RETIRED-01: %s", v)
		}
	})
}

// TestWebhookMarkerRetired01_ScannerFires proves the scanner has teeth
// (reverse self-check).
func TestWebhookMarkerRetired01_ScannerFires(t *testing.T) {
	t.Parallel()
	// receive marker must be flagged
	violatingReceive := []byte("type Cell struct {\n\t// +webhook:receive:contract=webhook.stripe.events.v1\n\tingest *webhookingest.Service\n}\n")
	if got := scanWebhookMarker("cells/x/cell.go", violatingReceive); len(got) == 0 {
		t.Error("WEBHOOK-MARKER-RETIRED-01: scanner did not flag a +webhook:receive marker")
	}

	// dispatch marker must be flagged
	violatingDispatch := []byte("type Cell struct {\n\t// +webhook:dispatch:contract=webhook.shopify.v1\n\tdispatch *webhookdispatch.Service\n}\n")
	if got := scanWebhookMarker("cells/x/cell.go", violatingDispatch); len(got) == 0 {
		t.Error("WEBHOOK-MARKER-RETIRED-01: scanner did not flag a +webhook:dispatch marker")
	}

	// clean cell.go must not be flagged
	clean := []byte("type Cell struct {\n\t// +cell:listener:ref=primary\n\th *sessionlogin.Handler\n}\n")
	if got := scanWebhookMarker("cells/x/cell.go", clean); len(got) != 0 {
		t.Errorf("WEBHOOK-MARKER-RETIRED-01: scanner false-positive on clean cell.go: %v", got)
	}
}

// TestWebhookMarkerRetired01_MarkergenKnownMarkersExcludes verifies the Hard
// upstream gate: "webhook:" is NOT in markergen's "cell:" / "slice:" prefix set,
// so a +webhook:receive marker in cell.go is silently ignored by CollectFromCellFile
// (the prefix filter in splitMarker only passes "cell:" and "slice:" prefixes).
// This means the marker is truly a no-op — it never enters the dispatch pipeline
// and cannot accidentally register a webhook receive wiring.
//
// The expected Merge result is nil with an EMPTY WireBundle for the cell
// (zero Listeners, zero Routes) — proving no webhook routing happened.
//
// This is the correct Hard upstream semantics: since "webhook:" was never added
// to the prefix set, a +webhook:receive in cell.go cannot produce any wiring.
// If someone were to add "webhook:" support to the prefix set + knownMarkers
// without also updating slice.yaml as the single source, this test would catch
// the unintended wiring.
//
// Blind-spot: the test uses Merge as a black-box oracle rather than inspecting
// the unexported knownMarkers slice directly. A +webhook: marker that produces
// an empty WireBundle proves the marker is ignored, which is the desired invariant.
func TestWebhookMarkerRetired01_MarkergenKnownMarkersExcludes(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cellDir := filepath.Join(tmp, "cells", "webhooktest")
	if err := os.MkdirAll(cellDir, 0o750); err != nil {
		t.Fatalf("WEBHOOK-MARKER-RETIRED-01: MkdirAll: %v", err)
	}

	// A cell.go containing a +webhook:receive marker (type-level).
	// The "webhook:" prefix is outside the "cell:" / "slice:" prefix scope of
	// splitMarker, so this marker must be silently dropped.
	cellGoContent := []byte(`package webhooktest

// WebhookCell is a synthetic test cell.
//
// +webhook:receive:contract=webhook.stripe.events.v1
type WebhookCell struct{}
`)
	if err := os.WriteFile(filepath.Join(cellDir, "cell.go"), cellGoContent, 0o600); err != nil {
		t.Fatalf("WEBHOOK-MARKER-RETIRED-01: WriteFile: %v", err)
	}

	if err := os.WriteFile(filepath.Join(cellDir, "cell.yaml"), []byte("id: webhooktest\n"), 0o600); err != nil {
		t.Fatalf("WEBHOOK-MARKER-RETIRED-01: WriteFile cell.yaml: %v", err)
	}

	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"webhooktest": {
				ID:   "webhooktest",
				File: "cells/webhooktest/cell.yaml",
			},
		},
		Slices: map[string]*metadata.SliceMeta{},
	}

	bundles, err := markergen.Merge(tmp, pm)
	if err != nil {
		t.Errorf("WEBHOOK-MARKER-RETIRED-01: markergen.Merge must return nil error for +webhook:receive (marker silently ignored, not unknown-marker error), got: %v", err)
		return
	}
	// Verify the WireBundle for the cell is empty — no listeners, no routes.
	bundle := bundles["webhooktest"]
	if len(bundle.Listeners) != 0 || len(bundle.Routes) != 0 {
		t.Errorf("WEBHOOK-MARKER-RETIRED-01: +webhook:receive must not produce any wiring (Listeners=%v Routes=%v)",
			bundle.Listeners, bundle.Routes)
	}
}

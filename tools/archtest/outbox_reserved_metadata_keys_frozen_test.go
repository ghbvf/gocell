// INVARIANT: OUTBOX-RESERVED-METADATA-KEYS-FROZEN-01
//
// This file owns ONE invariant: the kernel/outbox.ReservedMetadataKeys set —
// the keys that Entry.Validate rejects in the producer-owned Metadata namespace
// (the observability + principal bridge keys) — is frozen against a hardcoded
// want-set declared HERE, independent of the production slice.
//
// Why this exists (issue #1291 FP1, findings F1/F2): the pre-existing in-package
// test TestEntry_Validate_RejectsReservedMetadataKeys ranged over
// outbox.ReservedMetadataKeys itself — a self-referential tautology that cannot
// detect "one key silently dropped" (a removed key is simply never iterated, so
// every remaining key still passes). The godoc on the production var also named a
// "reservedMetadataKeyMembership invariant test" that never existed. This file is
// that test: a deny-by-default golden whose want-set is an INDEPENDENT witness.
//
// AI-robust rating (per .claude/rules/gocell/ai-robust.md §Hard 范本目录
// "reflect 字段冻结" / "codegen funnel + golden"):
//
//   - Hard (downstream): the reflect read of the production exported var compared
//     against a hardcoded want-set leaves no single-sided add/drop undetected at
//     PR time. An author cannot change the production set without this test going
//     red, and cannot make the test pass without consciously editing the golden.
//   - Medium (upstream) = Go permanent ceiling: ReservedMetadataKeys is a
//     package-level `var []string`; any in-package edit can mutate it and Go
//     cannot make that "unexpressible". This is the SAME ceiling as
//     PRINCIPAL-SEALED-FIELD-FROZEN-01 (reflect freeze of a package-level type).
//     Following that precedent we do NOT open a tracking issue — there is no
//     low-cost Hard upgrade path for a package-var golden — but the ceiling is
//     named here explicitly so it is not a silent carryover.
//
// Honest enforcement boundary for the broader FP1 reconcile (NOT over-claimed as
// "all Hard"): the REJECTION behavior (validateMetadata) is structurally Medium —
// Metadata is an open map[string]string by design (producers set arbitrary
// business keys), so "forbid this map key" cannot be made compile-time
// unexpressible; runtime rejection is the only available form. full-Hard is
// structurally unreachable here, and that is defensible.
//
// Tool blind spots (per AI-robust §载体决策原则 "强制盲区自检"):
//
//   - reflect/value read sees the slice CONTENTS but not whether validateMetadata
//     actually consults the set. "key in list but not enforced" is covered by the
//     behavioral witness kernel/outbox.TestEntry_Validate_RejectsReservedMetadataKeys
//     (which ranges over its OWN hardcoded want-set after FP1), not here.
//   - slice ORDER carries no semantics (the production set is also projected into
//     a map at init); the compare is order-insensitive via sort, so a reordering
//     is intentionally NOT a violation.
package archtest

import (
	"slices"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// wantReservedMetadataKeys is the frozen membership of
// outbox.ReservedMetadataKeys (11 keys = 7 observability + 4 principal).
// Adding / removing a bridge key requires editing this list under explicit
// reviewer attention (deny-by-default). occurred_at is deliberately NOT here:
// it is a typed time.Time scalar with no ctxkeys round-trip (unlike all 11
// keys, which RestoreToContext re-injects), and Entry is sealed-construction so
// no producer can forge the field — reserving its metadata key would be inert
// (issue #1291 FP1; ADR 202605281200-1042 §5 reconciled 12→11).
var wantReservedMetadataKeys = []string{
	// Observability family (7).
	"trace_id",
	"traceparent",
	"trace_state",
	"tracestate",
	"span_id",
	"request_id",
	"correlation_id",
	// Principal family (4) — issue #1229.
	"actor_id",
	"subject_id",
	"tenant_id",
	"session_id",
}

// TestOutboxReservedMetadataKeysFrozen01 freezes the ReservedMetadataKeys set
// against the independent hardcoded want-set (anti-tautology): both directions —
// nothing missing, nothing extra.
func TestOutboxReservedMetadataKeysFrozen01(t *testing.T) {
	t.Parallel()

	if diff := reservedKeysDiff(outbox.ReservedMetadataKeys, wantReservedMetadataKeys); diff != "" {
		t.Fatalf("OUTBOX-RESERVED-METADATA-KEYS-FROZEN-01: outbox.ReservedMetadataKeys drifted from the "+
			"frozen want-set.\n%s\n"+
			"A reserved key is part of the observability/principal bridge contract: adding/removing one "+
			"rewires what Entry.Validate rejects in the producer Metadata namespace. Update "+
			"wantReservedMetadataKeys here AND the behavioral witness "+
			"kernel/outbox.TestEntry_Validate_RejectsReservedMetadataKeys AND ADR 202605281200-1042 §5 "+
			"under reviewer attention.", diff)
	}
}

// TestOutboxReservedMetadataKeysFrozen01_NegativeControl proves the comparison
// is non-vacuous: a synthetically drifted set (one extra key + one missing key)
// MUST produce a non-empty diff (blind-spot self-check).
func TestOutboxReservedMetadataKeysFrozen01_NegativeControl(t *testing.T) {
	t.Parallel()

	// Extra key present, "session_id" dropped — both a superset and a subset
	// violation in one synthetic input.
	drifted := []string{
		"trace_id", "traceparent", "trace_state", "tracestate", "span_id",
		"request_id", "correlation_id", "actor_id", "subject_id", "tenant_id",
		"unexpected_extra_key",
	}
	if diff := reservedKeysDiff(drifted, wantReservedMetadataKeys); diff == "" {
		t.Fatal("OUTBOX-RESERVED-METADATA-KEYS-FROZEN-01 negative control: a drifted set produced an empty " +
			"diff — the membership comparison is vacuous and would not catch a real drift")
	}
}

// reservedKeysDiff returns "" when got and want hold the same set of keys
// (order-insensitive), else a human-readable description of the missing/extra
// keys. The want-set is the independent golden; got is the production set.
func reservedKeysDiff(got, want []string) string {
	gotSorted := slices.Clone(got)
	wantSorted := slices.Clone(want)
	slices.Sort(gotSorted)
	slices.Sort(wantSorted)
	if slices.Equal(gotSorted, wantSorted) {
		return ""
	}

	var extra, missing []string
	wantSet := make(map[string]struct{}, len(want))
	for _, k := range want {
		wantSet[k] = struct{}{}
	}
	gotSet := make(map[string]struct{}, len(got))
	for _, k := range got {
		gotSet[k] = struct{}{}
		if _, ok := wantSet[k]; !ok {
			extra = append(extra, k)
		}
	}
	for _, k := range want {
		if _, ok := gotSet[k]; !ok {
			missing = append(missing, k)
		}
	}
	slices.Sort(extra)
	slices.Sort(missing)
	return "  extra (in production, not in golden):   " + sliceOrNone(extra) + "\n" +
		"  missing (in golden, not in production): " + sliceOrNone(missing)
}

func sliceOrNone(s []string) string {
	if len(s) == 0 {
		return "(none)"
	}
	return "[" + strings.Join(s, ", ") + "]"
}

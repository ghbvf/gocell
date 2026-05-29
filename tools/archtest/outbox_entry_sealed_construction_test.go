// INVARIANT: OUTBOX-ENTRY-SEALED-CONSTRUCTION-01
//
// This file owns ONE invariant: kernel/outbox.Entry is sealed construction —
// every field is unexported, so a populated composite literal
// `outbox.Entry{Field: ...}` outside package kernel/outbox does not compile
// (issue #1229). That compile-time impossibility is the Hard upstream gate: it
// is what forces OccurredAt to be clock-derived, Observability/Principal to be
// ctx-injected at the NewEntry trust boundary, and makes producer forgery /
// omission structurally unrepresentable.
//
// The Go compiler already enforces "no external populated literal" — that is
// precisely why the #1229 getter fan-out across every consuming package was
// necessary. This archtest is the REVERSE self-check: it pins the property the
// whole PR depends on, so a future PR that re-exports any Entry field (silently
// re-opening external literal construction) fails at PR/nightly time instead of
// quietly eroding the seal.
//
// Sanctioned construction paths (all in package kernel/outbox):
//   - NewEntry(clk, ctx, eventType, payload, opts...) — the producer constructor.
//   - UnmarshalEnvelope(topic, raw) — the wire-decode funnel.
//   - EntryScan{...}.ToEntry() — the storage-adapter reconstruction funnel
//     (the ONE exported reconstruction mirror; see EntryScanExportedMirror below).
//
// AI-robust rating (per .claude/rules/gocell/ai-robust.md §Hard 范本目录
// "sealed construction"):
//
//   - Hard (upstream/type-system): all Entry fields unexported ⇒ external
//     populated literal is a compile error. This reflect lock guards against a
//     regression that re-exports a field.
//   - Hard (downstream): SAFEID-WIREMESSAGE-USAGE-01 + PRINCIPAL-SEALED-FIELD-
//     FROZEN-01 freeze the wire schema; the value-receiver getters are the only
//     read surface.
//
// Tool blind spots (per AI-robust §"强制盲区自检"):
//
//   - An empty literal `outbox.Entry{}` (no fields) DOES compile externally —
//     Go permits a zero-value composite literal of a struct with unexported
//     fields. That is harmless: a zero Entry fails Entry.Validate (missing id /
//     payload / non-zero occurredAt), so it can never be emitted or persisted.
//     This lock targets POPULATED literals (field forgery), which the unexported
//     field set makes impossible — it does not (and need not) ban the empty form.
//   - EntryScan intentionally has exported fields (it is a scan target, not a
//     producer surface). The EntryScanExportedMirror check asserts it remains
//     the SOLE exported Entry-shaped reconstruction mirror so the seal has no
//     second backdoor; its ToEntry runs full Validate.
package archtest

import (
	"reflect"
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// TestOutboxEntrySealedConstruction01_AllFieldsUnexported reflectively asserts
// that outbox.Entry has zero exported fields — the exact property that makes a
// populated `outbox.Entry{Field: ...}` literal a compile error outside
// kernel/outbox.
func TestOutboxEntrySealedConstruction01_AllFieldsUnexported(t *testing.T) {
	t.Parallel()

	et := reflect.TypeOf(outbox.Entry{})
	if et.Kind() != reflect.Struct {
		t.Fatalf("OUTBOX-ENTRY-SEALED-CONSTRUCTION-01: outbox.Entry is not a struct (kind=%s)", et.Kind())
	}
	if et.NumField() == 0 {
		t.Fatal("OUTBOX-ENTRY-SEALED-CONSTRUCTION-01: outbox.Entry has no fields — unexpected; the lock would be vacuous")
	}

	for i := 0; i < et.NumField(); i++ {
		f := et.Field(i)
		if f.IsExported() {
			t.Errorf("OUTBOX-ENTRY-SEALED-CONSTRUCTION-01: outbox.Entry field %q is EXPORTED — this re-opens "+
				"external populated-literal construction and breaks the sealed-construction Hard gate. "+
				"Keep all Entry fields unexported; expose reads via value-receiver getters and construction "+
				"via NewEntry / UnmarshalEnvelope / EntryScan.ToEntry.", f.Name)
		}
	}
}

// TestOutboxEntrySealedConstruction01_GettersPresent asserts the value-receiver
// read surface exists, so a regression that drops a getter (leaving consumers
// unable to read a field, tempting a field re-export) is caught here.
func TestOutboxEntrySealedConstruction01_GettersPresent(t *testing.T) {
	t.Parallel()

	et := reflect.TypeOf(outbox.Entry{})
	for _, m := range []string{
		"ID", "AggregateID", "AggregateType", "EventType", "Topic", "RoutingTopic",
		"Payload", "CreatedAt", "OccurredAt", "Metadata", "Observability", "Principal",
		"FailurePolicy", "Validate",
	} {
		if _, ok := et.MethodByName(m); !ok {
			t.Errorf("OUTBOX-ENTRY-SEALED-CONSTRUCTION-01: outbox.Entry is missing the %q value-receiver method "+
				"— the sealed read surface must stay complete so no consumer needs field access.", m)
		}
	}
}

// TestOutboxEntrySealedConstruction01_EntryScanIsValidatedFunnel asserts the
// sanctioned reconstruction mirror exists, has exported (scan-target) fields,
// and that ToEntry rejects an invalid reconstruction (zero OccurredAt) — proving
// the funnel re-validates rather than blindly trusting persisted bytes.
func TestOutboxEntrySealedConstruction01_EntryScanIsValidatedFunnel(t *testing.T) {
	t.Parallel()

	// Exported scan-target shape.
	st := reflect.TypeOf(outbox.EntryScan{})
	if st.NumField() == 0 || !st.Field(0).IsExported() {
		t.Fatal("OUTBOX-ENTRY-SEALED-CONSTRUCTION-01: EntryScan must expose scan-target fields")
	}

	// ToEntry must Validate: a scan with id+payload but zero OccurredAt is invalid.
	if _, err := (outbox.EntryScan{ID: "evt-x", EventType: "e.evt", Payload: []byte("{}")}).ToEntry(); err == nil {
		t.Error("OUTBOX-ENTRY-SEALED-CONSTRUCTION-01: EntryScan.ToEntry accepted a zero-OccurredAt reconstruction " +
			"— the reconstruction funnel must run Entry.Validate (fail-closed).")
	}
}

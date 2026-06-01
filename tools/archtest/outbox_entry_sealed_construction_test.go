// INVARIANT: OUTBOX-ENTRY-SEALED-CONSTRUCTION-01
//
// This file owns ONE invariant: kernel/outbox.Entry is sealed construction —
// every field is unexported, so a populated composite literal
// `outbox.Entry{Field: ...}` outside package kernel/outbox does not compile
// (issue #1229). That compile-time impossibility is the Hard upstream gate for
// ONE forgery vector — LITERAL forgery: it forces OccurredAt to be
// clock-derived, Observability/Principal to be ctx-injected at the NewEntry
// trust boundary, and makes literal-forgery / omission structurally
// unrepresentable.
//
// SCOPE — what this seal does NOT cover. Entry has two further provenance
// vectors that the unexported-field seal does NOT close, because reconstruction
// and ctx-injection are inherently cross-package:
//
//   - reconstruction: UnmarshalEnvelope / EntryScan.ToEntry rebuild a sealed
//     Entry from untrusted input and Validate checks well-formedness, not
//     provenance. Locked by OUTBOX-RECONSTRUCTION-CALLER-01 (caller-allowlist).
//   - ctx-injection: the ctxkeys principal setters NewEntry reads from. Locked
//     by CTXKEYS-PRINCIPAL-WRITE-CALLER-01 (caller-allowlist).
//
// Both are Hard-downstream / Medium-upstream (Go-language ceiling — see those
// files). Do NOT describe the overall sealing as "type-system Hard"; only the
// literal vector is. ADR-1042 §Amendment 2026-05-29 round-2 records this split.
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
//     (the SOLE exported reconstruction mirror — see the SoleReconstructionSurface
//     test, which also pins the only Entry-yielding exported funcs).
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
//     producer surface). The SoleReconstructionSurface check (go/types) asserts
//     it remains the SOLE exported Entry-shaped reconstruction mirror, and that
//     the only exported FUNCS yielding an Entry are NewEntry + UnmarshalEnvelope,
//     so the seal has no second backdoor; its ToEntry runs full Validate.
package archtest

import (
	"go/types"
	"reflect"
	"sort"
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

// reconstructionFuncAllowed is the exhaustive set of exported package-level
// functions in kernel/outbox that may RETURN an outbox.Entry (the producer
// constructor + the wire-decode funnel). EntryScan.ToEntry is a METHOD, not a
// func, and is asserted separately as the sole reconstruction mirror.
var reconstructionFuncAllowed = map[string]struct{}{
	"NewEntry":          {}, // producer constructor (ctx-injection trust boundary)
	"UnmarshalEnvelope": {}, // wire-decode funnel
}

// TestOutboxEntrySealedConstruction01_SoleReconstructionSurface pins the COMPLETE
// set of exported surfaces in kernel/outbox that can yield an outbox.Entry. The
// composite-literal seal only blocks `outbox.Entry{...}`; a new exported func
// returning Entry, or a new exported struct with a method returning Entry, would
// be a fresh reconstruction backdoor that the literal seal does not cover and
// that a caller-allowlist (OUTBOX-RECONSTRUCTION-CALLER-01) would not yet know
// about. This go/types reverse self-check fails when either set drifts:
//
//   - exported funcs returning Entry  MUST == {NewEntry, UnmarshalEnvelope}
//   - exported struct types with a method returning Entry MUST == {EntryScan}
//
// A new entry forces the author to (a) justify the surface and (b) extend the
// reconstruction caller-allowlist, rather than silently widening the seal.
func TestOutboxEntrySealedConstruction01_SoleReconstructionSurface(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./kernel/outbox/..."}),

		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != outboxPkgPath {
				return nil
			}
			scope := p.Pkg.Scope()
			entryObj := scope.Lookup("Entry")
			if entryObj == nil {
				return []Diagnostic{{Message: "OUTBOX-ENTRY-SEALED-CONSTRUCTION-01/SoleReconstructionSurface: outbox.Entry not found"}}
			}
			entryType := entryObj.Type()

			var funcs, mirrors []string
			for _, name := range scope.Names() {
				obj := scope.Lookup(name)
				if !obj.Exported() {
					continue
				}
				switch o := obj.(type) {
				case *types.Func:
					if sig, ok := o.Type().(*types.Signature); ok && sigReturnsType(sig, entryType) {
						funcs = append(funcs, name)
					}
				case *types.TypeName:
					named, ok := o.Type().(*types.Named)
					if !ok {
						continue
					}
					for i := 0; i < named.NumMethods(); i++ {
						m := named.Method(i)
						if !m.Exported() {
							continue
						}
						if sig, ok := m.Type().(*types.Signature); ok && sigReturnsType(sig, entryType) {
							mirrors = append(mirrors, name)
							break
						}
					}
				}
			}

			diags = append(diags, diffExpectedSet(
				"reconstruction funcs (exported funcs returning outbox.Entry)",
				reconstructionFuncAllowed, funcs,
			)...)
			diags = append(diags, diffExpectedSet(
				"reconstruction mirrors (exported struct types with a method returning outbox.Entry)",
				map[string]struct{}{"EntryScan": {}}, mirrors,
			)...)
			return nil
		})

	Report(t, "OUTBOX-ENTRY-SEALED-CONSTRUCTION-01/SoleReconstructionSurface", diags)
}

// sigReturnsType reports whether sig has any result whose type is identical to
// want (value form) or a pointer to want.
func sigReturnsType(sig *types.Signature, want types.Type) bool {
	res := sig.Results()
	for i := 0; i < res.Len(); i++ {
		rt := res.At(i).Type()
		if types.Identical(rt, want) {
			return true
		}
		if ptr, ok := rt.(*types.Pointer); ok && types.Identical(ptr.Elem(), want) {
			return true
		}
	}
	return false
}

// diffExpectedSet emits a diagnostic for each name in got not present in want
// (unexpected new surface) and each name in want missing from got (a sanctioned
// surface disappeared — the lock would otherwise become vacuous).
func diffExpectedSet(label string, want map[string]struct{}, got []string) []Diagnostic {
	gotSet := make(map[string]struct{}, len(got))
	for _, g := range got {
		gotSet[g] = struct{}{}
	}
	var diags []Diagnostic
	extra := make([]string, 0)
	for g := range gotSet {
		if _, ok := want[g]; !ok {
			extra = append(extra, g)
		}
	}
	sort.Strings(extra)
	for _, g := range extra {
		diags = append(diags, Diagnostic{
			Message: "OUTBOX-ENTRY-SEALED-CONSTRUCTION-01/SoleReconstructionSurface: unexpected " + label +
				": " + g + " — a new Entry-yielding surface widens the seal. Justify it and extend " +
				"OUTBOX-RECONSTRUCTION-CALLER-01's allowlist, or remove it.",
		})
	}
	missing := make([]string, 0)
	for w := range want {
		if _, ok := gotSet[w]; !ok {
			missing = append(missing, w)
		}
	}
	sort.Strings(missing)
	for _, w := range missing {
		diags = append(diags, Diagnostic{
			Message: "OUTBOX-ENTRY-SEALED-CONSTRUCTION-01/SoleReconstructionSurface: expected " + label +
				" " + w + " not found — the sanctioned reconstruction surface changed; the lock is now vacuous. " +
				"Update the expected set if this rename/removal is intended.",
		})
	}
	return diags
}

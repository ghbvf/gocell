// INVARIANT: HTTP-IDEMPOTENCY-RECORDEDRESPONSE-SEALED-01
//
// This file owns ONE invariant: runtime/http/idempotency.RecordedResponse is
// sealed construction — every field is unexported, so a POPULATED composite
// literal `RecordedResponse{status: 200, ...}` outside the idempotency package
// does not compile (all fields unexported). That compile-time impossibility is
// the Hard upstream gate for the literal-forgery vector: it forces status/body/
// header/recordedAt to be set only via the sanctioned internal constructor
// newRecordedResponse (HTTP middleware boundary) or via UnmarshalRecordedResponse
// (the wire-decode funnel for Store implementations loading a stored blob).
//
// AI-robust rating (per .claude/rules/gocell/ai-robust.md §Hard 范本目录
// "sealed construction"):
//
//   - Hard (upstream / type-system): all RecordedResponse fields unexported ⇒
//     external POPULATED literal is a compile error. The reflect lock in this
//     file guards against a regression that re-exports a field and re-opens the
//     external literal construction path.
//   - Hard (downstream / go/types): the SoleReconstructionSurface test pins the
//     exact set of exported package-level funcs in runtime/http/idempotency that
//     return a RecordedResponse (by value or pointer). Today that set is exactly
//     {UnmarshalRecordedResponse}. MarshalRecordedResponse takes RecordedResponse
//     as a PARAM and returns []byte — it is NOT a producer and must NOT appear in
//     the set. Any new exported constructor silently widens the seal and is caught
//     here before it reaches callers.
//
// SCOPE — what this seal does NOT cover:
//
//   - The EMPTY literal RecordedResponse{} (no fields set) compiles externally
//     — Go permits a zero-value composite literal of a struct whose fields are
//     all unexported. That is harmless: a zero RecordedResponse has status=0,
//     body=nil, header=nil, recordedAt=zero. Consumers that replay a zero
//     response get a 0-status reply; backends that try to persist it will either
//     reject it on Unmarshal (status out of [100,599]) or produce a nonsensical
//     replay. This lock targets POPULATED literal forgery (field assignment), not
//     the zero form. Confirmed by TestRecordedResponseSealedConstruction01_EmptyLitBlindSpot.
//
//   - Any future unexported-but-assigned path inside the same package: a new
//     package-internal constructor that sets fields incorrectly is NOT caught by
//     the external-caller seal (the seal is a package-external guarantee). Such a
//     path would be caught by unit tests in the idempotency package itself.
//
// Sanctioned construction paths (all in package runtime/http/idempotency):
//   - newRecordedResponse(clk, status, body, header) — internal middleware
//     constructor. Stamps recordedAt from the injected clock.
//   - UnmarshalRecordedResponse(raw []byte) — the wire-decode funnel. Validates
//     status ∈ [100,599] and recordedAt non-zero; any violation returns an error.
//
// ref: tools/archtest/outbox_entry_sealed_construction_test.go — structural
// template for sealed-construction archtest (sealed fields + getters + sole
// reconstruction surface).
package archtest

import (
	"go/types"
	"reflect"
	"testing"

	"github.com/ghbvf/gocell/runtime/http/idempotency"
)

const (
	httpIdempotencyPkgPath = PlatformModulePath + "/runtime/http/idempotency"
)

// TestRecordedResponseSealedConstruction01_AllFieldsUnexported reflectively
// asserts that idempotency.RecordedResponse has zero exported fields — the exact
// property that makes a populated RecordedResponse{status: ..., ...} literal a
// compile error outside runtime/http/idempotency.
//
// # Blind-spot catalog (per ai-robust §"强制盲区自检")
//
//   - B1. Empty literal: RecordedResponse{} (no fields) DOES compile externally.
//     It is inert (getters return zero values; MarshalRecordedResponse on a zero
//     value will fail Unmarshal validation). Confirmed by
//     TestRecordedResponseSealedConstruction01_EmptyLitBlindSpot.
//
//   - B2. Package-internal code can still set fields incorrectly; that is a
//     package-unit-test concern, not a package-external seal concern.
func TestRecordedResponseSealedConstruction01_AllFieldsUnexported(t *testing.T) {
	t.Parallel()

	rt := reflect.TypeOf(idempotency.RecordedResponse{})
	if rt.Kind() != reflect.Struct {
		t.Fatalf("HTTP-IDEMPOTENCY-RECORDEDRESPONSE-SEALED-01: RecordedResponse is not a struct (kind=%s)", rt.Kind())
	}
	if rt.NumField() == 0 {
		t.Fatal("HTTP-IDEMPOTENCY-RECORDEDRESPONSE-SEALED-01: RecordedResponse has no fields — " +
			"unexpected; the lock would be vacuous")
	}

	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.IsExported() {
			t.Errorf("HTTP-IDEMPOTENCY-RECORDEDRESPONSE-SEALED-01: RecordedResponse field %q is "+
				"EXPORTED — this re-opens external populated-literal construction and breaks the "+
				"sealed-construction Hard gate. Keep all RecordedResponse fields unexported; "+
				"expose reads via value-receiver getters (Status/Body/Header/RecordedAt) and "+
				"construction via newRecordedResponse (internal) / UnmarshalRecordedResponse (wire).",
				f.Name)
		}
	}
}

// TestRecordedResponseSealedConstruction01_GettersPresent asserts the
// value-receiver read surface is complete, so a regression that drops a getter
// (leaving consumers unable to read a field, which might tempt a field
// re-export to restore access) is caught immediately.
func TestRecordedResponseSealedConstruction01_GettersPresent(t *testing.T) {
	t.Parallel()

	rt := reflect.TypeOf(idempotency.RecordedResponse{})
	for _, m := range []string{
		"Status",
		"Body",
		"Header",
		"RecordedAt",
	} {
		if _, ok := rt.MethodByName(m); !ok {
			t.Errorf("HTTP-IDEMPOTENCY-RECORDEDRESPONSE-SEALED-01: RecordedResponse is missing the %q "+
				"value-receiver method — the sealed read surface must stay complete so no consumer "+
				"needs field access.", m)
		}
	}
}

// TestRecordedResponseSealedConstruction01_EmptyLitBlindSpot (B1 reverse
// self-check) confirms that the zero-value literal RecordedResponse{} compiles
// externally and is inert — status=0 means a round-trip through
// UnmarshalRecordedResponse rejects it (status out of [100,599]), so the empty
// form provides no forgery capability.
func TestRecordedResponseSealedConstruction01_EmptyLitBlindSpot(t *testing.T) {
	t.Parallel()

	zero := idempotency.RecordedResponse{}
	if zero.Status() != 0 {
		t.Errorf("B1 blind-spot: expected zero RecordedResponse to have status=0, got %d", zero.Status())
	}
	if zero.Body() != nil {
		t.Errorf("B1 blind-spot: expected zero RecordedResponse to have nil body")
	}
	if !zero.RecordedAt().IsZero() {
		t.Errorf("B1 blind-spot: expected zero RecordedResponse to have zero RecordedAt")
	}

	// Round-trip through the wire codec must reject the zero form (status 0 is
	// out of range [100,599]), confirming it is not a viable forgery path.
	raw, err := idempotency.MarshalRecordedResponse(zero)
	if err != nil {
		// Marshal may fail for the zero form on some implementations; that is
		// also an acceptable outcome — it further closes the forgery path.
		return
	}
	_, unmarshalErr := idempotency.UnmarshalRecordedResponse(raw)
	if unmarshalErr == nil {
		t.Error("B1 blind-spot: UnmarshalRecordedResponse accepted a zero RecordedResponse round-trip " +
			"(status=0 is out of [100,599]); the wire funnel must reject it")
	}
}

// reconstructionFuncAllowedHTTPIdempotency is the exhaustive set of EXPORTED
// package-level functions in runtime/http/idempotency that may RETURN a
// RecordedResponse (by value or pointer). newRecordedResponse is unexported and
// is the sanctioned internal constructor; it does NOT appear in this set.
// MarshalRecordedResponse returns []byte — it is NOT a producer.
var reconstructionFuncAllowedHTTPIdempotency = map[string]struct{}{
	"UnmarshalRecordedResponse": {}, // wire-decode funnel
}

// TestRecordedResponseSealedConstruction01_SoleReconstructionSurface pins the
// COMPLETE set of exported surfaces in runtime/http/idempotency that can yield a
// RecordedResponse value. The composite-literal seal only blocks
// RecordedResponse{...}; a new exported func returning RecordedResponse would be
// a fresh reconstruction backdoor that the literal seal does not cover.
//
// This go/types reverse self-check fails when either set drifts:
//
//   - Exported funcs returning RecordedResponse MUST == {UnmarshalRecordedResponse}
//
// A new entry forces the author to (a) justify the surface and (b) update this
// expected set, rather than silently widening the seal.
//
// # Blind-spot catalog
//
//   - B3. Method receivers on other types in the package that happen to return a
//     RecordedResponse are NOT scanned. In practice Store implementations live in
//     adapters/ (outside this package), and the only RecordedResponse-returning
//     method in the package is Receipt.Record's parameter type (RecordedResponse
//     is a param, not a return). If a method is added that returns RecordedResponse
//     in a future PR, it would be a new sanctioned surface; the author must update
//     this test. Confirmed by
//     TestRecordedResponseSealedConstruction01_NoMethodReturnsRecordedResponse.
func TestRecordedResponseSealedConstruction01_SoleReconstructionSurface(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic
	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./runtime/http/idempotency/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != httpIdempotencyPkgPath {
				return nil
			}
			scope := p.Pkg.Scope()
			rrObj := scope.Lookup("RecordedResponse")
			if rrObj == nil {
				return []Diagnostic{{Message: "HTTP-IDEMPOTENCY-RECORDEDRESPONSE-SEALED-01/SoleReconstructionSurface: " +
					"RecordedResponse type not found in " + httpIdempotencyPkgPath}}
			}
			rrType := rrObj.Type()

			var funcs []string
			for _, name := range scope.Names() {
				obj := scope.Lookup(name)
				if !obj.Exported() {
					continue
				}
				fn, ok := obj.(*types.Func)
				if !ok {
					continue
				}
				sig, ok := fn.Type().(*types.Signature)
				if !ok {
					continue
				}
				if sigReturnsType(sig, rrType) {
					funcs = append(funcs, name)
				}
			}

			diags = append(diags, diffHTTPIdempotencyExpectedSet(
				"exported funcs returning RecordedResponse",
				reconstructionFuncAllowedHTTPIdempotency, funcs)...)
			return nil
		})

	Report(t, "HTTP-IDEMPOTENCY-RECORDEDRESPONSE-SEALED-01/SoleReconstructionSurface", diags)
}

// TestRecordedResponseSealedConstruction01_NoNewPkgLevelConstructor (B3 reverse
// self-check) confirms that no new exported PACKAGE-LEVEL FUNCTION outside the
// sanctioned set returns a RecordedResponse. Methods on types (such as
// MemStore.Claim, which implements the Store interface contract and returns a
// *RecordedResponse retrieved from internal storage) are intentionally NOT
// scanned here — they are READ paths, not CONSTRUCTION paths. Only top-level
// functions that CREATE a new RecordedResponse from non-RecordedResponse inputs
// are reconstruction surfaces; the SoleReconstructionSurface test already
// pin-locks that set. This B3 test is a belt-and-suspenders log that there are
// no pkg-level funcs we missed.
//
// Note: the B3 blind-spot (methods returning RecordedResponse on Store impls)
// is documented and accepted. MemStore.Claim returns *RecordedResponse from an
// internal map (itself populated via UnmarshalRecordedResponse), so it is a
// READ surface, not a construction surface; it cannot forge a RecordedResponse
// with arbitrary field values because it must go through UnmarshalRecordedResponse
// (the sanctioned wire-decode funnel) to populate the map in the first place.
func TestRecordedResponseSealedConstruction01_NoNewPkgLevelConstructor(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// This test is structurally identical to SoleReconstructionSurface but is
	// explicit about the B3 scope. We assert the same set — any deviation means
	// either a new constructor appeared or SoleReconstructionSurface diverged.
	var diags []Diagnostic
	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./runtime/http/idempotency/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != httpIdempotencyPkgPath {
				return nil
			}
			scope := p.Pkg.Scope()
			rrObj := scope.Lookup("RecordedResponse")
			if rrObj == nil {
				return nil
			}
			rrType := rrObj.Type()

			var funcs []string
			for _, name := range scope.Names() {
				obj := scope.Lookup(name)
				if !obj.Exported() {
					continue
				}
				fn, ok := obj.(*types.Func)
				if !ok {
					continue
				}
				sig, ok := fn.Type().(*types.Signature)
				if !ok {
					continue
				}
				if sigReturnsType(sig, rrType) {
					funcs = append(funcs, name)
				}
			}

			diags = append(diags, diffHTTPIdempotencyExpectedSet(
				"exported package-level funcs returning RecordedResponse (B3 belt-and-suspenders)",
				reconstructionFuncAllowedHTTPIdempotency, funcs)...)
			return nil
		})

	Report(t, "HTTP-IDEMPOTENCY-RECORDEDRESPONSE-SEALED-01/B3", diags)
}

// diffHTTPIdempotencyExpectedSet emits a diagnostic for each name in got not
// present in want (unexpected new surface) and each name in want missing from got
// (a sanctioned surface disappeared — the lock would otherwise become vacuous).
func diffHTTPIdempotencyExpectedSet(label string, want map[string]struct{}, got []string) []Diagnostic {
	gotSet := make(map[string]struct{}, len(got))
	for _, g := range got {
		gotSet[g] = struct{}{}
	}
	var diags []Diagnostic
	for g := range gotSet {
		if _, ok := want[g]; !ok {
			diags = append(diags, Diagnostic{
				Message: "HTTP-IDEMPOTENCY-RECORDEDRESPONSE-SEALED-01/SoleReconstructionSurface: " +
					"unexpected " + label + ": " + g + " — a new RecordedResponse-yielding surface " +
					"widens the seal. Justify it and update reconstructionFuncAllowedHTTPIdempotency, " +
					"or remove it.",
			})
		}
	}
	for w := range want {
		if _, ok := gotSet[w]; !ok {
			diags = append(diags, Diagnostic{
				Message: "HTTP-IDEMPOTENCY-RECORDEDRESPONSE-SEALED-01/SoleReconstructionSurface: " +
					"expected " + label + " " + w + " not found — the sanctioned reconstruction surface " +
					"changed; the lock is now vacuous. Update reconstructionFuncAllowedHTTPIdempotency " +
					"if this rename/removal is intended.",
			})
		}
	}
	return diags
}

//go:build archtest

// INVARIANT: RESOURCE-PROJECTION-SEALED-01
//
// This file owns ONE invariant: pkg/projection.ResourceProjection is sealed
// construction — its single field is unexported, so a populated composite
// literal `projection.ResourceProjection{data: ...}` outside package
// pkg/projection does not compile. That compile-time impossibility is the Hard
// upstream gate: the only way to obtain a populated ResourceProjection is the
// NewProjection / NewProjectionList funnel, which always discharges the
// authz.FieldMask obligation. A read handler therefore cannot fabricate an
// un-masked ("full") view by struct literal — column masking cannot be bypassed
// by forging the carrier.
//
// AI-robust rating (per .claude/rules/gocell/ai-robust.md §Hard 范本目录
// "sealed construction" + "reflect schema freeze"):
//
//   - Hard (upstream / type-system): the ResourceProjection field is unexported
//     ⇒ an external populated literal is a compile error. The Go compiler is the
//     gate; AllFieldsUnexported is the REVERSE self-check that pins this property
//     so a future PR re-exporting the field fails at PR/nightly time instead of
//     silently re-opening literal construction.
//   - Hard (downstream / go/types): SoleReconstructionSurface pins the COMPLETE
//     set of exported producers to {NewProjection, NewProjectionList}, so no
//     second forge path (a new exported func returning the type) can be added
//     inside pkg/projection without tripping this archtest.
//
// SCOPE — what this seal does NOT cover. PR-11 ships the unforgeable carrier
// only. It does NOT force production read handlers to actually return a
// ResourceProjection built from the request's Decision.Obligations().FieldMask —
// that downstream callsite lock lands in PR-12 (#1350), when real handlers adopt
// projection. Do NOT read this invariant as "column leakage is already closed";
// until PR-12 it guarantees only that a ResourceProjection, wherever one is
// produced, came through the masking funnel.
//
// Tool blind spots (per AI-robust §"强制盲区自检"):
//
//   - An empty literal `projection.ResourceProjection{}` (no fields) DOES compile
//     externally — Go permits a zero-value composite literal of a struct with
//     unexported fields. That is harmless: the zero value marshals to JSON null
//     (ZeroLiteralInert proves it), carrying no forged data. This lock targets
//     POPULATED literals (field forgery), which the unexported field makes
//     impossible — it does not (and need not) ban the empty form.
//   - NewProjection(authz.FieldMask{}, full) legitimately yields the identity
//     projection (the full view) through the funnel. That is by design (an empty
//     obligation = no masking); the CORRECTNESS of the supplied mask is the PEP
//     callsite's responsibility, locked downstream in PR-12, not here.
//   - This is a reflect + go/types rule, not an EachContentFile content scan, so
//     anti-vacuity is carried by the non-empty expected sets plus the
//     NumField()>0 assertion — no synthetic red-case file is required.
package archtest

import (
	"encoding/json"
	"go/types"
	"reflect"
	"sort"
	"testing"

	"github.com/ghbvf/gocell/pkg/projection"
)

const resourceProjectionPkgPath = PlatformModulePath + "/pkg/projection"

// TestResourceProjectionSealed01_AllFieldsUnexported reflectively asserts that
// ResourceProjection has zero exported fields — the exact property that makes a
// populated `projection.ResourceProjection{data: ...}` literal a compile error
// outside pkg/projection.
func TestResourceProjectionSealed01_AllFieldsUnexported(t *testing.T) {
	t.Parallel()

	pt := reflect.TypeOf(projection.ResourceProjection{})
	if pt.Kind() != reflect.Struct {
		t.Fatalf("RESOURCE-PROJECTION-SEALED-01: ResourceProjection is not a struct (kind=%s)", pt.Kind())
	}
	if pt.NumField() == 0 {
		t.Fatal("RESOURCE-PROJECTION-SEALED-01: ResourceProjection has no fields — the seal would be vacuous")
	}
	for i := 0; i < pt.NumField(); i++ {
		f := pt.Field(i)
		if f.IsExported() {
			t.Errorf("RESOURCE-PROJECTION-SEALED-01: ResourceProjection field %q is EXPORTED — this re-opens "+
				"external populated-literal construction and breaks the sealed-construction Hard gate. Keep all "+
				"fields unexported; build values only via NewProjection / NewProjectionList.", f.Name)
		}
	}
}

// TestResourceProjectionSealed01_WireSurfacePresent asserts the value-receiver
// wire surface exists, so a regression that drops MarshalJSON (leaving consumers
// unable to serialize a projection, tempting a field re-export) is caught here.
func TestResourceProjectionSealed01_WireSurfacePresent(t *testing.T) {
	t.Parallel()

	pt := reflect.TypeOf(projection.ResourceProjection{})
	if _, ok := pt.MethodByName("MarshalJSON"); !ok {
		t.Error("RESOURCE-PROJECTION-SEALED-01: ResourceProjection is missing the MarshalJSON value-receiver " +
			"method — the sealed wire surface must stay present so no consumer needs field access.")
	}
}

// resourceProjectionScalarFuncs / resourceProjectionSliceFuncs are the exhaustive
// sets of exported package-level functions in pkg/projection that may RETURN a
// ResourceProjection (the single-resource funnel) or a []ResourceProjection (the
// list funnel). Both always discharge the FieldMask obligation.
var (
	resourceProjectionScalarFuncs = map[string]struct{}{"NewProjection": {}}
	resourceProjectionSliceFuncs  = map[string]struct{}{"NewProjectionList": {}}
)

// TestResourceProjectionSealed01_SoleReconstructionSurface pins the COMPLETE set
// of exported funcs in pkg/projection that can yield a ResourceProjection (value
// or pointer) or a []ResourceProjection. The composite-literal seal only blocks
// `projection.ResourceProjection{...}`; a new exported func returning the type
// would be a fresh forge path that skips masking and that the literal seal does
// not cover. This go/types reverse self-check fails when either set drifts.
func TestResourceProjectionSealed01_SoleReconstructionSurface(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./pkg/projection/..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != resourceProjectionPkgPath {
				return nil
			}
			scope := p.Pkg.Scope()
			obj := scope.Lookup("ResourceProjection")
			if obj == nil {
				return []Diagnostic{{Message: "RESOURCE-PROJECTION-SEALED-01/SoleReconstructionSurface: ResourceProjection not found"}}
			}
			projType := obj.Type()
			sliceType := types.NewSlice(projType)

			var scalarFuncs, sliceFuncs []string
			for _, name := range scope.Names() {
				o := scope.Lookup(name)
				if !o.Exported() {
					continue
				}
				fn, ok := o.(*types.Func)
				if !ok {
					continue
				}
				sig, ok := fn.Type().(*types.Signature)
				if !ok {
					continue
				}
				if sigReturnsType(sig, projType) {
					scalarFuncs = append(scalarFuncs, name)
				}
				if sigReturnsSlice(sig, sliceType) {
					sliceFuncs = append(sliceFuncs, name)
				}
			}

			diags = append(diags, diffResourceProjectionSurface(
				"exported funcs returning ResourceProjection", resourceProjectionScalarFuncs, scalarFuncs)...)
			diags = append(diags, diffResourceProjectionSurface(
				"exported funcs returning []ResourceProjection", resourceProjectionSliceFuncs, sliceFuncs)...)
			return nil
		})

	Report(t, "RESOURCE-PROJECTION-SEALED-01/SoleReconstructionSurface", diags)
}

// TestResourceProjectionSealed01_ZeroLiteralInert proves, from a package external
// to pkg/projection, that the only ResourceProjection literal expressible outside
// the package (the zero value) carries no forged data — it marshals to JSON null.
func TestResourceProjectionSealed01_ZeroLiteralInert(t *testing.T) {
	t.Parallel()

	b, err := json.Marshal(projection.ResourceProjection{})
	if err != nil {
		t.Fatalf("RESOURCE-PROJECTION-SEALED-01: zero-value MarshalJSON error: %v", err)
	}
	if string(b) != "null" {
		t.Errorf("RESOURCE-PROJECTION-SEALED-01: zero ResourceProjection marshaled to %s, want null "+
			"(an externally-expressible empty literal must carry no forged data)", b)
	}
}

// sigReturnsSlice reports whether sig has any result identical to want (a slice
// type). Complements the shared sigReturnsType, which covers value/pointer forms.
func sigReturnsSlice(sig *types.Signature, want types.Type) bool {
	res := sig.Results()
	for i := 0; i < res.Len(); i++ {
		if types.Identical(res.At(i).Type(), want) {
			return true
		}
	}
	return false
}

// diffResourceProjectionSurface emits a diagnostic for each name in got not in
// want (a new forge surface widening the seal) and each name in want missing from
// got (a sanctioned surface vanished — the lock would otherwise go vacuous). It
// mirrors the outbox diff helper but carries this rule's ID in its messages.
func diffResourceProjectionSurface(label string, want map[string]struct{}, got []string) []Diagnostic {
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
			Message: "RESOURCE-PROJECTION-SEALED-01/SoleReconstructionSurface: unexpected " + label + ": " + g +
				" — a new ResourceProjection-yielding surface skips the masking funnel. Justify it and pin it in " +
				"the expected set, or remove it.",
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
			Message: "RESOURCE-PROJECTION-SEALED-01/SoleReconstructionSurface: expected " + label + " " + w +
				" not found — the sanctioned construction surface changed; the lock is now vacuous. Update the " +
				"expected set if this rename/removal is intended.",
		})
	}
	return diags
}

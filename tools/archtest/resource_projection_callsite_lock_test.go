//go:build archtest

// INVARIANT: RESOURCE-PROJECTION-CALLSITE-LOCK-01
//
// This file owns ONE invariant: the DOWNSTREAM half of the column-masking
// funnel. For EVERY HTTP contract whose endpoints.http.responseProjection marker
// is true, the generated Response.Data field MUST be typed as the sealed carrier
// projection.ResourceProjection (single `data:object`) or
// []projection.ResourceProjection (`data[]:object`) — never a plain DTO. Because
// the carrier is sealed (RESOURCE-PROJECTION-SEALED-01) and obtainable only via
// the NewProjection / NewProjectionList masking funnel, pinning the generated
// field to that type makes a full, un-masked view NON-ASSIGNABLE at the handler
// callsite: a handler physically cannot hand back a raw map. This is the
// callsite lock that RESOURCE-PROJECTION-SEALED-01's godoc previously deferred to
// PR-12 (#1350).
//
// The seal (upstream, type-system Hard) makes the carrier unforgeable; this lock
// (downstream, go/types Hard) pins the generated Response.Data to that carrier so
// the unforgeability is actually load-bearing at the read endpoint. Global
// coverage — that every resource-bearing GET read IS marked, so no read can
// silently skip the funnel — is the sibling RESOURCE-PROJECTION-COVERAGE-01.
//
// # AI-robust rating (charter §Hard 范本 "reflect schema freeze" / go/types pin)
//
//   - Downstream HARD (go/types): the generated Response.Data field's TYPE is
//     pinned to projection.ResourceProjection / []projection.ResourceProjection
//     by this go/types identity check. A contractgen template regression that
//     re-points Data at a plain `[]*ResponseDataItem` (the un-masked DTO the
//     generator emitted before the projection rewrite) changes the field type
//     and trips this archtest. The compiler is the enforcement at the handler
//     callsite (an un-masked map is not assignable to a sealed-carrier field);
//     this archtest is the regression guard that the GENERATED type stays the
//     sealed carrier. The reverse self-check (BadResponse fixture) proves the
//     detector flags a non-carrier Data and passes the carrier controls.
//
// # Tool blind spots (charter §强制盲区自检)
//
//   - The check is keyed on the responseProjection marker. A resource-read GET
//     that is missing the marker entirely is NOT caught here — that gap is closed
//     by RESOURCE-PROJECTION-COVERAGE-01 (every resource-bearing GET must be
//     marked). The two rules are the complementary halves: COVERAGE forces the
//     marker on; this lock forces the marker's effect (the carrier field type)
//     to be present.
//   - It pins Response.Data only (the data envelope). A masked column smuggled
//     into a SIBLING top-level field (e.g. a second `meta` object) is not seen;
//     the masking model is `data`-envelope scoped by construction (single source
//     is the response schema's top-level `data` property — see
//     RESOURCE-PROJECTION-COVERAGE-01).
//   - Anti-vacuity is the non-empty marked-set assertion: if zero contracts are
//     marked (e.g. the marker is silently dropped from every contract.yaml), the
//     production test FAILS rather than passing vacuously.
package archtest

import (
	"fmt"
	"go/types"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

const projectionCallsiteFixturePkg = "./tools/archtest/internal/projectioncallsitefixture"

// resourceProjectionFixtureGreen / resourceProjectionFixtureRed are the exported
// fixture Response-shaped types the reverse self-check inspects. Green types have
// Data typed as the sealed carrier; the red type does not.
var (
	resourceProjectionFixtureGreen = []string{"GoodScalarResponse", "GoodSliceResponse"}
	resourceProjectionFixtureRed   = []string{"BadResponse"}
)

// TestResourceProjectionCallsiteLock01 asserts that every responseProjection-
// marked HTTP contract's generated Response.Data field is the sealed
// projection.ResourceProjection carrier (scalar or slice). It loads each marked
// contract's generated package via go/types, resolves the ResourceProjection
// named type from that package's pkg/projection import, and flags any
// Response.Data that is a different type.
func TestResourceProjectionCallsiteLock01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	project := mustParseProjectContracts(t, root)

	// want maps each marked contract's generated package import path → contract ID.
	want := markedProjectionPackages(project)
	if len(want) == 0 {
		t.Fatal("RESOURCE-PROJECTION-CALLSITE-LOCK-01: zero contracts carry endpoints.http.responseProjection " +
			"— the lock would be vacuous. After PR-12 the 10 GET read contracts are marked; if the marker was " +
			"dropped from every contract.yaml, restore it (or fix the parse).")
	}

	seen := map[string]struct{}{}
	var diags []Diagnostic

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./generated/contracts/http/..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			contractID, marked := want[p.Pkg.Path()]
			if !marked {
				return nil
			}
			seen[p.Pkg.Path()] = struct{}{}
			projType := resolveResourceProjectionFromImports(p)
			if projType == nil {
				diags = append(diags, Diagnostic{Message: fmt.Sprintf(
					"RESOURCE-PROJECTION-CALLSITE-LOCK-01: generated package %s (contract %q) does not import "+
						"pkg/projection, so its Response.Data cannot be the sealed carrier; regenerate with "+
						"`gocell generate contract %s`.", p.Pkg.Path(), contractID, contractID)})
				return nil
			}
			diags = append(diags, checkResponseDataCarrier(p.Pkg, "Response", projType, contractID)...)
			return nil
		})

	// Anti-vacuity / no-stale: every marked contract's generated package must have
	// been loaded and inspected, else a silently-missing package would pass.
	for path, id := range want {
		if _, ok := seen[path]; !ok {
			diags = append(diags, Diagnostic{Message: fmt.Sprintf(
				"RESOURCE-PROJECTION-CALLSITE-LOCK-01: marked contract %q expects generated package %s but it was "+
					"not loaded by the go/types scan; run `gocell generate contract %s` so the Response carrier can "+
					"be pinned.", id, path, id)})
		}
	}

	Report(t, "RESOURCE-PROJECTION-CALLSITE-LOCK-01", diags)
}

// TestResourceProjectionCallsiteLock01_ScannerCatchesViolation is the reverse
// self-check: it runs the SAME field-carrier detector (checkResponseDataCarrier)
// over the build-tagged RED/GREEN fixture and asserts it flags exactly the
// non-carrier Response (BadResponse) and NONE of the carrier controls
// (GoodScalarResponse / GoodSliceResponse). This proves the detector is not
// vacuous — it genuinely distinguishes a sealed-carrier Data from a plain DTO.
func TestResourceProjectionCallsiteLock01_ScannerCatchesViolation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var greenDiags, redDiags []Diagnostic

	_ = Run(t, Fixture(FixtureOpts{Tests: false}, []string{projectionCallsiteFixturePkg}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			projType := resolveResourceProjectionFromImports(p)
			if projType == nil {
				t.Fatal("RESOURCE-PROJECTION-CALLSITE-LOCK-01 self-check: fixture package must import pkg/projection")
			}
			for _, name := range resourceProjectionFixtureGreen {
				greenDiags = append(greenDiags, checkResponseDataCarrier(p.Pkg, name, projType, "fixture/"+name)...)
			}
			for _, name := range resourceProjectionFixtureRed {
				redDiags = append(redDiags, checkResponseDataCarrier(p.Pkg, name, projType, "fixture/"+name)...)
			}
			return nil
		})

	if len(greenDiags) != 0 {
		t.Errorf("RESOURCE-PROJECTION-CALLSITE-LOCK-01 self-check: detector over-flagged the carrier controls "+
			"(Data IS projection.ResourceProjection): %+v", greenDiags)
	}
	if len(redDiags) != len(resourceProjectionFixtureRed) {
		t.Fatalf("RESOURCE-PROJECTION-CALLSITE-LOCK-01 self-check: detector must flag exactly the %d non-carrier "+
			"fixture Response type(s) (Data is a plain DTO), got %d: %+v",
			len(resourceProjectionFixtureRed), len(redDiags), redDiags)
	}
}

// markedProjectionPackages returns the generated-package import path → contract
// ID map for every HTTP contract carrying endpoints.http.responseProjection.
func markedProjectionPackages(project *metadata.ProjectMeta) map[string]string {
	out := map[string]string{}
	for _, c := range project.Contracts {
		if c.Kind != "http" || c.Endpoints.HTTP == nil || !c.Endpoints.HTTP.ResponseProjection {
			continue
		}
		out[PlatformModulePath+"/"+contractIDToExpectedPkgPath(c.ID)] = c.ID
	}
	return out
}

// resolveResourceProjectionFromImports returns the projection.ResourceProjection
// named type as seen by p (resolved from p's pkg/projection import), or nil if p
// does not import pkg/projection. Resolving the type from the SAME loaded program
// the generated package belongs to guarantees types.Identical comparisons are
// against the same type identity the generated field references.
func resolveResourceProjectionFromImports(p *Pass) types.Type {
	if p.Pkg == nil {
		return nil
	}
	for _, imp := range p.Pkg.Imports() {
		if imp.Path() != resourceProjectionPkgPath {
			continue
		}
		obj := imp.Scope().Lookup("ResourceProjection")
		if obj == nil {
			return nil
		}
		return obj.Type()
	}
	return nil
}

// checkResponseDataCarrier looks up the named struct typeName in pkg, finds its
// Data field, and returns a diagnostic unless that field's type is identical to
// projType (scalar carrier) or []projType (list carrier). It is the SINGLE
// detection path shared by the production test and the fixture reverse
// self-check, so detector drift cannot pass the self-check.
func checkResponseDataCarrier(pkg *types.Package, typeName string, projType types.Type, contractID string) []Diagnostic {
	obj := pkg.Scope().Lookup(typeName)
	if obj == nil {
		return []Diagnostic{{Message: fmt.Sprintf(
			"RESOURCE-PROJECTION-CALLSITE-LOCK-01: contract %q generated package %s has no %s type; "+
				"regenerate with `gocell generate contract %s`.", contractID, pkg.Path(), typeName, contractID)}}
	}
	st, ok := underlyingStruct(obj.Type())
	if !ok {
		return []Diagnostic{{Message: fmt.Sprintf(
			"RESOURCE-PROJECTION-CALLSITE-LOCK-01: contract %q type %s is not a struct.", contractID, typeName)}}
	}
	field, found := structFieldByName(st, "Data")
	if !found {
		return []Diagnostic{{Message: fmt.Sprintf(
			"RESOURCE-PROJECTION-CALLSITE-LOCK-01: contract %q type %s has no Data field — a responseProjection "+
				"contract must wrap its resource in a `data` envelope.", contractID, typeName)}}
	}
	if dataFieldIsCarrier(field.Type(), projType) {
		return nil
	}
	return []Diagnostic{{Message: fmt.Sprintf(
		"RESOURCE-PROJECTION-CALLSITE-LOCK-01: contract %q (%s) Data field type is %s, want "+
			"projection.ResourceProjection or []projection.ResourceProjection — the responseProjection marker "+
			"requires the generated Data to be the sealed masking carrier so an un-masked view is non-assignable "+
			"at the handler callsite. A plain-DTO Data is a contractgen template regression; regenerate with "+
			"`gocell generate contract %s`.", contractID, typeName, field.Type().String(), contractID)}}
}

// dataFieldIsCarrier reports whether t is the sealed carrier projType or a slice
// of it ([]projType).
func dataFieldIsCarrier(t, projType types.Type) bool {
	if types.Identical(t, projType) {
		return true
	}
	if sl, ok := t.(*types.Slice); ok && types.Identical(sl.Elem(), projType) {
		return true
	}
	return false
}

// underlyingStruct returns t's underlying *types.Struct, if any.
func underlyingStruct(t types.Type) (*types.Struct, bool) {
	st, ok := t.Underlying().(*types.Struct)
	return st, ok
}

// structFieldByName returns the named field of st.
func structFieldByName(st *types.Struct, name string) (*types.Var, bool) {
	for i := 0; i < st.NumFields(); i++ {
		if st.Field(i).Name() == name {
			return st.Field(i), true
		}
	}
	return nil, false
}

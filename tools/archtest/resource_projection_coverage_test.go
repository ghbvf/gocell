//go:build archtest

// INVARIANT: RESOURCE-PROJECTION-COVERAGE-01
//
// This file owns ONE invariant: GLOBAL coverage of the column-masking funnel
// across read endpoints (FR-016). For EVERY kind=http contract whose
// endpoints.http.method is GET AND whose response schema has a top-level `data`
// property that is an object (single resource) OR an array of objects (resource
// list), the contract MUST set endpoints.http.responseProjection: true — unless
// it is in the function-level carve-out registry. A new resource-bearing GET
// read therefore cannot silently bypass the masking funnel: omitting the marker
// trips this archtest.
//
// This is the COVERAGE half of the funnel; the EFFECT half is
// RESOURCE-PROJECTION-CALLSITE-LOCK-01 (the marker, once set, forces the
// generated Response.Data to the sealed carrier). Together: every resource-read
// GET is marked (here) ⇒ its Data is the masking carrier (callsite lock) ⇒ an
// un-masked view is non-assignable at the handler. The carrier itself is
// unforgeable (RESOURCE-PROJECTION-SEALED-01).
//
// # Carve-out registry (error-handling.md §Carve-out — function-level only)
//
// resourceReadProjectionCarveOut lists contract IDs that are GET reads with a
// `data` resource response but are LEGITIMATELY exempt from the masking funnel
// (e.g. a GET whose composite `data` is genuinely non-maskable). It currently
// holds ONE entry — http.admin.health.cells.v1 (#1860): a runtime/global
// cell-health read with no tenant-scoped rows and no maskable column axis (a
// {overall,cells,adapters} composite). Per error-handling.md §Carve-out the
// carve-out is function-level
// (a Go map, not a file/package exemption); ANY addition or removal MUST be
// synced with the carve-out ADR registry in the same PR. Adding an entry without
// an ADR, or leaving an entry whose contract is in fact already marked (a stale
// slot), is a drift this rule reports.
//
// # AI-robust rating (charter §分级 — Medium type-aware/metadata scan)
//
//   - MEDIUM (metadata + schema scan): a new unmarked resource-read GET trips CI
//     here. The "must be marked" obligation cannot be expressed in Go's type
//     system (a contract.yaml is data, not Go), so the enforcement is a
//     governance-style archtest scan — the Go ceiling for a metadata predicate.
//     The marker's EFFECT is escalated to Hard downstream by
//     RESOURCE-PROJECTION-CALLSITE-LOCK-01 (go/types field pin).
//   - SINGLE-SOURCE schema parse: the resource-shape predicate resolves the
//     response schema through the SAME parser the code generator uses
//     (contractgen.Parse — recursive $ref resolution included), not a second
//     hand-rolled JSON walk. So the guard cannot diverge from what the generator
//     actually materializes: a `data` that generates a DTO is seen as a resource
//     here, closing the funnel's discovery side at the same fidelity as its
//     production side.
//
// # Tool blind spots (charter §强制盲区自检)
//
//   - Platform-scope. The masking funnel (FR-016) is a PLATFORM feature; this
//     rule scopes to platform contracts (contract dir under contracts/). The
//     examples/* consumer projects (ssobff / todoorder / iotdevice / demo /
//     orderfulfillment) ship their own demonstrative GET reads that are out of
//     the platform masking scope and are NOT required to adopt the carrier; they
//     are excluded by isPlatformContract. A platform contract is the single
//     enforced surface.
//   - GET-only. A resource-bearing read served via a NON-GET method (e.g. a
//     POST-search endpoint that returns a `data` list) is not seen — the masking
//     model today scopes read-projection to GET reads (single source: the GET
//     method gate). Extend the method set here if a non-GET read convention is
//     introduced, in the same PR as the convention.
//   - `data`-envelope scoped. A response that returns a resource WITHOUT the
//     top-level `data` wrapper (a bare top-level object) is not flagged — the
//     envelope convention (`data` / `nextCursor` / `hasMore`) is the single
//     structural signal. Responses that are pure scalars / status objects
//     (no `data`) are correctly exempt (the green control proves this).
//   - $ref IS resolved. Because the predicate parses via contractgen.Parse, a
//     response that uses a $ref for the `data` value (or for `data.items`) is
//     resolved to its target schema and detected as resource-bearing — it cannot
//     skip enforcement. The red fixtures red_data_ref / red_data_items_ref pin
//     this (previously a documented blind spot; closed in PR-12 F2).
//   - Anti-vacuity is the non-empty resource-read-GET set assertion (≥1; ≥11
//     after #1860). If the enumeration collapses to zero (e.g. the parser stops
//     surfacing GET endpoints), the production test FAILS rather than passing
//     vacuously.
package archtest

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen/contractgen"
)

// resourceReadProjectionCarveOut is the function-level carve-out registry for GET
// reads with a `data` resource response that are legitimately exempt from the
// masking funnel. Holds http.admin.health.cells.v1 (#1860); see file godoc
// §Carve-out for the rationale. Every change syncs the carve-out ADR registry.
var resourceReadProjectionCarveOut = map[string]struct{}{
	// http.admin.health.cells.v1 (#1860): the syscore aggregated cell-health read
	// is runtime/global observability state — NOT tenant-scoped, NO PII, NO
	// per-tenant rows. There is no maskable column axis, so routing it through the
	// tenant column-masking funnel (responseProjection) would be dishonest (an
	// identity mask over a composite, non-tabular {overall,cells,adapters} body).
	// Legitimately exempt per the rule's "genuinely non-maskable resource" branch.
	// Carve-out rationale + threat model: docs/architecture/202606130640-1860-adr-syscore-health-aggregation.md.
	"http.admin.health.cells.v1": {},
}

// minExpectedResourceReadGETs is the anti-vacuity floor for the resource-read GET
// scan. Update when platform GET resource-reads are added or removed; the floor
// catches a schema-predicate regression that silently drops contracts from the
// scan (e.g. if responseHasTopLevelDataResource stops detecting the data envelope
// shape, the count would collapse to zero and the test would pass vacuously).
// The scan counts every resource-bearing GET before the carve-out gate; this
// floor is the STABLE minimum (projection-marked platform reads + the #1860
// carve-out = 11). The live count can sit above it while a newly-merged unmarked
// read still awaits its marker, so the floor is a lower bound, not the exact total.
const minExpectedResourceReadGETs = 11

// TestResourceProjectionCoverage01 asserts every resource-bearing GET read
// contract is responseProjection-marked (or carved out), and that no carve-out
// slot is stale. It orchestrates three steps — scan, anti-vacuity floor, stale
// carve-out reverse check — each factored into a helper to stay within the
// project cognitive-complexity budget.
func TestResourceProjectionCoverage01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	project := mustParseProjectContracts(t, root)

	diags, resourceReadCount, observedCarveOut := scanResourceReadCoverage(root, project.Contracts)
	assertAntiVacuityFloor(t, resourceReadCount)
	diags = append(diags, staleCarveOutDiags(observedCarveOut)...)
	Report(t, "RESOURCE-PROJECTION-COVERAGE-01", diags)
}

// scanResourceReadCoverage walks every platform GET contract, classifies the
// resource-bearing ones, and returns the coverage diagnostics, the count of
// resource-read GETs seen (for the anti-vacuity floor), and the set of carve-out
// IDs actually observed (for the stale-slot reverse check).
func scanResourceReadCoverage(
	root string, contracts map[string]*metadata.ContractMeta,
) (diags []Diagnostic, resourceReadCount int, observedCarveOut map[string]struct{}) {
	observedCarveOut = map[string]struct{}{}
	for _, c := range contracts {
		if !isGETPlatformContract(c) {
			continue
		}
		refPath := filepath.Join(c.Dir, c.SchemaRefs.Response)
		hasResource, err := responseHasTopLevelDataResource(root, refPath)
		if err != nil {
			diags = append(diags, Diagnostic{Message: fmt.Sprintf(
				"RESOURCE-PROJECTION-COVERAGE-01: GET contract %q response schema %s could not be read/parsed: %v",
				c.ID, c.SchemaRefs.Response, err)})
			continue
		}
		if !hasResource {
			continue // bare scalar / status response, no maskable resource
		}
		resourceReadCount++
		if d := coverageDiagForContract(c, observedCarveOut); d != nil {
			diags = append(diags, *d)
		}
	}
	return diags, resourceReadCount, observedCarveOut
}

// isGETPlatformContract reports whether c is an in-scope platform GET http
// contract (the cheap metadata gate before the schema parse).
func isGETPlatformContract(c *metadata.ContractMeta) bool {
	if c.Kind != "http" || c.Endpoints.HTTP == nil || c.Endpoints.HTTP.Method != "GET" {
		return false
	}
	return isPlatformContract(c)
}

// coverageDiagForContract classifies one resource-bearing GET read: it records a
// carve-out observation and returns a diagnostic when the contract is unmarked
// (and not carved) or when it is a stale carve-out slot (carved AND marked), else
// nil. observedCarveOut is mutated to record carve-out hits for the reverse check.
func coverageDiagForContract(c *metadata.ContractMeta, observedCarveOut map[string]struct{}) *Diagnostic {
	marked := c.Endpoints.HTTP.ResponseProjection
	if _, carved := resourceReadProjectionCarveOut[c.ID]; carved {
		observedCarveOut[c.ID] = struct{}{}
		if marked {
			d := Diagnostic{Message: fmt.Sprintf(
				"RESOURCE-PROJECTION-COVERAGE-01: contract %q is BOTH carved out AND responseProjection-marked "+
					"— the carve-out slot is stale. Remove it from resourceReadProjectionCarveOut (and the "+
					"carve-out ADR registry) so it cannot mask a future regression.", c.ID)}
			return &d
		}
		return nil
	}
	if !marked {
		d := projectionCoverageDiag(c.ID)
		return &d
	}
	return nil
}

// assertAntiVacuityFloor fails the test when fewer than the expected floor of
// resource-read GETs were enumerated — the scan would otherwise pass vacuously if
// the schema predicate silently stopped surfacing contracts.
func assertAntiVacuityFloor(t *testing.T, resourceReadCount int) {
	t.Helper()
	if resourceReadCount < minExpectedResourceReadGETs {
		t.Fatalf("RESOURCE-PROJECTION-COVERAGE-01: only %d resource-bearing GET reads enumerated (floor: %d) — "+
			"the coverage check may be vacuous or contracts were removed without updating the floor. "+
			"The stable minimum is 11 (projection-marked platform reads + the #1860 carve-out); if the "+
			"enumeration collapsed, fix the parse or the response-schema convention, or update minExpectedResourceReadGETs.",
			resourceReadCount, minExpectedResourceReadGETs)
	}
}

// staleCarveOutDiags is the no-stale-carve-out reverse check: every carve-out
// entry must correspond to a live resource-read GET, else it is a dead bypass slot.
func staleCarveOutDiags(observedCarveOut map[string]struct{}) []Diagnostic {
	var diags []Diagnostic
	for id := range resourceReadProjectionCarveOut {
		if _, seen := observedCarveOut[id]; !seen {
			diags = append(diags, Diagnostic{Message: fmt.Sprintf(
				"RESOURCE-PROJECTION-COVERAGE-01: carve-out entry %q does not match any live resource-read GET "+
					"contract — drop the dead slot (and its carve-out ADR entry) so it cannot become a silent "+
					"bypass.", id)})
		}
	}
	return diags
}

// projectionCoverageDiag is the single diagnostic constructor for an unmarked
// resource-read GET, shared by the production scan and the reverse self-check.
func projectionCoverageDiag(contractID string) Diagnostic {
	return Diagnostic{Message: fmt.Sprintf(
		"RESOURCE-PROJECTION-COVERAGE-01: GET contract %q returns a `data` resource but does not set "+
			"endpoints.http.responseProjection: true — a resource-bearing read MUST route its wire data through "+
			"the column-masking funnel (RESOURCE-PROJECTION-CALLSITE-LOCK-01). Set the marker and regenerate "+
			"(`gocell generate contract %s`), or if this `data` is a genuinely non-maskable primitive, add %q "+
			"to resourceReadProjectionCarveOut WITH a carve-out ADR entry.", contractID, contractID, contractID)}
}

// TestResourceProjectionCoverage01_ScannerCatchesViolation is the reverse
// self-check: it drives the SAME schema predicate (responseHasTopLevelDataResource)
// over synthetic fixtures. The RED fixtures — an inline `data`-object response,
// plus a `data: {$ref}` and a `data.items: {$ref}` — must ALL be flagged as
// resource-bearing; the GREEN control (a scalar response with no `data`) must NOT
// be. The $ref reds prove the predicate resolves references (F2 blind-spot close).
func TestResourceProjectionCoverage01_ScannerCatchesViolation(t *testing.T) {
	t.Parallel()
	archDir := findArchTestDir(t)
	relBase := filepath.Join("testdata", "resource_projection_coverage_fixtures")

	reds := []string{"red_unmarked_get", "red_data_ref", "red_data_items_ref"}
	for _, name := range reds {
		refPath := filepath.Join(relBase, name, "response.schema.json")
		hasResource, err := responseHasTopLevelDataResource(archDir, refPath)
		if err != nil {
			t.Fatalf("RESOURCE-PROJECTION-COVERAGE-01 self-check: red fixture %q parse error: %v", name, err)
		}
		if !hasResource {
			t.Errorf("RESOURCE-PROJECTION-COVERAGE-01 self-check: red fixture %q has a `data` object resource but "+
				"the predicate did not detect it — the scanner is vacuous and would not flag an unmarked "+
				"resource-read GET.", name)
		}
	}

	greenRef := filepath.Join(relBase, "green_scalar_no_data", "response.schema.json")
	hasResourceGreen, err := responseHasTopLevelDataResource(archDir, greenRef)
	if err != nil {
		t.Fatalf("RESOURCE-PROJECTION-COVERAGE-01 self-check: green fixture parse error: %v", err)
	}
	// GREEN: no `data` resource ⇒ predicate must NOT require the marker (no over-flag).
	if hasResourceGreen {
		t.Error("RESOURCE-PROJECTION-COVERAGE-01 self-check: green control (no `data` resource) was treated as a " +
			"resource read — the predicate over-flags scalar/status responses that carry no maskable resource.")
	}
}

// isPlatformContract reports whether c is a platform contract — its contract
// dir sits under the top-level contracts/ tree (not an examples/* project). The
// masking funnel (FR-016) is enforced platform-wide; examples carry their own
// demonstrative reads outside this scope.
func isPlatformContract(c *metadata.ContractMeta) bool {
	return strings.HasPrefix(filepath.ToSlash(c.Dir), "contracts/")
}

// responseHasTopLevelDataResource reports whether the response JSON Schema at
// refPath (relative to root) declares a top-level `data` property that is an
// object (single resource) or an array whose items are an object (resource list)
// — the maskable-resource envelope shape. A `data` of any other shape (scalar,
// missing) is not a maskable resource.
//
// It parses through contractgen.Parse — the SAME parser the code generator uses,
// with recursive $ref resolution — so a `data` (or `data.items`) expressed as a
// $ref is resolved to its target schema and detected identically to an inline
// one. This single-source parse is what makes the guard's resource detection
// match the generator's DTO materialization (no second weak parser to diverge).
func responseHasTopLevelDataResource(root, refPath string) (bool, error) {
	schema, err := contractgen.Parse(root, refPath)
	if err != nil {
		return false, err
	}
	data := schema.Properties["data"]
	if data == nil {
		return false, nil
	}
	switch data.Type {
	case "object":
		return true, nil
	case "array":
		return data.Items != nil && data.Items.Type == "object", nil
	default:
		return false, nil
	}
}

// compile-time assertion that metadata.ContractMeta exposes the fields this rule
// relies on (keeps the rule honest if the metadata shape drifts).
var _ = func(c metadata.ContractMeta) (string, *metadata.HTTPTransportMeta, string) {
	return c.Dir, c.Endpoints.HTTP, c.SchemaRefs.Response
}

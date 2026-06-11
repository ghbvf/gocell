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
// (e.g. a future bare-scalar GET whose `data` is a non-maskable primitive). It
// is CURRENTLY EMPTY — all GET reads are projection-marked, so there is zero
// masking debt. Per error-handling.md §Carve-out the carve-out is function-level
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
//   - Anti-vacuity is the non-empty resource-read-GET set assertion (≥1; ≥10
//     after PR-12). If the enumeration collapses to zero (e.g. the parser stops
//     surfacing GET endpoints), the production test FAILS rather than passing
//     vacuously.
package archtest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// resourceReadProjectionCarveOut is the function-level carve-out registry for GET
// reads with a `data` resource response that are legitimately exempt from the
// masking funnel. EMPTY today (zero masking debt). See file godoc §Carve-out
// before changing this; every change syncs the carve-out ADR registry.
var resourceReadProjectionCarveOut = map[string]struct{}{}

// TestResourceProjectionCoverage01 asserts every resource-bearing GET read
// contract is responseProjection-marked (or carved out), and that no carve-out
// slot is stale.
func TestResourceProjectionCoverage01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	project := mustParseProjectContracts(t, root)

	var diags []Diagnostic
	resourceReadCount := 0
	observedCarveOut := map[string]struct{}{}

	for _, c := range project.Contracts {
		if c.Kind != "http" || c.Endpoints.HTTP == nil || c.Endpoints.HTTP.Method != "GET" {
			continue
		}
		if !isPlatformContract(c) {
			continue // examples/* projects are out of the platform masking scope
		}
		schemaPath := filepath.Join(root, c.Dir, c.SchemaRefs.Response)
		hasResource, err := responseHasTopLevelDataResource(schemaPath)
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

		marked := c.Endpoints.HTTP.ResponseProjection
		_, carved := resourceReadProjectionCarveOut[c.ID]
		if carved {
			observedCarveOut[c.ID] = struct{}{}
			if marked {
				diags = append(diags, Diagnostic{Message: fmt.Sprintf(
					"RESOURCE-PROJECTION-COVERAGE-01: contract %q is BOTH carved out AND responseProjection-marked "+
						"— the carve-out slot is stale. Remove it from resourceReadProjectionCarveOut (and the "+
						"carve-out ADR registry) so it cannot mask a future regression.", c.ID)})
			}
			continue
		}
		if !marked {
			diags = append(diags, projectionCoverageDiag(c.ID))
		}
	}

	if resourceReadCount == 0 {
		t.Fatal("RESOURCE-PROJECTION-COVERAGE-01: zero resource-bearing GET reads enumerated — the coverage check " +
			"would be vacuous. After PR-12 there are 10 (all marked); if the enumeration collapsed, fix the parse " +
			"or the response-schema convention.")
	}

	// No-stale carve-out reverse check: every carve-out entry must correspond to a
	// live resource-read GET, else it is a dead bypass slot.
	for id := range resourceReadProjectionCarveOut {
		if _, seen := observedCarveOut[id]; !seen {
			diags = append(diags, Diagnostic{Message: fmt.Sprintf(
				"RESOURCE-PROJECTION-COVERAGE-01: carve-out entry %q does not match any live resource-read GET "+
					"contract — drop the dead slot (and its carve-out ADR entry) so it cannot become a silent "+
					"bypass.", id)})
		}
	}

	Report(t, "RESOURCE-PROJECTION-COVERAGE-01", diags)
}

// projectionCoverageDiag is the single diagnostic constructor for an unmarked
// resource-read GET, shared by the production scan and the reverse self-check.
func projectionCoverageDiag(contractID string) Diagnostic {
	return Diagnostic{Message: fmt.Sprintf(
		"RESOURCE-PROJECTION-COVERAGE-01: GET contract %q returns a `data` resource but does not set "+
			"endpoints.http.responseProjection: true — a resource-bearing read MUST route its wire data through "+
			"the column-masking funnel (RESOURCE-PROJECTION-CALLSITE-LOCK-01). Set the marker and regenerate, or "+
			"if this `data` is a genuinely non-maskable primitive, add %q to resourceReadProjectionCarveOut WITH a "+
			"carve-out ADR entry.", contractID, contractID)}
}

// TestResourceProjectionCoverage01_ScannerCatchesViolation is the reverse
// self-check: it drives the SAME schema predicate (responseHasTopLevelDataResource)
// + marker logic over synthetic fixtures. The RED fixture (a `data`-object
// response, simulating an UNMARKED GET) must be flagged; the GREEN control (a
// scalar response with no `data`) must NOT be flagged.
func TestResourceProjectionCoverage01_ScannerCatchesViolation(t *testing.T) {
	t.Parallel()
	archDir := findArchTestDir(t)
	base := filepath.Join(archDir, "testdata", "resource_projection_coverage_fixtures")

	red := filepath.Join(base, "red_unmarked_get", "response.schema.json")
	hasResource, err := responseHasTopLevelDataResource(red)
	if err != nil {
		t.Fatalf("RESOURCE-PROJECTION-COVERAGE-01 self-check: red fixture parse error: %v", err)
	}
	// RED: resource present + (simulated) marker absent ⇒ predicate must require it.
	if !hasResource {
		t.Error("RESOURCE-PROJECTION-COVERAGE-01 self-check: red fixture has a `data` object resource but the " +
			"predicate did not detect it — the scanner is vacuous and would not flag an unmarked resource-read GET.")
	}

	green := filepath.Join(base, "green_scalar_no_data", "response.schema.json")
	hasResourceGreen, err := responseHasTopLevelDataResource(green)
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

// jsonSchemaNode is the minimal shape of a JSON Schema this rule inspects: the
// node `type`, its object `properties`, and (for arrays) `items`.
type jsonSchemaNode struct {
	Type       string                     `json:"type"`
	Properties map[string]*jsonSchemaNode `json:"properties"`
	Items      *jsonSchemaNode            `json:"items"`
}

// responseHasTopLevelDataResource reports whether the response JSON Schema at
// path declares a top-level `data` property that is an object (single resource)
// or an array whose items are an object (resource list) — the maskable-resource
// envelope shape. A `data` of any other shape (scalar, missing) is not a
// maskable resource.
func responseHasTopLevelDataResource(path string) (bool, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // archtest reads a schema path it derived from metadata
	if err != nil {
		return false, err
	}
	var root jsonSchemaNode
	if err := json.Unmarshal(raw, &root); err != nil {
		return false, err
	}
	data := root.Properties["data"]
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

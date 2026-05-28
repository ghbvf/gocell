// locator_manifest_e2e_test.go exercises the full
// NewParser → Parse() → governance.NewValidator → ValidateStrict() chain in
// manifest mode against a real on-disk temp directory.
//
// This is the T1 governance-validator third-layer E2E test from PR #1226
// review round-7.  It complements:
//   - kernel/metadata/locator_manifest_e2e_test.go — parser-layer tests
//   - kernel/governance/*_test.go — unit tests for individual rules
//
// The test lives in kernel/governance/ (not kernel/metadata/) because
// kernel/governance imports kernel/metadata; importing governance from
// metadata/test would create an import cycle.
package governance

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// governanceCellYAML returns a minimal valid cell.yaml for T1 E2E fixtures.
// All required fields (id/type/consistencyLevel/owner/schema.primary/verify.smoke)
// are present so governance rules that validate presence of mandatory fields
// do not fire.
//
// verify.smoke uses dot-separated format with ≥3 segments (VERIFY-05 rule).
func governanceCellYAML(id, team, primarySchema string) string {
	return "id: " + id + "\n" +
		"type: core\n" +
		"consistencyLevel: L1\n" +
		"owner:\n" +
		"  team: " + team + "\n" +
		"  role: cell-owner\n" +
		"schema:\n" +
		"  primary: " + primarySchema + "\n" +
		"verify:\n" +
		"  smoke:\n" +
		"    - smoke." + id + ".startup\n"
}

// governanceSliceYAML returns a minimal valid slice.yaml for T1 E2E fixtures.
//
// verify.unit uses dot-separated format with ≥3 segments (VERIFY-05 rule).
// allowedFiles is populated with the conventional slice path (FMT-14 rule).
func governanceSliceYAML(id, belongsToCell string) string {
	return "id: " + id + "\n" +
		"belongsToCell: " + belongsToCell + "\n" +
		"consistencyLevel: L1\n" +
		"contractUsages: []\n" +
		"verify:\n" +
		"  unit:\n" +
		"    - unit." + belongsToCell + "." + id + "\n" +
		"  contract: []\n" +
		"allowedFiles:\n" +
		"  - \"cells/" + belongsToCell + "/slices/" + id + "/**\"\n"
}

// governanceWriteFile writes content to path, creating parent dirs as needed.
func governanceWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// TestLocatorManifest_E2E_GovernanceValidate verifies the full three-layer chain:
//
//  1. metadata.NewParser(root, metadata.WithLocatorMode(metadata.LocatorManifest)).Parse()
//     discovers and parses a manifest-mode project.
//  2. governance.NewValidator(pm, root, clock.Real()).ValidateStrict(ctx, false, false)
//     runs base + dep governance rules.
//  3. The results contain no SeverityError entries for a well-formed minimal project.
//     Warnings are permitted (e.g. ADV-05 dead-event warnings on event contracts
//     without subscribers, which do not appear in this fixture).
func TestLocatorManifest_E2E_GovernanceValidate(t *testing.T) {
	root := t.TempDir()

	// manifest.yaml: root module, no custom includes (uses conventional walk).
	governanceWriteFile(t, filepath.Join(root, ".gocell", "manifest.yaml"),
		"version: v1\nmodules:\n  - path: .\n")

	// Minimal cell with all required YAML fields.
	governanceWriteFile(t,
		filepath.Join(root, "cells", "billing", "cell.yaml"),
		governanceCellYAML("billing", "finance", "billing_records"))

	// Minimal slice with all required YAML fields.
	governanceWriteFile(t,
		filepath.Join(root, "cells", "billing", "slices", "query", "slice.yaml"),
		governanceSliceYAML("query", "billing"))

	// Parse in manifest mode.
	pm, err := metadata.NewParser(root, metadata.WithLocatorMode(metadata.LocatorManifest)).Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if _, ok := pm.Cells["billing"]; !ok {
		t.Fatalf("Cells[\"billing\"] missing after Parse")
	}

	// Run governance validation (base + dep phases; not strict).
	v := NewValidator(pm, root, clock.Real())
	results, err := v.ValidateStrict(context.Background(), false, false)
	if err != nil {
		t.Fatalf("ValidateStrict: %v", err)
	}

	// Assert: no SeverityError results for a well-formed minimal project.
	var errs []ValidationResult
	for _, r := range results {
		if r.Severity == SeverityError {
			errs = append(errs, r)
		}
	}
	if len(errs) > 0 {
		t.Errorf("governance ValidateStrict returned %d error(s) for a minimal manifest-mode project:", len(errs))
		for _, e := range errs {
			t.Errorf("  [%s] %s: %s (fix: %s)", e.Code, e.File, e.Message, e.Fix)
		}
	}
}

// locator_manifest_e2e_test.go exercises the manifest-mode Locator/Parser
// chain against a real on-disk temp directory.  This complements the
// fstest.MapFS-based unit tests in locator_test.go by exercising the
// NewLocator (on-disk) path and the Parser.Parse() → Locator.Discover()
// full chain.
//
// Test coverage:
//   - TestLocatorManifest_E2E_DiscoverConsistency — Discover() SourceCell
//     paths match the cell.yaml files written in the temp dir.
//   - TestLocatorManifest_E2E_ParserParse — Parser.Parse() returns a
//     ProjectMeta whose Cells map contains the expected cell IDs.
//   - TestLocatorManifest_E2E_CellIDDerivation — Manifest mode with
//     conventional-layout includes derives CellID from path; manifest
//     mode with non-conventional layout leaves CellID empty (action path
//     per MetadataSource.CellID godoc: slice.yaml must declare
//     belongsToCell explicitly).
package metadata

import (
	"os"
	"path/filepath"
	"testing"
)

// cellYAML returns a minimal valid cell.yaml body for the given id, team, and
// primarySchema.  Split out to avoid repeated long inline strings.
func cellYAML(id, team, primarySchema string) string {
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
		"    - " + id + "/smoke.startup\n"
}

// sliceYAML returns a minimal valid slice.yaml body.
func sliceYAML(id, belongsToCell string) string {
	return "id: " + id + "\n" +
		"belongsToCell: " + belongsToCell + "\n" +
		"consistencyLevel: L1\n" +
		"contractUsages: []\n" +
		"verify:\n" +
		"  unit:\n" +
		"    - " + belongsToCell + "/" + id + "/unit\n" +
		"  contract: []\n" +
		"allowedFiles: []\n"
}

// writeFile writes content to path, creating parent directories as needed.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// TestLocatorManifest_E2E_DiscoverConsistency creates a temp dir with a
// manifest.yaml and conventional-layout cell/slice/contract files, then
// verifies Discover() returns SourceCell entries with matching paths.
func TestLocatorManifest_E2E_DiscoverConsistency(t *testing.T) {
	root := t.TempDir()

	writeFile(t, filepath.Join(root, ".gocell", "manifest.yaml"),
		"version: v1\nmodules:\n  - path: .\n    excludes:\n"+
			"      - \"generated/**\"\n      - \"vendor/**\"\n")
	writeFile(t, filepath.Join(root, "cells", "charge", "cell.yaml"),
		cellYAML("charge", "payments", "charges"))
	writeFile(t, filepath.Join(root, "cells", "charge", "slices", "api", "slice.yaml"),
		sliceYAML("api", "charge"))
	writeFile(t, filepath.Join(root, "contracts", "http", "charge", "create", "v1", "contract.yaml"),
		"id: http.charge.create.v1\nkind: http\nlifecycle: experimental\n"+
			"endpoints:\n  http:\n    method: POST\n    path: /api/v1/charge\n")

	loc, err := NewLocator(root, WithLocatorMode(LocatorManifest))
	if err != nil {
		t.Fatalf("NewLocator: %v", err)
	}
	if loc.Mode() != LocatorManifest {
		t.Fatalf("mode = %v, want LocatorManifest", loc.Mode())
	}

	sources, err := loc.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// (a) SourceCell path must match the file we wrote.
	var cellSrcs []MetadataSource
	for _, s := range sources {
		if s.Kind == SourceCell {
			cellSrcs = append(cellSrcs, s)
		}
	}
	if len(cellSrcs) != 1 {
		t.Fatalf("want 1 SourceCell, got %d: %v", len(cellSrcs), cellSrcs)
	}
	wantCellPath := "cells/charge/cell.yaml"
	if cellSrcs[0].Path != wantCellPath {
		t.Errorf("cell path = %q, want %q", cellSrcs[0].Path, wantCellPath)
	}

	// (b) CellID derivation: conventional-layout path → derived CellID.
	if cellSrcs[0].CellID != "charge" {
		t.Errorf("cell CellID = %q, want %q", cellSrcs[0].CellID, "charge")
	}

	// (c) SourceContract present.
	var contractCount int
	for _, s := range sources {
		if s.Kind == SourceContract {
			contractCount++
		}
	}
	if contractCount != 1 {
		t.Errorf("want 1 SourceContract, got %d", contractCount)
	}
}

// TestLocatorManifest_E2E_ParserParse verifies that the full
// NewParser → Parse() chain in manifest mode returns a ProjectMeta
// whose Cells map contains the cell IDs from cell.yaml.
func TestLocatorManifest_E2E_ParserParse(t *testing.T) {
	root := t.TempDir()

	writeFile(t, filepath.Join(root, ".gocell", "manifest.yaml"),
		"version: v1\nmodules:\n  - path: .\n")
	writeFile(t, filepath.Join(root, "cells", "invoice", "cell.yaml"),
		cellYAML("invoice", "billing", "invoices"))
	writeFile(t, filepath.Join(root, "cells", "invoice", "slices", "query", "slice.yaml"),
		"id: query\nbelongsToCell: invoice\nconsistencyLevel: L0\n"+
			"contractUsages: []\nverify:\n  unit:\n    - invoice/query/unit\n"+
			"  contract: []\nallowedFiles: []\n")

	pm, err := NewParser(root, WithLocatorMode(LocatorManifest)).Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// (a) ProjectMeta.Cells contains the expected cell.
	if _, ok := pm.Cells["invoice"]; !ok {
		t.Errorf("Cells[\"invoice\"] missing; got keys: %v", cellKeys(pm))
	}

	// (b) Slices resolved with correct belongsToCell and populated path fields.
	for _, sl := range pm.Slices {
		if sl.BelongsToCell != "invoice" {
			t.Errorf("slice %q belongsToCell = %q, want \"invoice\"",
				sl.ID, sl.BelongsToCell)
		}
		// T6: verify path fields are populated by the Locator-backed parser.
		if sl.File == "" {
			t.Errorf("slice %q File is empty; parser must set File from MetadataSource.Path", sl.ID)
		}
		if sl.Dir == "" {
			t.Errorf("slice %q Dir is empty; parser must set Dir from path.Base(path.Dir(src.Path))", sl.ID)
		}
		if sl.CellDir != "invoice" {
			t.Errorf("slice %q CellDir = %q, want \"invoice\"", sl.ID, sl.CellDir)
		}
	}
}

// TestLocatorManifest_E2E_CellIDDerivation checks the CellID derivation
// strategy documented in MetadataSource.CellID godoc:
//   - conventional includes path → CellID derived from path segments
//   - non-conventional layout (custom module path) → CellID empty,
//     slice must declare belongsToCell explicitly.
func TestLocatorManifest_E2E_CellIDDerivation(t *testing.T) {
	t.Run("conventional includes derive CellID", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, ".gocell", "manifest.yaml"),
			"version: v1\nmodules:\n  - path: .\n")
		writeFile(t, filepath.Join(root, "cells", "wallet", "cell.yaml"),
			cellYAML("wallet", "fin", "wallets"))

		loc, err := NewLocator(root, WithLocatorMode(LocatorManifest))
		if err != nil {
			t.Fatalf("NewLocator: %v", err)
		}
		sources, err := loc.Discover()
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		for _, s := range sources {
			if s.Kind == SourceCell && s.Path == "cells/wallet/cell.yaml" {
				if s.CellID != "wallet" {
					t.Errorf("CellID = %q, want %q", s.CellID, "wallet")
				}
				return
			}
		}
		t.Fatal("SourceCell for wallet not found")
	})

	t.Run("non-conventional layout SourceCell is discoverable", func(t *testing.T) {
		root := t.TempDir()
		// Custom layout: services/payments/cells/charge/cell.yaml
		// The manifest includes pattern matches, but the path segments
		// do not follow cells/<id>/cell.yaml (extra prefix directory).
		writeFile(t, filepath.Join(root, ".gocell", "manifest.yaml"),
			"version: v1\nmodules:\n  - path: .\n    includes:\n"+
				"      cells:\n        - \"services/*/cells/*/cell.yaml\"\n")
		writeFile(t,
			filepath.Join(root, "services", "payments", "cells", "charge", "cell.yaml"),
			cellYAML("charge", "pay", "charges"))

		loc, err := NewLocator(root, WithLocatorMode(LocatorManifest))
		if err != nil {
			t.Fatalf("NewLocator: %v", err)
		}
		sources, err := loc.Discover()
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		for _, s := range sources {
			if s.Kind == SourceCell {
				// Non-conventional layout: the Locator cannot derive CellID
				// from a path that does not match cells/<id>/cell.yaml.
				// MetadataSource.CellID must be empty; callers must read
				// the cell.yaml `id` field or require slice.yaml to declare
				// belongsToCell explicitly (per MetadataSource.CellID godoc).
				if s.CellID != "" {
					t.Errorf("non-conventional layout source CellID = %q, want empty"+
						" (only conventional walk can derive it)", s.CellID)
				}
				return
			}
		}
		t.Fatal("SourceCell not found for non-conventional layout fixture")
	})
}

// cellKeys returns sorted cell IDs from a ProjectMeta, used in error messages.
func cellKeys(pm *ProjectMeta) []string {
	keys := make([]string, 0, len(pm.Cells))
	for k := range pm.Cells {
		keys = append(keys, k)
	}
	return keys
}

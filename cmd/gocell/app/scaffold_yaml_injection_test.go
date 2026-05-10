package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestScaffoldSlice_YAMLSafeQuote_ColonInCellID verifies that scaffoldSlice
// handles a cell ID containing a YAML-meta character (colon) without injecting
// extra YAML fields.
//
// Current behaviour: validateScaffoldID rejects path separators but NOT colons.
// The inline template embeds the raw CellID value so "evil:abc" would render as
// "belongsToCell: evil:abc" which yaml.Unmarshal parses as {belongsToCell: evil,
// abc: nil} — field injection.
//
// RED: inline template does not use yamlsafe.Quote; raw colon breaks YAML.
func TestScaffoldSlice_YAMLSafeQuote_ColonInCellID(t *testing.T) {
	t.Parallel()

	// Note: validateScaffoldID in scaffold.go currently does NOT reject colons,
	// so "evil:abc" passes validation and reaches the template.
	// If in the future validateScaffoldID is tightened to reject colons, this
	// test should be updated to use a different YAML-meta character that passes.
	root := setupProject(t, "cells")

	// Pre-create the parent cell so scaffoldSlice can find it.
	cellDir := filepath.Join(root, "cells", "evil:abc")
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cellDir, "cell.yaml"),
		[]byte("id: evil:abc\ntype: core\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runScaffoldWithRoot(root, []string{
		"slice",
		"--id=myslice",
		"--cell=evil:abc",
	})
	if err != nil {
		// If validateScaffoldID is tightened, the test simply skips by checking
		// the error message — acceptable RED behaviour.
		if strings.Contains(err.Error(), "path traversal") ||
			strings.Contains(err.Error(), "forbidden") ||
			strings.Contains(err.Error(), "invalid") {
			t.Skipf("validateScaffoldID rejected colon — YAML injection test not applicable: %v", err)
		}
		t.Fatalf("scaffoldSlice(evil:abc cell): %v", err)
	}

	sliceYAMLPath := filepath.Join(root, "cells", "evil:abc", "slices", "myslice", "slice.yaml")
	data, err := os.ReadFile(sliceYAMLPath) //nolint:gosec // tempdir test fixture
	if err != nil {
		t.Fatalf("read slice.yaml: %v", err)
	}

	// Verify round-trip: yaml.Unmarshal must parse without error.
	var parsed map[string]interface{}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("slice.yaml is not valid YAML: %v\ncontent:\n%s", err, data)
	}

	// The injection attack: if "evil:abc" was embedded unquoted, YAML parses
	// "belongsToCell: evil:abc" as {belongsToCell: "evil", "abc": null}.
	// Verify that no unexpected top-level key "abc" was injected.
	if _, injected := parsed["abc"]; injected {
		t.Errorf("YAML injection detected: 'abc' key found in slice.yaml\ncontent:\n%s", data)
	}

	// The belongsToCell field must round-trip back to the original value.
	cellIDVal, ok := parsed["belongsToCell"]
	if !ok {
		t.Fatalf("slice.yaml missing 'belongsToCell' key\ncontent:\n%s", data)
	}
	if cellIDVal != "evil:abc" {
		t.Errorf("belongsToCell = %q, want %q (YAML injection stripped colon)\ncontent:\n%s",
			cellIDVal, "evil:abc", data)
	}
}

// TestScaffoldContract_YAMLSafeQuote_BraceInOwner verifies that scaffoldContract
// handles an owner containing braces without YAML injection.
//
// RED: inline template does not use yamlsafe.Quote.
func TestScaffoldContract_YAMLSafeQuote_BraceInOwner(t *testing.T) {
	t.Parallel()

	root := setupProject(t, "contracts")

	err := runScaffoldWithRoot(root, []string{
		"contract",
		"--id=http.test.brace.v1",
		"--kind=http",
		"--owner={evil}",
	})
	if err != nil {
		if strings.Contains(err.Error(), "path traversal") ||
			strings.Contains(err.Error(), "forbidden") ||
			strings.Contains(err.Error(), "invalid") {
			t.Skipf("validateScaffoldID rejected brace — YAML injection test not applicable: %v", err)
		}
		t.Fatalf("scaffoldContract({evil} owner): %v", err)
	}

	contractYAMLPath := filepath.Join(root, "contracts", "http", "test", "brace", "v1", "contract.yaml")
	data, err := os.ReadFile(contractYAMLPath) //nolint:gosec // tempdir test fixture
	if err != nil {
		t.Fatalf("read contract.yaml: %v", err)
	}

	var parsed map[string]interface{}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("contract.yaml is not valid YAML: %v\ncontent:\n%s", err, data)
	}

	// The ownerCell field must round-trip.
	ownerVal, ok := parsed["ownerCell"]
	if !ok {
		t.Fatalf("contract.yaml missing 'ownerCell' key\ncontent:\n%s", data)
	}
	if ownerVal != "{evil}" {
		t.Errorf("ownerCell = %v, want {evil}\ncontent:\n%s", ownerVal, data)
	}
}

// TestScaffoldJourney_YAMLSafeQuote_ColonInGoal verifies that scaffoldJourney
// handles a goal string containing a colon without YAML injection.
//
// RED: inline template does not use yamlsafe.Quote for goal.
func TestScaffoldJourney_YAMLSafeQuote_ColonInGoal(t *testing.T) {
	t.Parallel()

	root := setupProject(t, "journeys")

	goalWithColon := "ensure system: works correctly"
	err := runScaffoldWithRoot(root, []string{
		"journey",
		"--id=colon-goal",
		"--goal=" + goalWithColon,
		"--team=platform",
		"--cells=mycell",
	})
	if err != nil {
		if strings.Contains(err.Error(), "forbidden") || strings.Contains(err.Error(), "invalid") {
			t.Skipf("validateScaffoldText rejected colon in goal: %v", err)
		}
		t.Fatalf("scaffoldJourney(colon goal): %v", err)
	}

	journeyYAMLPath := filepath.Join(root, "journeys", "J-colon-goal.yaml")
	data, err := os.ReadFile(journeyYAMLPath) //nolint:gosec // tempdir test fixture
	if err != nil {
		t.Fatalf("read J-colon-goal.yaml: %v", err)
	}

	var parsed map[string]interface{}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("J-colon-goal.yaml is not valid YAML: %v\ncontent:\n%s", err, data)
	}

	goalVal, ok := parsed["goal"]
	if !ok {
		t.Fatalf("journey YAML missing 'goal' key\ncontent:\n%s", data)
	}
	if goalVal != goalWithColon {
		t.Errorf("goal = %q, want %q (colon injection)\ncontent:\n%s", goalVal, goalWithColon, data)
	}
}

// TestScaffoldAssembly_YAMLSafeQuote_ColonInTeam verifies that scaffoldAssembly
// handles a team value containing a colon without YAML injection.
//
// RED: inline template does not use yamlsafe.Quote.
func TestScaffoldAssembly_YAMLSafeQuote_ColonInTeam(t *testing.T) {
	t.Parallel()

	root := setupAssemblyTestProject(t, "examplecell")

	teamWithColon := "platform:backend"
	err := runScaffoldWithRoot(root, []string{
		"assembly",
		"--id=colonteamasm",
		"--cells=examplecell",
		"--team=" + teamWithColon,
		"--role=maintainer",
	})
	if err != nil {
		if strings.Contains(err.Error(), "forbidden") || strings.Contains(err.Error(), "invalid") {
			t.Skipf("validation rejected colon in team: %v", err)
		}
		t.Fatalf("scaffoldAssembly(colon team): %v", err)
	}

	asmYAMLPath := filepath.Join(root, "assemblies", "colonteamasm", "assembly.yaml")
	data, err := os.ReadFile(asmYAMLPath) //nolint:gosec // tempdir test fixture
	if err != nil {
		t.Fatalf("read assembly.yaml: %v", err)
	}

	var parsed map[string]interface{}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("assembly.yaml is not valid YAML: %v\ncontent:\n%s", err, data)
	}

	owner, ok := parsed["owner"]
	if !ok {
		t.Fatalf("assembly.yaml missing 'owner' key\ncontent:\n%s", data)
	}
	ownerMap, ok := owner.(map[string]interface{})
	if !ok {
		t.Fatalf("'owner' is not a map\ncontent:\n%s", data)
	}
	teamVal := ownerMap["team"]
	if teamVal != teamWithColon {
		t.Errorf("owner.team = %v, want %q\ncontent:\n%s", teamVal, teamWithColon, data)
	}
}

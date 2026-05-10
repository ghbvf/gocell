package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestScaffoldJourney_DefaultsLifecycleExperimental verifies that a scaffolded
// journey YAML includes `lifecycle: experimental` as the default value.
//
// Rationale: journey schema requires a lifecycle field; "experimental" is the
// correct default for a newly scaffolded journey (not yet validated in production).
// "draft" is the contract lifecycle default; journeys use "experimental".
//
// RED: the current inlineJourneyYAMLTpl does not emit a lifecycle field at all.
func TestScaffoldJourney_DefaultsLifecycleExperimental(t *testing.T) {
	t.Parallel()

	root := setupProject(t, "journeys")

	if err := runScaffoldWithRoot(root, []string{
		"journey",
		"--id=lifecycle-check",
		"--goal=verify lifecycle default",
		"--team=platform",
		"--cells=mycell",
	}); err != nil {
		t.Fatalf("scaffoldJourney: %v", err)
	}

	journeyFile := filepath.Join(root, "journeys", "J-lifecycle-check.yaml")
	data, err := os.ReadFile(journeyFile) //nolint:gosec // tempdir test fixture
	if err != nil {
		t.Fatalf("read J-lifecycle-check.yaml: %v", err)
	}

	// Structural check: must contain `lifecycle:` key.
	if !strings.Contains(string(data), "lifecycle:") {
		t.Errorf("J-lifecycle-check.yaml must contain 'lifecycle:' field\ncontent:\n%s", data)
	}

	// YAML round-trip check: lifecycle value must be "experimental".
	var parsed map[string]interface{}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("J-lifecycle-check.yaml is not valid YAML: %v\ncontent:\n%s", err, data)
	}

	lc, ok := parsed["lifecycle"]
	if !ok {
		t.Fatalf("J-lifecycle-check.yaml missing 'lifecycle' field\ncontent:\n%s", data)
	}
	if lc != "experimental" {
		t.Errorf("lifecycle = %q, want %q\ncontent:\n%s", lc, "experimental", data)
	}
}

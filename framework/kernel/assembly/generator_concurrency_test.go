package assembly

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/pkg/pathsafe"
	"github.com/ghbvf/gocell/framework/pkg/scaffoldid"
)

// TestGenerator_PlanAssemblyScaffold_ConcurrentSafe verifies that concurrent
// calls to PlanAssemblyScaffold on the same Generator instance are race-free
// and produce independent, correct results. Run with -race to trigger the
// detector on the old defer-revert pattern.
//
// Reference: kubernetes-sigs/kubebuilder pkg/machinery/scaffold.go —
// per-call render model with no shared mutable state across renders.
func TestGenerator_PlanAssemblyScaffold_ConcurrentSafe(t *testing.T) {
	t.Parallel()

	root, pm := scaffoldConcurrencyProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

	// realRoot resolves symlinks the same way PlanAssemblyScaffold does.
	realRoot, err := pathsafe.ResolveRoot(root)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}

	// Snapshot the original Assemblies map before concurrent calls.
	originalAssemblyIDs := make(map[string]struct{}, len(pm.Assemblies))
	for k := range pm.Assemblies {
		originalAssemblyIDs[k] = struct{}{}
	}

	specIDs := []string{"asma", "asmb", "asmc", "asmd"}

	type result struct {
		id   string
		plan []pathsafe.PlannedFile
		err  error
	}

	results := make([]result, len(specIDs))
	var wg sync.WaitGroup
	wg.Add(len(specIDs))

	for i, rawID := range specIDs {
		i, rawID := i, rawID
		go func() {
			defer wg.Done()
			spec := AssemblyScaffoldSpec{
				ID:        mustID(t, rawID),
				Cells:     []scaffoldid.ScaffoldID{mustID(t, "cellalpha"), mustID(t, "cellbeta")},
				OwnerTeam: "platform",
				OwnerRole: "maintainer",
			}
			plan, err := gen.PlanAssemblyScaffold(spec)
			results[i] = result{id: rawID, plan: plan, err: err}
		}()
	}
	wg.Wait()

	// Assert each goroutine got exactly 6 files with its own spec.ID in path.
	for _, r := range results {
		assertConcurrentResult(t, r.id, r.plan, r.err, realRoot)
	}

	// Assert g.project.Assemblies was not polluted by any spec.ID.
	assertAssembliesNotPolluted(t, pm, specIDs, originalAssemblyIDs)
}

// assertConcurrentResult validates a single goroutine's PlanAssemblyScaffold result.
func assertConcurrentResult(t *testing.T, id string, plan []pathsafe.PlannedFile, err error, realRoot string) {
	t.Helper()
	if err != nil {
		t.Errorf("goroutine %s: unexpected error: %v", id, err)
		return
	}
	if len(plan) != 6 {
		t.Errorf("goroutine %s: expected 6 planned files, got %d", id, len(plan))
		return
	}
	for _, f := range plan {
		if !strings.Contains(f.AbsPath, id) {
			t.Errorf("goroutine %s: file path %s does not contain spec ID %q", id, f.AbsPath, id)
		}
	}
	assertAllSixPathsPresent(t, id, plan, realRoot)
}

// assertAllSixPathsPresent checks that the canonical 6 scaffold paths are all present.
func assertAllSixPathsPresent(t *testing.T, id string, plan []pathsafe.PlannedFile, realRoot string) {
	t.Helper()
	for _, rel := range scaffoldSixPaths(id) {
		absWant := filepath.Join(realRoot, rel)
		found := false
		for _, f := range plan {
			if f.AbsPath == absWant {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("goroutine %s: expected path %s not found in plan", id, absWant)
		}
	}
}

// assertAssembliesNotPolluted checks that no concurrent call mutated the shared project map.
func assertAssembliesNotPolluted(t *testing.T, pm *metadata.ProjectMeta, specIDs []string, originalIDs map[string]struct{}) {
	t.Helper()
	for _, rawID := range specIDs {
		if _, ok := pm.Assemblies[rawID]; ok {
			t.Errorf("g.project.Assemblies[%q] was left behind after concurrent call", rawID)
		}
	}
	if len(pm.Assemblies) != len(originalIDs) {
		t.Errorf("g.project.Assemblies size changed: want %d, got %d",
			len(originalIDs), len(pm.Assemblies))
	}
}

// scaffoldConcurrencyProject creates a tempdir project with two cells
// (cellalpha + cellbeta) and returns (root, parsedProject).
func scaffoldConcurrencyProject(t *testing.T) (string, *metadata.ProjectMeta) {
	t.Helper()
	root := t.TempDir()

	cells := []struct {
		id         string
		structName string
	}{
		{"cellalpha", "CellAlpha"},
		{"cellbeta", "CellBeta"},
	}
	for _, c := range cells {
		cellDir := filepath.Join(root, "cells", c.id)
		if err := os.MkdirAll(cellDir, 0o755); err != nil {
			t.Fatal(err)
		}
		cellYAML := "id: " + c.id + `
type: core
consistencyLevel: L1
durabilityMode: durable
owner:
  team: platform
  role: cell-owner
schema:
  primary: ` + c.id + `
verify:
  smoke:
    - smoke.` + c.id + `.startup
goStructName: ` + c.structName + `
l0Dependencies: []
`
		if err := os.WriteFile(filepath.Join(cellDir, "cell.yaml"), []byte(cellYAML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module github.com/ghbvf/gocell\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pm, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("metadata.Parse: %v", err)
	}
	return root, pm
}

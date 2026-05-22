package assembly

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/pathsafe"
	"github.com/ghbvf/gocell/pkg/scaffoldid"
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
		if r.err != nil {
			t.Errorf("goroutine %s: unexpected error: %v", r.id, r.err)
			continue
		}
		if len(r.plan) != 6 {
			t.Errorf("goroutine %s: expected 6 planned files, got %d", r.id, len(r.plan))
			continue
		}
		for _, f := range r.plan {
			if !strings.Contains(f.AbsPath, r.id) {
				t.Errorf("goroutine %s: file path %s does not contain spec ID %q",
					r.id, f.AbsPath, r.id)
			}
		}
		// Also check the canonical 6 paths are all present.
		wantPaths := scaffoldSixPaths(r.id)
		for _, rel := range wantPaths {
			absWant := filepath.Join(realRoot, rel)
			found := false
			for _, f := range r.plan {
				if f.AbsPath == absWant {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("goroutine %s: expected path %s not found in plan", r.id, absWant)
			}
		}
	}

	// Assert g.project.Assemblies was not polluted by any spec.ID.
	for _, rawID := range specIDs {
		if _, ok := pm.Assemblies[rawID]; ok {
			t.Errorf("g.project.Assemblies[%q] was left behind after concurrent call", rawID)
		}
	}
	// Assert no extra keys were added to (or removed from) the original map.
	if len(pm.Assemblies) != len(originalAssemblyIDs) {
		t.Errorf("g.project.Assemblies size changed: want %d, got %d",
			len(originalAssemblyIDs), len(pm.Assemblies))
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

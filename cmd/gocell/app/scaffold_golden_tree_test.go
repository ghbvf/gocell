package app

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/pathsafe"
	"github.com/ghbvf/gocell/tools/codegen/cellgen"
)

// updateScaffoldGolden regenerates the byte-for-byte scaffold golden tree under
// testdata/scaffold_golden/goldcell/. Run once after an intentional scaffold /
// cellgen / contractgen template change, then commit the diff:
//
//	go test ./cmd/gocell/app/ -run TestScaffoldCell_GoldenTree -update-scaffold-golden
//
// A distinct flag name (not the bare -update used by cellgen's own golden tests)
// avoids cross-package collision when the whole suite is run with -update.
var updateScaffoldGolden = flag.Bool("update-scaffold-golden", false,
	"regenerate cmd/gocell scaffold golden tree fixtures")

// goldenTreeSpec is the fixed scaffold spec the golden tree locks. It reuses the
// `goldcell` identity of the sibling pattern-match gates (scaffold_golden_test.go)
// and the default --with-http variant of `gocell scaffold cell`.
func goldenTreeSpec(t *testing.T) cellgen.ScaffoldSpec {
	t.Helper()
	return cellgen.ScaffoldSpec{
		CellID:           mustID(t, "goldcell"),
		StructName:       "GoldCell",
		Package:          "goldcell",
		ModulePath:       "github.com/ghbvf/gocell",
		OwnerTeam:        "platform",
		OwnerRole:        "cell-owner",
		Type:             "core",
		ConsistencyLevel: "L2",
		WithHTTP:         true,
	}
}

const scaffoldGoldenDir = "testdata/scaffold_golden/goldcell"

// scaffoldGoldenRepoRoot returns the workspace (repo) root — the directory
// containing go.work — by walking up from the test working directory. findRoot
// is deliberately NOT used: since #1557 split cmd/gocell into its own go.work
// satellite module, findRoot stops at cmd/gocell/go.mod, but the canonical
// shared error schema lives at the OUTER repo root (the go.work directory).
func scaffoldGoldenRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.work")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("scaffold golden: repo root (directory containing go.work) not found above the test working dir")
		}
		dir = parent
	}
}

// setupScaffoldGoldenProject builds a self-contained temp project for the golden
// gate: a go.mod fixing the module path (so generated import grouping is stable)
// plus the canonical shared error schema copied from the repo root (contractgen
// resolves the scaffolded contract.yaml's SchemaRef link to it). Unlike the
// sibling setupBundleTestProject helper — which t.Skipf's when its hardcoded
// relative schema path does not resolve from the package working dir — this one
// locates the repo root via go.work and t.Fatal's on any setup failure: a Hard
// anti-drift gate must never silently skip.
func setupScaffoldGoldenProject(t *testing.T) string {
	t.Helper()
	repoRoot := scaffoldGoldenRepoRoot(t)
	canonical := filepath.Join(repoRoot, "contracts", "shared", "errors", "error-response-v1.schema.json")
	schema, err := os.ReadFile(canonical) //nolint:gosec // canonical in-repo schema, not user input
	if err != nil {
		t.Fatalf("read canonical shared error schema %s: %v", canonical, err)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module github.com/ghbvf/gocell\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	schemaDir := filepath.Join(root, "contracts", "shared", "errors")
	if err := os.MkdirAll(schemaDir, 0o755); err != nil {
		t.Fatalf("mkdir schema dir: %v", err)
	}
	schemaFile := filepath.Join(schemaDir, "error-response-v1.schema.json")
	//nolint:gosec // G703: schemaFile is t.TempDir()-rooted with a constant filename — no traversal
	if err := os.WriteFile(schemaFile, schema, 0o644); err != nil {
		t.Fatalf("write shared error schema: %v", err)
	}
	return root
}

// TestScaffoldCell_GoldenTree is the authoritative anti-drift gate for
// `gocell scaffold cell`. It drives the exact planner the CLI uses
// (cellgen.PlanCellBundleScaffold — same call as scaffoldCell) and byte-for-byte
// compares EVERY planned file (cell.go / cell.yaml / internal arch-layer doc.go /
// slice skeleton / contract.yaml + schemas AND the derived codegen: cell_gen.go /
// slice_gen.go / healthz_gen.go / generated/contracts/**/*_gen.go) against a
// committed golden tree. Locking the full output — not a subset — is what makes
// the demo's "scaffold golden 对照样例" a faithful sample and the gate Hard: any
// template drift (scaffold-*.tmpl, cell.tmpl, contractgen, a shared header) turns
// the byte diff red and forces the change through the explicit
// -update-scaffold-golden regenerate checkpoint.
//
// AI-robust: Hard (codegen funnel + golden / reflect-schema-freeze family).
//   - upstream Hard: byte-diff catches any rendered-output drift.
//   - completeness Hard: the file-set reverse guard below catches an added or
//     removed planned file (so a new/dropped template output cannot slip the lock
//     silently).
//
// This supersedes the string.Contains pattern-match gates in
// scaffold_golden_test.go, which are kept as fast, human-readable explainers of
// individual load-bearing invariants (K#04/K#05). This tree test is authoritative.
func TestScaffoldCell_GoldenTree(t *testing.T) {
	root := setupScaffoldGoldenProject(t)
	realRoot, err := pathsafe.ResolveRoot(root)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}

	plan, err := cellgen.PlanCellBundleScaffold(realRoot, goldenTreeSpec(t))
	if err != nil {
		t.Fatalf("PlanCellBundleScaffold: %v", err)
	}
	if len(plan) == 0 {
		t.Fatal("scaffold plan is empty — anti-vacuity guard")
	}

	if *updateScaffoldGolden {
		// Rewrite from scratch so a removed template output leaves no stale golden.
		if err := os.RemoveAll(scaffoldGoldenDir); err != nil {
			t.Fatalf("clean golden dir: %v", err)
		}
	}

	planRels := make([]string, 0, len(plan))
	for _, pf := range plan {
		rel, err := filepath.Rel(realRoot, pf.AbsPath)
		if err != nil {
			t.Fatalf("Rel(%s): %v", pf.AbsPath, err)
		}
		rel = filepath.ToSlash(rel)
		planRels = append(planRels, rel)

		goldenPath := filepath.Join(scaffoldGoldenDir, filepath.FromSlash(rel)) + ".golden"
		if *updateScaffoldGolden {
			if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
				t.Fatalf("mkdir golden: %v", err)
			}
			if err := os.WriteFile(goldenPath, pf.Content, 0o644); err != nil {
				t.Fatalf("write golden %s: %v", goldenPath, err)
			}
			continue
		}
		want, err := os.ReadFile(goldenPath) //nolint:gosec // golden path derived from the in-repo plan, not user input
		if err != nil {
			t.Errorf("missing golden for planned file %q (run -update-scaffold-golden): %v", rel, err)
			continue
		}
		if !bytes.Equal(pf.Content, want) {
			t.Errorf("scaffold golden drift: %s\n--- got ---\n%s\n--- want ---\n%s",
				rel, pf.Content, want)
		}
	}

	assertGoldenFileSet(t, planRels)
}

// assertGoldenFileSet is the completeness (reverse) guard: the set of planned
// relative paths must equal the set of committed .golden files. It catches a
// planned file that has no golden (an added template output) AND a stale golden
// with no planned file (a removed template output) — neither can slip past the
// byte comparison silently.
func assertGoldenFileSet(t *testing.T, planRels []string) {
	t.Helper()
	if *updateScaffoldGolden {
		return // just rewrote the tree to match the plan exactly
	}

	var goldenRels []string
	err := filepath.WalkDir(scaffoldGoldenDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".golden") {
			return nil
		}
		rel, relErr := filepath.Rel(scaffoldGoldenDir, path)
		if relErr != nil {
			return relErr
		}
		goldenRels = append(goldenRels, strings.TrimSuffix(filepath.ToSlash(rel), ".golden"))
		return nil
	})
	if err != nil {
		t.Fatalf("walk golden dir: %v", err)
	}

	sort.Strings(planRels)
	sort.Strings(goldenRels)
	if strings.Join(planRels, "\n") != strings.Join(goldenRels, "\n") {
		t.Errorf("scaffold golden file-set drift (run -update-scaffold-golden):\n"+
			"--- planned (%d) ---\n%s\n--- golden (%d) ---\n%s",
			len(planRels), strings.Join(planRels, "\n"),
			len(goldenRels), strings.Join(goldenRels, "\n"))
	}
}

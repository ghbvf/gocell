package cellmodulemeta

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

// repoRoot walks up from the test's working directory to the go.work workspace
// root (the unambiguous repo-root marker since #1565; nested modules carry go.mod
// but never go.work). The bundle generator runs against this real monorepo tree —
// the same posture as locator_equivalence_test — so the closure is exercised
// against the actual corecells cells + root contracts, not a hand-crafted fixture.
func repoRoot(t *testing.T) string {
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
			t.Fatal("repo root (go.work) not found above " + dir)
		}
		dir = parent
	}
}

// writeBundleToDir materializes a desiredBundle map under dir, creating parent
// directories. Used to re-parse the bundle in isolation (consumer simulation)
// without writing into the repo tree.
func writeBundleToDir(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, content, 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

// TestDesiredBundle_ReparsesToPlatformClosureEqualsMonorepo is the Layer-2
// correctness proof against the REAL tree: the generated bundle, parsed in
// isolation (as an external Operator-SDK consumer would from the module cache),
// must yield a platform closure (cell IDs + slice keys + reachable contract IDs)
// IDENTICAL to the monorepo's platform closure. A closure-traversal omission that
// drops a referenced contract makes the bundle's reachable-contract set smaller —
// caught here, the machine guard behind broker-cell completeness.
func TestDesiredBundle_ReparsesToPlatformClosureEqualsMonorepo(t *testing.T) {
	root := repoRoot(t)

	desired, err := desiredBundle(root)
	if err != nil {
		t.Fatalf("desiredBundle: %v", err)
	}

	monoPM, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("parse monorepo: %v", err)
	}
	want := platformClosureSignature(monoPM)

	// Anti-vacuity: the platform closure must be non-trivial, else an empty
	// closure would pass equivalence vacuously.
	if len(want.cells) == 0 || len(want.contracts) == 0 {
		t.Fatalf("vacuous platform closure: cells=%d contracts=%d", len(want.cells), len(want.contracts))
	}
	if _, ok := want.cells["accesscore"]; !ok {
		t.Fatalf("expected accesscore in platform closure; got cells=%v", keys(want.cells))
	}

	bundleDir := t.TempDir()
	writeBundleToDir(t, bundleDir, desired)
	bundlePM, err := metadata.NewParser(bundleDir).Parse()
	if err != nil {
		t.Fatalf("re-parse bundle: %v", err)
	}
	got := closureSignature(bundlePM)

	if drift := diffSignatures(want, got); len(drift) != 0 {
		t.Fatalf("bundle platform closure != monorepo platform closure:\n  %v", drift)
	}
}

// TestDesiredBundle_Deterministic asserts the generator is byte-stable: two runs
// produce identical file sets and identical content, so the --verify byte diff
// (Layer-1 golden) is reliable.
func TestDesiredBundle_Deterministic(t *testing.T) {
	root := repoRoot(t)
	a, err := desiredBundle(root)
	if err != nil {
		t.Fatalf("desiredBundle a: %v", err)
	}
	b, err := desiredBundle(root)
	if err != nil {
		t.Fatalf("desiredBundle b: %v", err)
	}
	if len(a) != len(b) {
		t.Fatalf("non-deterministic file count: %d vs %d", len(a), len(b))
	}
	for rel, ca := range a {
		cb, ok := b[rel]
		if !ok {
			t.Fatalf("file %s in run a but not b", rel)
		}
		if !bytes.Equal(ca, cb) {
			t.Fatalf("file %s content differs between runs", rel)
		}
	}
}

// TestPlatformClosureFiles_IncludesCorecellsAndContracts is the anti-vacuity
// floor for the closure: it must include corecells cell.yaml AND at least one
// referenced contract.yaml, so a closure that silently collects nothing fails.
func TestPlatformClosureFiles_IncludesCorecellsAndContracts(t *testing.T) {
	root := repoRoot(t)
	pm, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	files, err := platformClosureFiles(root, pm)
	if err != nil {
		t.Fatalf("platformClosureFiles: %v", err)
	}
	var sawCorecellsCell, sawContract bool
	for _, f := range files {
		if metadata.IsInCorecellsSubtree(f) && filepath.Base(f) == "cell.yaml" {
			sawCorecellsCell = true
		}
		if filepath.Base(f) == "contract.yaml" {
			sawContract = true
		}
	}
	if !sawCorecellsCell {
		t.Errorf("closure missing any corecells cell.yaml; files=%d", len(files))
	}
	if !sawContract {
		t.Errorf("closure missing any contract.yaml; files=%d", len(files))
	}
}

// TestVerifyByteLayer_DetectsMissingDriftStale is the Layer-1 synthetic red
// case: against a temp bundle dir, a missing desired file, a byte-divergent
// file, and an extra (stale) committed file must each be reported.
func TestVerifyByteLayer_DetectsMissingDriftStale(t *testing.T) {
	tmp := t.TempDir()
	desired := map[string][]byte{
		"present.yaml": []byte("ok\n"),
		"drifted.yaml": []byte("canonical\n"),
		"absent.yaml":  []byte("want\n"),
	}
	// present.yaml exact, drifted.yaml diverged, absent.yaml missing, plus a stale extra.
	bundleAbs := filepath.Join(tmp, filepath.FromSlash(bundleDirFromRoot))
	if err := os.MkdirAll(bundleAbs, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(bundleAbs, "present.yaml"), "ok\n")
	mustWrite(t, filepath.Join(bundleAbs, "drifted.yaml"), "TAMPERED\n")
	mustWrite(t, filepath.Join(bundleAbs, "stale.yaml"), "orphan\n")

	drift := verifyByteLayer(tmp, desired)
	assertContains(t, drift, "missing: absent.yaml")
	assertContains(t, drift, "byte-drift: drifted.yaml")
	assertContains(t, drift, "stale: stale.yaml")
	for _, d := range drift {
		if d == "byte-drift: present.yaml" || d == "missing: present.yaml" {
			t.Errorf("present.yaml falsely reported as drift: %q", d)
		}
	}
}

// TestDiffSignatures_DetectsMissingContract is the Layer-2 synthetic red case:
// a bundle whose reachable-contract set drops one contract (the broker-cell
// fail-open shape) must be flagged "missing from bundle".
func TestDiffSignatures_DetectsMissingContract(t *testing.T) {
	want := newClosureSig()
	want.cells["accesscore"] = struct{}{}
	want.contracts["event.session.created.v1"] = struct{}{}
	want.contracts["event.policy.updated.v1"] = struct{}{}

	got := newClosureSig()
	got.cells["accesscore"] = struct{}{}
	got.contracts["event.session.created.v1"] = struct{}{} // policy.updated dropped

	drift := diffSignatures(want, got)
	assertContains(t, drift, `semantic: contract "event.policy.updated.v1" missing from bundle`)
	if len(diffSignatures(want, want)) != 0 {
		t.Error("identical signatures must produce no drift (anti-vacuity)")
	}
}

func mustWrite(t *testing.T, abs, content string) {
	t.Helper()
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", abs, err)
	}
}

func assertContains(t *testing.T, got []string, want string) {
	t.Helper()
	for _, g := range got {
		if g == want {
			return
		}
	}
	t.Errorf("expected drift %q in %v", want, got)
}

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

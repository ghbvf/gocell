package modrelease

// INVARIANT: PUBLISHABLE-MODULE-SET-01
//
// The externally-published module set is DERIVED from go.work minus the
// examples/ + tests/ + cmd/ deny predicate ([IsPublishable]) and byte-locked into
// testdata/publishable.golden. This is a codegen-funnel+golden guard (ai-robust
// §载体 #1, Hard): adding an adapter to go.work, renaming a member, or changing
// the deny predicate shifts the derived set, the golden goes stale, and CI fails
// on the byte diff — the drift cannot be silent. Regenerate intentionally with
// `make update-modrelease-golden`.
//
// # Grading: Hard (golden byte-freeze of a derived set)
//
// The SET is derived from go.work (the single source the toolchain compiles), and
// the OUTPUT bytes are frozen; any drift in either the source (go.work) or the
// rule (IsPublishable) surfaces as a byte diff. This is stronger than a runtime
// equality assertion (Medium) because the expected output is a checked-in
// artifact a human must consciously regenerate.
//
// # Anti-vacuity + synthetic red case (ai-robust §archtest 文件命名)
//
//   - Anti-vacuity: TestPublishableModuleSet01 asserts len >= 2 and that the root
//     module (Dir ".") is present, so a mis-resolved / empty set cannot pass green.
//   - Synthetic red case: TestPublishableModuleSet01_DenyPredicate feeds a fixture
//     go.work containing synthetic examples/foo, tests/bar, and cmd/baz members and
//     asserts the deny predicate drops exactly those three while keeping the
//     non-leaf members — proving the filter is not a vacuous always-true pass.

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/workspace"
)

const publishableModuleSetRule = "PUBLISHABLE-MODULE-SET-01"

// updateGolden regenerates testdata/publishable.golden (via `make
// update-modrelease-golden`). Test-scope only; modrelease is not imported as a
// dependency, so this flag cannot collide with a consumer's own -update.
var updateGolden = flag.Bool("update", false, "regenerate testdata/publishable.golden")

// goldenTagPathRE matches a well-formed synchronized tag: a bare version for the
// root, or "<reldir>/vX.Y.Z" for a satellite. Rejects "..", absolute paths.
var goldenTagPathRE = regexp.MustCompile(`^(v\d+\.\d+\.\d+|[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)*/v\d+\.\d+\.\d+)$`)

func TestPublishableModuleSet01(t *testing.T) {
	root := workspaceRootForTest(t)

	mods, err := PublishableModules(root)
	if err != nil {
		t.Fatalf("%s: PublishableModules: %v", publishableModuleSetRule, err)
	}

	// Anti-vacuity: a real workspace has the core framework module + many
	// satellites; an empty or core-only result means the enumeration mis-resolved.
	if len(mods) < 2 {
		t.Fatalf("%s: anti-vacuity failed: derived %d publishable modules, want >= 2", publishableModuleSetRule, len(mods))
	}
	var hasCore bool
	for _, m := range mods {
		// Post-#1565 the core publishable module is the framework module at
		// ./framework (the repo root holds only go.work, no module).
		if filepath.ToSlash(filepath.Clean(m.Dir)) == "framework" {
			hasCore = true
		}
		// Tag-path well-formedness: every member maps to a valid synchronized tag.
		tag := tagPathFor(m.Dir, "v1.2.3")
		if !goldenTagPathRE.MatchString(tag) {
			t.Errorf("%s: module %q yields malformed tag %q", publishableModuleSetRule, m.Dir, tag)
		}
	}
	if !hasCore {
		t.Fatalf("%s: anti-vacuity failed: core framework module (Dir \"framework\") absent from publishable set", publishableModuleSetRule)
	}

	got := renderPublishable(mods)
	goldenPath := filepath.Clean(filepath.Join("testdata", "publishable.golden"))
	if *updateGolden {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("%s: write golden: %v", publishableModuleSetRule, err)
		}
		t.Logf("%s: wrote %s (%d modules)", publishableModuleSetRule, goldenPath, len(mods))
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("%s: read golden (run `make update-modrelease-golden`): %v", publishableModuleSetRule, err)
	}
	if got != string(want) {
		t.Errorf("%s: publishable module set drifted from testdata/publishable.golden.\n"+
			"A go.work member was added/removed/renamed, or IsPublishable changed. If intentional, run "+
			"`make update-modrelease-golden`.\n--- got ---\n%s\n--- want ---\n%s",
			publishableModuleSetRule, got, want)
	}
}

// TestPublishableModuleSet01_DenyPredicate is the synthetic red case: a fixture
// workspace whose go.work declares examples/foo, tests/bar, and cmd/baz alongside
// real-shaped library members. The deny predicate MUST drop exactly the three
// leaf/binary members; if it returned them (or filtered everything), the main
// green assertion would be vacuous.
func TestPublishableModuleSet01_DenyPredicate(t *testing.T) {
	root := workspaceRootForTest(t)
	fixture := filepath.Join(root, "tools", "modrelease", "testdata", "publishable_set_fixture")

	mods, err := PublishableModules(fixture)
	if err != nil {
		t.Fatalf("%s: PublishableModules(fixture): %v", publishableModuleSetRule, err)
	}
	got := make(map[string]bool, len(mods))
	for _, m := range mods {
		got[filepath.ToSlash(filepath.Clean(m.Dir))] = true
	}
	wantKept := []string{".", "adapters/keep", "corecells"}
	for _, d := range wantKept {
		if !got[d] {
			t.Errorf("%s: deny predicate wrongly dropped publishable member %q", publishableModuleSetRule, d)
		}
	}
	wantDropped := []string{"examples/foo", "tests/bar", "cmd/baz"}
	for _, d := range wantDropped {
		if got[d] {
			t.Errorf("%s: deny predicate failed to drop non-publishable member %q (filter is vacuous)", publishableModuleSetRule, d)
		}
	}
	if len(mods) != len(wantKept) {
		t.Errorf("%s: fixture yielded %d publishable modules %v, want exactly %v",
			publishableModuleSetRule, len(mods), keys(got), wantKept)
	}
}

func renderPublishable(mods []workspace.Module) string {
	var b strings.Builder
	b.WriteString("# Publishable module set — DERIVED from go.work minus examples/ + tests/ + cmd/.\n")
	b.WriteString("# Regenerate intentionally: make update-modrelease-golden\n")
	b.WriteString("# <reldir>\\t<import-path>\n")
	for _, m := range mods {
		fmt.Fprintf(&b, "%s\t%s\n", filepath.ToSlash(filepath.Clean(m.Dir)), m.ImportPath)
	}
	return b.String()
}

func workspaceRootForTest(t *testing.T) string {
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
			t.Fatalf("workspaceRootForTest: no go.work found walking up from %s", dir)
		}
		dir = parent
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

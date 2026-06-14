package metadata

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

// repoRootForEquivalence walks up from the test's working directory to the
// repository root, identified as the directory holding the go.work workspace
// file. Since #1565 moved the core layers into the framework/ module, the repo
// root no longer carries a go.mod (it is a pure go.work workspace); go.work is
// the unambiguous root marker — nested modules and tools/*/testdata fixtures
// have their own go.mod but never a go.work.
func repoRootForEquivalence(t *testing.T) string {
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
			t.Fatal("repo root (go.work workspace file) not found above " + dir)
		}
		dir = parent
	}
}

// TestLocator_RealTreeConventionalManifestEquivalence is the binding governance
// guard for #1554. Adding .gocell/manifest.yaml flips every gocell command from
// conventional to manifest mode (locator.go resolveMode LocatorAuto). The
// committed manifest MUST discover the identical MetadataSource set — same
// Path, Kind, AND CellID — or governance/codegen scope silently narrows.
//
// This runs against the REAL on-disk tree (not a hand-crafted fixture): an AI
// that drops the manifest's examples/* includes makes conventional mode still
// find the examples/ subtree while manifest mode does not, so DeepEqual fails.
// The anti-vacuity assertion (examples/ sources must be present) prevents a
// vacuous pass if both modes ever return empty. Hand-crafted-fixture equivalence
// would be Soft (the author could shrink both sides); real-tree comparison is
// the Medium guard. See plan 1554 §AI-robust. The negative control below
// (TestLocator_MinimalManifestNarrowsScope) proves the guard actually bites.
func TestLocator_RealTreeConventionalManifestEquivalence(t *testing.T) {
	root := repoRootForEquivalence(t)

	convLoc, err := NewLocator(root, WithLocatorMode(LocatorConventional))
	if err != nil {
		t.Fatalf("NewLocator conventional: %v", err)
	}
	defer func() { _ = convLoc.Close() }()
	convSrc, err := convLoc.Discover()
	if err != nil {
		t.Fatalf("Discover conventional: %v", err)
	}

	manLoc, err := NewLocator(root, WithLocatorMode(LocatorManifest))
	if err != nil {
		t.Fatalf("NewLocator manifest (needs committed .gocell/manifest.yaml): %v", err)
	}
	defer func() { _ = manLoc.Close() }()
	manSrc, err := manLoc.Discover()
	if err != nil {
		t.Fatalf("Discover manifest (needs committed .gocell/manifest.yaml): %v", err)
	}

	conv := summariseSources(convSrc)
	man := summariseSources(manSrc)

	assertConventionalDiscoveryNonVacuous(t, conv, convSrc)

	if !reflect.DeepEqual(conv, man) {
		onlyConv := difference(conv, man)
		onlyMan := difference(man, conv)
		t.Errorf("conventional vs manifest discovery diverge — the committed "+
			".gocell/manifest.yaml must mirror conventional scope.\n"+
			"only in conventional (manifest dropped these): %v\n"+
			"only in manifest (manifest added these): %v",
			onlyConv, onlyMan)
	}
}

func assertConventionalDiscoveryNonVacuous(t *testing.T, conv []string, convSrc []MetadataSource) {
	t.Helper()

	// Anti-vacuity: the comparison only proves something if conventional mode
	// actually discovered sources, including the examples/ subtree that a
	// minimal manifest would drop. Without this, two empty slices would pass.
	if len(conv) == 0 {
		t.Fatal("vacuous guard: conventional mode discovered 0 sources")
	}
	if !containsExamplesSource(conv) {
		t.Fatal("vacuous guard: conventional mode discovered no examples/ source; " +
			"the equivalence assertion would not catch a manifest that drops the examples subtree")
	}
	assertExamplesCellsHaveIDs(t, convSrc)
}

func assertExamplesCellsHaveIDs(t *testing.T, convSrc []MetadataSource) {
	t.Helper()

	// CellID-dimension anti-vacuity: summariseSources compares "kind:path:cellID",
	// so the DeepEqual would still pass if BOTH modes derived an empty CellID for
	// examples cells. Assert at least one examples/ SourceCell carries a non-empty
	// CellID, proving the manifest-mode CellID derivation (matchCellPath via
	// stripBase) is actually exercised and non-trivial.
	examplesCellWithID := false
	for _, s := range convSrc {
		if s.Kind == SourceCell && strings.HasPrefix(s.Path, "examples/") {
			if s.CellID == "" {
				t.Errorf("anti-vacuity: conventional cell source under examples/ has empty CellID: %s", s.Path)
			} else {
				examplesCellWithID = true
			}
		}
	}
	if !examplesCellWithID {
		t.Fatal("anti-vacuity: no examples/ SourceCell with a non-empty CellID; " +
			"CellID-dimension equivalence is not proven")
	}
}

// TestLocator_MinimalManifestNarrowsScope is the blind-spot reverse self-check
// (per ai-robust.md "工具选定后强制盲区自检 + 反向自检测试"). It proves the
// equivalence guard above actually bites: a minimal `modules: [{path: .}]`
// manifest (default top-level-only includes) is a STRICT subset of conventional
// discovery, dropping the examples/ subtree. This documents WHY the committed
// manifest needs explicit examples/* includes.
func TestLocator_MinimalManifestNarrowsScope(t *testing.T) {
	const minimalManifest = "version: v1\nmodules:\n  - path: .\n"
	layout := fstest.MapFS{
		"cells/platform/cell.yaml":           &fstest.MapFile{Data: []byte("id: platform\n")},
		"examples/demo/cells/demo/cell.yaml": &fstest.MapFile{Data: []byte("id: demo\n")},
		"examples/demo/assembly.yaml":        &fstest.MapFile{Data: []byte("id: demo\n")},
		"examples/demo/journeys/J-demo.yaml": &fstest.MapFile{Data: []byte("id: J-demo\n")},
	}

	convFS := fstest.MapFS{}
	manFS := fstest.MapFS{".gocell/manifest.yaml": &fstest.MapFile{Data: []byte(minimalManifest)}}
	for k, v := range layout {
		convFS[k] = v
		manFS[k] = v
	}

	convLoc, err := NewLocatorFS(convFS, WithLocatorMode(LocatorConventional))
	if err != nil {
		t.Fatalf("NewLocatorFS conventional: %v", err)
	}
	convSrc, err := convLoc.Discover()
	if err != nil {
		t.Fatalf("Discover conventional: %v", err)
	}
	manLoc, err := NewLocatorFS(manFS, WithLocatorMode(LocatorManifest))
	if err != nil {
		t.Fatalf("NewLocatorFS manifest: %v", err)
	}
	manSrc, err := manLoc.Discover()
	if err != nil {
		t.Fatalf("Discover manifest: %v", err)
	}

	conv := summariseSources(convSrc)
	man := summariseSources(manSrc)

	if reflect.DeepEqual(conv, man) {
		t.Fatal("minimal manifest unexpectedly equals conventional scope; the guard " +
			"in TestLocator_RealTreeConventionalManifestEquivalence would be ineffective")
	}
	if !isSubset(man, conv) {
		t.Errorf("minimal manifest is not a subset of conventional; got manifest-only entries: %v",
			difference(man, conv))
	}
	dropped := difference(conv, man)
	if !containsExamplesSource(dropped) {
		t.Errorf("expected minimal manifest to drop the examples/ subtree, dropped: %v", dropped)
	}
}

func containsExamplesSource(summaries []string) bool {
	for _, s := range summaries {
		// summary form is "kind:path:cellID"; the path segment carries examples/.
		if strings.Contains(s, "examples/") {
			return true
		}
	}
	return false
}

// difference returns the elements of a not present in b (both are sorted
// summary slices from summariseSources).
func difference(a, b []string) []string {
	set := make(map[string]struct{}, len(b))
	for _, s := range b {
		set[s] = struct{}{}
	}
	var out []string
	for _, s := range a {
		if _, ok := set[s]; !ok {
			out = append(out, s)
		}
	}
	return out
}

func isSubset(sub, super []string) bool {
	return len(difference(sub, super)) == 0
}

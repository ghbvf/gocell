// INVARIANT: ARCHTEST-HELPERS-01

package archtest

import (
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// typeOwner unwraps a *T → T and returns the *types.TypeName for the owning
// named type, or nil if the type isn't a *types.Named. Shared by the typed
// field/method receiver checks across funnel and reflect blind-spot archtests
// (credential_authority / session_revoked / reflect_string_arg).
func typeOwner(t types.Type) *types.TypeName {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil
	}
	return named.Obj()
}

// findAllGoFilesInDir walks dir and returns all .go files (including _test.go).
// Skips vendor, .git, generated, and testdata directories.
func findAllGoFilesInDir(dir string) ([]string, error) {
	scope := scanner.ModuleScope(dir, scanner.IncludeTests())
	files, err := scope.Files()
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// findCellProductionGoFiles enumerates production .go files for every cell
// declared in the project's metadata (covering both top-level cells/ and
// examples/*/cells/ via metadata.NewParser's path-pattern matching).
// Excludes _test.go, vendor, worktrees, testdata, generated, .git.
func findCellProductionGoFiles(root string) ([]string, error) {
	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		return nil, err
	}

	relDirs := make([]string, 0, len(project.Cells))
	for _, c := range project.Cells {
		relDirs = append(relDirs, filepath.Dir(c.File))
	}
	scope := scanner.DirsScope(root, relDirs)
	files, err := scope.Files()
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// findArchTestDir returns the absolute path of the tools/archtest directory,
// used to locate testdata fixtures at test runtime.
func findArchTestDir(t *testing.T) string {
	t.Helper()
	root := findModuleRoot(t)
	return filepath.Join(root, "tools", "archtest")
}

// TestFindCellProductionGoFiles_IncludesExamples is a Wave 1 RED test for
// Part B scanning-root unification. It asserts the metadata-rooted helper
// returns at least one cell file under examples/. Pre-refactor the helper
// only walks the top-level cells/ directory and FALSE-NEGATIVES any cell in
// examples/{iotdevice,todoorder}/cells/...; this test FAILS pre-GREEN and
// PASSES once findCellProductionGoFiles enumerates via metadata.NewParser.
func TestFindCellProductionGoFiles_IncludesExamples(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	files, err := findCellProductionGoFiles(root)
	if err != nil {
		t.Fatalf("findCellProductionGoFiles: %v", err)
	}
	var foundExample bool
	for _, p := range files {
		rel, _ := filepath.Rel(root, p)
		if strings.HasPrefix(filepath.ToSlash(rel), "examples/") {
			foundExample = true
			break
		}
	}
	if !foundExample {
		t.Errorf("findCellProductionGoFiles must enumerate cells under examples/ (Wave 2 GREEN " +
			"requires metadata.NewParser-based discovery covering examples/*/cells/...); got " +
			"only top-level cells/. Refactor: use *ProjectMeta.Cells + filepath.Dir(c.File).")
	}
}

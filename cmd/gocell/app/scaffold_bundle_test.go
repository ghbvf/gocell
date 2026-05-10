package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunScaffoldCell_BundleProducesSliceAndContract is a RED test for K#09:
// `gocell scaffold cell` upgrade produces full bundle (cell + slice + contract).
//
// Replaces the old behavior where only cell.yaml + cell.go were emitted.
func TestRunScaffoldCell_BundleProducesSliceAndContract(t *testing.T) {
	t.Parallel()

	root := setupBundleTestProject(t)

	args := []string{
		"--id=mybundlecell",
		"--type=core",
		"--level=L2",
		"--team=platform",
		"--role=cell-owner",
		"--with-http",
		"--skip-generate", // RED scope: only verify scaffold output, not codegen invocation
	}
	if err := scaffoldCell(root, args); err != nil {
		t.Fatalf("scaffoldCell bundle: %v", err)
	}

	wants := []string{
		"cells/mybundlecell/cell.yaml",
		"cells/mybundlecell/cell.go",
		"cells/mybundlecell/slices/mybundlecellexample/slice.yaml",
		"cells/mybundlecell/slices/mybundlecellexample/service.go",
		"cells/mybundlecell/slices/mybundlecellexample/service_test.go",
		"contracts/http/mybundlecell/example/v1/contract.yaml",
		"contracts/http/mybundlecell/example/v1/request.schema.json",
		"contracts/http/mybundlecell/example/v1/response.schema.json",
	}
	for _, rel := range wants {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("bundle missing %s: %v", rel, err)
		}
	}

	// contract.yaml must NOT carry an explicit `codegen:` line (K#09 funnel default).
	cYAMLPath := filepath.Join(root, "contracts", "http", "mybundlecell", "example", "v1", "contract.yaml")
	c, _ := os.ReadFile(cYAMLPath) //nolint:gosec // tempdir test fixture
	if strings.Contains(string(c), "codegen:") {
		t.Errorf("scaffold contract.yaml must not declare codegen: (parser default true);\n got:\n%s", c)
	}
}

// TestRunScaffoldCell_BundleWithAutoGenerate covers the autoGenerateCellBundleArtifacts
// path: scaffold cell without --skip-generate must produce both cellgen and
// contractgen output (cell_gen.go + types_gen.go) so the bundle is buildable
// + testable end-to-end.
func TestRunScaffoldCell_BundleWithAutoGenerate(t *testing.T) {
	t.Parallel()

	root := setupBundleTestProject(t)

	args := []string{
		"--id=autogencell",
		"--type=core",
		"--level=L2",
		"--team=platform",
		"--role=cell-owner",
		"--with-http",
	}
	if err := scaffoldCell(root, args); err != nil {
		t.Fatalf("scaffoldCell auto-generate: %v", err)
	}

	wants := []string{
		"cells/autogencell/cell_gen.go",
		"generated/contracts/http/autogencell/example/v1/types_gen.go",
		"generated/contracts/http/autogencell/example/v1/iface_gen.go",
	}
	for _, rel := range wants {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("auto-generate missing %s: %v", rel, err)
		}
	}
}

func setupBundleTestProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module github.com/ghbvf/gocell\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

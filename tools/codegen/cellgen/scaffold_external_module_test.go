package cellgen

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/testutil/fileutil"
)

// TestScaffoldCell_FrameworkImportModuleIndependent is the Hard golden guard for
// issue #2126 (cell side): a scaffolded cell.go's framework imports must be the
// fixed gocell framework module path (github.com/ghbvf/gocell/framework/...),
// INDEPENDENT of the consumer's own module path. The consumer module path flows
// ONLY into the errcode.RegisterPrefix owner identity, never into a framework import.
//
// It renders under an EXTERNAL consumer module (example.com/external) — the case the
// in-repo scaffold golden (cmd/gocell/app/testdata/scaffold_golden, ModulePath=gocell)
// could not expose, because there {{.ModulePath}}/framework coincidentally equals the
// real framework path. Re-parameterizing a framework import by {{.ModulePath}} would
// render example.com/external/framework/... (a path that does not exist under the
// consumer module → uncompilable) and diverge from this byte golden → CI red.
//
// Update with:
//
//	go test ./tools/codegen/cellgen/ -run TestScaffoldCell_FrameworkImportModuleIndependent -update
func TestScaffoldCell_FrameworkImportModuleIndependent(t *testing.T) {
	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:     mustID(t, "extcell"),
		StructName: "ExtCell",
		Package:    "extcell",
		ModulePath: "example.com/external",
		OwnerTeam:  "platform",
		OwnerRole:  "cell-owner",
	}
	if err := ScaffoldCell(dir, "cells/extcell", spec); err != nil {
		t.Fatalf("ScaffoldCell: %v", err)
	}
	got := fileutil.MustReadFile(t, filepath.Join(dir, "cells", "extcell", "cell.go"))

	goldenPath := filepath.Join("testdata", "golden", "scaffold_cell_external_module.go.golden")
	if *updateGolden {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("golden updated: %s", goldenPath)
		return
	}
	golden := fileutil.MustReadFile(t, goldenPath)
	if !bytes.Equal(got, golden) {
		t.Errorf("scaffold cell.go diverges from golden:\n--- got ---\n%s\n--- want ---\n%s", got, golden)
	}
	// Belt: framework import must be the fixed gocell path AND must NOT carry the
	// consumer module path (issue #2126) — grouping-independent semantic checks that
	// complement the byte golden (so a carelessly emptied golden cannot pass silently).
	if !bytes.Contains(got, []byte("github.com/ghbvf/gocell/framework/kernel/cell")) {
		t.Errorf("scaffold cell.go missing the fixed framework import (issue #2126):\n%s", got)
	}
	if bytes.Contains(got, []byte("example.com/external/framework")) {
		t.Errorf("framework import leaked consumer module path (issue #2126):\n%s", got)
	}
}

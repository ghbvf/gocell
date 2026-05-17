package cellgen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gofumpt "mvdan.cc/gofumpt/format"

	"github.com/ghbvf/gocell/kernel/scaffoldid"
	"github.com/ghbvf/gocell/tools/codegen"
)

// TestScaffoldCell_OutputIsGofumptClean drives cellgen's renderTemplate
// formatter contract: every .go file ScaffoldCell writes must round-trip
// through gofumpt without changes — proving cellgen emits the same canonical
// shape the CI formatter gate (.golangci.yml gofumpt) requires.
func TestScaffoldCell_OutputIsGofumptClean(t *testing.T) {
	dir := t.TempDir()
	spec := ScaffoldSpec{
		CellID:     scaffoldid.MustParse("fmtcell"),
		StructName: "FmtCell",
		Package:    "fmtcell",
		ModulePath: "github.com/example/myproject",
		OwnerTeam:  "platform",
		OwnerRole:  "cell-owner",
	}
	if err := ScaffoldCell(dir, "cells/fmtcell", spec); err != nil {
		t.Fatalf("ScaffoldCell: %v", err)
	}

	root := filepath.Join(dir, "cells", "fmtcell")
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		got, readErr := os.ReadFile(path) //nolint:gosec // R2-approved: walks only paths ScaffoldCell just wrote under t.TempDir
		if readErr != nil {
			return readErr
		}
		canonical, fmtErr := gofumpt.Source(got, codegen.GofumptOptions)
		if fmtErr != nil {
			t.Errorf("gofumpt.Source on %s: %v", path, fmtErr)
			return nil
		}
		if !bytes.Equal(got, canonical) {
			rel, _ := filepath.Rel(dir, path)
			t.Errorf("scaffold output %s is not gofumpt-canonical:\n--- got\n%s\n--- gofumpt(got)\n%s",
				rel, got, canonical)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk scaffold output: %v", walkErr)
	}
}

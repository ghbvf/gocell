package app

import (
	"go/format"
	"os"
	"path/filepath"
	"testing"

	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
	"github.com/ghbvf/gocell/tools/generatedcatalog"
)

// TestEmitCatalogFile_OutputCompiles verifies that emitCatalogFile produces
// valid Go source that gofmt accepts (i.e. the template produces syntactically
// correct code for a representative graph).
func TestEmitCatalogFile_OutputCompiles(t *testing.T) {
	t.Parallel()

	g := kerneldepgraph.FromNodes("github.com/example/mod", []*kerneldepgraph.Node{
		{
			ID:      "github.com/example/mod/pkg/a",
			Layer:   "pkg",
			Imports: []string{"github.com/example/mod/pkg/b", "fmt"},
		},
		{
			ID:      "github.com/example/mod/pkg/b",
			Layer:   "pkg",
			CellID:  "testcell",
			SliceID: "testslice",
			Imports: []string{},
		},
		{
			ID:       "github.com/example/mod/internal/x",
			Layer:    "kernel",
			TestOnly: true,
			Imports:  []string{},
		},
	})

	src, err := generatedcatalog.EmitFile("mypkg", "github.com/example/mod", g)
	if err != nil {
		t.Fatalf("emitCatalogFile: %v", err)
	}

	// Verify the output is valid Go (format.Source is the canonical check).
	if _, err := format.Source(src); err != nil {
		t.Fatalf("generated source is not valid Go: %v\n--- source ---\n%s", err, src)
	}
}

// TestEmitCatalogFile_EmptyGraph verifies that an empty graph produces a valid
// file with an empty nodes slice.
func TestEmitCatalogFile_EmptyGraph(t *testing.T) {
	t.Parallel()

	g := kerneldepgraph.FromNodes("github.com/example/mod", nil)
	src, err := generatedcatalog.EmitFile("main", "github.com/example/mod", g)
	if err != nil {
		t.Fatalf("emitCatalogFile: %v", err)
	}
	if _, err := format.Source(src); err != nil {
		t.Fatalf("generated source is not valid Go: %v", err)
	}
}

// TestGenerateCatalog_MissingFlags verifies that --out and --package are required.
func TestGenerateCatalog_MissingFlags(t *testing.T) {
	t.Parallel()

	if err := generateCatalog([]string{}); err == nil {
		t.Error("expected error when --out is missing")
	}
	if err := generateCatalog([]string{"--out=/tmp/x.go"}); err == nil {
		t.Error("expected error when --package is missing")
	}
}

// TestGenerateCatalog_WritesFile verifies that generateCatalog writes a valid
// Go file to the output path.
func TestGenerateCatalog_WritesFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	outPath := filepath.Join(dir, "catalog_gen.go")

	// Use the real project root so depgraph.Load has a valid module.
	root := projectRoot(t)

	if err := generateCatalog([]string{
		"--out=" + outPath,
		"--package=testpkg",
		"--root=" + root,
	}); err != nil {
		t.Fatalf("generateCatalog: %v", err)
	}

	content, err := os.ReadFile(outPath) //nolint:gosec // test-only path
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if _, err := format.Source(content); err != nil {
		t.Fatalf("output is not valid Go: %v", err)
	}
	// Must carry generated header.
	if string(content[:len("// Code generated")]) != "// Code generated" {
		t.Error("output does not start with '// Code generated' header")
	}
}

// projectRoot returns the nearest go.mod directory walking up from the test
// binary's working directory.
func projectRoot(t *testing.T) string {
	t.Helper()
	dir, err := findRoot()
	if err != nil {
		t.Fatalf("findRoot: %v", err)
	}
	return dir
}

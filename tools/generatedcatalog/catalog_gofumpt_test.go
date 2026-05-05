package generatedcatalog

import (
	"bytes"
	"testing"

	gofumpt "mvdan.cc/gofumpt/format"

	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
	"github.com/ghbvf/gocell/tools/codegen"
)

// TestEmitFile_OutputIsGofumptClean fences EmitFile's formatter contract.
// catalog_gen.go lands under cmd/corebundle/ where the CI lint exclusion
// for `generated/` does NOT apply, but the file is still build-tagged
// (`//go:build catalog_gen`) and only compiled by `go generate ./cmd/corebundle/`,
// not by the standard `go build ./...`. That gives the lint gate a narrow
// window where formatter regressions could slip through if a future template
// edit emits non-canonical bytes — this round-trip pins it down.
func TestEmitFile_OutputIsGofumptClean(t *testing.T) {
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
		},
	})

	got, err := EmitFile("main", "github.com/example/mod", g)
	if err != nil {
		t.Fatalf("EmitFile: %v", err)
	}
	canonical, err := gofumpt.Source(got, codegen.GofumptOptions)
	if err != nil {
		t.Fatalf("gofumpt.Source on EmitFile output: %v", err)
	}
	if !bytes.Equal(got, canonical) {
		t.Errorf("EmitFile output is not gofumpt-canonical:\n--- got\n%s\n--- gofumpt(got)\n%s",
			got, canonical)
	}
}

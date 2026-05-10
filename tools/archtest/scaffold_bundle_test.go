// invariants asserted in this file:
//   - INVARIANT: SCAFFOLD-BUNDLE-MARKER-01
//   - INVARIANT: SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01
//
// Package archtest — K#09 scaffold bundle invariants.
//
// SCAFFOLD-BUNDLE-MARKER-01: scaffolded cell.go (real ScaffoldCellBundle
// output) must embed the K#05 // +cell:listener: marker so the marker→cell.yaml
// drift detection (MARKERGEN-DRIFT-VERIFY-01) extends to scaffold output.
// AI-rebust: Medium (real-source AST capture — assertions read scaffold output,
// not template literals).
// Cannot be Hard: the marker is a hand-written string in a text/template, not a
// typed constant enforced by the type system. The Hard defense is in
// kernel/metadata.parseContract via contractYAMLHasKey AST inspection.
//
// SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01: scaffolded contract.yaml produced by
// ScaffoldCellBundle must NOT declare a top-level `codegen:` key — parser
// defaults Codegen=true (K#09 funnel), so emitting it is redundant and
// contradicts the funnel. AI-rebust: Medium (real-source AST capture — YAML
// parsed from actual scaffold output, not template string search).
// Cannot be Hard: same rationale as SCAFFOLD-BUNDLE-MARKER-01.
package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/tools/codegen/cellgen"
)

// scaffoldSmokeSpec is the ScaffoldSpec reused by both invariant tests.
var scaffoldSmokeSpec = cellgen.ScaffoldSpec{
	CellID:           "smokecell",
	StructName:       "SmokeCell",
	Package:          "smokecell",
	ModulePath:       "github.com/ghbvf/gocell",
	OwnerTeam:        "platform",
	OwnerRole:        "cell-owner",
	Type:             "core",
	ConsistencyLevel: "L1",
}

// TestScaffoldBundle_CellMarkerEmbedded asserts that ScaffoldCellBundle
// produces a cell.go that contains the K#05 +cell:listener: marker.
//
// INVARIANT: SCAFFOLD-BUNDLE-MARKER-01
// AI-rebust: Medium (real-source AST capture); see file-level godoc for rationale.
func TestScaffoldBundle_CellMarkerEmbedded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := cellgen.ScaffoldCellBundle(dir, scaffoldSmokeSpec); err != nil {
		t.Fatalf("ScaffoldCellBundle: %v", err)
	}
	cellGoPath := filepath.Join(dir, "cells", "smokecell", "cell.go")
	content, err := os.ReadFile(cellGoPath) //nolint:gosec // tempdir test fixture
	if err != nil {
		t.Fatalf("read cell.go: %v", err)
	}
	if !strings.Contains(string(content), "// +cell:listener:") {
		t.Errorf("INVARIANT SCAFFOLD-BUNDLE-MARKER-01 violated: scaffolded cell.go missing +cell:listener marker;\ngot:\n%s", content)
	}
}

// TestScaffoldBundle_ContractYAMLOmitsCodegenKey asserts that ScaffoldCellBundle
// produces a contract.yaml without a top-level `codegen:` key.
//
// INVARIANT: SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01
// AI-rebust: Medium (real-source AST capture); see file-level godoc for rationale.
func TestScaffoldBundle_ContractYAMLOmitsCodegenKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, cellgen.ScaffoldCellBundle(dir, scaffoldSmokeSpec))
	contractPath := filepath.Join(dir, "contracts", "http", "smokecell", "example", "v1", "contract.yaml")
	raw, err := os.ReadFile(contractPath) //nolint:gosec // tempdir test fixture
	require.NoError(t, err)

	var root yaml.Node
	require.NoError(t, yaml.Unmarshal(raw, &root))
	require.Equal(t, yaml.DocumentNode, root.Kind)
	require.Len(t, root.Content, 1)
	mapping := root.Content[0]
	require.Equal(t, yaml.MappingNode, mapping.Kind)
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key := mapping.Content[i]
		if key.Kind == yaml.ScalarNode && key.Value == "codegen" {
			t.Errorf("INVARIANT SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01 violated: scaffolded "+
				"contract.yaml top-level mapping must not declare `codegen:` key "+
				"(parser default true is the K#09 funnel); got:\n%s", raw)
			return
		}
	}
}

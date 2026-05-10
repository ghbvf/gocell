// invariants asserted in this file:
//   - INVARIANT: SCAFFOLD-BUNDLE-MARKER-01
//   - INVARIANT: SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01
//   - INVARIANT: SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01
//
// Package archtest — K#09 scaffold bundle invariants.
//
// SCAFFOLD-BUNDLE-MARKER-01: scaffolded cell.go (real ScaffoldCellBundle
// output) must embed the K#05 // +cell:listener: marker so the marker→cell.yaml
// drift detection (MARKERGEN-DRIFT-VERIFY-01) extends to scaffold output.
// AI-rebust: Medium (typed marker funnel via cellgen.ListenerMarker exported const;
// archtest cross-validates const definition + template reference).
//
// SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01: scaffolded contract.yaml produced by
// ScaffoldCellBundle must NOT declare a top-level `codegen:` key — parser
// defaults Codegen=true (K#09 funnel), so emitting it is redundant and
// contradicts the funnel. AI-rebust: Medium (real-source AST capture — YAML
// parsed from actual scaffold output, not template string search).
// Cannot be Hard: contract.yaml is source of truth; YAML Node structured
// parsing is the Medium ceiling for this invariant.
//
// SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01: cellgen.ListenerMarker must exist
// as an exported const with the canonical K#05 marker literal value, and the
// scaffold-cell template must reference it via {{.ListenerMarker}} rather than
// hand-typing the literal. AI-rebust: Medium (typed const + typeseval
// cross-validation; template literal hand-typing is statically rejected by this
// archtest).
package archtest

import (
	"go/constant"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
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
	if !strings.Contains(string(content), cellgen.ListenerMarker) {
		t.Errorf("INVARIANT SCAFFOLD-BUNDLE-MARKER-01 violated: scaffolded cell.go missing %s marker;\ngot:\n%s", cellgen.ListenerMarker, content)
	}
}

// TestScaffoldBundle_ListenerMarkerTypedConst asserts that
// cellgen.ListenerMarker exists as an exported const with the expected
// K#05 marker literal value, and that the scaffold-cell template references
// it via {{.ListenerMarker}} rather than hand-typing the literal string.
//
// INVARIANT: SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01
// AI-rebust: Medium (typed const + typeseval cross-validation).
func TestScaffoldBundle_ListenerMarkerTypedConst(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	// Load cellgen package with full type information.
	resolver, err := typeseval.SharedResolver(root, false, nil,
		"./tools/codegen/cellgen/...")
	if err != nil {
		t.Fatalf("typeseval.SharedResolver: %v", err)
	}

	const wantMarker = "// +cell:listener:"
	const wantConst = "ListenerMarker"
	const cellgenPkgPath = "github.com/ghbvf/gocell/tools/codegen/cellgen"

	// Locate the exported ListenerMarker const in the cellgen package.
	var constFound bool
	var constValue string
	for _, pkg := range resolver.Packages() {
		if pkg.Types == nil || pkg.PkgPath != cellgenPkgPath {
			continue
		}
		obj := pkg.Types.Scope().Lookup(wantConst)
		if obj == nil {
			break
		}
		c, ok := obj.(*types.Const)
		if !ok {
			t.Errorf("INVARIANT SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01 violated: "+
				"cellgen.%s is not a const (got %T)", wantConst, obj)
			return
		}
		if c.Val().Kind() != constant.String {
			t.Errorf("INVARIANT SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01 violated: "+
				"cellgen.%s is not a string const", wantConst)
			return
		}
		constFound = true
		constValue = constant.StringVal(c.Val())
		break
	}

	if !constFound {
		t.Errorf("INVARIANT SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01 violated: "+
			"cellgen.%s exported const not found in package %s; "+
			"add: const ListenerMarker = %q", wantConst, cellgenPkgPath, wantMarker)
		return
	}
	if constValue != wantMarker {
		t.Errorf("INVARIANT SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01 violated: "+
			"cellgen.ListenerMarker = %q; want %q", constValue, wantMarker)
		return
	}

	// Additionally verify that the scaffold-cell template:
	//   (a) references {{.ListenerMarker}} (confirming the typed-const funnel is wired)
	//   (b) does NOT contain the bare literal string outside of a template action
	//       (e.g. hand-typed "// +cell:listener:" without {{.ListenerMarker}})
	tmplPath := filepath.Join(root, "tools", "codegen", "cellgen", "templates", "scaffold-cell.tmpl")
	tmplContent, err := os.ReadFile(tmplPath) //nolint:gosec // repo-relative path, not user-supplied
	require.NoError(t, err, "read scaffold-cell.tmpl")
	tmplStr := string(tmplContent)

	const tmplRef = "{{.ListenerMarker}}"
	if !strings.Contains(tmplStr, tmplRef) {
		t.Errorf("INVARIANT SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01 violated: "+
			"scaffold-cell.tmpl does not reference %s; the template must use "+
			"the typed-const funnel instead of a hand-typed literal", tmplRef)
	}
	if strings.Contains(tmplStr, wantMarker) {
		t.Errorf("INVARIANT SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01 violated: "+
			"scaffold-cell.tmpl contains literal %q outside of {{.ListenerMarker}}; "+
			"remove the literal and rely solely on the typed-const reference", wantMarker)
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

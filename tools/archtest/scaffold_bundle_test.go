// invariants asserted in this file:
//   - INVARIANT: SCAFFOLD-BUNDLE-MARKER-01
//   - INVARIANT: SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01
//
// Package archtest — K#09 scaffold bundle invariants.
//
// SCAFFOLD-BUNDLE-MARKER-01: scaffold-cell.tmpl must embed the K#05
// // +cell:listener: marker as a const literal so the marker→cell.yaml
// drift detection (MARKERGEN-DRIFT-VERIFY-01) extends to scaffold output.
// AI-rebust: Soft (string anchor on template content; the Hard funnel is in
// kernel/metadata.parseContract via contractYAMLHasKey AST inspection —
// these archtests are belt-and-suspenders against template regression).
//
// SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01: scaffold-contract template (the
// K#09 ScaffoldExampleContract path) must NOT emit `codegen:` field —
// parser defaults Codegen=true (K#09 funnel), so emitting it is redundant
// and contradicts the funnel. AI-rebust: Soft (string anchor on template
// content; the Hard funnel is in kernel/metadata.parseContract via
// contractYAMLHasKey AST inspection — these archtests are
// belt-and-suspenders against template regression).
package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	scaffoldCellTplRel     = "tools/codegen/cellgen/templates/scaffold-cell.tmpl"
	scaffoldContractTplRel = "tools/codegen/cellgen/templates/scaffold-contract.tmpl"
)

// TestScaffoldBundle_CellMarkerEmbedded asserts that the cellgen scaffold
// cell template embeds `// +cell:listener:` so K#05 marker drift detection
// extends to scaffold output.
//
// INVARIANT: SCAFFOLD-BUNDLE-MARKER-01
// AI-rebust archtest gate; see file-level godoc for the rationale.
func TestScaffoldBundle_CellMarkerEmbedded(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	tmpl := filepath.Join(root, scaffoldCellTplRel)
	content, err := os.ReadFile(tmpl) //nolint:gosec // archtest reads in-repo template files
	if err != nil {
		t.Fatalf("read scaffold-cell.tmpl: %v", err)
	}
	if !strings.Contains(string(content), "// +cell:listener:") {
		t.Errorf("INVARIANT SCAFFOLD-BUNDLE-MARKER-01 violated: scaffold-cell.tmpl must embed "+
			"`// +cell:listener:` const literal so K#05 MARKERGEN-DRIFT-VERIFY-01 covers scaffold output;\ngot template:\n%s",
			content)
	}
}

// TestScaffoldBundle_ContractTemplateNoCodegenLiteral asserts that the
// K#09 contract scaffold template does NOT emit `codegen:` field.
// Parser default = true (K#09 funnel) → field is redundant; embedding it
// in scaffold output contradicts the funnel.
//
// INVARIANT: SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01
// AI-rebust archtest gate; see file-level godoc for the rationale.
func TestScaffoldBundle_ContractTemplateNoCodegenLiteral(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	tmpl := filepath.Join(root, scaffoldContractTplRel)
	content, err := os.ReadFile(tmpl) //nolint:gosec // archtest reads in-repo template files
	if err != nil {
		t.Fatalf("read scaffold-contract.tmpl: %v", err)
	}
	if strings.Contains(string(content), "codegen:") {
		t.Errorf("INVARIANT SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01 violated: scaffold-contract.tmpl "+
			"must NOT emit `codegen:` (parser default true is the K#09 funnel);\ngot template:\n%s",
			content)
	}
}

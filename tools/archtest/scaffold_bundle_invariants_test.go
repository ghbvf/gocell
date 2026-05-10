// invariants:
//   - INVARIANT: SCAFFOLD-BUNDLE-MARKER-01
//   - INVARIANT: SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01
//
// Package archtest — K#09 scaffold bundle invariants.
//
// SCAFFOLD-BUNDLE-MARKER-01: scaffold-cell.tmpl must embed the K#05
// // +cell:listener: marker as a const literal so the marker→cell.yaml
// drift detection (MARKERGEN-DRIFT-VERIFY-01) extends to scaffold output.
// AI-rebust: Hard — verified by template const literal embed (cannot be
// dropped without changing the embedded template).
//
// SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01: scaffold-contract template (the
// K#09 ScaffoldExampleContract path) must NOT emit `codegen:` field —
// parser defaults Codegen=true (K#09 funnel), so emitting it is redundant
// and contradicts the funnel. AI-rebust: Hard — schema default value funnel
// makes the field unrepresentable in scaffold output.
package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// findRepoRoot walks upward from cwd looking for go.mod (the project root).
func scaffoldFindRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("repo root not found from %s", dir)
		}
		dir = parent
	}
}

// TestScaffoldBundle_CellMarkerEmbedded asserts that the cellgen scaffold
// cell template embeds `// +cell:listener:` so K#05 marker drift detection
// extends to scaffold output.
//
// INVARIANT: SCAFFOLD-BUNDLE-MARKER-01
func TestScaffoldBundle_CellMarkerEmbedded(t *testing.T) {
	t.Parallel()

	root := scaffoldFindRepoRoot(t)
	tmpl := filepath.Join(root, "tools/codegen/cellgen/templates/scaffold-cell.tmpl")
	content, err := os.ReadFile(tmpl)
	if err != nil {
		t.Fatalf("read scaffold-cell.tmpl: %v", err)
	}
	if !strings.Contains(string(content), "// +cell:listener:") {
		t.Errorf("INVARIANT SCAFFOLD-BUNDLE-MARKER-01 violated: scaffold-cell.tmpl must embed `// +cell:listener:` const literal so K#05 MARKERGEN-DRIFT-VERIFY-01 covers scaffold output;\ngot template:\n%s", content)
	}
}

// TestScaffoldBundle_ContractTemplateNoCodegenLiteral asserts that the
// K#09 contract scaffold template does NOT emit `codegen:` field.
// Parser default = true (K#09 funnel) → field is redundant; embedding it
// in scaffold output contradicts the funnel.
//
// INVARIANT: SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01
func TestScaffoldBundle_ContractTemplateNoCodegenLiteral(t *testing.T) {
	t.Parallel()

	root := scaffoldFindRepoRoot(t)
	tmpl := filepath.Join(root, "tools/codegen/cellgen/templates/scaffold-contract.tmpl")
	content, err := os.ReadFile(tmpl)
	if err != nil {
		t.Fatalf("read scaffold-contract.tmpl: %v", err)
	}
	if strings.Contains(string(content), "codegen:") {
		t.Errorf("INVARIANT SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01 violated: scaffold-contract.tmpl must NOT emit `codegen:` (parser default true is the K#09 funnel);\ngot template:\n%s", content)
	}
}

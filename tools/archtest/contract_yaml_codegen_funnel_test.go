// invariants asserted in this file:
//   - INVARIANT: CONTRACT-YAML-NO-CODEGEN-TRUE-LITERAL-01
//
// Package archtest — contract.yaml codegen funnel cleanup invariant.
//
// CONTRACT-YAML-NO-CODEGEN-TRUE-LITERAL-01: contract.yaml in contracts/,
// examples/, and cells/ must NOT declare `codegen: true` —
// kernel/metadata.parseContract AST funnel (K#09) defaults
// ContractMeta.Codegen=true when the key is absent, making the literal
// redundant. Explicit `codegen: false` is allowed (opt-out for kind=command
// contracts deferred from K#06 PR-4). cells/ is included defensively: no
// contract.yaml is expected there, but the scope guards against future drift.
//
// AI-rebust: Medium (YAML Node 结构化解析；contract.yaml 是 source of
// truth，Medium 是 contract-yaml-level guard 的天花板).
//
// Scan is fail-closed: traverses the entire top-level mapping without early
// return; flags (a) any value `true` for a `codegen` key AND (b) any duplicate
// `codegen` key occurrence (which is structurally invalid — yaml.v3 struct
// decode rejects duplicate keys at parse time, but the raw Node AST preserves
// them, so the guard catches them before they reach the parser).
package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// scanContractCodegenViolations walks the top-level mapping of a contract.yaml
// document and returns one violation message per offending occurrence.
//
// Two violation classes:
//   - "codegen: true" literal (redundant under K#09 funnel default)
//   - duplicate `codegen` keys (structurally invalid; fail-closed guard so
//     order-dependent false→true sequences cannot slip past the scan)
//
// Caller is responsible for parsing the YAML; this helper only inspects an
// already-decoded *yaml.Node. Returns nil when no violation is found.
func scanContractCodegenViolations(rel string, root *yaml.Node) []string {
	if root == nil {
		return nil
	}
	doc := root
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return nil
		}
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	var (
		violations   []string
		codegenCount int
	)
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key := doc.Content[i]
		val := doc.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Value != "codegen" {
			continue
		}
		codegenCount++
		if val.Kind == yaml.ScalarNode && val.Value == "true" {
			violations = append(violations, fmt.Sprintf(
				"INVARIANT CONTRACT-YAML-NO-CODEGEN-TRUE-LITERAL-01 violated: %s: "+
					"declares `codegen: true` (at `codegen` key #%d) which is redundant — "+
					"kernel/metadata.parseContract (K#09 funnel) defaults Codegen=true "+
					"when the key is absent. Remove the line; keep `codegen: false` "+
					"only for opt-out contracts.",
				rel, codegenCount))
		}
	}
	if codegenCount > 1 {
		violations = append(violations, fmt.Sprintf(
			"INVARIANT CONTRACT-YAML-NO-CODEGEN-TRUE-LITERAL-01 violated: %s: "+
				"declares `codegen` %d times (duplicate key). yaml.v3 struct decode "+
				"rejects duplicate keys at parse time; the static guard also flags "+
				"them fail-closed so order-dependent sequences cannot slip past.",
			rel, codegenCount))
	}
	return violations
}

// parseTopLevelYAML decodes the file bytes into a raw yaml.Node (no struct
// schema) so scanContractCodegenViolations can inspect the top-level mapping
// faithfully, preserving duplicate keys.
func parseTopLevelYAML(b []byte) (*yaml.Node, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(b, &node); err != nil {
		return nil, err
	}
	return &node, nil
}

// TestContractYAML_NoCodegenTrueLiteral asserts that no contract.yaml under
// contracts/, examples/, or cells/ declares a top-level `codegen: true` key.
// cells/ is included defensively even though no contract.yaml is expected
// there — the scope prevents future drift if someone adds one.
//
// INVARIANT: CONTRACT-YAML-NO-CODEGEN-TRUE-LITERAL-01
// AI-rebust: Medium (YAML Node structured parse; cannot be Hard because
// contract.yaml is a hand-authored text file, not a Go type-system artifact).
func TestContractYAML_NoCodegenTrueLiteral(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	scope := scanner.DirsScope(root, []string{"contracts", "examples", "cells"},
		scanner.MatchRels(func(rel string) bool {
			return filepath.Base(rel) == "contract.yaml"
		}),
	)

	scanner.EachContentFile(t, scope, []string{".yaml"}, func(t *testing.T, fc scanner.ContentContext) {
		t.Helper()
		node, err := parseTopLevelYAML(fc.Bytes)
		if err != nil {
			t.Errorf("INVARIANT CONTRACT-YAML-NO-CODEGEN-TRUE-LITERAL-01: %s: failed to parse YAML: %v", fc.Rel, err)
			return
		}
		for _, v := range scanContractCodegenViolations(fc.Rel, node) {
			t.Error(v)
		}
	})
}

// TestContractYAML_NoCodegenTrueLiteral_NegativeFixture verifies that the
// invariant check fires for synthetic contract.yaml fixtures.
//
// INVARIANT: CONTRACT-YAML-NO-CODEGEN-TRUE-LITERAL-01
//
// Covers four fail-closed cases, each of which must yield ≥1 violation:
//   - single `codegen: true`
//   - duplicate `codegen` keys with `true` second
//   - duplicate `codegen` keys with `true` first
//   - duplicate `codegen: true` keys
//
// Absence of a violation in any case would indicate a fail-open scan.
func TestContractYAML_NoCodegenTrueLiteral_NegativeFixture(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{
			name: "single_codegen_true",
			body: "id: http.bad-contract.v1\nkind: http\ncodegen: true\n",
		},
		{
			name: "dup_codegen_false_then_true",
			body: "id: http.bad-contract.v1\nkind: http\ncodegen: false\ncodegen: true\n",
		},
		{
			name: "dup_codegen_true_then_false",
			body: "id: http.bad-contract.v1\nkind: http\ncodegen: true\ncodegen: false\n",
		},
		{
			name: "dup_codegen_true_then_true",
			body: "id: http.bad-contract.v1\nkind: http\ncodegen: true\ncodegen: true\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			contractDir := filepath.Join(tmp, "contracts", "http", "bad-contract", "v1")
			if err := os.MkdirAll(contractDir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(contractDir, "contract.yaml"), []byte(tc.body), 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}

			scope := scanner.DirsScope(tmp, []string{"contracts"},
				scanner.MatchRels(func(rel string) bool {
					return filepath.Base(rel) == "contract.yaml"
				}),
			)

			total := 0
			scanner.EachContentFile(t, scope, []string{".yaml"}, func(t *testing.T, fc scanner.ContentContext) {
				node, err := parseTopLevelYAML(fc.Bytes)
				if err != nil {
					t.Fatalf("parse fixture: %v", err)
				}
				total += len(scanContractCodegenViolations(fc.Rel, node))
			})

			if total == 0 {
				t.Errorf("negative fixture %q: expected ≥1 violation, got 0 (scan is fail-open for this case)", tc.name)
			}
		})
	}
}

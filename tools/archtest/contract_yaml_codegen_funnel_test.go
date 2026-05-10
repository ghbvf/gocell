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
package archtest

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

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
		var root yaml.Node
		if err := yaml.Unmarshal(fc.Bytes, &root); err != nil {
			t.Errorf("INVARIANT CONTRACT-YAML-NO-CODEGEN-TRUE-LITERAL-01: %s: failed to parse YAML: %v", fc.Rel, err)
			return
		}
		if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
			return
		}
		mapping := root.Content[0]
		if mapping.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			key := mapping.Content[i]
			val := mapping.Content[i+1]
			if key.Kind == yaml.ScalarNode && key.Value == "codegen" {
				if val.Kind == yaml.ScalarNode && val.Value == "true" {
					t.Errorf("INVARIANT CONTRACT-YAML-NO-CODEGEN-TRUE-LITERAL-01 violated: %s: "+
						"declares `codegen: true` which is redundant — kernel/metadata.parseContract "+
						"(K#09 funnel) defaults Codegen=true when the key is absent. "+
						"Remove the `codegen: true` line; keep `codegen: false` only for opt-out contracts.",
						fc.Rel)
				}
				return
			}
		}
	})
}

// TestContractYAML_NoCodegenTrueLiteral_NegativeFixture verifies that the
// invariant check fires for a synthetic contract.yaml declaring `codegen: true`.
// Uses testing.TempDir to avoid polluting the real source tree.
//
// INVARIANT: CONTRACT-YAML-NO-CODEGEN-TRUE-LITERAL-01.
func TestContractYAML_NoCodegenTrueLiteral_NegativeFixture(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	contractDir := filepath.Join(tmp, "contracts", "http", "bad-contract", "v1")
	if err := os.MkdirAll(contractDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	badYAML := "id: http.bad-contract.v1\nkind: http\ncodegen: true\n"
	if err := os.WriteFile(filepath.Join(contractDir, "contract.yaml"), []byte(badYAML), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	scope := scanner.DirsScope(tmp, []string{"contracts"},
		scanner.MatchRels(func(rel string) bool {
			return filepath.Base(rel) == "contract.yaml"
		}),
	)

	violations := 0
	scanner.EachContentFile(t, scope, []string{".yaml"}, func(t *testing.T, fc scanner.ContentContext) {
		var root yaml.Node
		if err := yaml.Unmarshal(fc.Bytes, &root); err != nil {
			return
		}
		if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
			return
		}
		mapping := root.Content[0]
		if mapping.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			key := mapping.Content[i]
			val := mapping.Content[i+1]
			if key.Kind == yaml.ScalarNode && key.Value == "codegen" {
				if val.Kind == yaml.ScalarNode && val.Value == "true" {
					violations++
				}
				return
			}
		}
	})

	if violations != 1 {
		t.Errorf("negative fixture: expected 1 violation for codegen: true, got %d", violations)
	}
}

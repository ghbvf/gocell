// invariants asserted in this file:
//   - INVARIANT: CONTRACT-YAML-NO-CODEGEN-TRUE-LITERAL-01
//
// Package archtest — contract.yaml codegen funnel cleanup invariant.
//
// CONTRACT-YAML-NO-CODEGEN-TRUE-LITERAL-01: contract.yaml in contracts/ and
// examples/ must NOT declare `codegen: true` — kernel/metadata.parseContract
// AST funnel (K#09) defaults ContractMeta.Codegen=true when the key is
// absent, making the literal redundant. Explicit `codegen: false` is
// allowed (opt-out for kind=command contracts deferred from K#06 PR-4).
//
// AI-rebust: Medium (YAML Node 结构化解析；contract.yaml 是 source of
// truth，Medium 是 contract-yaml-level guard 的天花板).
package archtest

import (
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// TestContractYAML_NoCodegenTrueLiteral asserts that no contract.yaml under
// contracts/ or examples/ declares a top-level `codegen: true` key.
//
// INVARIANT: CONTRACT-YAML-NO-CODEGEN-TRUE-LITERAL-01
// AI-rebust: Medium (YAML Node structured parse; cannot be Hard because
// contract.yaml is a hand-authored text file, not a Go type-system artifact).
func TestContractYAML_NoCodegenTrueLiteral(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	scope := scanner.DirsScope(root, []string{"contracts", "examples"},
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

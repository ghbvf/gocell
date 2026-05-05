// COMMAND-PROJECTION-EXPLICIT-01 — forward-guard for kind=command|projection codegen.
// SPEC-GEN-VALUE-PARITY-01       — guard for generated spec_gen.go value accuracy.
//
// # COMMAND-PROJECTION-EXPLICIT-01
//
// Invariant: when a kind=command or kind=projection contract opts into codegen
// (codegen=true), the generator emits only types_gen.go + iface_gen.go; it must
// NOT emit handler_gen.go (HTTP-only), spec_gen.go, or subscription_gen.go
// (event-only). This gate detects generator regressions that would accidentally
// emit event-style or http-style artifacts for these placeholder kinds.
//
// No contracts currently have kind=command|projection with codegen=true, so this
// gate is permanently GREEN today. It becomes load-bearing the moment a future
// maintainer adds codegen: true to a command or projection contract.
//
// # SPEC-GEN-VALUE-PARITY-01
//
// Invariant: for every kind=event contract with codegen=true, the generated
// spec_gen.go must contain:
//   - ID field value equal to the contract ID
//   - Topic field value equal to contractID with the trailing .vN suffix stripped
//
// A template bug or manual edit could silently produce wrong ID/Topic values that
// compile successfully but route events to the wrong queue at runtime. This gate
// catches that class of drift before it reaches production.
//
// ref: docs/plans/202605011500-029-master-roadmap.md K#06
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestCOMMAND_PROJECTION_EXPLICIT_01 verifies that kind=command|projection contracts
// with codegen=true do not have event-style or http-style generated artifacts.
func TestCOMMAND_PROJECTION_EXPLICIT_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	project := mustParseProjectContracts(t, root)

	for _, contract := range project.Contracts {
		if contract.Kind != "command" && contract.Kind != "projection" {
			continue
		}
		if !contract.Codegen {
			continue // not opted into codegen — gate ignores these
		}

		pkgDir := filepath.Join(root, contractIDToExpectedPkgPath(contract.ID))
		contract := contract // capture loop var

		t.Run(contract.ID, func(t *testing.T) {
			t.Parallel()

			// handler_gen.go must NOT exist (http-only artifact).
			handlerPath := filepath.Join(pkgDir, "handler_gen.go")
			if _, err := os.Stat(handlerPath); err == nil {
				t.Errorf(
					"COMMAND-PROJECTION-EXPLICIT-01: contract %q (kind=%s) has unexpected handler_gen.go at %s;"+
						" handler generation is HTTP-only — remove it and re-run `gocell generate contract %s`",
					contract.ID, contract.Kind, handlerPath, contract.ID,
				)
			}

			// spec_gen.go must NOT exist (event-only artifact).
			specPath := filepath.Join(pkgDir, "spec_gen.go")
			if _, err := os.Stat(specPath); err == nil {
				t.Errorf(
					"COMMAND-PROJECTION-EXPLICIT-01: contract %q (kind=%s) has unexpected spec_gen.go at %s;"+
						" spec generation is event-only — remove it and re-run `gocell generate contract %s`",
					contract.ID, contract.Kind, specPath, contract.ID,
				)
			}

			// subscription_gen.go must NOT exist (event-only artifact).
			subPath := filepath.Join(pkgDir, "subscription_gen.go")
			if _, err := os.Stat(subPath); err == nil {
				t.Errorf(
					"COMMAND-PROJECTION-EXPLICIT-01: contract %q (kind=%s) has unexpected subscription_gen.go at %s;"+
						" subscription generation is event-only — remove it and re-run `gocell generate contract %s`",
					contract.ID, contract.Kind, subPath, contract.ID,
				)
			}
		})
	}
}

// TestSPEC_GEN_VALUE_PARITY_01 verifies that for every kind=event contract with
// codegen=true, the generated spec_gen.go contains the correct ID and Topic values.
func TestSPEC_GEN_VALUE_PARITY_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	project := mustParseProjectContracts(t, root)

	for _, contract := range project.Contracts {
		if contract.Kind != "event" {
			continue
		}
		if !contract.Codegen {
			continue // not opted into codegen — gate ignores these
		}

		pkgDir := filepath.Join(root, contractIDToExpectedPkgPath(contract.ID))
		specPath := filepath.Join(pkgDir, "spec_gen.go")
		contract := contract // capture loop var

		t.Run(contract.ID, func(t *testing.T) {
			t.Parallel()

			content, err := os.ReadFile(specPath) //nolint:gosec // archtest reads paths it discovered
			if err != nil {
				t.Errorf(
					"SPEC-GEN-VALUE-PARITY-01: contract %q (kind=event, codegen=true) "+
						"cannot read spec_gen.go at %s: %v; run `gocell generate contract %s`",
					contract.ID, specPath, err, contract.ID,
				)
				return
			}

			src := string(content)

			id, topic, ok := extractSpecGenIDTopic(src)
			if !ok {
				t.Errorf(
					"SPEC-GEN-VALUE-PARITY-01: contract %q: cannot extract ContractSpec literal "+
						"from spec_gen.go at %s; regenerate with `gocell generate contract %s`",
					contract.ID, specPath, contract.ID,
				)
				return
			}
			if id != contract.ID {
				t.Errorf(
					"SPEC-GEN-VALUE-PARITY-01: contract %q: spec_gen.go ID field is %q;"+
						" expected %q; regenerate with `gocell generate contract %s`",
					contract.ID, id, contract.ID, contract.ID,
				)
			}
			if topic != contract.ID {
				t.Errorf(
					"SPEC-GEN-VALUE-PARITY-01: contract %q: spec_gen.go Topic field is %q;"+
						" expected %q; regenerate with `gocell generate contract %s`",
					contract.ID, topic, contract.ID, contract.ID,
				)
			}
		})
	}
}

// extractSpecGenIDTopic parses src as Go source and returns the ID and Topic
// field values from the first wrapper.ContractSpec composite literal. The
// fields are extracted from *ast.CompositeLit / *ast.KeyValueExpr nodes —
// comment / string-constant occurrences of ID/Topic literals do not count.
func extractSpecGenIDTopic(src string) (id, topic string, ok bool) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "spec_gen.go", src, parser.SkipObjectResolution)
	if err != nil {
		return "", "", false
	}
	var foundID, foundTopic string
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		cl, isCL := n.(*ast.CompositeLit)
		if !isCL {
			return true
		}
		sel, isSel := cl.Type.(*ast.SelectorExpr)
		if !isSel || sel.Sel.Name != "ContractSpec" {
			return true
		}
		for _, elt := range cl.Elts {
			kv, isKV := elt.(*ast.KeyValueExpr)
			if !isKV {
				continue
			}
			key, isIdent := kv.Key.(*ast.Ident)
			if !isIdent {
				continue
			}
			val, isLit := kv.Value.(*ast.BasicLit)
			if !isLit || val.Kind != token.STRING {
				continue
			}
			unq, uqErr := strconv.Unquote(val.Value)
			if uqErr != nil {
				continue
			}
			switch key.Name {
			case "ID":
				foundID = unq
			case "Topic":
				foundTopic = unq
			}
		}
		found = true
		return false
	})
	if !found {
		return "", "", false
	}
	return foundID, foundTopic, true
}

// TestSPEC_GEN_VALUE_PARITY_01_NegativeFixture_WrongIDInStruct asserts the
// scanner extracts the actual *ast.CompositeLit ID/Topic values rather than
// matching free-form text. The fixture has the expected ID/Topic in a
// comment but wrong values in the struct literal — legacy strings.Contains
// FALSE-POSITIVES via the comment; AST extract returns the wrong values.
func TestSPEC_GEN_VALUE_PARITY_01_NegativeFixture_WrongIDInStruct(t *testing.T) {
	t.Parallel()
	archDir := findArchTestDir(t)
	fixturePath := filepath.Join(archDir, "testdata", "spec_gen_value_parity_fixtures", "wrong_id", "spec_gen.go")
	body, err := os.ReadFile(fixturePath) //nolint:gosec // archtest fixture
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	id, topic, ok := extractSpecGenIDTopic(string(body))
	if !ok {
		t.Fatalf("extractSpecGenIDTopic: ContractSpec literal not found in fixture %s", fixturePath)
	}
	const wrongID = "event.fake.WRONG.v1"
	if id != wrongID {
		t.Errorf("SPEC-GEN-VALUE-PARITY-01 negative fixture wrong_id: extracted ID = %q; "+
			"want fixture's wrong-on-purpose value %q (AST scan must read CompositeLit, "+
			"not comment text)", id, wrongID)
	}
	if topic != wrongID {
		t.Errorf("SPEC-GEN-VALUE-PARITY-01 negative fixture wrong_id: extracted Topic = %q; "+
			"want fixture's wrong-on-purpose value %q", topic, wrongID)
	}
}

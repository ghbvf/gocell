// INVARIANT: INTERNAL-CONTRACT-CLIENTS-REQUIRED-01
//
// # INTERNAL-CONTRACT-CLIENTS-REQUIRED-01
//
// Invariant: every wrapper.ContractSpec{...} composite literal whose Path
// starts with "/internal/v1/" must declare a non-empty Clients field.
// Missing or empty Clients on an internal contract means there is no
// caller-cell allowlist, which defeats the purpose of the 4-part
// service-token caller-identity propagation.
//
// Exemption: specs whose ID appears in awaitingRealCallerAllowlist are in
// transition; they get a grace period until Wave 3 wires the real Clients.
// Once a spec is in the allowlist AND the corresponding RouteGroup has been
// wired (i.e. the spec appears in a reg.RouteGroup call), it is removed from
// the allowlist — the gate itself enforces this anti-forget rule.
//
// Detection: AST walk of all non-_test.go, non-generated/ production .go
// files under the module, scanning *ast.CompositeLit nodes whose type is
// wrapper.ContractSpec (by structural heuristic: has fields Path + ID).
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const ruleInternalContractClients01 = "INTERNAL-CONTRACT-CLIENTS-REQUIRED-01"

// awaitingRealCallerAllowlist holds spec IDs that are in transition:
// the Clients field has not yet been set because Wave 3 has not landed.
// Each entry MUST be removed when the real Clients are wired in.
var awaitingRealCallerAllowlist = map[string]bool{}

// TestINTERNAL_CONTRACT_CLIENTS_REQUIRED_01 enforces that every
// wrapper.ContractSpec composite literal with an /internal/v1/* Path
// declares a non-empty Clients field.
//
// Note: this test FAILS (RED) until Wave 2 adds Clients to ContractSpec
// and Wave 3 wires Clients on all internal contract literals.
func TestINTERNAL_CONTRACT_CLIENTS_REQUIRED_01(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	var violations []string
	var files []string

	// Collect all production .go files under the module (excluding generated/).
	searchDirs := []string{
		filepath.Join(root, "runtime"),
		filepath.Join(root, "cells"),
		filepath.Join(root, "cmd"),
		filepath.Join(root, "kernel"),
		filepath.Join(root, "adapters"),
	}

	for _, dir := range searchDirs {
		got, err := findProductionGoFilesInDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
		files = append(files, got...)
	}
	sort.Strings(files)

	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)

		hits, err := scanContractSpecMissingClients(f, rel)
		require.NoError(t, err)
		violations = append(violations, hits...)
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	if len(violations) > 0 {
		t.Errorf("%s: %d /internal/v1/* ContractSpec literals missing Clients field.\n"+
			"All internal contract specs must declare Clients to enforce caller-cell identity.\n"+
			"Add spec.Clients: []string{\"callerCellID\"} or add to awaitingRealCallerAllowlist.",
			ruleInternalContractClients01, len(violations))
	}
}

// scanContractSpecMissingClients parses a single .go file and returns
// violation strings for wrapper.ContractSpec composite literals that have
// an /internal/v1/* Path but no Clients field.
//
// Heuristic: a composite literal is treated as a ContractSpec candidate
// when it contains both "Path" and "ID" key fields.
func scanContractSpecMissingClients(path, rel string) ([]string, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	var violations []string
	scanner.EachNode[ast.CompositeLit](f, func(cl *ast.CompositeLit) {
		// Check if this looks like a ContractSpec by field names.
		if !isContractSpecLit(cl) {
			return
		}
		// Extract Path value.
		pathVal := contractSpecStringField(cl, "Path")
		if pathVal == "" || !strings.HasPrefix(pathVal, "/internal/v1/") {
			return
		}
		// Extract ID value for allowlist check.
		idVal := contractSpecStringField(cl, "ID")
		if awaitingRealCallerAllowlist[idVal] {
			return
		}
		// Check whether Clients field is present and non-empty.
		if !hasNonEmptyClientsField(cl) {
			pos := fset.Position(cl.Pos())
			violations = append(violations, fmt.Sprintf(
				"%s:%d: ContractSpec{ID:%q, Path:%q} has no Clients — "+
					"internal contracts must declare caller allowlist",
				rel, pos.Line, idVal, pathVal))
		}
	})
	return violations, nil
}

// isContractSpecLit heuristically identifies a composite literal as a
// wrapper.ContractSpec by checking for both "ID" and "Path" key fields.
func isContractSpecLit(cl *ast.CompositeLit) bool {
	hasID := false
	hasPath := false
	scanner.EachNode[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			return
		}
		switch key.Name {
		case "ID":
			hasID = true
		case "Path":
			hasPath = true
		}
	})
	return hasID && hasPath
}

// contractSpecStringField returns the string literal value of the named field
// in a composite literal, or "" if absent or not a string literal.
func contractSpecStringField(cl *ast.CompositeLit, fieldName string) string {
	result := ""
	scanner.EachNode[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != fieldName {
			return
		}
		lit, ok := kv.Value.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return
		}
		// Strip surrounding quotes.
		s := lit.Value
		if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
			result = s[1 : len(s)-1]
		} else {
			result = s
		}
	})
	return result
}

// hasNonEmptyClientsField returns true if the composite literal has a
// Clients field that is a non-empty slice literal.
func hasNonEmptyClientsField(cl *ast.CompositeLit) bool {
	found := false
	scanner.EachNode[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Clients" {
			return
		}
		// Clients field exists — check it's a non-empty slice literal.
		compLit, ok := kv.Value.(*ast.CompositeLit)
		if ok && len(compLit.Elts) > 0 {
			found = true
		}
	})
	return found
}

// TestINTERNAL_CONTRACT_CLIENTS_REQUIRED_01_NotContractSpecFalsePositive_Wave2_RED
// is a RED-step regression test (TDD per ai-collab.md). It pins down the
// false-positive of the current `hasID && hasPath` heuristic — specifically
// the family of bugs documented in PR445-FU finding F1:
//
//  1. A struct that merely shares the field names ID/Path/Clients but is NOT
//     wrapper.ContractSpec (e.g. SubItem below) is currently mis-identified
//     as ContractSpec. A type-aware identification (Wave 2) would resolve
//     cl.Type via go/types and judge equality with wrapper.ContractSpec,
//     correctly rejecting SubItem.
//  2. The `scanner.EachNode[ast.KeyValueExpr](cl, ...)` recursion (PR-Φ
//     subtree-walk semantics) leaks nested struct field names into the
//     outer literal's identification. Outer{Inner: ContractSpec{...}}
//     therefore appears to "have" ID/Path at the outer level.
//
// Wave 2 changes isContractSpecLit to type-aware (signature accepts
// *types.Info; nil → fail-safe false; non-wrapper.ContractSpec → false), and
// rewrites contractSpecStringField / hasNonEmptyClientsField to iterate
// cl.Elts directly (not subtree). After Wave 2, this sub-test transitions
// to GREEN by calling isContractSpecLit(cl, nil) which returns false for
// inline-parsed sources (no TypesInfo), so SubItem is no longer matched.
//
// Wave 1 (current heuristic) — assertion fails because:
//   - SubItem matches hasID && hasPath → true (false positive #1).
//   - Outer matches via subtree recursion picking up inner ID/Path
//     (false positive #2).
func TestINTERNAL_CONTRACT_CLIENTS_REQUIRED_01_NotContractSpecFalsePositive_Wave2_RED(t *testing.T) {
	t.Parallel()

	// Two false-positive cases for the `hasID && hasPath` heuristic:
	//  - SubItem: same field names but not wrapper.ContractSpec.
	//  - Outer:   not ContractSpec, but EachNode subtree walk leaks the
	//             inner ContractSpec's ID/Path into the outer literal's
	//             field-name set.
	src := `package fake

type SubItem struct {
	ID      string
	Path    string
	Clients []string
}

type ContractSpec struct {
	ID      string
	Path    string
	Clients []string
}

type Outer struct {
	Inner ContractSpec
}

var subItem = SubItem{ID: "a", Path: "/internal/v1/x", Clients: []string{"y"}}

var outer = Outer{
	Inner: ContractSpec{ID: "b", Path: "/internal/v1/y"},
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fake.go", src, parser.SkipObjectResolution)
	require.NoError(t, err, "parse inline fixture")

	var matched []string
	scanner.EachNode[ast.CompositeLit](f, func(cl *ast.CompositeLit) {
		if !isContractSpecLit(cl) {
			return
		}
		// Capture the outermost type identifier text for diagnosis.
		switch t := cl.Type.(type) {
		case *ast.Ident:
			matched = append(matched, t.Name)
		default:
			matched = append(matched, fmt.Sprintf("%T", t))
		}
	})

	// Wave 2 (type-aware) expectation: NEITHER SubItem nor Outer should be
	// matched (only true wrapper.ContractSpec literals are matched, and inline
	// parser.ParseFile has no TypesInfo so type-aware returns false → 0 matches).
	//
	// Wave 1 (current heuristic): matches SubItem (hasID && hasPath) AND
	// Outer (subtree recursion leak). Test fails → RED.
	require.Empty(t, matched,
		"isContractSpecLit must not match non-ContractSpec types or outer wrappers; got %v", matched)
}

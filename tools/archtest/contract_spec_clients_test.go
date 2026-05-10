// INVARIANT: INTERNAL-CONTRACT-CLIENTS-REQUIRED-01
//
// INTERNAL-CONTRACT-CLIENTS-REQUIRED-01
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
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		// Check if this looks like a ContractSpec by field names.
		if !isContractSpecLit(cl) {
			return true
		}
		// Extract Path value.
		pathVal := contractSpecStringField(cl, "Path")
		if pathVal == "" || !strings.HasPrefix(pathVal, "/internal/v1/") {
			return true
		}
		// Extract ID value for allowlist check.
		idVal := contractSpecStringField(cl, "ID")
		if awaitingRealCallerAllowlist[idVal] {
			return true
		}
		// Check whether Clients field is present and non-empty.
		if !hasNonEmptyClientsField(cl) {
			pos := fset.Position(cl.Pos())
			violations = append(violations, fmt.Sprintf(
				"%s:%d: ContractSpec{ID:%q, Path:%q} has no Clients — "+
					"internal contracts must declare caller allowlist",
				rel, pos.Line, idVal, pathVal))
		}
		return true
	})
	return violations, nil
}

// isContractSpecLit heuristically identifies a composite literal as a
// wrapper.ContractSpec by checking for both "ID" and "Path" key fields.
func isContractSpecLit(cl *ast.CompositeLit) bool {
	hasID := false
	hasPath := false
	for _, elt := range cl.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "ID":
			hasID = true
		case "Path":
			hasPath = true
		}
	}
	return hasID && hasPath
}

// contractSpecStringField returns the string literal value of the named field
// in a composite literal, or "" if absent or not a string literal.
func contractSpecStringField(cl *ast.CompositeLit, fieldName string) string {
	for _, elt := range cl.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != fieldName {
			continue
		}
		lit, ok := kv.Value.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return ""
		}
		// Strip surrounding quotes.
		s := lit.Value
		if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
			return s[1 : len(s)-1]
		}
		return s
	}
	return ""
}

// hasNonEmptyClientsField returns true if the composite literal has a
// Clients field that is a non-empty slice literal.
func hasNonEmptyClientsField(cl *ast.CompositeLit) bool {
	for _, elt := range cl.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Clients" {
			continue
		}
		// Clients field exists — check it's a non-empty slice literal.
		compLit, ok := kv.Value.(*ast.CompositeLit)
		if !ok {
			return false
		}
		return len(compLit.Elts) > 0
	}
	return false
}

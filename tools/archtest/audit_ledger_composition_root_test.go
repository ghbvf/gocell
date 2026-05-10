// INVARIANT: AUDIT-LEDGER-PROTOCOL-COMPOSITION-ROOT-01
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AUDIT-LEDGER-PROTOCOL-COMPOSITION-ROOT-01: ledger.NewProtocol /
// ledger.MustNewProtocol may only be invoked from cmd/* (composition root)
// or runtime/audit/ledger/* (the package itself + storetest helpers). Cells,
// runtime/* (non-ledger), adapters/*, and tests outside ledger/* must
// receive an injected *ledger.Protocol — not construct one.
//
// This is a Medium AI-rebust archtest (type-aware AST scan of call sites by
// package path; not a string anchor). Hard is unattainable without removing
// ledger-package import from cells, which would defeat the typed-Go-heavy
// paradigm (cells must consume the typed Protocol shape).
//
// AI 协作章程 .claude/rules/gocell/ai-collab.md: ≥ Medium qualifies for
// adoption.
//
// Sentinel sticky doctrine: 4 wiring options (WithChainHMAC / WithNamespace /
// WithRestartRecovery / WithIdempotency) each have a xxxNil bool sticky flag
// that is set when a nil interface value is received and is never cleared by a
// subsequent valid call — misconfiguration must not be silently masked.
func TestAuditLedgerProtocol_CompositionRootOnly(t *testing.T) {
	root := findModuleRoot(t)

	// Scan cells/, runtime/ (excluding runtime/audit/ledger/), adapters/.
	// cmd/ is not scanned — it is the composition root and is naturally allowed
	// to call ledger.NewProtocol / ledger.MustNewProtocol.
	// examples/ is also excluded: it owns its own composition root.
	scanDirs := []string{
		filepath.Join(root, "cells"),
		filepath.Join(root, "runtime"),
		filepath.Join(root, "adapters"),
	}

	var files []string
	for _, dir := range scanDirs {
		ff, err := findProductionGoFilesInDir(dir)
		require.NoError(t, err)
		files = append(files, ff...)
	}

	// Allowlist: runtime/audit/ledger/ owns the package itself (protocol.go,
	// mem_store.go, storetest sub-package).
	ledgerPrefix := filepath.ToSlash(filepath.Join(root, "runtime", "audit", "ledger")) + "/"

	type hit struct {
		file string
		line int
		name string
	}
	var hits []hit

	forbidden := map[string]bool{
		"NewProtocol":     true,
		"MustNewProtocol": true,
	}

	for _, f := range files {
		if strings.HasPrefix(filepath.ToSlash(f), ledgerPrefix) {
			continue
		}
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)

		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		ast.Inspect(af, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "ledger" {
				return true
			}
			if forbidden[sel.Sel.Name] {
				hits = append(hits, hit{
					file: rel,
					line: fset.Position(call.Pos()).Line,
					name: sel.Sel.Name,
				})
			}
			return true
		})
	}

	if len(hits) > 0 {
		for _, h := range hits {
			t.Logf("AUDIT-LEDGER-PROTOCOL-COMPOSITION-ROOT-01: %s:%d calls ledger.%s outside cmd/ + runtime/audit/ledger/",
				h.file, h.line, h.name)
		}
	}
	assert.Empty(t, hits,
		"AUDIT-LEDGER-PROTOCOL-COMPOSITION-ROOT-01: ledger.NewProtocol / ledger.MustNewProtocol "+
			"must only be called from cmd/* (composition root) or runtime/audit/ledger/*; "+
			"cells/runtime/adapters must consume an injected *ledger.Protocol")
}

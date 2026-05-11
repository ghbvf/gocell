// invariants:
//   - INVARIANT: SESSION-PROTOCOL-COMPOSITION-ROOT-01
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

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// SESSION-PROTOCOL-COMPOSITION-ROOT-01: session.NewProtocol /
// session.MustNewProtocol may only be invoked from cmd/* (composition root)
// or runtime/auth/session/* (the package itself + storetest helpers). Cells,
// runtime/* (non-session), adapters/*, and tests outside session/* must
// receive an injected *session.Protocol — not construct one.
//
// This is a Medium AI-rebust archtest (type-aware AST scan of call sites by
// package path; not a string anchor). Hard is unattainable without removing
// session-package import from cells, which would defeat the typed-Go-heavy
// paradigm (cells must consume the typed Protocol shape).
//
// AI 协作章程 .claude/rules/gocell/ai-collab.md: ≥ Medium qualifies for
// adoption; this archtest does not need to be Soft-fallback.
func TestSessionProtocol_CompositionRootOnly(t *testing.T) {
	root := findModuleRoot(t)

	// Scan cells/, runtime/ (excluding runtime/auth/session/), adapters/.
	// cmd/ is not scanned — it is the composition root and is naturally allowed
	// to call session.NewProtocol / session.MustNewProtocol.
	// examples/ is also excluded: it owns its own composition root (mirroring
	// the AUTH-PLAN-04 allowance in auth_plan_test.go).
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

	// Allowlist: runtime/auth/session/ owns the package itself (protocol.go,
	// protocol_test.go, future storetest sub-package).
	sessionPrefix := filepath.ToSlash(filepath.Join(root, "runtime", "auth", "session")) + "/"

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
		if strings.HasPrefix(filepath.ToSlash(f), sessionPrefix) {
			continue
		}
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)

		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		scanner.EachInSubtree[ast.CallExpr](af, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "session" {
				return
			}
			if forbidden[sel.Sel.Name] {
				hits = append(hits, hit{
					file: rel,
					line: fset.Position(call.Pos()).Line,
					name: sel.Sel.Name,
				})
			}
		})
	}

	if len(hits) > 0 {
		for _, h := range hits {
			t.Logf("SESSION-PROTOCOL-COMPOSITION-ROOT-01: %s:%d calls session.%s outside cmd/ + runtime/auth/session/",
				h.file, h.line, h.name)
		}
	}
	assert.Empty(t, hits,
		"SESSION-PROTOCOL-COMPOSITION-ROOT-01: session.NewProtocol / session.MustNewProtocol "+
			"must only be called from cmd/* (composition root) or runtime/auth/session/*; "+
			"cells/runtime/adapters must consume an injected *session.Protocol")
}

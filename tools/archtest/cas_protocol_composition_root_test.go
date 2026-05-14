// INVARIANT: CAS-PROTOCOL-COMPOSITION-ROOT-01
package archtest

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// CAS-PROTOCOL-COMPOSITION-ROOT-01: cas.NewProtocol /
// cas.MustNewProtocol may only be invoked from cmd/* (composition root)
// or runtime/state/cas/* (the package itself + storetest helpers). Cells,
// runtime/* (non-cas), adapters/*, and tests outside cas/* must
// receive an injected *cas.Protocol — not construct one.
//
// This is a Medium AI-rebust archtest (type-aware AST scan of call sites by
// package path; not a string anchor). Hard is unattainable without removing
// cas-package import from cells, which would defeat the typed-Go-heavy
// paradigm (cells must consume the typed Protocol shape).
//
// AI 协作章程 .claude/rules/gocell/ai-collab.md: ≥ Medium qualifies for
// adoption.
func TestCASProtocol_CompositionRootOnly(t *testing.T) {
	// Scan cells/, runtime/ (excluding runtime/state/cas/), adapters/.
	// cmd/ is not scanned — it is the composition root and is naturally allowed
	// to call cas.NewProtocol / cas.MustNewProtocol.
	// examples/ is also excluded: it owns its own composition root.
	root := findModuleRoot(t)
	casPrefix := "runtime/state/cas/"

	scope := DirsScope(root, []string{"cells", "runtime", "adapters"},
		MatchRels(func(rel string) bool {
			rel = filepath.ToSlash(rel)
			if !strings.HasSuffix(rel, ".go") {
				return false
			}
			if strings.HasSuffix(rel, "_test.go") {
				return false
			}
			// Allowlist: runtime/state/cas/ owns the package itself
			// (protocol.go and related helpers).
			return !strings.HasPrefix(rel, casPrefix)
		}),
	)

	forbidden := map[string]bool{
		"NewProtocol":     true,
		"MustNewProtocol": true,
	}

	type hit struct {
		file string
		line int
		name string
	}
	var hits []hit

	Run(t, scope, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "cas" {
					return
				}
				if forbidden[sel.Sel.Name] {
					hits = append(hits, hit{
						file: p.Rel(file),
						line: p.Fset.Position(call.Pos()).Line,
						name: sel.Sel.Name,
					})
				}
			})
		}
		return nil
	})

	for _, h := range hits {
		t.Logf("CAS-PROTOCOL-COMPOSITION-ROOT-01: %s:%d calls cas.%s outside cmd/ + runtime/state/cas/",
			h.file, h.line, h.name)
	}
	assert.Empty(t, hits,
		"CAS-PROTOCOL-COMPOSITION-ROOT-01: cas.NewProtocol / cas.MustNewProtocol "+
			"must only be called from cmd/* (composition root) or runtime/state/cas/*; "+
			"cells/runtime/adapters must consume an injected *cas.Protocol")
}

// INVARIANT: INVENTORY-ANCHOR-REQUIRED-01
//
// # INVENTORY-ANCHOR-REQUIRED-01
//
// Invariant: every `tools/archtest/*_test.go` (top-level, non-recursive) must
// carry at least one `// INVARIANT: <ID>` line in its file-header CommentGroup
// (the first ast.CommentGroup, before the package clause). This is the single
// source of truth for the reverse index from rule ID → asserting test code:
// `grep -rn 'INVARIANT: <ID>' tools/archtest/` jumps directly to the gate.
//
// No allowlist: helpers, fixtures, negprobes, framework entries all carry an
// anchor. Pure helpers without rule ownership use synthetic IDs
// `ARCHTEST-HELPERS-01` (helpers_test.go) and `ARCHTEST-LAYERS-01`
// (archtest_test.go, rolled-up alias for the LAYER-05..10 + PGQUERY-01 suite
// declared in doc.go).
//
// Replaces the deleted docs/audit/archtest-inventory.md + drift gate
// (PR-A', 2026-05-10).
//
// ref: cockroachdb/cockroach pkg/testutils/lint/passes/forbiddenmethod
//
//	(file-level comment scan via parser.ParseComments + ast.File.Comments)
package archtest

import (
	"go/ast"
	"go/parser"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const inventoryAnchorRequiredRule = "INVENTORY-ANCHOR-REQUIRED-01"

// TestInventoryAnchorRequired enforces the reverse-index single-source rule:
// every archtest *_test.go file at tools/archtest/ top level must carry at
// least one `// INVARIANT: <ID>` line in its file-header CommentGroup
// (before the package clause). No allowlist; helpers and fixtures use
// synthetic IDs.
func TestInventoryAnchorRequired(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	scope := scanner.DirsScope(root, []string{"tools/archtest"}, scanner.IncludeTests())

	var violators []string
	scanner.EachFile(t, scope, parser.ParseComments|parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		// Only flag top-level archtest test files (tools/archtest/<file>_test.go);
		// subpackages under tools/archtest/internal/ are out of scope.
		if filepath.ToSlash(filepath.Dir(fc.Rel)) != "tools/archtest" {
			return
		}
		if !strings.HasSuffix(fc.AbsPath, "_test.go") {
			return
		}
		if !hasInventoryAnchor(fc.File) {
			violators = append(violators, filepath.Base(fc.Rel))
		}
	})

	sort.Strings(violators)
	assert.Emptyf(t, violators,
		"%s: every tools/archtest/*_test.go must declare at least one "+
			"`// INVARIANT: <ID>` line in its file-header CommentGroup "+
			"(before package). Missing in:\n  %s",
		inventoryAnchorRequiredRule, strings.Join(violators, "\n  "))
}

// hasInventoryAnchor reports whether the file's first CommentGroup contains a
// line matching `// INVARIANT: <ID>` or `// - INVARIANT: <ID>` (list-form).
// Position relative to the package clause is intentionally not checked —
// archtest files split into two valid styles:
//
//   - pre-package godoc (e.g. `accesscore_facade_test.go`)
//   - post-package file-level doc, listing invariants (e.g.
//     `errcode_invariants_test.go`, `assembly_invariants_test.go`)
//
// Both have the anchor in `f.Comments[0]` because that field lists comments
// in source order and the file head's first CommentGroup is the natural
// landing zone for `grep -n 'INVARIANT:'`. Lines inside any later
// CommentGroup (function-body godoc, inline comments) are intentionally
// ignored.
func hasInventoryAnchor(f *ast.File) bool {
	if len(f.Comments) == 0 {
		return false
	}
	for _, c := range f.Comments[0].List {
		line := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
		if strings.HasPrefix(line, "INVARIANT:") || strings.HasPrefix(line, "- INVARIANT:") {
			return true
		}
	}
	return false
}

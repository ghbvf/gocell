// invariants asserted in this file:
//   - INVARIANT: INVENTORY-ANCHOR-REQUIRED-01
//   - INVARIANT: INVENTORY-ANCHOR-VALID-ID-01
//
// # INVENTORY-ANCHOR-REQUIRED-01
//
// Every `tools/archtest/*_test.go` (top-level, non-recursive) must carry at
// least one valid `// INVARIANT: <ID>` line in its file-header CommentGroup
// (the first `*ast.CommentGroup` parsed by `parser.ParseComments`, regardless
// of position relative to the package clause). This is the single source of
// the reverse index from rule ID → asserting test code:
// `grep -rn 'INVARIANT: <ID>' tools/archtest/` jumps directly to the gate.
//
// No allowlist: helpers, fixtures, negprobes, framework entries all carry an
// anchor. Pure helpers without rule ownership use the synthetic ID
// `ARCHTEST-HELPERS-01` (helpers_test.go).
//
// # INVENTORY-ANCHOR-VALID-ID-01
//
// Every `// INVARIANT: <ID>` and `// - INVARIANT: <ID>` line in *any*
// CommentGroup of every `tools/archtest/*_test.go` must declare an ID
// matching the canonical grammar:
//
//	^[A-Z][A-Z0-9]+(-[A-Z0-9]+)*-[0-9]+([A-Za-z]|-[A-Z0-9]+)?$
//
// Covering: bare `LAYER-05`, sub-suffixed `LAYER-05T`, lowercase index
// `KERNEL-POOLSTATS-LOCATION-01a`, uppercase sub-id `RMQ-CHANNEL-MAX-PER-CONN-01-A`,
// compound prefixes `CONTRACT-CONSISTENCY-EMIT-01`, etc. Anchors that fail to
// match (typo, truncation, non-canonical shape) fail this gate so the rule-ID
// space stays grep-able and never silently drifts.
//
// Together these two invariants make the source-code anchors the canonical
// grammar source — `scripts/audit/list-archtests.sh` is a raw-output audit
// view and does not re-implement the grammar.
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
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const (
	inventoryAnchorRequiredRule = "INVENTORY-ANCHOR-REQUIRED-01"
	inventoryAnchorValidIDRule  = "INVENTORY-ANCHOR-VALID-ID-01"
)

// inventoryAnchorIDPattern is the canonical grammar for INVARIANT anchor IDs.
// See INVENTORY-ANCHOR-VALID-ID-01 in the file-header godoc above for the
// shapes accepted (bare numeric, single trailing letter, uppercase sub-id).
//
// IMPORTANT: this is the only place in the repository that defines the
// anchor-ID grammar. `scripts/audit/list-archtests.sh` deliberately does NOT
// re-implement parsing; it grep-prints raw `// INVARIANT: …` lines so there
// is no second grammar to drift.
var inventoryAnchorIDPattern = regexp.MustCompile(
	`^[A-Z][A-Z0-9]+(-[A-Z0-9]+)*-[0-9]+([A-Za-z]|-[A-Z0-9]+)?$`,
)

// anchorRef is one parsed `// INVARIANT: <ID>` (or `// - INVARIANT: <ID>`)
// occurrence. Line number is recovered separately via `fc.Fset.Position`
// because the AST comment carries position via its parent group, not the
// individual `*ast.Comment`.
type anchorRef struct {
	id string
}

// TestInventoryAnchorRequired enforces the reverse-index single-source rule:
// every archtest *_test.go file at tools/archtest/ top level must carry at
// least one valid INVARIANT anchor in its file-header CommentGroup.
func TestInventoryAnchorRequired(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	scope := archtestScope(t, root)

	var violators []string
	scanner.EachFile(t, scope, parser.ParseComments|parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		if !isTopLevelArchtestTestFile(fc) {
			return
		}
		if !hasValidInventoryAnchorInHeader(fc.File) {
			violators = append(violators, filepath.Base(fc.Rel))
		}
	})

	sort.Strings(violators)
	assert.Emptyf(t, violators,
		"%s: every tools/archtest/*_test.go must declare at least one valid "+
			"`// INVARIANT: <ID>` line in its file-header CommentGroup "+
			"(the first ast.CommentGroup, regardless of position relative to "+
			"the package clause). Missing or malformed in:\n  %s",
		inventoryAnchorRequiredRule, strings.Join(violators, "\n  "))
}

// TestInventoryAnchorValidID enforces canonical grammar across all anchors —
// not just the file-header CommentGroup, but every `// INVARIANT: <ID>` and
// `// - INVARIANT: <ID>` line anywhere in the file. This catches malformed
// IDs (typos, truncation, non-canonical shapes like dropping a trailing
// uppercase suffix) that `INVENTORY-ANCHOR-REQUIRED-01` alone would miss
// when there is at least one valid anchor in the header.
func TestInventoryAnchorValidID(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	scope := archtestScope(t, root)

	type violation struct {
		file  string
		line  int
		token string
	}
	var violations []violation
	scanner.EachFile(t, scope, parser.ParseComments|parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		if !isTopLevelArchtestTestFile(fc) {
			return
		}
		for _, group := range fc.File.Comments {
			for _, c := range group.List {
				ref, ok := parseInventoryAnchor(c.Text)
				if !ok {
					continue
				}
				if !inventoryAnchorIDPattern.MatchString(ref.id) {
					line := fc.Fset.Position(c.Pos()).Line
					violations = append(violations, violation{
						file:  filepath.Base(fc.Rel),
						line:  line,
						token: ref.id,
					})
				}
			}
		}
	})

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].file != violations[j].file {
			return violations[i].file < violations[j].file
		}
		return violations[i].line < violations[j].line
	})

	if len(violations) == 0 {
		return
	}
	lines := make([]string, len(violations))
	for i, v := range violations {
		lines[i] = v.file + ":" + intToStr(v.line) + ": " + v.token
	}
	assert.Failf(t, "non-canonical INVARIANT anchor",
		"%s: every `// INVARIANT: <ID>` line must match grammar %s. "+
			"Offending tokens:\n  %s",
		inventoryAnchorValidIDRule, inventoryAnchorIDPattern, strings.Join(lines, "\n  "))
}

// isTopLevelArchtestTestFile reports whether fc points at a tools/archtest/<name>_test.go
// file directly (subpackages under tools/archtest/internal/ are out of scope).
func isTopLevelArchtestTestFile(fc scanner.FileContext) bool {
	if filepath.ToSlash(filepath.Dir(fc.Rel)) != "tools/archtest" {
		return false
	}
	return strings.HasSuffix(fc.AbsPath, "_test.go")
}

// archtestScope returns a Scope over tools/archtest/ that **matches the
// `scripts/audit/list-archtests.sh` discovery model exactly** — only files
// tracked by git are considered. This eliminates the gate / audit-script
// asymmetry where an untracked local `*_test.go` would fail the gate but
// be invisible to the audit listing (and vice-versa for an `index`-removed
// file still on disk).
//
// The predicate runs a single `git ls-files -- tools/archtest/` invocation,
// builds a string set of slash-paths, and feeds it to `MatchRels`. Local
// editor swap files (`.foo_test.go.swp`) and other untracked artifacts are
// silently skipped, matching the script's behavior.
func archtestScope(t *testing.T, root string) scanner.Scope {
	t.Helper()
	tracked := loadGitTrackedSet(t, root)
	return scanner.DirsScope(root, []string{"tools/archtest"},
		scanner.IncludeTests(),
		scanner.MatchRels(func(rel string) bool {
			return tracked[filepath.ToSlash(rel)]
		}),
	)
}

// loadGitTrackedSet runs `git ls-files -- tools/archtest/` and returns the
// set of slash-form module-relative paths under tracking. The single shell
// call is acceptable in test scope: CI and dev environments both have git;
// archtest itself is a git-managed test corpus.
func loadGitTrackedSet(t *testing.T, root string) map[string]bool {
	t.Helper()
	// G204 false-positive: `root` is the module root resolved by
	// findModuleRoot from go.mod, not user input. Same exception pattern
	// as lintgate_smoke_test.go's golangci-lint invocation.
	cmd := exec.Command("git", "-C", root, "ls-files", "--", "tools/archtest/") //nolint:gosec // G204 const args + go.mod-derived root
	out, err := cmd.Output()
	require.NoErrorf(t, err, "%s: git ls-files", inventoryAnchorRequiredRule)
	set := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		set[line] = true
	}
	return set
}

// hasValidInventoryAnchorInHeader returns true if the file's first
// CommentGroup contains at least one INVARIANT anchor whose ID matches the
// canonical grammar.
func hasValidInventoryAnchorInHeader(f *ast.File) bool {
	if len(f.Comments) == 0 {
		return false
	}
	for _, c := range f.Comments[0].List {
		ref, ok := parseInventoryAnchor(c.Text)
		if !ok {
			continue
		}
		if inventoryAnchorIDPattern.MatchString(ref.id) {
			return true
		}
	}
	return false
}

// parseInventoryAnchor extracts the ID token from a `// INVARIANT: <ID>` or
// `// - INVARIANT: <ID>` comment line. Returns (anchorRef, true) when the
// line carries an INVARIANT marker; the ID token is the first whitespace- or
// comma-delimited word after `INVARIANT:`, with a trailing colon stripped
// (e.g. `// - INVARIANT: MESSAGE-CONST-LITERAL-01: fixture` yields
// `MESSAGE-CONST-LITERAL-01`).
//
// `(anchorRef{}, false)` for non-anchor comment lines.
func parseInventoryAnchor(commentText string) (anchorRef, bool) {
	line := strings.TrimSpace(strings.TrimPrefix(commentText, "//"))
	const (
		plainPrefix = "INVARIANT:"
		listPrefix  = "- INVARIANT:"
	)
	var payload string
	switch {
	case strings.HasPrefix(line, listPrefix):
		payload = strings.TrimSpace(strings.TrimPrefix(line, listPrefix))
	case strings.HasPrefix(line, plainPrefix):
		payload = strings.TrimSpace(strings.TrimPrefix(line, plainPrefix))
	default:
		return anchorRef{}, false
	}
	tok := payload
	if i := strings.IndexAny(tok, " ,"); i >= 0 {
		tok = tok[:i]
	}
	tok = strings.TrimSuffix(tok, ":")
	return anchorRef{id: tok}, true
}

// intToStr is a tiny helper for diagnostic line numbers; avoids importing fmt
// for a single Sprintf("%d") call.
func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	negative := n < 0
	if negative {
		n = -n
	}
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

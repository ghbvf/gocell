// INVARIANT: SLOWGATE-ALLOWLIST-01
//
// SLOWGATE-ALLOWLIST-01 — drift guard for tools/slowgate/allowlist.txt.
//
// Invariant: every (Package, TestName) entry in tools/slowgate/allowlist.txt
// must (a) correspond to a real top-level `func TestXxx` in the named package,
// and (b) be preceded by a `# <reason>` comment line in the allowlist file
// (non-empty after `#`, immediately on the line above the data line).
//
// Why a comment-line guard, not a sleep-annotation guard:
// Empirical scan of GoCell unit shards shows the vast majority of >2s tests
// are slow due to type-graph loading (archtest / typeseval / generatedverify
// run packages.Load over the entire module), subprocess go-toolchain tests
// (kernel/verify TestRunGoTest_*), or large-fixture verify jobs (cmd/gocell
// app.TestRunVerifySlice_ValidID etc.). Tying the runtime budget gate to
// the static `//archtest:allow:test-sleep` annotation would force inserting
// fake sleeps into these tests for no engineering reason. The comment-line
// guard preserves the two real invariants (no orphan entries, every entry
// must justify itself in writing) without that distortion.
//
// Companion gates:
//   - TEST-SLEEP-DISCIPLINE-01 (this directory) enforces the sleep
//     annotation on every time.Sleep in test code. Independent of slowgate.
//   - PR-V11-SLOW-TEST-BUDGET wires `tools/slowgate` into CI to fail any
//     test whose Elapsed exceeds the threshold unless allowlisted.
//
// ref: docs/plans/202605011500-029-master-roadmap.md G9
package archtest

import (
	"bufio"
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// slowgateAllowlistPath is module-relative.
const slowgateAllowlistPath = "tools/slowgate/allowlist.txt"

// TestSlowgateAllowlist enforces SLOWGATE-ALLOWLIST-01.
//
// ref: docs/plans/202605011500-029-master-roadmap.md G9
func TestSlowgateAllowlist(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	entries, err := loadSlowgateAllowlist(filepath.Join(root, slowgateAllowlistPath))
	require.NoError(t, err, "load allowlist")

	if len(entries) == 0 {
		// Empty allowlist is legal (e.g. early development); nothing to verify.
		return
	}

	// Group entries by package so we only invoke packages.Load once per pkg.
	byPkg := map[string][]string{}
	for _, e := range entries {
		byPkg[e.Package] = append(byPkg[e.Package], e.Test)
	}

	patterns := make([]string, 0, len(byPkg))
	for p := range byPkg {
		patterns = append(patterns, p)
	}
	sort.Strings(patterns)

	// We reuse testTimeLiteralBuildTags (the same build-tag set used by
	// TEST-TIME-LITERAL-01) because the slowgate allowlist contains
	// integration- and pg-tagged tests (e.g. kernel/verify integration
	// tests that exec subprocess go-toolchain) that would otherwise be
	// invisible to packages.Load and falsely flagged as "orphan entries".
	// Any new build tag introduced repo-wide must be added there; the two
	// gates inherit the same scope by construction.
	pkgs, errs, err := typeseval.LoadPackages(root, true, testTimeLiteralBuildTags, patterns...)
	require.NoError(t, err, "packages.Load failed")
	require.Empty(t, errs, "package load errors must fail-closed: %v", errs)

	loaded := map[string]*packages.Package{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		loaded[p.PkgPath] = p
	})

	var violations []string

	for _, pkgPath := range patterns {
		p, ok := loaded[pkgPath]
		if !ok {
			violations = append(violations, fmt.Sprintf(
				"%s: package %q not found by packages.Load (orphan allowlist entry?)",
				slowgateAllowlistPath, pkgPath,
			))
			continue
		}

		funcs := map[string]bool{}
		for _, file := range p.Syntax {
			for _, decl := range file.Decls {
				switch fn := decl.(type) {
				case *ast.FuncDecl:
					if fn.Recv != nil {
						continue
					}
					if strings.HasPrefix(fn.Name.Name, "Test") {
						funcs[fn.Name.Name] = true
					}
				}
			}
		}

		for _, testName := range byPkg[pkgPath] {
			if !funcs[testName] {
				violations = append(violations, fmt.Sprintf(
					"%s: %s.%s — no top-level func with that name found (test renamed or deleted?)",
					slowgateAllowlistPath, pkgPath, testName,
				))
			}
		}
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"SLOWGATE-ALLOWLIST-01: every entry in "+slowgateAllowlistPath+
			" must point to a real top-level test func. "+
			"Remove stale entries when tests are renamed or deleted. "+
			"ref: docs/plans/202605011500-029-master-roadmap.md G9")
}

// slowgateEntry represents a single (Package, Test) allowlist line.
type slowgateEntry struct {
	Package string
	Test    string
}

// loadSlowgateAllowlist parses the line-based allowlist file and additionally
// enforces the "preceding `# <reason>` comment" rule. Format:
//   - blank lines and lines starting with `#` are comments (allowed anywhere)
//   - data lines are `Package<TAB>Test` (TAB or any whitespace)
//   - every data line MUST have a non-empty `# <reason>` comment line as its
//     last preceding non-blank line; any number of blank lines between the
//     comment and the data line is fine, but no other content (including
//     section dividers like a bare `#` with empty body) may intervene
//
// The function returns the parsed entries OR an error describing the first
// violation encountered (orphan data line, malformed line, etc).
func loadSlowgateAllowlist(path string) ([]slowgateEntry, error) {
	f, err := os.Open(path) //nolint:gosec // path is module-relative const joined to findModuleRoot
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var entries []slowgateEntry
	sc := bufio.NewScanner(f)
	lineNum := 0
	var lastNonBlank string
	var lastNonBlankWasComment bool

	for sc.Scan() {
		lineNum++
		raw := strings.TrimRight(sc.Text(), "\r")
		// Do not TrimSpace here: a `pkg<TAB>` data line (empty test name)
		// must reach splitAllowlistFields with its TAB intact so the empty
		// trailing field is diagnosed as "empty test name" rather than
		// collapsing into a single-field "expected 2 fields" error.
		// Mirrors tools/slowgate/main.go parseAllowlist.
		leftTrimmed := strings.TrimLeft(raw, " \t")
		if leftTrimmed == "" {
			// Blank line: keep lastNonBlank* state so a comment can be
			// visually separated from its data line by blank lines.
			continue
		}
		if strings.HasPrefix(leftTrimmed, "#") {
			reason := strings.TrimSpace(strings.TrimPrefix(leftTrimmed, "#"))
			lastNonBlank = leftTrimmed
			lastNonBlankWasComment = reason != ""
			continue
		}

		// Data line.
		if !lastNonBlankWasComment {
			return nil, fmt.Errorf(
				"%s:%d: data line %q is not preceded by a `# <reason>` comment "+
					"(every allowlist entry must justify itself)",
				path, lineNum, leftTrimmed,
			)
		}
		_ = lastNonBlank // retained for diagnostics-friendly future use
		fields := splitAllowlistFields(raw)
		if len(fields) != 2 {
			return nil, fmt.Errorf(
				"%s:%d: expected 2 fields (Package<TAB>Test), got %d (%q)",
				path, lineNum, len(fields), raw,
			)
		}
		if fields[0] == "" || fields[1] == "" {
			return nil, fmt.Errorf("%s:%d: empty package or empty test name (%q)", path, lineNum, raw)
		}
		entries = append(entries, slowgateEntry{Package: fields[0], Test: fields[1]})

		// After consuming a data line, reset comment state so the NEXT data
		// line must have its own preceding comment.
		lastNonBlankWasComment = false
		lastNonBlank = leftTrimmed
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// splitAllowlistFields mirrors tools/slowgate/main.go's splitAllowlistLine
// byte-for-byte. The two parsers cannot share an import (tools/slowgate is
// package main and not importable as a library, by design — flat single-
// binary layout). Behavioral parity is preserved by: (a) identical token
// splitting (TAB-preferring with whitespace fallback), and (b) the
// "preserve all positional fields" policy in the TAB branch so that
// `pkg<TAB>` (empty test) is diagnosed by the field-count check rather
// than collapsing into a single-field error. Any change to either side
// must be mirrored here.
func splitAllowlistFields(s string) []string {
	if strings.Contains(s, "\t") {
		parts := strings.Split(s, "\t")
		out := make([]string, len(parts))
		for i, p := range parts {
			out[i] = strings.TrimSpace(p)
		}
		return out
	}
	return strings.Fields(s)
}

// TestLoadSlowgateAllowlist_ParseRules covers loadSlowgateAllowlist's
// parser invariants in isolation from the live tools/slowgate/allowlist.txt
// file, so changes to the comment-precedence rule can be exercised without
// touching the production allowlist. Each case writes a tiny synthetic
// allowlist into t.TempDir() and asserts the expected (entries, error).
//
// ref: docs/plans/202605011500-029-master-roadmap.md G9
func TestLoadSlowgateAllowlist_ParseRules(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		content     string
		wantEntries int
		wantErr     string // substring; "" means expect no error
	}{
		{
			name:        "happy_path_minimal",
			content:     "# reason\npkg/a\tTestA\n",
			wantEntries: 1,
		},
		{
			name:        "blank_line_between_comment_and_data_ok",
			content:     "# reason\n\npkg/a\tTestA\n",
			wantEntries: 1,
		},
		{
			name:        "many_blank_lines_between_comment_and_data_ok",
			content:     "# reason\n\n\n\n\npkg/a\tTestA\n",
			wantEntries: 1,
		},
		{
			name:    "data_without_preceding_comment_rejected",
			content: "pkg/a\tTestA\n",
			wantErr: "not preceded by",
		},
		{
			name:    "bare_hash_does_not_count_as_reason",
			content: "#\npkg/a\tTestA\n",
			wantErr: "not preceded by",
		},
		{
			name:    "section_header_then_bare_hash_then_data_rejected",
			content: "# section title\n#\npkg/a\tTestA\n",
			wantErr: "not preceded by",
		},
		{
			name:    "two_consecutive_data_lines_second_rejected",
			content: "# reason\npkg/a\tTestA\npkg/b\tTestB\n",
			wantErr: "not preceded by",
		},
		{
			name:        "tab_and_whitespace_mix_ok",
			content:     "# reason\npkg/a\t  TestA \n",
			wantEntries: 1,
		},
		{
			name:        "crlf_line_endings_ok",
			content:     "# reason\r\npkg/a\tTestA\r\n",
			wantEntries: 1,
		},
		{
			name:    "single_field_rejected",
			content: "# reason\npkgonly\n",
			wantErr: "expected 2 fields",
		},
		{
			name:    "three_fields_rejected",
			content: "# reason\npkg/a\tTestA\textra\n",
			wantErr: "expected 2 fields",
		},
		{
			name:    "empty_test_name_rejected",
			content: "# reason\npkg/a\t\n",
			wantErr: "empty package or empty test name",
		},
		{
			name:    "empty_package_rejected",
			content: "# reason\n\tTestA\n",
			wantErr: "empty package or empty test name",
		},
		{
			name:        "empty_file_returns_no_entries",
			content:     "",
			wantEntries: 0,
		},
		{
			name:        "comment_only_file_returns_no_entries",
			content:     "# header\n# more\n",
			wantEntries: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "allowlist.txt")
			require.NoError(t, os.WriteFile(path, []byte(tc.content), 0o600))

			entries, err := loadSlowgateAllowlist(path)
			if tc.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErr,
					"err %q must contain %q", err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Len(t, entries, tc.wantEntries)
		})
	}
}

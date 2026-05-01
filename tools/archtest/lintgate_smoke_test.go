// LINT-GATE-SMOKE-01 — behavior-level proof that .golangci.yml's depguard /
// forbidigo / revive / goimports gates actually fire on the violations they
// claim to forbid, and that the cmd/+examples/ exemption does NOT fire on
// legitimate CLI/demo `fmt.Print*` calls.
//
// Background: G1+G2 (PR #347) moved LAYER-01..04 + dot-import + observability
// rules out of executable archtest tests into YAML config. Without a
// behavior-level fixture, a config drift (glob anchor mistake / message-format
// shift in upstream forbidigo / a typo in an allow list) silently turns rules
// off. The same PR already needed a late glob correction (commit d8fdf5a0)
// that no test would have caught — only manual full-repo lint.
//
// This gate runs the project's real .golangci.yml against synthetic temp
// modules, one per rule family, and asserts each diagnostic is produced by
// the expected linter at the expected file. The cmd/+examples/ exemption is
// asserted by NEGATIVE test (presence of `fmt.Println` in cmd/foo.go must
// produce ZERO forbidigo findings).
//
// ref: docs/plans/202605011500-029-master-roadmap.md G2 (post-merge follow-up)
package archtest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lintGateCase models one fixture: a synthetic module's file tree plus the
// expected (linter, file) tuples that golangci-lint should report.
//
// expectFindings declares the diagnostics that MUST appear. Each entry is
// matched by `(linter, filename)`; the test ignores other fields so wording
// shifts in upstream linters do not flake the test.
//
// expectNoFindings declares (linter, filename) tuples that MUST NOT appear —
// the negative case for cmd/+examples/ exemption.
type lintGateCase struct {
	name             string
	files            map[string]string
	expectFindings   []lintGateFinding
	expectNoFindings []lintGateFinding
}

type lintGateFinding struct {
	linter string
	file   string
}

// golangciIssue is the subset of golangci-lint v2's JSON output schema that
// this test depends on. Other fields (severity, replacement, etc.) are
// ignored.
type golangciIssue struct {
	FromLinter string `json:"FromLinter"`
	Pos        struct {
		Filename string `json:"Filename"`
		Line     int    `json:"Line"`
	} `json:"Pos"`
	Text string `json:"Text"`
}

type golangciReport struct {
	Issues []golangciIssue `json:"Issues"`
}

// TestLintGateSmoke is the driver: per case, write fixture files into
// t.TempDir(), copy the project .golangci.yml, run golangci-lint, parse JSON
// output, and assert the expected positive/negative findings.
func TestLintGateSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping lint-gate smoke test in -short mode (spawns golangci-lint, ~5-15s per case)")
	}
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		t.Skip("golangci-lint not in PATH; smoke test skipped (CI installs it)")
	}

	root := findModuleRoot(t)
	configSrc := filepath.Join(root, ".golangci.yml")

	cases := buildLintGateCases()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()

			// Synthetic module declares the same module path as the real repo
			// so depguard's `pkg: github.com/ghbvf/gocell/...` allow-list
			// entries match imports in the fixture.
			writeFixtureFile(t, tmp, "go.mod",
				"module github.com/ghbvf/gocell\n\ngo 1.22\n")
			for path, content := range tc.files {
				writeFixtureFile(t, tmp, path, content)
			}
			copyConfig(t, configSrc, filepath.Join(tmp, ".golangci.yml"))

			issues := runGolangciLint(t, tmp)

			for _, want := range tc.expectFindings {
				assertFindingPresent(t, issues, want, tc.name)
			}
			for _, unwant := range tc.expectNoFindings {
				assertFindingAbsent(t, issues, unwant, tc.name)
			}
		})
	}
}

// buildLintGateCases enumerates the 11 fixtures covering each migrated rule
// family plus the cmd/+examples/ negative case for forbidigo's text-coupled
// exemption (P2 finding from PR #347 review — drift would surface here).
func buildLintGateCases() []lintGateCase {
	stub := func(pkg string) string { return "package " + pkg + "\n" }

	return []lintGateCase{
		{
			name: "depguard_kernel_imports_runtime",
			files: map[string]string{
				"kernel/foo.go":   "package kernel\n\nimport _ \"github.com/ghbvf/gocell/runtime\"\n",
				"runtime/stub.go": stub("runtime"),
			},
			expectFindings: []lintGateFinding{{linter: "depguard", file: "kernel/foo.go"}},
		},
		{
			name: "depguard_kernel_imports_tools",
			files: map[string]string{
				"kernel/foo.go": "package kernel\n\nimport _ \"github.com/ghbvf/gocell/tools/archtest\"\n",
				"tools/archtest/stub.go": stub("archtest"),
			},
			expectFindings: []lintGateFinding{{linter: "depguard", file: "kernel/foo.go"}},
		},
		{
			name: "depguard_pkg_imports_runtime",
			files: map[string]string{
				"pkg/util/foo.go": "package util\n\nimport _ \"github.com/ghbvf/gocell/runtime\"\n",
				"runtime/stub.go": stub("runtime"),
			},
			expectFindings: []lintGateFinding{{linter: "depguard", file: "pkg/util/foo.go"}},
		},
		{
			name: "depguard_cells_imports_adapters",
			files: map[string]string{
				"cells/foo/c.go":   "package foo\n\nimport _ \"github.com/ghbvf/gocell/adapters\"\n",
				"adapters/stub.go": stub("adapters"),
			},
			expectFindings: []lintGateFinding{{linter: "depguard", file: "cells/foo/c.go"}},
		},
		{
			name: "depguard_runtime_imports_cells",
			files: map[string]string{
				"runtime/r.go":    "package runtime\n\nimport _ \"github.com/ghbvf/gocell/cells\"\n",
				"cells/stub.go":   stub("cells"),
			},
			expectFindings: []lintGateFinding{{linter: "depguard", file: "runtime/r.go"}},
		},
		{
			name: "depguard_adapters_imports_cells",
			files: map[string]string{
				"adapters/a.go": "package adapters\n\nimport _ \"github.com/ghbvf/gocell/cells\"\n",
				"cells/stub.go": stub("cells"),
			},
			expectFindings: []lintGateFinding{{linter: "depguard", file: "adapters/a.go"}},
		},
		{
			name: "forbidigo_log_printf_in_runtime",
			files: map[string]string{
				"runtime/r.go": "package runtime\n\nimport \"log\"\n\nfunc F() { log.Printf(\"oops\") }\n",
			},
			expectFindings: []lintGateFinding{{linter: "forbidigo", file: "runtime/r.go"}},
		},
		{
			name: "forbidigo_fmt_println_in_runtime",
			files: map[string]string{
				"runtime/r.go": "package runtime\n\nimport \"fmt\"\n\nfunc F() { fmt.Println(\"oops\") }\n",
			},
			expectFindings: []lintGateFinding{{linter: "forbidigo", file: "runtime/r.go"}},
		},
		{
			// Negative: cmd/ exemption MUST hold — `fmt.Println` in cmd/ is
			// legitimate stdout. If forbidigo's message format changes upstream
			// and the text-coupled `exclusions.rules.text` regex stops matching,
			// the issue would resurface here and this case would FAIL.
			name: "forbidigo_fmt_println_in_cmd_is_exempt",
			files: map[string]string{
				"cmd/foo/main.go": "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hello\") }\n",
			},
			expectNoFindings: []lintGateFinding{{linter: "forbidigo", file: "cmd/foo/main.go"}},
		},
		{
			name: "forbidigo_fmt_println_in_examples_is_exempt",
			files: map[string]string{
				"examples/foo/main.go": "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"demo\") }\n",
			},
			expectNoFindings: []lintGateFinding{{linter: "forbidigo", file: "examples/foo/main.go"}},
		},
		{
			name: "revive_dot_import_in_runtime",
			files: map[string]string{
				"runtime/r.go": "package runtime\n\nimport . \"strings\"\n\nvar _ = ToUpper(\"x\")\n",
			},
			expectFindings: []lintGateFinding{{linter: "revive", file: "runtime/r.go"}},
		},
	}
}

func writeFixtureFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	require.NoError(t, os.WriteFile(abs, []byte(content), 0o600))
}

func copyConfig(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dst, data, 0o600))
}

// runGolangciLint invokes the binary in cwd=workDir, capturing the JSON
// report. golangci-lint exits non-zero when issues are present, so we do not
// fail-fast on exit code; we trust the JSON for assertions.
func runGolangciLint(t *testing.T, workDir string) []golangciIssue {
	t.Helper()
	out := filepath.Join(workDir, "lint-out.json")
	cmd := exec.Command("golangci-lint", "run",
		"--output.json.path", out,
		"--output.text.path", "/dev/null",
		"./...",
	)
	cmd.Dir = workDir
	combined, _ := cmd.CombinedOutput()

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read JSON report failed: %v\ngolangci-lint stdout/stderr:\n%s", err, combined)
	}
	var report golangciReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("parse JSON report failed: %v\nraw:\n%s", err, data)
	}
	return report.Issues
}

func assertFindingPresent(t *testing.T, issues []golangciIssue, want lintGateFinding, caseName string) {
	t.Helper()
	for _, iss := range issues {
		if iss.FromLinter == want.linter && strings.HasSuffix(filepath.ToSlash(iss.Pos.Filename), want.file) {
			return
		}
	}
	t.Errorf("[%s] expected (%s @ %s) not present in issues:\n%s",
		caseName, want.linter, want.file, formatIssues(issues))
}

func assertFindingAbsent(t *testing.T, issues []golangciIssue, unwanted lintGateFinding, caseName string) {
	t.Helper()
	for _, iss := range issues {
		if iss.FromLinter == unwanted.linter && strings.HasSuffix(filepath.ToSlash(iss.Pos.Filename), unwanted.file) {
			t.Errorf("[%s] unexpected (%s @ %s) present:\n  %s",
				caseName, unwanted.linter, unwanted.file, iss.Text)
			return
		}
	}
	// Not found = pass for negative case.
	assert.True(t, true, "negative case held")
}

func formatIssues(issues []golangciIssue) string {
	if len(issues) == 0 {
		return "  (no issues reported)"
	}
	var b strings.Builder
	for _, iss := range issues {
		b.WriteString("  ")
		b.WriteString(iss.FromLinter)
		b.WriteString(" @ ")
		b.WriteString(filepath.ToSlash(iss.Pos.Filename))
		b.WriteString(": ")
		b.WriteString(iss.Text)
		b.WriteString("\n")
	}
	return b.String()
}

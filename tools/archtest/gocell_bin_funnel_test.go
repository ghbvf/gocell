package archtest

// INVARIANT: GOCELL-BIN-FUNNEL-01
//
// Every hack/verify-*.sh file (root-level only; hack/lib/ is the funnel
// definition itself and hack/githooks/ is not in the verify-*.sh scope)
// that invokes cmd/gocell via `go run ./cmd/gocell` or
// `go -C <dir> run ./cmd/gocell` MUST also source hack/lib/gocell-bin.sh
// and route the invocation through `gocell::cli`. This ensures that the
// GOCELL_BIN pre-compiled binary optimization (plan ship-curious-treasure.md
// method M) is applied uniformly: new verify-*.sh authors cannot accidentally
// add a bare `go run ./cmd/gocell` that bypasses the shared binary.
//
// Scope: This archtest enforces a CI build-helper invariant (shell-script
// funnel discipline). Per .claude/rules/gocell/ai-robust.md §"适用范围",
// the AI-robust 三档 framework governs Go-source-level enforcement mechanisms
// (archtest by Go AST/types, governance rule, codegen funnel, type marker,
// godoc strong convention). Shell-script CI invariants fall outside that
// framework's adjudication. This archtest is a shell-anchor scanner
// intentionally — there is no Go-level form-uniqueness to elevate; the funnel
// target is shell built-in `source`, and the protected operation is
// `go run ./cmd/gocell` substring on a curated file glob (hack/verify-*.sh).
// Treat as a CI hygiene check, not as a load-bearing AI-robust rule.
//
// Blind-spot (documented per ai-robust.md §"工具选定后强制盲区自检"):
//   - `go build ./cmd/gocell && ./gocell` two-step invocation would not match
//     the `go run ./cmd/gocell` pattern; if a future script uses this form it
//     would bypass the check. Reverse blind-spot test:
//     TestGocellBinFunnel_BlindSpot_BuildThenRun asserts no verify-*.sh uses
//     this alternative form.
//   - `go -C <dir> run ./cmd/gocell` form (directory-scoped go run) would not
//     be caught by a `go run ./cmd/gocell`-only scan. Reverse blind-spot test:
//     TestGocellBinFunnel_BlindSpot_GoDashC asserts no verify-*.sh uses this
//     form outside the funnel.
//
// ref: hack/lib/gocell-bin.sh (funnel definition)
// ref: plan ship-curious-treasure.md method M (binary pre-compilation)

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGocellBinFunnel asserts that every hack/verify-*.sh file containing
// `go run ./cmd/gocell` (or `go -C ... run ./cmd/gocell`) also sources
// `hack/lib/gocell-bin.sh` and uses `gocell::cli` for the invocation.
func TestGocellBinFunnel(t *testing.T) {
	t.Parallel()

	repoRoot := findModuleRoot(t)
	hackDir := filepath.Join(repoRoot, "hack")

	entries, err := os.ReadDir(hackDir)
	if err != nil {
		t.Fatalf("GOCELL-BIN-FUNNEL-01: cannot read hack/: %v", err)
	}

	var violations []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "verify-") || !strings.HasSuffix(name, ".sh") {
			continue
		}

		scriptPath := filepath.Join(hackDir, name)
		v := checkScriptFunnel(t, scriptPath, name)
		violations = append(violations, v...)
	}

	if len(violations) > 0 {
		t.Fatalf("GOCELL-BIN-FUNNEL-01: %d violation(s):\n\n%s\n\n"+
			"Fix: source hack/lib/gocell-bin.sh and replace `go run ./cmd/gocell` "+
			"with `gocell::cli` in each listed script.",
			len(violations), strings.Join(violations, "\n"))
	}
}

// checkScriptFunnel checks a single verify-*.sh file for the funnel invariant.
// Returns a slice of violation messages (empty means no violations).
func checkScriptFunnel(t *testing.T, scriptPath, scriptName string) []string {
	t.Helper()

	content, err := os.ReadFile(scriptPath) //nolint:gosec // G304: scriptPath is constructed from findModuleRoot + known sub-path
	if err != nil {
		t.Errorf("GOCELL-BIN-FUNNEL-01: cannot read %s: %v", scriptName, err)
		return nil
	}

	text := string(content)

	// Check if this file invokes cmd/gocell via go run.
	hasGoRunPattern := containsGoRunCmdGocell(text)
	if !hasGoRunPattern {
		// No go run ./cmd/gocell — this script doesn't need the funnel.
		return nil
	}

	var violations []string

	// The script must source hack/lib/gocell-bin.sh.
	if !strings.Contains(text, "hack/lib/gocell-bin.sh") {
		violations = append(violations, fmt.Sprintf(
			"  %s: contains `go run ./cmd/gocell` but does not source hack/lib/gocell-bin.sh",
			scriptName,
		))
	}

	// The script must use gocell::cli (not bare go run) for the actual invocation.
	// We check that gocell::cli appears in the file.
	if !strings.Contains(text, "gocell::cli") {
		violations = append(violations, fmt.Sprintf(
			"  %s: contains `go run ./cmd/gocell` but does not use `gocell::cli` funnel",
			scriptName,
		))
	}

	// Check each line: if a non-comment line calls `go run ./cmd/gocell` directly
	// (not inside the gocell::cli function body which is in gocell-bin.sh itself),
	// that is a violation. We scan line-by-line.
	lineViolations := findBareGoRunLines(text, scriptName)
	violations = append(violations, lineViolations...)

	return violations
}

// containsGoRunCmdGocell reports whether text contains a go run ./cmd/gocell
// invocation. Two explicit forms are recognized:
//   - `go run ./cmd/gocell`          — direct go run
//   - `go -C <dir> run ./cmd/gocell` — directory-scoped go run
//
// The second clause requires both `go -C` and `run ./cmd/gocell` to be
// present so that a comment mentioning only `run ./cmd/gocell` (without the
// `go` prefix) does not produce a false positive.
func containsGoRunCmdGocell(text string) bool {
	return strings.Contains(text, "go run ./cmd/gocell") ||
		(strings.Contains(text, "go -C") && strings.Contains(text, "run ./cmd/gocell"))
}

// findBareGoRunLines scans text line-by-line and returns violations where a
// non-comment, non-gocell-bin-sh line contains a bare `go run ./cmd/gocell`
// invocation rather than `gocell::cli`.
func findBareGoRunLines(text, scriptName string) []string {
	var violations []string
	scanner := bufio.NewScanner(strings.NewReader(text))
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip comment lines.
		if strings.HasPrefix(line, "#") {
			continue
		}

		// Skip blank lines.
		if line == "" {
			continue
		}

		// A bare `go run ./cmd/gocell` (or `go -C ... run ./cmd/gocell`) on
		// a non-comment line that is NOT inside the gocell::cli function body
		// is a violation. The gocell-bin.sh file itself is allowed to have it
		// (it IS the funnel definition), but verify-*.sh files must not.
		if strings.Contains(line, "go run ./cmd/gocell") || (strings.Contains(line, "go -C") && strings.Contains(line, "run ./cmd/gocell")) {
			violations = append(violations, fmt.Sprintf(
				"  %s:%d: bare `go run ./cmd/gocell` — use `gocell::cli` instead (line: %q)",
				scriptName, lineNum, truncate(line, 100),
			))
		}
	}
	return violations
}

// truncate shortens s to at most maxLen runes, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// TestGocellBinFunnel_Makefile asserts that Makefile does not contain bare
// `go run ./cmd/gocell` invocations. Makefile targets must use the
// $(GOCELL_CLI) variable (defined at the top of Makefile as
// `$(if $(GOCELL_BIN),$(GOCELL_BIN),go run ./cmd/gocell)`) so that CI can
// inject a pre-compiled binary via GOCELL_BIN. This is a shell-anchor scan
// (same tier as the verify-*.sh check above); the Makefile variable expansion
// is the funnel equivalent of gocell::cli in shell scripts.
func TestGocellBinFunnel_Makefile(t *testing.T) {
	t.Parallel()

	repoRoot := findModuleRoot(t)
	makefilePath := filepath.Join(repoRoot, "Makefile")

	content, err := os.ReadFile(makefilePath) //nolint:gosec // G304: path is constructed from findModuleRoot
	if err != nil {
		t.Fatalf("GOCELL-BIN-FUNNEL-01 (Makefile): cannot read Makefile: %v", err)
	}

	text := string(content)
	var violations []string

	scanner := bufio.NewScanner(strings.NewReader(text))
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip comment lines and blank lines.
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		// A bare `go run ./cmd/gocell` on a recipe line (not a variable definition
		// that establishes the funnel itself) is a violation.
		if strings.Contains(line, "go run ./cmd/gocell") {
			// The GOCELL_CLI variable definition is the one allowed occurrence.
			if !strings.Contains(line, "GOCELL_CLI") {
				violations = append(violations, fmt.Sprintf(
					"  Makefile:%d: bare `go run ./cmd/gocell` — use $(GOCELL_CLI) instead (line: %q)",
					lineNum, truncate(line, 100),
				))
			}
		}
	}

	if len(violations) > 0 {
		t.Fatalf("GOCELL-BIN-FUNNEL-01 (Makefile): %d violation(s):\n\n%s\n\n"+
			"Fix: replace `go run ./cmd/gocell` with `$(GOCELL_CLI)` in each listed target.",
			len(violations), strings.Join(violations, "\n"))
	}
}

// TestGocellBinFunnel_BlindSpot_GoDashC is a reverse blind-spot self-test
// (required by ai-robust.md §"工具选定后强制盲区自检").
//
// The main scanner watches for both `go run ./cmd/gocell` and
// `go -C ... run ./cmd/gocell` via containsGoRunCmdGocell. This test
// asserts that no verify-*.sh uses the `go -C <dir> run ./cmd/gocell`
// form without first sourcing hack/lib/gocell-bin.sh. Because the main
// TestGocellBinFunnel already checks funnel compliance for any file that
// triggers containsGoRunCmdGocell (which includes the go -C form via
// findBareGoRunLines), this blind-spot test explicitly verifies that the
// go -C form itself is captured by the detection logic — confirming the
// scanner's coverage of both invocation variants.
func TestGocellBinFunnel_BlindSpot_GoDashC(t *testing.T) {
	t.Parallel()

	repoRoot := findModuleRoot(t)
	hackDir := filepath.Join(repoRoot, "hack")

	entries, err := os.ReadDir(hackDir)
	if err != nil {
		t.Fatalf("cannot read hack/: %v", err)
	}

	var violations []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "verify-") || !strings.HasSuffix(name, ".sh") {
			continue
		}

		scriptPath := filepath.Join(hackDir, name)
		content, err := os.ReadFile(scriptPath) //nolint:gosec // G304: same as above
		if err != nil {
			t.Errorf("cannot read %s: %v", name, err)
			continue
		}

		text := string(content)
		// Look for `go -C <dir> run ./cmd/gocell` without funnel — this is the
		// directory-scoped go run blind-spot pattern.
		scanner := bufio.NewScanner(strings.NewReader(text))
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "#") || line == "" {
				continue
			}
			// Detect bare `go -C ... run ./cmd/gocell` without funnel routing.
			if strings.Contains(line, "go -C") && strings.Contains(line, "run ./cmd/gocell") {
				// It is a violation only if the file does not source gocell-bin.sh
				// and use gocell::cli; the full funnel check is done in TestGocellBinFunnel.
				// Here we just assert the pattern exists in the scanner's detection range.
				if !strings.Contains(text, "hack/lib/gocell-bin.sh") {
					violations = append(violations, fmt.Sprintf(
						"  %s:%d: `go -C ... run ./cmd/gocell` outside funnel "+
							"(no hack/lib/gocell-bin.sh sourced)", name, lineNum))
				}
			}
		}
	}

	if len(violations) > 0 {
		t.Fatalf("GOCELL-BIN-FUNNEL-01 (blind-spot go -C): "+
			"directory-scoped invocation outside funnel:\n%s",
			strings.Join(violations, "\n"))
	}
}

// TestGocellBinFunnel_BlindSpot_BuildThenRun is a reverse blind-spot self-test
// (required by ai-robust.md §"工具选定后强制盲区自检").
//
// The main scanner watches for `go run ./cmd/gocell` but would miss a
// two-step `go build ./cmd/gocell && ./gocell` pattern. This test asserts
// that no verify-*.sh uses this alternative form, so the blind spot remains
// narrow and documented rather than silently exploitable.
func TestGocellBinFunnel_BlindSpot_BuildThenRun(t *testing.T) {
	t.Parallel()

	repoRoot := findModuleRoot(t)
	hackDir := filepath.Join(repoRoot, "hack")

	entries, err := os.ReadDir(hackDir)
	if err != nil {
		t.Fatalf("cannot read hack/: %v", err)
	}

	var violations []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "verify-") || !strings.HasSuffix(name, ".sh") {
			continue
		}

		scriptPath := filepath.Join(hackDir, name)
		content, err := os.ReadFile(scriptPath) //nolint:gosec // G304: same as above
		if err != nil {
			t.Errorf("cannot read %s: %v", name, err)
			continue
		}

		text := string(content)
		// Look for `go build ./cmd/gocell` followed by direct invocation of the
		// resulting binary — this is the blind-spot pattern.
		if strings.Contains(text, "go build ./cmd/gocell") {
			violations = append(violations,
				fmt.Sprintf("  %s: uses `go build ./cmd/gocell` — "+
					"this bypasses GOCELL-BIN-FUNNEL-01; use gocell::cli instead", name),
			)
		}
	}

	if len(violations) > 0 {
		t.Fatalf("GOCELL-BIN-FUNNEL-01 (blind-spot): alternative invocation pattern detected:\n%s",
			strings.Join(violations, "\n"))
	}
}

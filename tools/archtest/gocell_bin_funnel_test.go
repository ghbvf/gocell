package archtest

// INVARIANT: GOCELL-BIN-FUNNEL-01
//
// Every hack/verify-*.sh file that invokes cmd/gocell via `go run ./cmd/gocell`
// or `go -C <dir> run ./cmd/gocell` MUST also source hack/lib/gocell-bin.sh
// and route the invocation through `gocell::cli`. This ensures that the
// GOCELL_BIN pre-compiled binary optimization (plan ship-curious-treasure.md
// method M) is applied uniformly: new verify-*.sh authors cannot accidentally
// add a bare `go run ./cmd/gocell` that bypasses the shared binary.
//
// AI-robust rating: Medium.
//   Enforcement is a shell-file string scan (not Go type system), so this is
//   archtest-bound rather than compile-time. However, form-uniqueness is
//   clear: `go run ./cmd/gocell` is the only way to invoke the CLI without
//   the funnel, and the archtest names both required strings in the same file.
//   An author bypassing the funnel (writing `go run ./cmd/gocell` without
//   sourcing gocell-bin.sh) is caught immediately at the next archtest run
//   (nightly ≤24h; local via `make verify`).
//
// Blind-spot (documented per ai-robust.md §"工具选定后强制盲区自检"):
//   - `go build ./cmd/gocell && ./gocell` two-step invocation would not match
//     the `go run ./cmd/gocell` pattern; if a future script uses this form it
//     would bypass the check. Reverse blind-spot test:
//     TestGocellBinFunnel_BlindSpot_BuildThenRun asserts no verify-*.sh uses
//     this alternative form.
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
// invocation (either `go run ./cmd/gocell` or `go -C ... run ./cmd/gocell`).
func containsGoRunCmdGocell(text string) bool {
	return strings.Contains(text, "go run ./cmd/gocell") ||
		strings.Contains(text, "run ./cmd/gocell")
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

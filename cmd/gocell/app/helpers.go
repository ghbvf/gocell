package app

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ghbvf/gocell/kernel/governance"
)

// findRoot walks up from the current working directory to find the directory
// containing go.mod, which is treated as the project root.
func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found in any parent directory")
		}
		dir = parent
	}
}

// readModule reads the module path from go.mod in the given root directory.
func readModule(root string) (string, error) {
	f, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("open go.mod: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only file

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}

	return "", fmt.Errorf("module directive not found in go.mod")
}

// formatResultsFailFast prints only the first error found and returns. It
// emits no banner, no warnings, and no summary — giving CI a single, loud
// signal. A caller that needs rich output should use formatResults instead.
func formatResultsFailFast(results []governance.ValidationResult) {
	for i := range results {
		if results[i].Severity == governance.SeverityError {
			printResult(results[i])
			return
		}
	}
}

// formatResults prints validation results grouped by severity.
func formatResults(results []governance.ValidationResult) {
	if len(results) == 0 {
		fmt.Println("No issues found.")
		return
	}

	// Group by severity.
	var errors, warnings []governance.ValidationResult
	for i := range results {
		switch results[i].Severity {
		case governance.SeverityError:
			errors = append(errors, results[i])
		case governance.SeverityWarning:
			warnings = append(warnings, results[i])
		}
	}

	if len(errors) > 0 {
		fmt.Printf("ERRORS (%d):\n", len(errors))
		for _, r := range errors {
			printResult(r)
		}
		fmt.Println()
	}

	if len(warnings) > 0 {
		fmt.Printf("WARNINGS (%d):\n", len(warnings))
		for _, r := range warnings {
			printResult(r)
		}
		fmt.Println()
	}
}

// isWithinRoot checks that target resolves to a path inside root.
// Both sides are normalized to absolute paths, and symlinks are resolved
// when possible, to prevent both relative-path and symlink-based bypasses.
// SYNC: kernel/governance/helpers.go:isWithinRoot — keep in sync.
func isWithinRoot(root, target string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = resolved
	}
	if resolved, err := filepath.EvalSymlinks(absTarget); err == nil {
		absTarget = resolved
	} else {
		// Resolve longest existing ancestor for non-existent paths.
		absTarget = evalExistingPrefix(absTarget)
	}
	cleanRoot := absRoot + string(os.PathSeparator)
	return strings.HasPrefix(absTarget, cleanRoot) || absTarget == absRoot
}

// evalExistingPrefix resolves symlinks on the longest existing ancestor of p,
// then appends the non-existent suffix. This handles platforms where
// intermediate directories are symlinks (e.g., macOS /tmp → /private/tmp).
// SYNC: kernel/governance/helpers.go:evalExistingPrefix — keep in sync.
func evalExistingPrefix(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(evalExistingPrefix(parent), filepath.Base(p))
}

// printResult prints a single validation result in human-readable format.
//
// Output shape differs by what the finding is anchored to:
//   - File set:  "at <file>[:<line>[:<col>]]" — a plain file:line:col prefix
//     so IDE / terminal "click-to-open" (GoLand, VS Code, iTerm2) can jump.
//   - Scope set: "at [scope: <name>]" — a virtual domain (e.g. "project")
//     rendered as a bracketed label so users do not mistake it for a
//     jumpable path.
//   - Neither:   no location line is printed.
//
// The field name stays on the message line in every case.
func printResult(r governance.ValidationResult) {
	msg := r.Message
	if r.Field != "" {
		msg += fmt.Sprintf(" (field: %s)", r.Field)
	}
	fmt.Printf("  [%s] %s\n", r.Code, msg)

	switch {
	case r.Scope != "":
		fmt.Printf("         at [scope: %s]\n", r.Scope)
	case r.File != "":
		location := r.File
		if r.Line > 0 {
			location += fmt.Sprintf(":%d", r.Line)
			if r.Column > 0 {
				location += fmt.Sprintf(":%d", r.Column)
			}
		}
		fmt.Printf("         at %s\n", location)
	}
}

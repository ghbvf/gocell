package main

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
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}

	return "", fmt.Errorf("module directive not found in go.mod")
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
func printResult(r governance.ValidationResult) {
	location := r.File
	if r.Field != "" {
		location += " -> " + r.Field
	}
	fmt.Printf("  [%s] %s\n", r.Code, r.Message)
	if location != "" {
		fmt.Printf("         at %s\n", location)
	}
}

package archtestrunner

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ghbvf/gocell/pkg/cmdrun"
)

// changedArchtestFiles returns the set of top-level tools/archtest/*_test.go
// file paths (repo-relative, slash-separated) that differ between the current
// working tree and the merge-base with origin/develop.
//
// Two git diffs are unioned to capture both committed changes (vs merge-base)
// and uncommitted working-tree changes:
//  1. git diff --name-only <merge-base>  (committed changes since branch point)
//  2. git diff --name-only HEAD          (working tree vs HEAD)
//
// "Mechanical --changed" semantic: this function returns the set of archtest
// files that were touched; if the set is empty, the caller (Run/ListTests)
// selects the empty set and returns a trivially passed Report without running
// any tests. This is intentional — --changed is a focused filter, not a
// "run nothing if nothing changed" shortcut.
func changedArchtestFiles(ctx context.Context, workspaceRoot string) ([]string, error) {
	gitTool, err := cmdrun.NewTool("git")
	if err != nil {
		return nil, fmt.Errorf("archtestrunner: resolve git tool: %w", err)
	}

	mergeBase, err := gitMergeBase(ctx, gitTool, workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("archtestrunner: git merge-base: %w", err)
	}

	// Committed changes relative to merge-base.
	committed, err := gitDiffNames(ctx, gitTool, workspaceRoot, mergeBase)
	if err != nil {
		return nil, fmt.Errorf("archtestrunner: git diff committed: %w", err)
	}

	// Working-tree changes relative to HEAD.
	worktree, err := gitDiffNames(ctx, gitTool, workspaceRoot, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("archtestrunner: git diff working-tree: %w", err)
	}

	all := dedupe(append(committed, worktree...))
	return filterArchtestFiles(all), nil
}

// changedFilesToTests maps a list of changed file paths (repo-relative) to the
// top-level Test* function names declared in those files. Only files that pass
// filterArchtestFiles are scanned; others are silently ignored.
//
// Used by the --changed selection path to avoid re-running the full suite when
// only a few archtest files were modified.
func changedFilesToTests(workspaceRoot string, changedFiles []string) ([]string, error) {
	archtestFiles := filterArchtestFiles(changedFiles)
	if len(archtestFiles) == 0 {
		return nil, nil
	}

	var tests []string
	for _, relPath := range archtestFiles {
		absPath := filepath.Join(workspaceRoot, relPath)
		_, funcs, err := parseTestFile(absPath)
		if err != nil {
			return nil, fmt.Errorf("archtestrunner: parse changed file %s: %w", relPath, err)
		}
		tests = append(tests, funcs...)
	}
	return tests, nil
}

// filterArchtestFiles returns the subset of paths that are top-level
// tools/archtest/*_test.go files (not in subdirs). Input paths may use
// OS-native or forward-slash separators.
func filterArchtestFiles(paths []string) []string {
	var out []string
	for _, p := range paths {
		// Normalize to forward slashes for consistent matching.
		norm := filepath.ToSlash(p)
		if isTopLevelArchtestFile(norm) {
			out = append(out, p)
		}
	}
	return out
}

// isTopLevelArchtestFile reports whether path (slash-separated) is a top-level
// tools/archtest/*_test.go file (not under a subdirectory of archtest/).
func isTopLevelArchtestFile(slashPath string) bool {
	prefix := archtestPkgDir + "/"
	if !strings.HasPrefix(slashPath, prefix) {
		return false
	}
	rest := slashPath[len(prefix):]
	if !strings.HasSuffix(rest, "_test.go") {
		return false
	}
	// Must not contain a "/" (i.e. not in a subdir).
	return !strings.Contains(rest, "/")
}

// gitMergeBase returns the merge base commit SHA between HEAD and origin/develop.
func gitMergeBase(ctx context.Context, tool cmdrun.ValidatedTool, workspaceRoot string) (string, error) {
	out, err := cmdrun.RunWith(ctx, tool, cmdrun.RunOptions{Dir: workspaceRoot},
		"merge-base", "origin/develop", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git merge-base: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// gitDiffNames runs `git diff --name-only <ref>` and returns the list of changed files.
func gitDiffNames(ctx context.Context, tool cmdrun.ValidatedTool, workspaceRoot, ref string) ([]string, error) {
	out, err := cmdrun.RunWith(ctx, tool, cmdrun.RunOptions{Dir: workspaceRoot},
		"diff", "--name-only", ref)
	if err != nil {
		// Non-zero exit from git diff is an error (e.g. bad ref).
		return nil, fmt.Errorf("git diff --name-only %s: %w", ref, err)
	}
	return splitLines(string(out)), nil
}

// splitLines splits a newline-separated string into non-empty lines.
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// dedupe removes duplicate strings while preserving order.
func dedupe(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

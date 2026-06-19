package archtestrunner

import (
	"context"
	"fmt"
	"strings"

	"github.com/ghbvf/gocell/framework/pkg/cmdrun"
)

// changedRepoFiles returns the set of repo-relative, slash-separated file paths
// (of any kind — source, test, contract, doc) that differ between the current
// working tree and the merge-base with origin/develop.
//
// Three git listings are unioned so no in-tree change escapes:
//  1. git diff --name-only <merge-base>           committed since branch point
//  2. git diff --name-only HEAD                   tracked working-tree edits
//  3. git ls-files --others --exclude-standard    untracked (non-ignored) files
//
// (3) matters for local use: a newly-created-but-not-yet-`git add`-ed source
// file would otherwise be invisible, letting the local --changed gate silently
// skip it. PR CI is unaffected (its head is committed).
//
// This is the single git seam for --changed: it reports *what* changed (raw),
// and the selection policy (which rules a change affects) lives downstream in
// applyFilters → selectByChangedSource (#1877). If the set is empty, the caller
// (Run/ListTests) selects the empty set and returns a trivially passed Report —
// --changed is a focused pre-filter, not a "run nothing if nothing changed"
// shortcut.
func changedRepoFiles(ctx context.Context, workspaceRoot string) ([]string, error) {
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

	// Tracked working-tree changes relative to HEAD.
	worktree, err := gitDiffNames(ctx, gitTool, workspaceRoot, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("archtestrunner: git diff working-tree: %w", err)
	}

	// Untracked (non-ignored) files.
	untracked, err := gitUntrackedNames(ctx, gitTool, workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("archtestrunner: git ls-files untracked: %w", err)
	}

	all := append(committed, worktree...) //nolint:gocritic // intentional union of three disjoint listings
	all = append(all, untracked...)
	return dedupe(all), nil
}

// gitUntrackedNames runs `git ls-files --others --exclude-standard` and returns
// the untracked (non-ignored) files.
func gitUntrackedNames(ctx context.Context, tool cmdrun.ValidatedTool, workspaceRoot string) ([]string, error) {
	out, err := cmdrun.RunWith(ctx, tool, cmdrun.RunOptions{Dir: workspaceRoot},
		"ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, fmt.Errorf("git ls-files --others --exclude-standard: %w", err)
	}
	return splitLines(string(out)), nil
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

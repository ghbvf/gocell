package codegen

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// VerifyResult reports the outcome of a worktree-sandboxed verify pass.
type VerifyResult struct {
	// Drifted lists the relative paths whose disk content differs from the
	// freshly-generated content (per `git status --porcelain`). Empty when
	// the worktree is clean after running the generator.
	Drifted []string
	// DiffSummary is the truncated `git diff` output for the drifted files,
	// suitable for printing to CI logs. Empty when Drifted is empty.
	DiffSummary string
}

// maxDiffLinesPerFile bounds the per-file diff output included in
// DiffSummary, preventing CI log explosion when a generator regresses
// across many files. Beyond this limit a "(diff truncated)" marker is
// appended and the rest is dropped.
const maxDiffLinesPerFile = 200

// VerifyInWorktree runs generateFn inside an ephemeral git worktree
// detached at HEAD, then reports any resulting diff as drift.
//
// Pattern (K8s hack/lib/verify-generated.sh):
//  1. `git worktree add --detach <tmp> HEAD` — shares .git object store,
//     no history copy
//  2. generateFn(tmp) — caller runs the codegen pipeline rooted at tmp
//  3. `git status --porcelain` — precise diff list
//  4. `git diff` per file (truncated) — DiffSummary for CI logs
//  5. `git worktree remove --force <tmp>` + os.RemoveAll — cleanup
//
// generateFn receives the absolute path of the temporary worktree and is
// expected to invoke the codegen pipeline with that path as the project
// root. It must not mutate state outside tmp (the K8s pattern relies on
// worktree isolation; cross-tree writes break the diff signal).
func VerifyInWorktree(repoRoot string, generateFn func(workdir string) error) (VerifyResult, error) {
	var res VerifyResult

	if repoRoot == "" {
		return res, fmt.Errorf("verify: repoRoot is empty")
	}
	if generateFn == nil {
		return res, fmt.Errorf("verify: generateFn is nil")
	}

	tmp, err := os.MkdirTemp("", "gocell-codegen-verify-*")
	if err != nil {
		return res, fmt.Errorf("verify: mktemp: %w", err)
	}
	// Defer cleanup before issuing the worktree-add so a partial failure
	// still releases the temp directory.
	defer func() {
		// Best-effort cleanup; log via fmt.Fprintln if needed but never
		// shadow the function's primary error.
		_ = exec.Command("git", "-C", repoRoot, "worktree", "remove", "--force", tmp).Run()
		_ = os.RemoveAll(tmp)
	}()

	add := exec.Command("git", "-C", repoRoot, "worktree", "add", "--detach", tmp, "HEAD")
	if out, err := add.CombinedOutput(); err != nil {
		return res, fmt.Errorf("verify: git worktree add %s: %w; output: %s", tmp, err, out)
	}

	if err := generateFn(tmp); err != nil {
		return res, fmt.Errorf("verify: generateFn: %w", err)
	}

	statusOut, err := exec.Command("git", "-C", tmp, "status", "--porcelain").CombinedOutput()
	if err != nil {
		return res, fmt.Errorf("verify: git status: %w; output: %s", err, statusOut)
	}
	if len(bytes.TrimSpace(statusOut)) == 0 {
		return res, nil
	}

	res.Drifted = parseStatusFiles(statusOut)
	res.DiffSummary = buildDiffSummary(tmp, res.Drifted)
	return res, nil
}

// parseStatusFiles extracts the file paths from `git status --porcelain`
// output. Lines shorter than 4 bytes (the "XY <path>" minimum) are skipped.
// Renames and copies (`R<old> -> <new>`) report only the new path.
func parseStatusFiles(porcelain []byte) []string {
	var out []string
	for _, line := range bytes.Split(porcelain, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if len(line) < 4 {
			continue
		}
		// porcelain format: "XY <path>" — first 2 chars status, then space.
		raw := string(bytes.TrimSpace(line[3:]))
		if raw == "" {
			continue
		}
		// Rename / copy: "R  old -> new" or "C  old -> new". Take new path.
		if idx := strings.LastIndex(raw, " -> "); idx >= 0 {
			raw = raw[idx+len(" -> "):]
		}
		out = append(out, filepath.Clean(raw))
	}
	return out
}

// buildDiffSummary collects a stat header plus per-file diffs (truncated
// at maxDiffLinesPerFile lines) for inclusion in CI logs.
func buildDiffSummary(workdir string, files []string) string {
	var sb strings.Builder

	stat, _ := exec.Command("git", "-C", workdir, "diff", "--stat").CombinedOutput()
	sb.Write(stat)
	sb.WriteString("\n")

	for _, f := range files {
		sb.WriteString("===== " + f + " =====\n")
		perOut, _ := exec.Command("git", "-C", workdir, "diff", "--", f).CombinedOutput()
		writeTruncatedLines(&sb, perOut, maxDiffLinesPerFile)
	}
	return sb.String()
}

// writeTruncatedLines copies up to maxLines lines from src into dst,
// appending a truncation marker when src exceeds the budget.
func writeTruncatedLines(dst *strings.Builder, src []byte, maxLines int) {
	lines := bytes.Split(src, []byte("\n"))
	if len(lines) <= maxLines {
		dst.Write(src)
		return
	}
	for _, line := range lines[:maxLines] {
		dst.Write(line)
		dst.WriteByte('\n')
	}
	dst.WriteString("... (diff truncated)\n")
}

package codegen

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/pkg/pathsafe"
)

const (
	generatedFileMode os.FileMode = 0o644
)

// WriteAction is an outcome enum for a Write call.
//
// Drift is reported as ActionDrifted (not an error) so verify-mode callers
// can iterate Results and aggregate drift counts without errors.Is plumbing.
type WriteAction string

const (
	// ActionWritten reports that the file was created or rewritten on disk.
	ActionWritten WriteAction = "written"
	// ActionUnchanged reports that the on-disk content already matches the
	// requested content; no write occurred.
	ActionUnchanged WriteAction = "unchanged"
	// ActionWouldWrite reports that DryRun was true; no write occurred but
	// would have created or changed the file.
	ActionWouldWrite WriteAction = "would-write"
	// ActionDrifted reports that Verify was true and the requested content
	// differs from the on-disk file (or the file is missing). No write
	// occurred. Drift is a normal verify outcome — not an error.
	ActionDrifted WriteAction = "drifted"
)

// WriteOptions controls a single Write call.
type WriteOptions struct {
	// Path is the absolute target file path.
	Path string
	// Content is the bytes to compare against / write to the target.
	Content []byte
	// RepoRoot, when non-empty, gates Path through governance.IsWithinRoot
	// to reject path traversal.
	RepoRoot string
	// DryRun suppresses the filesystem mutation and returns ActionWouldWrite
	// (or ActionUnchanged when content matches).
	DryRun bool
	// Verify suppresses the filesystem mutation and returns ActionDrifted
	// (or ActionUnchanged) by comparing Content with the on-disk file.
	// DryRun and Verify are mutually exclusive at the CLI layer; combining
	// them here is harmless — Verify dominates (no write either way).
	Verify bool
}

// WriteResult reports the outcome of a Write call.
type WriteResult struct {
	Action WriteAction
	Path   string
}

// Write persists Content to opts.Path with two safety guards:
//
//  1. opts.Path must be inside opts.RepoRoot (governance.IsWithinRoot)
//     when RepoRoot is set. Out-of-root writes are refused.
//  2. If opts.Path already exists on disk, its first bytes must match
//     governance.IsGoCellGenerated. User-edited files cannot be silently
//     overwritten — the caller must move the file or delete it first.
//
// Returns a WriteResult.Action describing what happened. The error return
// is non-nil only for real failures (IO errors, path traversal, refusal
// to overwrite a user file). Drift is reported via ActionDrifted, not error.
func Write(opts WriteOptions) (WriteResult, error) {
	res := WriteResult{Path: opts.Path}
	if opts.Path == "" {
		return res, fmt.Errorf("codegen write: Path is empty")
	}
	if opts.RepoRoot != "" && !governance.IsWithinRoot(opts.RepoRoot, opts.Path) {
		return res, fmt.Errorf("codegen write: path escapes RepoRoot: %s", opts.Path)
	}

	existing, readErr := os.ReadFile(filepath.Clean(opts.Path))
	switch {
	case readErr == nil:
		if !governance.IsGoCellGenerated(existing) {
			return res, fmt.Errorf("codegen write: refusing to overwrite non-generated file %s "+
				"(generated files must start with the gocell header; remove the file or move "+
				"hand-written code to a sibling location and re-run generation)", opts.Path)
		}
		if bytes.Equal(existing, opts.Content) {
			res.Action = ActionUnchanged
			return res, nil
		}
		if opts.Verify {
			res.Action = ActionDrifted
			return res, nil
		}
	case errors.Is(readErr, fs.ErrNotExist):
		if opts.Verify {
			// Missing target counts as drift under verify.
			res.Action = ActionDrifted
			return res, nil
		}
	default:
		return res, fmt.Errorf("codegen write: read existing file %s: %w", opts.Path, readErr)
	}

	if opts.DryRun {
		res.Action = ActionWouldWrite
		return res, nil
	}

	if err := pathsafe.WriteFileForce(opts.RepoRoot, opts.Path, opts.Content, generatedFileMode); err != nil {
		return res, fmt.Errorf("codegen write: write %s: %w", opts.Path, err)
	}
	res.Action = ActionWritten
	return res, nil
}

package verify

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestResult represents the outcome of a single test target.
type TestResult struct {
	Name      string
	Passed    bool
	Output    string
	ZeroMatch bool // true when -run pattern matched no tests
}

// VerifyResult represents the outcome of verifying a slice, cell, or journey.
type VerifyResult struct {
	TargetID      string
	Passed        bool
	Results       []TestResult
	Errors        []error
	ManualPending []string // text of manual criteria not yet verified
}

// Ref prefix and criteria mode constants.
const (
	PrefixJourney  = "journey"
	PrefixSmoke    = "smoke"
	PrefixUnit     = "unit"
	PrefixContract = "contract"

	ModeAuto   = "auto"
	ModeManual = "manual"
)

// Runner executes metadata-driven verification tests.
type Runner struct {
	project *metadata.ProjectMeta
	root    string // Go module root (where go.mod lives)
}

// NewRunner creates a Runner for executing verification tests.
func NewRunner(project *metadata.ProjectMeta, root string) *Runner {
	return &Runner{project: project, root: root}
}

// VerifySlice runs tests for a slice driven by metadata verify.unit and
// verify.contract declarations. If neither is declared, falls back to
// running all tests in the slice package.
func (r *Runner) VerifySlice(ctx context.Context, sliceKey string) (*VerifyResult, error) {
	cellID, sliceID, err := parseSliceKey(sliceKey)
	if err != nil {
		return nil, err
	}

	sm := r.project.Slices[sliceKey]
	if sm == nil {
		return nil, errcode.New(errcode.ErrSliceNotFound,
			fmt.Sprintf("slice %q not found in project metadata", sliceKey))
	}

	// Try metadata-style dir first; if it doesn't exist as a Go package,
	// fall back to the hyphen-stripped variant (e.g., session-login → sessionlogin).
	pkg := resolveSlicePkg(r.root, cellID, sliceID)
	result := &VerifyResult{TargetID: sliceKey, Passed: true}

	unitRefs := sm.Verify.Unit
	contractRefs := sm.Verify.Contract

	// If metadata declares specific refs, use them.
	if len(unitRefs) > 0 || len(contractRefs) > 0 {
		r.runRefs(ctx, result, pkg, unitRefs)
		r.runRefs(ctx, result, pkg, contractRefs)
		return result, nil
	}

	// Fallback: no metadata refs, run all tests in the slice package.
	res := runGoTest(ctx, r.root, []string{pkg, "-v"})
	recordResult(result, sliceKey, res, pkg, "")
	return result, nil
}

// VerifyCell runs smoke tests for a cell driven by metadata verify.smoke.
// If no smoke refs are declared, logs a warning and returns passed.
func (r *Runner) VerifyCell(ctx context.Context, cellID string) (*VerifyResult, error) {
	cm := r.project.Cells[cellID]
	if cm == nil {
		return nil, errcode.New(errcode.ErrCellNotFound,
			fmt.Sprintf("cell %q not found in project metadata", cellID))
	}

	result := &VerifyResult{TargetID: cellID, Passed: true}

	smokeRefs := cm.Verify.Smoke
	if len(smokeRefs) == 0 {
		slog.Warn("cell has no verify.smoke declarations", slog.String("cell", cellID))
		result.Results = append(result.Results, TestResult{
			Name:   cellID,
			Passed: true,
			Output: "warning: no verify.smoke declarations — zero verification performed",
		})
		return result, nil
	}

	cellPkg := fmt.Sprintf("./cells/%s/...", cellID)
	for _, ref := range smokeRefs {
		resolved, err := resolveRef(ref)
		if err != nil {
			result.Errors = append(result.Errors, err)
			result.Results = append(result.Results, TestResult{Name: ref, Passed: false})
			result.Passed = false
			continue
		}
		pkg := resolved.Pkg
		if pkg == "" {
			pkg = cellPkg
		}
		res := runGoTest(ctx, r.root, []string{pkg, "-v", "-run", resolved.RunPattern})
		recordResult(result, ref, res, pkg, resolved.RunPattern)
	}
	return result, nil
}

// RunJourney runs auto-mode pass criteria for a journey and collects
// manual criteria into ManualPending.
func (r *Runner) RunJourney(ctx context.Context, journeyID string) (*VerifyResult, error) {
	j := r.project.Journeys[journeyID]
	if j == nil {
		return nil, errcode.New(errcode.ErrJourneyNotFound,
			fmt.Sprintf("journey %q not found in project metadata", journeyID))
	}

	result := &VerifyResult{TargetID: journeyID, Passed: true}

	// Single pass: classify criteria into manual / auto-runnable / auto-incomplete.
	var autoRefs []string
	for _, pc := range j.PassCriteria {
		switch {
		case pc.Mode == ModeManual:
			result.ManualPending = append(result.ManualPending, pc.Text)
		case pc.Mode == ModeAuto && pc.CheckRef != "":
			autoRefs = append(autoRefs, pc.CheckRef)
		case pc.Mode == ModeAuto && pc.CheckRef == "":
			result.Results = append(result.Results, TestResult{
				Name:   pc.Text,
				Passed: false,
				Output: "auto criterion has no checkRef — cannot verify automatically",
			})
			result.Passed = false
		}
	}

	if len(autoRefs) == 0 {
		if len(result.ManualPending) > 0 && result.Passed {
			result.Results = append(result.Results, TestResult{
				Name:   journeyID,
				Passed: true,
				Output: "warning: only manual criteria — automated verification not possible",
			})
		}
		return result, nil
	}

	for _, ref := range autoRefs {
		resolved, err := resolveRef(ref)
		if err != nil {
			result.Errors = append(result.Errors, err)
			result.Results = append(result.Results, TestResult{Name: ref, Passed: false})
			result.Passed = false
			continue
		}
		pkg, extraArgs := r.resolveJourneyPkg(resolved)
		args := append([]string{pkg, "-v", "-run", resolved.RunPattern}, extraArgs...)
		res := runGoTest(ctx, r.root, args)
		recordResult(result, ref, res, pkg, resolved.RunPattern)
	}
	return result, nil
}

// runRefs resolves each ref independently and runs go test per-ref.
// Individual execution ensures a stale or misspelled ref cannot hide
// behind a passing sibling pattern.
func (r *Runner) runRefs(ctx context.Context, result *VerifyResult, fallbackPkg string, refs []string) {
	for _, ref := range refs {
		resolved, err := resolveRef(ref)
		if err != nil {
			result.Errors = append(result.Errors, err)
			result.Results = append(result.Results, TestResult{Name: ref, Passed: false})
			result.Passed = false
			continue
		}
		pkg := fallbackPkg
		if resolved.Pkg != "" {
			pkg = resolved.Pkg
		}
		res := runGoTest(ctx, r.root, []string{pkg, "-v", "-run", resolved.RunPattern})
		recordResult(result, ref, res, pkg, resolved.RunPattern)
	}
}

// recordResult appends a goTestResult to the VerifyResult, handling ZeroMatch
// and error propagation in a single place.
func recordResult(result *VerifyResult, name string, res goTestResult, pkg, pattern string) {
	tr := TestResult{
		Name:      name,
		Passed:    res.Passed,
		Output:    res.Output,
		ZeroMatch: res.ZeroMatch,
	}
	if res.ZeroMatch {
		tr.Passed = false
		msg := fmt.Sprintf("matched no tests in %s", pkg)
		if pattern != "" {
			msg = fmt.Sprintf("pattern %q %s — check your YAML ref", pattern, msg)
		}
		result.Errors = append(result.Errors, errcode.New(errcode.ErrZeroTestMatch, msg))
	}
	result.Results = append(result.Results, tr)
	if !tr.Passed {
		result.Passed = false
	}
	if res.Err != nil {
		result.Errors = append(result.Errors, res.Err)
	}
}

// resolveJourneyPkg determines the Go test package and extra args for a journey ref.
// Prefers ./tests/integration/... (with -tags=integration) if present,
// falls back to ./journeys/..., then ./... as last resort.
func (r *Runner) resolveJourneyPkg(ref resolvedRef) (pkg string, extraArgs []string) {
	if ref.Pkg != "" {
		return ref.Pkg, nil
	}
	if dirExists(filepath.Join(r.root, "tests", "integration")) {
		return "./tests/integration/...", []string{"-tags=integration"}
	}
	if dirExists(filepath.Join(r.root, "journeys")) {
		return "./journeys/...", nil
	}
	return "./...", nil
}

// resolveSlicePkg determines the Go test package path for a slice.
// In this repo, metadata dirs (session-login/) contain only slice.yaml,
// while the Go package lives in a hyphen-stripped sibling (sessionlogin/).
// We check for Go source files, not just directory existence.
//
// Precondition: cellID and sliceID must have passed parseSliceKey validation.
func resolveSlicePkg(root, cellID, sliceID string) string {
	base := filepath.Join("cells", cellID, "slices")
	// Prefer the dir that actually contains Go files.
	stripped := strings.ReplaceAll(sliceID, "-", "")
	if hasGoFiles(filepath.Join(root, base, stripped)) {
		return fmt.Sprintf("./%s/%s/...", base, stripped)
	}
	if hasGoFiles(filepath.Join(root, base, sliceID)) {
		return fmt.Sprintf("./%s/%s/...", base, sliceID)
	}
	// Fallback: try stripped dir existence (may have Go files in subdirs).
	if dirExists(filepath.Join(root, base, stripped)) {
		return fmt.Sprintf("./%s/%s/...", base, stripped)
	}
	// Last resort: metadata-style path (go test will give clear error).
	return fmt.Sprintf("./%s/%s/...", base, sliceID)
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func hasGoFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			return true
		}
	}
	return false
}

// parseSliceKey splits "cellID/sliceID" into its parts.
func parseSliceKey(key string) (cellID, sliceID string, err error) {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("invalid slice key %q: expected format \"cellID/sliceID\"", key))
	}
	if strings.Contains(parts[0], "..") || strings.ContainsAny(parts[0], `/\`) {
		return "", "", errcode.New(errcode.ErrValidationFailed, "invalid cellID: contains path separator or traversal")
	}
	if strings.Contains(parts[1], "..") || strings.ContainsAny(parts[1], `/\`) {
		return "", "", errcode.New(errcode.ErrValidationFailed, "invalid sliceID: contains path separator or traversal")
	}
	return parts[0], parts[1], nil
}


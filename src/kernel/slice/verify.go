// Package slice provides verification runners for slices, cells, and journeys.
// It shells out to `go test` to execute the relevant test suites.
package slice

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/registry"
)

// TestResult represents the outcome of a single test target.
type TestResult struct {
	Name   string
	Passed bool
	Output string
}

// VerifyResult represents the outcome of verifying a slice, cell, or journey.
type VerifyResult struct {
	TargetID string
	Passed   bool
	Results  []TestResult
	Errors   []error
}

// Runner executes verification tests.
type Runner struct {
	project *metadata.ProjectMeta
	cells   *registry.CellRegistry
	root    string // project root for running go test
}

// NewRunner creates a Runner for executing verification tests.
// The root parameter should point to the Go module root (where go.mod lives).
func NewRunner(project *metadata.ProjectMeta, root string) *Runner {
	return &Runner{
		project: project,
		cells:   registry.NewCellRegistry(project),
		root:    root,
	}
}

// VerifySlice runs unit + contract tests for a slice.
// sliceKey uses the format "cellID/sliceID".
// It runs `go test ./cells/{cellID}/slices/{sliceID}/... -v` from the project root.
func (r *Runner) VerifySlice(ctx context.Context, sliceKey string) (*VerifyResult, error) {
	cellID, sliceID, err := parseSliceKey(sliceKey)
	if err != nil {
		return nil, err
	}

	if _, ok := r.project.Slices[sliceKey]; !ok {
		return nil, fmt.Errorf("slice %q not found in project metadata", sliceKey)
	}

	pkg := fmt.Sprintf("./cells/%s/slices/%s/...", cellID, sliceID)
	output, passed, execErr := runGoTest(ctx, r.root, []string{pkg, "-v"})

	result := &VerifyResult{
		TargetID: sliceKey,
		Passed:   passed,
		Results: []TestResult{
			{
				Name:   sliceKey,
				Passed: passed,
				Output: output,
			},
		},
	}
	if execErr != nil {
		result.Errors = append(result.Errors, execErr)
	}
	return result, nil
}

// VerifyCell runs smoke tests for a cell.
// It runs `go test ./cells/{cellID}/... -v -run Smoke` from the project root.
func (r *Runner) VerifyCell(ctx context.Context, cellID string) (*VerifyResult, error) {
	if r.project.Cells[cellID] == nil {
		return nil, fmt.Errorf("cell %q not found in project metadata", cellID)
	}

	pkg := fmt.Sprintf("./cells/%s/...", cellID)
	output, passed, execErr := runGoTest(ctx, r.root, []string{pkg, "-v", "-run", "Smoke"})

	result := &VerifyResult{
		TargetID: cellID,
		Passed:   passed,
		Results: []TestResult{
			{
				Name:   cellID,
				Passed: passed,
				Output: output,
			},
		},
	}
	if execErr != nil {
		result.Errors = append(result.Errors, execErr)
	}
	return result, nil
}

// RunJourney runs auto-mode pass criteria for a journey.
// It collects all PassCriteria with Mode=="auto" and a non-empty CheckRef,
// then runs them via go test. Criteria without CheckRef are skipped.
func (r *Runner) RunJourney(ctx context.Context, journeyID string) (*VerifyResult, error) {
	journey := r.project.Journeys[journeyID]
	if journey == nil {
		return nil, fmt.Errorf("journey %q not found in project metadata", journeyID)
	}

	result := &VerifyResult{
		TargetID: journeyID,
		Passed:   true,
	}

	autoRefs := collectAutoCheckRefs(journey)
	if len(autoRefs) == 0 {
		// No auto criteria with checkRefs; nothing to run.
		return result, nil
	}

	for _, ref := range autoRefs {
		pkg, runPattern := resolveCheckRef(ref)
		args := []string{pkg, "-v", "-run", runPattern}
		output, passed, execErr := runGoTest(ctx, r.root, args)

		result.Results = append(result.Results, TestResult{
			Name:   ref,
			Passed: passed,
			Output: output,
		})
		if !passed {
			result.Passed = false
		}
		if execErr != nil {
			result.Errors = append(result.Errors, execErr)
		}
	}

	return result, nil
}

// parseSliceKey splits "cellID/sliceID" into its parts.
// It rejects cellID or sliceID containing path traversal sequences or separators.
func parseSliceKey(key string) (cellID, sliceID string, err error) {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid slice key %q: expected format \"cellID/sliceID\"", key)
	}
	if strings.Contains(parts[0], "..") || strings.ContainsAny(parts[0], `/\`) {
		return "", "", fmt.Errorf("invalid cellID: contains path separator or traversal")
	}
	if strings.Contains(parts[1], "..") || strings.ContainsAny(parts[1], `/\`) {
		return "", "", fmt.Errorf("invalid sliceID: contains path separator or traversal")
	}
	return parts[0], parts[1], nil
}

// collectAutoCheckRefs returns all CheckRef values from auto-mode pass criteria.
func collectAutoCheckRefs(j *metadata.JourneyMeta) []string {
	var refs []string
	for _, pc := range j.PassCriteria {
		if pc.Mode == "auto" && pc.CheckRef != "" {
			refs = append(refs, pc.CheckRef)
		}
	}
	return refs
}

// resolveCheckRef converts a checkRef like "journey.J-sso-login.oidc-redirect"
// into a go test package and -run pattern.
// Convention: checkRef = "journey.{journeyID}.{testSuffix}"
// Maps to: package "./journeys/..." with -run pattern matching the suffix.
// If the format doesn't match, it falls back to running "./..." with the ref as pattern.
func resolveCheckRef(ref string) (pkg string, runPattern string) {
	parts := strings.SplitN(ref, ".", 3)
	if len(parts) == 3 && parts[0] == "journey" {
		return "./journeys/...", parts[2]
	}
	// Fallback: run from root with full ref as pattern.
	return "./...", ref
}

// runGoTest executes `go test` with the given arguments in the specified directory.
// It returns the combined stdout+stderr output, whether the test passed (exit 0),
// and any error that is not an ExitError (indicating the command couldn't run at all).
func runGoTest(ctx context.Context, dir string, args []string) (output string, passed bool, err error) {
	fullArgs := append([]string{"test"}, args...)
	cmd := exec.CommandContext(ctx, "go", fullArgs...)
	cmd.Dir = filepath.Clean(dir)

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	runErr := cmd.Run()
	output = buf.String()

	if runErr == nil {
		return output, true, nil
	}

	// If it's an ExitError, the test ran but failed (non-zero exit).
	var exitErr *exec.ExitError
	if ok := isExitError(runErr, &exitErr); ok {
		return output, false, nil
	}

	// Other error: command couldn't execute at all.
	return output, false, fmt.Errorf("go test execution failed: %w", runErr)
}

// isExitError checks whether err is an *exec.ExitError and assigns it to target.
// This is a helper to avoid importing errors in the main flow.
func isExitError(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}

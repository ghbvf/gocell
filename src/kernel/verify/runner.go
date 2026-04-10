package verify

import (
	"context"
	"fmt"
	"log/slog"
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

	pkg := fmt.Sprintf("./cells/%s/slices/%s/...", cellID, sliceID)
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
			pkg = fmt.Sprintf("./cells/%s/...", cellID)
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

	// Collect manual criteria.
	for _, pc := range j.PassCriteria {
		if pc.Mode == ModeManual {
			result.ManualPending = append(result.ManualPending, pc.Text)
		}
	}

	// Run auto criteria.
	autoRefs := collectAutoCheckRefs(j)
	if len(autoRefs) == 0 {
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
		pkg := resolved.Pkg
		if pkg == "" {
			pkg = "./..."
		}
		res := runGoTest(ctx, r.root, []string{pkg, "-v", "-run", resolved.RunPattern})
		recordResult(result, ref, res, pkg, resolved.RunPattern)
	}
	return result, nil
}

// runRefs resolves each ref and runs go test with the CamelCase pattern.
func (r *Runner) runRefs(ctx context.Context, result *VerifyResult, fallbackPkg string, refs []string) {
	if len(refs) == 0 {
		return
	}
	// Collect all patterns and run as a single invocation with | alternation.
	var patterns []string
	var names []string
	for _, ref := range refs {
		resolved, err := resolveRef(ref)
		if err != nil {
			result.Errors = append(result.Errors, err)
			result.Results = append(result.Results, TestResult{Name: ref, Passed: false})
			result.Passed = false
			continue
		}
		patterns = append(patterns, resolved.RunPattern)
		names = append(names, ref)
	}

	if len(patterns) == 0 {
		return
	}

	pkg := fallbackPkg
	combined := strings.Join(patterns, "|")
	res := runGoTest(ctx, r.root, []string{pkg, "-v", "-run", combined})
	recordResult(result, strings.Join(names, " + "), res, pkg, combined)
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

// collectAutoCheckRefs returns all CheckRef values from auto-mode pass criteria.
func collectAutoCheckRefs(j *metadata.JourneyMeta) []string {
	var refs []string
	for _, pc := range j.PassCriteria {
		if pc.Mode == ModeAuto && pc.CheckRef != "" {
			refs = append(refs, pc.CheckRef)
		}
	}
	return refs
}

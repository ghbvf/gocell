package verify

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestResult represents the outcome of a single test target.
type TestResult struct {
	Name        string
	Passed      bool
	Output      string
	ZeroMatch   bool // true when -run pattern matched no tests
	SkippedOnly bool // true when matched tests all skipped
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
	project   *metadata.ProjectMeta
	root      string // Go module root (where go.mod lives)
	goTest    goTestRunner
	goTestErr error
}

// NewRunner creates a Runner for executing verification tests.
func NewRunner(project *metadata.ProjectMeta, root string) *Runner {
	goTest, err := newGoTestRunner()
	return &Runner{project: project, root: root, goTest: goTest, goTestErr: err}
}

func (r *Runner) runGoTest(ctx context.Context, dir string, args []string) goTestResult {
	if r.goTestErr != nil {
		return goTestResult{Err: r.goTestErr}
	}
	return r.goTest.run(ctx, dir, args)
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
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrSliceNotFound,
			"slice not found in project metadata",
			errcode.WithInternal(fmt.Sprintf("slice=%q", sliceKey)))
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
	res := r.runGoTest(ctx, r.root, []string{pkg, "-v"})
	recordResult(result, sliceKey, res, pkg, "")
	return result, nil
}

// VerifyCell runs smoke tests for a cell driven by metadata verify.smoke.
// If no smoke refs are declared, logs a warning and returns passed.
func (r *Runner) VerifyCell(ctx context.Context, cellID string) (*VerifyResult, error) {
	cm := r.project.Cells[cellID]
	if cm == nil {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrCellNotFound,
			"cell not found in project metadata",
			errcode.WithInternal(fmt.Sprintf("cell=%q", cellID)))
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
		res := r.runGoTest(ctx, r.root, []string{pkg, "-v", "-run", resolved.RunPattern})
		recordResult(result, ref, res, pkg, resolved.RunPattern)
	}
	return result, nil
}

// RunJourney runs auto-mode pass criteria for a journey and collects
// manual criteria into ManualPending.
func (r *Runner) RunJourney(ctx context.Context, journeyID string) (*VerifyResult, error) {
	j := r.project.Journeys[journeyID]
	if j == nil {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrJourneyNotFound,
			"journey not found in project metadata",
			errcode.WithInternal(fmt.Sprintf("journey=%q", journeyID)))
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
		tr, errs := r.RunJourneyCheckRef(ctx, j, ref)
		result.Results = append(result.Results, tr)
		result.Errors = append(result.Errors, errs...)
		if !tr.Passed || len(errs) > 0 {
			result.Passed = false
		}
	}
	return result, nil
}

// RunActiveJourneys runs every active journey in the parsed project.
func (r *Runner) RunActiveJourneys(ctx context.Context) (*VerifyResult, error) {
	result := &VerifyResult{TargetID: "active journeys", Passed: true}
	if r.project == nil {
		return result, nil
	}
	for _, id := range sortedJourneyIDs(r.project.Journeys) {
		j := r.project.Journeys[id]
		if j.Lifecycle != "active" {
			continue
		}
		jr, err := r.RunJourney(ctx, j.ID)
		if err != nil {
			result.Errors = append(result.Errors, err)
			result.Results = append(result.Results, TestResult{Name: j.ID, Passed: false})
			result.Passed = false
			continue
		}
		result.Results = append(result.Results, jr.Results...)
		result.Errors = append(result.Errors, jr.Errors...)
		result.ManualPending = append(result.ManualPending, jr.ManualPending...)
		if !hasAutoCheckRef(j) {
			result.Results = append(result.Results, TestResult{
				Name:   j.ID,
				Passed: false,
				Output: "active journey has no auto checkRef — automated verification required",
			})
			result.Passed = false
		}
		if !jr.Passed || len(jr.Errors) > 0 {
			result.Passed = false
		}
	}
	return result, nil
}

func hasAutoCheckRef(j *metadata.JourneyMeta) bool {
	for _, pc := range j.PassCriteria {
		if pc.Mode == ModeAuto && strings.TrimSpace(pc.CheckRef) != "" {
			return true
		}
	}
	return false
}

func sortedJourneyIDs(journeys map[string]*metadata.JourneyMeta) []string {
	ids := make([]string, 0, len(journeys))
	for id := range journeys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// RunJourneyCheckRef executes one journey checkRef using the same resolver as
// RunJourney. Governance strict mode calls this so promotion gates and runtime
// verification share the exact same target binding.
func (r *Runner) RunJourneyCheckRef(ctx context.Context, j *metadata.JourneyMeta, ref string) (TestResult, []error) {
	targetID := ref
	if j != nil {
		targetID = j.ID
	}
	result := &VerifyResult{TargetID: targetID, Passed: true}
	resolved, err := resolveRef(ref)
	if err != nil {
		return TestResult{Name: ref, Passed: false}, []error{err}
	}
	if resolved.Kind != PrefixJourney {
		return TestResult{Name: ref, Passed: false}, []error{errcode.New(errcode.KindInvalid, errcode.ErrCheckRefInvalid,
			"journey checkRef must use journey prefix",
			errcode.WithInternal(fmt.Sprintf("ref=%q", ref)))}
	}
	if j != nil && resolved.Scope != j.ID {
		return TestResult{Name: ref, Passed: false}, []error{errcode.New(errcode.KindInvalid, errcode.ErrCheckRefInvalid,
			"journey checkRef belongs to a different journey",
			errcode.WithInternal(fmt.Sprintf("ref=%q scope=%q journey=%q", ref, resolved.Scope, j.ID)))}
	}
	pkg, extraArgs := r.resolveJourneyPkg(j, resolved)
	args := append([]string{pkg, "-v", "-run", resolved.RunPattern}, extraArgs...)
	res := r.runGoTest(ctx, r.root, args)
	recordResult(result, ref, res, pkg, resolved.RunPattern)
	if len(result.Results) == 0 {
		return TestResult{Name: ref, Passed: false}, result.Errors
	}
	return result.Results[0], result.Errors
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
		res := r.runGoTest(ctx, r.root, []string{pkg, "-v", "-run", resolved.RunPattern})
		recordResult(result, ref, res, pkg, resolved.RunPattern)
	}
}

// recordResult appends a goTestResult to the VerifyResult, handling ZeroMatch
// and error propagation in a single place.
func recordResult(result *VerifyResult, name string, res goTestResult, pkg, pattern string) {
	tr := TestResult{
		Name:        name,
		Passed:      res.Passed,
		Output:      res.Output,
		ZeroMatch:   res.ZeroMatch,
		SkippedOnly: res.SkippedOnly,
	}
	if res.ZeroMatch {
		tr.Passed = false
		if pattern != "" {
			result.Errors = append(result.Errors, errcode.New(errcode.KindNotFound, errcode.ErrZeroTestMatch,
				"pattern matched no tests — check your YAML ref",
				errcode.WithInternal(fmt.Sprintf("pattern=%q pkg=%s", pattern, pkg))))
		} else {
			result.Errors = append(result.Errors, errcode.New(errcode.KindNotFound, errcode.ErrZeroTestMatch,
				"matched no tests",
				errcode.WithInternal(fmt.Sprintf("pkg=%s", pkg))))
		}
	}
	if res.SkippedOnly {
		tr.Passed = false
		if pattern != "" {
			result.Errors = append(result.Errors, errcode.New(errcode.KindNotFound, errcode.ErrZeroTestMatch,
				"pattern matched only skipped tests — replace stubs with executable checks",
				errcode.WithInternal(fmt.Sprintf("pattern=%q pkg=%s", pattern, pkg))))
		} else {
			result.Errors = append(result.Errors, errcode.New(errcode.KindNotFound, errcode.ErrZeroTestMatch,
				"matched only skipped tests",
				errcode.WithInternal(fmt.Sprintf("pkg=%s", pkg))))
		}
	}
	result.Results = append(result.Results, tr)
	if !tr.Passed {
		result.Passed = false
	}
	if res.Err != nil {
		result.Errors = append(result.Errors, res.Err)
	}
}

// resolveJourneyPkg determines the Go test package and extra args for a journey
// ref. Example-local journey files run against their owning example tree;
// project-level journeys still prefer ./tests/integration/... with integration
// tags, then ./journeys/..., then ./... as last resort.
func (r *Runner) resolveJourneyPkg(j *metadata.JourneyMeta, ref resolvedRef) (pkg string, extraArgs []string) {
	if ref.Pkg != "" {
		return ref.Pkg, nil
	}
	if j != nil {
		if exampleName, ok := exampleNameFromJourneyFile(j.File); ok {
			if dirExists(filepath.Join(r.root, "examples", exampleName)) {
				return fmt.Sprintf("./examples/%s/...", exampleName), nil
			}
		}
	}
	if dirExists(filepath.Join(r.root, "tests", "integration")) {
		return "./tests/integration/...", []string{"-tags=integration"}
	}
	if dirExists(filepath.Join(r.root, "journeys")) {
		return "./journeys/...", nil
	}
	return "./...", nil
}

func exampleNameFromJourneyFile(file string) (string, bool) {
	parts := strings.Split(filepath.ToSlash(file), "/")
	if len(parts) < 4 {
		return "", false
	}
	if parts[0] != "examples" || parts[1] == "" || parts[2] != "journeys" {
		return "", false
	}
	return parts[1], true
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
		return "", "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"invalid slice key: expected format \"cellID/sliceID\"",
			errcode.WithInternal(fmt.Sprintf("key=%q", key)))
	}
	if strings.Contains(parts[0], "..") || strings.ContainsAny(parts[0], `/\`) {
		return "", "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "invalid cellID: contains path separator or traversal")
	}
	if strings.Contains(parts[1], "..") || strings.ContainsAny(parts[1], `/\`) {
		return "", "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "invalid sliceID: contains path separator or traversal")
	}
	return parts[0], parts[1], nil
}

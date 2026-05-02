package app

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/cmd/gocell/app/printers"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/registry"
	"github.com/ghbvf/gocell/tools/nogo/unconditionalskip"
)

// checkL0NonL0SkipMsg is the message printed when --cell targets a non-L0 cell.
// Extracted as a constant so tests can assert against it without brittle substring matching.
// Format args: (cellID, consistencyLevel).
const checkL0NonL0SkipMsg = "cell %q is %s (not L0); l0-imports check skipped"

const (
	cmdSliceCoverage      = "slice-coverage"
	cmdJourneyReadiness   = "journey-readiness"
	cmdL0Imports          = "l0-imports"
	errCannotFindRoot     = "cannot find project root: %w"
	errMetadataParse      = "metadata parse: %w"
	flagFormatDescription = "output format: text|json|sarif"
	flagSetCheckPrefix    = "check "
)

// runCheck implements:
//
//	gocell check contract-health [--format text|json|sarif]
//	gocell check slice-coverage --cell=<cellID>
//	gocell check assembly-completeness --id=<assemblyID>
//	gocell check journey-readiness --journey=<journeyID>
//	gocell check l0-imports --cell=<cellID>
//	gocell check unconditional-skip [--format text|json|sarif]
func runCheck(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gocell check <contract-health|slice-coverage|assembly-completeness" +
			"|journey-readiness|l0-imports|unconditional-skip> [flags]")
	}
	if isHelpFlag(args[0]) {
		return printCheckHelp()
	}

	subtype := args[0]
	subArgs := args[1:]

	switch subtype {
	case "contract-health":
		return checkContractHealth(subArgs)
	case cmdSliceCoverage:
		return checkSliceCoverage(subArgs)
	case "assembly-completeness":
		return checkAssemblyCompleteness(subArgs)
	case cmdJourneyReadiness:
		return checkJourneyReadiness(subArgs)
	case cmdL0Imports:
		return checkL0Imports(subArgs)
	case "unconditional-skip":
		return checkUnconditionalSkip(subArgs)
	default:
		return fmt.Errorf("unknown check type: %s", subtype)
	}
}

func checkContractHealth(args []string) error {
	fs := flag.NewFlagSet("check contract-health", flag.ContinueOnError)
	format := fs.String("format", string(printers.FormatText),
		"output format: text (non-stable, default) | json | sarif")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := findRoot()
	if err != nil {
		return fmt.Errorf(errCannotFindRoot, err)
	}

	parser := metadata.NewParser(root)
	project, err := parser.Parse()
	if err != nil {
		return fmt.Errorf(errMetadataParse, err)
	}

	reg := registry.NewContractRegistry(project)
	ids := reg.AllIDs()

	contracts := make([]*metadata.ContractMeta, 0, len(ids))
	for _, id := range ids {
		contracts = append(contracts, reg.Get(id))
	}

	printer, err := printers.New(*format, os.Stdout, toolVersion())
	if err != nil {
		return err
	}

	// Text mode keeps the human-readable summary table on top of the issues
	// list — JSON / SARIF consumers don't want it because it's not a
	// finding (it's just descriptive metadata).
	if *format == string(printers.FormatText) {
		printContractHealthTable(contracts)
	}

	validator := governance.NewValidator(project, root, clock.Real())
	results := validator.CheckContractHealth(contracts)
	results = append(results, validator.CheckHTTPResponseAlignment(contracts, root)...)
	results = append(results, validator.CheckHTTPPathParamUUID(contracts, root)...)

	if err := printer.Print(results); err != nil {
		return fmt.Errorf(errEmitResultsFmt, err)
	}

	if errCount := countContractHealthErrors(results); errCount > 0 {
		return fmt.Errorf("contract-health: %d issue(s) found", errCount)
	}
	if *format == string(printers.FormatText) && len(contracts) > 0 {
		fmt.Println("\nPASS: all contracts healthy")
	}
	return nil
}

// printContractHealthTable renders the human-readable summary of contracts.
// Columns include METHOD and PATH for HTTP contracts (PR239-OB1) so the
// table conveys transport-level metadata at a glance — previously a
// dashboard could not tell from this output whether HTTP contracts had a
// concrete method/path declared.
//
// Non-HTTP contracts render "-" in METHOD/PATH so column widths stay stable.
func printContractHealthTable(contracts []*metadata.ContractMeta) {
	if len(contracts) == 0 {
		fmt.Println("No contracts found.")
		return
	}

	// Single format string drives header, separator, and every data row so
	// column widths stay aligned in one place.
	const rowFormat = "  %-40s %-12s %-12s %-22s %-7s %s\n"

	fmt.Printf("Contract Health (%d contracts):\n\n", len(contracts))
	fmt.Printf(rowFormat, "ID", "KIND", "LIFECYCLE", "OWNER", "METHOD", "PATH")
	fmt.Printf(rowFormat, "---", "----", "---------", "-----", "------", "----")

	for _, c := range contracts {
		lifecycle := c.Lifecycle
		if lifecycle == "" {
			lifecycle = "(unset)"
		}
		method, path := httpTransportColumns(c)
		fmt.Printf(rowFormat, c.ID, c.Kind, lifecycle, c.OwnerCell, method, path)
	}
}

// httpTransportColumns extracts the method/path pair for the table view.
// Non-HTTP contracts get "-" placeholders; HTTP contracts with a missing
// method or path also use "-" so the absence is visible (rather than an
// empty cell that looks like a render glitch).
func httpTransportColumns(c *metadata.ContractMeta) (method, path string) {
	if c.Kind != "http" || c.Endpoints.HTTP == nil {
		return "-", "-"
	}
	method = c.Endpoints.HTTP.Method
	if method == "" {
		method = "-"
	}
	path = c.Endpoints.HTTP.Path
	if path == "" {
		path = "-"
	}
	return method, path
}

// countContractHealthErrors counts SeverityError findings — currently every
// contract-health rule emits an error, but the helper keeps us safe if we
// later add advisory warnings.
// Delegates to countErrors to avoid duplicate logic.
func countContractHealthErrors(results []governance.ValidationResult) int {
	return countErrors(results)
}

// availableCellsMsg builds the "available cells: [...]" fragment for error messages.
// Returns at most 10 sorted cell IDs, appending ", ..." when there are more.
func availableCellsMsg(cells map[string]*metadata.CellMeta) string {
	ids := make([]string, 0, len(cells))
	for id := range cells {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	suffix := ""
	if len(ids) > 10 {
		ids = ids[:10]
		suffix = ", ..."
	}
	return "[" + strings.Join(ids, ", ") + suffix + "]"
}

// checkSliceCoverage checks that every slices/ subdirectory has a slice.yaml
// and that each parsed SliceMeta correctly references its parent cell.
//
// Flags: --cell=<cellID> (empty = all cells), --format=<text|json|sarif>.
func checkSliceCoverage(args []string) error {
	fs := flag.NewFlagSet(flagSetCheckPrefix+cmdSliceCoverage, flag.ContinueOnError)
	cellID := fs.String("cell", "", "restrict check to this cell ID (empty = all cells)")
	format := fs.String("format", string(printers.FormatText), flagFormatDescription)
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := findRoot()
	if err != nil {
		return fmt.Errorf(errCannotFindRoot, err)
	}

	parser := metadata.NewParser(root)
	project, err := parser.Parse()
	if err != nil {
		return fmt.Errorf(errMetadataParse, err)
	}

	if *cellID != "" {
		if _, ok := project.Cells[*cellID]; !ok {
			return printAndCheck(*format, []governance.ValidationResult{{
				Code:      "CHECK-CELL-NOT-FOUND",
				Severity:  governance.SeverityError,
				IssueType: governance.IssueRequired,
				Scope:     cmdSliceCoverage,
				Message:   fmt.Sprintf("cell %q not found in project; available cells: %s", *cellID, availableCellsMsg(project.Cells)),
			}}, cmdSliceCoverage, "")
		}
	}

	var results []governance.ValidationResult
	cellCount := 0
	sliceCount := 0
	if *cellID != "" {
		cellCount = 1
		// Count slices for this cell.
		for _, sl := range project.Slices {
			if sl.BelongsToCell == *cellID {
				sliceCount++
			}
		}
		results = sliceCoverageForCell(root, project, *cellID)
	} else {
		cellCount = len(project.Cells)
		sliceCount = len(project.Slices)
		for cid := range project.Cells {
			results = append(results, sliceCoverageForCell(root, project, cid)...)
		}
	}

	return printAndCheck(*format, results, cmdSliceCoverage,
		fmt.Sprintf("PASS: slice coverage OK (checked %d slices across %d cells)", sliceCount, cellCount))
}

// sliceCoverageForCell runs the slice-coverage checks for a single cell.
func sliceCoverageForCell(root string, project *metadata.ProjectMeta, cid string) []governance.ValidationResult {
	var results []governance.ValidationResult

	results = append(results, sliceDirCheck(root, cid)...)
	results = append(results, sliceMetaCheck(project, cid)...)
	return results
}

// sliceDirCheck verifies every subdir under cells/<cid>/slices/ has a slice.yaml.
func sliceDirCheck(root, cid string) []governance.ValidationResult {
	var results []governance.ValidationResult
	slicesDir := filepath.Join(root, "cells", cid, "slices")
	entries, err := os.ReadDir(slicesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // cell has no slices/ subdir — not a violation
		}
		return []governance.ValidationResult{{
			Code:      "CHECK-SLICE-DIR-READ-ERROR",
			Severity:  governance.SeverityError,
			IssueType: governance.IssueInvalid,
			File:      filepath.ToSlash(filepath.Join("cells", cid, "slices")),
			Message:   fmt.Sprintf("cannot read slices dir for cell %q: %v", cid, err),
		}}
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sliceYAML := filepath.Join(slicesDir, e.Name(), "slice.yaml")
		if _, statErr := os.Stat(sliceYAML); statErr != nil {
			results = append(results, governance.ValidationResult{
				Code:      "CHECK-SLICE-EMPTY-DIR",
				Severity:  governance.SeverityError,
				IssueType: governance.IssueRequired,
				Scope:     cmdSliceCoverage,
				Message:   fmt.Sprintf("cell %q: slices/%s has no slice.yaml", cid, e.Name()),
			})
		}
	}
	return results
}

// sliceMetaCheck verifies parsed SliceMeta consistency for a single cell.
func sliceMetaCheck(project *metadata.ProjectMeta, cid string) []governance.ValidationResult {
	cellMeta, ok := project.Cells[cid]
	if !ok {
		return nil
	}
	// The canonical parent directory for this cell's slices is derived from the
	// cell's own file path (e.g. "cells/accesscore/cell.yaml" → "cells/accesscore/slices").
	// This handles both top-level cells and cells nested under examples/.
	cellDir := filepath.Dir(cellMeta.File)
	expectedSlicesParent := filepath.ToSlash(filepath.Join(cellDir, "slices"))

	var results []governance.ValidationResult
	for _, sl := range project.Slices {
		if sl.BelongsToCell != cid {
			continue
		}
		actualParent := filepath.ToSlash(filepath.Dir(sl.File))
		if !strings.HasPrefix(actualParent, expectedSlicesParent) {
			results = append(results, governance.ValidationResult{
				Code:      "CHECK-SLICE-BELONGS-TO-MISMATCH",
				Severity:  governance.SeverityError,
				IssueType: governance.IssueMismatch,
				File:      sl.File,
				Message: fmt.Sprintf(
					"slice %q has belongsToCell=%q but lives under %q (expected under %q)",
					sl.ID, sl.BelongsToCell, actualParent, expectedSlicesParent,
				),
			})
		}
		if sl.Dir != sl.ID {
			results = append(results, governance.ValidationResult{
				Code:      "CHECK-SLICE-ID-MISMATCH",
				Severity:  governance.SeverityError,
				IssueType: governance.IssueMismatch,
				File:      sl.File,
				Message:   fmt.Sprintf("slice %q: directory name %q does not match slice id %q", sl.ID, sl.Dir, sl.ID),
			})
		}
	}
	return results
}

// checkAssemblyCompleteness verifies that all cells declared in an assembly
// exist in the parsed project metadata and that there are no duplicate entries.
//
// Flags: --id=<assemblyID> (required).
func checkAssemblyCompleteness(args []string) error {
	fs := flag.NewFlagSet("check assembly-completeness", flag.ContinueOnError)
	id := fs.String("id", "", "assembly ID (required)")
	format := fs.String("format", string(printers.FormatText), flagFormatDescription)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("--id is required")
	}

	root, err := findRoot()
	if err != nil {
		return fmt.Errorf(errCannotFindRoot, err)
	}

	parser := metadata.NewParser(root)
	project, err := parser.Parse()
	if err != nil {
		return fmt.Errorf(errMetadataParse, err)
	}

	asm, ok := project.Assemblies[*id]
	if !ok {
		return fmt.Errorf("assembly %q not found in project metadata", *id)
	}

	var results []governance.ValidationResult

	seen := make(map[string]bool, len(asm.Cells))
	for _, cid := range asm.Cells {
		if seen[cid] {
			results = append(results, governance.ValidationResult{
				Code:      "CHECK-ASSEMBLY-DUPLICATE-CELL",
				Severity:  governance.SeverityError,
				IssueType: governance.IssueDuplicate,
				File:      asm.File,
				Message:   fmt.Sprintf("assembly %q: cell %q declared more than once", *id, cid),
			})
			continue
		}
		seen[cid] = true
		if _, exists := project.Cells[cid]; !exists {
			results = append(results, governance.ValidationResult{
				Code:      "CHECK-ASSEMBLY-MISSING-CELL",
				Severity:  governance.SeverityError,
				IssueType: governance.IssueRefNotFound,
				File:      asm.File,
				Message:   fmt.Sprintf("assembly %q: cell %q not found in project metadata", *id, cid),
			})
		}
	}

	return printAndCheck(*format, results, "assembly-completeness",
		fmt.Sprintf("PASS: assembly %q complete", *id))
}

// checkJourneyReadiness verifies that each journey has exactly one status-board
// entry and that all referenced contracts and cells exist in the project.
//
// Flags: --journey=<journeyID> (empty = all journeys).
func checkJourneyReadiness(args []string) error {
	fs := flag.NewFlagSet(flagSetCheckPrefix+cmdJourneyReadiness, flag.ContinueOnError)
	journeyID := fs.String("journey", "", "restrict check to this journey ID (empty = all)")
	format := fs.String("format", string(printers.FormatText), flagFormatDescription)
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := findRoot()
	if err != nil {
		return fmt.Errorf(errCannotFindRoot, err)
	}

	parser := metadata.NewParser(root)
	project, err := parser.Parse()
	if err != nil {
		return fmt.Errorf(errMetadataParse, err)
	}

	statusCount := buildStatusCount(project)

	var results []governance.ValidationResult
	journeyCount := 0
	if *journeyID != "" {
		jm, ok := project.Journeys[*journeyID]
		if !ok {
			return fmt.Errorf("journey %q not found", *journeyID)
		}
		journeyCount = 1
		results = journeyReadinessFor(jm, project, statusCount)
	} else {
		journeyCount = len(project.Journeys)
		for _, jm := range project.Journeys {
			results = append(results, journeyReadinessFor(jm, project, statusCount)...)
		}
	}

	return printAndCheck(*format, results, cmdJourneyReadiness,
		fmt.Sprintf("PASS: journey readiness OK (checked %d journeys)", journeyCount))
}

// buildStatusCount builds a map of journeyID → count of status-board entries.
func buildStatusCount(project *metadata.ProjectMeta) map[string]int {
	counts := make(map[string]int, len(project.StatusBoard))
	for _, e := range project.StatusBoard {
		counts[e.JourneyID]++
	}
	return counts
}

// journeyReadinessFor checks a single journey's readiness.
func journeyReadinessFor(
	jm *metadata.JourneyMeta, project *metadata.ProjectMeta, statusCount map[string]int,
) []governance.ValidationResult {
	var results []governance.ValidationResult

	results = append(results, journeyStatusCheck(jm, statusCount)...)
	results = append(results, journeyContractCheck(jm, project)...)
	results = append(results, journeyCellCheck(jm, project)...)
	return results
}

// journeyStatusCheck validates the status-board entry count for a journey.
func journeyStatusCheck(jm *metadata.JourneyMeta, statusCount map[string]int) []governance.ValidationResult {
	count := statusCount[jm.ID]
	switch {
	case count == 0:
		return []governance.ValidationResult{{
			Code:      "CHECK-JOURNEY-NO-STATUS-ENTRY",
			Severity:  governance.SeverityError,
			IssueType: governance.IssueRequired,
			File:      jm.File,
			Message:   fmt.Sprintf("journey %q has no entry in status-board.yaml", jm.ID),
		}}
	case count > 1:
		return []governance.ValidationResult{{
			Code:      "CHECK-JOURNEY-DUP-STATUS-ENTRY",
			Severity:  governance.SeverityError,
			IssueType: governance.IssueDuplicate,
			Scope:     cmdJourneyReadiness,
			Message:   fmt.Sprintf("journey %q has %d entries in status-board.yaml (expected 1)", jm.ID, count),
		}}
	}
	return nil
}

// journeyContractCheck validates contract references for a journey.
func journeyContractCheck(jm *metadata.JourneyMeta, project *metadata.ProjectMeta) []governance.ValidationResult {
	var results []governance.ValidationResult
	for _, contractID := range jm.Contracts {
		if _, exists := project.Contracts[contractID]; !exists {
			results = append(results, governance.ValidationResult{
				Code:      "CHECK-JOURNEY-MISSING-CONTRACT",
				Severity:  governance.SeverityError,
				IssueType: governance.IssueRefNotFound,
				File:      jm.File,
				Message:   fmt.Sprintf("journey %q references unknown contract %q", jm.ID, contractID),
			})
		}
	}
	return results
}

// journeyCellCheck validates cell references for a journey.
func journeyCellCheck(jm *metadata.JourneyMeta, project *metadata.ProjectMeta) []governance.ValidationResult {
	var results []governance.ValidationResult
	for _, cid := range jm.Cells {
		if _, exists := project.Cells[cid]; !exists {
			results = append(results, governance.ValidationResult{
				Code:      "CHECK-JOURNEY-MISSING-CELL",
				Severity:  governance.SeverityError,
				IssueType: governance.IssueRefNotFound,
				File:      jm.File,
				Message:   fmt.Sprintf("journey %q references unknown cell %q", jm.ID, cid),
			})
		}
	}
	return results
}

// checkL0Imports verifies that L0 cells only import the L0 cell dependencies
// they declare in cell.yaml::l0Dependencies, and vice versa.
//
// Flags: --cell=<cellID> (empty = all L0 cells).
func checkL0Imports(args []string) error {
	fs := flag.NewFlagSet(flagSetCheckPrefix+cmdL0Imports, flag.ContinueOnError)
	cellID := fs.String("cell", "", "restrict check to this cell ID (empty = all L0 cells)")
	format := fs.String("format", string(printers.FormatText), flagFormatDescription)
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := findRoot()
	if err != nil {
		return fmt.Errorf(errCannotFindRoot, err)
	}

	parser := metadata.NewParser(root)
	project, err := parser.Parse()
	if err != nil {
		return fmt.Errorf(errMetadataParse, err)
	}

	if *cellID != "" {
		return checkL0ImportsForSingleCell(root, project, *cellID, *format)
	}
	return checkL0ImportsForAllCells(root, project, *format)
}

// checkL0ImportsForSingleCell runs l0-imports for a single named cell.
// Non-L0 cells are silently skipped in all formats — json/sarif consumers
// receive an empty results array ({"results":[]}) instead of no output at all.
func checkL0ImportsForSingleCell(root string, project *metadata.ProjectMeta, cellID, format string) error {
	cm, ok := project.Cells[cellID]
	if !ok {
		return fmt.Errorf("cell %q not found in project metadata", cellID)
	}
	if cm.ConsistencyLevel != "L0" {
		if format == string(printers.FormatText) {
			fmt.Printf(checkL0NonL0SkipMsg+"\n", cellID, cm.ConsistencyLevel)
			return nil
		}
		// json/sarif: emit an empty results document so machine consumers see
		// "ran but found nothing" rather than silence.
		return printAndCheck(format, nil, cmdL0Imports, "")
	}
	results := l0ImportsForCell(root, cm)
	return printAndCheck(format, results, cmdL0Imports, "PASS: L0 import declarations OK (checked 1 L0 cells)")
}

// checkL0ImportsForAllCells runs l0-imports across all L0 cells in the project.
// Zero L0 cells case is handled uniformly: text prints a message, json/sarif
// emit an empty results document so machine consumers see a valid empty run.
func checkL0ImportsForAllCells(root string, project *metadata.ProjectMeta, format string) error {
	var results []governance.ValidationResult
	l0Count := 0
	for _, cm := range project.Cells {
		if cm.ConsistencyLevel == "L0" {
			l0Count++
			results = append(results, l0ImportsForCell(root, cm)...)
		}
	}
	if l0Count == 0 {
		if format == string(printers.FormatText) {
			fmt.Println("checked 0 L0 cells, 0 issues")
			return nil
		}
		// json/sarif: emit empty results so machine consumers see a valid empty run.
		return printAndCheck(format, nil, cmdL0Imports, "")
	}
	return printAndCheck(format, results, cmdL0Imports,
		fmt.Sprintf("PASS: L0 import declarations OK (checked %d L0 cells)", l0Count))
}

// l0ImportsForCell runs all L0 import checks for a single cell.
func l0ImportsForCell(root string, cm *metadata.CellMeta) []governance.ValidationResult {
	declaredDeps := buildDeclaredDeps(cm)
	var results []governance.ValidationResult

	if len(declaredDeps) == 0 {
		results = append(results, governance.ValidationResult{
			Code:      "CHECK-L0-MISSING-L0DEPS",
			Severity:  governance.SeverityError,
			IssueType: governance.IssueRequired,
			File:      cm.File,
			Message:   fmt.Sprintf("L0 cell %q has no l0Dependencies declared in cell.yaml", cm.ID),
		})
	}

	imported, loadResults, fatalLoad := loadCellImports(root, cm)
	results = append(results, loadResults...)
	if fatalLoad {
		return results // packages.Load failed; skip diff checks
	}

	// Fix 2.5: if no l0Dependencies declared, skip undeclared/dangling checks
	// to avoid noisy false positives.
	if len(declaredDeps) == 0 {
		return results
	}

	results = append(results, l0UndeclaredImports(cm, imported, declaredDeps)...)
	results = append(results, l0DanglingDeclarations(cm, imported, declaredDeps)...)
	return results
}

// buildDeclaredDeps builds a set of declared L0 dependency cell IDs.
func buildDeclaredDeps(cm *metadata.CellMeta) map[string]bool {
	deps := make(map[string]bool, len(cm.L0Dependencies))
	for _, dep := range cm.L0Dependencies {
		deps[dep.Cell] = true
	}
	return deps
}

// loadCellImports loads packages in the cell directory and returns the set of
// imported sibling cell IDs. The third return value is true when packages.Load
// itself fails (fatal — no import data available); per-package errors are
// returned as error ValidationResults but the import map is still populated.
//
// Cell directory is derived from cm.File (the actual parsed cell.yaml path),
// not from "cells/<cm.ID>", so cells under examples/**/cells/ are also resolved
// correctly. This mirrors sliceMetaCheck which already uses cellMeta.File.
func loadCellImports(root string, cm *metadata.CellMeta) (map[string]bool, []governance.ValidationResult, bool) {
	const cellsImportPrefix = "github.com/ghbvf/gocell/cells/"
	cellDir := filepath.Dir(filepath.FromSlash(cm.File))
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedImports,
		Dir:  filepath.Join(root, cellDir),
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, []governance.ValidationResult{{
			Code:      "CHECK-L0-LOAD-ERROR",
			Severity:  governance.SeverityError,
			IssueType: governance.IssueInvalid,
			File:      filepath.ToSlash(cm.File),
			Scope:     cmdL0Imports,
			Message:   fmt.Sprintf("packages.Load failed for cell %q: %v", cm.ID, err),
		}}, true
	}

	var loadErrs []governance.ValidationResult
	imported := make(map[string]bool)
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			for _, pe := range pkg.Errors {
				loadErrs = append(loadErrs, governance.ValidationResult{
					Code:      "CHECK-L0-LOAD-ERROR",
					Severity:  governance.SeverityError,
					IssueType: governance.IssueInvalid,
					File:      filepath.ToSlash(cm.File),
					Scope:     cmdL0Imports,
					Message:   fmt.Sprintf("packages.Load error for cell %q package %q: %v", cm.ID, pkg.PkgPath, pe),
				})
			}
		}
		for importPath := range pkg.Imports {
			after, ok := strings.CutPrefix(importPath, cellsImportPrefix)
			if !ok {
				continue
			}
			importedCellID := strings.SplitN(after, "/", 2)[0]
			if importedCellID != cm.ID {
				imported[importedCellID] = true
			}
		}
	}
	return imported, loadErrs, false
}

// l0UndeclaredImports finds imported cells not declared as L0 dependencies.
func l0UndeclaredImports(cm *metadata.CellMeta, imported, declared map[string]bool) []governance.ValidationResult {
	var results []governance.ValidationResult
	for importedCellID := range imported {
		if !declared[importedCellID] {
			results = append(results, governance.ValidationResult{
				Code:      "CHECK-L0-UNDECLARED-IMPORT",
				Severity:  governance.SeverityError,
				IssueType: governance.IssueInvalid,
				File:      cm.File,
				Message:   fmt.Sprintf("L0 cell %q imports cell %q but does not declare it in l0Dependencies", cm.ID, importedCellID),
			})
		}
	}
	return results
}

// l0DanglingDeclarations finds declared L0 deps that are not actually imported.
func l0DanglingDeclarations(cm *metadata.CellMeta, imported, declared map[string]bool) []governance.ValidationResult {
	var results []governance.ValidationResult
	for declaredCellID := range declared {
		if !imported[declaredCellID] {
			results = append(results, governance.ValidationResult{
				Code:      "CHECK-L0-DANGLING-DECLARATION",
				Severity:  governance.SeverityError,
				IssueType: governance.IssueInvalid,
				File:      cm.File,
				Message:   fmt.Sprintf("L0 cell %q declares l0Dependency %q but does not import it", cm.ID, declaredCellID),
			})
		}
	}
	return results
}

// countErrors counts SeverityError findings in a result set.
func countErrors(results []governance.ValidationResult) int {
	n := 0
	for i := range results {
		if results[i].Severity == governance.SeverityError {
			n++
		}
	}
	return n
}

// printAndCheck emits results through a text-mode printer and returns an error
// if any SeverityError findings exist. passMsg is only printed in text mode
// when all checks pass.
func printAndCheck(format string, results []governance.ValidationResult, checkName, passMsg string) error {
	printer, err := printers.New(format, os.Stdout, toolVersion())
	if err != nil {
		return err
	}
	if err := printer.Print(results); err != nil {
		return fmt.Errorf(errEmitResultsFmt, err)
	}
	if n := countErrors(results); n > 0 {
		return fmt.Errorf("%s: %d issue(s) found", checkName, n)
	}
	if format == string(printers.FormatText) {
		fmt.Println(passMsg)
	}
	return nil
}

// checkUnconditionalSkip implements `gocell check unconditional-skip`.
//
// It loads packages matching the given patterns (default: "./..."), runs the
// unconditionalskip analyzer over them, and renders the diagnostics as
// governance.ValidationResult entries using the configured output format.
//
// Exit behavior mirrors checkContractHealth: a non-zero error is returned
// when one or more SeverityError findings are emitted, so CI callers can
// gate on the exit code without parsing the output format.
func checkUnconditionalSkip(args []string) error {
	const defaultPattern = "./..."
	fs := flag.NewFlagSet("check unconditional-skip", flag.ContinueOnError)
	format := fs.String("format", string(printers.FormatText),
		"output format: text (default) | json | sarif")
	if err := fs.Parse(args); err != nil {
		return err
	}

	patterns := fs.Args()
	if len(patterns) == 0 {
		patterns = []string{defaultPattern}
	}

	// Resolve project root so diagnostic file paths can be made repo-relative
	// for SARIF SRCROOT mapping (artifactLocation.uri must be relative when
	// uriBaseId="SRCROOT", per PR#270 SARIF SRCROOT contract).
	root, err := findRoot()
	if err != nil {
		return fmt.Errorf("cannot find project root: %w", err)
	}

	printer, err := printers.New(*format, os.Stdout, toolVersion())
	if err != nil {
		return err
	}

	if *format == string(printers.FormatText) {
		// Scope hint: scan boundary is the project root, not the user's CWD.
		// Avoids the trap where a sub-tree invocation emits "PASS" the user
		// reads as a repo-wide pass.
		fmt.Printf("Scanned scope: %s (patterns=%v)\n", root, patterns)
	}

	results, err := runUnconditionalSkipAnalyzer(patterns, root)
	if err != nil {
		return err
	}

	if err := printer.Print(results); err != nil {
		return fmt.Errorf(errEmitResultsFmt, err)
	}

	errCount := countContractHealthErrors(results)
	if errCount > 0 {
		return fmt.Errorf("unconditional-skip: %d issue(s) found", errCount)
	}
	if *format == string(printers.FormatText) {
		fmt.Println("\nPASS: no unconditional skips found")
	}
	return nil
}

// runUnconditionalSkipAnalyzer loads patterns, runs the analyzer, and
// returns governance ValidationResult entries with repo-relative file
// paths suitable for SARIF SRCROOT mapping.
//
// Pinning Config.Dir to the project root is what makes "./..." a
// repo-wide scan regardless of where the user invoked the CLI. Without
// it, packages.Load resolves relative patterns against the current
// working directory and silently emits "PASS" for a sub-tree scan when
// the user runs the command from inside one cell.
func runUnconditionalSkipAnalyzer(patterns []string, root string) ([]governance.ValidationResult, error) {
	// packages.LoadAllSyntax loads type-annotated syntax for initial packages
	// and all transitive dependencies — the minimum mode checker.Analyze needs.
	// BuildFlags includes the build tags used by integration, e2e, and smoke
	// test files so the analyzer sees those files instead of silently skipping
	// them — without this, //go:build integration test files are invisible and
	// unconditional t.Skip calls inside them are never reported.
	cfg := &packages.Config{
		Mode:       packages.LoadAllSyntax,
		Tests:      true,
		Dir:        root,
		BuildFlags: []string{"-tags=integration,e2e,examples_smoke"},
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("load packages: %w", err)
	}
	if loadErr := collectPackageErrors(pkgs); loadErr != nil {
		return nil, loadErr
	}

	graph, err := checker.Analyze(
		[]*analysis.Analyzer{unconditionalskip.Analyzer},
		pkgs,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("run analyzer: %w", err)
	}

	var results []governance.ValidationResult
	for act := range graph.All() {
		for _, diag := range act.Diagnostics {
			pos := act.Package.Fset.Position(diag.Pos)
			results = append(results, governance.ValidationResult{
				Code:      "UNCONDITIONAL-SKIP-01",
				Severity:  governance.SeverityError,
				IssueType: governance.IssueForbidden,
				File:      relativeToRoot(root, pos.Filename),
				Line:      pos.Line,
				Column:    pos.Column,
				Message:   diag.Message,
			})
		}
	}
	return results, nil
}

// collectPackageErrors aggregates per-package load errors into a single
// structured error. Returning a non-nil error suppresses analyzer execution
// — diagnostics on a partially-loaded graph would be incomplete.
func collectPackageErrors(pkgs []*packages.Package) error {
	var pkgErrs []packages.Error
	for _, p := range pkgs {
		pkgErrs = append(pkgErrs, p.Errors...)
	}
	if len(pkgErrs) == 0 {
		return nil
	}
	var b strings.Builder
	for _, e := range pkgErrs {
		fmt.Fprintf(&b, "  %s\n", e.Error())
	}
	return fmt.Errorf("package load errors:\n%s", b.String())
}

// relativeToRoot converts an absolute file path returned by go/packages
// (token.Position.Filename) into a slash-separated path relative to the
// project root. Required so SARIF artifactLocation.uri stays repo-relative
// under uriBaseId="SRCROOT" — GitHub Code Scanning silently drops findings
// whose URI doesn't resolve under the declared base.
//
// Falls back to the original path on any failure (filepath.Rel error or
// unrelated path) so the printer never crashes; SARIF emit is best-effort
// and a degraded absolute path is preferable to a panic.
func relativeToRoot(root, abs string) string {
	if abs == "" {
		return ""
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

package app

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/cmd/gocell/app/printers"
	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/registry"
	"github.com/ghbvf/gocell/tools/nogo/unconditionalskip"
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
		return fmt.Errorf("usage: gocell check <contract-health|slice-coverage|assembly-completeness|journey-readiness|l0-imports|unconditional-skip> [flags]")
	}

	subtype := args[0]
	subArgs := args[1:]

	switch subtype {
	case "contract-health":
		return checkContractHealth(subArgs)
	case "slice-coverage":
		return checkPlaceholder("slice-coverage", subArgs)
	case "assembly-completeness":
		return checkPlaceholder("assembly-completeness", subArgs)
	case "journey-readiness":
		return checkPlaceholder("journey-readiness", subArgs)
	case "l0-imports":
		return checkPlaceholder("l0-imports", subArgs)
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
		return fmt.Errorf("cannot find project root: %w", err)
	}

	parser := metadata.NewParser(root)
	project, err := parser.Parse()
	if err != nil {
		return fmt.Errorf("metadata parse: %w", err)
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

	validator := governance.NewValidator(project, root)
	results := validator.CheckContractHealth(contracts)

	if err := printer.Print(results); err != nil {
		return fmt.Errorf("emit results: %w", err)
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
func countContractHealthErrors(results []governance.ValidationResult) int {
	n := 0
	for i := range results {
		if results[i].Severity == governance.SeverityError {
			n++
		}
	}
	return n
}

func checkPlaceholder(name string, args []string) error {
	// Parse flags even for placeholders, so --help works and invalid flags are caught.
	fs := flag.NewFlagSet("check "+name, flag.ContinueOnError)
	_ = fs.String("cell", "", "cell ID")
	_ = fs.String("id", "", "assembly ID")
	_ = fs.String("journey", "", "journey ID")
	if err := fs.Parse(args); err != nil {
		return err
	}

	return fmt.Errorf("not implemented: gocell check %s", name)
}

// checkUnconditionalSkip implements `gocell check unconditional-skip`.
//
// It loads packages matching the given patterns (default: "./..."), runs the
// unconditionalskip analyzer over them, and renders the diagnostics as
// governance.ValidationResult entries using the configured output format.
//
// Exit behaviour mirrors checkContractHealth: a non-zero error is returned
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

	results, err := runUnconditionalSkipAnalyzer(patterns, root)
	if err != nil {
		return err
	}

	if err := printer.Print(results); err != nil {
		return fmt.Errorf("emit results: %w", err)
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
func runUnconditionalSkipAnalyzer(patterns []string, root string) ([]governance.ValidationResult, error) {
	// packages.LoadAllSyntax loads type-annotated syntax for initial packages
	// and all transitive dependencies — the minimum mode checker.Analyze needs.
	cfg := &packages.Config{
		Mode:  packages.LoadAllSyntax,
		Tests: true,
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

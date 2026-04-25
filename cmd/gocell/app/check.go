package app

import (
	"flag"
	"fmt"
	"os"

	"github.com/ghbvf/gocell/cmd/gocell/app/printers"
	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/registry"
)

// runCheck implements:
//
//	gocell check contract-health [--format text|json|sarif]
//	gocell check slice-coverage --cell=<cellID>
//	gocell check assembly-completeness --id=<assemblyID>
//	gocell check journey-readiness --journey=<journeyID>
//	gocell check l0-imports --cell=<cellID>
func runCheck(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gocell check <contract-health|slice-coverage|assembly-completeness|journey-readiness|l0-imports> [flags]")
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

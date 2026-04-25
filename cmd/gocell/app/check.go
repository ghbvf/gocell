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

// Contract-health rule codes. Kept distinct from the FMT/REF/TOPO families
// in kernel/governance so it's clear at a glance whether a finding came
// from `gocell validate` (governance rules) or `gocell check
// contract-health` (CI-blocking contract metadata invariants).
const (
	codeContractHealthOwner     = "CH-01" // ownerCell missing
	codeContractHealthLifecycle = "CH-02" // lifecycle missing
	codeContractHealthSchema    = "CH-03" // HTTP schemaRefs incomplete or invalid
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

	results := validateContractHealth(contracts)

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

	fmt.Printf("Contract Health (%d contracts):\n\n", len(contracts))
	fmt.Printf("  %-40s %-12s %-12s %-22s %-7s %s\n",
		"ID", "KIND", "LIFECYCLE", "OWNER", "METHOD", "PATH")
	fmt.Printf("  %-40s %-12s %-12s %-22s %-7s %s\n",
		"---", "----", "---------", "-----", "------", "----")

	for _, c := range contracts {
		lifecycle := c.Lifecycle
		if lifecycle == "" {
			lifecycle = "(unset)"
		}
		method, path := httpTransportColumns(c)
		fmt.Printf("  %-40s %-12s %-12s %-22s %-7s %s\n",
			c.ID, c.Kind, lifecycle, c.OwnerCell, method, path)
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

// validateContractHealth checks contracts for CI-blocking issues:
//   - ownerCell must be set
//   - lifecycle must be set
//   - HTTP contracts must have schemaRefs (request + response unless noContent)
//
// Returns governance.ValidationResult so the same Printer infrastructure
// renders these findings as text/json/sarif. File is set to the
// contract.yaml path (relative to project root) when known so SARIF and
// IDE click-to-open work.
func validateContractHealth(contracts []*metadata.ContractMeta) []governance.ValidationResult {
	var results []governance.ValidationResult

	for _, c := range contracts {
		if c.OwnerCell == "" {
			results = append(results, contractHealthResult(c,
				codeContractHealthOwner, governance.IssueRequired,
				"ownerCell",
				fmt.Sprintf("%s: missing ownerCell", c.ID)))
		}
		if c.Lifecycle == "" {
			results = append(results, contractHealthResult(c,
				codeContractHealthLifecycle, governance.IssueRequired,
				"lifecycle",
				fmt.Sprintf("%s: missing lifecycle", c.ID)))
		}
		if c.Kind == "http" {
			results = append(results, validateHTTPSchemaRefs(c)...)
		}
	}

	return results
}

// validateHTTPSchemaRefs checks that an HTTP contract has the required
// schema references. Logic: noContent endpoints (typically DELETE/204) may
// omit response schema; all other endpoints need a response schema;
// PUT/PATCH always need a request schema.
func validateHTTPSchemaRefs(c *metadata.ContractMeta) []governance.ValidationResult {
	noContent := c.Endpoints.HTTP != nil && c.Endpoints.HTTP.NoContent

	if noContent {
		return nil
	}

	var results []governance.ValidationResult

	// Non-noContent endpoints must have at least one schema reference.
	if c.SchemaRefs.Request == "" && c.SchemaRefs.Response == "" {
		return []governance.ValidationResult{contractHealthResult(c,
			codeContractHealthSchema, governance.IssueRequired,
			"schemaRefs",
			fmt.Sprintf("%s: HTTP contract missing schemaRefs", c.ID))}
	}

	if c.SchemaRefs.Response == "" {
		results = append(results, contractHealthResult(c,
			codeContractHealthSchema, governance.IssueRequired,
			"schemaRefs.response",
			fmt.Sprintf("%s: HTTP contract missing response schemaRefs", c.ID)))
	}

	if c.Endpoints.HTTP != nil {
		method := c.Endpoints.HTTP.Method
		if (method == "PUT" || method == "PATCH") && c.SchemaRefs.Request == "" {
			results = append(results, contractHealthResult(c,
				codeContractHealthSchema, governance.IssueRequired,
				"schemaRefs.request",
				fmt.Sprintf("%s: %s contract missing request schemaRefs", c.ID, method)))
		}

		// Structural check: every declared responses[N] entry must have a
		// non-empty schemaRef. Whether that file actually exists on disk
		// is validated by REF-12 in the governance layer; this check
		// ensures the declaration itself is complete.
		for status, resp := range c.Endpoints.HTTP.Responses {
			if resp.SchemaRef == "" {
				results = append(results, contractHealthResult(c,
					codeContractHealthSchema, governance.IssueRequired,
					fmt.Sprintf("endpoints.http.responses[%d].schemaRef", status),
					fmt.Sprintf("%s: responses[%d] declared but missing schemaRef", c.ID, status)))
			}
		}
	}

	return results
}

// contractHealthResult builds a ValidationResult for a contract-health
// finding. All of these are SeverityError (CI-blocking); File is sourced
// from the contract metadata so json/sarif consumers get a working anchor.
func contractHealthResult(c *metadata.ContractMeta, code string, issueType governance.IssueType, field, message string) governance.ValidationResult {
	return governance.ValidationResult{
		Code:      code,
		Severity:  governance.SeverityError,
		IssueType: issueType,
		File:      c.File,
		Field:     field,
		Message:   message,
	}
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

package main

import (
	"flag"
	"fmt"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/registry"
)

// runCheck implements:
//
//	gocell check contract-health
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

func checkContractHealth(_ []string) error {
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

	if len(ids) == 0 {
		fmt.Println("No contracts found.")
		return nil
	}

	// Collect contracts for validation.
	contracts := make([]*metadata.ContractMeta, 0, len(ids))
	for _, id := range ids {
		contracts = append(contracts, reg.Get(id))
	}

	// Print summary.
	fmt.Printf("Contract Health (%d contracts):\n\n", len(contracts))
	fmt.Printf("  %-40s %-12s %-12s %s\n", "ID", "KIND", "LIFECYCLE", "OWNER")
	fmt.Printf("  %-40s %-12s %-12s %s\n", "---", "----", "---------", "-----")

	for _, c := range contracts {
		lifecycle := c.Lifecycle
		if lifecycle == "" {
			lifecycle = "(unset)"
		}
		fmt.Printf("  %-40s %-12s %-12s %s\n", c.ID, c.Kind, lifecycle, c.OwnerCell)
	}

	// Validate.
	issues := validateContractHealth(contracts)
	if len(issues) > 0 {
		fmt.Printf("\nISSUES (%d):\n", len(issues))
		for _, issue := range issues {
			fmt.Printf("  - %s\n", issue)
		}
		return fmt.Errorf("contract-health: %d issue(s) found", len(issues))
	}

	fmt.Println("\nPASS: all contracts healthy")
	return nil
}

// validateContractHealth checks contracts for CI-blocking issues:
//   - ownerCell must be set
//   - lifecycle must be set
//   - HTTP contracts must have schemaRefs (request + response unless noContent)
func validateContractHealth(contracts []*metadata.ContractMeta) []string {
	var issues []string

	for _, c := range contracts {
		if c.OwnerCell == "" {
			issues = append(issues, fmt.Sprintf("%s: missing ownerCell", c.ID))
		}
		if c.Lifecycle == "" {
			issues = append(issues, fmt.Sprintf("%s: missing lifecycle", c.ID))
		}
		if c.Kind == "http" {
			issues = append(issues, validateHTTPSchemaRefs(c)...)
		}
	}

	return issues
}

// validateHTTPSchemaRefs checks that an HTTP contract has the required schema references.
func validateHTTPSchemaRefs(c *metadata.ContractMeta) []string {
	var issues []string

	if c.SchemaRefs.Request == "" && c.SchemaRefs.Response == "" {
		return []string{fmt.Sprintf("%s: HTTP contract missing schemaRefs", c.ID)}
	}

	// Response schema required unless noContent is true.
	noContent := c.Endpoints.HTTP != nil && c.Endpoints.HTTP.NoContent
	if c.SchemaRefs.Response == "" && !noContent {
		issues = append(issues, fmt.Sprintf("%s: HTTP contract missing response schemaRefs", c.ID))
	}

	// Request schema required for PUT/PATCH (always body-bearing).
	// POST is excluded: action-style POSTs (publish, ack) are legitimately body-less.
	if c.Endpoints.HTTP != nil {
		method := c.Endpoints.HTTP.Method
		if (method == "PUT" || method == "PATCH") && c.SchemaRefs.Request == "" {
			issues = append(issues, fmt.Sprintf("%s: %s contract missing request schemaRefs", c.ID, method))
		}
	}

	return issues
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

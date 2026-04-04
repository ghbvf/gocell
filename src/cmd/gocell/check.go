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

	contracts := registry.NewContractRegistry(project)
	ids := contracts.AllIDs()

	if len(ids) == 0 {
		fmt.Println("No contracts found.")
		return nil
	}

	fmt.Printf("Contract Health (%d contracts):\n\n", len(ids))
	fmt.Printf("  %-40s %-12s %-12s %s\n", "ID", "KIND", "LIFECYCLE", "OWNER")
	fmt.Printf("  %-40s %-12s %-12s %s\n", "---", "----", "---------", "-----")

	for _, id := range ids {
		c := contracts.Get(id)
		lifecycle := c.Lifecycle
		if lifecycle == "" {
			lifecycle = "(unset)"
		}
		fmt.Printf("  %-40s %-12s %-12s %s\n", c.ID, c.Kind, lifecycle, c.OwnerCell)
	}

	return nil
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

	fmt.Printf("check %s: not implemented yet\n", name)
	return nil
}

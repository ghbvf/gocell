package app

import (
	"flag"
	"fmt"
	"os"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen/contractgen"
)

// generateContract implements:
//
//	gocell generate contract <contractID>  # generate one opted-in contract
//	gocell generate contract --all         # generate every Codegen=true contract
//	gocell generate contract ... --dry-run # print would-write file paths without writing
//	gocell generate contract ... --verify  # diff against disk, exit non-zero on drift, no write
//
// Flags --dry-run and --verify are mutually exclusive. --all and a positional
// contract id are mutually exclusive.
func generateContract(args []string) error {
	dryRun, verify, only, err := parseGenerateContractFlags(args)
	if err != nil {
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

	res, err := contractgen.Generate(root, project, contractgen.Options{
		DryRun:       dryRun,
		Verify:       verify,
		OnlyContract: only,
	})
	if err != nil {
		return err
	}

	if verify && len(res.Drifted) > 0 {
		for _, f := range res.Drifted {
			fmt.Fprintf(os.Stderr, "drift: %s\n", f)
		}
		return fmt.Errorf("codegen drift in %d files; run `gocell generate contract --all` to refresh", len(res.Drifted))
	}

	for _, f := range res.Generated {
		fmt.Printf("Generated: %s\n", f)
	}
	return nil
}

// parseGenerateContractFlags parses CLI flags + positional args and returns
// (dryRun, verify, onlyContract). Extracted to keep generateContract within
// the cognitive-complexity ceiling.
func parseGenerateContractFlags(args []string) (dryRunFlag, verifyFlag bool, onlyContract string, err error) {
	fs := flag.NewFlagSet("generate contract", flag.ContinueOnError)
	all := fs.Bool("all", false, "generate for every contract with codegen=true")
	dryRun := fs.Bool("dry-run", false, "print would-write file paths without writing")
	verify := fs.Bool("verify", false, "diff against disk, exit non-zero on drift, no write")
	if perr := fs.Parse(args); perr != nil {
		return false, false, "", perr
	}
	if *dryRun && *verify {
		return false, false, "", fmt.Errorf("--dry-run (stdout preview) and --verify (CI drift check, no write) are mutually exclusive; pick one")
	}
	pos := fs.Args()
	if !*all && len(pos) == 0 {
		if *dryRun || *verify {
			return false, false, "", fmt.Errorf("specify a contract id or --all when using --dry-run/--verify")
		}
		return false, false, "", fmt.Errorf("usage: gocell generate contract <contractID> | --all [--dry-run | --verify]")
	}
	if *all && len(pos) > 0 {
		return false, false, "", fmt.Errorf("--all is mutually exclusive with positional contract id")
	}
	only := ""
	if !*all {
		only = pos[0]
	}
	return *dryRun, *verify, only, nil
}

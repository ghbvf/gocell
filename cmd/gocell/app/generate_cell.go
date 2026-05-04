package app

import (
	"flag"
	"fmt"
	"os"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen/cellgen"
)

// generateCell implements:
//
//	gocell generate cell <cellID>           # generate one cell
//	gocell generate cell --all              # generate every cell with goStructName set
//	gocell generate cell ... --dry-run      # print would-write file paths without writing
//	gocell generate cell ... --verify       # diff vs disk, exit 1 on drift, no write
//
// Flags --dry-run and --verify are mutually exclusive (both suppress writes
// but for different audiences). --all and a positional cellID are mutually
// exclusive.
//
// Exit code is 0 on success or 1 on any failure; specifics go to stderr.
func generateCell(args []string) error {
	dryRun, verify, only, err := parseGenerateCellFlags(args)
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

	res, err := cellgen.Generate(root, project, cellgen.Options{
		DryRun:   dryRun,
		Verify:   verify,
		OnlyCell: only,
	})
	if err != nil {
		return err
	}

	if verify && len(res.Drifted) > 0 {
		for _, f := range res.Drifted {
			fmt.Fprintf(os.Stderr, "drift: %s\n", f)
		}
		return fmt.Errorf("codegen drift in %d files; run `gocell generate cell --all` to refresh", len(res.Drifted))
	}

	for _, f := range res.Generated {
		fmt.Printf("Generated: %s\n", f)
	}
	return nil
}

// parseGenerateCellFlags parses CLI flags + positional args and returns
// (dryRun, verify, onlyCell). Extracted to keep generateCell within
// the cognitive-complexity ceiling. --all is consumed here but not
// returned; only == "" signals all-cell generation to cellgen.Generate.
func parseGenerateCellFlags(args []string) (dryRunFlag, verifyFlag bool, onlyCell string, err error) {
	fs := flag.NewFlagSet("generate cell", flag.ContinueOnError)
	all := fs.Bool("all", false, "generate for every cell with goStructName set")
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
		return false, false, "", fmt.Errorf("usage: gocell generate cell <cellID> | --all [--dry-run | --verify]")
	}
	if *all && len(pos) > 0 {
		return false, false, "", fmt.Errorf("--all is mutually exclusive with positional cellID")
	}
	only := ""
	if !*all {
		only = pos[0]
	}
	return *dryRun, *verify, only, nil
}

package app

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ghbvf/gocell/kernel/scaffold"
)

// Shared flag name + usage for scaffold sub-commands. Constants avoid the
// "magic string" duplication SonarCloud flags across scaffoldCell/Slice/
// Contract/Journey; also makes it safe to rename in one place if the CLI
// convention evolves.
const (
	dryRunFlag  = "dry-run"
	dryRunUsage = "validate inputs and path conflict; do not write files"
)

// runScaffold implements:
//
//	gocell scaffold cell --id=<id> [--type=core] [--level=L2] [--team=<team>] [--dry-run]
//	gocell scaffold slice --id=<id> --cell=<cellID> [--dry-run]
//	gocell scaffold contract --id=<id> --kind=<kind> --owner=<cellID> [--dry-run]
//	gocell scaffold journey --id=<id> --goal=<goal> [--team=<team>] [--cells=<a,b>] [--dry-run]
//
// --dry-run validates opts and detects path conflicts without writing files;
// CI pre-commit hooks can use it to fail fast on bad inputs.
func runScaffold(args []string) error {
	// Check args shape before resolving project root — lets callers
	// (and tests) hit the usage error path without a valid cwd/go.mod.
	if len(args) < 1 {
		return fmt.Errorf("usage: gocell scaffold <cell|slice|contract|journey> [flags]")
	}
	root, err := findRoot()
	if err != nil {
		return fmt.Errorf("cannot find project root: %w", err)
	}
	return runScaffoldWithRoot(root, args)
}

// runScaffoldWithRoot dispatches a scaffold sub-command against an explicit
// project root — decoupling the dispatch from process cwd so tests can drive
// a temp directory without os.Chdir (which serialises the whole test binary).
func runScaffoldWithRoot(root string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gocell scaffold <cell|slice|contract|journey> [flags]")
	}

	subtype := args[0]
	subArgs := args[1:]

	switch subtype {
	case "cell":
		return scaffoldCell(root, subArgs)
	case "slice":
		return scaffoldSlice(root, subArgs)
	case "contract":
		return scaffoldContract(root, subArgs)
	case "journey":
		return scaffoldJourney(root, subArgs)
	default:
		return fmt.Errorf("unknown scaffold type: %s (expected cell, slice, contract, or journey)", subtype)
	}
}

// scaffoldReport carries everything reportScaffold needs. Using a struct
// instead of positional params makes call sites self-describing and safer
// against future additions (e.g. a template-version field).
type scaffoldReport struct {
	DryRun bool
	Kind   string // "cell" | "slice" | "contract" | "journey"
	ID     string // user-visible identifier
	Target string // path that was or would have been written
}

// reportScaffold prints the standard success line, switching prefix in dry-run.
func reportScaffold(r scaffoldReport) {
	if r.DryRun {
		fmt.Printf("(dry-run) Would create %s %s at %s\n", r.Kind, r.ID, r.Target)
		return
	}
	fmt.Printf("Created %s %s at %s\n", r.Kind, r.ID, r.Target)
}

func scaffoldCell(root string, args []string) error {
	fs := flag.NewFlagSet("scaffold cell", flag.ContinueOnError)
	id := fs.String("id", "", "cell ID (required)")
	cellType := fs.String("type", "core", "cell type")
	level := fs.String("level", "L2", "consistency level")
	team := fs.String("team", "", "owner team (required)")
	dryRun := fs.Bool(dryRunFlag, false, dryRunUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *id == "" {
		return fmt.Errorf("--id is required")
	}
	if *team == "" {
		return fmt.Errorf("--team is required")
	}

	s := scaffold.New(root).WithDryRun(*dryRun)
	if err := s.CreateCell(scaffold.CellOpts{
		ID:               *id,
		Type:             *cellType,
		ConsistencyLevel: *level,
		OwnerTeam:        *team,
	}); err != nil {
		return err
	}

	reportScaffold(scaffoldReport{
		DryRun: *dryRun,
		Kind:   "cell",
		ID:     *id,
		Target: filepath.Join("cells", *id, "cell.yaml"),
	})
	return nil
}

func scaffoldSlice(root string, args []string) error {
	fs := flag.NewFlagSet("scaffold slice", flag.ContinueOnError)
	id := fs.String("id", "", "slice ID (required)")
	cellID := fs.String("cell", "", "parent cell ID (required)")
	dryRun := fs.Bool(dryRunFlag, false, dryRunUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *id == "" {
		return fmt.Errorf("--id is required")
	}
	if *cellID == "" {
		return fmt.Errorf("--cell is required")
	}

	s := scaffold.New(root).WithDryRun(*dryRun)
	if err := s.CreateSlice(scaffold.SliceOpts{
		ID:     *id,
		CellID: *cellID,
	}); err != nil {
		return err
	}

	reportScaffold(scaffoldReport{
		DryRun: *dryRun,
		Kind:   "slice",
		ID:     *cellID + "/" + *id,
		Target: filepath.Join("cells", *cellID, "slices", *id, "slice.yaml"),
	})
	return nil
}

func scaffoldContract(root string, args []string) error {
	fs := flag.NewFlagSet("scaffold contract", flag.ContinueOnError)
	id := fs.String("id", "", "contract ID (required)")
	kind := fs.String("kind", "", "contract kind: http|event|command|projection (required)")
	owner := fs.String("owner", "", "owner cell ID (required)")
	dryRun := fs.Bool(dryRunFlag, false, dryRunUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *id == "" {
		return fmt.Errorf("--id is required")
	}
	if *kind == "" {
		return fmt.Errorf("--kind is required")
	}
	if *owner == "" {
		return fmt.Errorf("--owner is required")
	}

	s := scaffold.New(root).WithDryRun(*dryRun)
	if err := s.CreateContract(scaffold.ContractOpts{
		ID:        *id,
		Kind:      *kind,
		OwnerCell: *owner,
	}); err != nil {
		return err
	}

	// Contract ID format: {kind}.{domain...}.{version}
	pathParts := append([]string{"contracts"}, strings.Split(*id, ".")...)
	pathParts = append(pathParts, "contract.yaml")
	reportScaffold(scaffoldReport{
		DryRun: *dryRun,
		Kind:   "contract",
		ID:     *id,
		Target: filepath.Join(pathParts...),
	})
	return nil
}

func scaffoldJourney(root string, args []string) error {
	fs := flag.NewFlagSet("scaffold journey", flag.ContinueOnError)
	id := fs.String("id", "", "journey ID (required)")
	goal := fs.String("goal", "", "journey goal (required)")
	team := fs.String("team", "", "owner team (required)")
	cells := fs.String("cells", "", "comma-separated cell IDs (required)")
	dryRun := fs.Bool(dryRunFlag, false, dryRunUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *id == "" {
		return fmt.Errorf("--id is required")
	}
	if *goal == "" {
		return fmt.Errorf("--goal is required")
	}
	if *team == "" {
		return fmt.Errorf("--team is required")
	}
	if *cells == "" {
		return fmt.Errorf("--cells is required")
	}

	cellList := strings.Split(*cells, ",")
	for i := range cellList {
		cellList[i] = strings.TrimSpace(cellList[i])
	}

	s := scaffold.New(root).WithDryRun(*dryRun)
	if err := s.CreateJourney(scaffold.JourneyOpts{
		ID:        *id,
		Goal:      *goal,
		OwnerTeam: *team,
		Cells:     cellList,
	}); err != nil {
		return err
	}

	// Kernel scaffold normalizes ID to carry J- prefix for the filename.
	fileID := *id
	if !strings.HasPrefix(fileID, "J-") {
		fileID = "J-" + fileID
	}
	reportScaffold(scaffoldReport{
		DryRun: *dryRun,
		Kind:   "journey",
		ID:     *id,
		Target: filepath.Join("journeys", fileID+".yaml"),
	})
	return nil
}

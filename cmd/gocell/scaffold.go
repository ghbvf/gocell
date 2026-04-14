package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/ghbvf/gocell/kernel/scaffold"
)

// runScaffold implements:
//
//	gocell scaffold cell --id=<id> [--type=core] [--level=L2] [--team=<team>]
//	gocell scaffold slice --id=<id> --cell=<cellID>
//	gocell scaffold contract --id=<id> --kind=<kind> --owner=<cellID>
//	gocell scaffold journey --id=<id> --goal=<goal> [--team=<team>] [--cells=<a,b>]
func runScaffold(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gocell scaffold <cell|slice|contract|journey> [flags]")
	}

	subtype := args[0]
	subArgs := args[1:]

	root, err := findRoot()
	if err != nil {
		return fmt.Errorf("cannot find project root: %w", err)
	}
	s := scaffold.New(root)

	switch subtype {
	case "cell":
		return scaffoldCell(s, subArgs)
	case "slice":
		return scaffoldSlice(s, subArgs)
	case "contract":
		return scaffoldContract(s, subArgs)
	case "journey":
		return scaffoldJourney(s, subArgs)
	default:
		return fmt.Errorf("unknown scaffold type: %s (expected cell, slice, contract, or journey)", subtype)
	}
}

func scaffoldCell(s *scaffold.Scaffolder, args []string) error {
	fs := flag.NewFlagSet("scaffold cell", flag.ContinueOnError)
	id := fs.String("id", "", "cell ID (required)")
	cellType := fs.String("type", "core", "cell type")
	level := fs.String("level", "L2", "consistency level")
	team := fs.String("team", "", "owner team (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *id == "" {
		return fmt.Errorf("--id is required")
	}
	if *team == "" {
		return fmt.Errorf("--team is required")
	}

	if err := s.CreateCell(scaffold.CellOpts{
		ID:               *id,
		Type:             *cellType,
		ConsistencyLevel: *level,
		OwnerTeam:        *team,
	}); err != nil {
		return err
	}

	fmt.Printf("Created cell: %s\n", *id)
	return nil
}

func scaffoldSlice(s *scaffold.Scaffolder, args []string) error {
	fs := flag.NewFlagSet("scaffold slice", flag.ContinueOnError)
	id := fs.String("id", "", "slice ID (required)")
	cellID := fs.String("cell", "", "parent cell ID (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *id == "" {
		return fmt.Errorf("--id is required")
	}
	if *cellID == "" {
		return fmt.Errorf("--cell is required")
	}

	if err := s.CreateSlice(scaffold.SliceOpts{
		ID:     *id,
		CellID: *cellID,
	}); err != nil {
		return err
	}

	fmt.Printf("Created slice: %s/%s\n", *cellID, *id)
	return nil
}

func scaffoldContract(s *scaffold.Scaffolder, args []string) error {
	fs := flag.NewFlagSet("scaffold contract", flag.ContinueOnError)
	id := fs.String("id", "", "contract ID (required)")
	kind := fs.String("kind", "", "contract kind: http|event|command|projection (required)")
	owner := fs.String("owner", "", "owner cell ID (required)")
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

	if err := s.CreateContract(scaffold.ContractOpts{
		ID:        *id,
		Kind:      *kind,
		OwnerCell: *owner,
	}); err != nil {
		return err
	}

	fmt.Printf("Created contract: %s\n", *id)
	return nil
}

func scaffoldJourney(s *scaffold.Scaffolder, args []string) error {
	fs := flag.NewFlagSet("scaffold journey", flag.ContinueOnError)
	id := fs.String("id", "", "journey ID (required)")
	goal := fs.String("goal", "", "journey goal (required)")
	team := fs.String("team", "", "owner team (required)")
	cells := fs.String("cells", "", "comma-separated cell IDs (required)")
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

	if err := s.CreateJourney(scaffold.JourneyOpts{
		ID:        *id,
		Goal:      *goal,
		OwnerTeam: *team,
		Cells:     cellList,
	}); err != nil {
		return err
	}

	fmt.Printf("Created journey: %s\n", *id)
	return nil
}

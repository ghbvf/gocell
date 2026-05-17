// scaffold_assembly.go implements `gocell scaffold assembly` (K#09).
//
// Produces an assembly bundle via kernel/assembly.Generator.PlanAssemblyScaffold:
// 3 skeleton files (assembly.yaml, run.go, app.go) + 3 K#10 derived files
// (modules_gen.go, main.go, boundary.yaml), written through a single
// pathsafe.WritePlannedFiles call (SCAFFOLD-WRITE-FUNNEL-01).
// --skip-generate limits the plan to the 3 skeleton files only.
package app

import (
	"flag"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/pathsafe"
	"github.com/ghbvf/gocell/pkg/scaffoldid"
)

// scaffoldAssembly is the subcommand entry for `gocell scaffold assembly`.
// Flag set:
//
//	--id=<assemblyID>           required
//	--cells=<a,b,c>             required (comma-separated existing cells)
//	--team=<team>               required
//	--role=<role>               required
//	--deploy=<k8s|compose|binary> default k8s — k8s is omitted from yaml
//	--dry-run                   render only, no writes
//	--skip-generate             skip K#10 derived files (modules_gen.go / main.go / boundary.yaml)
func scaffoldAssembly(root string, args []string) error {
	fs := flag.NewFlagSet("scaffold assembly", flag.ContinueOnError)
	id := fs.String("id", "", "assembly ID (required)")
	cells := fs.String("cells", "", "comma-separated cell IDs (required, must already exist)")
	team := fs.String("team", "", "owner team (required)")
	role := fs.String("role", "", "owner role, e.g. maintainer (required)")
	deploy := fs.String("deploy", "k8s", "deployment template: one of [k8s compose binary]")
	dryRun := fs.Bool(dryRunFlag, false, dryRunUsage)
	skipGenerate := fs.Bool(skipGenerateFlag, false, skipGenerateAssemblyUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}

	asmID, cellList, err := validateAssemblyFlags(*id, *cells, *team, *role)
	if err != nil {
		return err
	}

	mod, err := readModule(root)
	if err != nil {
		return fmt.Errorf("scaffold assembly: read module path: %w", err)
	}

	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		return fmt.Errorf("scaffold assembly: parse project: %w", err)
	}

	spec := assembly.AssemblyScaffoldSpec{
		ID:           asmID,
		Cells:        cellList,
		OwnerTeam:    *team,
		OwnerRole:    *role,
		Deploy:       *deploy,
		SkipGenerate: *skipGenerate,
	}

	gen := assembly.NewGenerator(project, mod, root)
	plan, err := gen.PlanAssemblyScaffold(spec)
	if err != nil {
		return err
	}

	realRoot, err := pathsafe.ResolveRoot(root)
	if err != nil {
		return fmt.Errorf("scaffold assembly: resolve project root: %w", err)
	}

	ps, err := pathsafe.NewPlanSet(plan)
	if err != nil {
		return fmt.Errorf("scaffold assembly: build plan: %w", err)
	}
	if err := pathsafe.WritePlannedFiles(realRoot, ps, *dryRun); err != nil {
		return fmt.Errorf("scaffold assembly: write files: %w", err)
	}

	if *dryRun {
		for _, p := range ps.Paths() {
			rel, _ := filepath.Rel(realRoot, p)
			fmt.Printf(dryRunCreatePathFmt, filepath.ToSlash(rel))
		}
		return nil
	}

	reportScaffold(scaffoldReport{
		Kind:   "assembly",
		ID:     *id,
		Target: filepath.Join("assemblies", *id),
	})

	if *skipGenerate {
		fmt.Printf("scaffold assembly: skipped auto-generate (--skip-generate). "+
			"Run `gocell generate assembly --id=%s` to materialize "+
			"modules_gen.go / main.go / boundary.yaml.\n", *id)
	}
	return nil
}

// validateAssemblyFlags consolidates the required-field + identifier-pattern
// + free-text control-char checks for `gocell scaffold assembly` flags.
// Returns the parsed cell list on success. Lifted out of scaffoldAssembly to
// keep cognitive complexity inside the project budget.
//
// Routes through kernel/metadata single-source helpers: MatchAssemblyID (--id),
// MatchCellID (--cells[] elements), IsValidMetadataText (--team / --role).
// AssemblyIDPattern / CellIDPattern character set physically excludes path
// separators and control characters, so the legacy validateScaffoldID
// defensive layer is not needed on this flag path.
//
// ref: kubernetes/apimachinery pkg/util/validation/validation.go — single
// exported helper IsDNS1123Label invoked from CLI, scaffold, and admission.
func validateAssemblyFlags(id, cells, team, role string) (scaffoldid.ScaffoldID, []scaffoldid.ScaffoldID, error) {
	if id == "" {
		return scaffoldid.ScaffoldID{}, nil, fmt.Errorf("--id is required")
	}
	if cells == "" {
		return scaffoldid.ScaffoldID{}, nil, fmt.Errorf("--cells is required")
	}
	if team == "" {
		return scaffoldid.ScaffoldID{}, nil, fmt.Errorf("--team is required")
	}
	if role == "" {
		return scaffoldid.ScaffoldID{}, nil, fmt.Errorf("--role is required")
	}
	asmID, err := scaffoldid.Parse(id)
	if err != nil {
		return scaffoldid.ScaffoldID{}, nil, errcode.Wrap(errcode.KindInvalid, ErrScaffoldInvalidOpts,
			"--id does not match metadata AssemblyIDPattern", err,
			errcode.WithDetails(
				slog.String("flag", "--id"),
				slog.String("pattern", metadata.AssemblyIDPattern),
			),
			errcode.WithInternal(fmt.Sprintf("flag=--id value=%q pattern=%s",
				id, metadata.AssemblyIDPattern)))
	}
	if !metadata.IsValidMetadataText(team) {
		return scaffoldid.ScaffoldID{}, nil, errcode.New(errcode.KindInvalid, ErrScaffoldInvalidOpts,
			"--team contains forbidden control characters",
			errcode.WithInternal(fmt.Sprintf("flag=--team value=%q", team)))
	}
	if !metadata.IsValidMetadataText(role) {
		return scaffoldid.ScaffoldID{}, nil, errcode.New(errcode.KindInvalid, ErrScaffoldInvalidOpts,
			"--role contains forbidden control characters",
			errcode.WithInternal(fmt.Sprintf("flag=--role value=%q", role)))
	}
	rawCells := splitAndTrim(cells, ",")
	if len(rawCells) == 0 {
		return scaffoldid.ScaffoldID{}, nil, fmt.Errorf("--cells must list at least one cell")
	}
	cellList := make([]scaffoldid.ScaffoldID, 0, len(rawCells))
	for _, c := range rawCells {
		parsed, err := scaffoldid.Parse(c)
		if err != nil {
			return scaffoldid.ScaffoldID{}, nil, errcode.Wrap(errcode.KindInvalid, ErrScaffoldInvalidOpts,
				"--cells[] entry does not match metadata CellIDPattern", err,
				errcode.WithDetails(
					slog.String("flag", "--cells[]"),
					slog.String("pattern", metadata.CellIDPattern),
				),
				errcode.WithInternal(fmt.Sprintf("flag=--cells[] value=%q pattern=%s",
					c, metadata.CellIDPattern)))
		}
		cellList = append(cellList, parsed)
	}
	return asmID, cellList, nil
}

// splitAndTrim splits s by sep and trims whitespace from each segment;
// empty segments are dropped.
func splitAndTrim(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

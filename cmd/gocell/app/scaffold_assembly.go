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
	"path/filepath"
	"strings"

	"github.com/ghbvf/gocell/framework/kernel/assembly"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/pathsafe"
	"github.com/ghbvf/gocell/framework/pkg/scaffoldid"
	"github.com/ghbvf/gocell/tools/gomodutil"
)

// scaffoldAssembly is the subcommand entry for `gocell scaffold assembly`.
// Flag set:
//
//	--id=<assemblyID>           required
//	--cells=<a,b,c>             same-module cell IDs (mutually exclusive with --cell)
//	--cell=<id[@module]>        repeatable cell entry; @module makes it cross-module
//	                            (mutually exclusive with --cells)
//	--team=<team>               required
//	--role=<role>               required
//	--deploy=<k8s|compose|binary> default k8s — k8s is omitted from yaml
//	--dry-run                   render only, no writes
//	--skip-generate             skip K#10 derived files (modules_gen.go / main.go / boundary.yaml)
//
// A cross-module --cell entry (id@module) emits the object form
// cells[].{id,module} + build.compositionAPI: true, and implicitly skips the
// K#10 derived files (the cross-module cell metadata is not locally resolvable;
// see Generator.PlanAssemblyScaffold and #1516).
func scaffoldAssembly(root string, args []string) error {
	fs := flag.NewFlagSet("scaffold assembly", flag.ContinueOnError)
	id := fs.String("id", "", "assembly ID (required)")
	cells := fs.String("cells", "", "comma-separated same-module cell IDs (mutually exclusive with --cell)")
	var cellEntries repeatedFlag
	fs.Var(&cellEntries, "cell", "cell entry <id[@module]>; repeatable; @module = cross-module; exclusive with --cells")
	team := fs.String("team", "", "owner team (required)")
	role := fs.String("role", "", "owner role, e.g. maintainer (required)")
	deploy := fs.String("deploy", "k8s", "deployment template: one of [k8s compose binary]")
	modulePath := fs.String("module-path", "", "consuming repo's Go module path (e.g. github.com/acme/svc); default: read from go.mod")
	dryRun := fs.Bool(dryRunFlag, false, dryRunUsage)
	skipGenerate := fs.Bool(skipGenerateFlag, false, skipGenerateAssemblyUsage)
	layout, manifestPath := addLocatorFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	asmID, cellRefs, err := validateAssemblyFlags(*id, *team, *role, *cells, cellEntries)
	if err != nil {
		return err
	}

	locatorOpts, err := buildLocatorOptions(*layout, *manifestPath)
	if err != nil {
		return fmt.Errorf("scaffold assembly: %w", err)
	}

	mod, err := resolveModule(root, *modulePath)
	if err != nil {
		return fmt.Errorf("scaffold assembly: resolve module path: %w", err)
	}

	project, err := metadata.NewParser(root, locatorOpts...).Parse()
	if err != nil {
		return fmt.Errorf("scaffold assembly: parse project: %w", err)
	}

	spec := assembly.AssemblyScaffoldSpec{
		ID:           asmID,
		Cells:        cellRefs,
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

	switch {
	case *skipGenerate:
		fmt.Printf("scaffold assembly: skipped auto-generate (--skip-generate). "+
			"Run `gocell generate assembly --id=%s` to materialize "+
			"modules_gen.go / main.go / boundary.yaml.\n", *id)
	case hasCrossModuleCell(cellRefs, mod):
		// Cross-module cell metadata is not locally resolvable, so only the
		// assembly.yaml (build.compositionAPI: true) + run.go/app.go skeleton are
		// emitted; the K#10 derived files are deferred (mirrors --skip-generate).
		// The hint keeps the two distinct prerequisites separate (Go build vs
		// GoCell metadata discovery) and does not over-promise a working build:
		// the generated run.go skeleton is the legacy form and still needs manual
		// composition wiring for cross-module assemblies (tracked on PR #2480).
		fmt.Printf("scaffold assembly: cross-module cell(s) present — wrote assembly.yaml "+
			"(build.compositionAPI: true) + skeleton; derived files (modules_gen.go / "+
			"main.go / boundary.yaml) skipped.\nBefore `gocell generate assembly --id=%s` "+
			"can materialize them, the cross-module cells must be (a) buildable — add the "+
			"external module(s) to your go.work — and (b) metadata-discoverable in this "+
			"project (e.g. workspace mode via .gocell/manifest.yaml; see "+
			"docs/guides/cell-development-guide.md). The compositionAPI run.go wiring may "+
			"still need manual completion.\n", *id)
	}
	return nil
}

// hasCrossModuleCell reports whether any ref names a cell in a Go module other
// than the assembly's own (ownModule). It uses the same assembly.IsCrossModule
// predicate the generator uses for its derived-file skip, so the CLI hint fires
// exactly when the generator skipped the derived files (no duplicated logic).
func hasCrossModuleCell(refs []assembly.ScaffoldCellRef, ownModule string) bool {
	for _, r := range refs {
		if assembly.IsCrossModule(r.Module, ownModule) {
			return true
		}
	}
	return false
}

// repeatedFlag collects a repeatable string flag (e.g. --cell a --cell b) into
// a slice, in flag order. Go's flag package has no native repeatable flag.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ",") }

func (r *repeatedFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// validateAssemblyFlags consolidates the required-field + identifier-pattern
// + free-text control-char checks for `gocell scaffold assembly` flags and
// resolves the cell list from the (mutually exclusive) --cells / --cell inputs.
// Lifted out of scaffoldAssembly to keep cognitive complexity within budget.
//
// Routes through kernel/metadata single-source helpers: MatchAssemblyID (--id),
// CellIDPattern (cell ids via scaffoldid.Parse), MatchAssemblyModulePath (cell
// module paths), IsValidMetadataText (--team / --role). Module-path hygiene here
// is an early, precise diagnostic; the authoritative closed funnel is the
// generator's validateAssemblyScaffoldSpec (a spec built by any caller is
// re-validated there).
//
// ref: kubernetes/apimachinery pkg/util/validation/validation.go — single
// exported helper IsDNS1123Label invoked from CLI, scaffold, and admission.
func validateAssemblyFlags(
	id, team, role, cellsCSV string, cellEntries []string,
) (scaffoldid.ScaffoldID, []assembly.ScaffoldCellRef, error) {
	if id == "" {
		return scaffoldid.ScaffoldID{}, nil, fmt.Errorf("--id is required")
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
				errcode.PublicString("flag", "--id"),
				errcode.PublicString("pattern", metadata.AssemblyIDPattern),
			),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("flag=--id value=%q pattern=%s",
				id, metadata.AssemblyIDPattern))))
	}
	if !metadata.IsValidMetadataText(team) {
		return scaffoldid.ScaffoldID{}, nil, errcode.New(errcode.KindInvalid, ErrScaffoldInvalidOpts,
			"--team contains forbidden control characters",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("flag=--team value=%q", team))))
	}
	if !metadata.IsValidMetadataText(role) {
		return scaffoldid.ScaffoldID{}, nil, errcode.New(errcode.KindInvalid, ErrScaffoldInvalidOpts,
			"--role contains forbidden control characters",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("flag=--role value=%q", role))))
	}
	refs, err := parseScaffoldCells(cellsCSV, cellEntries)
	if err != nil {
		return scaffoldid.ScaffoldID{}, nil, err
	}
	return asmID, refs, nil
}

// parseScaffoldCells resolves the cell list from the mutually exclusive --cells
// (same-module CSV shorthand) and --cell (repeatable, cross-module-capable)
// flags. Exactly one source must be supplied; each is parsed and de-duplicated
// by its own helper.
func parseScaffoldCells(cellsCSV string, cellEntries []string) ([]assembly.ScaffoldCellRef, error) {
	haveCells := strings.TrimSpace(cellsCSV) != ""
	haveCell := len(cellEntries) > 0
	switch {
	case haveCells && haveCell:
		return nil, errcode.New(errcode.KindInvalid, ErrScaffoldInvalidOpts,
			"--cells and --cell are mutually exclusive; use --cell for cross-module entries",
			errcode.WithInternal(errcode.InternalAttr("_", "both --cells and --cell supplied")))
	case haveCell:
		return parseCellFlagEntries(cellEntries)
	case haveCells:
		return parseCellsCSV(cellsCSV)
	default:
		return nil, fmt.Errorf("one of --cells or --cell is required")
	}
}

// parseCellFlagEntries parses the repeatable --cell <id[@module]> entries,
// rejecting duplicate cell ids.
func parseCellFlagEntries(entries []string) ([]assembly.ScaffoldCellRef, error) {
	refs := make([]assembly.ScaffoldCellRef, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, raw := range entries {
		ref, err := parseCellEntry(raw)
		if err != nil {
			return nil, err
		}
		if err := markSeenCell(seen, ref.ID.String()); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// parseCellsCSV parses the same-module --cells=a,b,c shorthand into bare-id
// refs, rejecting duplicates. An embedded '@' fails scaffoldid.Parse, keeping
// this flag same-module only.
func parseCellsCSV(cellsCSV string) ([]assembly.ScaffoldCellRef, error) {
	raws := splitAndTrim(cellsCSV, ",")
	refs := make([]assembly.ScaffoldCellRef, 0, len(raws))
	seen := make(map[string]bool, len(raws))
	for _, raw := range raws {
		cid, err := scaffoldid.Parse(raw)
		if err != nil {
			return nil, wrapBadCellID("--cells[]", raw, err)
		}
		if err := markSeenCell(seen, cid.String()); err != nil {
			return nil, err
		}
		refs = append(refs, assembly.ScaffoldCellRef{ID: cid})
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("--cells must list at least one cell")
	}
	return refs, nil
}

// markSeenCell records id in seen, returning a duplicate-cell error if it was
// already present.
func markSeenCell(seen map[string]bool, id string) error {
	if seen[id] {
		return errcode.New(errcode.KindInvalid, ErrScaffoldInvalidOpts,
			"duplicate cell",
			errcode.WithDetails(errcode.PublicString("cell", id)),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("cell=%q", id))))
	}
	seen[id] = true
	return nil
}

// parseCellEntry parses one --cell value of the form <id> or <id@module>. The id
// is validated against CellIDPattern; a non-empty module is validated against
// MatchAssemblyModulePath (rejecting characters that could break out of the
// generated cellmodules import literal). An empty module after '@' is rejected.
func parseCellEntry(raw string) (assembly.ScaffoldCellRef, error) {
	raw = strings.TrimSpace(raw)
	idPart, modulePart, hasAt := strings.Cut(raw, "@")
	cid, err := scaffoldid.Parse(idPart)
	if err != nil {
		return assembly.ScaffoldCellRef{}, wrapBadCellID("--cell", idPart, err)
	}
	if !hasAt {
		return assembly.ScaffoldCellRef{ID: cid}, nil
	}
	if modulePart == "" {
		return assembly.ScaffoldCellRef{}, errcode.New(errcode.KindInvalid, ErrScaffoldInvalidOpts,
			"--cell module is empty after '@' (omit '@' for a same-module cell)",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("flag=--cell value=%q", raw))))
	}
	// The module value flows into the generated "<module>/cellmodules/<id>" import,
	// so validate it as a Go import path via the single-source gomodutil helper
	// (the same one --module-path uses). It is stronger than the metadata
	// char-blocklist: it also rejects '@', whitespace, and trailing/double slashes
	// that would corrupt the emitted import. The generator-side funnel
	// (validateAssemblyScaffoldSpec → MatchAssemblyModulePath) stays as the
	// YAML/import-literal hygiene check that must mirror the assembly.yaml decoder.
	if err := gomodutil.ValidateModulePath(modulePart); err != nil {
		return assembly.ScaffoldCellRef{}, errcode.Wrap(errcode.KindInvalid, ErrScaffoldInvalidOpts,
			"--cell module is not a valid Go module path", err,
			errcode.WithDetails(errcode.PublicString("flag", "--cell")),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("flag=--cell module=%q", modulePart))))
	}
	return assembly.ScaffoldCellRef{ID: cid, Module: modulePart}, nil
}

// wrapBadCellID wraps a scaffoldid.Parse failure for a cell id supplied via
// flagName into the shared ERR_SCAFFOLD_INVALID_OPTS diagnostic.
func wrapBadCellID(flagName, value string, err error) error {
	return errcode.Wrap(errcode.KindInvalid, ErrScaffoldInvalidOpts,
		"cell id does not match metadata CellIDPattern", err,
		errcode.WithDetails(
			errcode.PublicString("flag", flagName),
			errcode.PublicString("pattern", metadata.CellIDPattern),
		),
		errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("flag=%s value=%q pattern=%s",
			flagName, value, metadata.CellIDPattern))))
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

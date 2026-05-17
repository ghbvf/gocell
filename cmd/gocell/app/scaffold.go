package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"unicode"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/pathsafe"
	"github.com/ghbvf/gocell/pkg/yamlsafe"
	"github.com/ghbvf/gocell/tools/codegen/cellgen"
)

// ErrScaffoldInvalidOpts is the public error code surfaced when scaffold
// arguments fail the input contract (e.g. kebab-case slice IDs). Asserted
// by hack/verify-scaffold-reject.sh CI gate.
const ErrScaffoldInvalidOpts errcode.Code = "ERR_SCAFFOLD_INVALID_OPTS"

// validateScaffoldID rejects empty / "." / ".." / path separators / control
// characters in identifier flags. Identifiers are written verbatim into both
// filesystem paths (defending traversal) and inline YAML scalars (defending
// newline injection that could fabricate adjacent YAML fields). The
// control-char branch is a strict superset of validateScaffoldText so all
// ID call sites get newline rejection automatically.
//
// Mirrors cellgen.validateScaffoldSpec for parity across all scaffold CLI
// paths after kernel/scaffold removal in K#09.
func validateScaffoldID(value, field string) error {
	if value == "" {
		return errcode.New(errcode.KindInvalid, ErrScaffoldInvalidOpts,
			"scaffold field is required",
			errcode.WithInternal(fmt.Sprintf(internalFieldFmt, field)))
	}
	if value == "." || strings.Contains(value, "..") || strings.ContainsAny(value, `/\`) {
		return errcode.New(errcode.KindInvalid, ErrScaffoldInvalidOpts,
			"scaffold field contains path traversal or separator",
			errcode.WithInternal(fmt.Sprintf("field=%s value=%q", field, value)))
	}
	if strings.ContainsAny(value, "\n\r\x00") {
		return errcode.New(errcode.KindInvalid, ErrScaffoldInvalidOpts,
			"scaffold field contains forbidden control characters",
			errcode.WithInternal(fmt.Sprintf(internalFieldFmt, field)))
	}
	return nil
}

// validateScaffoldText rejects newline / carriage-return / NUL in free-text
// inputs (goal, team, role) so user values cannot inject extra YAML fields
// or break scalar quoting in the inline templates.
func validateScaffoldText(value, field string) error {
	if strings.ContainsAny(value, "\n\r\x00") {
		return errcode.New(errcode.KindInvalid, ErrScaffoldInvalidOpts,
			"scaffold field contains forbidden control characters",
			errcode.WithInternal(fmt.Sprintf(internalFieldFmt, field)))
	}
	return nil
}

// validateContractFlags consolidates required-field + traversal/control-char
// + kind whitelist + ID-segment checks for `gocell scaffold contract`.
// Returns the parsed dot-separated ID segments on success. Lifted out of
// scaffoldContract to keep cognitive complexity inside the project budget.
func validateContractFlags(id, kind, owner string) ([]string, error) {
	if id == "" {
		return nil, errors.New(errMsgIDRequired)
	}
	if kind == "" {
		return nil, fmt.Errorf("--kind is required")
	}
	if owner == "" {
		return nil, fmt.Errorf("--owner is required")
	}
	if err := validateScaffoldID(id, "--id"); err != nil {
		return nil, err
	}
	if err := validateScaffoldID(kind, "--kind"); err != nil {
		return nil, err
	}
	if err := validateScaffoldID(owner, "--owner"); err != nil {
		return nil, err
	}
	validKinds := map[string]bool{"http": true, "event": true, "command": true, "projection": true}
	if !validKinds[kind] {
		return nil, fmt.Errorf("scaffold contract: --kind must be one of [http event command projection], got %q", kind)
	}
	parts := strings.Split(id, ".")
	if len(parts) < 3 {
		return nil, fmt.Errorf("scaffold contract: --id must have at least 3 dot-separated segments (got %q)", id)
	}
	if parts[0] != kind {
		return nil, fmt.Errorf("scaffold contract: --id prefix %q must match --kind %q", parts[0], kind)
	}
	return parts, nil
}

// validateJourneyFlags consolidates required-field + traversal/control-char
// checks for `gocell scaffold journey`. Returns the parsed cell list on
// success. Lifted out of scaffoldJourney to keep cognitive complexity
// inside the project budget.
func validateJourneyFlags(id, goal, team, cells string) ([]string, error) {
	if id == "" {
		return nil, errors.New(errMsgIDRequired)
	}
	if goal == "" {
		return nil, fmt.Errorf("--goal is required")
	}
	if team == "" {
		return nil, fmt.Errorf("--team is required")
	}
	if cells == "" {
		return nil, fmt.Errorf("--cells is required")
	}
	if err := validateScaffoldID(id, "--id"); err != nil {
		return nil, err
	}
	if err := validateScaffoldText(goal, "--goal"); err != nil {
		return nil, err
	}
	if err := validateScaffoldText(team, "--team"); err != nil {
		return nil, err
	}
	cellList := splitAndTrim(cells, ",")
	if len(cellList) == 0 {
		return nil, fmt.Errorf("scaffold journey: --cells must list at least one cell")
	}
	for _, c := range cellList {
		if err := validateScaffoldID(c, "--cells[]"); err != nil {
			return nil, err
		}
	}
	return cellList, nil
}

// Shared flag name + usage for scaffold sub-commands. Constants avoid the
// "magic string" duplication SonarCloud flags across scaffoldCell/Slice/
// Contract/Journey/Assembly; also makes it safe to rename in one place if
// the CLI convention evolves.
const (
	dryRunFlag                = "dry-run"
	dryRunUsage               = "validate inputs and path conflict; do not write files"
	skipGenerateFlag          = "skip-generate"
	skipGenerateCellUsage     = "skip auto-invocation of cell + contract codegen after scaffold"
	skipGenerateAssemblyUsage = "skip auto-invocation of assembly codegen (modules_gen.go / main.go / boundary.yaml)"
	withHTTPFlag              = "with-http"
	withHTTPUsage             = "include an HTTP example contract in the bundle (default if neither --with-events nor --with-both is set)"
	withEventsFlag            = "with-events"
	withEventsUsage           = "include an event example contract in the bundle"
	// errMsgIDRequired is the canonical "--id required" CLI error message.
	// Extracted to avoid SonarCloud duplicate-literal smell across the four
	// scaffold subcommand validators (cell/slice/contract/journey/assembly).
	errMsgIDRequired = "--id is required"
	// dryRunCreatePathFmt is the canonical single-path dry-run report line.
	// Used by scaffold cell + scaffold assembly when listing the files that
	// would be written. The 3-argument variant (Kind/ID/Target) lives only
	// in reportScaffold and stays inline.
	dryRunCreatePathFmt = "(dry-run) Would create %s\n"
	withBothFlag        = "with-both"
	withBothUsage       = "include both HTTP and event example contracts in the bundle"
	// internalFieldFmt is the WithInternal format string for field-level
	// validation context. Extracted to avoid duplicate-literal smell across
	// validateScaffoldID and validateScaffoldText call sites.
	internalFieldFmt = "field=%s"
	// errFmtScaffoldSlice / errFmtScaffoldContract / errFmtScaffoldJourney are
	// the canonical error-wrapping format strings for each scaffold sub-command.
	// Extracted to avoid duplicate-literal smell across the multiple
	// fmt.Errorf call sites within each sub-command.
	errFmtScaffoldSlice    = "scaffold slice: %w"
	errFmtScaffoldContract = "scaffold contract: %w"
	errFmtScaffoldJourney  = "scaffold journey: %w"
)

// runScaffold implements:
//
//	gocell scaffold cell --id=<id> --team=<team> --role=<role> [--type=core] [--level=L2] [--dry-run]
//	gocell scaffold slice --id=<id> --cell=<cellID> [--dry-run]
//	gocell scaffold contract --id=<id> --kind=<kind> --owner=<cellID> [--dry-run]
//	gocell scaffold journey --id=<id> --goal=<goal> [--team=<team>] [--cells=<a,b>] [--dry-run]
//	gocell scaffold assembly --id=<id> --cells=<a,b> --team=<team> --role=<role> [--deploy=k8s] [--dry-run] [--skip-generate]  # K#09
//
// --dry-run renders templates (validating their output) and detects path
// conflicts without writing files; CI pre-commit hooks can use it to fail fast
// on bad inputs.
func runScaffold(ctx context.Context, args []string) error {
	// Check args shape before resolving project root — lets callers
	// (and tests) hit the usage error path without a valid cwd/go.mod.
	if len(args) < 1 {
		return fmt.Errorf("usage: gocell scaffold <%s> [flags]",
			strings.Join(subNames(scaffoldSubcommands), "|"))
	}
	if isHelpFlag(args[0]) {
		return renderSubHelp("scaffold", scaffoldSubcommands,
			"--dry-run validates inputs and path conflicts without writing.")
	}
	root, err := findRoot()
	if err != nil {
		return fmt.Errorf("cannot find project root: %w", err)
	}
	return runScaffoldWithRoot(ctx, root, args)
}

// scaffoldSubcommands is the single source of truth for `gocell scaffold`
// (see subcommand.go / CLI-UNIMPL-HIDE-01). The handler also takes the
// resolved project root so tests drive a temp dir via runScaffoldWithRoot
// without os.Chdir. Scaffolding is metadata + file IO + codegen with no
// cancelable downstream, so handlers discard ctx; it is threaded for
// signature uniformity with the other verb trees.
var scaffoldSubcommands = []subcommand[func(ctx context.Context, root string, args []string) error]{
	{
		name: "cell",
		help: []string{
			"Create cell skeleton + example slice + example contract.",
			"--id=<id> --team=<team> --role=<role>",
			"[--type=core|edge|support] [--level=L0..L4]",
			"[--with-http] [--with-events] [--with-both]",
			"[--skip-generate] [--dry-run]",
			"Note: --id must not contain '-' (use nodash identifiers).",
		},
		run: func(_ context.Context, root string, a []string) error { return scaffoldCell(root, a) },
	},
	{
		name: "slice",
		help: []string{
			"Create cells/<cellID>/slices/<id>/slice.yaml.",
			"--id=<id> --cell=<cellID> [--dry-run]",
			"Note: --id must not contain '-' (use nodash identifiers).",
		},
		run: func(_ context.Context, root string, a []string) error { return scaffoldSlice(root, a) },
	},
	{
		name: "contract",
		help: []string{
			"Create contracts/<kind>/<domain>/<v>/contract.yaml.",
			"--id=<id> --kind=<kind> --owner=<cellID> [--dry-run]",
		},
		run: func(_ context.Context, root string, a []string) error { return scaffoldContract(root, a) },
	},
	{
		name: "journey",
		help: []string{
			"Create journeys/<id>.yaml.",
			"--id=<id> --goal=<goal> --team=<team> --cells=<a,b,...>",
			"[--dry-run]",
		},
		run: func(_ context.Context, root string, a []string) error { return scaffoldJourney(root, a) },
	},
	{
		name: "assembly",
		help: []string{
			"Create assemblies/<id>/assembly.yaml + cmd/<id>/run.go + app.go.",
			"--id=<id> --cells=<a,b,...> --team=<team> --role=<role>",
			"[--deploy=k8s|compose|binary] (default: k8s) [--skip-generate] [--dry-run]",
			"Note: --id must not contain '-' (use nodash identifiers; min 2 chars).",
		},
		run: func(_ context.Context, root string, a []string) error { return scaffoldAssembly(root, a) },
	},
}

// runScaffoldWithRoot dispatches a scaffold sub-command against an explicit
// project root — decoupling the dispatch from process cwd so tests can drive
// a temp directory without os.Chdir (which serializes the whole test binary).
func runScaffoldWithRoot(ctx context.Context, root string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gocell scaffold <%s> [flags]",
			strings.Join(subNames(scaffoldSubcommands), "|"))
	}
	if isHelpFlag(args[0]) {
		return renderSubHelp("scaffold", scaffoldSubcommands,
			"--dry-run validates inputs and path conflicts without writing.")
	}
	run, ok := findSub(scaffoldSubcommands, args[0])
	if !ok {
		return fmt.Errorf("unknown scaffold type: %s (expected %s)",
			args[0], strings.Join(subNames(scaffoldSubcommands), ", "))
	}
	return run(ctx, root, args[1:])
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

// scaffoldCellInputs groups the parsed flag values for buildScaffoldCellSpec.
// Introduced to replace the 11-parameter signature that exceeded the Sonar
// cognitive-complexity cap (max 7 params per function).
type scaffoldCellInputs struct {
	ID, ResolvedStruct, Package, ModulePath, OwnerTeam, OwnerRole, CellType, Level string
	WithHTTP, WithEvents, WithBoth                                                 bool
}

// buildScaffoldCellSpec constructs a cellgen.ScaffoldSpec from the parsed
// flag values. resolvedStruct must already be computed (PascalCase of id if
// --struct was not provided). DryRun is always false here; callers that need
// DryRun=true set it after construction.
func buildScaffoldCellSpec(in scaffoldCellInputs) cellgen.ScaffoldSpec {
	return cellgen.ScaffoldSpec{
		CellID:           in.ID,
		StructName:       in.ResolvedStruct,
		Package:          in.Package,
		ModulePath:       in.ModulePath,
		OwnerTeam:        in.OwnerTeam,
		OwnerRole:        in.OwnerRole,
		Type:             in.CellType,
		ConsistencyLevel: in.Level,
		WithHTTP:         in.WithHTTP,
		WithEvents:       in.WithEvents,
		WithBoth:         in.WithBoth,
	}
}

// validateScaffoldCellFlags enforces required-field, control-char, and no-dash
// constraints on the parsed scaffold cell flags. Extracted from scaffoldCell to
// keep the latter's cognitive complexity within the project budget (≤15).
func validateScaffoldCellFlags(id, team, role string) error {
	if id == "" {
		return errors.New(errMsgIDRequired)
	}
	if team == "" {
		return fmt.Errorf("--team is required")
	}
	if role == "" {
		return fmt.Errorf("--role is required")
	}
	// Round-7: control-char + path-traversal guard.
	if err := validateScaffoldID(id, "--id"); err != nil {
		return err
	}
	if err := validateScaffoldText(team, "--team"); err != nil {
		return err
	}
	if err := validateScaffoldText(role, "--role"); err != nil {
		return err
	}
	// F11: reject kebab-case cell IDs (aligned with scaffoldSlice behavior).
	if strings.Contains(id, "-") {
		return errcode.New(errcode.KindInvalid, ErrScaffoldInvalidOpts,
			"scaffold cell: --id must not contain '-'; use no-dash identifier",
			errcode.WithInternal(fmt.Sprintf("id=%q suggestion=%q", id, strings.ReplaceAll(id, "-", ""))))
	}
	return nil
}

// scaffoldCell implements the K#09 SCAFFOLD-ONE-CMD bundle: a one-shot command
// that produces cell + 1 example slice + 1 example contract + JSON schemas in
// a single atomic pathsafe.WritePlannedFiles call. When --skip-generate is
// false (default), derived codegen files (cell_gen.go, types_gen.go,
// iface_gen.go, handler_gen.go) are merged into the same plan so the bundle is
// compilable immediately on success, and the entire write is rolled back on any
// conflict or error.
//
// Mirrors scaffold_assembly.go (assembly cross-stage pattern): ResolveRoot →
// PlanCellBundleScaffold → WritePlannedFiles → dry-run print → reportScaffold.
func scaffoldCell(root string, args []string) error {
	fs := flag.NewFlagSet("scaffold cell", flag.ContinueOnError)
	id := fs.String("id", "", "cell ID (required)")
	cellType := fs.String("type", "core", "cell type: one of [core edge support]")
	level := fs.String("level", "L2", "consistency level: one of [L0 L1 L2 L3 L4]")
	team := fs.String("team", "", "owner team (required)")
	role := fs.String("role", "", "owner role, e.g. cell-owner (required)")
	structName := fs.String("struct", "", "Go struct name (default: PascalCase of --id)")
	dryRun := fs.Bool(dryRunFlag, false, dryRunUsage)
	skipGenerate := fs.Bool(skipGenerateFlag, false, skipGenerateCellUsage)
	withHTTP := fs.Bool(withHTTPFlag, false, withHTTPUsage)
	withEvents := fs.Bool(withEventsFlag, false, withEventsUsage)
	withBoth := fs.Bool(withBothFlag, false, withBothUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validateScaffoldCellFlags(*id, *team, *role); err != nil {
		return err
	}

	resolvedStruct := *structName
	if resolvedStruct == "" {
		resolvedStruct = cellIDToPascalCase(*id)
	}

	mod, err := readModule(root)
	if err != nil {
		return fmt.Errorf("scaffold cell: read module path: %w", err)
	}

	spec := buildScaffoldCellSpec(scaffoldCellInputs{
		ID:             *id,
		ResolvedStruct: resolvedStruct,
		Package:        *id,
		ModulePath:     mod,
		OwnerTeam:      *team,
		OwnerRole:      *role,
		CellType:       *cellType,
		Level:          *level,
		WithHTTP:       *withHTTP,
		WithEvents:     *withEvents,
		WithBoth:       *withBoth,
	})
	spec.SkipGenerate = *skipGenerate

	realRoot, err := pathsafe.ResolveRoot(root)
	if err != nil {
		return fmt.Errorf("scaffold cell: resolve project root: %w", err)
	}

	plan, err := cellgen.PlanCellBundleScaffold(realRoot, spec)
	if err != nil {
		return err
	}

	if err := pathsafe.WritePlannedFiles(realRoot, plan, *dryRun); err != nil {
		return err
	}

	if *dryRun {
		for _, p := range pathsafe.PlannedPaths(plan) {
			rel, _ := filepath.Rel(realRoot, p)
			fmt.Printf(dryRunCreatePathFmt, filepath.ToSlash(rel))
		}
		return nil
	}

	reportScaffold(scaffoldReport{
		Kind:   "cell",
		ID:     *id,
		Target: filepath.Join("cells", *id),
	})

	if *skipGenerate {
		fmt.Printf("scaffold cell: skipped auto-generate (--skip-generate). "+
			"Run `gocell generate cell %s` and `gocell generate contract --all` to materialize codegen output.\n",
			*id)
	}
	return nil
}

// cellIDToPascalCase converts a cell ID (possibly hyphenated or underscored)
// to a PascalCase Go struct name. Examples:
//
//	"foocell"   → "Foocell"
//	"foo-cell"  → "FooCell"
//	"my_core"   → "MyCore"
func cellIDToPascalCase(id string) string {
	if id == "" {
		return ""
	}
	var sb strings.Builder
	capitalizeNext := true
	for _, r := range id {
		switch {
		case r == '-' || r == '_':
			capitalizeNext = true
		case capitalizeNext:
			sb.WriteRune(unicode.ToUpper(r))
			capitalizeNext = false
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// validateSliceScaffoldFlags validates the --id, --cell, and --level flags
// for `scaffold slice`. Extracted to keep scaffoldSlice cognitive complexity ≤ 15.
func validateSliceScaffoldFlags(id, cellID, level string) error {
	if id == "" {
		return errors.New(errMsgIDRequired)
	}
	if cellID == "" {
		return fmt.Errorf("--cell is required")
	}
	if err := validateScaffoldID(id, "--id"); err != nil {
		return err
	}
	if err := validateScaffoldID(cellID, "--cell"); err != nil {
		return err
	}
	if strings.Contains(id, "-") {
		return errcode.New(errcode.KindInvalid, ErrScaffoldInvalidOpts,
			"scaffold slice: --id must not contain '-'; use no-dash identifier",
			errcode.WithInternal(fmt.Sprintf("id=%q suggestion=%q", id, strings.ReplaceAll(id, "-", ""))))
	}
	if !validSliceLevels[level] {
		return errcode.New(errcode.KindInvalid, ErrScaffoldInvalidOpts,
			"scaffold slice: --level must be one of L0|L1|L2|L3|L4",
			errcode.WithInternal(fmt.Sprintf("level=%q", level)))
	}
	return nil
}

// scaffoldSlice produces an empty slice skeleton (slice.yaml only) under
// cells/{cellID}/slices/{sliceID}/. K#09 inline-template path replaces the
// deleted kernel/scaffold package; the bundle path used by `scaffold cell`
// produces a richer slice via cellgen.PlanCellBundleScaffold. For a complete
// slice with service.go + service_test.go skeleton, prefer
// `gocell scaffold cell --id=<cell> --with-http`.
//
// All filesystem writes go through pathsafe.WritePlannedFiles (SCAFFOLD-WRITE-FUNNEL-01).
func scaffoldSlice(root string, args []string) error {
	fs := flag.NewFlagSet("scaffold slice", flag.ContinueOnError)
	id := fs.String("id", "", "slice ID (required)")
	cellID := fs.String("cell", "", "parent cell ID (required)")
	level := fs.String("level", "L1", "consistency level for the slice: L0|L1|L2|L3|L4 (default L1)")
	dryRun := fs.Bool(dryRunFlag, false, dryRunUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validateSliceScaffoldFlags(*id, *cellID, *level); err != nil {
		return err
	}

	realRoot, err := pathsafe.ResolveRoot(root)
	if err != nil {
		return fmt.Errorf("scaffold slice: resolve root: %w", err)
	}

	// Verify parent cell exists.
	cellDirAbs, err := pathsafe.ContainPath(realRoot, filepath.Join("cells", *cellID))
	if err != nil {
		return fmt.Errorf(errFmtScaffoldSlice, err)
	}
	if _, statErr := os.Stat(cellDirAbs); statErr != nil {
		return fmt.Errorf("scaffold slice: parent cell does not exist (%s); create it first via `gocell scaffold cell --id=%s ...`",
			*cellID, *cellID)
	}

	content, err := renderInlineSliceYAML(*id, *cellID, *level)
	if err != nil {
		return fmt.Errorf("scaffold slice: render: %w", err)
	}

	sliceRelDir := filepath.Join("cells", *cellID, "slices", *id)
	absYAML, err := pathsafe.ContainPath(realRoot, filepath.Join(sliceRelDir, "slice.yaml"))
	if err != nil {
		return fmt.Errorf(errFmtScaffoldSlice, err)
	}
	plan := []pathsafe.PlannedFile{{AbsPath: absYAML, Content: content}}

	// WritePlannedFiles handles both dry-run (validation + conflict detection only)
	// and live write paths.
	if err := pathsafe.WritePlannedFiles(realRoot, plan, *dryRun); err != nil {
		return fmt.Errorf(errFmtScaffoldSlice, err)
	}

	if *dryRun {
		for _, absPath := range pathsafe.PlannedPaths(plan) {
			rel, _ := filepath.Rel(root, absPath)
			fmt.Printf(dryRunCreatePathFmt, filepath.ToSlash(rel))
		}
		return nil
	}

	reportScaffold(scaffoldReport{
		Kind:   "slice",
		ID:     *cellID + "/" + *id,
		Target: filepath.Join("cells", *cellID, "slices", *id, "slice.yaml"),
	})
	return nil
}

// scaffoldContract produces an empty contract skeleton (contract.yaml only)
// under contracts/{kind}/{...}/contract.yaml. K#09 inline-template path
// replaces the deleted kernel/scaffold package. K#09 funnel: contract.yaml
// does NOT carry the `codegen:` field — parser defaults Codegen=true when
// absent (see kernel/metadata.parseContract).
func scaffoldContract(root string, args []string) error {
	fs := flag.NewFlagSet("scaffold contract", flag.ContinueOnError)
	id := fs.String("id", "", "contract ID (required)")
	kind := fs.String("kind", "", "contract kind: http|event|command|projection (required)")
	owner := fs.String("owner", "", "owner cell ID (required)")
	dryRun := fs.Bool(dryRunFlag, false, dryRunUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}

	parts, err := validateContractFlags(*id, *kind, *owner)
	if err != nil {
		return err
	}

	realRoot, err := pathsafe.ResolveRoot(root)
	if err != nil {
		return fmt.Errorf("scaffold contract: resolve root: %w", err)
	}

	contractRelDir := filepath.Join(append([]string{"contracts"}, parts...)...)
	absYAML, err := pathsafe.ContainPath(realRoot, filepath.Join(contractRelDir, "contract.yaml"))
	if err != nil {
		return fmt.Errorf(errFmtScaffoldContract, err)
	}

	content, err := renderInlineContractYAML(*id, *kind, *owner)
	if err != nil {
		return fmt.Errorf("scaffold contract: render: %w", err)
	}

	plan := []pathsafe.PlannedFile{{AbsPath: absYAML, Content: content}}

	// WritePlannedFiles handles both dry-run (validation + conflict detection only)
	// and live write paths. On dry-run it returns nil or a conflict/containment error.
	if err := pathsafe.WritePlannedFiles(realRoot, plan, *dryRun); err != nil {
		return fmt.Errorf(errFmtScaffoldContract, err)
	}

	if *dryRun {
		for _, absPath := range pathsafe.PlannedPaths(plan) {
			rel, _ := filepath.Rel(root, absPath)
			fmt.Printf(dryRunCreatePathFmt, filepath.ToSlash(rel))
		}
		return nil
	}

	reportRel := append([]string{"contracts"}, parts...)
	reportRel = append(reportRel, "contract.yaml")
	reportScaffold(scaffoldReport{
		Kind:   "contract",
		ID:     *id,
		Target: filepath.Join(reportRel...),
	})
	return nil
}

// scaffoldJourney produces an empty journey skeleton (J-{id}.yaml). K#09
// inline-template path replaces the deleted kernel/scaffold package.
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

	cellList, err := validateJourneyFlags(*id, *goal, *team, *cells)
	if err != nil {
		return err
	}

	// Normalize: ensure ID carries the J- prefix for both filename and yaml.
	// Dashes within the ID are preserved (journey IDs use dash-separated segments).
	rawID := *id
	if !strings.HasPrefix(rawID, "J-") {
		rawID = "J-" + rawID
	}
	filename := rawID + ".yaml"

	realRoot, err := pathsafe.ResolveRoot(root)
	if err != nil {
		return fmt.Errorf("scaffold journey: resolve root: %w", err)
	}

	absYAML, err := pathsafe.ContainPath(realRoot, filepath.Join("journeys", filename))
	if err != nil {
		return fmt.Errorf(errFmtScaffoldJourney, err)
	}

	content, err := renderInlineJourneyYAML(rawID, *goal, *team, cellList)
	if err != nil {
		return fmt.Errorf("scaffold journey: render: %w", err)
	}

	plan := []pathsafe.PlannedFile{{AbsPath: absYAML, Content: content}}

	// WritePlannedFiles handles both dry-run (validation + conflict detection only)
	// and live write paths.
	if err := pathsafe.WritePlannedFiles(realRoot, plan, *dryRun); err != nil {
		return fmt.Errorf(errFmtScaffoldJourney, err)
	}

	if *dryRun {
		for _, absPath := range pathsafe.PlannedPaths(plan) {
			rel, _ := filepath.Rel(root, absPath)
			fmt.Printf(dryRunCreatePathFmt, filepath.ToSlash(rel))
		}
		return nil
	}

	reportScaffold(scaffoldReport{
		Kind:   "journey",
		ID:     *id,
		Target: filepath.Join("journeys", filename),
	})
	return nil
}

// renderInlineSliceYAML returns the slice.yaml content for an empty slice.
// ID and CellID are wrapped with yamlsafe.Quote so YAML-meta characters in
// user input cannot inject extra fields or break scalar parsing.
var inlineSliceYAMLTpl = template.Must(template.New("slice-yaml").Parse(`id: {{.ID}}
belongsToCell: {{.CellID}}
consistencyLevel: {{.Level}}
contractUsages: []
verify:
  unit: []
  contract: []
allowedFiles:
  - cells/{{.CellID}}/slices/{{.ID}}/**
`))

// validSliceLevels is the set of accepted consistencyLevel values for scaffold slice.
var validSliceLevels = map[string]bool{
	"L0": true, "L1": true, "L2": true, "L3": true, "L4": true,
}

func renderInlineSliceYAML(id, cellID, level string) ([]byte, error) {
	var buf strings.Builder
	data := struct {
		ID, CellID yamlsafe.Scalar
		Level      string
	}{
		ID:     yamlsafe.Quote(id),
		CellID: yamlsafe.Quote(cellID),
		Level:  level,
	}
	if err := inlineSliceYAMLTpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

// renderInlineContractYAML returns contract.yaml content for an empty contract.
// K#09 standalone contract scaffold: emits explicit codegen: false because
// this draft has no schemaRefs yet; flip to true (or remove) once schemas are
// filled in. Mirrors the 5 deferred kind=command contracts.
// ID, Kind, and OwnerCell are wrapped with yamlsafe.Quote so YAML-meta
// characters in user input cannot inject extra fields or break scalar parsing.
var inlineContractYAMLTpl = template.Must(template.New("contract-yaml").Parse(`id: {{.ID}}
kind: {{.Kind}}
ownerCell: {{.OwnerCell}}
consistencyLevel: L1
lifecycle: draft
# K#09 funnel: standalone scaffold draft has no schemaRefs yet, so opt out
# of codegen explicitly. Flip to true (or remove) once schemas are filled in.
codegen: false
endpoints:
{{- if eq .Kind "http"}}
  server: {{.OwnerCell}}
  clients: []
{{- else if eq .Kind "event"}}
  publisher: {{.OwnerCell}}
  subscribers: []
{{- else if eq .Kind "command"}}
  handler: {{.OwnerCell}}
  invokers: []
{{- else if eq .Kind "projection"}}
  provider: {{.OwnerCell}}
  readers: []
{{- end}}
`))

func renderInlineContractYAML(id, kind, owner string) ([]byte, error) {
	var buf strings.Builder
	data := struct{ ID, Kind, OwnerCell yamlsafe.Scalar }{
		ID:        yamlsafe.Quote(id),
		Kind:      yamlsafe.Quote(kind),
		OwnerCell: yamlsafe.Quote(owner),
	}
	if err := inlineContractYAMLTpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

// renderInlineJourneyYAML returns the journey skeleton.
// ID, Goal, OwnerTeam, and each cell in Cells are wrapped with yamlsafe.Quote
// so YAML-meta characters in user input cannot inject extra fields.
// lifecycle: experimental is emitted as a const literal (schema-required default).
var inlineJourneyYAMLTpl = template.Must(template.New("journey-yaml").Parse(`id: {{.ID}}
goal: {{.Goal}}
lifecycle: experimental
owner:
  team: {{.OwnerTeam}}
  role: journey-owner
cells:
{{- range .Cells}}
  - {{.}}
{{- end}}
contracts: []
passCriteria: []
`))

func renderInlineJourneyYAML(id, goal, team string, cells []string) ([]byte, error) {
	var buf strings.Builder
	quotedCells := make([]yamlsafe.Scalar, len(cells))
	for i, c := range cells {
		quotedCells[i] = yamlsafe.Quote(c)
	}
	data := struct {
		ID, Goal, OwnerTeam yamlsafe.Scalar
		Cells               []yamlsafe.Scalar
	}{
		ID:        yamlsafe.Quote(id),
		Goal:      yamlsafe.Quote(goal),
		OwnerTeam: yamlsafe.Quote(team),
		Cells:     quotedCells,
	}
	if err := inlineJourneyYAMLTpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

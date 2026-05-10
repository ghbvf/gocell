package app

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"unicode"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/tools/codegen/cellgen"
	"github.com/ghbvf/gocell/tools/codegen/contractgen"
)

// ErrScaffoldInvalidOpts is the public error code surfaced when scaffold
// arguments fail the input contract (e.g. kebab-case slice IDs). Asserted
// by hack/verify-scaffold-reject.sh CI gate.
const ErrScaffoldInvalidOpts errcode.Code = "ERR_SCAFFOLD_INVALID_OPTS"

// validateScaffoldID rejects empty / "." / ".." / path separators in identifier
// flags. Mirrors cellgen.validateScaffoldSpec for parity across all scaffold
// CLI paths after kernel/scaffold removal in K#09.
func validateScaffoldID(value, field string) error {
	if value == "" {
		return errcode.New(errcode.KindInvalid, ErrScaffoldInvalidOpts,
			"scaffold field is required",
			errcode.WithInternal(fmt.Sprintf("field=%s", field)))
	}
	if value == "." || strings.Contains(value, "..") || strings.ContainsAny(value, `/\`) {
		return errcode.New(errcode.KindInvalid, ErrScaffoldInvalidOpts,
			"scaffold field contains path traversal or separator",
			errcode.WithInternal(fmt.Sprintf("field=%s value=%q", field, value)))
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
			errcode.WithInternal(fmt.Sprintf("field=%s", field)))
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
	dryRunFlag        = "dry-run"
	dryRunUsage       = "validate inputs and path conflict; do not write files"
	skipGenerateFlag  = "skip-generate"
	skipGenerateUsage = "skip auto-invocation of cell + contract codegen after scaffold"
	withHTTPFlag      = "with-http"
	withHTTPUsage     = "include an HTTP example contract in the bundle (default if neither --with-events nor --with-both is set)"
	withEventsFlag    = "with-events"
	withEventsUsage   = "include an event example contract in the bundle"
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
func runScaffold(args []string) error {
	// Check args shape before resolving project root — lets callers
	// (and tests) hit the usage error path without a valid cwd/go.mod.
	if len(args) < 1 {
		return fmt.Errorf("usage: gocell scaffold <cell|slice|contract|journey|assembly> [flags]")
	}
	if isHelpFlag(args[0]) {
		return printScaffoldHelp()
	}
	root, err := findRoot()
	if err != nil {
		return fmt.Errorf("cannot find project root: %w", err)
	}
	return runScaffoldWithRoot(root, args)
}

// runScaffoldWithRoot dispatches a scaffold sub-command against an explicit
// project root — decoupling the dispatch from process cwd so tests can drive
// a temp directory without os.Chdir (which serializes the whole test binary).
func runScaffoldWithRoot(root string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gocell scaffold <cell|slice|contract|journey|assembly> [flags]")
	}
	if isHelpFlag(args[0]) {
		return printScaffoldHelp()
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
	case "assembly":
		return scaffoldAssembly(root, subArgs)
	default:
		return fmt.Errorf("unknown scaffold type: %s (expected cell, slice, contract, journey, or assembly)", subtype)
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

// scaffoldCell implements the K#09 SCAFFOLD-ONE-CMD bundle: a one-shot
// command that produces cell + 1 example slice + 1 example contract +
// JSON schemas, then auto-invokes cell + contract codegen so the bundle
// is compilable and the example slice's unit test passes immediately.
//
// --skip-generate skips the codegen step (useful for dry-run sanity checks
// or pre-commit scaffold invocations); the resulting bundle is still a
// compilable cell skeleton but lacks the generated handler glue.
func scaffoldCell(root string, args []string) error {
	fs := flag.NewFlagSet("scaffold cell", flag.ContinueOnError)
	id := fs.String("id", "", "cell ID (required)")
	cellType := fs.String("type", "core", "cell type: one of [core edge support]")
	level := fs.String("level", "L2", "consistency level: one of [L0 L1 L2 L3 L4]")
	team := fs.String("team", "", "owner team (required)")
	role := fs.String("role", "", "owner role, e.g. cell-owner (required)")
	structName := fs.String("struct", "", "Go struct name (default: PascalCase of --id)")
	dryRun := fs.Bool(dryRunFlag, false, dryRunUsage)
	skipGenerate := fs.Bool(skipGenerateFlag, false, skipGenerateUsage)
	withHTTP := fs.Bool(withHTTPFlag, false, withHTTPUsage)
	withEvents := fs.Bool(withEventsFlag, false, withEventsUsage)
	withBoth := fs.Bool(withBothFlag, false, withBothUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *id == "" {
		return errors.New(errMsgIDRequired)
	}
	if *team == "" {
		return fmt.Errorf("--team is required")
	}
	if *role == "" {
		return fmt.Errorf("--role is required")
	}

	// Resolve Go identifiers shared by both dry-run and live paths.
	resolvedStruct := *structName
	if resolvedStruct == "" {
		resolvedStruct = cellIDToPascalCase(*id)
	}
	pkg := strings.ReplaceAll(*id, "-", "")

	mod, err := readModule(root)
	if err != nil {
		return fmt.Errorf("scaffold cell: read module path: %w", err)
	}

	if err := cellgen.ScaffoldCellBundle(root, cellgen.ScaffoldSpec{
		CellID:           *id,
		StructName:       resolvedStruct,
		Package:          pkg,
		ModulePath:       mod,
		OwnerTeam:        *team,
		OwnerRole:        *role,
		Type:             *cellType,
		ConsistencyLevel: *level,
		DryRun:           *dryRun,
		WithHTTP:         *withHTTP,
		WithEvents:       *withEvents,
		WithBoth:         *withBoth,
	}); err != nil {
		return err
	}

	if *dryRun {
		// Report each file the bundle would write so callers can see paths.
		yamlPath := filepath.Join("cells", *id, "cell.yaml")
		goPath := filepath.Join("cells", *id, "cell.go")
		fmt.Printf(dryRunCreatePathFmt, filepath.ToSlash(yamlPath))
		fmt.Printf(dryRunCreatePathFmt, filepath.ToSlash(goPath))
		fmt.Printf("(dry-run) Would create cell bundle (slice + contract) under %s\n",
			filepath.ToSlash(filepath.Join("cells", *id)))
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
		return nil
	}
	return autoGenerateCellBundleArtifacts(root, *id)
}

// autoGenerateCellBundleArtifacts runs cellgen + contractgen for a freshly
// scaffolded bundle. The just-written cell + contract yaml files are not in
// the parser's in-memory state; re-parse from disk before generating.
func autoGenerateCellBundleArtifacts(root, cellID string) error {
	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		return fmt.Errorf("scaffold cell: re-parse project for codegen: %w", err)
	}
	// Generate contracts first so generated DTO types are available when
	// cell_gen.go is rendered.
	if _, err := contractgen.Generate(root, project, contractgen.Options{}); err != nil {
		return fmt.Errorf("scaffold cell: generate contracts: %w", err)
	}
	if _, err := cellgen.Generate(root, project, cellgen.Options{OnlyCell: cellID}); err != nil {
		return fmt.Errorf("scaffold cell: generate cell: %w", err)
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

// scaffoldSlice produces an empty slice skeleton (slice.yaml only) under
// cells/{cellID}/slices/{sliceID}/. K#09 inline-template path replaces the
// deleted kernel/scaffold package; the bundle path used by `scaffold cell`
// produces a richer slice via cellgen.ScaffoldCellBundle. For a complete
// slice with service.go + service_test.go skeleton, prefer
// `gocell scaffold cell --id=<cell> --with-http`.
func scaffoldSlice(root string, args []string) error {
	fs := flag.NewFlagSet("scaffold slice", flag.ContinueOnError)
	id := fs.String("id", "", "slice ID (required)")
	cellID := fs.String("cell", "", "parent cell ID (required)")
	dryRun := fs.Bool(dryRunFlag, false, dryRunUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *id == "" {
		return errors.New(errMsgIDRequired)
	}
	if *cellID == "" {
		return fmt.Errorf("--cell is required")
	}
	if err := validateScaffoldID(*id, "--id"); err != nil {
		return err
	}
	if err := validateScaffoldID(*cellID, "--cell"); err != nil {
		return err
	}
	if strings.Contains(*id, "-") {
		return errcode.New(errcode.KindInvalid, ErrScaffoldInvalidOpts,
			"scaffold slice: --id must not contain '-'; use no-dash identifier",
			errcode.WithInternal(fmt.Sprintf("id=%q suggestion=%q", *id, strings.ReplaceAll(*id, "-", ""))))
	}

	cellDir := filepath.Join(root, "cells", *cellID)
	if _, err := os.Stat(cellDir); err != nil {
		return fmt.Errorf("scaffold slice: parent cell does not exist (%s); create it first via `gocell scaffold cell --id=%s ...`",
			*cellID, *cellID)
	}

	sliceDir := filepath.Join(cellDir, "slices", *id)
	yamlPath := filepath.Join(sliceDir, "slice.yaml")
	if _, err := os.Stat(yamlPath); err == nil {
		return fmt.Errorf("scaffold slice: file already exists: %s", yamlPath)
	}

	content, err := renderInlineSliceYAML(*id, *cellID)
	if err != nil {
		return fmt.Errorf("scaffold slice: render: %w", err)
	}

	if !*dryRun {
		if err := os.MkdirAll(sliceDir, 0o750); err != nil {
			return fmt.Errorf("scaffold slice: mkdir: %w", err)
		}
		if err := os.WriteFile(yamlPath, content, 0o600); err != nil {
			return fmt.Errorf("scaffold slice: write: %w", err)
		}
	}

	reportScaffold(scaffoldReport{
		DryRun: *dryRun,
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

	pathParts := append([]string{root, "contracts"}, parts...)
	dir := filepath.Join(pathParts...)
	yamlPath := filepath.Join(dir, "contract.yaml")
	if _, statErr := os.Stat(yamlPath); statErr == nil {
		return fmt.Errorf("scaffold contract: file already exists: %s", yamlPath)
	}

	content, err := renderInlineContractYAML(*id, *kind, *owner)
	if err != nil {
		return fmt.Errorf("scaffold contract: render: %w", err)
	}

	if !*dryRun {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("scaffold contract: mkdir: %w", err)
		}
		if err := os.WriteFile(yamlPath, content, 0o600); err != nil {
			return fmt.Errorf("scaffold contract: write: %w", err)
		}
	}

	reportRel := append([]string{"contracts"}, parts...)
	reportRel = append(reportRel, "contract.yaml")
	reportScaffold(scaffoldReport{
		DryRun: *dryRun,
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

	// Normalize: ensure ID carries the J- prefix for both filename and yaml,
	// then strip secondary dashes after the prefix (FMT-16 no-dash convention).
	rawID := *id
	if !strings.HasPrefix(rawID, "J-") {
		rawID = "J-" + rawID
	}
	rawID = "J-" + strings.ReplaceAll(rawID[2:], "-", "")
	filename := rawID + ".yaml"

	dir := filepath.Join(root, "journeys")
	yamlPath := filepath.Join(dir, filename)
	if _, statErr := os.Stat(yamlPath); statErr == nil {
		return fmt.Errorf("scaffold journey: file already exists: %s", yamlPath)
	}

	content, err := renderInlineJourneyYAML(rawID, *goal, *team, cellList)
	if err != nil {
		return fmt.Errorf("scaffold journey: render: %w", err)
	}

	if !*dryRun {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("scaffold journey: mkdir: %w", err)
		}
		if err := os.WriteFile(yamlPath, content, 0o600); err != nil {
			return fmt.Errorf("scaffold journey: write: %w", err)
		}
	}

	reportScaffold(scaffoldReport{
		DryRun: *dryRun,
		Kind:   "journey",
		ID:     *id,
		Target: filepath.Join("journeys", filename),
	})
	return nil
}

// renderInlineSliceYAML returns the slice.yaml content for an empty slice.
var inlineSliceYAMLTpl = template.Must(template.New("slice-yaml").Parse(`id: {{.ID}}
belongsToCell: {{.CellID}}
contractUsages: []
verify:
  unit: []
  contract: []
allowedFiles:
  - cells/{{.CellID}}/slices/{{.ID}}/**
`))

func renderInlineSliceYAML(id, cellID string) ([]byte, error) {
	var buf strings.Builder
	if err := inlineSliceYAMLTpl.Execute(&buf, struct{ ID, CellID string }{id, cellID}); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

// renderInlineContractYAML returns contract.yaml content for an empty contract.
// K#09 standalone contract scaffold: emits explicit codegen: false because
// this draft has no schemaRefs yet; flip to true (or remove) once schemas are
// filled in. Mirrors the 5 deferred kind=command contracts.
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
	if err := inlineContractYAMLTpl.Execute(&buf, struct{ ID, Kind, OwnerCell string }{id, kind, owner}); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

// renderInlineJourneyYAML returns the journey skeleton.
var inlineJourneyYAMLTpl = template.Must(template.New("journey-yaml").Parse(`id: {{.ID}}
goal: {{.Goal}}
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
	if err := inlineJourneyYAMLTpl.Execute(&buf, struct {
		ID, Goal, OwnerTeam string
		Cells               []string
	}{id, goal, team, cells}); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

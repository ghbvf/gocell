// generator.go produces derived files (main.go entrypoint and boundary.yaml)
// for an assembly based on project metadata.
//
// Design ref: go-zero goctl genFile abstraction
//   - embed templates via gentpl.FS
//   - genFile: template load -> Parse -> Execute -> bytes
//   - strong-typed context structs for each template
//   - boundary.yaml is always overwritten (generated artifact)
package assembly

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/ghbvf/gocell/kernel/assembly/gentpl"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/registry"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/pathsafe"
	"github.com/ghbvf/gocell/pkg/yamlsafe"
)

// Generator produces derived files for an assembly.
type Generator struct {
	project     *metadata.ProjectMeta
	cells       *registry.CellRegistry
	contracts   *registry.ContractRegistry
	module      string // Go module path (e.g., "github.com/ghbvf/gocell")
	projectRoot string // absolute path to project root for reading schema files (empty = skip)
}

// NewGenerator creates a Generator from project metadata, a Go module path,
// and the absolute filesystem path to the project root (the directory
// containing go.mod). projectRoot is required when contracts reference schema
// files.
func NewGenerator(project *metadata.ProjectMeta, module, projectRoot string) *Generator {
	return &Generator{
		project:     project,
		cells:       registry.NewCellRegistry(project),
		contracts:   registry.NewContractRegistry(project),
		module:      module,
		projectRoot: projectRoot,
	}
}

// entrypointContext is the template context for main.go.tpl.
type entrypointContext struct {
	Module     string
	AssemblyID string
	HelperName string
	Cells      []string
}

// boundaryContext is the template context for boundary.yaml.tpl.
type boundaryContext struct {
	Fingerprint       string
	AssemblyID        string
	ExportedContracts []string
	ImportedContracts []string
	SmokeTargets      []string
}

const (
	internalAssemblyQuotedFmt = "assembly=%q"
	internalTemplateQuotedFmt = "template=%q"
	// msgAssemblyNotFound is the public message for ErrAssemblyNotFound;
	// shared by GenerateEntrypoint / GenerateBoundary / GenerateModulesGen so
	// the wire wording stays in one place.
	msgAssemblyNotFound = "assembly not found"
)

// GenerateEntrypoint generates the main.go content for an assembly.
func (g *Generator) GenerateEntrypoint(assemblyID string) ([]byte, error) {
	asm := g.project.Assemblies[assemblyID]
	if asm == nil {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAssemblyNotFound,
			msgAssemblyNotFound,
			errcode.WithInternal(fmt.Sprintf(internalAssemblyQuotedFmt, assemblyID)))
	}

	helperName, err := assemblyRunHelperName(assemblyID)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"invalid assembly for generated run helper", err,
			errcode.WithInternal(fmt.Sprintf(internalAssemblyQuotedFmt, assemblyID)))
	}

	ctx := entrypointContext{
		Module:     g.module,
		AssemblyID: assemblyID,
		HelperName: helperName,
		Cells:      append([]string(nil), asm.Cells...),
	}

	return g.executeTemplate("main.go.tpl", ctx)
}

// modulesContext is the template context for modules_gen.go.tpl.
type modulesContext struct {
	AssemblyID string
	Modules    []string // CellModule struct names, in cells.yaml order
}

// AssemblyScaffoldSpec drives Generator.PlanAssemblyScaffold (K#09 SCAFFOLD-ONE-CMD).
// The Generator already owns the assembly.yaml / run.go / app.go renderers
// and downstream codegen (modules_gen.go, main.go, boundary.yaml) so a
// single PlanAssemblyScaffold method composes the full assembly bundle without a new
// subpackage.
type AssemblyScaffoldSpec struct {
	// ID is the assembly identifier (e.g. "myassembly"). Required.
	ID string
	// Cells lists the cell IDs that compose this assembly, in startup order.
	// Each entry must reference an existing cells/{cellID}/cell.yaml.
	Cells []string
	// OwnerTeam, OwnerRole identify the maintainers of this assembly.
	// Both required; written verbatim to assembly.yaml owner.
	OwnerTeam string
	OwnerRole string
	// Deploy selects the target deployment template; legal values are
	// "k8s" (default), "compose", "binary". Per ADR 202605061800 the
	// k8s value is omitted from assembly.yaml — the parser inherits the
	// default. compose/binary are written verbatim.
	Deploy string
	// SkipGenerate when true causes PlanAssemblyScaffold to return only the
	// 3 skeleton PlannedFiles (assembly.yaml + cmd/{id}/run.go + cmd/{id}/app.go),
	// skipping in-memory codegen for the 3 K#10 derived files.
	SkipGenerate bool
}

// scaffoldAssemblyContext is the template context for the K#09 scaffold
// templates (assembly-yaml / run-go / app-go). User-input fields are typed
// as yamlsafe.Scalar so the type system rejects raw string interpolation
// into the inline YAML template; buildScaffoldContext is the single funnel
// that wraps user input through yamlsafe.Quote.
type scaffoldAssemblyContext struct {
	ID             yamlsafe.Scalar
	Cells          []yamlsafe.Scalar
	OwnerTeam      yamlsafe.Scalar
	OwnerRole      yamlsafe.Scalar
	DeployTemplate yamlsafe.Scalar             // empty when --deploy=k8s (default — omitted from yaml)
	HelperName     string                      // run{ID-PascalCase} for runXxx() in run.go (Go identifier, not YAML scalar)
	CellModules    []scaffoldAssemblyCellEntry // {StructName + Module suffix, cellID} pairs for run.go stubs
}

// scaffoldAssemblyCellEntry pairs a generated *Module struct name with the
// cell ID it identifies. Used inside scaffold-run-go.tpl to declare a stub
// Module per cell so modules_gen.go references compile immediately.
type scaffoldAssemblyCellEntry struct {
	Name string // {GoStructName}Module — same convention as K#10 modules_gen.go
	ID   string // cell ID (cell.yaml `id:`)
}

// GenerateBoundary generates the boundary.yaml content for an assembly.
//
// Boundary contains:
//   - exportedContracts: contracts whose provider cell is in this assembly
//     but has consumers outside this assembly (or consumers list is empty)
//   - importedContracts: contracts whose consumer cell is in this assembly
//     but provider is outside
//   - smokeTargets: all cell.verify.smoke targets for cells in assembly
func (g *Generator) GenerateBoundary(assemblyID string) ([]byte, error) {
	asm := g.project.Assemblies[assemblyID]
	if asm == nil {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAssemblyNotFound,
			msgAssemblyNotFound,
			errcode.WithInternal(fmt.Sprintf(internalAssemblyQuotedFmt, assemblyID)))
	}

	cellSet := make(map[string]bool, len(asm.Cells))
	for _, c := range asm.Cells {
		cellSet[c] = true
	}

	exported, imported, err := g.computeBoundaryContracts(cellSet)
	if err != nil {
		return nil, err
	}
	smokeTargets := g.collectSmokeTargets(cellSet)
	fingerprint, fpErr := g.sourceFingerprint(assemblyID, exported, imported)
	if fpErr != nil {
		return nil, fpErr
	}

	ctx := boundaryContext{
		Fingerprint:       fingerprint,
		AssemblyID:        assemblyID,
		ExportedContracts: exported,
		ImportedContracts: imported,
		SmokeTargets:      smokeTargets,
	}

	return g.executeTemplate("boundary.yaml.tpl", ctx)
}

// GenerateModulesGen generates the modules_gen.go content for an assembly's
// CellModule factory list. cells appear in the order declared in
// assembly.yaml.cells (not sorted), preserving runtime startup order.
//
// Each cell must have GoStructName set (cell.yaml schema extension consumed
// by codegen). The generated factory references {GoStructName}Module by
// convention; the *Module struct is hand-written in cmd/{assemblyID}/.
func (g *Generator) GenerateModulesGen(assemblyID string) ([]byte, error) {
	asm := g.project.Assemblies[assemblyID]
	if asm == nil {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAssemblyNotFound,
			msgAssemblyNotFound,
			errcode.WithInternal(fmt.Sprintf(internalAssemblyQuotedFmt, assemblyID)))
	}

	modules := make([]string, 0, len(asm.Cells))
	for _, cellID := range asm.Cells {
		cm := g.cells.Get(cellID)
		if cm == nil {
			return nil, errcode.New(errcode.KindNotFound, errcode.ErrMetadataInvalid,
				"assembly references unknown cell",
				errcode.WithInternal(fmt.Sprintf("assembly=%q cell=%q", assemblyID, cellID)))
		}
		if cm.GoStructName.IsZero() {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
				"cell missing GoStructName for modules_gen factory derivation",
				errcode.WithInternal(fmt.Sprintf("assembly=%q cell=%q", assemblyID, cellID)))
		}
		modules = append(modules, cm.GoStructName.String()+"Module")
	}

	ctx := modulesContext{
		AssemblyID: assemblyID,
		Modules:    modules,
	}
	return g.executeTemplate("modules_gen.go.tpl", ctx)
}

// PlanAssemblyScaffold builds the complete []pathsafe.PlannedFile for a new
// assembly: 3 skeleton files (assembly.yaml + cmd/{id}/run.go + cmd/{id}/app.go)
// plus 3 K#10 derived files (cmd/{id}/modules_gen.go + cmd/{id}/main.go +
// assemblies/{id}/generated/boundary.yaml) when SkipGenerate=false.
//
// PURE RENDER: no filesystem mutation, no re-parse. Caller (typically cmd/
// CLI) feeds the returned plan into pathsafe.WritePlannedFiles, which is the
// single funnel for both dry-run and live writes (SCAFFOLD-WRITE-FUNNEL-01).
//
// Generator is not safe for concurrent use.
//
// PlanAssemblyScaffold may be called sequentially on the same Generator
// instance; each call leaves g.project.Assemblies in its original state
// (defer revert in appendGeneratedFiles).
//
// The Generator must have been constructed with a non-empty projectRoot.
// Each cell in spec.Cells must exist in g.project.Cells.
//
// K#10 derived files are produced by in-memory injection of a synthesized
// metadata.AssemblyMeta into g.project.Assemblies[spec.ID] before calling
// GenerateModulesGen / GenerateEntrypoint / GenerateBoundary; injection is
// reverted before return so the Generator stays idempotent across calls.
//
// Returns the plan or an error; the plan is empty on error.
func (g *Generator) PlanAssemblyScaffold(spec AssemblyScaffoldSpec) ([]pathsafe.PlannedFile, error) {
	if g.projectRoot == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"assembly.Generator.PlanAssemblyScaffold requires non-empty projectRoot")
	}
	if err := validateAssemblyScaffoldSpec(g, spec); err != nil {
		return nil, err
	}

	ctx, err := g.buildScaffoldContext(spec)
	if err != nil {
		return nil, err
	}

	realRoot, err := pathsafe.ResolveRoot(g.projectRoot)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"assembly.Generator.PlanAssemblyScaffold: resolve project root", err)
	}

	asmDir := filepath.Join("assemblies", spec.ID)
	cmdDir := filepath.Join("cmd", spec.ID)

	templateFiles := []scaffoldAssemblyFile{
		{Path: filepath.Join(asmDir, "assembly.yaml"), Template: "scaffold-assembly-yaml.tpl"},
		{Path: filepath.Join(cmdDir, "run.go"), Template: "scaffold-run-go.tpl"},
		{Path: filepath.Join(cmdDir, "app.go"), Template: "scaffold-app-go.tpl"},
	}

	plan, err := g.renderAssemblyScaffoldFiles(realRoot, templateFiles, ctx)
	if err != nil {
		return nil, err
	}

	if spec.SkipGenerate {
		return plan, nil
	}

	return g.appendGeneratedFiles(plan, spec, realRoot, asmDir, cmdDir)
}

// appendGeneratedFiles injects a synthesized AssemblyMeta, calls the three
// Generate* methods, appends their output to plan, and reverts the injection
// before returning. Kept separate from PlanAssemblyScaffold to stay under the
// cognitive-complexity limit.
func (g *Generator) appendGeneratedFiles(
	plan []pathsafe.PlannedFile,
	spec AssemblyScaffoldSpec,
	realRoot, asmDir, cmdDir string,
) ([]pathsafe.PlannedFile, error) {
	// In-memory inject synthesized AssemblyMeta so Generate* see the new assembly.
	synth := synthesizeAssemblyMeta(spec)
	prior, hadPrior := g.project.Assemblies[spec.ID]
	g.project.Assemblies[spec.ID] = synth
	defer func() {
		if hadPrior {
			g.project.Assemblies[spec.ID] = prior
		} else {
			delete(g.project.Assemblies, spec.ID)
		}
	}()

	type derivedFile struct {
		relPath string
		gen     func(string) ([]byte, error)
	}
	derived := []derivedFile{
		{filepath.Join(cmdDir, "modules_gen.go"), g.GenerateModulesGen},
		{filepath.Join(cmdDir, "main.go"), g.GenerateEntrypoint},
		{filepath.Join(asmDir, "generated", "boundary.yaml"), g.GenerateBoundary},
	}

	for _, d := range derived {
		content, gerr := d.gen(spec.ID)
		if gerr != nil {
			return nil, gerr
		}
		absPath, cerr := pathsafe.ContainPath(realRoot, d.relPath)
		if cerr != nil {
			return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed,
				"scaffold assembly: derived path containment failed", cerr,
				errcode.WithInternal(fmt.Sprintf("path=%s", d.relPath)))
		}
		plan = append(plan, pathsafe.PlannedFile{
			AbsPath: absPath,
			Content: content,
		})
	}
	return plan, nil
}

// synthesizeAssemblyMeta builds an in-memory AssemblyMeta for spec so that
// GenerateModulesGen / GenerateEntrypoint / GenerateBoundary can produce K#10
// derived files before the assembly exists on disk.
//
// In-memory only; reverted by PlanAssemblyScaffold after Generate* completes.
// Field-set completeness is enforced by ASSEMBLY-META-SYNTHESIS-FIELD-GUARD
// (synthesize_field_guard_test.go) — adding a field to metadata.AssemblyMeta
// without populating it here (or exempting it with a documented reason)
// fails CI.
//
// Build.Binary defaults to spec.ID, matching the entrypoint path's ID
// component (cmd/{spec.ID}/main.go). Build.DeployTemplate mirrors
// kernel/metadata.deriveAssembly: an empty spec.Deploy (or "k8s") derives
// to "k8s" — the same value the parser fills in when reading the on-disk
// assembly.yaml where the build block is omitted for the k8s default. This
// keeps scaffold-time in-memory AssemblyMeta byte-equal to parse-time output,
// which the boundary sourceFingerprint depends on.
//
// Note: buildScaffoldContext keeps its own empty-sentinel for DeployTemplate
// (zero yamlsafe.Scalar signals "omit block from yaml via {{if .DeployTemplate}}"),
// which is a template-render-side concern and is unaffected by this change.
//
// Entrypoint is always derived as "cmd/{spec.ID}/main.go" — the scaffold
// template writes main.go at that path, so the synthesized meta stays aligned.
func synthesizeAssemblyMeta(spec AssemblyScaffoldSpec) *metadata.AssemblyMeta {
	deployTemplate := spec.Deploy
	if deployTemplate == "" || deployTemplate == "k8s" {
		// Mirror kernel/metadata.deriveAssembly: an unset or explicit "k8s"
		// deployTemplate derives to "k8s" at parse time. Synthesizing the same
		// default keeps scaffold-time and parse-time AssemblyMeta byte-equal,
		// which the boundary sourceFingerprint depends on.
		deployTemplate = "k8s"
	}
	return &metadata.AssemblyMeta{
		ID:    spec.ID,
		Cells: append([]string(nil), spec.Cells...),
		Owner: metadata.OwnerMeta{
			Team: spec.OwnerTeam,
			Role: spec.OwnerRole,
		},
		Build: metadata.BuildMeta{
			Entrypoint:     filepath.Join("cmd", spec.ID, "main.go"),
			Binary:         spec.ID,
			DeployTemplate: deployTemplate,
		},
	}
}

// scaffoldAssemblyFile pairs an output path with the template used to render
// it; lifted out of Scaffold for readability and to keep funlen happy.
type scaffoldAssemblyFile struct {
	Path     string
	Template string
}

// buildScaffoldContext builds the scaffoldAssemblyContext for a spec by
// resolving the helper name, normalizing the deploy template, and collecting
// per-cell Module stub entries. Every user-input field is routed through
// pkg/yamlsafe.Quote so YAML metacharacters in user values (`:` `{` `#`
// leading whitespace, embedded quotes) cannot inject adjacent keys or
// break scalar structure in the inline YAML template.
//
// ref: pkg/yamlsafe.Quote — single funnel enforced by archtest
// YAML-QUOTE-FUNNEL-01.
func (g *Generator) buildScaffoldContext(spec AssemblyScaffoldSpec) (scaffoldAssemblyContext, error) {
	helperName, err := assemblyRunHelperName(spec.ID)
	if err != nil {
		return scaffoldAssemblyContext{}, errcode.Wrap(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"assembly id has no identifier characters", err,
			errcode.WithInternal(fmt.Sprintf(internalAssemblyQuotedFmt, spec.ID)))
	}

	// deployTemplate is a typed-Scalar sentinel: zero value `Scalar("")`
	// signals "omit from yaml" (template guard `{{- if .DeployTemplate }}`
	// evaluates the alias's zero value as false). Non-default values go
	// through yamlsafe.Quote like other user-input fields.
	var deployTemplate yamlsafe.Scalar
	if spec.Deploy != "" && spec.Deploy != "k8s" {
		// K#10 minimal default — parser/schema inherits k8s; omit from yaml.
		deployTemplate = yamlsafe.Quote(spec.Deploy)
	}

	cellModuleEntries := make([]scaffoldAssemblyCellEntry, 0, len(spec.Cells))
	quotedCells := make([]yamlsafe.Scalar, 0, len(spec.Cells))
	for _, cellID := range spec.Cells {
		cellMeta := g.cells.Get(cellID)
		// Cell existence already validated; fall back to cellID when
		// GoStructName is unset so legacy cells still produce a compilable stub.
		structName := cellID
		if cellMeta != nil && !cellMeta.GoStructName.IsZero() {
			structName = cellMeta.GoStructName.String()
		}
		cellModuleEntries = append(cellModuleEntries, scaffoldAssemblyCellEntry{
			Name: structName + "Module",
			ID:   cellID,
		})
		quotedCells = append(quotedCells, yamlsafe.Quote(cellID))
	}

	return scaffoldAssemblyContext{
		ID:             yamlsafe.Quote(spec.ID),
		Cells:          quotedCells,
		OwnerTeam:      yamlsafe.Quote(spec.OwnerTeam),
		OwnerRole:      yamlsafe.Quote(spec.OwnerRole),
		DeployTemplate: deployTemplate,
		HelperName:     helperName,
		CellModules:    cellModuleEntries,
	}, nil
}

// renderAssemblyScaffoldFiles renders each template and returns a []PlannedFile
// ready for pathsafe.WritePlannedFiles. Conflict detection is delegated to
// WritePlannedFiles (F14: render/write decoupled).
func (g *Generator) renderAssemblyScaffoldFiles(
	realRoot string,
	files []scaffoldAssemblyFile,
	ctx scaffoldAssemblyContext,
) ([]pathsafe.PlannedFile, error) {
	plan := make([]pathsafe.PlannedFile, 0, len(files))
	for _, f := range files {
		out, err := g.executeTemplate(f.Template, ctx)
		if err != nil {
			return nil, err
		}
		absPath, containErr := pathsafe.ContainPath(realRoot, f.Path)
		if containErr != nil {
			return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed,
				"scaffold assembly: path containment check failed", containErr,
				errcode.WithInternal(fmt.Sprintf("path=%s", f.Path)))
		}
		plan = append(plan, pathsafe.PlannedFile{
			AbsPath: absPath,
			Content: out,
		})
	}
	return plan, nil
}

// validateAssemblyScaffoldSpec checks required fields, syntactic identifier
// rules, and verifies that every cell in spec.Cells exists in the parsed
// project. Identifier and free-text rules are routed through kernel/metadata
// single-source helpers (MatchAssemblyID, MatchCellID, IsValidMetadataText)
// — there is no kernel-internal mirror; the metadata package is the sole
// declaration site.
//
// AssemblyIDPattern / CellIDPattern (`^[a-z][a-z0-9]+$`) physically exclude
// path separators (`/`, `\`), traversal sequences (`.`, `..`), and control
// characters (`\n`, `\r`, `\x00`) — the legacy validateAssemblyPathComponent
// defensive layer was redundant and has been removed.
//
// ref: kubernetes/apimachinery pkg/util/validation/validation.go —
// IsDNS1123Label single-helper validation; same pattern applied here.
func validateAssemblyScaffoldSpec(g *Generator, spec AssemblyScaffoldSpec) error {
	if spec.ID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"assembly scaffold: ID is required")
	}
	if !metadata.MatchAssemblyID(spec.ID) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"assembly scaffold: ID does not match metadata AssemblyIDPattern",
			errcode.WithInternal(fmt.Sprintf("field=ID value=%q pattern=%s",
				spec.ID, metadata.AssemblyIDPattern)))
	}
	if len(spec.Cells) == 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"assembly scaffold: at least one cell is required")
	}
	if spec.OwnerTeam == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"assembly scaffold: OwnerTeam is required")
	}
	if !metadata.IsValidMetadataText(spec.OwnerTeam) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"assembly scaffold: OwnerTeam contains forbidden control characters",
			errcode.WithInternal(fmt.Sprintf("field=OwnerTeam value=%q", spec.OwnerTeam)))
	}
	if spec.OwnerRole == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"assembly scaffold: OwnerRole is required")
	}
	if !metadata.IsValidMetadataText(spec.OwnerRole) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"assembly scaffold: OwnerRole contains forbidden control characters",
			errcode.WithInternal(fmt.Sprintf("field=OwnerRole value=%q", spec.OwnerRole)))
	}
	if spec.Deploy != "" && !metadata.IsKnownDeployTemplate(spec.Deploy) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"assembly scaffold: --deploy must be one of [k8s compose binary]",
			errcode.WithInternal(fmt.Sprintf("deploy=%q", spec.Deploy)))
	}
	for _, cellID := range spec.Cells {
		if !metadata.MatchCellID(cellID) {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"assembly scaffold: Cells[] entry does not match metadata CellIDPattern",
				errcode.WithInternal(fmt.Sprintf("field=Cells[] value=%q pattern=%s",
					cellID, metadata.CellIDPattern)))
		}
		if g.cells.Get(cellID) == nil {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"assembly scaffold: --cells references unknown cell",
				errcode.WithInternal(fmt.Sprintf("cell=%q", cellID)))
		}
	}
	return nil
}

// computeBoundaryContracts determines which contracts cross the assembly boundary.
func (g *Generator) computeBoundaryContracts(cellSet map[string]bool) (exported, imported []string, err error) {
	exportedSet := make(map[string]bool)
	importedSet := make(map[string]bool)

	for _, contractID := range g.contracts.AllIDs() {
		provider, provErr := g.contracts.Provider(contractID)
		if provErr != nil {
			return nil, nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed,
				"boundary: resolve provider failed", provErr,
				errcode.WithInternal(fmt.Sprintf("contract=%q", contractID)))
		}
		consumers, consErr := g.contracts.Consumers(contractID)
		if consErr != nil {
			return nil, nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed,
				"boundary: resolve consumers failed", consErr,
				errcode.WithInternal(fmt.Sprintf("contract=%q", contractID)))
		}
		classifyBoundary(contractID, provider, consumers, cellSet, exportedSet, importedSet)
	}

	exported = sortedKeys(exportedSet)
	imported = sortedKeys(importedSet)
	return exported, imported, nil
}

// classifyBoundary categorizes a single contract as exported, imported, or internal
// relative to the assembly cell set.
func classifyBoundary(contractID, provider string, consumers []string, cellSet, exportedSet, importedSet map[string]bool) {
	providerInAssembly := cellSet[provider]

	if providerInAssembly {
		if len(consumers) == 0 {
			exportedSet[contractID] = true
		} else {
			for _, c := range consumers {
				if !cellSet[c] {
					exportedSet[contractID] = true
					break
				}
			}
		}
	}

	if !providerInAssembly {
		for _, c := range consumers {
			if cellSet[c] {
				importedSet[contractID] = true
				break
			}
		}
	}
}

// collectSmokeTargets gathers all verify.smoke entries from cells in the assembly.
func (g *Generator) collectSmokeTargets(cellSet map[string]bool) []string {
	var targets []string
	for cellID := range cellSet {
		cellMeta := g.cells.Get(cellID)
		if cellMeta == nil {
			continue
		}
		targets = append(targets, cellMeta.Verify.Smoke...)
	}
	sort.Strings(targets)
	return targets
}

// sourceFingerprint computes a SHA-256 hex digest from a canonical serialization
// of all ContractMeta for the assembly's boundary contracts. Adding a new field
// to ContractMeta automatically changes the fingerprint — no manual update to the
// hashing logic is required.
//
// The fingerprint covers:
//  1. Assembly identity (ID + sorted cell list + build config)
//  2. Each boundary contract's full structural metadata (via canonicalEncode)
//     prefixed by its ID to prevent cross-contract collisions
//  3. Schema file contents (when projectRoot is set)
//  4. The sorted contract-ID membership list for the boundary itself
func (g *Generator) sourceFingerprint(assemblyID string, exported, imported []string) (string, error) {
	asm := g.project.Assemblies[assemblyID]
	if asm == nil {
		return "", nil
	}

	h := sha256.New()
	if err := g.hashAssemblyIdentity(h, asm); err != nil {
		return "", err
	}

	if err := g.hashBoundaryContracts(h, exported, imported); err != nil {
		return "", err
	}

	// Record the boundary membership lists so that adding or removing a contract
	// from the boundary also shifts the fingerprint.
	if err := writeHash(h, "exported:"); err != nil {
		return "", err
	}
	for _, cID := range exported {
		if err := writeHash(h, "%s\x00", cID); err != nil {
			return "", err
		}
	}
	if err := writeHash(h, "imported:"); err != nil {
		return "", err
	}
	for _, cID := range imported {
		if err := writeHash(h, "%s\x00", cID); err != nil {
			return "", err
		}
	}

	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// hashAssemblyIdentity writes the assembly's stable identity fields into h:
// assembly ID, build config (entrypoint / binary / deployTemplate), runtime
// cell order, and per-cell structural metadata.
//
// All three Build fields are included so that a --deploy=k8s → --deploy=compose
// switch changes the sourceFingerprint and signals that the boundary.yaml needs
// regeneration.
func (g *Generator) hashAssemblyIdentity(h io.Writer, asm *metadata.AssemblyMeta) error {
	if err := writeHash(h, "assembly:%s\n", asm.ID); err != nil {
		return err
	}
	if err := writeHash(h, "build.entrypoint:%s\n", asm.Build.Entrypoint); err != nil {
		return err
	}
	if err := writeHash(h, "build.binary:%s\n", asm.Build.Binary); err != nil {
		return err
	}
	if err := writeHash(h, "build.deployTemplate:%s\n", asm.Build.DeployTemplate); err != nil {
		return err
	}
	for i, c := range asm.Cells {
		if err := writeHash(h, "cells.order:%d:%s\n", i, c); err != nil {
			return err
		}
	}
	for _, cellID := range asm.Cells {
		if err := g.hashCellIdentity(h, cellID); err != nil {
			return err
		}
	}
	return nil
}

func (g *Generator) hashCellIdentity(h io.Writer, cellID string) error {
	cellMeta := g.cells.Get(cellID)
	if cellMeta == nil {
		return writeHash(h, "cell:%s:missing\n", cellID)
	}
	if err := writeHash(h, "cell:%s:type:%s\n", cellID, cellMeta.Type); err != nil {
		return err
	}
	if err := writeHash(h, "cell:%s:consistency:%s\n", cellID, cellMeta.ConsistencyLevel); err != nil {
		return err
	}
	if err := writeHash(h, "cell:%s:owner:%s\n", cellID, cellMeta.Owner.Team); err != nil {
		return err
	}
	if err := writeHash(h, "cell:%s:schema:%s\n", cellID, cellMeta.Schema.Primary); err != nil {
		return err
	}
	for _, s := range cellMeta.Verify.Smoke {
		if err := writeHash(h, "cell:%s:smoke:%s\n", cellID, s); err != nil {
			return err
		}
	}
	return nil
}

// hashBoundaryContracts writes each boundary contract's canonical encoding into h.
// Contracts are visited in sorted ID order to ensure determinism.
func (g *Generator) hashBoundaryContracts(h io.Writer, exported, imported []string) error {
	allContracts := make([]string, 0, len(exported)+len(imported))
	allContracts = append(allContracts, exported...)
	allContracts = append(allContracts, imported...)
	sort.Strings(allContracts)

	for _, cID := range allContracts {
		c := g.contracts.Get(cID)
		// Write the contract ID as a separator even for nil contracts.
		if err := writeHash(h, "contract:%s\x00", cID); err != nil {
			return err
		}
		if c == nil {
			if err := writeHash(h, "nil\n"); err != nil {
				return err
			}
			continue
		}
		// normalizeContract sorts participant lists (Subscribers, Clients, etc.)
		// so declaration order does not affect the fingerprint — only membership does.
		nc := normalizeContract(*c)
		if err := canonicalEncode(h, nc); err != nil {
			return fmt.Errorf("fingerprint: canonical encode contract %q: %w", cID, err)
		}
		// Schema file contents are outside ContractMeta itself; hash them separately.
		if err := writeSchemaFileContents(h, g.projectRoot, c); err != nil {
			return err
		}
	}
	return nil
}

// normalizeContract returns a copy of c with participant slice fields sorted so
// that declaration order does not influence the fingerprint — only membership
// does. Triggers are NOT sorted because their order may carry semantic meaning
// (e.g. emission sequence). The returned value is a shallow copy; the caller
// must not modify it.
func normalizeContract(c metadata.ContractMeta) metadata.ContractMeta {
	e := c.Endpoints
	e.Clients = sortedCopy(e.Clients)
	e.Subscribers = sortedCopy(e.Subscribers)
	e.Invokers = sortedCopy(e.Invokers)
	e.Readers = sortedCopy(e.Readers)
	c.Endpoints = e
	return c
}

// writeSchemaFileContents hashes the content of each non-empty schema ref file
// for the contract. Paths are resolved through the metadata schema resolver so
// every generator/governance consumer shares the same schema-ref boundary.
func writeSchemaFileContents(h io.Writer, projectRoot string, c *metadata.ContractMeta) error {
	if c == nil {
		return nil
	}
	refs, err := metadata.ResolveContractSchemaRefs(projectRoot, c)
	if err != nil {
		return fmt.Errorf("fingerprint: resolve schema for contract %q: %w", c.ID, err)
	}
	for _, ref := range refs {
		content, err := os.ReadFile(ref.AbsPath)
		if err != nil {
			return fmt.Errorf("fingerprint: read schema %s for contract %q: %w", ref.Ref, c.ID, err)
		}
		if err := writeHash(h, "%s:%s:", ref.Field, ref.Ref); err != nil {
			return err
		}
		if _, err := h.Write(content); err != nil {
			return err
		}
		if err := writeHash(h, "\n"); err != nil {
			return err
		}
	}
	return nil
}

func writeHash(w io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(w, format, args...)
	return err
}

// assemblyRunHelperName derives the Go function name for the assembly run
// helper from the assembly ID. Only ASCII alphanumerics are preserved; other
// characters are skipped (assembly IDs are validated upstream to be
// ASCII-only).
func assemblyRunHelperName(assemblyID string) (string, error) {
	var suffix strings.Builder
	upperNext := true
	for _, r := range assemblyID {
		switch {
		case r >= 'a' && r <= 'z':
			if upperNext {
				r -= 'a' - 'A'
			}
			suffix.WriteRune(r)
			upperNext = false
		case r >= 'A' && r <= 'Z' || r >= '0' && r <= '9':
			suffix.WriteRune(r)
			upperNext = false
		default:
			upperNext = true
		}
	}
	if suffix.Len() == 0 {
		return "", fmt.Errorf("assembly ID contains no identifier characters")
	}
	return "run" + suffix.String(), nil
}

// executeTemplate loads a template from the embedded FS, parses it, and
// executes it with the given context.
func (g *Generator) executeTemplate(name string, ctx any) ([]byte, error) {
	content, err := gentpl.FS.ReadFile(name)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"failed to read template", err,
			errcode.WithInternal(fmt.Sprintf(internalTemplateQuotedFmt, name)))
	}

	tmpl, err := template.New(name).Parse(string(content))
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"failed to parse template", err,
			errcode.WithInternal(fmt.Sprintf(internalTemplateQuotedFmt, name)))
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"failed to execute template", err,
			errcode.WithInternal(fmt.Sprintf(internalTemplateQuotedFmt, name)))
	}

	return buf.Bytes(), nil
}

// sortedCopy returns a sorted copy of the input slice.
func sortedCopy(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	cp := make([]string, len(ss))
	copy(cp, ss)
	sort.Strings(cp)
	return cp
}

// sortedKeys returns sorted keys from a bool map.
func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

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
	"github.com/ghbvf/gocell/pkg/scaffoldid"
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
	internalAssemblyCellFmt   = "assembly=%q cell=%q"
	internalTemplateQuotedFmt = "template=%q"
	// msgAssemblyNotFound is the public message for ErrAssemblyNotFound;
	// shared by GenerateEntrypoint / GenerateBoundary / GenerateModulesGen so
	// the wire wording stays in one place.
	msgAssemblyNotFound    = "assembly not found"
	msgAssemblyUnknownCell = "assembly references unknown cell"
)

// GenerateEntrypoint generates the main.go content for an assembly.
func (g *Generator) GenerateEntrypoint(assemblyID string) ([]byte, error) {
	asm := g.project.Assemblies[assemblyID]
	if asm == nil {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAssemblyNotFound,
			msgAssemblyNotFound,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalAssemblyQuotedFmt, assemblyID))))
	}

	helperName, err := assemblyRunHelperName(assemblyID)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"invalid assembly for generated run helper", err,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalAssemblyQuotedFmt, assemblyID))))
	}

	ctx := entrypointContext{
		Module:     g.module,
		AssemblyID: assemblyID,
		HelperName: helperName,
		Cells:      metadata.CellIDs(asm.Cells),
	}

	return g.executeTemplate("main.go.tpl", ctx)
}

// modulesContext is the template context for modules_gen.go.tpl.
type modulesContext struct {
	AssemblyID string
	SourcePath string   // path to the assembly.yaml that drove generation (asm.File)
	Modules    []string // CellModule struct names, in cells.yaml order
	// Capabilities are runtime/capability.Kind const names (e.g. "Postgres"),
	// the sorted de-duplicated union of the assembly cells' cell.yaml `requires`
	// (Design Y, #855). When non-empty the template emits generatedCapabilities()
	// + the capability import; empty leaves the file byte-identical to the
	// pre-capabilities form so assemblies whose cells require nothing are unaffected.
	Capabilities []string
}

// modulesCompositionContext is the template context for
// modules_gen_composition.go.tpl. Used when assembly.yaml declares
// build.compositionAPI: true — the composition API form uses
// runtime/composition.CellModule and cellmodules/{cellID}.Module() calls
// instead of local *Module struct types.
type modulesCompositionContext struct {
	AssemblyID    string
	SourcePath    string   // path to the assembly.yaml that drove generation
	Modules       []string // Module call expressions, e.g. "cellmodulesconfigcore.Module()"
	ModuleImports []string // aliased import lines, e.g. `cellmodulesconfigcore "github.com/ghbvf/gocell/cellmodules/configcore"`
	Capabilities  []string
	// ProjectionSourceTopics is the sorted de-duplicated set of outbox-projection
	// contract ids (== routing topics) declared across the assembly cells'
	// slice.yaml contractUsages (role: subscribe, projection set, EXPLICIT
	// projectionSource: outbox; saga-journal and empty source excluded — empty fails
	// the generation closed). The composition root injects it into the
	// journaling outbox writer decorator so only events a projection will replay are
	// double-written to projection_events (EPIC #1504 D4 / I5). The template ALWAYS
	// emits generatedProjectionSourceTopics() (even when empty) so the decorator
	// wiring can call it unconditionally.
	ProjectionSourceTopics []string
	// DeploymentTopology is the assembly's deployment placement spec, single-sourced
	// from assembly.yaml topology. The template ALWAYS emits
	// generatedDeploymentTopology() (empty spec when no topology declared) so the
	// composition root can call it unconditionally. Validated by
	// metadata.ValidateTopologyStructure before codegen proceeds — illegal topology
	// (mutual exclusion, non-exhaustive, bad endpoint) fails generation closed.
	DeploymentTopology deploymentTopologyTemplateData
}

// deploymentTopologyTemplateData is the flattened template-serialisable form of
// an assembly's topology.Colocated/Remote. It is distinct from
// metadata.TopologyMeta because template rendering requires plain exported-field
// types (no methods); the translation is done once in
// buildDeploymentTopologyData.
type deploymentTopologyTemplateData struct {
	Colocated []string
	Remote    []remoteEntryTemplateData
}

// remoteEntryTemplateData is the per-remote-cell entry used in the template.
type remoteEntryTemplateData struct {
	CellID   string
	Endpoint string
}

// capabilityConstNames maps cell.yaml `requires` enum values to their
// runtime/capability.Kind const identifiers. The enum is closed and mirrored by
// metadata.CapabilityEnum + cell.schema.json + runtime/capability.Kind;
// TestCapabilityConstNamesMatchCapabilityEnum locks this map's key set to
// metadata.CapabilityEnum so adding a capability to the enum without a const
// mapping (or vice versa) fails in CI. An unknown value reaching here means that
// guard was bypassed — GenerateModulesGen fails rather than emit an undefined
// const (the FMT-36 validation in gocell validate is the primary upstream gate).
var capabilityConstNames = map[string]string{
	"postgres": "Postgres",
	"redis":    "Redis",
	"rabbitmq": "RabbitMQ",
}

// AssemblyScaffoldSpec drives Generator.PlanAssemblyScaffold (K#09 SCAFFOLD-ONE-CMD).
// The Generator already owns the assembly.yaml / run.go / app.go renderers
// and downstream codegen (modules_gen.go, main.go, boundary.yaml) so a
// single PlanAssemblyScaffold method composes the full assembly bundle without a new
// subpackage.
type AssemblyScaffoldSpec struct {
	// ID is the assembly identifier (e.g. "myassembly"). Required. Typed so the
	// (^[a-z][a-z0-9]+$) constraint is established at construction time via
	// scaffoldid.Parse — callers cannot supply an unvalidated raw string
	// (SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01).
	ID scaffoldid.ScaffoldID
	// Cells lists the cell IDs that compose this assembly, in startup order.
	// Each entry must reference an existing cells/{cellID}/cell.yaml. Typed
	// for the same reason as ID.
	Cells []scaffoldid.ScaffoldID
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
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalAssemblyQuotedFmt, assemblyID))))
	}

	cellSet := make(map[string]bool, len(asm.Cells))
	for _, ref := range asm.Cells {
		cellSet[ref.ID] = true
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
// When assembly.yaml declares build.compositionAPI: true, the output uses
// the runtime/composition.CellModule form (cellmodules/{cellID}.Module() calls)
// — see modules_gen_composition.go.tpl. Otherwise, the legacy local-type
// form is emitted (modules_gen.go.tpl), which is used by examples/.
//
// Each cell must have GoStructName set (cell.yaml schema extension consumed
// by codegen). The legacy factory references {GoStructName}Module by
// convention; the *Module struct is hand-written in cmd/{assemblyID}/.
//
// generatedCapabilities() is the sorted, de-duplicated union of the assembly
// cells' cell.yaml `requires` (Design Y, #855). Changing a cell's `requires`
// therefore requires re-running `gocell generate assembly` to refresh
// modules_gen.go; `--verify` catches stale output in CI.
func (g *Generator) GenerateModulesGen(assemblyID string) ([]byte, error) {
	asm := g.project.Assemblies[assemblyID]
	if asm == nil {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAssemblyNotFound,
			msgAssemblyNotFound,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalAssemblyQuotedFmt, assemblyID))))
	}

	capConsts, err := g.collectCapabilityConsts(assemblyID, asm.Cells)
	if err != nil {
		return nil, err
	}

	if asm.Build.CompositionAPI {
		return g.generateModulesGenComposition(assemblyID, asm, capConsts)
	}
	return g.generateModulesGenLegacy(assemblyID, asm, capConsts)
}

// collectCapabilityConsts returns the sorted de-duplicated capability.Kind
// const names for all cells in the assembly.
func (g *Generator) collectCapabilityConsts(assemblyID string, cellRefs []metadata.AssemblyCellRef) ([]string, error) {
	capSet := make(map[string]struct{})
	for _, ref := range cellRefs {
		cm := g.cells.Get(ref.ID)
		if cm == nil {
			return nil, errcode.New(errcode.KindNotFound, errcode.ErrMetadataInvalid,
				msgAssemblyUnknownCell,
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalAssemblyCellFmt, assemblyID, ref.ID))))
		}
		for _, c := range cm.Requires {
			capSet[c] = struct{}{}
		}
	}
	requiredCaps := make([]string, 0, len(capSet))
	for c := range capSet {
		requiredCaps = append(requiredCaps, c)
	}
	sort.Strings(requiredCaps)
	capConsts := make([]string, 0, len(requiredCaps))
	for _, c := range requiredCaps {
		name, ok := capabilityConstNames[c]
		if !ok {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
				"cell declares an unknown capability in requires",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("assembly=%q capability=%q", assemblyID, c))))
		}
		capConsts = append(capConsts, name)
	}
	return capConsts, nil
}

// projectionSourceOutbox / projectionSourceSagaJournal mirror
// cellvocab.ProjectionSourceOutbox / ProjectionSourceSagaJournal, declared locally to
// keep kernel/assembly's codegen dependencies minimal (same convention as
// tools/codegen/cellgen). The outbox double-write topic set is a POSITIVE allowlist of
// these — only projections that declare an EXPLICIT projectionSource: outbox flow
// through the outbox writer; saga-journal reads the global saga_events journal, and any
// future projectionSource value is excluded by construction rather than silently
// included. An empty source is NOT defaulted to outbox: it is a parser-invariant
// violation and fails the generation closed (see collectOutboxProjectionTopics).
const (
	projectionSourceOutbox      = "outbox"
	projectionSourceSagaJournal = "saga-journal"

	// msgProjectionSourceRequiredCodegen is returned when a projection contractUsage
	// reaches the journal-topic collector with an empty projectionSource — a
	// parser-invariant violation (metadata.checkProjectionSourcePlacement requires an
	// explicit source whenever projection is set). The collector fails closed rather
	// than defaulting empty to outbox and journaling the event into the durable
	// allowlist (a security boundary, EPIC #1504 I5).
	msgProjectionSourceRequiredCodegen = "projection contractUsage requires an explicit projectionSource"
)

// collectOutboxProjectionTopics returns the sorted, de-duplicated set of contract
// ids that feed outbox-sourced projections across the assembly's cells — derived
// from slice.yaml contractUsages (role: subscribe + projection set + EXPLICIT
// projectionSource: outbox). Each contract id is the event's routing topic (the
// producer emits with WithTopic == contract id, and the projection Coordinator filters
// on it), so this set is exactly the topics the journaling decorator must double-write
// (EPIC #1504 D4 / I5). Saga-journal and any future explicit source are excluded by the
// positive allowlist.
//
// It fails CLOSED: a projection contractUsage that reaches here with an empty
// projectionSource is a parser-invariant violation (the parser requires an explicit
// source whenever projection is set), so generation errors rather than fail-open-
// defaulting empty to outbox and journaling it into the durable allowlist — the
// allowlist bounds journal growth (a security boundary), so the generation funnel must
// be closed even if a value reaches codegen by a non-parser path. Mirrors the
// capabilityConstNames "guard bypassed → GenerateModulesGen fails" posture.
func (g *Generator) collectOutboxProjectionTopics(cellRefs []metadata.AssemblyCellRef) ([]string, error) {
	inAssembly := make(map[string]bool, len(cellRefs))
	for _, ref := range cellRefs {
		inAssembly[ref.ID] = true
	}
	topicSet := make(map[string]struct{})
	for _, s := range g.project.Slices {
		if s == nil || !inAssembly[s.BelongsToCell] {
			continue
		}
		for _, cu := range s.ContractUsages {
			isOutbox, err := outboxProjectionTopic(s.ID, cu)
			if err != nil {
				return nil, err
			}
			if isOutbox {
				topicSet[cu.Contract] = struct{}{}
			}
		}
	}
	var topics []string // nil when no outbox projections (corebundle today)
	for t := range topicSet {
		topics = append(topics, t)
	}
	sort.Strings(topics)
	return topics, nil
}

// outboxProjectionTopic classifies cu for the outbox journal allowlist. It returns
// (true, nil) for a projection (role subscribe + projection set) whose projectionSource
// is EXPLICITLY outbox; (false, nil) for a non-projection or a non-outbox source
// (saga-journal / any future explicit source — excluded by the positive allowlist); and
// (false, err) when cu IS a projection but its projectionSource is empty. An empty
// source is a parser-invariant violation (the parser requires an explicit source
// whenever projection is set), so the collector fails closed rather than treating empty
// as outbox. sliceID is threaded only for the error's diagnostic context.
func outboxProjectionTopic(sliceID string, cu metadata.ContractUsage) (bool, error) {
	if cu.Role != "subscribe" || cu.Projection == "" {
		return false, nil
	}
	if cu.ProjectionSource == "" {
		return false, errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			msgProjectionSourceRequiredCodegen,
			errcode.WithInternal(errcode.InternalAttr("_",
				fmt.Sprintf("slice=%q contract=%q", sliceID, cu.Contract))))
	}
	return cu.ProjectionSource == projectionSourceOutbox, nil
}

// generateModulesGenLegacy emits the legacy local-CellModule-type form used
// by examples/ assemblies.
func (g *Generator) generateModulesGenLegacy(
	assemblyID string, asm *metadata.AssemblyMeta, capConsts []string,
) ([]byte, error) {
	modules := make([]string, 0, len(asm.Cells))
	for _, ref := range asm.Cells {
		// The legacy form references a local CellModule type ({GoStructName}Module)
		// hand-written in cmd/{assemblyID}/; it has no import path and therefore
		// cannot express a cell sourced from another Go module. Cross-module
		// assembly composition requires build.compositionAPI: true (the
		// cellmodules/{cell}.Module() form, which carries a per-cell import path).
		if g.isCrossModule(ref) {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
				"cross-module cell requires build.compositionAPI: true",
				errcode.WithInternal(errcode.InternalAttr("_",
					fmt.Sprintf("assembly=%q cell=%q module=%q", assemblyID, ref.ID, ref.Module))))
		}
		cm := g.cells.Get(ref.ID)
		if cm == nil {
			return nil, errcode.New(errcode.KindNotFound, errcode.ErrMetadataInvalid,
				msgAssemblyUnknownCell,
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalAssemblyCellFmt, assemblyID, ref.ID))))
		}
		if cm.GoStructName.IsZero() {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
				"cell missing GoStructName for modules_gen factory derivation",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalAssemblyCellFmt, assemblyID, ref.ID))))
		}
		modules = append(modules, cm.GoStructName.String()+"Module")
	}
	ctx := modulesContext{
		AssemblyID:   assemblyID,
		SourcePath:   asm.File,
		Modules:      modules,
		Capabilities: capConsts,
	}
	return g.executeTemplate("modules_gen.go.tpl", ctx)
}

// isCrossModule reports whether ref names a cell sourced from a Go module other
// than the assembly's own (g.module). An empty ref.Module, or one equal to
// g.module, is same-module.
func (g *Generator) isCrossModule(ref metadata.AssemblyCellRef) bool {
	return ref.Module != "" && ref.Module != g.module
}

// moduleOf returns the Go module path a cell's cellmodules/ package lives in:
// ref.Module when set, otherwise the assembly's own module (g.module).
func (g *Generator) moduleOf(ref metadata.AssemblyCellRef) string {
	if ref.Module != "" {
		return ref.Module
	}
	return g.module
}

// cellModuleImportPath is the SINGLE sanctioned construction site for a
// cellmodules import path. The module prefix is resolved per-cell upstream
// (g.moduleOf, from AssemblyCellRef.Module — the cross-module funnel); this
// function is the sole place that interpolates "/cellmodules/". Archtest
// ASSEMBLY-CROSS-MODULE-IMPORT-01 locks that uniqueness so no second code path
// can fabricate a cellmodules import outside the per-cell module funnel.
func cellModuleImportPath(module, cellID string) string {
	return module + "/cellmodules/" + cellID
}

// generateModulesGenComposition emits the composition.CellModule form used by
// platform assemblies (assembly.yaml build.compositionAPI: true).
// Each cell maps to cellmodules{cellID}.Module() with a matching import alias.
func (g *Generator) generateModulesGenComposition(
	assemblyID string, asm *metadata.AssemblyMeta, capConsts []string,
) ([]byte, error) {
	moduleCalls := make([]string, 0, len(asm.Cells))
	importLines := make([]string, 0, len(asm.Cells))
	seen := make(map[string]bool, len(asm.Cells))
	for _, ref := range asm.Cells {
		cm := g.cells.Get(ref.ID)
		if cm == nil {
			return nil, errcode.New(errcode.KindNotFound, errcode.ErrMetadataInvalid,
				msgAssemblyUnknownCell,
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalAssemblyCellFmt, assemblyID, ref.ID))))
		}
		alias := "cellmodules" + ref.ID
		if !seen[ref.ID] {
			seen[ref.ID] = true
			importLines = append(importLines, fmt.Sprintf("%s %q",
				alias, cellModuleImportPath(g.moduleOf(ref), ref.ID)))
		}
		moduleCalls = append(moduleCalls, alias+".Module()")
	}
	// Sort import lines by their (alias, path) string so the rendered import
	// block is gofmt-clean regardless of cell declaration order. The alias is
	// "platform"+cellID and the path ends in /cellmodules/cellID, so string-sorting
	// the import lines matches gofmt's path-based ordering. moduleCalls stay in
	// cell (assembly.yaml) order for deterministic, predictable output — that
	// order is NOT runtime-significant: the former auditcore→accesscore
	// BootstrapLedgerStore handoff was removed in #1423 (cross-cell wiring is now
	// event-driven), so module Provide order carries no runtime dependency.
	sort.Strings(importLines)
	projTopics, err := g.collectOutboxProjectionTopics(asm.Cells)
	if err != nil {
		return nil, err
	}
	// Validate topology structure before emitting — illegal topology (mutual
	// exclusion, non-exhaustive, invalid endpoint) fails generation closed rather
	// than propagating bad data into the generated funnel.
	if err := metadata.ValidateTopologyStructure(asm); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"assembly topology validation failed before codegen", err,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalAssemblyQuotedFmt, assemblyID))))
	}
	// INTERIM gate (US4 #1963 removes this block): topology.remote cannot be honored
	// yet — cross-process transport is not wired. Fail codegen closed to prevent silent
	// degrade. CheckRemotePlacementSupported returns nil when topology.remote is empty.
	if err := metadata.CheckRemotePlacementSupported(asm); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"assembly topology.remote is not yet supported (US4 #1963)", err,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalAssemblyQuotedFmt, assemblyID))))
	}
	topoData := buildDeploymentTopologyData(asm.Topology)
	ctx := modulesCompositionContext{
		AssemblyID:             assemblyID,
		SourcePath:             asm.File,
		Modules:                moduleCalls,
		ModuleImports:          importLines,
		Capabilities:           capConsts,
		ProjectionSourceTopics: projTopics,
		DeploymentTopology:     topoData,
	}
	return g.executeTemplate("modules_gen_composition.go.tpl", ctx)
}

// buildDeploymentTopologyData translates the metadata.TopologyMeta into the
// flattened template-serialisable form. Empty topology (no colocated, no remote)
// returns a zero-value deploymentTopologyTemplateData so the template emits
// bootstrap.DeploymentTopologySpec{} — the all-colocated default.
func buildDeploymentTopologyData(topo metadata.TopologyMeta) deploymentTopologyTemplateData {
	var d deploymentTopologyTemplateData
	if len(topo.Colocated) == 0 && len(topo.Remote) == 0 {
		return d
	}
	d.Colocated = append([]string(nil), topo.Colocated...)
	d.Remote = make([]remoteEntryTemplateData, 0, len(topo.Remote))
	for _, r := range topo.Remote {
		d.Remote = append(d.Remote, remoteEntryTemplateData{CellID: r.CellID, Endpoint: r.Endpoint})
	}
	return d
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
// PlanAssemblyScaffold is safe for concurrent calls on the same Generator:
// each call operates on a shadow ProjectMeta and never mutates g.project.
//
// The Generator must have been constructed with a non-empty projectRoot.
// Each cell in spec.Cells must exist in g.project.Cells.
//
// K#10 derived files are produced by constructing a transient Generator that
// holds a shallow-copied ProjectMeta with a synthesized AssemblyMeta injected
// into its Assemblies map. The original g.project.Assemblies is never touched.
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

	asmDir := filepath.Join("assemblies", spec.ID.String())
	cmdDir := filepath.Join("cmd", spec.ID.String())

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

// appendGeneratedFiles builds a transient Generator with a shadow ProjectMeta
// that holds the synthesized AssemblyMeta, calls the three Generate* methods,
// and appends their output to plan. The original g.project.Assemblies is never
// touched, making this function safe for concurrent callers.
//
// ref: kubernetes-sigs/kubebuilder pkg/machinery/scaffold.go — per-call file
// model with no shared mutable state across renders.
func (g *Generator) appendGeneratedFiles(
	plan []pathsafe.PlannedFile,
	spec AssemblyScaffoldSpec,
	realRoot, _ /*asmDir*/, cmdDir string,
) ([]pathsafe.PlannedFile, error) {
	synth := synthesizeAssemblyMeta(spec)

	// Build a shadow ProjectMeta: clone only the Assemblies map (the only field
	// written here), share all other fields by pointer — they are read-only at
	// this layer (cells, contracts, slices, journeys, actors, statusBoard).
	shadowAssemblies := make(map[string]*metadata.AssemblyMeta, len(g.project.Assemblies)+1)
	for k, v := range g.project.Assemblies {
		shadowAssemblies[k] = v
	}
	shadowAssemblies[spec.ID.String()] = synth

	shadowProject := *g.project // shallow copy of ProjectMeta value
	shadowProject.Assemblies = shadowAssemblies

	// Transient Generator: same module/projectRoot/cells/contracts as g, but
	// with the shadow project. Generate* methods read g.project through the
	// receiver; the transient is discarded after this call.
	transient := &Generator{
		project:     &shadowProject,
		cells:       g.cells,
		contracts:   g.contracts,
		module:      g.module,
		projectRoot: g.projectRoot,
	}

	// generatedDir is derived from the synthesized AssemblyMeta.File field.
	// For scaffold (File is empty, i.e. assemblies/ context) this returns
	// assemblies/{id}/generated/; the derivation mirrors
	// metadata.AssemblyGeneratedDir — single source of truth.
	generatedDir := filepath.FromSlash(metadata.AssemblyGeneratedDir(synth))

	type derivedFile struct {
		relPath string
		gen     func(string) ([]byte, error)
	}
	derived := []derivedFile{
		{filepath.Join(cmdDir, "modules_gen.go"), transient.GenerateModulesGen},
		{filepath.Join(cmdDir, "main.go"), transient.GenerateEntrypoint},
		{filepath.Join(generatedDir, "boundary.yaml"), transient.GenerateBoundary},
	}

	for _, d := range derived {
		content, gerr := d.gen(spec.ID.String())
		if gerr != nil {
			return nil, gerr
		}
		absPath, cerr := pathsafe.ContainPath(realRoot, d.relPath)
		if cerr != nil {
			return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed,
				"scaffold assembly: derived path containment failed", cerr,
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("path=%s", d.relPath))))
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
// In-memory only; consumed by appendGeneratedFiles via a shadow ProjectMeta.
// The caller (PlanAssemblyScaffold) never mutates g.project.
// Field-set completeness is enforced by ASSEMBLY-META-SYNTHESIS-FIELD-GUARD
// (synthesize_field_guard_test.go) — adding a field to metadata.AssemblyMeta
// without populating it here (or exempting it with a documented reason)
// fails CI.
//
// Build.Binary defaults to spec.ID.String(), matching the entrypoint path's ID
// component (cmd/{spec.ID.String()}/main.go). Build.DeployTemplate mirrors
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
// Entrypoint is always derived as "cmd/{spec.ID.String()}/main.go" — the scaffold
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
	// Scaffolded assemblies are always same-module (cross-module authoring is a
	// hand-edit / future scaffold flag, tracked in backlog); synthesize bare
	// same-module cell refs via the metadata.CellRefs constructor.
	cellIDs := make([]string, len(spec.Cells))
	for i, c := range spec.Cells {
		cellIDs[i] = c.String()
	}
	return &metadata.AssemblyMeta{
		ID:    spec.ID.String(),
		Cells: metadata.CellRefs(cellIDs...),
		Owner: metadata.OwnerMeta{
			Team: spec.OwnerTeam,
			Role: spec.OwnerRole,
		},
		Build: metadata.BuildMeta{
			Entrypoint:     filepath.Join("cmd", spec.ID.String(), "main.go"),
			Binary:         spec.ID.String(),
			DeployTemplate: deployTemplate,
		},
		// File is set so that metadata.AssemblyGeneratedDir derives
		// path.Dir(File)+"/generated" => "assemblies/<id>/generated".
		// Before M1's Locator funnel, AssemblyGeneratedDir had an internal
		// "if no examples/ prefix then assemblies/<id>" fallback that masked
		// missing-File; the fallback was removed so synthesized meta must
		// declare File explicitly.
		File: "assemblies/" + spec.ID.String() + "/assembly.yaml",
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
	helperName, err := assemblyRunHelperName(spec.ID.String())
	if err != nil {
		return scaffoldAssemblyContext{}, errcode.Wrap(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"assembly id has no identifier characters", err,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalAssemblyQuotedFmt, spec.ID.String()))))
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
		cellIDStr := cellID.String()
		cellMeta := g.cells.Get(cellIDStr)
		// Cell existence already validated; fall back to cellID when
		// GoStructName is unset so legacy cells still produce a compilable stub.
		structName := cellIDStr
		if cellMeta != nil && !cellMeta.GoStructName.IsZero() {
			structName = cellMeta.GoStructName.String()
		}
		cellModuleEntries = append(cellModuleEntries, scaffoldAssemblyCellEntry{
			Name: structName + "Module",
			ID:   cellIDStr,
		})
		quotedCells = append(quotedCells, yamlsafe.Quote(cellIDStr))
	}

	return scaffoldAssemblyContext{
		ID:             yamlsafe.Quote(spec.ID.String()),
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
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("path=%s", f.Path))))
		}
		plan = append(plan, pathsafe.PlannedFile{
			AbsPath: absPath,
			Content: out,
		})
	}
	return plan, nil
}

// validateAssemblyScaffoldSpec checks required fields and verifies that every
// cell in spec.Cells exists in the parsed project. Identifier pattern
// validation is no longer performed here: spec.ID and spec.Cells[] are typed
// (scaffoldid.ScaffoldID), so the AssemblyIDPattern (`^[a-z][a-z0-9]+$`)
// constraint is established at construction time via scaffoldid.Parse
// (SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01). Free-text rules (OwnerTeam /
// OwnerRole) still route through kernel/metadata.IsValidMetadataText —
// the metadata package is the sole declaration site.
//
// ref: kubernetes/apimachinery pkg/util/validation/validation.go —
// IsDNS1123Label single-helper validation; same pattern applied here.
func validateAssemblyScaffoldSpec(g *Generator, spec AssemblyScaffoldSpec) error {
	if spec.ID.IsZero() {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"assembly scaffold: ID is required")
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
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("field=OwnerTeam value=%q", spec.OwnerTeam))))
	}
	if spec.OwnerRole == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"assembly scaffold: OwnerRole is required")
	}
	if !metadata.IsValidMetadataText(spec.OwnerRole) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"assembly scaffold: OwnerRole contains forbidden control characters",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("field=OwnerRole value=%q", spec.OwnerRole))))
	}
	if spec.Deploy != "" && !metadata.IsKnownDeployTemplate(spec.Deploy) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"assembly scaffold: --deploy must be one of [k8s compose binary]",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("deploy=%q", spec.Deploy))))
	}
	// spec.Cells is []scaffoldid.ScaffoldID — the AssemblyIDPattern
	// (^[a-z][a-z0-9]+$) is identical to CellIDPattern, so a typed entry has
	// already passed pattern validation at scaffoldid.Parse time. We still
	// verify each cell exists in the parsed project.
	for _, cellID := range spec.Cells {
		if g.cells.Get(cellID.String()) == nil {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"assembly scaffold: --cells references unknown cell",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("cell=%q", cellID.String()))))
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
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("contract=%q", contractID))))
		}
		consumers, consErr := g.contracts.Consumers(contractID)
		if consErr != nil {
			return nil, nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed,
				"boundary: resolve consumers failed", consErr,
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("contract=%q", contractID))))
		}
		kind := ""
		if meta := g.contracts.Get(contractID); meta != nil {
			kind = meta.Kind
		}
		classifyBoundary(contractID, kind, provider, consumers, cellSet, exportedSet, importedSet)
	}

	exported = sortedKeys(exportedSet)
	imported = sortedKeys(importedSet)
	return exported, imported, nil
}

// classifyBoundary categorizes a single contract as exported, imported, or internal
// relative to the assembly cell set.
func classifyBoundary(contractID, kind, provider string, consumers []string, cellSet, exportedSet, importedSet map[string]bool) {
	// Saga contracts are provider-only orchestration definitions, not an external
	// API surface: the orchestrating cell drives the workflow and there is no
	// consumer/invoker actor set (registry.Consumers returns nil for saga). That
	// nil consumer list would otherwise be read by isExportedContract as
	// "exported", leaking the saga definition onto the assembly's external
	// boundary. Saga is always internal — neither exported nor imported.
	if kind == "saga" {
		return
	}
	if cellSet[provider] {
		if isExportedContract(consumers, cellSet) {
			exportedSet[contractID] = true
		}
	} else {
		if hasInternalConsumer(consumers, cellSet) {
			importedSet[contractID] = true
		}
	}
}

// isExportedContract reports whether a provider-in-assembly contract is
// exported: it has no consumers, or at least one consumer is outside the
// assembly.
func isExportedContract(consumers []string, cellSet map[string]bool) bool {
	if len(consumers) == 0 {
		return true
	}
	for _, c := range consumers {
		if !cellSet[c] {
			return true
		}
	}
	return false
}

// hasInternalConsumer reports whether at least one consumer of a
// provider-outside-assembly contract belongs to the assembly.
func hasInternalConsumer(consumers []string, cellSet map[string]bool) bool {
	for _, c := range consumers {
		if cellSet[c] {
			return true
		}
	}
	return false
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
	for i, ref := range asm.Cells {
		if err := writeHash(h, "cells.order:%d:%s\n", i, ref.ID); err != nil {
			return err
		}
		// Same-module entries (Module == "") keep the historical fingerprint
		// byte-for-byte so existing boundary.yaml outputs do not churn; a
		// cross-module entry adds its module to the hash.
		if ref.Module != "" {
			if err := writeHash(h, "cells.module:%d:%s\n", i, ref.Module); err != nil {
				return err
			}
		}
	}
	for _, ref := range asm.Cells {
		if err := g.hashCellIdentity(h, ref.ID); err != nil {
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
	e.Receivers = sortedCopy(e.Receivers)
	e.Dispatchers = sortedCopy(e.Dispatchers)
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
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalTemplateQuotedFmt, name))))
	}

	tmpl, err := template.New(name).Parse(string(content))
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"failed to parse template", err,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalTemplateQuotedFmt, name))))
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"failed to execute template", err,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalTemplateQuotedFmt, name))))
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

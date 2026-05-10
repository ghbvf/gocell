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

// AssemblyScaffoldSpec drives Generator.Scaffold (K#09 SCAFFOLD-ONE-CMD).
// The Generator already owns the assembly.yaml / run.go / app.go renderers
// and downstream codegen (modules_gen.go, main.go, boundary.yaml) so a
// single Scaffold method composes the full assembly bundle without a new
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
	// DryRun renders templates and runs conflict detection without writing.
	DryRun bool
}

// scaffoldAssemblyContext is the template context for the K#09 scaffold
// templates (assembly-yaml / run-go / app-go).
type scaffoldAssemblyContext struct {
	ID             string
	Cells          []string
	OwnerTeam      string
	OwnerRole      string
	DeployTemplate string                      // empty when --deploy=k8s (default — omitted from yaml)
	HelperName     string                      // run{ID-PascalCase} for runXxx() in run.go
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

// Scaffold produces an assembly skeleton: assembly.yaml + cmd/{id}/run.go +
// cmd/{id}/app.go. K#09 SCAFFOLD-ONE-CMD.
//
// Note: Scaffold writes to the filesystem under projectRoot. The Generator
// must have been constructed with a non-empty projectRoot. Each cell in
// spec.Cells must exist in g.project.Cells; unknown cell IDs fail-fast.
//
// On --deploy=k8s (the K#10 minimal default) the scaffolded assembly.yaml
// omits the deployTemplate field per ADR 202605061800. compose/binary are
// written verbatim. Callers may run GenerateModulesGen / GenerateEntrypoint
// / GenerateBoundary after Scaffold to materialize cmd/{id}/main.go +
// modules_gen.go + assemblies/{id}/generated/boundary.yaml.
func (g *Generator) Scaffold(spec AssemblyScaffoldSpec) error {
	if g.projectRoot == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"assembly.Generator.Scaffold requires non-empty projectRoot")
	}
	if err := validateAssemblyScaffoldSpec(g, spec); err != nil {
		return err
	}

	ctx, err := g.buildScaffoldContext(spec)
	if err != nil {
		return err
	}

	asmDir := filepath.Join(g.projectRoot, "assemblies", spec.ID)
	cmdDir := filepath.Join(g.projectRoot, "cmd", spec.ID)

	files := []scaffoldAssemblyFile{
		{Path: filepath.Join(asmDir, "assembly.yaml"), Template: "scaffold-assembly-yaml.tpl"},
		{Path: filepath.Join(cmdDir, "run.go"), Template: "scaffold-run-go.tpl"},
		{Path: filepath.Join(cmdDir, "app.go"), Template: "scaffold-app-go.tpl"},
	}

	rendered, err := g.renderAssemblyScaffoldFiles(files, ctx)
	if err != nil {
		return err
	}

	if spec.DryRun {
		return nil
	}
	return writeAssemblyScaffoldFiles([]string{asmDir, cmdDir}, rendered)
}

// scaffoldAssemblyFile pairs an output path with the template used to render
// it; lifted out of Scaffold for readability and to keep funlen happy.
type scaffoldAssemblyFile struct {
	Path     string
	Template string
}

// buildScaffoldContext builds the scaffoldAssemblyContext for a spec by
// resolving the helper name, normalizing the deploy template, and collecting
// per-cell Module stub entries.
func (g *Generator) buildScaffoldContext(spec AssemblyScaffoldSpec) (scaffoldAssemblyContext, error) {
	helperName, err := assemblyRunHelperName(spec.ID)
	if err != nil {
		return scaffoldAssemblyContext{}, errcode.Wrap(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"assembly id has no identifier characters", err,
			errcode.WithInternal(fmt.Sprintf(internalAssemblyQuotedFmt, spec.ID)))
	}

	deployTemplate := spec.Deploy
	if deployTemplate == "k8s" {
		// K#10 minimal default — parser/schema inherits k8s; omit from yaml.
		deployTemplate = ""
	}

	cellModuleEntries := make([]scaffoldAssemblyCellEntry, 0, len(spec.Cells))
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
	}

	return scaffoldAssemblyContext{
		ID:             spec.ID,
		Cells:          append([]string(nil), spec.Cells...),
		OwnerTeam:      spec.OwnerTeam,
		OwnerRole:      spec.OwnerRole,
		DeployTemplate: deployTemplate,
		HelperName:     helperName,
		CellModules:    cellModuleEntries,
	}, nil
}

// renderAssemblyScaffoldFiles runs conflict detection then renders each
// template; the returned map is keyed by output path so the writer step
// stays trivial.
func (g *Generator) renderAssemblyScaffoldFiles(files []scaffoldAssemblyFile, ctx scaffoldAssemblyContext) (map[string][]byte, error) {
	for _, f := range files {
		if _, err := os.Stat(f.Path); err == nil {
			return nil, errcode.New(errcode.KindConflict, errcode.ErrValidationFailed,
				"scaffold assembly: file already exists",
				errcode.WithInternal(fmt.Sprintf("path=%s", f.Path)))
		}
	}
	rendered := make(map[string][]byte, len(files))
	for _, f := range files {
		out, err := g.executeTemplate(f.Template, ctx)
		if err != nil {
			return nil, err
		}
		rendered[f.Path] = out
	}
	return rendered, nil
}

// writeAssemblyScaffoldFiles materializes rendered bytes — directories first,
// then files — wrapping every error with errcode so callers see the failing
// path.
func writeAssemblyScaffoldFiles(dirs []string, rendered map[string][]byte) error {
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
				"scaffold assembly: mkdir failed", err,
				errcode.WithInternal(fmt.Sprintf("dir=%s", dir)))
		}
	}
	for path, content := range rendered {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
				"scaffold assembly: write failed", err,
				errcode.WithInternal(fmt.Sprintf("path=%s", path)))
		}
	}
	return nil
}

// validateAssemblyScaffoldSpec checks required fields and verifies that every
// cell in spec.Cells exists in the parsed project. Unknown cell IDs are
// rejected with KindInvalid so `gocell scaffold assembly --cells=...` cannot
// silently produce an assembly that points at non-existent cells.
func validateAssemblyScaffoldSpec(g *Generator, spec AssemblyScaffoldSpec) error {
	if spec.ID == "" {
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
	if spec.OwnerRole == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"assembly scaffold: OwnerRole is required")
	}
	switch spec.Deploy {
	case "", "k8s", "compose", "binary":
		// ok — empty defaults to k8s
	default:
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"assembly scaffold: --deploy must be one of [k8s compose binary]",
			errcode.WithInternal(fmt.Sprintf("deploy=%q", spec.Deploy)))
	}
	for _, cellID := range spec.Cells {
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
// build config, runtime cell order, and per-cell structural metadata.
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

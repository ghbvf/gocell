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
	"os"
	"path/filepath"
	"sort"
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
// files; passing "" causes fingerprinting to skip schema content hashing,
// which is acceptable only for tests that use contracts without SchemaRefs.
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

// GenerateEntrypoint generates the main.go content for an assembly.
func (g *Generator) GenerateEntrypoint(assemblyID string) ([]byte, error) {
	asm := g.project.Assemblies[assemblyID]
	if asm == nil {
		return nil, errcode.New(errcode.ErrAssemblyNotFound,
			fmt.Sprintf("assembly %q not found", assemblyID))
	}

	ctx := entrypointContext{
		Module:     g.module,
		AssemblyID: assemblyID,
		Cells:      sortedCopy(asm.Cells),
	}

	return g.executeTemplate("main.go.tpl", ctx)
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
		return nil, errcode.New(errcode.ErrAssemblyNotFound,
			fmt.Sprintf("assembly %q not found", assemblyID))
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

// computeBoundaryContracts determines which contracts cross the assembly boundary.
func (g *Generator) computeBoundaryContracts(cellSet map[string]bool) (exported, imported []string, err error) {
	exportedSet := make(map[string]bool)
	importedSet := make(map[string]bool)

	for _, contractID := range g.contracts.AllIDs() {
		provider, provErr := g.contracts.Provider(contractID)
		if provErr != nil {
			return nil, nil, errcode.Wrap(errcode.ErrValidationFailed,
				fmt.Sprintf("boundary: resolve provider for %q", contractID), provErr)
		}
		consumers, consErr := g.contracts.Consumers(contractID)
		if consErr != nil {
			return nil, nil, errcode.Wrap(errcode.ErrValidationFailed,
				fmt.Sprintf("boundary: resolve consumers for %q", contractID), consErr)
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
	g.hashAssemblyIdentity(h, asm)

	if err := g.hashBoundaryContracts(h, exported, imported); err != nil {
		return "", err
	}

	// Record the boundary membership lists so that adding or removing a contract
	// from the boundary also shifts the fingerprint.
	fmt.Fprint(h, "exported:") //nolint:errcheck
	for _, cID := range exported {
		fmt.Fprintf(h, "%s\x00", cID) //nolint:errcheck
	}
	fmt.Fprint(h, "imported:") //nolint:errcheck
	for _, cID := range imported {
		fmt.Fprintf(h, "%s\x00", cID) //nolint:errcheck
	}

	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// hashAssemblyIdentity writes the assembly's stable identity fields into h:
// build config, sorted cell list, and per-cell structural metadata.
func (g *Generator) hashAssemblyIdentity(h hashWriter, asm *metadata.AssemblyMeta) {
	cells := sortedCopy(asm.Cells)
	// hash.Hash.Write never returns error — safe to ignore per Go spec.
	fmt.Fprintf(h, "assembly:%s\n", asm.ID)                       //nolint:errcheck
	fmt.Fprintf(h, "build.entrypoint:%s\n", asm.Build.Entrypoint) //nolint:errcheck
	fmt.Fprintf(h, "build.binary:%s\n", asm.Build.Binary)         //nolint:errcheck
	for _, c := range cells {
		fmt.Fprintf(h, "cells:%s\n", c) //nolint:errcheck
	}
	for _, cellID := range cells {
		cellMeta := g.cells.Get(cellID)
		if cellMeta == nil {
			fmt.Fprintf(h, "cell:%s:missing\n", cellID) //nolint:errcheck
			continue
		}
		fmt.Fprintf(h, "cell:%s:type:%s\n", cellID, cellMeta.Type)                    //nolint:errcheck
		fmt.Fprintf(h, "cell:%s:consistency:%s\n", cellID, cellMeta.ConsistencyLevel) //nolint:errcheck
		fmt.Fprintf(h, "cell:%s:owner:%s\n", cellID, cellMeta.Owner.Team)             //nolint:errcheck
		fmt.Fprintf(h, "cell:%s:schema:%s\n", cellID, cellMeta.Schema.Primary)        //nolint:errcheck
		for _, s := range cellMeta.Verify.Smoke {
			fmt.Fprintf(h, "cell:%s:smoke:%s\n", cellID, s) //nolint:errcheck
		}
	}
}

// hashBoundaryContracts writes each boundary contract's canonical encoding into h.
// Contracts are visited in sorted ID order to ensure determinism.
func (g *Generator) hashBoundaryContracts(h hashWriter, exported, imported []string) error {
	allContracts := make([]string, 0, len(exported)+len(imported))
	allContracts = append(allContracts, exported...)
	allContracts = append(allContracts, imported...)
	sort.Strings(allContracts)

	for _, cID := range allContracts {
		c := g.contracts.Get(cID)
		// Write the contract ID as a separator even for nil contracts.
		fmt.Fprintf(h, "contract:%s\x00", cID) //nolint:errcheck
		if c == nil {
			fmt.Fprint(h, "nil\n") //nolint:errcheck
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
// for the contract. Paths are resolved relative to the contract's Dir.
func writeSchemaFileContents(h hashWriter, projectRoot string, c *metadata.ContractMeta) error {
	if c == nil || projectRoot == "" {
		return nil
	}
	contractDir := filepath.Join(projectRoot, filepath.FromSlash(c.Dir))
	refs := []struct {
		key  string
		path string
	}{
		{"schema.request", c.SchemaRefs.Request},
		{"schema.response", c.SchemaRefs.Response},
		{"schema.payload", c.SchemaRefs.Payload},
		{"schema.headers", c.SchemaRefs.Headers},
	}
	for _, r := range refs {
		if r.path == "" {
			continue
		}
		absPath := filepath.Join(contractDir, r.path)
		content, err := os.ReadFile(absPath)
		if err != nil {
			return fmt.Errorf("fingerprint: read schema %s for contract %q: %w", r.path, c.ID, err)
		}
		fmt.Fprintf(h, "%s:%s:", r.key, r.path) //nolint:errcheck
		_, _ = h.Write(content)
		fmt.Fprint(h, "\n") //nolint:errcheck
	}
	keys := sortedMapKeys(c.SchemaRefs.Extra)
	for _, k := range keys {
		path := c.SchemaRefs.Extra[k]
		if path == "" {
			continue
		}
		absPath := filepath.Join(contractDir, path)
		content, err := os.ReadFile(absPath)
		if err != nil {
			return fmt.Errorf("fingerprint: read schema %s for contract %q: %w", path, c.ID, err)
		}
		fmt.Fprintf(h, "schema.extra.%s:%s:", k, path) //nolint:errcheck
		_, _ = h.Write(content)
		fmt.Fprint(h, "\n") //nolint:errcheck
	}
	return nil
}

// hashWriter is the interface satisfied by hash.Hash for fingerprint writes.
type hashWriter interface {
	Write(p []byte) (n int, err error)
}

// executeTemplate loads a template from the embedded FS, parses it, and
// executes it with the given context.
func (g *Generator) executeTemplate(name string, ctx any) ([]byte, error) {
	content, err := gentpl.FS.ReadFile(name)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrMetadataInvalid,
			fmt.Sprintf("failed to read template %q", name), err)
	}

	tmpl, err := template.New(name).Parse(string(content))
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrMetadataInvalid,
			fmt.Sprintf("failed to parse template %q", name), err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return nil, errcode.Wrap(errcode.ErrMetadataInvalid,
			fmt.Sprintf("failed to execute template %q", name), err)
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

// sortedMapKeys returns sorted keys from any string-keyed map.
func sortedMapKeys[V any](m map[string]V) []string {
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

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
	"sort"
	"text/template"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly/gentpl"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/registry"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Generator produces derived files for an assembly.
type Generator struct {
	project   *metadata.ProjectMeta
	cells     *registry.CellRegistry
	contracts *registry.ContractRegistry
	module    string // Go module path (e.g., "github.com/ghbvf/gocell")
}

// NewGenerator creates a Generator from project metadata and a Go module path.
// It builds CellRegistry and ContractRegistry internally from the project.
func NewGenerator(project *metadata.ProjectMeta, module string) *Generator {
	return &Generator{
		project:   project,
		cells:     registry.NewCellRegistry(project),
		contracts: registry.NewContractRegistry(project),
		module:    module,
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
	GeneratedAt       string
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
	fingerprint := g.sourceFingerprint(assemblyID, exported, imported)

	ctx := boundaryContext{
		GeneratedAt:       time.Now().UTC().Format(time.RFC3339),
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
			return nil, nil, fmt.Errorf("boundary: resolve provider for %q: %w", contractID, provErr)
		}
		consumers, consErr := g.contracts.Consumers(contractID)
		if consErr != nil {
			return nil, nil, fmt.Errorf("boundary: resolve consumers for %q: %w", contractID, consErr)
		}

		providerInAssembly := cellSet[provider]

		// Exported: provider is inside, and either no consumers or at least
		// one consumer is outside.
		if providerInAssembly {
			if len(consumers) == 0 {
				exportedSet[contractID] = true
			} else {
				for _, consumer := range consumers {
					if !cellSet[consumer] {
						exportedSet[contractID] = true
						break
					}
				}
			}
		}

		// Imported: at least one consumer is inside, and provider is outside.
		if !providerInAssembly {
			for _, consumer := range consumers {
				if cellSet[consumer] {
					importedSet[contractID] = true
					break
				}
			}
		}
	}

	exported = sortedKeys(exportedSet)
	imported = sortedKeys(importedSet)
	return exported, imported, nil
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

// sourceFingerprint computes a SHA-256 hex digest of all source YAML for the
// assembly. It hashes the assembly ID and all related cell IDs in sorted order
// to produce a deterministic fingerprint. The exported/imported boundary
// contracts are passed in to avoid recomputing them.
func (g *Generator) sourceFingerprint(assemblyID string, exported, imported []string) string {
	asm := g.project.Assemblies[assemblyID]
	if asm == nil {
		return ""
	}

	h := sha256.New()
	cells := sortedCopy(asm.Cells)

	// Hash assembly identity.
	// hash.Hash.Write never returns error — safe to ignore per Go spec.
	fmt.Fprintf(h, "assembly:%s\n", asm.ID) //nolint:errcheck
	for _, c := range cells {
		fmt.Fprintf(h, "cells:%s\n", c) //nolint:errcheck
	}
	fmt.Fprintf(h, "build.entrypoint:%s\n", asm.Build.Entrypoint) //nolint:errcheck
	fmt.Fprintf(h, "build.binary:%s\n", asm.Build.Binary)         //nolint:errcheck

	// Hash cell metadata in sorted order for determinism.
	cellSet := make(map[string]bool, len(asm.Cells))
	for _, cellID := range cells {
		cellSet[cellID] = true
		cellMeta := g.cells.Get(cellID)
		if cellMeta == nil {
			fmt.Fprintf(h, "cell:%s:missing\n", cellID)
			continue
		}
		fmt.Fprintf(h, "cell:%s:type:%s\n", cellID, cellMeta.Type)
		fmt.Fprintf(h, "cell:%s:consistency:%s\n", cellID, cellMeta.ConsistencyLevel)
		fmt.Fprintf(h, "cell:%s:owner:%s\n", cellID, cellMeta.Owner.Team)
		fmt.Fprintf(h, "cell:%s:schema:%s\n", cellID, cellMeta.Schema.Primary)
		for _, s := range cellMeta.Verify.Smoke {
			fmt.Fprintf(h, "cell:%s:smoke:%s\n", cellID, s)
		}
	}

	// Hash boundary contracts so that endpoint changes invalidate the fingerprint.
	for _, cID := range exported {
		fmt.Fprintf(h, "export:%s\n", cID)
	}
	for _, cID := range imported {
		fmt.Fprintf(h, "import:%s\n", cID)
	}

	return fmt.Sprintf("%x", h.Sum(nil))
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

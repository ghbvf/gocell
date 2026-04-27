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
	"sort"
	"strconv"
	"strings"
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
		GeneratedAt:       sourceDateEpochOrNow(),
		Fingerprint:       fingerprint,
		AssemblyID:        assemblyID,
		ExportedContracts: exported,
		ImportedContracts: imported,
		SmokeTargets:      smokeTargets,
	}

	return g.executeTemplate("boundary.yaml.tpl", ctx)
}

// sourceDateEpochOrNow returns a deterministic RFC3339 timestamp when the
// SOURCE_DATE_EPOCH environment variable is set (Unix seconds), otherwise
// returns the current UTC time. This enables reproducible builds.
// ref: https://reproducible-builds.org/docs/source-date-epoch/
func sourceDateEpochOrNow() string {
	if v := os.Getenv("SOURCE_DATE_EPOCH"); v != "" {
		if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
			return time.Unix(secs, 0).UTC().Format(time.RFC3339)
		}
	}
	return time.Now().UTC().Format(time.RFC3339)
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

	// Hash boundary contracts with full structural detail. Each contract's
	// kind, lifecycle, consistency level, transport details (HTTP method/path/
	// status/listener/responses, event/command/projection identifiers) and
	// participant lists are folded in so that any structural drift — even one
	// that leaves the contract ID unchanged — invalidates the fingerprint and
	// triggers a boundary.yaml regenerate.
	for _, cID := range exported {
		writeContractFingerprint(h, "export", cID, g.contracts.Get(cID))
	}
	for _, cID := range imported {
		writeContractFingerprint(h, "import", cID, g.contracts.Get(cID))
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}

// listenerKindFromPath classifies an HTTP path by its listener stripe. Paths
// under /internal/ map to the InternalListener; everything else (api, admin,
// webhooks) maps to PrimaryListener. Path moves between stripes are a load-
// bearing structural change that callers must regenerate boundary.yaml on.
func listenerKindFromPath(path string) string {
	if strings.HasPrefix(path, "/internal/") {
		return "internal"
	}
	return "primary"
}

func sortedIntKeys(m map[int]metadata.HTTPResponseMeta) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

// hashWriter is the interface satisfied by hash.Hash for fingerprint writes.
type hashWriter interface {
	Write(p []byte) (n int, err error)
}

func writeContractFingerprint(h hashWriter, prefix, cID string, c *metadata.ContractMeta) {
	fmt.Fprintf(h, "%s:%s\n", prefix, cID) //nolint:errcheck
	if c == nil {
		return
	}
	fmt.Fprintf(h, "%s:%s:kind:%s\n", prefix, cID, c.Kind)                    //nolint:errcheck
	fmt.Fprintf(h, "%s:%s:lifecycle:%s\n", prefix, cID, c.Lifecycle)          //nolint:errcheck
	fmt.Fprintf(h, "%s:%s:consistency:%s\n", prefix, cID, c.ConsistencyLevel) //nolint:errcheck
	fmt.Fprintf(h, "%s:%s:idempotency:%s\n", prefix, cID, c.IdempotencyKey)   //nolint:errcheck
	fmt.Fprintf(h, "%s:%s:delivery:%s\n", prefix, cID, c.DeliverySemantics)   //nolint:errcheck
	if c.Replayable != nil {
		fmt.Fprintf(h, "%s:%s:replayable:%t\n", prefix, cID, *c.Replayable) //nolint:errcheck
	}
	for _, t := range sortedCopy(c.Triggers) {
		fmt.Fprintf(h, "%s:%s:trigger:%s\n", prefix, cID, t) //nolint:errcheck
	}
	writeContractEndpointsFingerprint(h, prefix, cID, c)
}

// writeContractEndpointsFingerprint folds kind-specific endpoint fields into
// the fingerprint stream. Split out from writeContractFingerprint to keep
// gocognit ≤ 15 (gocell governance threshold).
func writeContractEndpointsFingerprint(h hashWriter, prefix, cID string, c *metadata.ContractMeta) {
	switch c.Kind {
	case "http":
		fmt.Fprintf(h, "%s:%s:server:%s\n", prefix, cID, c.Endpoints.Server) //nolint:errcheck
		for _, x := range sortedCopy(c.Endpoints.Clients) {
			fmt.Fprintf(h, "%s:%s:client:%s\n", prefix, cID, x) //nolint:errcheck
		}
		writeHTTPTransportFingerprint(h, prefix, cID, c.Endpoints.HTTP)
	case "event":
		fmt.Fprintf(h, "%s:%s:publisher:%s\n", prefix, cID, c.Endpoints.Publisher) //nolint:errcheck
		for _, x := range sortedCopy(c.Endpoints.Subscribers) {
			fmt.Fprintf(h, "%s:%s:subscriber:%s\n", prefix, cID, x) //nolint:errcheck
		}
	case "command":
		fmt.Fprintf(h, "%s:%s:handler:%s\n", prefix, cID, c.Endpoints.Handler) //nolint:errcheck
		for _, x := range sortedCopy(c.Endpoints.Invokers) {
			fmt.Fprintf(h, "%s:%s:invoker:%s\n", prefix, cID, x) //nolint:errcheck
		}
	case "projection":
		fmt.Fprintf(h, "%s:%s:provider:%s\n", prefix, cID, c.Endpoints.Provider) //nolint:errcheck
		for _, x := range sortedCopy(c.Endpoints.Readers) {
			fmt.Fprintf(h, "%s:%s:reader:%s\n", prefix, cID, x) //nolint:errcheck
		}
	}
}

func writeHTTPTransportFingerprint(h hashWriter, prefix, cID string, t *metadata.HTTPTransportMeta) {
	if t == nil {
		return
	}
	fmt.Fprintf(h, "%s:%s:method:%s\n", prefix, cID, t.Method)                       //nolint:errcheck
	fmt.Fprintf(h, "%s:%s:path:%s\n", prefix, cID, t.Path)                           //nolint:errcheck
	fmt.Fprintf(h, "%s:%s:listener:%s\n", prefix, cID, listenerKindFromPath(t.Path)) //nolint:errcheck
	fmt.Fprintf(h, "%s:%s:status:%d\n", prefix, cID, t.SuccessStatus)                //nolint:errcheck
	fmt.Fprintf(h, "%s:%s:noContent:%t\n", prefix, cID, t.NoContent)                 //nolint:errcheck
	for _, status := range sortedIntKeys(t.Responses) {
		fmt.Fprintf(h, "%s:%s:resp:%d\n", prefix, cID, status) //nolint:errcheck
	}
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

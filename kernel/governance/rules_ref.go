package governance

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// validateREF01 checks that slice.belongsToCell references an existing cell.
func (v *Validator) validateREF01() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		if _, ok := v.project.Cells[s.BelongsToCell]; !ok {
			results = append(results, v.newResult(
				"REF-01", SeverityError, IssueRefNotFound,
				sliceFile(s),
				"belongsToCell",
				fmt.Sprintf("slice %q references non-existent cell %q", s.ID, s.BelongsToCell),
			))
		}
	}
	return results
}

// validateREF02 checks that slice.contractUsages[].contract references an existing contract.
func (v *Validator) validateREF02() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		for i, cu := range s.ContractUsages {
			if _, ok := v.project.Contracts[cu.Contract]; !ok {
				results = append(results, v.newResult(
					"REF-02", SeverityError, IssueRefNotFound,
					sliceFile(s),
					fmt.Sprintf("contractUsages[%d].contract", i),
					fmt.Sprintf("slice %q references non-existent contract %q", s.ID, cu.Contract),
				))
			}
		}
	}
	return results
}

// validateREF03 checks that contract.ownerCell is a cell (not an external actor).
func (v *Validator) validateREF03() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if _, ok := v.project.Cells[c.OwnerCell]; !ok {
			results = append(results, v.newResult(
				"REF-03", SeverityError, IssueRefNotFound,
				contractFile(c),
				"ownerCell",
				fmt.Sprintf("contract %q ownerCell %q is not a known cell", c.ID, c.OwnerCell),
			))
		}
	}
	return results
}

// validateREF04 checks that cell.id equals the filesystem directory name.
// The check reads CellMeta.Dir (populated by the parser from the walked
// path) rather than the map key, because the map key is also m.ID — using
// the key degenerates into a tautology that cannot catch a path/id split.
// Cells synthesized in tests without a Dir are skipped.
func (v *Validator) validateREF04() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Cells {
		if c.Dir == "" {
			continue
		}
		if c.ID != c.Dir {
			results = append(results, v.newResult(
				"REF-04", SeverityError, IssueRefNotFound,
				cellFile(c),
				"id",
				fmt.Sprintf("cell id %q does not match directory name %q", c.ID, c.Dir),
			))
		}
	}
	return results
}

// validateREF05 checks that slice.id equals the filesystem directory name.
// The check reads SliceMeta.Dir (populated by the parser) rather than the
// map key, because the map key embeds m.ID and self-comparing m.ID against
// itself can never fail. Slices synthesized in tests without a Dir are
// skipped.
func (v *Validator) validateREF05() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		if s.Dir == "" {
			continue
		}
		if s.ID != s.Dir {
			results = append(results, v.newResult(
				"REF-05", SeverityError, IssueRefNotFound,
				sliceFile(s),
				"id",
				fmt.Sprintf("slice id %q does not match directory name %q", s.ID, s.Dir),
			))
		}
	}
	return results
}

// validateREF06 checks that journey.cells[] references existing cells.
func (v *Validator) validateREF06() []ValidationResult {
	var results []ValidationResult
	for _, j := range v.project.Journeys {
		for i, cellRef := range j.Cells {
			if _, ok := v.project.Cells[cellRef]; !ok {
				results = append(results, v.newResult(
					"REF-06", SeverityError, IssueRefNotFound,
					journeyFile(j),
					fmt.Sprintf("cells[%d]", i),
					fmt.Sprintf("journey %q references non-existent cell %q", j.ID, cellRef),
				))
			}
		}
	}
	return results
}

// validateREF07 checks that journey.contracts[] references existing contracts.
func (v *Validator) validateREF07() []ValidationResult {
	var results []ValidationResult
	for _, j := range v.project.Journeys {
		for i, cRef := range j.Contracts {
			if _, ok := v.project.Contracts[cRef]; !ok {
				results = append(results, v.newResult(
					"REF-07", SeverityError, IssueRefNotFound,
					journeyFile(j),
					fmt.Sprintf("contracts[%d]", i),
					fmt.Sprintf("journey %q references non-existent contract %q", j.ID, cRef),
				))
			}
		}
	}
	return results
}

// validateREF08 checks that assembly.cells[] references existing cells.
func (v *Validator) validateREF08() []ValidationResult {
	var results []ValidationResult
	for _, a := range v.project.Assemblies {
		for i, cellRef := range a.Cells {
			if _, ok := v.project.Cells[cellRef]; !ok {
				results = append(results, v.newResult(
					"REF-08", SeverityError, IssueRefNotFound,
					assemblyFile(a),
					fmt.Sprintf("cells[%d]", i),
					fmt.Sprintf("assembly %q references non-existent cell %q", a.ID, cellRef),
				))
			}
		}
	}
	return results
}

// validateREF09 checks that l0Dependencies[].cell references an existing cell.
func (v *Validator) validateREF09() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Cells {
		for i, dep := range c.L0Dependencies {
			if _, ok := v.project.Cells[dep.Cell]; !ok {
				results = append(results, v.newResult(
					"REF-09", SeverityError, IssueRefNotFound,
					cellFile(c),
					fmt.Sprintf("l0Dependencies[%d].cell", i),
					fmt.Sprintf("cell %q l0Dependencies references non-existent cell %q", c.ID, dep.Cell),
				))
			}
		}
	}
	return results
}

// validateREF10 checks that every assembly has a non-empty build.entrypoint.
func (v *Validator) validateREF10() []ValidationResult {
	var results []ValidationResult
	for _, a := range v.project.Assemblies {
		if a.Build.Entrypoint == "" {
			results = append(results, v.newResult(
				"REF-10", SeverityError, IssueRequired,
				assemblyFile(a),
				"build.entrypoint",
				fmt.Sprintf("assembly %q must have build.entrypoint", a.ID),
			))
		}
	}
	return results
}

// validateREF11 checks that assembly.build.entrypoint file exists on disk.
// Skipped when root is empty.
func (v *Validator) validateREF11() []ValidationResult {
	if v.root == "" {
		return nil
	}
	var results []ValidationResult
	for _, a := range v.project.Assemblies {
		if a.Build.Entrypoint == "" {
			continue // REF-10 covers this
		}
		// The entrypoint path is relative to the repository root (parent of go.mod directory).
		repoRoot := repositoryRoot(v.root)
		fullPath := filepath.Join(repoRoot, a.Build.Entrypoint)
		if !IsWithinRoot(repoRoot, fullPath) {
			results = append(results, v.newResult(
				"REF-11", SeverityError, IssueInvalid,
				assemblyFile(a),
				"build.entrypoint",
				fmt.Sprintf("assembly %q build.entrypoint %q: path escapes project root", a.ID, a.Build.Entrypoint),
			))
			continue
		}
		if !v.fileExists(fullPath) {
			results = append(results, v.newResult(
				"REF-11", SeverityError, IssueRefNotFound,
				assemblyFile(a),
				"build.entrypoint",
				fmt.Sprintf("assembly %q build.entrypoint %q does not exist", a.ID, a.Build.Entrypoint),
			))
		}
	}
	return results
}

// validateREF12 checks that contract.schemaRefs files exist on disk.
// Skipped when root is empty.
func (v *Validator) validateREF12() []ValidationResult {
	if v.root == "" {
		return nil
	}
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		contractDir := filepath.Join(v.root, contractDirFromMeta(c))
		results = append(results, v.checkREF12SchemaRefs(c, contractDir)...)
		results = append(results, v.checkREF12Responses(c, contractDir)...)
	}
	return results
}

// checkREF12SchemaRefs validates the schemaRefs.* fields of a single contract.
func (v *Validator) checkREF12SchemaRefs(c *metadata.ContractMeta, contractDir string) []ValidationResult {
	type refEntry struct {
		field string
		value string
	}
	refs := []refEntry{
		{"schemaRefs.request", c.SchemaRefs.Request},
		{"schemaRefs.response", c.SchemaRefs.Response},
		{"schemaRefs.payload", c.SchemaRefs.Payload},
		{"schemaRefs.headers", c.SchemaRefs.Headers},
	}
	for key, val := range c.SchemaRefs.Extra {
		refs = append(refs, refEntry{fmt.Sprintf("schemaRefs.%s", key), val})
	}

	var results []ValidationResult
	for _, ref := range refs {
		results = append(results, v.checkSchemaRefFile(c.ID, contractDir, ref.field, ref.value)...)
	}
	return results
}

// checkREF12Responses validates endpoints.http.responses[N].schemaRef entries.
// These were introduced in PR#181 alongside HTTPTransportMeta.Responses but were
// not previously walked by the governance layer.
//
// Unlike schemaRefs.* (which must stay inside the contract directory), response
// schemas may reference shared schemas via relative paths that navigate up to
// the contracts/shared/ subtree. The bounds check therefore uses the project root
// (v.root) rather than the contract directory.
func (v *Validator) checkREF12Responses(c *metadata.ContractMeta, contractDir string) []ValidationResult {
	if c.Endpoints.HTTP == nil {
		return nil
	}
	var results []ValidationResult
	for status, resp := range c.Endpoints.HTTP.Responses {
		field := fmt.Sprintf("endpoints.http.responses[%d].schemaRef", status)
		results = append(results, v.checkSchemaRefFileWithRoot(c.ID, contractDir, v.root, field, resp.SchemaRef)...)
	}
	return results
}

// checkSchemaRefFile checks that a single schemaRef value points to an existing
// file within the contract directory (boundsRoot == contractDir).
// Empty values are silently skipped.
func (v *Validator) checkSchemaRefFile(contractID, contractDir, field, value string) []ValidationResult {
	return v.checkSchemaRefFileWithRoot(contractID, contractDir, contractDir, field, value)
}

// checkSchemaRefFileWithRoot resolves value relative to contractDir and checks
// that the result exists on disk and does not escape boundsRoot. Empty values
// are silently skipped.
func (v *Validator) checkSchemaRefFileWithRoot(contractID, contractDir, boundsRoot, field, value string) []ValidationResult {
	if value == "" {
		return nil
	}
	fullPath := filepath.Join(contractDir, value)
	if !IsWithinRoot(boundsRoot, fullPath) {
		return []ValidationResult{v.newResult(
			"REF-12", SeverityError, IssueInvalid,
			contractFileFromID(contractID),
			field,
			fmt.Sprintf("contract %q %s %q: path escapes project root", contractID, field, value),
		)}
	}
	if !v.fileExists(fullPath) {
		return []ValidationResult{v.newResult(
			"REF-12", SeverityError, IssueRefNotFound,
			contractFileFromID(contractID),
			field,
			fmt.Sprintf("contract %q %s points to missing file %q", contractID, field, value),
		)}
	}
	return nil
}

// validateREF13 checks that the contract provider actor exists as a cell or actor.
func (v *Validator) validateREF13() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		provider := contractProvider(c)
		if provider == "" {
			continue // FMT-07 covers missing provider
		}
		if !v.actorExists(provider) {
			results = append(results, v.newResult(
				"REF-13", SeverityError, IssueRefNotFound,
				contractFile(c),
				"endpoints",
				fmt.Sprintf("contract %q provider actor %q is not a known cell or actor", c.ID, provider),
			))
		}
	}
	return results
}

// validateREF14 checks that all contract consumer actors exist as cells or actors.
// The wildcard "*" is skipped.
func (v *Validator) validateREF14() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		consumers := contractConsumers(c)
		for i, actor := range consumers {
			if isWildcardConsumer(actor) {
				continue
			}
			if !v.actorExists(actor) {
				results = append(results, v.newResult(
					"REF-14", SeverityError, IssueRefNotFound,
					contractFile(c),
					// The YAML key depends on kind: clients / subscribers /
					// invokers / readers. Using a logical name "consumers"
					// here would defeat the locator because it does not exist
					// in the source file.
					fmt.Sprintf("endpoints.%s[%d]", consumerFieldName(c.Kind), i),
					fmt.Sprintf("contract %q consumer actor %q is not a known cell or actor", c.ID, actor),
				))
			}
		}
	}
	return results
}

// validateREF15 checks that assembly.id matches the map key (directory name).
func (v *Validator) validateREF15() []ValidationResult {
	var results []ValidationResult
	for _, a := range v.project.Assemblies {
		if a.ID != a.Dir {
			results = append(results, v.newResult(
				"REF-15", SeverityError, IssueMismatch,
				assemblyFile(a),
				"id",
				fmt.Sprintf("assembly id %q does not match map key %q (expected directory name)", a.ID, a.Dir),
			))
		}
	}
	return results
}

// validateREF16 checks that each assembly has a generated boundary.yaml file.
// The boundary.yaml is produced by `gocell generate` and lives at
// assemblies/{id}/generated/boundary.yaml relative to the metadata root (v.root).
// Note: unlike REF-11 which uses repositoryRoot for entrypoint paths,
// boundary.yaml lives under the metadata root alongside other metadata dirs.
// Skipped when root is empty (no filesystem checks).
func (v *Validator) validateREF16() []ValidationResult {
	if v.root == "" {
		return nil
	}
	var results []ValidationResult
	for _, a := range v.project.Assemblies {
		boundaryPath := filepath.Join(v.root, "assemblies", a.ID, "generated", "boundary.yaml")
		if !IsWithinRoot(v.root, boundaryPath) {
			results = append(results, v.newResult(
				"REF-16", SeverityError, IssueInvalid,
				assemblyFile(a),
				"id",
				fmt.Sprintf("assembly %q boundary.yaml path escapes project root", a.ID),
			))
			continue
		}
		if !v.fileExists(boundaryPath) {
			results = append(results, v.newResult(
				"REF-16", SeverityWarning, IssueRefNotFound,
				assemblyFile(a),
				"id",
				fmt.Sprintf("assembly %q has no generated boundary.yaml at assemblies/%s/generated/boundary.yaml; run 'gocell generate' to create it", a.ID, a.ID),
			))
		}
	}
	return results
}

// validateREF17 checks that HTTP contracts on the internal audience
// (cell.InternalPathPrefix) do not list any external actor as a client.
// Internal endpoints are reserved for cell-to-cell traffic and admin/ops
// callers reached through trusted internal listeners; routing a registered
// external actor through them bypasses the public-API contract surface and
// the auth posture that comes with it.
//
// Audience comes from the runtime path-prefix SoR (kernel/cell), not a
// governance-local string, so router/registrar/admission stay aligned.
//
// External-actor membership comes from actors.yaml: every entry is external
// by construction (see ActorMeta godoc). The wildcard "*" client is also
// rejected on internal paths — its allow-all semantics include external
// actors, which is exactly what this rule forbids.
//
// ref: kubernetes pkg/apis/core/validation/validation.go (audience-aware
// admission). We diverge from k8s by validating contract.endpoints.clients
// against actors.yaml membership rather than RBAC roles.
func (v *Validator) validateREF17() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != "http" || c.Endpoints.HTTP == nil {
			continue
		}
		path := c.Endpoints.HTTP.Path
		if !strings.HasPrefix(path, cell.InternalPathPrefix) {
			continue
		}
		for i, client := range c.Endpoints.Clients {
			field := fmt.Sprintf("endpoints.%s[%d]", consumerFieldName(c.Kind), i)
			switch {
			case client == "*":
				results = append(results, v.newResult(
					"REF-17", SeverityError, IssueForbidden,
					contractFile(c), field,
					fmt.Sprintf("contract %q is internal (path %q) but clients contains wildcard %q; wildcards admit external actors, list explicit internal cell IDs instead", c.ID, path, client),
				))
			case v.isExternalActor(client):
				results = append(results, v.newResult(
					"REF-17", SeverityError, IssueForbidden,
					contractFile(c), field,
					fmt.Sprintf("contract %q is internal (path %q) but client %q is registered in actors.yaml (external); remove it or move the endpoint to a public path", c.ID, path, client),
				))
			}
		}
	}
	return results
}

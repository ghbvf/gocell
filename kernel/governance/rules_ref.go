package governance

import (
	"fmt"
	"path/filepath"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/fspath"
)

const fieldBuildEntrypoint = "build.entrypoint"

// validateREF01 checks that slice.belongsToCell references an existing cell.
func (v *Validator) validateREF01() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		if _, ok := v.project.Cells[s.BelongsToCell]; !ok {
			results = append(results, v.newError(
				codeREF01, IssueRefNotFound,
				sliceFile(s),
				"belongsToCell",
				fmt.Sprintf("slice %q references non-existent cell %q", s.ID, s.BelongsToCell),
				"update belongsToCell to an existing cell id",
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
				results = append(results, v.newError(
					codeREF02, IssueRefNotFound,
					sliceFile(s),
					fmt.Sprintf("contractUsages[%d].contract", i),
					fmt.Sprintf("slice %q references non-existent contract %q", s.ID, cu.Contract),
					"add the contract to contracts/ or remove this contractUsage",
				))
			}
		}
	}
	return results
}

// validateREF03 checks that a cell-owned contract's ownerCell is a known cell.
//
// Owner kind is read through the sealed ContractOwner funnel (c.Owner()), not by
// indexing project.Cells with the raw OwnerCell string: a FRAMEWORK-owned
// contract resolves Owner().Cell() to ok=false and is structurally skipped here
// (it is governed by FRAMEWORK-OWNED-CONTRACT-SCOPED-01 instead). A cell owner
// with a typo'd id still resolves to a cell and fires this rule, so REF-03 keeps
// its value.
func (v *Validator) validateREF03() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		cellID, isCell := c.Owner().Cell()
		if !isCell {
			continue // framework-owned: see FRAMEWORK-OWNED-CONTRACT-SCOPED-01
		}
		if _, ok := v.project.Cells[cellID]; !ok {
			results = append(results, v.newError(
				codeREF03, IssueRefNotFound,
				contractFile(c),
				"ownerCell",
				fmt.Sprintf("contract %q ownerCell %q is not a known cell", c.ID, cellID),
				"set ownerCell to an existing cell id",
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
			results = append(results, v.newError(
				codeREF04, IssueRefNotFound,
				cellFile(c),
				"id",
				fmt.Sprintf("cell id %q does not match directory name %q", c.ID, c.Dir),
				"rename the directory to match the cell id",
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
			results = append(results, v.newError(
				codeREF05, IssueRefNotFound,
				sliceFile(s),
				"id",
				fmt.Sprintf("slice id %q does not match directory name %q", s.ID, s.Dir),
				"rename the directory to match the slice id",
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
				results = append(results, v.newError(
					codeREF06, IssueRefNotFound,
					journeyFile(j),
					fmt.Sprintf("cells[%d]", i),
					fmt.Sprintf("journey %q references non-existent cell %q", j.ID, cellRef),
					"remove the cell reference or create the cell",
				))
			}
		}
	}
	return results
}

// validateREF07 checks that journey.contracts[] references existing metadata.
func (v *Validator) validateREF07() []ValidationResult {
	var results []ValidationResult
	for _, j := range v.project.Journeys {
		for i, cRef := range j.Contracts {
			if _, ok := v.project.Contracts[cRef]; !ok {
				results = append(results, v.newError(
					codeREF07, IssueRefNotFound,
					journeyFile(j),
					fmt.Sprintf("contracts[%d]", i),
					fmt.Sprintf("journey %q references non-existent contract %q", j.ID, cRef),
					"remove the contract reference or create the contract",
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
		for i, ref := range a.Cells {
			if _, ok := v.project.Cells[ref.ID]; !ok {
				results = append(results, v.newError(
					codeREF08, IssueRefNotFound,
					assemblyFile(a),
					fmt.Sprintf("cells[%d]", i),
					fmt.Sprintf("assembly %q references non-existent cell %q", a.ID, ref.ID),
					"remove the cell reference or create the cell",
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
				results = append(results, v.newError(
					codeREF09, IssueRefNotFound,
					cellFile(c),
					fmt.Sprintf("l0Dependencies[%d].cell", i),
					fmt.Sprintf("cell %q l0Dependencies references non-existent cell %q", c.ID, dep.Cell),
					"remove the dependency or create the referenced cell",
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
			results = append(results, v.newError(
				codeREF10, IssueRequired,
				assemblyFile(a),
				fieldBuildEntrypoint,
				fmt.Sprintf("assembly %q must have build.entrypoint", a.ID),
				"set build.entrypoint to the main package path",
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
		if !fspath.IsWithinRoot(repoRoot, fullPath) {
			results = append(results, v.newError(
				codeREF11, IssueInvalid,
				assemblyFile(a),
				fieldBuildEntrypoint,
				fmt.Sprintf("assembly %q build.entrypoint %q: path escapes project root", a.ID, a.Build.Entrypoint),
				"use a path relative to the repository root",
			))
			continue
		}
		if !v.fileExists(fullPath) {
			results = append(results, v.newError(
				codeREF11, IssueRefNotFound,
				assemblyFile(a),
				fieldBuildEntrypoint,
				fmt.Sprintf("assembly %q build.entrypoint %q does not exist", a.ID, a.Build.Entrypoint),
				"create the entrypoint file or correct the path",
			))
		}
	}
	return results
}

// validateREF13 checks that the contract provider actor exists as a cell or actor.
//
// Framework-owned contracts are skipped: their provider IS the framework (the
// provider endpoint resolves to the FrameworkOwnerSentinel, which is neither a
// cell nor an actors.yaml entry by design). They are governed by
// FRAMEWORK-OWNED-CONTRACT-SCOPED-01 instead.
func (v *Validator) validateREF13() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Owner().IsFramework() {
			continue // framework-owned: provider is the framework, not a cell/actor
		}
		provider := contractProvider(c)
		if provider == "" {
			continue // FMT-07 covers missing provider
		}
		if !v.actorExists(provider) {
			results = append(results, v.newError(
				codeREF13, IssueRefNotFound,
				contractFile(c),
				"endpoints",
				fmt.Sprintf("contract %q provider actor %q is not a known cell or actor", c.ID, provider),
				"register the actor in actors.yaml or use an existing cell id",
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
				results = append(results, v.newError(
					codeREF14, IssueRefNotFound,
					contractFile(c),
					// The YAML key depends on kind: clients / subscribers /
					// invokers / readers. Using a logical name "consumers"
					// here would defeat the locator because it does not exist
					// in the source file.
					fmt.Sprintf("endpoints.%s[%d]", consumerFieldName(c.Kind), i),
					fmt.Sprintf("contract %q consumer actor %q is not a known cell or actor", c.ID, actor),
					"register the actor in actors.yaml or use an existing cell id",
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
			results = append(results, v.newError(
				codeREF15, IssueMismatch,
				assemblyFile(a),
				"id",
				fmt.Sprintf("assembly id %q does not match map key %q (expected directory name)", a.ID, a.Dir),
				"rename the directory to match the assembly id",
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
		generatedDir := metadata.AssemblyGeneratedDir(a)
		boundaryPath := filepath.Join(v.root, filepath.FromSlash(generatedDir), "boundary.yaml")
		if !fspath.IsWithinRoot(v.root, boundaryPath) {
			results = append(results, v.newError(
				codeREF16, IssueInvalid,
				assemblyFile(a),
				"id",
				fmt.Sprintf("assembly %q boundary.yaml path escapes project root", a.ID),
				"ensure the assembly id does not contain path traversal characters",
			))
			continue
		}
		if !v.fileExists(boundaryPath) {
			results = append(results, v.newWarning(
				codeREF16, IssueRefNotFound,
				assemblyFile(a),
				"id",
				fmt.Sprintf(
					"assembly %q has no generated boundary.yaml at %s/boundary.yaml",
					a.ID, generatedDir,
				),
				"run 'gocell generate' to create the boundary.yaml",
			))
		}
	}
	return results
}

// validateREF17 checks that HTTP contracts on the internal audience
// (metadata.IsInternalHTTPPath) do not list any external actor as a client.
// Internal endpoints are reserved for cell-to-cell traffic and admin/ops
// callers reached through trusted internal listeners; routing a registered
// external actor through them bypasses the public-API contract surface and
// the auth posture that comes with it.
//
// Audience comes from metadata.IsInternalHTTPPath — the single source of
// truth that also drives FMT-28 (auth.clientsOnly placement), FMT-31
// (caller-clients required on internal paths), and runtime listener
// attribution. Using the canonical predicate (rather than inlining
// strings.HasPrefix on cellvocab.InternalPathPrefix) keeps the bare
// "/internal/v1" root in scope: a contract declared at the root would
// otherwise slip past REF-17 with an external actor client.
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
		if !metadata.IsInternalHTTPPath(path) {
			continue
		}
		for i, client := range c.Endpoints.Clients {
			field := fmt.Sprintf("endpoints.%s[%d]", consumerFieldName(c.Kind), i)
			switch {
			case client == "*":
				results = append(results, v.newError(
					codeREF17, IssueForbidden,
					contractFile(c), field,
					fmt.Sprintf(
						"contract %q is internal (path %q) but clients contains wildcard %q;"+
							" wildcards admit external actors, list explicit internal cell IDs instead",
						c.ID, path, client,
					),
					"replace the wildcard with explicit cell IDs",
				))
			case v.isExternalActor(client):
				results = append(results, v.newError(
					codeREF17, IssueForbidden,
					contractFile(c), field,
					fmt.Sprintf(
						"contract %q is internal (path %q) but client %q is registered in actors.yaml (external);"+
							" remove it or move the endpoint to a public path",
						c.ID, path, client,
					),
					"remove the external actor from clients or change the endpoint path to a public prefix",
				))
			}
		}
	}
	return results
}

// validateREF18 checks that every entry in an event contract's
// endpoints.actorSubscribers is a registered EXTERNAL actor — not a cell ID and
// not a wildcard.
//
// Since the subscribe single-source flip, cell subscribers are DERIVED from
// slice.yaml contractUsages[role=subscribe] (a cell never appears in the
// contract.yaml). actorSubscribers is the one remaining hand-written subscriber
// list, reserved for external systems (no slice, not derivable) registered in
// actors.yaml. Without this rule, writing a cell ID or "*" into
// actorSubscribers would union straight into the derived Subscribers
// (parser.deriveEventSubscribers), silently re-opening the hand-written
// cell-subscriber bypass the flip was designed to close. REF-18 makes that
// bypass a governance error.
//
// External-actor membership comes from actors.yaml (every entry is external by
// construction — see ActorMeta godoc), mirroring REF-17's audience model.
func (v *Validator) validateREF18() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != "event" {
			continue
		}
		for i, sub := range c.Endpoints.ActorSubscribers {
			field := fmt.Sprintf("actorSubscribers[%d]", i)
			switch {
			case sub == "*":
				results = append(results, v.newError(
					codeREF18, IssueForbidden,
					contractFile(c), field,
					fmt.Sprintf(
						"event contract %q lists wildcard %q in actorSubscribers;"+
							" wildcards are not a valid external-actor identity",
						c.ID, sub,
					),
					"replace the wildcard with explicit external-actor IDs registered in actors.yaml",
				))
			case v.isCellID(sub):
				results = append(results, v.newError(
					codeREF18, IssueForbidden,
					contractFile(c), field,
					fmt.Sprintf(
						"event contract %q lists cell %q in actorSubscribers;"+
							" cell subscribers are derived from slice.yaml contractUsages[role=subscribe]"+
							" and must not be hand-written here",
						c.ID, sub,
					),
					"remove the cell from actorSubscribers and declare contractUsages[role=subscribe] in the cell's slice.yaml instead",
				))
			case !v.isExternalActor(sub):
				results = append(results, v.newError(
					codeREF18, IssueRefNotFound,
					contractFile(c), field,
					fmt.Sprintf(
						"event contract %q lists %q in actorSubscribers but it is not registered in actors.yaml",
						c.ID, sub,
					),
					"register the external subscriber in actors.yaml, or remove it from actorSubscribers",
				))
			}
		}
	}
	return results
}

// isCellID reports whether id names a cell in the project.
func (v *Validator) isCellID(id string) bool {
	_, ok := v.project.Cells[id]
	return ok
}

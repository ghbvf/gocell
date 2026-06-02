// Package metadata implements structural derivation for AssemblyMeta and related
// types. Derivation fills in omitted fields (entrypoint, binary, deployTemplate,
// maxConsistencyLevel) from declared field values and peer metadata.
//
// ref: parser.parseSlice G-7 belongsToCell auto-derive / parser.parseContract
// ownerCell auto-derive — same auto-derive pattern applied to assembly build
// fields and cross-cell consistency aggregation.
package metadata

import (
	"log/slog"
	"path"
	"path/filepath"

	"github.com/ghbvf/gocell/kernel/cellvocab"
)

// AssemblyGeneratedDir returns the project-relative path of the generated/
// directory for an assembly. It is derived uniformly as
// path.Dir(asm.File) + "/generated", which works for the conventional
// assemblies/<id>/ layout, the examples/<id>/ subtree, and arbitrary paths
// emitted by Locator manifest mode.
//
// This is the single source of truth shared by:
//   - kernel/assembly Generator.appendGeneratedFiles (boundary.yaml write path)
//   - cmd/gocell/app generateOneAssembly (boundary.yaml + modules_gen cmd)
//   - cmd/gocell/app generateMetricsSchema (metrics-schema.yaml write path)
//   - kernel/governance validateREF16 (boundary.yaml existence check)
//
// asm.File is the path as loaded by the parser — relative to the project
// root, with forward slashes (parser already normalised via filepath.ToSlash).
func AssemblyGeneratedDir(asm *AssemblyMeta) string {
	if asm == nil || asm.File == "" {
		return ""
	}
	return path.Join(path.Dir(filepath.ToSlash(asm.File)), "generated")
}

// applyAssemblyDerivations fills derived AssemblyMeta fields after parsing.
// Single source of truth for build defaults and MaxConsistencyLevel; the
// governance Validator only asserts derivations, never recomputes them.
//
// Layering note: this function performs structural derivation only — it does
// not validate referential integrity (unknown cell IDs, invalid level strings).
// Those failures are governance concerns (REF-* / TOPO-09) and would conflate
// parser concerns with validator concerns. When derivation cannot complete
// because of missing/invalid references, the affected field is left at its
// zero value so governance can report the underlying issue without being
// shadowed by a parser-level error.
func applyAssemblyDerivations(pm *ProjectMeta) {
	for _, asm := range pm.Assemblies {
		if asm == nil {
			continue
		}
		deriveAssembly(pm, asm)
	}
}

func deriveAssembly(pm *ProjectMeta, asm *AssemblyMeta) {
	if asm.Build.Entrypoint == "" {
		// Entrypoint derivation follows "identity by location" for assemblies
		// whose file lives outside the conventional assemblies/<id>/ directory
		// (examples subtree, Manifest-mode custom layout). For those, the
		// entrypoint is the main.go adjacent to the assembly.yaml.
		//
		// Conventional assemblies (assemblies/<id>/assembly.yaml) keep the
		// historical cmd/<id>/main.go entrypoint so that the conventional
		// starter layout is not silently broken — "cmd" is a conventional layout
		// token and is allowlisted in LOCATOR-DISCOVERY-FUNNEL-01.A5 for this
		// file only (see tools/archtest/locator_discovery_funnel_test.go godoc).
		//
		// ref: helm/helm pkg/chartutil/create.go, kustomize-sigs
		// pkg/types/kustomization.go — identity by location.
		if asm.File != "" && !IsConventionalAssemblyPath(asm.File) {
			// Non-conventional layout (examples/, Manifest-mode custom path):
			// use identity-by-location derivation.
			asm.Build.Entrypoint = path.Join(path.Dir(filepath.ToSlash(asm.File)), "main.go")
		} else {
			// Conventional layout (assemblies/<id>/) or unset File: preserve
			// the historical cmd/<id>/main.go entrypoint so the conventional
			// starter layout is not silently broken.
			asm.Build.Entrypoint = filepath.ToSlash(filepath.Join("cmd", asm.ID, "main.go"))
		}
		slog.Debug("metadata: assembly entrypoint derived",
			slog.String("assembly", asm.ID),
			slog.String("entrypoint", asm.Build.Entrypoint),
		)
	}
	if asm.Build.Binary == "" {
		asm.Build.Binary = asm.ID
		slog.Debug("metadata: assembly binary derived",
			slog.String("assembly", asm.ID),
			slog.String("binary", asm.Build.Binary),
		)
	}
	if asm.Build.DeployTemplate == "" {
		asm.Build.DeployTemplate = "k8s"
		slog.Debug("metadata: assembly deployTemplate derived to k8s default",
			slog.String("assembly", asm.ID),
		)
	}
	if maxLevel, ok := computeMaxConsistencyLevel(pm, asm); ok {
		asm.MaxConsistencyLevel = maxLevel
	} else {
		slog.Warn("metadata: assembly maxConsistencyLevel skipped — derive failed",
			slog.String("assembly", asm.ID),
			slog.String("reason", "unknown cell ref or invalid consistencyLevel — see governance REF/FMT-03"),
		)
	}
}

// computeMaxConsistencyLevel returns the max consistency level string among all
// cells referenced by asm. Returns ("L0", true) when asm.Cells is empty —
// empty cells returning L0 is a graceful default agreed with TOPO-09; TOPO-09
// itself skips empty-cells assemblies so there is no governance double-report.
// Returns (_, false) when any referenced cell is unknown or has an invalid
// ConsistencyLevel — those cases are governance failures (REF-* / FMT-03)
// and the field is left empty so the governance layer can report the
// underlying issue.
func computeMaxConsistencyLevel(pm *ProjectMeta, asm *AssemblyMeta) (string, bool) {
	if len(asm.Cells) == 0 {
		return "L0", true
	}
	maxRank := -1
	maxLevel := "L0"
	for _, ref := range asm.Cells {
		c, ok := pm.Cells[ref.ID]
		if !ok {
			return "", false
		}
		rank := cellvocab.Rank(c.ConsistencyLevel)
		if rank < 0 {
			return "", false
		}
		if rank > maxRank {
			maxRank = rank
			maxLevel = c.ConsistencyLevel
		}
	}
	return maxLevel, true
}

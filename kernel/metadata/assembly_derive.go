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
	"path/filepath"

	"github.com/ghbvf/gocell/kernel/cellvocab"
)

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
		asm.Build.Entrypoint = filepath.ToSlash(filepath.Join("cmd", asm.ID, "main.go"))
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
	for _, cellID := range asm.Cells {
		c, ok := pm.Cells[cellID]
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

package metadata

import "path/filepath"

// consistencyOrder maps level string to numeric rank for comparison.
// Mirrors kernel/cell.Level (L0=0..L4=4); kernel/metadata cannot import
// kernel/cell because kernel/cell imports kernel/metadata (cycle).
var consistencyOrder = map[string]int{
	"L0": 0,
	"L1": 1,
	"L2": 2,
	"L3": 3,
	"L4": 4,
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
		asm.Build.Entrypoint = filepath.ToSlash(filepath.Join("cmd", asm.ID, "main.go"))
	}
	if asm.Build.Binary == "" {
		asm.Build.Binary = asm.ID
	}
	if asm.Build.DeployTemplate == "" {
		asm.Build.DeployTemplate = "k8s"
	}
	if maxLevel, ok := computeMaxConsistencyLevel(pm, asm); ok {
		asm.MaxConsistencyLevel = maxLevel
	}
}

// computeMaxConsistencyLevel returns the max consistency level string among all
// cells referenced by asm. Returns ("L0", true) when asm.Cells is empty.
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
		rank, valid := consistencyOrder[c.ConsistencyLevel]
		if !valid {
			return "", false
		}
		if rank > maxRank {
			maxRank = rank
			maxLevel = c.ConsistencyLevel
		}
	}
	return maxLevel, true
}

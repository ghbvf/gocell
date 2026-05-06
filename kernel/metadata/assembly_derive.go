package metadata

import (
	"fmt"
	"path/filepath"

	"github.com/ghbvf/gocell/pkg/errcode"
)

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
func applyAssemblyDerivations(pm *ProjectMeta) error {
	for _, asm := range pm.Assemblies {
		if asm == nil {
			continue
		}
		if err := deriveAssembly(pm, asm); err != nil {
			return err
		}
	}
	return nil
}

func deriveAssembly(pm *ProjectMeta, asm *AssemblyMeta) error {
	if err := validateAssemblyOwner(asm); err != nil {
		return err
	}
	if asm.Build.Entrypoint == "" {
		asm.Build.Entrypoint = filepath.ToSlash(filepath.Join("cmd", asm.ID, "main.go"))
	}
	if asm.Build.Binary == "" {
		asm.Build.Binary = asm.ID
	}
	if asm.Build.DeployTemplate == "" {
		asm.Build.DeployTemplate = "k8s"
	}

	maxLevel, err := computeMaxConsistencyLevel(pm, asm)
	if err != nil {
		return err
	}
	asm.MaxConsistencyLevel = maxLevel
	return nil
}

func validateAssemblyOwner(asm *AssemblyMeta) error {
	if asm.Owner.Team == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"assembly owner.team is required",
			errcode.WithInternal(fmt.Sprintf("assembly=%q", asm.ID)))
	}
	return nil
}

// computeMaxConsistencyLevel returns the max consistency level string among all
// cells referenced by asm. Returns "L0" when asm.Cells is empty.
func computeMaxConsistencyLevel(pm *ProjectMeta, asm *AssemblyMeta) (string, error) {
	if len(asm.Cells) == 0 {
		return "L0", nil
	}
	maxRank := -1
	maxLevel := "L0"
	for i, cellID := range asm.Cells {
		c, ok := pm.Cells[cellID]
		if !ok {
			return "", errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
				"assembly references unknown cell",
				errcode.WithInternal(fmt.Sprintf("assembly=%q cell=%q index=%d", asm.ID, cellID, i)))
		}
		rank, valid := consistencyOrder[c.ConsistencyLevel]
		if !valid {
			return "", errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
				"assembly cell has invalid consistencyLevel",
				errcode.WithInternal(fmt.Sprintf("assembly=%q cell=%q level=%q", asm.ID, cellID, c.ConsistencyLevel)))
		}
		if rank > maxRank {
			maxRank = rank
			maxLevel = c.ConsistencyLevel
		}
	}
	return maxLevel, nil
}

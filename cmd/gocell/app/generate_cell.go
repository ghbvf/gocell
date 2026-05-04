package app

import (
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen/cellgen"
)

var generateCellSpec = codegenSpec[cellgen.Result]{
	Kind:            "cell",
	GenerateUsage:   "gocell generate cell <cellID> | --all [--dry-run | --verify]",
	AllFlagDesc:     "generate for every cell with goStructName set",
	PluralNoun:      "cell scaffolds",
	SourceArtifacts: "cell.yaml/slice.yaml",
	Generate: func(root string, p *metadata.ProjectMeta, dryRun, verify bool, only string) (cellgen.Result, error) {
		return cellgen.Generate(root, p, cellgen.Options{
			DryRun: dryRun, Verify: verify, OnlyCell: only,
		})
	},
}

func generateCell(args []string) error { return runCodegenGenerate(generateCellSpec, args) }

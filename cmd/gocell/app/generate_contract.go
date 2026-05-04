package app

import (
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen/contractgen"
)

var generateContractSpec = codegenSpec[contractgen.Result]{
	Kind:            "contract",
	GenerateUsage:   "gocell generate contract <contractID> | --all [--dry-run | --verify]",
	AllFlagDesc:     "generate for every contract with codegen=true",
	PluralNoun:      "contract DTOs",
	SourceArtifacts: "contract.yaml / schema files",
	Generate: func(root string, p *metadata.ProjectMeta, dryRun, verify bool, only string) (contractgen.Result, error) {
		return contractgen.Generate(root, p, contractgen.Options{
			DryRun: dryRun, Verify: verify, OnlyContract: only,
		})
	},
}

func generateContract(args []string) error { return runCodegenGenerate(generateContractSpec, args) }

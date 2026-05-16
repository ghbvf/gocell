// stage_render.go — ephemeral staging dir helpers for cross-stage scaffold.
//
// INVARIANT: SCAFFOLD-CELL-BUNDLE-CROSS-STAGE-PLAN-MERGE-01
//
// materializeSkeletonStage and appendDerivedCodegen together implement the
// ephemeral-staging pattern that merges skeleton + codegen-derived artifacts
// into a single pathsafe.WritePlannedFiles plan for PlanCellBundleScaffold.
//
// Staging strategy (zero new OS-ban exemptions):
//   - os.MkdirTemp / os.RemoveAll are NOT in the SCAFFOLD-WRITE-FUNNEL-01
//     banned set (MkdirAll / WriteFile / Mkdir / Create / OpenFile); they only
//     manage the temporary directory lifetime.
//   - Skeleton files are written into the staging root via
//     pathsafe.WritePlannedFiles itself — staging USES the funnel, not bypass.
//   - The shared error-response schema is copied into staging as a PlannedFile
//     so contractgen's relative SchemaRef resolution can find it.
//   - Derived artifacts are rendered in-memory against the staging tree and
//     rebased onto realRoot with ForceOverwrite=true before merging.
//
// The depguard scaffold-os-ban rule covers files starting with "scaffold" or
// "generate_" in tools/codegen/cellgen/; this file does not match either
// prefix and is therefore not in scope. Direct os.MkdirTemp / os.RemoveAll
// calls here are intentional and reviewed.
package cellgen

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/pathsafe"
	"github.com/ghbvf/gocell/tools/codegen/contractgen"
)

// sharedErrorSchemaRelPath is the repo-relative path of the shared error
// response schema. contractgen BuildContractSpec follows SchemaRef links that
// point to this file; it must be present in the staging tree for renders to
// succeed.
const sharedErrorSchemaRelPath = "contracts/shared/errors/error-response-v1.schema.json"

// materializeSkeletonStage writes the skeleton plan into a temporary staging
// directory via pathsafe.WritePlannedFiles (funnel reuse, not bypass) and
// returns the staging root path. The caller must defer os.RemoveAll(stageRoot).
//
// The shared error schema is copied from realRoot into the staging plan so
// contractgen can resolve relative SchemaRef links in scaffolded contract.yaml
// files without requiring the schema to be present in the real project tree.
//
// Returns the staging root path or an error.
func materializeSkeletonStage(realRoot string, skeletonPlan []pathsafe.PlannedFile) (stageRoot string, err error) {
	stageRoot, err = os.MkdirTemp("", "gocell-scaffold-stage-*")
	if err != nil {
		return "", errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"scaffold stage: create temp dir", err)
	}

	// Build the staging plan by rebasing skeleton AbsPaths from realRoot → stageRoot.
	stagePlan := make([]pathsafe.PlannedFile, 0, len(skeletonPlan)+1)
	for _, f := range skeletonPlan {
		rel, relErr := filepath.Rel(realRoot, f.AbsPath)
		if relErr != nil {
			return "", errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
				"scaffold stage: rebase skeleton path", relErr,
				errcode.WithDetails(slog.String("path", f.AbsPath)))
		}
		stagePlan = append(stagePlan, pathsafe.PlannedFile{
			AbsPath:        filepath.Join(stageRoot, rel),
			Content:        f.Content,
			ForceOverwrite: f.ForceOverwrite,
		})
	}

	// Copy the shared error schema from realRoot so contractgen can resolve
	// relative SchemaRef paths in the scaffolded contract.yaml.
	schemaAbs := filepath.Join(realRoot, filepath.FromSlash(sharedErrorSchemaRelPath))
	schemaContent, readErr := os.ReadFile(schemaAbs) //nolint:gosec // known fixed path under project root
	if readErr == nil {
		// Only include when present — projects may not have the shared schema yet.
		stagePlan = append(stagePlan, pathsafe.PlannedFile{
			AbsPath: filepath.Join(stageRoot, filepath.FromSlash(sharedErrorSchemaRelPath)),
			Content: schemaContent,
		})
	}

	// Write skeleton + schema into the staging root via the funnel.
	if writeErr := pathsafe.WritePlannedFiles(stageRoot, stagePlan, false); writeErr != nil {
		return "", errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"scaffold stage: materialize skeleton", writeErr)
	}
	return stageRoot, nil
}

// appendDerivedCodegenStaged is the top-level entry point for the ephemeral
// staging lifecycle. It creates a temp staging dir, materializes the skeleton
// via the pathsafe funnel, renders derived artifacts, rebases them to realRoot,
// appends to skeletonPlan with ForceOverwrite=true, and removes the staging dir.
// Called from scaffold_bundle.go (which is in the depguard scaffold-os-ban list
// and therefore cannot import "os" directly).
func appendDerivedCodegenStaged(realRoot, cellID string, skeletonPlan []pathsafe.PlannedFile) ([]pathsafe.PlannedFile, error) {
	stageRoot, err := materializeSkeletonStage(realRoot, skeletonPlan)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(stageRoot) }()

	return appendDerivedCodegen(realRoot, stageRoot, cellID, skeletonPlan)
}

// appendDerivedCodegen parses the staging root, renders contractgen and cellgen
// artifacts in-memory (contract first, then cell so DTO types are available),
// and appends them to mergedPlan with AbsPaths rebased to realRoot and
// ForceOverwrite=true. The staging root is ephemeral and must be cleaned up by
// the caller.
func appendDerivedCodegen(realRoot, stageRoot, cellID string, mergedPlan []pathsafe.PlannedFile) ([]pathsafe.PlannedFile, error) {
	project, err := metadata.NewParser(stageRoot).Parse()
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"scaffold stage: parse staged metadata", err,
			errcode.WithDetails(slog.String("cellID", cellID)))
	}

	// Contract artifacts first so generated DTO types are available for cellgen.
	contractIDs, err := contractgen.ContractIDsForCell(project, cellID)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"scaffold stage: list contracts for cell", err,
			errcode.WithDetails(slog.String("cellID", cellID)))
	}
	for _, cid := range contractIDs {
		artifacts, artErr := contractgen.RenderContractArtifacts(stageRoot, project, cid)
		if artErr != nil {
			return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
				"scaffold stage: render contract artifacts", artErr,
				errcode.WithDetails(slog.String("contractID", cid)))
		}
		for _, a := range artifacts {
			// a.Path is stageRoot-relative (slash-separated).
			absStage := filepath.Join(stageRoot, filepath.FromSlash(a.Path))
			rel, relErr := filepath.Rel(stageRoot, absStage)
			if relErr != nil {
				return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
					"scaffold stage: rebase contract artifact", relErr,
					errcode.WithDetails(slog.String("artifactPath", a.Path)))
			}
			mergedPlan = append(mergedPlan, pathsafe.PlannedFile{
				AbsPath:        filepath.Join(realRoot, rel),
				Content:        a.Content,
				ForceOverwrite: true,
			})
		}
	}

	// Cell artifacts (cell_gen.go + slice_gen.go).
	cellArtifacts, err := RenderCellArtifacts(stageRoot, project, cellID)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"scaffold stage: render cell artifacts", err,
			errcode.WithDetails(slog.String("cellID", cellID)))
	}
	for _, a := range cellArtifacts {
		// a.RelPath is stageRoot-relative.
		mergedPlan = append(mergedPlan, pathsafe.PlannedFile{
			AbsPath:        filepath.Join(realRoot, filepath.FromSlash(a.RelPath)),
			Content:        a.Content,
			ForceOverwrite: true,
		})
	}

	return mergedPlan, nil
}

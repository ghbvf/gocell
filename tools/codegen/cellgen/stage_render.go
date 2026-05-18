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
//     rebased onto realRoot via planDerivedArtifact, which restores the
//     governance.IsGoCellGenerated overwrite gate (a pre-existing
//     non-generated file on realRoot is refused, not silently replaced) and
//     is the sole ForceOverwrite=true PlannedFile constructor for the derived
//     path (archtest SCAFFOLD-DERIVED-FORCEOVERWRITE-01).
//
// The depguard scaffold-os-ban rule covers files starting with "scaffold" or
// "generate_" in tools/codegen/cellgen/; stage_render.go IS also scanned by
// archtest SCAFFOLD-WRITE-FUNNEL-01 (scaffoldFunnelPred accepts base=="stage_render.go").
// Direct os.MkdirTemp / os.RemoveAll / os.ReadFile calls here are intentional
// and reviewed; write-side ops (MkdirAll/WriteFile/Mkdir/Create/OpenFile) remain
// banned and would trip the archtest.
//
// Temp-dir cleanup: materializeSkeletonStage uses a named-return + deferred
// cleanup so that any inner failure (filepath.Rel / WritePlannedFiles) removes
// the staging dir before returning. appendDerivedCodegenStaged additionally
// defers RemoveAll for the success path.
package cellgen

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/pathsafe"
	"github.com/ghbvf/gocell/tools/codegen/contractgen"
)

// stageTempParent is the parent directory passed to os.MkdirTemp when
// materializing the skeleton staging tree. Empty (the default) means
// os.TempDir() — production behavior is unchanged. Tests override it with an
// isolated per-test directory so concurrent leak assertions
// (TestMaterializeSkeletonStage_NoLeakOnInnerFailure / cross-stage rollback)
// scan only their own staging parent and cannot misattribute a sibling
// parallel test's gocell-scaffold-stage-* dir as a leak.
var stageTempParent = ""

// planDerivedArtifact is the SINGLE constructor for a ForceOverwrite derived
// codegen PlannedFile. It restores the governance gate that the legacy
// codegen.Write (tools/codegen/writer.go) enforced before PR #544 routed
// derived writes through the pathsafe single-plan funnel: a file that already
// exists on realRoot and does NOT carry the gocell generator header
// (governance.IsGoCellGenerated) is refused, never silently overwritten.
//
// relSlashPath is a stageRoot-relative slash path (artifact.Path / RelPath);
// it is rebased onto realRoot via pathsafe.ContainPath (defense-in-depth
// parent-symlink walk). ForceOverwrite is set true only when the target is
// absent or a prior gocell-generated artifact — exactly the codegen-regenerate
// case. Direct construction of pathsafe.PlannedFile{ForceOverwrite:true} in
// the derived-append path is statically forbidden by archtest
// SCAFFOLD-DERIVED-FORCEOVERWRITE-01, which keeps this gate unbypassable
// (Medium downstream; upstream Hard tracked by backlog
// PATHSAFE-FORCEOVERWRITE-TYPED-CTOR-01).
func planDerivedArtifact(realRoot, relSlashPath string, content []byte) (pathsafe.PlannedFile, error) {
	absReal, err := pathsafe.ContainPath(realRoot, filepath.FromSlash(relSlashPath))
	if err != nil {
		return pathsafe.PlannedFile{}, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"scaffold stage: rebase derived artifact", err,
			errcode.WithDetails(slog.String("artifactPath", relSlashPath)))
	}
	existing, readErr := os.ReadFile(absReal) //nolint:gosec // contained path under realRoot (ContainPath above); governance-gate read
	switch {
	case readErr == nil:
		if !governance.IsGoCellGenerated(existing) {
			return pathsafe.PlannedFile{}, errcode.New(errcode.KindConflict, errcode.ErrConflict,
				"scaffold stage: refusing to overwrite non-generated file "+
					"(remove the file or move hand-written code to a sibling location, then re-run scaffold)",
				errcode.WithDetails(slog.String("artifactPath", relSlashPath)))
		}
	case os.IsNotExist(readErr):
		// Absent → fresh write. ForceOverwrite is still set (uniform plan
		// entry); pathsafe conflictPass skips it and writePass captureOriginal
		// records kindNone, identical to a plain create.
	default:
		return pathsafe.PlannedFile{}, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"scaffold stage: stat derived artifact target", readErr,
			errcode.WithDetails(slog.String("artifactPath", relSlashPath)))
	}
	return pathsafe.DerivedOverwrite(absReal, content), nil
}

// sharedErrorSchemaRelPath is the repo-relative path of the shared error
// response schema. contractgen BuildContractSpec follows SchemaRef links that
// point to this file; it must be present in the staging tree for renders to
// succeed.
const sharedErrorSchemaRelPath = "contracts/shared/errors/error-response-v1.schema.json"

// materializeSkeletonStage writes the skeleton plan into a temporary staging
// directory via pathsafe.WritePlannedFiles (funnel reuse, not bypass) and
// returns the resolved staging root path. Cleanup of the staging dir is
// internal: if any step after MkdirTemp fails, the dir is removed before
// returning. On success, the caller (appendDerivedCodegenStaged) defers the
// final RemoveAll.
//
// realRoot must already be the output of pathsafe.ResolveRoot.
//
// The shared error schema is copied from realRoot into the staging plan so
// contractgen can resolve relative SchemaRef links in scaffolded contract.yaml
// files without requiring the schema to be present in the real project tree.
// If the schema is absent in realRoot, a Warn is logged and the file is omitted
// from staging (contractgen will fail gracefully if it actually requires it).
//
// Returns the resolved staging root path or an error.
func materializeSkeletonStage(realRoot string, skeletonPlan []pathsafe.PlannedFile) (_ string, err error) {
	rawStage, mkErr := os.MkdirTemp(stageTempParent, "gocell-scaffold-stage-*")
	if mkErr != nil {
		return "", errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"scaffold stage: create temp dir", mkErr)
	}

	// Resolve symlinks on the staging root (e.g. macOS /var→/private/var) so
	// pathsafe.WritePlannedFiles ContainPath checks work correctly.
	stageRoot, resolveErr := pathsafe.ResolveRoot(rawStage)
	if resolveErr != nil {
		_ = os.RemoveAll(rawStage)
		return "", errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"scaffold stage: resolve temp dir", resolveErr,
			errcode.WithInternal(fmt.Sprintf("stageRoot=%s", rawStage)))
	}

	// If any step below fails, remove the staging dir before returning.
	// stageRoot is captured by closure — not overwritten on error return path —
	// so RemoveAll always targets the correct directory.
	defer func() {
		if err != nil {
			_ = os.RemoveAll(stageRoot)
		}
	}()

	// Build the staging plan by rebasing skeleton AbsPaths from realRoot → stageRoot.
	stagePlan := make([]pathsafe.PlannedFile, 0, len(skeletonPlan)+1)
	for _, f := range skeletonPlan {
		rel, relErr := filepath.Rel(realRoot, f.AbsPath)
		if relErr != nil {
			err = errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
				"scaffold stage: rebase skeleton path", relErr,
				errcode.WithDetails(slog.String("path", f.AbsPath)),
				errcode.WithInternal(fmt.Sprintf("stageRoot=%s", stageRoot)))
			return "", err
		}
		// Skeleton entries never carry forceOverwrite (only DerivedOverwrite
		// constructs that flag, and skeletonPlan precedes derived rendering),
		// so the rebase is a fresh PlannedFile.
		stagePlan = append(stagePlan, pathsafe.PlannedFile{
			AbsPath: filepath.Join(stageRoot, rel),
			Content: f.Content,
		})
	}

	// Copy the shared error schema from realRoot so contractgen can resolve
	// relative SchemaRef paths in the scaffolded contract.yaml.
	// If absent, log a warning — projects may not have the shared schema yet.
	schemaAbs := filepath.Join(realRoot, filepath.FromSlash(sharedErrorSchemaRelPath))
	schemaContent, readErr := os.ReadFile(schemaAbs) //nolint:gosec // known fixed path under project root
	if readErr == nil {
		// Only include when present.
		stagePlan = append(stagePlan, pathsafe.PlannedFile{
			AbsPath: filepath.Join(stageRoot, filepath.FromSlash(sharedErrorSchemaRelPath)),
			Content: schemaContent,
		})
	} else {
		slog.Warn("scaffold stage: shared error schema not found; derived codegen may be incomplete",
			slog.String("path", schemaAbs))
	}

	// Write skeleton + schema into the staging root via the funnel.
	stageSet, planErr := pathsafe.NewPlanSet(stagePlan)
	if planErr != nil {
		err = errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"scaffold stage: build staging PlanSet", planErr,
			errcode.WithInternal(fmt.Sprintf("stageRoot=%s", stageRoot)))
		return "", err
	}
	if writeErr := pathsafe.WritePlannedFiles(stageRoot, stageSet, false); writeErr != nil {
		err = errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"scaffold stage: materialize skeleton", writeErr,
			errcode.WithInternal(fmt.Sprintf("stageRoot=%s", stageRoot)))
		return "", err
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
// ForceOverwrite=true. The staging root is managed by the caller
// (appendDerivedCodegenStaged), which defers RemoveAll.
func appendDerivedCodegen(realRoot, stageRoot, cellID string, mergedPlan []pathsafe.PlannedFile) ([]pathsafe.PlannedFile, error) {
	project, err := metadata.NewParser(stageRoot).Parse()
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"scaffold stage: parse staged metadata", err,
			errcode.WithDetails(slog.String("cellID", cellID)),
			errcode.WithInternal(fmt.Sprintf("stageRoot=%s", stageRoot)))
	}

	// Contract artifacts first so generated DTO types are available for cellgen.
	contractIDs := contractgen.ContractIDsForCell(project, cellID)
	for _, cid := range contractIDs {
		artifacts, artErr := contractgen.RenderContractArtifacts(stageRoot, project, cid)
		if artErr != nil {
			return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
				"scaffold stage: render contract artifacts", artErr,
				errcode.WithDetails(slog.String("contractID", cid)),
				errcode.WithInternal(fmt.Sprintf("stageRoot=%s", stageRoot)))
		}
		for _, a := range artifacts {
			// a.Path is stageRoot-relative (slash-separated). planDerivedArtifact
			// rebases onto realRoot (ContainPath defense-in-depth symlink walk)
			// and enforces the IsGoCellGenerated overwrite gate.
			pf, perr := planDerivedArtifact(realRoot, a.Path, a.Content)
			if perr != nil {
				return nil, perr
			}
			mergedPlan = append(mergedPlan, pf)
		}
	}

	// Cell artifacts (cell_gen.go + slice_gen.go).
	cellArtifacts, err := RenderCellArtifacts(stageRoot, project, cellID)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"scaffold stage: render cell artifacts", err,
			errcode.WithDetails(slog.String("cellID", cellID)),
			errcode.WithInternal(fmt.Sprintf("stageRoot=%s", stageRoot)))
	}
	for _, a := range cellArtifacts {
		// a.RelPath is stageRoot-relative. planDerivedArtifact rebases onto
		// realRoot (ContainPath defense-in-depth symlink walk) and enforces
		// the IsGoCellGenerated overwrite gate.
		pf, perr := planDerivedArtifact(realRoot, a.RelPath, a.Content)
		if perr != nil {
			return nil, perr
		}
		mergedPlan = append(mergedPlan, pf)
	}

	return mergedPlan, nil
}

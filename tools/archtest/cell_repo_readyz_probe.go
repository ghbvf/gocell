package archtest

// cell_repo_readyz_probe.go — importable constants for CELL-REPO-READYZ-PROBE-01.
//
// Platform symbol paths anchored to PlatformModulePath so a module rename
// updates exactly one place. The rule scanner logic lives in the _test.go file
// (cell_repo_readyz_probe_test.go) and is not needed by external Cell repos;
// only the package-path constants need to live here for importability.
//
// NOT registered in StandardCellRules(): this rule reasons about GoCell's own
// kernel/healthz.RepoProber interface — a GoCell-internal layout that is
// vacuous for external Cell repos which would implement their own prober
// contracts, not the platform's.

const (
	// repoProberIfacePkgPath is the Go package path of kernel/healthz,
	// where the RepoProber interface is declared.
	repoProberIfacePkgPath = PlatformModulePath + "/kernel/healthz"

	// repoReadinessConformancePkgPath is the Go package path of
	// kernel/cell/celltest, where RunRepoReadinessConformance is declared.
	repoReadinessConformancePkgPath = PlatformModulePath + "/kernel/cell/celltest"

	// repoProberAdapterPostgresPkgPath is the Go package path of the
	// adapters/postgres package, used in RED fixture tests to reference
	// concrete RepoProber implementations.
	repoProberAdapterPostgresPkgPath = PlatformModulePath + "/adapters/postgres"

	// repoProberRuntimeSagaPkgPath is the Go package path of the
	// runtime/saga package, used in RED fixture tests to reference
	// the Coordinator concrete RepoProber implementation.
	repoProberRuntimeSagaPkgPath = PlatformModulePath + "/runtime/saga"
)

package archtest

// cell_init_checknotnoop.go — importable platform-symbol constant for
// CELL-L2-INIT-CHECKNOTNOOP-CALLED-01.
//
// Platform symbol paths (kernel/outbox.CheckNotNoop) are anchored to
// PlatformModulePath so a module rename updates exactly one place and no bare
// "github.com/ghbvf/gocell" literal appears here. The rule scanner logic
// (collectL2PlusTargets, scanCellsForInitCheckNotNoop, parseAndIncludeTarget,
// matchTarget, initReachesCheckNotNoop, …) lives in
// cell_init_checknotnoop_test.go — it is the single source the dogfood test and
// fixture self-checks run, with no parallel rule body. Only the
// platform-anchored callee path needs a non-test home, so the _test.go can
// reference it via the kernelCellCheckNotNoopFullName alias instead of a bare
// literal.
//
// NOT registered in StandardCellRules(): Phase A enumerates L2+ targets by
// walking cell.yaml files under cells/**, a GoCell-internal directory layout
// that is vacuous for external Cell repos (which organize their own cells
// differently). This matches the other gocell-internal cell rules
// (cell_init.go, cell_public_option_param.go, cell_repo_readyz_probe.go,
// cell_test_no_adapter_import.go), none of which ship in StandardCellRules().

// checkNotNoopFullName is the fully-qualified callee name produced by
// (*types.Func).FullName() for kernel/outbox.CheckNotNoop. Anchored to
// PlatformModulePath — not a bare string literal. Consumed by the Phase B
// scanner in cell_init_checknotnoop_test.go (via the kernelCellCheckNotNoopFullName
// alias).
const checkNotNoopFullName = PlatformFrameworkModulePath + "/kernel/outbox.CheckNotNoop"

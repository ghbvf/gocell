package archtest

// prom_cell_label_funnel.go — importable PROM-CELL-LABEL-FUNNEL-01
// rule constants.
//
// Platform symbol paths are anchored to PlatformModulePath so a module rename
// updates exactly one place. The scanner logic (scanPromCellLabelFunnel,
// approvedCellIDArgs, isPromFunnelCall, cellIDSelectorExprs, isHookEventType)
// lives in the _test.go file because it is not needed by external Cell repos;
// only the gocell-path constants need to be in a non-test file.
//
// This file is NOT registered in StandardCellRules(): the rule reasons about
// adapters/prometheus-specific call patterns and depends on the full typed
// scanner infrastructure that lives in the _test.go.

const (
	// promAdapterPkgPath is the canonical import path for adapters/prometheus.
	promAdapterPkgPath = PlatformModulePath + "/adapters/prometheus"

	// cellPkgPath is the canonical import path for kernel/cell.
	cellPkgPath = PlatformModulePath + "/kernel/cell"
)

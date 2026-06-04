package archtest

// cell_test_no_adapter_import.go — importable CELL-TEST-NO-ADAPTER-IMPORT-01
// rule constant.
//
// Platform symbol paths anchored to PlatformModulePath so a module rename
// updates exactly one place. The rule scanner logic (cellTestAdapterImportFindings,
// fileDefaultVisible, etc.) lives in the _test.go file because it operates on
// filesystem paths rather than go/types and is not needed by external Cell repos
// in its current form.
//
// Registered in StandardCellRules(): this rule reasons about how cell test files
// use the platform adapters/ layer — applicable to any Cell repository that
// imports the GoCell platform.

const (
	// platformAdapterPkgPath is the root import path of the GoCell platform
	// adapters layer. Cell unit tests must not import this path or any sub-path.
	platformAdapterPkgPath = PlatformModulePath + "/adapters"
)

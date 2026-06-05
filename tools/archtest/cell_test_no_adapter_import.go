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
// NOT registered in StandardCellRules(): this rule reasons about how cell test
// files use the platform adapters/ layer — portable in principle to any Cell
// repository that imports the GoCell platform, but its scanner
// (cellTestAdapterImportFindings, fileDefaultVisible, …) currently lives in the
// _test.go file. A full Check* extraction (parallel to CheckPanicRegistered) is
// tracked at #1302 and can land in a later M3 PR — same posture as
// cell_public_option_param.go.

const (
	// platformAdapterPkgPath is the root import path of the GoCell platform
	// adapters layer. Cell unit tests must not import this path or any sub-path.
	platformAdapterPkgPath = PlatformModulePath + "/adapters"
)

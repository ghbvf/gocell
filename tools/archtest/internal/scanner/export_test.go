package scanner

// export_test.go bridges internal helpers to scanner_test for white-box
// assertions. Only compile-time exports; no runtime logic lives here.

var (
	// EachFileInternal exposes eachFile for black-box tests that need to
	// verify error propagation paths without going through the testing.T
	// adapter in EachFile.
	EachFileInternal = eachFile

	// SortDiagnosticsForTest exposes sortDiagnostics so sort-order
	// properties can be asserted from scanner_test without duplicating
	// the ordering logic.
	SortDiagnosticsForTest = sortDiagnostics

	// FormatReportForTest exposes formatReport for message-format assertions.
	// Report is already exported and indirectly covers this path; this alias
	// is provided for tests that need the []string return value directly.
	FormatReportForTest = formatReport

	// BuildAllowSetForTest exposes buildAllowSet so the AllowRels
	// normalization contract (filepath.Clean + ToSlash, matching fc.Rel
	// produced by walk.go) can be asserted in isolation.
	BuildAllowSetForTest = buildAllowSet
)

// DetectForTest exposes the unexported detect method so black-box tests
// can assert violation detection without going through Run (which calls
// t.Errorf on violations).
func (b ImportBan) DetectForTest(s Scope) ([]Diagnostic, error) { return b.detect(s) }

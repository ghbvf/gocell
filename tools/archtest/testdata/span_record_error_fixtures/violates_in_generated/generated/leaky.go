// Package generated is a fixture for SPAN-RECORD-ERROR-REDACT-01 placed
// underneath a "generated" directory to verify that
// runSpanRecordErrorFixtureScan + IncludeGenerated() can reach it. If
// IncludeGenerated() is removed from the fixture scope, the default skip
// set drops this directory and the violation goes silent — TestSpanRecord
// ErrorRedactedFixtures/violates_in_generated then expects 1 but observes
// 0, turning the test red. Parsed by archtest; not intended to compile.
package generated

// Span mirrors the wrapper.Span shape used by archtest fixture scanning.
type Span interface {
	RecordError(error)
}

// LeakRaw is the canonical violation pattern under generated/: span.Record
// Error(err) without redaction.RedactError wrapping. Same shape as
// violates/recorder.go but lives in a directory the default skip set
// excludes, so its discovery requires IncludeGenerated().
func LeakRaw(span Span, err error) {
	span.RecordError(err)
}

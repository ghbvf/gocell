//go:build archtest_fixture

// Package violation is the RED/GREEN fixture for PANIC-LOG-REDACT-01. It contains
// one compliant slog.Any("panic", redaction.RedactAny(v)) call and two violations
// (a bare value, and a non-RedactAny wrapper). The detector
// (panicLogRedactViolations, go/types IsCallToPkgFunc) must flag exactly the two
// violations — the non-vacuity proof for TestPanicLogRedact_DetectsViolation.
//
// Parsed + type-checked by archtest via Run(t, Fixture(...)); intentionally violates
// the funnel, so the archtest_fixture build tag keeps it out of normal builds.
package violation

import (
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/framework/pkg/redaction"
)

// compliant is the sanctioned form — must NOT be flagged.
func compliant(v any) {
	slog.Error("recovered", slog.Any("panic", redaction.RedactAny(v)))
}

// bareValue logs the recovered value with no redaction wrapper — VIOLATION.
func bareValue(v any) {
	slog.Error("recovered", slog.Any("panic", v))
}

// wrongWrapper uses RedactString(fmt.Sprint(...)) instead of RedactAny — the
// value is still scrubbed, but the form-lock requires RedactAny specifically, so
// this is a VIOLATION (the canonical recovery form across production is RedactAny).
func wrongWrapper(v any) {
	slog.Error("recovered", slog.Any("panic", redaction.RedactString(fmt.Sprint(v))))
}

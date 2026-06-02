//go:build archtest_fixture

// Package violation is the RED/GREEN fixture for HTTPUTIL-5XX-LOG-REDACT-01. Its
// log4xx / log5xx (the two locked target functions) append real
// pkg/errcode-detail AsSlogAttr() values both bare (VIOLATION) and wrapped in
// redaction.RedactSlogAttr (compliant). The typed detector
// (httputilLogRedactViolations: IsCallToPkgFunc for RedactSlogAttr +
// go/types ObjectOf for the errcode AsSlogAttr methods) must flag exactly the two
// bare appends — the non-vacuity proof for TestHTTPUtil5xxLogRedact_DetectsViolation.
//
// Real pkg/errcode + pkg/redaction imports are required so go/types resolves the
// AsSlogAttr methods to their defining package. Parsed + type-checked via
// Run(t, Fixture(...)); the archtest_fixture build tag keeps it out of normal builds.
package violation

import (
	"log/slog"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/redaction"
)

// log5xx: a bare InternalDetail AsSlogAttr() (VIOLATION) plus a compliant wrapped
// Detail AsSlogAttr().
func log5xx(err *errcode.Error) {
	logAttrs := []any{}
	for _, d := range err.InternalDetails {
		logAttrs = append(logAttrs, d.AsSlogAttr()) // VIOLATION: not wrapped
	}
	for _, d := range err.Details {
		logAttrs = append(logAttrs, redaction.RedactSlogAttr(d.AsSlogAttr())) // compliant
	}
	slog.Error("5xx", logAttrs...)
}

// log4xx: a bare Detail AsSlogAttr() (VIOLATION).
func log4xx(err *errcode.Error) {
	logAttrs := []any{}
	for _, d := range err.Details {
		logAttrs = append(logAttrs, d.AsSlogAttr()) // VIOLATION: not wrapped
	}
	slog.Warn("4xx", logAttrs...)
}

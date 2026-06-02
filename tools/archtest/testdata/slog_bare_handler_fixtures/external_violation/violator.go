//go:build archtest_fixture

// Package externalviolation is a RED fixture for SLOG-HANDLER-SEALED-FUNNEL-01
// A1: a production-shaped package OUTSIDE runtime/observability/logging that
// constructs raw, non-redacting slog handlers. The A1 detector
// (slogBareHandlerViolations, go/types IsCallToPkgFunc) must flag both call
// sites — the external-violation non-vacuity proof for A1
// (TestSlogHandlerSealedFunnel_A1_DetectsViolation).
//
// The slog import uses a NON-default local name (slogsink) on purpose: A1
// resolves the callee by package PATH via go/types, not by a textual "slog."
// prefix, so this fixture also proves import-alias resolution.
//
// Parsed + type-checked by archtest via Run(t, Fixture(...)); intentionally violates
// the funnel, so it must NEVER be compiled into a production build (the
// archtest_fixture build tag keeps it out of normal builds).
package externalviolation

import (
	"os"

	slogsink "log/slog"
)

// leakingJSON builds a non-redacting JSON handler directly — bypasses
// logging.NewHandler, so it never routes through the redacting contextHandler.
func leakingJSON() slogsink.Handler {
	return slogsink.NewJSONHandler(os.Stdout, nil)
}

// leakingText is the same violation via the text constructor.
func leakingText() slogsink.Handler {
	return slogsink.NewTextHandler(os.Stdout, nil)
}

//go:build archtest_fixture

// Package indirect_ref_red is a RED fixture for SLOG-CAPTURE-GLOBAL-FUNNEL-01's
// blind-spot reverse self-test: healthtest.NewCapture is bound to a
// function-value var (an indirect reference, not a CallExpr) — the shape that
// bypasses the direct-call main rule. The reverse self-test must flag it.
package indirect_ref_red

import "github.com/ghbvf/gocell/runtime/http/health/healthtest"

// captureFn is an indirect (function-value) reference to the banned symbol.
var captureFn = healthtest.NewCapture

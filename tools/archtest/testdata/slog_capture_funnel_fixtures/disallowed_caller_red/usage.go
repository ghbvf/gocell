//go:build archtest_fixture

// Package disallowed_caller_red is a RED fixture for SLOG-CAPTURE-GLOBAL-FUNNEL-01:
// a non-allowlisted package calls healthtest.NewCapture (which mutates the
// process-global slog.Default()). The funnel must flag the call.
package disallowed_caller_red

import (
	"testing"

	"github.com/ghbvf/gocell/runtime/http/health/healthtest"
)

// UseNewCapture is a disallowed direct call to the global-mutating helper.
func UseNewCapture(t *testing.T) {
	_ = healthtest.NewCapture(t) // violation on this line
}

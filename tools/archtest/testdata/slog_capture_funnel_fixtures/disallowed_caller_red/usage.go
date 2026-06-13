//go:build archtest_fixture

// Package disallowed_caller_red is a RED fixture for SLOG-CAPTURE-GLOBAL-FUNNEL-01:
// a non-allowlisted package calls the raw primitive slog.SetDefault (which mutates
// the process-global slog.Default()). The funnel must flag the call.
package disallowed_caller_red

import (
	"io"
	"log/slog"
)

// UseSetDefault is a disallowed direct call to the global-mutating primitive.
func UseSetDefault() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(io.Discard, nil))) // violation on this line
}

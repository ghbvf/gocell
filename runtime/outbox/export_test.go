// Package outbox — test-only exports for internal helpers.
// This file is compiled only during `go test`; it exposes unexported symbols
// needed by the external _test package without widening the public API.
package outbox

import "log/slog"

// NewFailureBudgetWithLogger exposes newFailureBudgetWithLogger for
// external test packages that need to inject a capturing slog.Handler.
func NewFailureBudgetWithLogger(name string, threshold int, logger *slog.Logger) *FailureBudget {
	return newFailureBudgetWithLogger(name, threshold, logger)
}

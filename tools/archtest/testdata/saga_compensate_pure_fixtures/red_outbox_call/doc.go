//go:build archtest_fixture

// Package redoutboxcall is a RED fixture for SAGA-STEP-COMPENSATE-PURE-01:
// a CompensateFunc literal calls outbox.Writer.Write. Compensate must be
// pure-reverse (app-domain only); the Coordinator owns the tx layer.
// Expect one diagnostic from A1.
package redoutboxcall

//go:build archtest_fixture

// Package redcompensateoutboxcall is a RED fixture for
// SAGA-STEP-COMPENSATE-PURE-01: a CompensateFunc literal body directly calls
// outbox.Writer.Write, violating the "pure-reverse, no durable side effects"
// rule. Expect one A1 diagnostic.
package redcompensateoutboxcall

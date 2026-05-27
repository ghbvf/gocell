//go:build archtest_fixture

// Package redcompensatevaluespecfunclit is a RED fixture for
// SAGA-STEP-COMPENSATE-PURE-01 form 1 (ValueSpec + func literal):
// a `var c saga.CompensateFunc = func(...) {...}` whose body calls
// outbox.Writer.Write, a forbidden durable side effect. Expect one A1
// diagnostic. Proves the ValueSpec-with-func-literal detection path fires.
package redcompensatevaluespecfunclit

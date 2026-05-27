//go:build archtest_fixture

// Package redcompensateassignfunclit is a RED fixture for
// SAGA-STEP-COMPENSATE-PURE-01 form 2 (AssignStmt + func literal):
// a `c = func(...) {...}` assignment to a saga.CompensateFunc-typed variable
// whose body calls (*sql.Tx).Exec, a forbidden durable side effect. Expect one
// A1 diagnostic. Proves the AssignStmt-with-func-literal detection path fires.
package redcompensateassignfunclit

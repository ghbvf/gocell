//go:build archtest_fixture

// Package redcompensatenamedfunc is a RED fixture for
// SAGA-STEP-COMPENSATE-PURE-01: a named function is assigned to a
// ksaga.CompensateFunc-typed variable and its body calls *database/sql.Tx
// methods — a forbidden durable side effect. Expect one A1 diagnostic from
// the named-function assignment path.
package redcompensatenamedfunc

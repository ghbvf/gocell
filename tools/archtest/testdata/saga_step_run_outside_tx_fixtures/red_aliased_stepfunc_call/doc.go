//go:build archtest_fixture

// Package redaliasedstepfunccall is a RED fixture for SAGA-STEP-RUN-OUTSIDE-TX-01
// A1 (signature identity). It calls StepFunc-shaped values outside safeRun
// through escape shapes the OLD exact-Named A1 check did not fully cover: a type
// ALIAS (same *types.Named), a DEFINED type `type DefStep ksaga.StepFunc`
// (distinct Named — MISSED by old A1), a raw structurally-identical func value
// (no named type — MISSED by old A1), and an alias-of-alias (chained alias, no
// kernel/saga selector — MISSED by old A1). The signature-identity A1
// (types.Identical against kernel/saga.StepFunc's signature, any import name)
// flags all of them. Proves the alias/defined-type escape class is structurally
// immune (gh #979; folds the former B1 into A1).
// Expect FOUR diagnostics from A1 (alias-of-alias is an alias subclass that
// adds a 4th callsite).
package redaliasedstepfunccall

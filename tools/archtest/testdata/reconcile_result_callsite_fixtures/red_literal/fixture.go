//go:build archtest_fixture

// Package recordresultred is a RED fixture for the
// RECONCILE-RESULT-LABEL-VALUES-FROZEN-01 callsite guard: recordResult called
// with an inline string literal (the residual hole the sealed resultLabel type
// cannot close — an untyped string constant is assignable to a defined string
// type) must be flagged.
package recordresultred

import "context"

type resultLabel string

const resultOK resultLabel = "ok"

// Metrics mirrors kernel/reconcile.Metrics' recordResult method shape.
type Metrics struct{}

func (m Metrics) recordResult(_ context.Context, _ string, _ resultLabel) {}

func caller(ctx context.Context, m Metrics) {
	m.recordResult(ctx, "rc", "panic") // inline literal — MUST be flagged
	_ = resultOK
}

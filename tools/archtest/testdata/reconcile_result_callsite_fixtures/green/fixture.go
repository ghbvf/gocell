//go:build archtest_fixture

// Package recordresultgreen is the GREEN over-fire guard for the
// RECONCILE-RESULT-LABEL-VALUES-FROZEN-01 callsite guard: a declared const and a
// typed (non-constant) resultLabel variable are both allowed — only inline
// constants are banned.
package recordresultgreen

import "context"

type resultLabel string

const resultOK resultLabel = "ok"

// Metrics mirrors kernel/reconcile.Metrics' recordResult method shape.
type Metrics struct{}

func (m Metrics) recordResult(_ context.Context, _ string, _ resultLabel) {}

func caller(ctx context.Context, m Metrics, label resultLabel) {
	m.recordResult(ctx, "rc", resultOK) // named const — allowed
	m.recordResult(ctx, "rc", label)    // typed (non-constant) var — allowed
}

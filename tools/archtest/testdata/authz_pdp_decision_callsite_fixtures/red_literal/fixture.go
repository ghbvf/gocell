//go:build archtest_fixture

// Package recorddecisionred is a RED fixture for the
// AUTHZ-PDP-DECISION-LABEL-VALUES-FROZEN-01 callsite guard: recordDecision called
// with an inline string literal for the decision argument (the residual hole the
// sealed pdpDecisionLabel type cannot close — an untyped string constant is
// assignable to a defined string type) must be flagged.
package recorddecisionred

import (
	"context"
	"time"
)

type pdpDecisionLabel string

const pdpDecisionAllow pdpDecisionLabel = "allow"

// PDPMetrics mirrors runtime/auth.PDPMetrics' recordDecision method shape:
// (ctx, action string, decision pdpDecisionLabel, dur time.Duration).
type PDPMetrics struct{}

func (m PDPMetrics) recordDecision(_ context.Context, _ string, _ pdpDecisionLabel, _ time.Duration) {}

func caller(ctx context.Context, m PDPMetrics) {
	m.recordDecision(ctx, "audit:read", "deny", 0) // inline literal — MUST be flagged
	_ = pdpDecisionAllow
}

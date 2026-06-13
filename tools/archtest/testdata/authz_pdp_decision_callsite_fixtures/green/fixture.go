//go:build archtest_fixture

// Package recorddecisiongreen is the GREEN over-fire guard for the
// AUTHZ-PDP-DECISION-LABEL-VALUES-FROZEN-01 callsite guard: a declared const and a
// typed (non-constant) pdpDecisionLabel value are both allowed — only inline
// constants are banned.
package recorddecisiongreen

import (
	"context"
	"time"
)

type pdpDecisionLabel string

const pdpDecisionAllow pdpDecisionLabel = "allow"

// PDPMetrics mirrors runtime/auth.PDPMetrics' recordDecision method shape.
type PDPMetrics struct{}

func (m PDPMetrics) recordDecision(_ context.Context, _ string, _ pdpDecisionLabel, _ time.Duration) {}

func caller(ctx context.Context, m PDPMetrics, d pdpDecisionLabel) {
	m.recordDecision(ctx, "audit:read", pdpDecisionAllow, 0) // named const — allowed
	m.recordDecision(ctx, "audit:read", d, 0)                // typed (non-constant) var — allowed
}

//go:build archtest_fixture

// Package recordoutcomered is a RED fixture for the
// OUTBOX-RELAY-LABEL-VALUES-FROZEN-01 callsite guard: recordOutcome called with
// inline string literals in the kind / outcome positions (the residual hole the
// sealed entryKind / relayOutcome types cannot close — an untyped string constant
// is assignable to a defined string type) must be flagged, once per position.
package recordoutcomered

import "context"

type entryKind string
type relayOutcome string

// collector mirrors kernel/outbox.providerRelayCollector's recordOutcome shape.
type collector struct{}

func (collector) recordOutcome(_ context.Context, _ entryKind, _ relayOutcome, _ int) {}

func caller(ctx context.Context, c collector) {
	c.recordOutcome(ctx, "command", "published", 1) // two inline literals — MUST flag both
}

//go:build archtest_fixture

// Package recordoutcomelocal is a RED fixture for the
// OUTBOX-RELAY-LABEL-VALUES-FROZEN-01 callsite guard's package-scope clause: a
// FUNCTION-LOCAL const typed relayOutcome passes the types.Identical check but is NOT
// enumerated by metricschema's package-scope-only resolveEnumStringConsts, so it was
// never frozen into the golden — the guard must reject it (guard-allowed ≡
// golden-frozen requires package scope).
package recordoutcomelocal

import "context"

type entryKind string
type relayOutcome string

const kindCommand entryKind = "command"

type collector struct{}

func (collector) recordOutcome(_ context.Context, _ entryKind, _ relayOutcome, _ int) {}

func caller(ctx context.Context, c collector) {
	const localOutcome relayOutcome = "sneaky" // typed but function-local → not in golden
	c.recordOutcome(ctx, kindCommand, localOutcome, 1) // MUST flag (1)
}

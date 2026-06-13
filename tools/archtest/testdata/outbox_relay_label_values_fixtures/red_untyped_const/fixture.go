//go:build archtest_fixture

// Package recordoutcomeuntyped is a RED fixture for the
// OUTBOX-RELAY-LABEL-VALUES-FROZEN-01 callsite guard's types.Identical clause: an
// UNTYPED string const passed in the outcome position is assignable to relayOutcome
// (Go converts the untyped constant) but its const object type is `untyped string`,
// NOT relayOutcome — so it was never frozen into the golden and must be flagged.
package recordoutcomeuntyped

import "context"

type entryKind string
type relayOutcome string

const (
	kindCommand entryKind = "command"
	rogue                 = "bogus" // untyped — NOT a relayOutcome const, not in the golden
)

type collector struct{}

func (collector) recordOutcome(_ context.Context, _ entryKind, _ relayOutcome, _ int) {}

func caller(ctx context.Context, c collector) {
	c.recordOutcome(ctx, kindCommand, rogue, 1) // outcome=untyped const → MUST flag (1)
}

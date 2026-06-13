//go:build archtest_fixture

// Package recordoutcomegreen is the GREEN over-fire guard for the
// OUTBOX-RELAY-LABEL-VALUES-FROZEN-01 callsite guard: declared consts and typed
// (non-constant) entryKind / relayOutcome variables are all allowed — only inline
// constants are banned.
package recordoutcomegreen

import "context"

type entryKind string
type relayOutcome string

const (
	kindEvent        entryKind    = "event"
	outcomePublished relayOutcome = "published"
)

// collector mirrors kernel/outbox.providerRelayCollector's recordOutcome shape.
type collector struct{}

func (collector) recordOutcome(_ context.Context, _ entryKind, _ relayOutcome, _ int) {}

func caller(ctx context.Context, c collector, k entryKind, o relayOutcome) {
	c.recordOutcome(ctx, kindEvent, outcomePublished, 1) // declared consts — allowed
	c.recordOutcome(ctx, k, o, 2)                        // typed (non-constant) vars — allowed
}

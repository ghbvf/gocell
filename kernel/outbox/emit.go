package outbox

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ghbvf/gocell/kernel/clock"
)

// Emit marshals payload to JSON, wraps it in an Entry with a fresh ID and the
// given topic, stamps OccurredAt from clk, and delegates to emitter.Emit. clk
// MUST be the producer's injected clock (every producer holds clock.Clock per
// CLOCK-POSITIONAL-INJECTION-01) so OccurredAt reflects the producer-domain
// event time rather than an ambient wall clock — this is what makes the
// PROD-CLOCK-INJECTION-01 archtest happy and keeps OccurredAt semantically
// "producer-clock" not "writer-clock".
//
// Replaces the hand-written "json.Marshal → Entry{} → Emit" pattern at producer
// call sites: one line instead of four, and the signature mechanically rules
// out silent marshal-error drops (_, _ := json.Marshal). The helper is
// transport-only — callers remain responsible for any surrounding transaction
// or atomicity scope their persistence path requires.
//
// Error contract: Emit does not log on failure. Callers MUST either return the
// error (so the surrounding HTTP handler / consumer / runPersist path logs it
// at the correct correlation-ID scope) or log it themselves before swallowing.
// Silently discarding the returned error defeats the S41 guard this helper
// exists to enforce.
//
// ref: ThreeDotsLabs/watermill components/cqrs/marshaler.go — typed struct at
// publisher, reflection-based marshal at call site. GoCell keeps the helper
// minimal (no CQRS command/event taxonomy, no header shim); callers that need
// Metadata / AggregateID / FailurePolicy construct the Entry by hand and call
// emitter.Emit directly.
func Emit[T any](ctx context.Context, clk clock.Clock, emitter Emitter, topic string, payload T) error {
	clock.MustHaveClock(clk, "outbox.Emit")
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("outbox.Emit(%s): marshal payload: %w", topic, err)
	}
	id, err := NewEntryID()
	if err != nil {
		return fmt.Errorf("outbox.Emit(%s): new entry id: %w", topic, err)
	}
	entry := Entry{
		ID:         id,
		EventType:  topic,
		Payload:    data,
		OccurredAt: clk.Now().UTC(),
	}
	if err := emitter.Emit(ctx, entry); err != nil {
		return fmt.Errorf("outbox.Emit(%s): %w", topic, err)
	}
	return nil
}

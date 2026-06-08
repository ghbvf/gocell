package command

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ghbvf/gocell/kernel/clock"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	idemkey "github.com/ghbvf/gocell/runtime/http/idempotency"
)

// CommandIDMetadataKey is the outbox.Entry metadata key under which an async
// command's per-instance idempotency identity (commandID) is carried. It is the
// single source shared by the producer funnel (EmitAsync writes it) and the relay
// funnel (ClaimKeyFromEntry reads it back).
//
// The "gocell.command." prefix deliberately avoids the kernel-reserved metadata
// namespace (kout.ReservedMetadataKeys holds observability/principal keys like
// trace_id / actor_id); a reserved key would be rejected by Entry.Validate at
// construction. commandID is NOT a principal/observability identity — it is the
// command instance dedup token — so it lives in the producer-owned business
// metadata map.
const CommandIDMetadataKey = "gocell.command.idempotency_id"

// EmitAsync is the sole sanctioned emit exit for an async command entry. subject
// and commandID are required positional params — it is compile-time inexpressible
// to "emit an async command but omit the idempotency identity slot".
//
// dispatchID becomes the entry's RoutingTopic (the command topic); the relay
// routes a command entry to its in-process generated DispatchAsync by matching
// the RoutingTopic against the composition-root dispatcher-map. subject is written
// to AggregateID and commandID to Metadata[CommandIDMetadataKey]; the relay reads
// both back via ClaimKeyFromEntry to derive the Claimer dedup key (tenant is taken
// from the entry's principal envelope, injected by NewEntry from ctx).
//
// WARNING: subject and commandID are adjacent same-typed (string) positional
// params — transposing them is NOT a compile error and silently mis-partitions
// the dedup slot across subjects. subject = the dedup aggregate (e.g. deviceID);
// commandID = the per-instance identity (e.g. the source event id).
//
// Error contract mirrors kout.Emit: every failure is wrapped with context and
// returned (never swallowed). Callers MUST return or log the error.
func EmitAsync[T any](
	ctx context.Context,
	clk clock.Clock,
	emitter kout.Emitter,
	dispatchID CommandID,
	subject, commandID string,
	payload T,
) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("command.EmitAsync(%s): marshal payload: %w", dispatchID, err)
	}
	entry, err := kout.NewEntry(clk, ctx, string(dispatchID), data,
		kout.WithAggregateID(subject),
		kout.WithMetadata(map[string]string{CommandIDMetadataKey: commandID}))
	if err != nil {
		return fmt.Errorf("command.EmitAsync(%s): %w", dispatchID, err)
	}
	if err := emitter.Emit(ctx, entry); err != nil {
		return fmt.Errorf("command.EmitAsync(%s): %w", dispatchID, err)
	}
	return nil
}

// ClaimKeyFromEntry extracts an async command entry's idempotency identity and
// derives the flattened Claimer key. ok=false means the entry is missing its
// identity slot (empty subject or empty commandID) — the relay MUST fail-closed
// and dead-letter such an entry rather than silently skipping deduplication.
//
// The tenant dimension is taken from the entry's principal envelope (injected by
// NewEntry from ctx at emit time, or _notenant when empty); subject from
// AggregateID; commandID from the CommandIDMetadataKey metadata entry. These flow
// through the sealed DeriveCommandKey + Flat funnel so the resulting key is
// node-agnostic and stable.
func ClaimKeyFromEntry(e kout.Entry) (key string, ok bool) {
	tenant := string(e.Principal().TenantID)
	subject := e.AggregateID()
	commandID := e.Metadata()[CommandIDMetadataKey]
	if subject == "" || commandID == "" {
		return "", false
	}
	return idemkey.DeriveCommandKey(tenant, subject, commandID).Flat(), true
}

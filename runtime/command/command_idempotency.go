package command

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	idemkey "github.com/ghbvf/gocell/runtime/http/idempotency"
)

// CommandIDMetadataKey is the outbox.Entry metadata key under which an async
// command's per-instance idempotency identity (commandID) is carried. It is the
// single source shared by the producer funnel (EmitAsync writes it) and the relay
// funnel (ClaimKeyFromEntry reads it back).
//
// Sibling key: CommandDeadlineMetadataKey — both keys are written together by
// WithActiveUniqueness (via EmitAsync) and read together by the relay's
// dispatchCommand.
//
// The "gocell.command." prefix deliberately avoids the kernel-reserved metadata
// namespace (kout.ReservedMetadataKeys holds observability/principal keys like
// trace_id / actor_id); a reserved key would be rejected by Entry.Validate at
// construction. commandID is NOT a principal/observability identity — it is the
// command instance dedup token — so it lives in the producer-owned business
// metadata map.
const CommandIDMetadataKey = "gocell.command.idempotency_id"

// CommandDeadlineMetadataKey is the outbox.Entry metadata key under which an
// async command's opt-in active-uniqueness terminal-deadline is carried. It is
// written by WithActiveUniqueness (via EmitAsync) and read by the relay's
// dispatchCommand to inject (key, deadline) into ctx via WithDispatchedUniqueness.
//
// Sibling key: CommandIDMetadataKey — both keys are written together by
// WithActiveUniqueness (via EmitAsync) and read together by the relay's
// dispatchCommand.
//
// The "gocell.command." prefix is in the same producer-owned namespace as
// CommandIDMetadataKey and is non-reserved (kout.ReservedMetadataKeys holds
// observability/principal keys). Its presence is opt-in: commands that do not
// call WithActiveUniqueness never write this key, and the relay skips the
// injection silently.
const CommandDeadlineMetadataKey = "gocell.command.overall_deadline"

// emitConfig holds the accumulated state built by EmitOption values. It is
// unexported; callers interact only through the WithActiveUniqueness constructor.
// hasActiveUniqueness is set to true whenever WithActiveUniqueness is called,
// even if deadline is zero, so EmitAsync can distinguish "no opt" from "opt with
// zero deadline" and apply the coupling guard.
type emitConfig struct {
	deadline            time.Time
	hasActiveUniqueness bool
}

// EmitOption is a functional option for EmitAsync. It is deliberately narrow:
// the currently available option is WithActiveUniqueness, which couples active-queue
// uniqueness with a terminal-guaranteeing deadline so "uniqueness without
// deadline" is inexpressible. New options should be reviewed for footgun risk
// before being added (e.g. options that silently relax the coupling guard).
type EmitOption func(*emitConfig)

// WithActiveUniqueness opts an async command into queue active-uniqueness and
// couples it with a terminal-guaranteeing deadline. The coupling is intentional:
// active-uniqueness without a deadline risks indefinite queue occupancy when a
// command stalls. This single option makes "uniqueness without deadline"
// inexpressible at the call site.
//
// deadline must be non-zero; EmitAsync returns a hard error if it is zero (the
// coupling guard). The deadline is stored as RFC3339Nano UTC in
// CommandDeadlineMetadataKey and read by the relay's dispatchCommand, which
// injects (claimKey, deadline) into ctx via WithDispatchedUniqueness.
func WithActiveUniqueness(deadline time.Time) EmitOption {
	return func(c *emitConfig) {
		c.hasActiveUniqueness = true
		c.deadline = deadline
	}
}

// errCommandEmitOp is the errcode.Code used for EmitAsync validation errors
// (e.g. zero deadline coupling guard).
const errCommandEmitOp errcode.Code = "ERR_COMMAND_EMIT_OP"

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
// opts is variadic and additive. Existing callers passing no opts are unaffected.
// Use WithActiveUniqueness(deadline) to opt into active-queue-uniqueness with a
// terminal-guaranteeing deadline; the relay will inject the (key, deadline) pair
// into ctx via WithDispatchedUniqueness on dispatch.
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
	opts ...EmitOption,
) error {
	cfg := &emitConfig{}
	for _, o := range opts {
		o(cfg)
	}

	// Coupling guard: WithActiveUniqueness requires a non-zero deadline. A zero
	// deadline with active-uniqueness set is a producer bug — fail fast so it
	// cannot silently produce a deadline-less uniqueness constraint.
	if cfg.hasActiveUniqueness && cfg.deadline.IsZero() {
		return errcode.New(errcode.KindInvalid, errCommandEmitOp,
			"command.EmitAsync: WithActiveUniqueness deadline must be non-zero")
	}

	md := map[string]string{CommandIDMetadataKey: commandID}
	if cfg.hasActiveUniqueness {
		md[CommandDeadlineMetadataKey] = cfg.deadline.UTC().Format(time.RFC3339Nano)
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("command.EmitAsync(%s): marshal payload: %w", dispatchID, err)
	}
	entry, err := kout.NewEntry(clk, ctx, string(dispatchID), data,
		kout.WithAggregateID(subject),
		kout.WithMetadata(md))
	if err != nil {
		return fmt.Errorf("command.EmitAsync(%s): %w", dispatchID, err)
	}
	if err := emitter.Emit(ctx, entry); err != nil {
		return fmt.Errorf("command.EmitAsync(%s): %w", dispatchID, err)
	}
	return nil
}

// dispatchedUniquenessKey is the unexported context key type for the dispatched
// uniqueness value, preventing collisions with other packages.
type dispatchedUniquenessKey struct{}

// dispatchedUniquenessValue holds the (claimKey, deadline) pair injected by the
// relay when it dispatches an active-uniqueness command.
type dispatchedUniquenessValue struct {
	key      string
	deadline time.Time
}

// WithDispatchedUniqueness injects the active-uniqueness (claimKey, deadline)
// pair into ctx. It is called by the relay's dispatchCommand on the
// ClaimAcquired path when CommandDeadlineMetadataKey is present in the entry
// metadata. key is the flattened Claimer key derived by ClaimKeyFromEntry;
// deadline is the parsed RFC3339Nano UTC deadline from CommandDeadlineMetadataKey.
func WithDispatchedUniqueness(ctx context.Context, key string, deadline time.Time) context.Context {
	return context.WithValue(ctx, dispatchedUniquenessKey{}, dispatchedUniquenessValue{
		key:      key,
		deadline: deadline,
	})
}

// DispatchedUniqueness reads the active-uniqueness (key, deadline) pair from ctx.
// ok=false when the pair is absent (the command did not opt into active-uniqueness
// or the relay did not inject it). Consumers MUST check ok before using the values.
func DispatchedUniqueness(ctx context.Context) (key string, deadline time.Time, ok bool) {
	v, present := ctx.Value(dispatchedUniquenessKey{}).(dispatchedUniquenessValue)
	if !present {
		return "", time.Time{}, false
	}
	return v.key, v.deadline, true
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

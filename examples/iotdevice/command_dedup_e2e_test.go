package main

// command_dedup_e2e_test.go — relay-level dedup end-to-end (#1698 Batch-3).
//
// Drives the full reactive loop: a device-registered event is "redelivered"
// twice to the real devicebootstrap.HandleDeviceRegistered producer, which emits
// a command.devicecommand.enqueue.v1 entry per delivery through a writer-emitter
// into a FakeStore the relay polls. Because command_id == source event entry.ID()
// is deterministic across redelivery, the two emitted command entries carry the
// SAME command_id (different store ids); the relay's Claimer-wrapped dispatch
// (#1698) therefore invokes the enqueue handler exactly once (dedup), while both
// rows settle to published (one ClaimAcquired-dispatch, one ClaimDone-skip).
//
// examples/ may import generated/ + runtime/, so this is the natural home for an
// integration test of the producer→store→relay async loop.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	devicebootstrap "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicebootstrap"
	enqueue "github.com/ghbvf/gocell/generated/contracts/command/devicecommand/enqueue/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/command"
	"github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/outbox/outboxtest"
)

// dedupEnqueueHandler counts how many times the enqueue handler runs so the test
// can assert relay-level deduplication (handler must fire exactly once even when
// the source event is redelivered).
type dedupEnqueueHandler struct {
	calls    int
	lastReqs []*enqueue.Request
}

func (h *dedupEnqueueHandler) HandleEnqueue(_ context.Context, req *enqueue.Request) (*enqueue.Response, error) {
	h.calls++
	h.lastReqs = append(h.lastReqs, req)
	return &enqueue.Response{Data: &enqueue.ResponseData{ID: "cmd-1", Status: "Pending"}}, nil
}

const dedupSourceEventTopic = "event.device-registered.v1"

// newDedupRelay builds a relay over store wired with the Claimer-backed command
// dispatch for the generated enqueue command — the production composition-root
// shape (map value = generated DispatchAsync direct symbol).
func newDedupRelay(t *testing.T, store *outboxtest.FakeStore, reg *command.Registry) *outbox.Relay {
	t.Helper()
	relay := outbox.NewRelay(clock.Real(), store, &kout.DiscardPublisher{},
		// Small BaseRetryDelay so the ClaimBusy→MarkRetry second entry (both
		// command entries claimed in one batch before the first commits) re-claims
		// quickly within the test deadline; the default 5s would exceed it.
		outbox.RelayConfig{
			PollInterval:   5 * time.Millisecond,
			BaseRetryDelay: 5 * time.Millisecond,
		}.WithDefaults())
	relay.WithCommandDispatch(reg, map[command.CommandID]command.AsyncDispatchFunc{
		enqueue.DispatchID: enqueue.DispatchAsync,
	}, idempotency.NewInMemClaimer(clock.Real()))
	return relay
}

// registeredPayload marshals a minimal device-registered event body.
func registeredPayload(t *testing.T, deviceID string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"id":       deviceID,
		"name":     "edge-sensor",
		"status":   "online",
		"lastSeen": time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	return b
}

// sourceEvent builds a deterministic device-registered entry with a fixed ID so a
// "redelivery" reuses the same entry.ID() — the value HandleDeviceRegistered uses
// as command_id, making the dedup key stable across deliveries.
func sourceEvent(t *testing.T, deviceID, eventID string) kout.Entry {
	t.Helper()
	entry, err := kout.NewEntry(clock.Real(), context.Background(),
		dedupSourceEventTopic, registeredPayload(t, deviceID), kout.WithID(eventID))
	require.NoError(t, err)
	return entry
}

// TestCommandRelay_DedupOnEventRedelivery is scenario A: a device-registered
// event redelivered twice produces two command entries with the SAME command_id
// (different store ids). The relay dispatches the enqueue handler exactly once
// (dedup) and both rows settle to published.
func TestCommandRelay_DedupOnEventRedelivery(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	h := &dedupEnqueueHandler{}
	require.NoError(t, enqueue.Register(reg, h))

	store := outboxtest.NewFakeStore()
	// Producer side: a writer-emitter over the SAME store the relay polls — the
	// production devicebootstrap path. Both deliveries reuse one source event id,
	// so HandleDeviceRegistered stamps both command entries with command_id =
	// that event id (different store entry ids though).
	we, err := kout.NewWriterEmitter(store)
	require.NoError(t, err)
	svc, err := devicebootstrap.NewService(clock.Real(),
		devicebootstrap.WithEmitter(kout.WrapEmitterForCell(we)))
	require.NoError(t, err)

	src := sourceEvent(t, "d1", "evt-device-registered-redelivered")
	// At-least-once redelivery: handle the SAME source event twice.
	require.Equal(t, kout.DispositionAck, svc.HandleDeviceRegistered(context.Background(), src).Disposition)
	require.Equal(t, kout.DispositionAck, svc.HandleDeviceRegistered(context.Background(), src).Disposition)

	rows := store.Snapshot()
	require.Len(t, rows, 2, "two command entries (distinct store ids) must be written")
	cmd0, ok0 := command.ClaimKeyFromEntry(rows[0].Entry)
	cmd1, ok1 := command.ClaimKeyFromEntry(rows[1].Entry)
	require.True(t, ok0)
	require.True(t, ok1)
	assert.Equal(t, cmd0, cmd1, "redelivery must derive the SAME claim key (dedup token)")
	assert.NotEqual(t, rows[0].Entry.ID(), rows[1].Entry.ID(), "store ids must differ")

	relay := newDedupRelay(t, store, reg)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = relay.Start(ctx) }()

	require.NoError(t, store.WaitFor(ctx, func(rows []outboxtest.FakeRow) bool {
		return len(rows) == 2 &&
			rows[0].Status == kout.StatePublished &&
			rows[1].Status == kout.StatePublished
	}), "both command entries must settle to published (one dispatched, one deduped)")

	assert.Equal(t, 1, h.calls, "enqueue handler must run exactly once across the redelivered command")

	// The two command entries are claimed in one batch before the first commits,
	// so the second sees ClaimBusy → MarkRetry → re-claim → ClaimDone. At least
	// one row must therefore show attempts >= 1 — this witnesses the retry path
	// (the #1698 core), guarding against a future regression that dead-letters or
	// drops the busy entry instead of retrying it into dedup.
	final := store.Snapshot()
	require.Len(t, final, 2)
	assert.True(t, final[0].Attempts >= 1 || final[1].Attempts >= 1,
		"at least one row must have retried (ClaimBusy→MarkRetry) before deduping")
}

// TestCommandRelay_FailClosedOnMissingIdentity is scenario B: a command entry
// with no idempotency identity (no AggregateID / no command_id metadata) is
// fail-closed dead-lettered, and the handler never runs.
func TestCommandRelay_FailClosedOnMissingIdentity(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	h := &dedupEnqueueHandler{}
	require.NoError(t, enqueue.Register(reg, h))

	store := outboxtest.NewFakeStore()
	// Identity-less command entry: correct topic but no AggregateID + no
	// CommandIDMetadataKey → ClaimKeyFromEntry returns ok=false → MarkDead.
	payload, err := json.Marshal(enqueue.Request{DeviceID: "d1", Payload: "now"})
	require.NoError(t, err)
	bad, err := kout.NewEntry(clock.Real(), context.Background(), string(enqueue.DispatchID), payload)
	require.NoError(t, err)
	store.Seed(outbox.ClaimedEntry{Entry: bad})

	relay := newDedupRelay(t, store, reg)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = relay.Start(ctx) }()

	require.NoError(t, store.WaitFor(ctx, func(rows []outboxtest.FakeRow) bool {
		return len(rows) == 1 && rows[0].Status == kout.StateDead
	}), "identity-less command entry must be fail-closed dead-lettered")
	assert.Equal(t, 0, h.calls, "handler must not run on an identity-less entry")
}

// TestCommandRelay_NormalCommitDispatch is scenario C: a single identity-bearing
// command entry dispatches to the handler and the row settles to published.
func TestCommandRelay_NormalCommitDispatch(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	h := &dedupEnqueueHandler{}
	require.NoError(t, enqueue.Register(reg, h))

	store := outboxtest.NewFakeStore()
	we, err := kout.NewWriterEmitter(store)
	require.NoError(t, err)
	svc, err := devicebootstrap.NewService(clock.Real(),
		devicebootstrap.WithEmitter(kout.WrapEmitterForCell(we)))
	require.NoError(t, err)
	require.Equal(t, kout.DispositionAck,
		svc.HandleDeviceRegistered(context.Background(), sourceEvent(t, "d2", "evt-single")).Disposition)

	relay := newDedupRelay(t, store, reg)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = relay.Start(ctx) }()

	require.NoError(t, store.WaitFor(ctx, func(rows []outboxtest.FakeRow) bool {
		return len(rows) == 1 && rows[0].Status == kout.StatePublished
	}), "command entry must settle to published after dispatch")
	assert.Equal(t, 1, h.calls, "handler must run exactly once for a single command")
}

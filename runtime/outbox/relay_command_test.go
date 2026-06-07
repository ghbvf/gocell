// Package outbox — white-box tests for the relay's in-process command dispatch
// branch (#1667 / ADR 202606040550-1044 §5 ④). These live in package outbox so
// they can reach the unexported publishBatch / commandDispatchFor / cmdDispatch.
//
// These tests bind FAKE AsyncDispatchFunc values, which is why they are exempt
// from COMMAND-ASYNC-DISPATCH-CALLER-01 (production composition-root wiring must
// bind generated DispatchAsync symbols; the end-to-end test with the real
// generated enqueue.DispatchAsync lives in examples/iotdevice, which may import
// generated/contracts/command/**).
package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/command"
)

// newCommandClaimer builds an in-memory Claimer for command-dispatch tests.
func newCommandClaimer() idempotency.Claimer {
	return idempotency.NewInMemClaimer(clock.Real())
}

// recordingPublisher captures the topics it was asked to publish so a test can
// assert command entries are NOT published to the broker.
type recordingPublisher struct {
	topics []string
}

func (p *recordingPublisher) Publish(_ context.Context, topic string, _ []byte) error {
	p.topics = append(p.topics, topic)
	return nil
}

func (p *recordingPublisher) Close(_ context.Context) error { return nil }

// claimedEntry builds a ClaimedEntry whose RoutingTopic == topic (EventType and
// Topic both set) with the given JSON payload, for direct publishBatch tests. The
// entry carries a full command idempotency identity (AggregateID=subject +
// CommandIDMetadataKey metadata) keyed off id so ClaimKeyFromEntry succeeds.
func claimedEntry(t *testing.T, id, topic, payload string) ClaimedEntry {
	t.Helper()
	now := time.Now()
	e, err := kout.EntryScan{
		ID: id, AggregateID: "subject-" + id, EventType: topic, Topic: topic,
		Payload:    []byte(payload),
		Metadata:   map[string]string{command.CommandIDMetadataKey: "cmd-" + id},
		CreatedAt:  now,
		OccurredAt: now,
	}.ToEntry()
	require.NoError(t, err)
	return ClaimedEntry{Entry: e, LeaseID: "lease-1"}
}

// seedPendingCommand seeds a pending command entry (RoutingTopic == topic) into
// the minimalStore (defined in relay_internal_test.go, same package). The entry
// carries a full command idempotency identity so ClaimKeyFromEntry succeeds.
func (s *minimalStore) seedPendingCommand(topic, payload string) {
	const id = "c1"
	s.mu.Lock()
	defer s.mu.Unlock()
	past := time.Now().Add(relayStaleAge)
	entry, err := kout.EntryScan{
		ID: id, AggregateID: "subject-" + id, EventType: topic, Topic: topic,
		Payload:    []byte(payload),
		Metadata:   map[string]string{command.CommandIDMetadataKey: "cmd-" + id},
		CreatedAt:  past,
		OccurredAt: past,
	}.ToEntry()
	if err != nil {
		panic("relay_command_test.go seedPendingCommand: " + err.Error())
	}
	s.rows[id] = &minimalRow{entry: entry, status: "pending"}
}

// TestPublishBatch_CommandDispatchVsBroker asserts publishBatch routes a command
// entry (RoutingTopic ∈ cmdDispatch) to its in-process handler and an event
// entry to the broker — each via the single shared []publishResult.
func TestPublishBatch_CommandDispatchVsBroker(t *testing.T) {
	t.Parallel()
	const cmdID = "command.test.do.v1"
	const eventTopic = "event.test.happened.v1"

	reg := command.NewRegistry()
	var dispatched []string
	pub := &recordingPublisher{}

	r := &Relay{}
	r.pub = pub
	r.WithCommandDispatch(reg, map[command.CommandID]command.AsyncDispatchFunc{
		cmdID: func(_ context.Context, gotReg *command.Registry, e kout.Entry) error {
			dispatched = append(dispatched, e.RoutingTopic())
			assert.Same(t, reg, gotReg, "relay must pass its own registry to the dispatch func")
			return nil
		},
	}, newCommandClaimer())

	results := r.publishBatch(context.Background(), []ClaimedEntry{
		claimedEntry(t, "c1", cmdID, `{"deviceId":"d1"}`),
		claimedEntry(t, "e1", eventTopic, `{}`),
	})

	require.Len(t, results, 2)
	assert.NoError(t, results[0].err)
	assert.NoError(t, results[1].err)
	assert.Equal(t, []string{cmdID}, dispatched, "command must be dispatched in-process")
	assert.Equal(t, []string{eventTopic}, pub.topics, "command must NOT be published to the broker; only the event")
}

// TestPublishBatch_CommandDispatchErrorPropagates asserts a dispatch error is
// surfaced as the entry's publishResult.err, which the shared writeBack routes
// to MarkRetry (verified end-to-end by TestPollOnce_CommandDispatchRetriesOnError).
func TestPublishBatch_CommandDispatchErrorPropagates(t *testing.T) {
	t.Parallel()
	const cmdID = "command.test.fail.v1"
	sentinel := errors.New("handler boom")

	r := &Relay{}
	r.pub = &recordingPublisher{}
	r.WithCommandDispatch(command.NewRegistry(), map[command.CommandID]command.AsyncDispatchFunc{
		cmdID: func(context.Context, *command.Registry, kout.Entry) error { return sentinel },
	}, newCommandClaimer())

	results := r.publishBatch(context.Background(), []ClaimedEntry{claimedEntry(t, "c1", cmdID, `{}`)})
	require.Len(t, results, 1)
	assert.ErrorIs(t, results[0].err, sentinel)
}

// TestPollOnce_CommandDispatchSettlesPublished drives the full claim → dispatch
// → writeBack cycle: a registered command entry is dispatched in-process and its
// row settles to "published" (consumed), reusing the broker path's MarkPublished.
func TestPollOnce_CommandDispatchSettlesPublished(t *testing.T) {
	t.Parallel()
	const cmdID = "command.test.do.v1"
	store := newMinimalStore()
	store.seedPendingCommand(cmdID, `{"deviceId":"d1"}`)

	var called int
	r := NewRelay(clock.Real(), store, &recordingPublisher{}, RelayConfig{}.WithDefaults())
	r.WithCommandDispatch(command.NewRegistry(), map[command.CommandID]command.AsyncDispatchFunc{
		cmdID: func(context.Context, *command.Registry, kout.Entry) error { called++; return nil },
	}, newCommandClaimer())

	require.NoError(t, r.pollOnce(context.Background()))
	assert.Equal(t, 1, called, "command handler must be dispatched once")

	store.mu.Lock()
	status := store.rows["c1"].status
	store.mu.Unlock()
	assert.Equal(t, "published", status, "dispatched command row settles to published (consumed)")
}

// TestPollOnce_CommandDispatchRetriesOnError drives the full cycle with a failing
// dispatch: the entry goes back to pending (MarkRetry), not published.
func TestPollOnce_CommandDispatchRetriesOnError(t *testing.T) {
	t.Parallel()
	const cmdID = "command.test.fail.v1"
	store := newMinimalStore()
	store.seedPendingCommand(cmdID, `{}`)

	r := NewRelay(clock.Real(), store, &recordingPublisher{},
		RelayConfig{MaxAttempts: 5, BaseRetryDelay: time.Millisecond, MaxRetryDelay: time.Second}.WithDefaults())
	r.WithCommandDispatch(command.NewRegistry(), map[command.CommandID]command.AsyncDispatchFunc{
		cmdID: func(context.Context, *command.Registry, kout.Entry) error { return errors.New("boom") },
	}, newCommandClaimer())

	require.NoError(t, r.pollOnce(context.Background()))
	store.mu.Lock()
	status := store.rows["c1"].status
	attempts := store.rows["c1"].attempts
	store.mu.Unlock()
	assert.Equal(t, "pending", status, "failed command dispatch must MarkRetry (back to pending)")
	assert.Equal(t, 1, attempts, "retry increments attempts")
}

// TestWithCommandDispatch_NoopGuards asserts the builder option is a silent
// no-op for nil registry, empty map, and all-nil entries.
func TestWithCommandDispatch_NoopGuards(t *testing.T) {
	t.Parallel()
	fn := func(context.Context, *command.Registry, kout.Entry) error { return nil }

	claimer := newCommandClaimer()
	r := &Relay{}
	r.WithCommandDispatch(nil, map[command.CommandID]command.AsyncDispatchFunc{"command.x.v1": fn}, claimer)
	_, ok := r.commandDispatchFor("command.x.v1")
	assert.False(t, ok, "nil registry → no command dispatch wired")
	assert.Nil(t, r.cmdDispatch)
	assert.Nil(t, r.cmdClaimer, "no-op must not store the claimer")

	r.WithCommandDispatch(command.NewRegistry(), nil, claimer)
	assert.Nil(t, r.cmdDispatch, "empty map → no-op")

	r.WithCommandDispatch(command.NewRegistry(), map[command.CommandID]command.AsyncDispatchFunc{"command.x.v1": nil}, claimer)
	assert.Nil(t, r.cmdDispatch, "all-nil entries dropped → no-op")
}

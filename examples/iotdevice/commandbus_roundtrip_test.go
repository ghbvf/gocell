package main

// commandbus_roundtrip_test.go — end-to-end exercise of the generated command-bus
// funnel for command.device-command.cmdremote.v1 (#1044 PR-1). This is the only test
// that drives the REAL generated package (cmdremote.Register / cmdremote.Dispatch /
// cmdremote.Handler) through a runtime command.Registry, covering the three runtime
// branches the contractgen golden test cannot execute: success invoke, no-handler
// (KindNotFound/ErrCommandNotFound), and the duplicate-registration guard.
// examples/ may import generated/ + runtime/, so this is the natural home for an
// integration test of the generated funnel (runtime/command cannot import the
// generated package — that would be an import cycle).

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/idempotency"
	kout "github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/runtime/command"
	"github.com/ghbvf/gocell/framework/runtime/outbox"
	"github.com/ghbvf/gocell/framework/runtime/outbox/outboxtest"
	cmdremote "github.com/ghbvf/gocell/generated/contracts/command/remotecommand/v1"
)

// fakeRemoteCommandHandler implements the generated cmdremote.Handler interface.
type fakeRemoteCommandHandler struct {
	called  bool
	lastReq *cmdremote.Request
	retErr  error
}

func (h *fakeRemoteCommandHandler) HandleRemotecommand(_ context.Context, req *cmdremote.Request) (*cmdremote.Response, error) {
	h.called = true
	h.lastReq = req
	if h.retErr != nil {
		return nil, h.retErr
	}
	return &cmdremote.Response{Data: &cmdremote.ResponseData{ID: "cmd-1", Status: "Pending"}}, nil
}

func TestCommandBus_Enqueue_RegisterDispatchRoundTrip(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	h := &fakeRemoteCommandHandler{}
	if err := cmdremote.Register(reg, h); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// DeviceID (a schema-required field) is intentionally omitted: sync Dispatch
	// is a first-party trusted boundary and does NOT run schema value-validation
	// (ADR 202606040550-1044 §D8). Only the async DispatchAsync path validates the
	// untrusted payload (#1588). Adding DeviceID here would obscure that contract.
	req := &cmdremote.Request{CommandType: "reboot", Payload: "now"}
	resp, err := cmdremote.Dispatch(context.Background(), reg, req)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !h.called {
		t.Fatal("handler was not invoked")
	}
	if h.lastReq != req {
		t.Errorf("handler received a different request pointer than dispatched")
	}
	if resp == nil || resp.Data == nil || resp.Data.ID != "cmd-1" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestCommandBus_Enqueue_HandlerErrorPropagates(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	sentinel := errors.New("downstream failure")
	if err := cmdremote.Register(reg, &fakeRemoteCommandHandler{retErr: sentinel}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, err := cmdremote.Dispatch(context.Background(), reg, &cmdremote.Request{Payload: "x"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected handler error to propagate, got: %v", err)
	}
}

func TestCommandBus_Enqueue_DispatchWithoutHandler(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	_, err := cmdremote.Dispatch(context.Background(), reg, &cmdremote.Request{Payload: "x"})
	errcodetest.AssertCode(t, err, errcode.ErrCommandNotFound)
	var ec *errcode.Error
	if errors.As(err, &ec) && ec.Kind != errcode.KindNotFound {
		t.Errorf("expected KindNotFound, got %v", ec.Kind)
	}
}

func TestCommandBus_Enqueue_DuplicateRegistration(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	if err := cmdremote.Register(reg, &fakeRemoteCommandHandler{}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := cmdremote.Register(reg, &fakeRemoteCommandHandler{})
	errcodetest.AssertCode(t, err, errcode.ErrConflict)
}

func TestCommandBus_Enqueue_RegisterNilHandler(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	// Untyped nil and typed-nil both rejected by the generated Register guard.
	errcodetest.AssertCode(t, cmdremote.Register(reg, nil), errcode.ErrValidationFailed)
	var typedNil *fakeRemoteCommandHandler
	errcodetest.AssertCode(t, cmdremote.Register(reg, typedNil), errcode.ErrValidationFailed)
}

// TestCommandBus_Enqueue_RegisterNilRegistry verifies the generated Register
// guards a nil *command.Registry with a structured error instead of panicking
// on the receiver deref (#1578 F4).
func TestCommandBus_Enqueue_RegisterNilRegistry(t *testing.T) {
	t.Parallel()
	errcodetest.AssertCode(t, cmdremote.Register(nil, &fakeRemoteCommandHandler{}), errcode.ErrValidationFailed)
}

// TestCommandBus_Enqueue_DispatchNilRegistry verifies the generated Dispatch
// guards a nil *command.Registry with a structured error instead of panicking
// on the receiver deref (#1578 F4).
func TestCommandBus_Enqueue_DispatchNilRegistry(t *testing.T) {
	t.Parallel()
	_, err := cmdremote.Dispatch(context.Background(), nil, &cmdremote.Request{Payload: "x"})
	errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)
}

// TestCommandBus_Enqueue_DispatchNilRequest verifies the generated Dispatch
// rejects a nil request before it can reach a handler (#1578 F4).
func TestCommandBus_Enqueue_DispatchNilRequest(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	if err := cmdremote.Register(reg, &fakeRemoteCommandHandler{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, err := cmdremote.Dispatch(context.Background(), reg, nil)
	errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)
}

// ---------------------------------------------------------------------------
// Async dispatch (#1667 / ADR 202606040550-1044 §5 ④)
// ---------------------------------------------------------------------------

// newCommandEntry builds a command outbox.Entry whose RoutingTopic == the enqueue
// DispatchID, carrying req as the JSON payload — what a producer writes to the
// outbox and the relay claims for in-process async dispatch.
func newCommandEntry(t *testing.T, req cmdremote.Request) kout.Entry {
	t.Helper()
	payload, err := json.Marshal(req)
	require.NoError(t, err)
	entry, err := kout.NewEntry(clock.Real(), context.Background(), string(cmdremote.DispatchID), payload)
	require.NoError(t, err)
	return entry
}

// newAsyncCommandEntry builds a command entry that carries the idempotency
// identity slot the relay's Claimer-wrapped dispatch path requires (AggregateID =
// subject, Metadata[CommandIDMetadataKey] = commandID), mirroring the shape
// command.EmitAsync produces for an async command. The async relay end-to-end
// test seeds this so ClaimKeyFromEntry derives a key instead of fail-closing.
func newAsyncCommandEntry(t *testing.T, req cmdremote.Request) kout.Entry {
	t.Helper()
	payload, err := json.Marshal(req)
	require.NoError(t, err)
	entry, err := kout.NewEntry(clock.Real(), context.Background(), string(cmdremote.DispatchID), payload,
		kout.WithAggregateID(req.DeviceID),
		kout.WithMetadata(map[string]string{command.CommandIDMetadataKey: "cmd-" + req.DeviceID}))
	require.NoError(t, err)
	return entry
}

// newRawCommandEntry builds a command entry from raw JSON bytes — needed for the
// #1588 value-validation cases that can't be expressed via a typed cmdremote.Request
// (missing required field, additionalProperties), which json.Marshal of the struct
// would never produce.
func newRawCommandEntry(t *testing.T, rawJSON string) kout.Entry {
	t.Helper()
	entry, err := kout.NewEntry(clock.Real(), context.Background(), string(cmdremote.DispatchID), []byte(rawJSON))
	require.NoError(t, err)
	return entry
}

// TestCommandBus_Enqueue_DispatchAsyncRoundTrip drives the REAL generated
// cmdremote.DispatchAsync: it decodes the entry payload into *Request and invokes
// the registered Handler — the async sibling of the sync Dispatch round-trip.
func TestCommandBus_Enqueue_DispatchAsyncRoundTrip(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	h := &fakeRemoteCommandHandler{}
	require.NoError(t, cmdremote.Register(reg, h))

	entry := newCommandEntry(t, cmdremote.Request{DeviceID: "d1", CommandType: "reboot", Payload: "now"})
	require.NoError(t, cmdremote.DispatchAsync(context.Background(), reg, entry))

	assert.True(t, h.called, "handler must be invoked")
	require.NotNil(t, h.lastReq)
	assert.Equal(t, "d1", h.lastReq.DeviceID)
	assert.Equal(t, "reboot", h.lastReq.CommandType)
}

// TestCommandBus_Enqueue_DispatchAsyncHandlerError verifies the handler error is
// returned (the relay routes it to MarkRetry). The payload is schema-VALID
// (DeviceID + Payload present) so it passes the #1588 value funnel and reaches
// the handler — the handler's own error is what propagates, not a validation error.
func TestCommandBus_Enqueue_DispatchAsyncHandlerError(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	sentinel := errors.New("downstream failure")
	require.NoError(t, cmdremote.Register(reg, &fakeRemoteCommandHandler{retErr: sentinel}))

	entry := newCommandEntry(t, cmdremote.Request{DeviceID: "d1", CommandType: "reboot", Payload: "x"})
	err := cmdremote.DispatchAsync(context.Background(), reg, entry)
	assert.ErrorIs(t, err, sentinel)
}

// TestCommandBus_Enqueue_DispatchAsyncBadPayload verifies a payload that does not
// decode into *Request returns ErrValidationFailed before reaching a handler.
func TestCommandBus_Enqueue_DispatchAsyncBadPayload(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	h := &fakeRemoteCommandHandler{}
	require.NoError(t, cmdremote.Register(reg, h))

	entry, err := kout.NewEntry(clock.Real(), context.Background(), string(cmdremote.DispatchID), []byte("not json"))
	require.NoError(t, err)
	errcodetest.AssertCode(t, cmdremote.DispatchAsync(context.Background(), reg, entry), errcode.ErrValidationFailed)
	assert.False(t, h.called, "handler must not run on a malformed payload")
}

// TestCommandBus_Enqueue_DispatchAsyncValueValidation is the #1588 value funnel:
// a payload that decodes fine into *Request but violates the request schema's
// value constraints (required / minLength / additionalProperties) is rejected
// with ErrValidationFailed BEFORE the handler runs, and the error is PERMANENT
// (kout.PermanentError) so the relay dead-letters it instead of burning retries.
// This is the untrusted-boundary counterpart to HTTP body validation; the sync
// Dispatch path stays unvalidated (first-party typed boundary, ADR §D8).
func TestCommandBus_Enqueue_DispatchAsyncValueValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		rawJSON string
	}{
		{"missing required payload", `{"deviceId":"d1","commandType":"reboot"}`},
		{"missing required deviceId", `{"payload":"x","commandType":"reboot"}`},
		{"missing required commandType", `{"deviceId":"d1","payload":"x"}`},                          // #1694 F9
		{"empty commandType violates minLength", `{"deviceId":"d1","payload":"x","commandType":""}`}, // #1694 F9
		{"empty deviceId violates minLength", `{"deviceId":"","payload":"x","commandType":"reboot"}`},
		{"empty payload violates minLength", `{"deviceId":"d1","payload":"","commandType":"reboot"}`},
		{"additionalProperties rejected", `{"deviceId":"d1","payload":"x","commandType":"reboot","bogus":"y"}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg := command.NewRegistry()
			h := &fakeRemoteCommandHandler{}
			require.NoError(t, cmdremote.Register(reg, h))

			err := cmdremote.DispatchAsync(context.Background(), reg, newRawCommandEntry(t, tc.rawJSON))

			errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)
			assert.False(t, h.called, "handler must not run on a schema-invalid payload")
			var pe *kout.PermanentError
			assert.True(t, errors.As(err, &pe),
				"value-validation failure must be permanent (relay → MarkDead, not retry)")
		})
	}
}

// TestCommandBus_Enqueue_DispatchAsyncNoHandler verifies the no-handler branch.
// Payload is schema-VALID so it clears the value funnel and reaches the
// LookupHandler check (which is where the no-handler error originates).
func TestCommandBus_Enqueue_DispatchAsyncNoHandler(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	entry := newCommandEntry(t, cmdremote.Request{DeviceID: "d1", CommandType: "reboot", Payload: "x"})
	err := cmdremote.DispatchAsync(context.Background(), reg, entry)
	errcodetest.AssertCode(t, err, errcode.ErrCommandNotFound)
	var ec *errcode.Error
	if errors.As(err, &ec) {
		assert.Equal(t, errcode.KindNotFound, ec.Kind)
	}
}

// TestCommandBus_Enqueue_DispatchAsyncNilRegistry verifies the nil-registry guard
// fires first (before the value funnel). Payload is valid so the asserted error
// is unambiguously the nil-registry one, not a schema violation.
func TestCommandBus_Enqueue_DispatchAsyncNilRegistry(t *testing.T) {
	t.Parallel()
	entry := newCommandEntry(t, cmdremote.Request{DeviceID: "d1", CommandType: "reboot", Payload: "x"})
	err := cmdremote.DispatchAsync(context.Background(), nil, entry)
	errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)
}

// TestCommandBus_Enqueue_AsyncRelayEndToEnd plays the composition root: it binds
// the REAL generated cmdremote.DispatchAsync into a relay (keyed by the generated
// cmdremote.DispatchID — the direct-symbol shape COMMAND-ASYNC-DISPATCH-CALLER-01
// requires), seeds a command outbox entry, starts the relay, and asserts the
// entry is dispatched to the registered handler in-process and settles to
// published (consumed) — the full in-proc → outbox → relay → handler async loop.
func TestCommandBus_Enqueue_AsyncRelayEndToEnd(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	h := &fakeRemoteCommandHandler{}
	require.NoError(t, cmdremote.Register(reg, h))

	store := outboxtest.NewFakeStore()
	// The relay's command-dispatch path now wraps every dispatch in the Claimer
	// (#1698); ClaimKeyFromEntry requires the entry carry its idempotency identity
	// slot (AggregateID = subject, Metadata[CommandIDMetadataKey] = commandID) — the
	// exact shape command.EmitAsync produces. An identity-less entry is fail-closed
	// (dead-lettered), so seed an identity-bearing entry here.
	store.Seed(outbox.ClaimedEntry{Entry: newAsyncCommandEntry(t, cmdremote.Request{DeviceID: "d1", CommandType: "reboot", Payload: "now"})})

	relay := outbox.NewRelay(clock.Real(), store, &kout.DiscardPublisher{},
		outbox.RelayConfig{PollInterval: testtime.FastPoll}.WithDefaults())
	relay.WithCommandDispatch(reg, map[command.CommandID]command.AsyncDispatchFunc{
		cmdremote.DispatchID: cmdremote.DispatchAsync,
	}, idempotency.NewInMemClaimer(clock.Real()))

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer cancel()
	go func() { _ = relay.Start(ctx) }()

	require.NoError(t, store.WaitFor(ctx, func(rows []outboxtest.FakeRow) bool {
		return len(rows) == 1 && rows[0].Status == kout.StatePublished
	}), "command entry must settle to published after in-process dispatch")
	assert.True(t, h.called, "the registered enqueue handler must be invoked via the relay")
}

// TestCommandBus_Enqueue_AsyncRelayValueValidationDeadLetters is the end-to-end
// proof of the #1588 settle classification: a schema-invalid command entry, once
// claimed by the relay and dispatched to the REAL generated cmdremote.DispatchAsync,
// fails the value funnel with a PERMANENT error, so the relay routes it straight
// to MarkDead (StateDead) — not MarkRetry, not published, and the handler never
// runs. Mirrors AsyncRelayEndToEnd but with an invalid payload + dead terminal.
func TestCommandBus_Enqueue_AsyncRelayValueValidationDeadLetters(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	h := &fakeRemoteCommandHandler{}
	require.NoError(t, cmdremote.Register(reg, h))

	store := outboxtest.NewFakeStore()
	// Identity-bearing (so it passes the #1698 Claimer wrap and reaches the REAL
	// DispatchAsync) but schema-invalid payload (missing required "payload"): this
	// proves the #1588 value funnel dead-letters AT DISPATCH — distinct from the
	// #1698 missing-identity fail-closed, which would also dead-letter but BEFORE
	// DispatchAsync (and its value funnel) ever runs.
	invalidEntry, err := kout.NewEntry(clock.Real(), context.Background(), string(cmdremote.DispatchID),
		[]byte(`{"deviceId":"d1"}`),
		kout.WithAggregateID("d1"),
		kout.WithMetadata(map[string]string{command.CommandIDMetadataKey: "cmd-d1"}))
	require.NoError(t, err)
	store.Seed(outbox.ClaimedEntry{Entry: invalidEntry})

	relay := outbox.NewRelay(clock.Real(), store, &kout.DiscardPublisher{},
		outbox.RelayConfig{PollInterval: testtime.FastPoll}.WithDefaults())
	relay.WithCommandDispatch(reg, map[command.CommandID]command.AsyncDispatchFunc{
		cmdremote.DispatchID: cmdremote.DispatchAsync,
	}, idempotency.NewInMemClaimer(clock.Real()))

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer cancel()
	go func() { _ = relay.Start(ctx) }()

	require.NoError(t, store.WaitFor(ctx, func(rows []outboxtest.FakeRow) bool {
		return len(rows) == 1 && rows[0].Status == kout.StateDead
	}), "schema-invalid command entry must dead-letter (permanent), not retry or publish")
	assert.False(t, h.called, "the handler must never run for a schema-invalid payload")
}

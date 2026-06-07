package main

// commandbus_roundtrip_test.go — end-to-end exercise of the generated command-bus
// funnel for command.device-command.enqueue.v1 (#1044 PR-1). This is the only test
// that drives the REAL generated package (enqueue.Register / enqueue.Dispatch /
// enqueue.Handler) through a runtime command.Registry, covering the three runtime
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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	enqueue "github.com/ghbvf/gocell/generated/contracts/command/devicecommand/enqueue/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/runtime/command"
	"github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/outbox/outboxtest"
)

// fakeEnqueueHandler implements the generated enqueue.Handler interface.
type fakeEnqueueHandler struct {
	called  bool
	lastReq *enqueue.Request
	retErr  error
}

func (h *fakeEnqueueHandler) HandleEnqueue(_ context.Context, req *enqueue.Request) (*enqueue.Response, error) {
	h.called = true
	h.lastReq = req
	if h.retErr != nil {
		return nil, h.retErr
	}
	return &enqueue.Response{Data: &enqueue.ResponseData{ID: "cmd-1", Status: "Pending"}}, nil
}

func TestCommandBus_Enqueue_RegisterDispatchRoundTrip(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	h := &fakeEnqueueHandler{}
	if err := enqueue.Register(reg, h); err != nil {
		t.Fatalf("Register: %v", err)
	}

	req := &enqueue.Request{CommandType: "reboot", Payload: "now"}
	resp, err := enqueue.Dispatch(context.Background(), reg, req)
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
	if err := enqueue.Register(reg, &fakeEnqueueHandler{retErr: sentinel}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, err := enqueue.Dispatch(context.Background(), reg, &enqueue.Request{Payload: "x"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected handler error to propagate, got: %v", err)
	}
}

func TestCommandBus_Enqueue_DispatchWithoutHandler(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	_, err := enqueue.Dispatch(context.Background(), reg, &enqueue.Request{Payload: "x"})
	errcodetest.AssertCode(t, err, errcode.ErrCommandNotFound)
	var ec *errcode.Error
	if errors.As(err, &ec) && ec.Kind != errcode.KindNotFound {
		t.Errorf("expected KindNotFound, got %v", ec.Kind)
	}
}

func TestCommandBus_Enqueue_DuplicateRegistration(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	if err := enqueue.Register(reg, &fakeEnqueueHandler{}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := enqueue.Register(reg, &fakeEnqueueHandler{})
	errcodetest.AssertCode(t, err, errcode.ErrConflict)
}

func TestCommandBus_Enqueue_RegisterNilHandler(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	// Untyped nil and typed-nil both rejected by the generated Register guard.
	errcodetest.AssertCode(t, enqueue.Register(reg, nil), errcode.ErrValidationFailed)
	var typedNil *fakeEnqueueHandler
	errcodetest.AssertCode(t, enqueue.Register(reg, typedNil), errcode.ErrValidationFailed)
}

// TestCommandBus_Enqueue_RegisterNilRegistry verifies the generated Register
// guards a nil *command.Registry with a structured error instead of panicking
// on the receiver deref (#1578 F4).
func TestCommandBus_Enqueue_RegisterNilRegistry(t *testing.T) {
	t.Parallel()
	errcodetest.AssertCode(t, enqueue.Register(nil, &fakeEnqueueHandler{}), errcode.ErrValidationFailed)
}

// TestCommandBus_Enqueue_DispatchNilRegistry verifies the generated Dispatch
// guards a nil *command.Registry with a structured error instead of panicking
// on the receiver deref (#1578 F4).
func TestCommandBus_Enqueue_DispatchNilRegistry(t *testing.T) {
	t.Parallel()
	_, err := enqueue.Dispatch(context.Background(), nil, &enqueue.Request{Payload: "x"})
	errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)
}

// TestCommandBus_Enqueue_DispatchNilRequest verifies the generated Dispatch
// rejects a nil request before it can reach a handler (#1578 F4).
func TestCommandBus_Enqueue_DispatchNilRequest(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	if err := enqueue.Register(reg, &fakeEnqueueHandler{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, err := enqueue.Dispatch(context.Background(), reg, nil)
	errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)
}

// ---------------------------------------------------------------------------
// Async dispatch (#1667 / ADR 202606040550-1044 §5 ④)
// ---------------------------------------------------------------------------

// newCommandEntry builds a command outbox.Entry whose RoutingTopic == the enqueue
// DispatchID, carrying req as the JSON payload — what a producer writes to the
// outbox and the relay claims for in-process async dispatch.
func newCommandEntry(t *testing.T, req enqueue.Request) kout.Entry {
	t.Helper()
	payload, err := json.Marshal(req)
	require.NoError(t, err)
	entry, err := kout.NewEntry(clock.Real(), context.Background(), string(enqueue.DispatchID), payload)
	require.NoError(t, err)
	return entry
}

// newAsyncCommandEntry builds a command entry that carries the idempotency
// identity slot the relay's Claimer-wrapped dispatch path requires (AggregateID =
// subject, Metadata[CommandIDMetadataKey] = commandID), mirroring the shape
// command.EmitAsync produces for an async command. The async relay end-to-end
// test seeds this so ClaimKeyFromEntry derives a key instead of fail-closing.
func newAsyncCommandEntry(t *testing.T, req enqueue.Request) kout.Entry {
	t.Helper()
	payload, err := json.Marshal(req)
	require.NoError(t, err)
	entry, err := kout.NewEntry(clock.Real(), context.Background(), string(enqueue.DispatchID), payload,
		kout.WithAggregateID(req.DeviceID),
		kout.WithMetadata(map[string]string{command.CommandIDMetadataKey: "cmd-" + req.DeviceID}))
	require.NoError(t, err)
	return entry
}

// TestCommandBus_Enqueue_DispatchAsyncRoundTrip drives the REAL generated
// enqueue.DispatchAsync: it decodes the entry payload into *Request and invokes
// the registered Handler — the async sibling of the sync Dispatch round-trip.
func TestCommandBus_Enqueue_DispatchAsyncRoundTrip(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	h := &fakeEnqueueHandler{}
	require.NoError(t, enqueue.Register(reg, h))

	entry := newCommandEntry(t, enqueue.Request{DeviceID: "d1", CommandType: "reboot", Payload: "now"})
	require.NoError(t, enqueue.DispatchAsync(context.Background(), reg, entry))

	assert.True(t, h.called, "handler must be invoked")
	require.NotNil(t, h.lastReq)
	assert.Equal(t, "d1", h.lastReq.DeviceID)
	assert.Equal(t, "reboot", h.lastReq.CommandType)
}

// TestCommandBus_Enqueue_DispatchAsyncHandlerError verifies the handler error is
// returned (the relay routes it to MarkRetry).
func TestCommandBus_Enqueue_DispatchAsyncHandlerError(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	sentinel := errors.New("downstream failure")
	require.NoError(t, enqueue.Register(reg, &fakeEnqueueHandler{retErr: sentinel}))

	err := enqueue.DispatchAsync(context.Background(), reg, newCommandEntry(t, enqueue.Request{Payload: "x"}))
	assert.ErrorIs(t, err, sentinel)
}

// TestCommandBus_Enqueue_DispatchAsyncBadPayload verifies a payload that does not
// decode into *Request returns ErrValidationFailed before reaching a handler.
func TestCommandBus_Enqueue_DispatchAsyncBadPayload(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	h := &fakeEnqueueHandler{}
	require.NoError(t, enqueue.Register(reg, h))

	entry, err := kout.NewEntry(clock.Real(), context.Background(), string(enqueue.DispatchID), []byte("not json"))
	require.NoError(t, err)
	errcodetest.AssertCode(t, enqueue.DispatchAsync(context.Background(), reg, entry), errcode.ErrValidationFailed)
	assert.False(t, h.called, "handler must not run on a malformed payload")
}

// TestCommandBus_Enqueue_DispatchAsyncNoHandler verifies the no-handler branch.
func TestCommandBus_Enqueue_DispatchAsyncNoHandler(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	err := enqueue.DispatchAsync(context.Background(), reg, newCommandEntry(t, enqueue.Request{Payload: "x"}))
	errcodetest.AssertCode(t, err, errcode.ErrCommandNotFound)
	var ec *errcode.Error
	if errors.As(err, &ec) {
		assert.Equal(t, errcode.KindNotFound, ec.Kind)
	}
}

// TestCommandBus_Enqueue_DispatchAsyncNilRegistry verifies the nil-registry guard.
func TestCommandBus_Enqueue_DispatchAsyncNilRegistry(t *testing.T) {
	t.Parallel()
	err := enqueue.DispatchAsync(context.Background(), nil, newCommandEntry(t, enqueue.Request{Payload: "x"}))
	errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)
}

// TestCommandBus_Enqueue_AsyncRelayEndToEnd plays the composition root: it binds
// the REAL generated enqueue.DispatchAsync into a relay (keyed by the generated
// enqueue.DispatchID — the direct-symbol shape COMMAND-ASYNC-DISPATCH-CALLER-01
// requires), seeds a command outbox entry, starts the relay, and asserts the
// entry is dispatched to the registered handler in-process and settles to
// published (consumed) — the full in-proc → outbox → relay → handler async loop.
func TestCommandBus_Enqueue_AsyncRelayEndToEnd(t *testing.T) {
	t.Parallel()
	reg := command.NewRegistry()
	h := &fakeEnqueueHandler{}
	require.NoError(t, enqueue.Register(reg, h))

	store := outboxtest.NewFakeStore()
	// The relay's command-dispatch path now wraps every dispatch in the Claimer
	// (#1698); ClaimKeyFromEntry requires the entry carry its idempotency identity
	// slot (AggregateID = subject, Metadata[CommandIDMetadataKey] = commandID) — the
	// exact shape command.EmitAsync produces. An identity-less entry is fail-closed
	// (dead-lettered), so seed an identity-bearing entry here.
	store.Seed(outbox.ClaimedEntry{Entry: newAsyncCommandEntry(t, enqueue.Request{DeviceID: "d1", Payload: "now"})})

	relay := outbox.NewRelay(clock.Real(), store, &kout.DiscardPublisher{},
		outbox.RelayConfig{PollInterval: 5 * time.Millisecond}.WithDefaults())
	relay.WithCommandDispatch(reg, map[command.CommandID]command.AsyncDispatchFunc{
		enqueue.DispatchID: enqueue.DispatchAsync,
	}, idempotency.NewInMemClaimer(clock.Real()))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = relay.Start(ctx) }()

	require.NoError(t, store.WaitFor(ctx, func(rows []outboxtest.FakeRow) bool {
		return len(rows) == 1 && rows[0].Status == kout.StatePublished
	}), "command entry must settle to published after in-process dispatch")
	assert.True(t, h.called, "the registered enqueue handler must be invoked via the relay")
}

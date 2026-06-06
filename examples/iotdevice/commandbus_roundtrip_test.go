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
	"errors"
	"testing"

	enqueue "github.com/ghbvf/gocell/generated/contracts/command/devicecommand/enqueue/v1"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/runtime/command"
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

package wrapper_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

func eventSpec() wrapper.ContractSpec {
	return wrapper.ContractSpec{
		ID:        "event.session.revoked.v1",
		Kind:      "event",
		Transport: "amqp",
		Topic:     "session.revoked.v1",
	}
}

func TestWrapConsumer_PassesAckResultThrough(t *testing.T) {
	t.Parallel()
	tr := &spyTracer{}
	inner := func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}
	w := wrapper.WrapConsumer(eventSpec(), inner, wrapper.WithTracer(tr))
	res := w(context.Background(), outbox.Entry{EventType: "session.revoked.v1"})
	if res.Disposition != outbox.DispositionAck {
		t.Errorf("want Ack, got %v", res.Disposition)
	}
	span := tr.only(t)
	if span.status != wrapper.StatusOK {
		t.Errorf("want StatusOK on Ack, got %v", span.status)
	}
	if !span.ended {
		t.Error("span not ended")
	}
	if got := span.attrMap()["gocell.contract.id"]; got != "event.session.revoked.v1" {
		t.Errorf("contract.id attr: got %v", got)
	}
	if got := span.attrMap()["messaging.destination"]; got != "session.revoked.v1" {
		t.Errorf("messaging.destination attr: got %v", got)
	}
	if got := span.attrMap()["messaging.system"]; got != "amqp" {
		t.Errorf("messaging.system attr: got %v", got)
	}
	if span.name != "CONSUME session.revoked.v1" {
		t.Errorf("span name: got %q", span.name)
	}
}

func TestWrapConsumer_MarksErrorOnRequeue(t *testing.T) {
	t.Parallel()
	tr := &spyTracer{}
	transient := errors.New("db unreachable")
	inner := func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: transient}
	}
	w := wrapper.WrapConsumer(eventSpec(), inner, wrapper.WithTracer(tr))
	res := w(context.Background(), outbox.Entry{})

	if res.Disposition != outbox.DispositionRequeue {
		t.Errorf("want Requeue, got %v", res.Disposition)
	}
	span := tr.only(t)
	if span.status != wrapper.StatusError {
		t.Errorf("want StatusError on transient, got %v", span.status)
	}
	if len(span.errs) == 0 || !errors.Is(span.errs[0], transient) {
		t.Errorf("RecordError not called with %v, got %v", transient, span.errs)
	}
}

func TestWrapConsumer_MarksErrorOnReject(t *testing.T) {
	t.Parallel()
	tr := &spyTracer{}
	permanent := outbox.NewPermanentError(errors.New("schema mismatch"))
	inner := func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionReject, Err: permanent}
	}
	w := wrapper.WrapConsumer(eventSpec(), inner, wrapper.WithTracer(tr))
	res := w(context.Background(), outbox.Entry{})

	if res.Disposition != outbox.DispositionReject {
		t.Errorf("want Reject, got %v", res.Disposition)
	}
	span := tr.only(t)
	if span.status != wrapper.StatusError {
		t.Errorf("want StatusError on reject, got %v", span.status)
	}
}

func TestWrapConsumer_PanicsOnNonEventSpec(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on http spec")
		}
	}()
	_ = wrapper.WrapConsumer(loginSpec(), func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})
}

func TestWrapConsumer_PanicsOnNilFn(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil fn")
		}
	}()
	_ = wrapper.WrapConsumer(eventSpec(), nil)
}

func TestWrapConsumer_PutsContractIDInContext(t *testing.T) {
	t.Parallel()
	var seen string
	inner := func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		seen = wrapper.ContractIDFromContext(ctx)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}
	w := wrapper.WrapConsumer(eventSpec(), inner)
	_ = w(context.Background(), outbox.Entry{})
	if seen != "event.session.revoked.v1" {
		t.Errorf("ContractID missing from ctx; got %q", seen)
	}
}

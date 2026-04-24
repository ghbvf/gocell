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
	tr := &spyTracer{}
	setSpyTracer(t, tr)

	inner := func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}
	w := wrapper.WrapConsumer(eventSpec(), inner)
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
	tr := &spyTracer{}
	setSpyTracer(t, tr)

	transient := errors.New("db unreachable")
	inner := func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: transient}
	}
	w := wrapper.WrapConsumer(eventSpec(), inner)
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
	tr := &spyTracer{}
	setSpyTracer(t, tr)

	permanent := outbox.NewPermanentError(errors.New("schema mismatch"))
	inner := func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionReject, Err: permanent}
	}
	w := wrapper.WrapConsumer(eventSpec(), inner)
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
	wrapper.SetTracer(wrapper.NoopTracer{})
	t.Cleanup(wrapper.ResetTracerForTest)

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

// TestWrapConsumer_PanicsIfTracerNotSet verifies that an unset tracer panics
// on first event delivery with a descriptive message.
func TestWrapConsumer_PanicsIfTracerNotSet(t *testing.T) {
	wrapper.ResetTracerForTest()
	t.Cleanup(wrapper.ResetTracerForTest)

	w := wrapper.WrapConsumer(eventSpec(), func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when tracer not set")
		}
	}()
	_ = w(context.Background(), outbox.Entry{})
}

// TestWrapConsumer_PanicInHandler verifies that a panic in fn ends the span
// (SetStatus=Error, RecordError called, End called) and the panic is re-thrown.
func TestWrapConsumer_PanicInHandler(t *testing.T) {
	tr := &spyTracer{}
	setSpyTracer(t, tr)

	boom := errors.New("consumer exploded")
	inner := func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		panic(boom)
	}
	w := wrapper.WrapConsumer(eventSpec(), inner)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic to be re-thrown")
		}
		if !errors.Is(r.(error), boom) {
			t.Errorf("expected boom, got %v", r)
		}
		span := tr.only(t)
		if !span.ended {
			t.Error("span must be ended on panic")
		}
		if span.status != wrapper.StatusError {
			t.Errorf("want StatusError on panic, got %v", span.status)
		}
		if len(span.errs) == 0 {
			t.Error("RecordError must be called on panic")
		}
	}()
	_ = w(context.Background(), outbox.Entry{})
}

// TestWrapConsumer_PanicNonError verifies non-error panic values are also
// wrapped in an error for RecordError.
func TestWrapConsumer_PanicNonError(t *testing.T) {
	tr := &spyTracer{}
	setSpyTracer(t, tr)

	inner := func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		panic("string panic value")
	}
	w := wrapper.WrapConsumer(eventSpec(), inner)

	defer func() {
		_ = recover()
		span := tr.only(t)
		if span.status != wrapper.StatusError {
			t.Errorf("want StatusError on non-error panic, got %v", span.status)
		}
		if len(span.errs) == 0 {
			t.Error("RecordError must be called even on non-error panic")
		}
	}()
	_ = w(context.Background(), outbox.Entry{})
}

// TestWrapConsumer_ReExportedConstants verifies wrapper re-exports match outbox.
func TestWrapConsumer_ReExportedConstants(t *testing.T) {
	t.Parallel()
	if wrapper.DispositionAck != outbox.DispositionAck {
		t.Errorf("wrapper.DispositionAck (%v) != outbox.DispositionAck (%v)", wrapper.DispositionAck, outbox.DispositionAck)
	}
	if wrapper.DispositionRequeue != outbox.DispositionRequeue {
		t.Errorf("wrapper.DispositionRequeue mismatch")
	}
	if wrapper.DispositionReject != outbox.DispositionReject {
		t.Errorf("wrapper.DispositionReject mismatch")
	}
}

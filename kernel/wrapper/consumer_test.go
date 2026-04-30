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
	inner := func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}
	w := wrapper.MustWrapConsumer(tr, eventSpec(), inner)
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
	transient := errors.New("db unreachable")
	inner := func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: transient}
	}
	w := wrapper.MustWrapConsumer(tr, eventSpec(), inner)
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

func TestWrapConsumer_RedactsDispositionErrors(t *testing.T) {
	tr := &spyTracer{}
	inner := func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: errors.New("raw token")}
	}
	w := wrapper.MustWrapConsumer(tr, eventSpec(), inner, wrapper.WithConsumerErrorRedactor(func(error) error {
		return errors.New("redacted")
	}))
	res := w(context.Background(), outbox.Entry{})

	if res.Disposition != outbox.DispositionRequeue {
		t.Errorf("want Requeue, got %v", res.Disposition)
	}
	span := tr.only(t)
	if len(span.errs) != 1 || span.errs[0].Error() != "redacted" {
		t.Errorf("redacted error not recorded, got %v", span.errs)
	}
}

func TestWrapConsumer_RedactsMissingDispositionErrorFallback(t *testing.T) {
	tr := &spyTracer{}
	inner := func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionReject}
	}
	w := wrapper.MustWrapConsumer(tr, eventSpec(), inner, wrapper.WithConsumerErrorRedactor(func(err error) error {
		return errors.New("safe: " + err.Error())
	}))
	_ = w(context.Background(), outbox.Entry{})

	span := tr.only(t)
	if len(span.errs) != 1 || span.errs[0].Error() != "safe: consumer returned Reject without error" {
		t.Errorf("redacted fallback error not recorded, got %v", span.errs)
	}
}

func TestWrapConsumer_MarksErrorOnReject(t *testing.T) {
	tr := &spyTracer{}
	permanent := outbox.NewPermanentError(errors.New("schema mismatch"))
	inner := func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionReject, Err: permanent}
	}
	w := wrapper.MustWrapConsumer(tr, eventSpec(), inner)
	res := w(context.Background(), outbox.Entry{})

	if res.Disposition != outbox.DispositionReject {
		t.Errorf("want Reject, got %v", res.Disposition)
	}
	span := tr.only(t)
	if span.status != wrapper.StatusError {
		t.Errorf("want StatusError on reject, got %v", span.status)
	}
}

func TestWrapConsumer_ReturnsErrorOnNonEventSpec(t *testing.T) {
	t.Parallel()
	_, err := wrapper.WrapConsumer(wrapper.NoopTracer{}, loginSpec(), func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})
	if err == nil {
		t.Fatal("expected error on http spec")
	}
}

func TestWrapConsumer_ReturnsErrorOnNilFn(t *testing.T) {
	t.Parallel()
	_, err := wrapper.WrapConsumer(wrapper.NoopTracer{}, eventSpec(), nil)
	if err == nil {
		t.Fatal("expected error on nil fn")
	}
}

func TestMustWrapConsumer_PanicsOnNilFn(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on nil fn")
		}
	}()
	_ = wrapper.MustWrapConsumer(wrapper.NoopTracer{}, eventSpec(), nil)
}

func TestWrapConsumer_PutsContractIDInContext(t *testing.T) {
	t.Parallel()
	var seen string
	inner := func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		seen = wrapper.ContractIDFromContext(ctx)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}
	w := wrapper.MustWrapConsumer(wrapper.NoopTracer{}, eventSpec(), inner)
	_ = w(context.Background(), outbox.Entry{})
	if seen != "event.session.revoked.v1" {
		t.Errorf("ContractID missing from ctx; got %q", seen)
	}
}

// TestWrapConsumer_NilTracer_FallsBackToNoop verifies WrapConsumer handles a
// nil Tracer argument the same way as HTTPHandler: falls back to NoopTracer{}
// so missing wiring never panics at event delivery time.
func TestWrapConsumer_NilTracer_FallsBackToNoop(t *testing.T) {
	t.Parallel()
	inner := func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}
	w := wrapper.MustWrapConsumer(nil, eventSpec(), inner)
	res := w(context.Background(), outbox.Entry{})
	if res.Disposition != outbox.DispositionAck {
		t.Errorf("want Ack with nil tracer, got %v", res.Disposition)
	}
}

// TestWrapConsumer_PanicInHandler verifies that a panic in fn ends the span
// (SetStatus=Error, RecordError called, End called) and the panic is re-thrown.
func TestWrapConsumer_PanicInHandler(t *testing.T) {
	tr := &spyTracer{}
	boom := errors.New("consumer exploded")
	inner := func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		panic(boom)
	}
	w := wrapper.MustWrapConsumer(tr, eventSpec(), inner)

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
	inner := func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		panic("string panic value")
	}
	w := wrapper.MustWrapConsumer(tr, eventSpec(), inner)

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

// TestWrapConsumer_InvalidDispositionRecordsError verifies that a result
// with an unrecognized Disposition (e.g. the zero value of
// outbox.HandleResult) produces SetStatus(Error, "invalid disposition")
// AND a RecordError event on the span — symmetric with the Requeue/Reject
// branches so ops can recognize the misbehaving handler from the span
// alone instead of cross-referencing logs.
func TestWrapConsumer_InvalidDispositionRecordsError(t *testing.T) {
	tr := &spyTracer{}
	inner := func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{} // zero value: Disposition is invalid
	}
	w := wrapper.MustWrapConsumer(tr, eventSpec(), inner)
	res := w(context.Background(), outbox.Entry{})

	// Result is passed through untouched — downstream decides downgrade.
	if res.Disposition != outbox.Disposition(0) {
		t.Errorf("want zero-value disposition pass-through, got %v", res.Disposition)
	}
	span := tr.only(t)
	if span.status != wrapper.StatusError {
		t.Errorf("want StatusError on invalid disposition, got %v", span.status)
	}
	if span.stDesc != "invalid disposition" {
		t.Errorf("want status desc 'invalid disposition', got %q", span.stDesc)
	}
	if len(span.errs) == 0 {
		t.Fatal("RecordError must be called on invalid disposition (F3)")
	}
}

// TestWrapConsumer_InvalidDispositionWithExplicitError ensures the
// recorded error prefers the handler-supplied res.Err over the synthetic
// fallback when both are present — the redactor path is exercised too.
func TestWrapConsumer_InvalidDispositionWithExplicitError(t *testing.T) {
	tr := &spyTracer{}
	explicit := errors.New("handler chose nonsense")
	inner := func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.Disposition(99), Err: explicit}
	}
	w := wrapper.MustWrapConsumer(tr, eventSpec(), inner)
	_ = w(context.Background(), outbox.Entry{})
	span := tr.only(t)
	if len(span.errs) != 1 || !errors.Is(span.errs[0], explicit) {
		t.Errorf("want explicit err recorded, got %v", span.errs)
	}
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

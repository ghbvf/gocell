package wrapper_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

func TestWrapSubscriber_ClaimDoneSpanEndsAfterSettlement(t *testing.T) {
	tr := &spyTracer{}
	wrapped := mustWrapSubscriberForTest(t, tr, eventSpec(),
		func(ctx context.Context, _ outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
			if got := wrapper.ContractIDFromContext(ctx); got != eventSpec().ID {
				t.Fatalf("contract id missing from handler context: %q", got)
			}
			return outbox.HandleResult{Disposition: outbox.DispositionAck}, nil
		})

	entry := outbox.Entry{ID: "evt-done", Topic: eventSpec().Topic}
	res, settlement := wrapped(context.Background(), entry)
	if settlement != nil {
		t.Fatalf("ClaimDone-style handler returned settlement: %T", settlement)
	}

	span := tr.only(t)
	if span.ended {
		t.Fatal("span ended before final settlement notification")
	}
	outbox.NotifySettlement(context.Background(), res, entry,
		outbox.DispositionAck, outbox.SettlementResultSuccess, nil)

	if !span.ended {
		t.Fatal("span must end after final settlement notification")
	}
	if span.status != wrapper.StatusOK {
		t.Fatalf("want OK status, got %v", span.status)
	}
}

func TestWrapSubscriber_ClaimBusySpanRecordsRequeueSettlement(t *testing.T) {
	tr := &spyTracer{}
	wrapped := mustWrapSubscriberForTest(t, tr, eventSpec(),
		func(context.Context, outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
			return outbox.HandleResult{Disposition: outbox.DispositionRequeue}, nil
		})

	entry := outbox.Entry{ID: "evt-busy", Topic: eventSpec().Topic}
	res, _ := wrapped(context.Background(), entry)
	outbox.NotifySettlement(context.Background(), res, entry,
		outbox.DispositionRequeue, outbox.SettlementResultSuccess, nil)

	span := tr.only(t)
	if !span.ended {
		t.Fatal("span must end after requeue settlement")
	}
	if span.status != wrapper.StatusError {
		t.Fatalf("want error status for requeue delivery, got %v", span.status)
	}
	if span.stDesc != "requeue" {
		t.Fatalf("want requeue status description, got %q", span.stDesc)
	}
	if len(span.errs) != 1 {
		t.Fatalf("want fallback RecordError for nil Requeue error, got %d", len(span.errs))
	}
}

func TestWrapSubscriber_CommitFailedUsesFinalSettlementAndPreservesObservers(t *testing.T) {
	tr := &spyTracer{}
	var existing []outbox.SettlementObservation
	existingObserver := outbox.SettlementObserverFunc(func(_ context.Context, obs outbox.SettlementObservation) {
		existing = append(existing, obs)
	})
	commitErr := errors.New("lease expired")

	wrapped := mustWrapSubscriberForTest(t, tr, eventSpec(),
		func(context.Context, outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
			return outbox.HandleResult{
				Disposition:         outbox.DispositionAck,
				SettlementObservers: []outbox.SettlementObserver{existingObserver},
			}, nil
		})

	entry := outbox.Entry{ID: "evt-commit-failed", Topic: eventSpec().Topic}
	res, _ := wrapped(context.Background(), entry)
	if len(res.SettlementObservers) != 2 {
		t.Fatalf("want existing observer plus tracing observer, got %d", len(res.SettlementObservers))
	}

	outbox.NotifySettlement(context.Background(), res, entry,
		outbox.DispositionRequeue, outbox.SettlementResultCommitFailed, commitErr)

	if len(existing) != 1 {
		t.Fatalf("existing observer was not preserved")
	}
	span := tr.only(t)
	if !span.ended {
		t.Fatal("span must end after commit_failed settlement")
	}
	if span.status != wrapper.StatusError || span.stDesc != "commit_failed" {
		t.Fatalf("want commit_failed error status, got %v/%q", span.status, span.stDesc)
	}
	if len(span.errs) != 1 || !errors.Is(span.errs[0], commitErr) {
		t.Fatalf("want commit error recorded, got %v", span.errs)
	}
}

func TestWrapSubscriber_PanicEndsSpan(t *testing.T) {
	tr := &spyTracer{}
	boom := errors.New("subscriber handler exploded")
	wrapped := mustWrapSubscriberForTest(t, tr, eventSpec(),
		func(context.Context, outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
			panic(boom)
		})

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic to be re-thrown")
		}
		if !errors.Is(r.(error), boom) {
			t.Fatalf("expected boom, got %v", r)
		}
		span := tr.only(t)
		if !span.ended {
			t.Fatal("span must be ended on panic")
		}
		if span.status != wrapper.StatusError || span.stDesc != "panic" {
			t.Fatalf("want panic error status, got %v/%q", span.status, span.stDesc)
		}
	}()

	_, _ = wrapped(context.Background(), outbox.Entry{ID: "evt-panic", Topic: eventSpec().Topic})
}

func TestWrapSubscriber_ReturnsErrorsForInvalidInputs(t *testing.T) {
	t.Parallel()

	ackHandler := func(context.Context, outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}, nil
	}

	if _, err := wrapper.WrapSubscriber(wrapper.NoopTracer{}, eventSpec(), nil); err == nil {
		t.Fatal("expected error on nil subscriber handler")
	}
	if _, err := wrapper.WrapSubscriber(wrapper.NoopTracer{}, loginSpec(), ackHandler); err == nil {
		t.Fatal("expected error on non-event spec")
	}

	invalid := eventSpec()
	invalid.ID = ""
	if _, err := wrapper.WrapSubscriber(wrapper.NoopTracer{}, invalid, ackHandler); err == nil {
		t.Fatal("expected error on invalid event spec")
	}
}

// mustWrapSubscriberForTest returns a SubscriberHandler from
// wrapper.WrapSubscriber, failing the test on error. This is a test-only
// helper that replaces the deleted wrapper.MustWrapSubscriber: production
// callers always handle the error explicitly (N8 (c)).
func mustWrapSubscriberForTest(
	t *testing.T,
	tr wrapper.Tracer,
	spec wrapper.ContractSpec,
	fn outbox.SubscriberHandler,
) outbox.SubscriberHandler {
	t.Helper()
	wrapped, err := wrapper.WrapSubscriber(tr, spec, fn)
	if err != nil {
		t.Fatalf("WrapSubscriber: %v", err)
	}
	return wrapped
}

func TestWrapSubscriber_NilTracerFallsBackToNoop(t *testing.T) {
	t.Parallel()

	wrapped := mustWrapSubscriberForTest(t, nil, eventSpec(),
		func(context.Context, outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
			return outbox.HandleResult{Disposition: outbox.DispositionAck}, nil
		})
	res, settlement := wrapped(context.Background(), outbox.Entry{ID: "evt-noop", Topic: eventSpec().Topic})
	if settlement != nil {
		t.Fatalf("want nil settlement, got %T", settlement)
	}
	outbox.NotifySettlement(context.Background(), res, outbox.Entry{ID: "evt-noop", Topic: eventSpec().Topic},
		outbox.DispositionAck, outbox.SettlementResultSuccess, nil)
}

func TestWrapSubscriber_SettlementStatusBranches(t *testing.T) {
	tests := []struct {
		name        string
		disposition outbox.Disposition
		result      outbox.SettlementResult
		err         error
		wantDesc    string
	}{
		{
			name:        "reject_success",
			disposition: outbox.DispositionReject,
			result:      outbox.SettlementResultSuccess,
			wantDesc:    "reject",
		},
		{
			name:        "invalid_success",
			disposition: outbox.Disposition(99),
			result:      outbox.SettlementResultSuccess,
			wantDesc:    "invalid disposition",
		},
		{
			name:        "retry_exhausted",
			disposition: outbox.DispositionReject,
			result:      outbox.SettlementResultRetryExhausted,
			err:         errors.New("retry budget exhausted"),
			wantDesc:    "retry_exhausted",
		},
		{
			name:        "ack_failed",
			disposition: outbox.DispositionAck,
			result:      outbox.SettlementResultAckFailed,
			err:         errors.New("broker ack failed"),
			wantDesc:    "ack_failed",
		},
		{
			name:        "nack_failed",
			disposition: outbox.DispositionRequeue,
			result:      outbox.SettlementResultNackFailed,
			err:         errors.New("broker nack failed"),
			wantDesc:    "nack_failed",
		},
		{
			name:        "unknown_result",
			disposition: outbox.DispositionAck,
			result:      outbox.SettlementResult("unexpected"),
			err:         errors.New("unexpected settlement"),
			wantDesc:    "unexpected",
		},
		{
			name:        "empty_unknown_result",
			disposition: outbox.DispositionAck,
			result:      outbox.SettlementResult(""),
			wantDesc:    "unknown settlement result",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := &spyTracer{}
			wrapped := mustWrapSubscriberForTest(t, tr, eventSpec(),
				func(context.Context, outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
					return outbox.HandleResult{Disposition: outbox.DispositionAck}, nil
				})

			entry := outbox.Entry{ID: "evt-" + tt.name, Topic: eventSpec().Topic}
			res, _ := wrapped(context.Background(), entry)
			outbox.NotifySettlement(context.Background(), res, entry, tt.disposition, tt.result, tt.err)

			span := tr.only(t)
			if !span.ended {
				t.Fatal("span must end after settlement notification")
			}
			if span.status != wrapper.StatusError || span.stDesc != tt.wantDesc {
				t.Fatalf("want error status %q, got %v/%q", tt.wantDesc, span.status, span.stDesc)
			}
			if len(span.errs) != 1 {
				t.Fatalf("want exactly one recorded settlement error, got %d", len(span.errs))
			}
		})
	}
}

func TestWrapSubscriber_SettlementObserverEndsSpanOnce(t *testing.T) {
	tr := &spyTracer{}
	wrapped := mustWrapSubscriberForTest(t, tr, eventSpec(),
		func(context.Context, outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
			return outbox.HandleResult{Disposition: outbox.DispositionAck}, nil
		})

	entry := outbox.Entry{ID: "evt-once", Topic: eventSpec().Topic}
	res, _ := wrapped(context.Background(), entry)
	outbox.NotifySettlement(context.Background(), res, entry,
		outbox.DispositionAck, outbox.SettlementResultSuccess, nil)
	outbox.NotifySettlement(context.Background(), res, entry,
		outbox.DispositionRequeue, outbox.SettlementResultNackFailed, errors.New("late duplicate"))

	span := tr.only(t)
	if span.status != wrapper.StatusOK || span.stDesc != "" {
		t.Fatalf("span must keep first settlement status, got %v/%q", span.status, span.stDesc)
	}
	if len(span.errs) != 0 {
		t.Fatalf("duplicate settlement must not record a second error, got %v", span.errs)
	}
}

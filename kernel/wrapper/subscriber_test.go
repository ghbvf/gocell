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
	wrapped := wrapper.MustWrapSubscriber(tr, eventSpec(),
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
	wrapped := wrapper.MustWrapSubscriber(tr, eventSpec(),
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

	wrapped := wrapper.MustWrapSubscriber(tr, eventSpec(),
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
	wrapped := wrapper.MustWrapSubscriber(tr, eventSpec(),
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

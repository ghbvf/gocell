package outbox_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// Compile-time: idempotency.Receipt implicitly satisfies outbox.Settlement.
// This is the single most important contract of K#12 — it lets ConsumerBase.Wrap
// return (HandleResult, Settlement) using the receipt directly, with no adapter
// type and no leak of idempotency.Receipt into Subscriber implementations.
var _ outbox.Settlement = (idempotency.Receipt)(nil)

// Compile-time: NonAcquiredReceipt sentinel also satisfies Settlement so
// fail-open / ClaimDone / ClaimBusy paths can return it without nil branches.
var _ outbox.Settlement = idempotency.NonAcquiredReceipt()

// fakeSettlement records Commit/Release call counts and returns injected errors.
type fakeSettlement struct {
	commitCalled  int
	releaseCalled int
	commitErr     error
	releaseErr    error
}

func (f *fakeSettlement) Commit(_ context.Context) error {
	f.commitCalled++
	return f.commitErr
}

func (f *fakeSettlement) Release(_ context.Context) error {
	f.releaseCalled++
	return f.releaseErr
}

func TestSettlement_FakeCommitDelegates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		commitErr error
	}{
		{"happy", nil},
		{"propagates_error", errors.New("backend offline")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &fakeSettlement{commitErr: tc.commitErr}
			err := s.Commit(context.Background())
			if !errors.Is(err, tc.commitErr) {
				t.Fatalf("Commit error = %v; want %v", err, tc.commitErr)
			}
			if s.commitCalled != 1 {
				t.Fatalf("commit called %d times; want 1", s.commitCalled)
			}
		})
	}
}

func TestSettlement_FakeReleaseDelegates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		releaseErr error
	}{
		{"happy", nil},
		{"propagates_error", errors.New("network down")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &fakeSettlement{releaseErr: tc.releaseErr}
			err := s.Release(context.Background())
			if !errors.Is(err, tc.releaseErr) {
				t.Fatalf("Release error = %v; want %v", err, tc.releaseErr)
			}
			if s.releaseCalled != 1 {
				t.Fatalf("release called %d times; want 1", s.releaseCalled)
			}
		})
	}
}

// TestSettlement_NilCallerNilCheck documents the contract that Subscriber
// implementations MUST nil-check Settlement before invoking. ConsumerBase.Wrap
// returns nil Settlement on fail-open claim error / ClaimDone / ClaimBusy
// paths to avoid noop wrapper allocation.
func TestSettlement_NilCallerNilCheck(t *testing.T) {
	t.Parallel()
	var s outbox.Settlement
	if s != nil {
		t.Fatal("zero-value Settlement should be nil")
	}
}

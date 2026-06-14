package outbox_test

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/outbox"
)

func TestAck(t *testing.T) {
	t.Parallel()

	got := outbox.Ack()
	if got.Disposition != outbox.DispositionAck {
		t.Fatalf("Disposition = %v, want DispositionAck", got.Disposition)
	}
	if got.Err != nil {
		t.Fatalf("Err = %v, want nil", got.Err)
	}
}

func TestRequeue(t *testing.T) {
	t.Parallel()

	want := errors.New("transient")
	got := outbox.Requeue(want)
	if got.Disposition != outbox.DispositionRequeue {
		t.Fatalf("Disposition = %v, want DispositionRequeue", got.Disposition)
	}
	if !errors.Is(got.Err, want) {
		t.Fatalf("Err = %v, want %v", got.Err, want)
	}
}

func TestRequeueNilErr(t *testing.T) {
	t.Parallel()

	got := outbox.Requeue(nil)
	if got.Disposition != outbox.DispositionRequeue {
		t.Fatalf("Disposition = %v, want DispositionRequeue", got.Disposition)
	}
	if got.Err != nil {
		t.Fatalf("Err = %v, want nil (handler may signal Requeue without an error attached)", got.Err)
	}
}

func TestReject(t *testing.T) {
	t.Parallel()

	want := errors.New("permanent")
	got := outbox.Reject(want)
	if got.Disposition != outbox.DispositionReject {
		t.Fatalf("Disposition = %v, want DispositionReject", got.Disposition)
	}
	if !errors.Is(got.Err, want) {
		t.Fatalf("Err = %v, want %v", got.Err, want)
	}
}

func TestRejectNilErr(t *testing.T) {
	t.Parallel()

	got := outbox.Reject(nil)
	if got.Disposition != outbox.DispositionReject {
		t.Fatalf("Disposition = %v, want DispositionReject", got.Disposition)
	}
	if got.Err != nil {
		t.Fatalf("Err = %v, want nil", got.Err)
	}
}

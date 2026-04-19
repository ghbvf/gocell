package worker_test

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/kernel/worker"
)

// fakeWorker verifies the Worker interface contract at compile time.
type fakeWorker struct {
	startErr error
	stopErr  error
}

func (f *fakeWorker) Start(ctx context.Context) error { return f.startErr }
func (f *fakeWorker) Stop(ctx context.Context) error  { return f.stopErr }

var _ worker.Worker = (*fakeWorker)(nil)

func TestWorker_InterfaceContract(t *testing.T) {
	// This test is compile-time only; if Worker interface signature changes,
	// the compile-time assertion above will fail.
	var w worker.Worker = &fakeWorker{}
	if err := w.Start(context.Background()); err != nil {
		t.Errorf("unexpected Start error: %v", err)
	}
	if err := w.Stop(context.Background()); err != nil {
		t.Errorf("unexpected Stop error: %v", err)
	}
}

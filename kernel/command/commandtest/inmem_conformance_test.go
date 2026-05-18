package commandtest_test

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
)

// TestInMemQueue_Conformance enrolls InMemQueue against the shared
// conformance suite so both the in-memory and PG implementations stay
// in lock-step on Queue + ActiveScanner semantics.
func TestInMemQueue_Conformance(t *testing.T) {
	factory := func(t *testing.T) (command.Queue, command.ActiveScanner, commandtest.TxRunner, func() time.Time, func()) {
		t.Helper()
		q := commandtest.NewInMemQueue()
		return q, q, noopTxRunner{}, time.Now, func() {}
	}
	commandtest.RunQueueConformance(t, factory, commandtest.Features{
		RequiresAmbientTx:    false,
		SupportsLeaseRenewal: true,
	})
}

// noopTxRunner satisfies commandtest.TxRunner for the in-memory implementation
// which does not need a real transaction scope. It simply calls fn with the
// same context.
type noopTxRunner struct{}

func (noopTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

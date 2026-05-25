package mem

import (
	"context"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/kernel/clock"
)

// TestLeaseInvalidatedAfterTxReturn is the escape regression for the
// self-invalidating lease (#972). A repo method invoked with an inner ctx that
// escaped its RunInTx closure must NOT skip its per-call store.mu lock: once the
// tx returns and unlock() runs, the lease is dead, so the in-tx check reports
// false and the method takes the per-call lock (fail-closed). Run under -race: a
// stale-skip would be a concurrent map write on the unsynchronized maps.
//
// Under the pre-#972 witness model this FAILS: the witness proof is pointer
// identity that never expires, so the escaped ctx still reports "in tx" after the
// lock is released — the capability-escape the lease closes.
func TestLeaseInvalidatedAfterTxReturn(t *testing.T) {
	store := NewStore(clock.Real())
	runner, ok := store.TxRunner().(memTxRunner)
	if !ok {
		t.Fatal("Store.TxRunner() is not a memTxRunner")
	}

	var escaped context.Context
	err := runner.RunInTx(context.Background(), func(inner context.Context) error {
		escaped = inner // capture the in-tx ctx (carries a live lease)
		if !store.txHoldsLock(inner) {
			t.Fatal("in-tx check must be true inside RunInTx (lock genuinely held)")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunInTx: %v", err)
	}

	// After RunInTx returned, the lease must be dead for the escaped ctx.
	if store.txHoldsLock(escaped) {
		t.Fatal("in-tx check must be FALSE for an escaped inner ctx after the tx " +
			"returned (self-invalidating lease); a true result re-arms the " +
			"sentinel-without-lock flake — the lock is no longer held")
	}

	// Concurrency proof: many goroutines hit the escaped ctx. Because the lease is
	// dead, each takes per-call store.mu — no concurrent map write under -race.
	repo := store.UserRepository()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = repo.GetByID(escaped, "missing")
		}()
	}
	wg.Wait()
}

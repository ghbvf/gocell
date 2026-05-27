package mem

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth/credentialfence"
)

// TestLeaseInvalidatedAfterTxReturn is the escape regression for the
// self-invalidating lease (#972). A repo method invoked with an inner ctx that
// escaped its RunInTx closure must NOT skip its per-call store.mu lock: once the
// tx returns and unlock() runs, the lease is dead, so inLiveTx reports false and
// the method takes the per-call lock (fail-closed). Run under -race: with a
// regression (lease still live after return), the concurrent WRITE goroutines
// below would skip the lock and race the maps (concurrent map writes / DATA
// RACE). Note the writes are essential — concurrent map READS are safe in Go, so
// a read-only loop would not detect the stale-skip.
//
// Under the pre-#972 witness model this FAILS at the inLiveTx(escaped) assertion:
// the witness proof is pointer identity that never expires.
func TestLeaseInvalidatedAfterTxReturn(t *testing.T) {
	store := NewStore(clock.Real())
	runner, ok := store.TxRunner().(memTxRunner)
	if !ok {
		t.Fatal("Store.TxRunner() is not a memTxRunner")
	}
	repo := store.UserRepository()

	// Seed a user so the escaped-ctx goroutines exercise the read-modify-write
	// path (BumpAuthzEpoch rewrites usersByID/byName/byEmail) rather than a miss.
	seed, err := domain.NewUser("escapee", "escapee@example.com", "$2a$12$hash", time.Now())
	if err != nil {
		t.Fatalf("NewUser: %v", err)
	}
	seed.ID = "usr-escape-001"
	if err := repo.Create(context.Background(), seed); err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	var escaped context.Context
	err = runner.RunInTx(context.Background(), func(inner context.Context) error {
		escaped = inner // capture the in-tx ctx (carries a live lease)
		if !store.inLiveTx(inner) {
			t.Fatal("in-tx check must be true inside RunInTx (lock genuinely held)")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunInTx: %v", err)
	}

	// After RunInTx returned, the lease must be dead for the escaped ctx.
	if store.inLiveTx(escaped) {
		t.Fatal("in-tx check must be FALSE for an escaped inner ctx after the tx " +
			"returned (self-invalidating lease); a true result re-arms the " +
			"sentinel-without-lock flake — the lock is no longer held")
	}

	// Concurrency proof (write path): many goroutines drive BumpAuthzEpoch — a
	// read-modify-write on the shared maps — with the escaped (dead-lease) ctx.
	// Because inLiveTx is false, each call takes per-call store.mu and the writes
	// serialize: no concurrent map write under -race. A stale-skip regression
	// would race the maps here.
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := repo.BumpAuthzEpoch(escaped, seed.ID, credentialfence.Mint())
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Errorf("BumpAuthzEpoch on escaped ctx (per-call locked path): %v", e)
		}
	}
}

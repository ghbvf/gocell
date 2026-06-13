package sagaprojectiondeps

import (
	"context"
	"fmt"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	adapterpgsaga "github.com/ghbvf/gocell/adapters/postgres/saga"
	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/distlock"
)

// sagaProjectionLockNamespace scopes the per-projection leader-lock Redis keys.
// It is an owner/role-dimensioned namespace (not the "_runtime" sentinel), per
// observability.md §"Redis namespace": keys read as
// "saga-projection-lock:saga-journal-tailer:<cell>:<projection>".
const sagaProjectionLockNamespace adapterredis.KeyNamespace = "saga-projection-lock"

// Config carries the caller-owned infrastructure the resolver wires. Both
// handles are opened (and closed) by the composition root — not here — because
// the root already needs the PG pool for migrations and its own repos; opening a
// second pool would be wasteful and wrong.
type Config struct {
	// Pool is the PG pool for postgres topology. Required when
	// topo.StorageBackend()==postgres; ignored (must be nil) otherwise.
	Pool *adapterpg.Pool
	// RedisClient backs the per-projection leader lock in real multi-pod
	// topology. Required when topo.RequiresDistributedReplay(); ignored in
	// demo / single-pod.
	RedisClient *adapterredis.Client
}

// Deps is the resolved saga-projection dependency set. Journal is shared with the
// saga Coordinator (the resolver constructs exactly one journal); Reader is the
// same instance, narrowed to the GlobalReader the projection tailer scans.
type Deps struct {
	Journal    journal.Journal
	Reader     journal.GlobalReader
	OwnerStore projection.OwnerCheckpointStore
	Locker     distlock.Locker
	TxRunner   persistence.TxRunner
}

// Resolve maps topo to the saga-projection backend set. clk is the mandatory
// positional clock. See the package doc for the selection matrix and the two
// fail-closed invariants (postgres-needs-pool, multi-pod-needs-Redis).
func Resolve(ctx context.Context, clk clock.Clock, topo bootstrap.Topology, cfg Config) (Deps, error) {
	clock.MustHaveClock(clk, "sagaprojectiondeps.Resolve")

	j, ownerStore, txRunner, err := resolveStore(clk, topo, cfg)
	if err != nil {
		return Deps{}, err
	}
	reader, ok := j.(journal.GlobalReader)
	if !ok {
		// Defensive: every journal backend the store resolver builds (Mem / PG)
		// implements GlobalReader. A backend that does not cannot feed a
		// projection tailer, so fail-closed rather than wire a nil reader.
		return Deps{}, errcode.New(errcode.KindInternal, errcode.ErrValidationFailed,
			"sagaprojectiondeps: resolved journal does not implement journal.GlobalReader")
	}

	locker, err := resolveLocker(ctx, clk, topo, cfg)
	if err != nil {
		return Deps{}, err
	}

	return Deps{
		Journal:    j,
		Reader:     reader,
		OwnerStore: ownerStore,
		TxRunner:   txRunner,
		Locker:     locker,
	}, nil
}

// resolveStore selects the journal + owner checkpoint store + tx runner by
// storage backend. postgres with a nil pool is fail-closed.
func resolveStore(
	clk clock.Clock, topo bootstrap.Topology, cfg Config,
) (journal.Journal, projection.OwnerCheckpointStore, persistence.TxRunner, error) {
	if topo.StorageBackend() == bootstrap.StorageBackendPostgres {
		if cfg.Pool == nil {
			return nil, nil, nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"sagaprojectiondeps: postgres topology requires a PG pool (Config.Pool); "+
					"refusing to silently degrade to an in-memory journal that loses events across restart")
		}
		pgxPool := cfg.Pool.DB()
		j, err := adapterpgsaga.NewJournal(pgxPool, clk)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("sagaprojectiondeps: build PG saga journal: %w", err)
		}
		ownerStore, err := adapterpg.NewProjectionCheckpointStore(pgxPool)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("sagaprojectiondeps: build PG projection checkpoint store: %w", err)
		}
		return j, ownerStore, adapterpg.NewTxManager(cfg.Pool), nil
	}

	// demo / memory.
	j, err := journal.NewMemJournal(clk)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("sagaprojectiondeps: build in-memory saga journal: %w", err)
	}
	return j, projection.NewMemOwnerCheckpointStore(), outbox.DemoTxRunner{}, nil
}

// resolveLocker selects the per-projection leader locker. Real multi-pod
// topology requires a Redis-backed locker; a missing Redis client is fail-closed.
// Single-pod / demo uses the in-process driver — the SOLE sanctioned construction
// site of distlock.NewInProcessDriver (SAGA-PROJECTION-DEPS-INMEM-FUNNEL-01).
func resolveLocker(
	_ context.Context, clk clock.Clock, topo bootstrap.Topology, cfg Config,
) (distlock.Locker, error) {
	if topo.RequiresDistributedReplay() {
		if cfg.RedisClient == nil {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"sagaprojectiondeps: real multi-pod topology requires a Redis-backed projection "+
					"leader locker (Config.RedisClient); an in-process locker cannot coordinate "+
					"leadership across replicas — refusing to silently fall back")
		}
		driver, err := adapterredis.NewRedisDriver(cfg.RedisClient, sagaProjectionLockNamespace)
		if err != nil {
			return nil, fmt.Errorf("sagaprojectiondeps: build Redis distlock driver: %w", err)
		}
		locker, err := distlock.New(driver, clk)
		if err != nil {
			return nil, fmt.Errorf("sagaprojectiondeps: build Redis locker: %w", err)
		}
		return locker, nil
	}

	locker, err := distlock.New(distlock.NewInProcessDriver(clk), clk)
	if err != nil {
		return nil, fmt.Errorf("sagaprojectiondeps: build in-process locker: %w", err)
	}
	return locker, nil
}

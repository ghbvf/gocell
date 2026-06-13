// Package sagaprojectiondeps is the topology-gated single source for the
// saga-journal projection runtime dependencies a composition root wires into
// bootstrap: the saga journal (shared with the saga Coordinator and re-exposed as
// the projection's journal.GlobalReader), the projection OwnerCheckpointStore,
// the per-projection leader distlock.Locker, and the projection TxRunner.
//
// It is the saga-projection sibling of cellmodules/eventtransport.Resolve (event
// transport) and cellmodules/replaydeps.Resolve (consumer claimer + service-token
// nonce): one Resolve maps a bootstrap.Topology to the correct backends, so a
// composition root never hand-picks mem vs PG vs Redis per primitive.
//
// # Backend selection
//
//	demo / memory topology:
//	  Journal      = journal.MemJournal (also the GlobalReader)
//	  OwnerStore   = projection.MemOwnerCheckpointStore
//	  Locker       = distlock over an in-process Driver (single pod, no contention)
//	  TxRunner     = outbox.DemoTxRunner (pass-through)
//
//	postgres topology:
//	  Journal      = adapters/postgres/saga.PGJournal (durable; also GlobalReader)
//	  OwnerStore   = adapters/postgres.ProjectionCheckpointStore (fenced CAS)
//	  TxRunner     = adapters/postgres.TxManager
//	  Locker       = in-process (single pod) OR Redis-backed (multi pod) — see below
//
// The PG pool and (multi-pod) Redis client are INJECTED via Config, not opened
// here: a composition root already owns the pool (for migrations and its own
// repos), so opening a second one would be wasteful and wrong. The resolver is
// therefore pure wiring + gating; the leaf constructors it selects are each
// conformance-tested in their own packages.
//
// # Fail-closed invariants
//
//   - postgres storage with a nil Config.Pool is a startup error — never a silent
//     degrade to a MemJournal, which would lose events across restart (the exact
//     durability the postgres topology promises).
//   - real multi-pod topology (topo.RequiresDistributedReplay()) with a nil
//     Config.RedisClient is a startup error — an in-process locker cannot
//     coordinate leadership across replicas, so two pods would each believe they
//     hold the projection lock and double-apply. Never a silent in-process
//     fallback. This mirrors replaydeps.Resolve's claimer/nonce fail-closed gate.
//
// # SAGA-PROJECTION-DEPS-INMEM-FUNNEL-01 (Medium)
//
// INVARIANT: SAGA-PROJECTION-DEPS-INMEM-FUNNEL-01 — the in-process single-pod
// distlock primitive distlock.NewInProcessDriver is constructed ONLY inside this
// package's demo branch. A composition root (cmd/*, cellmodules/*, examples/*)
// that constructs it directly bypasses the topology gate and re-opens the
// silent-single-pod-in-multi-pod footgun. The bypass is banned by an AST callsite
// scan in tools/archtest (the call-granularity sibling of REPLAYDEPS-INMEM-FUNNEL-01;
// a depguard import-ban cannot express it because runtime/distlock is legitimately
// imported for its Locker type). See .claude/rules/gocell/eventbus.md §"复用层选型"
// — this is the 4th sealed single-pod primitive alongside bus / claimer / nonce.
package sagaprojectiondeps

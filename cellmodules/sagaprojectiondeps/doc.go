// Package sagaprojectiondeps is the topology-gated single source for the
// saga-journal projection runtime dependencies a composition root wires into
// bootstrap: the saga journal (shared with the saga Coordinator and re-exposed as
// the projection's journal.GlobalReader), the projection OwnerCheckpointStore,
// the projection DeadLetterStore (poison-event sink, #2110), the per-projection
// leader distlock.Locker, and the projection TxRunner.
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
//	  DeadLetters  = projection.MemDeadLetterStore
//	  Locker       = distlock over an in-process Driver (single pod, no contention)
//	  TxRunner     = outbox.DemoTxRunner (pass-through)
//
//	postgres topology:
//	  Journal      = adapters/postgres/saga.PGJournal (durable; also GlobalReader)
//	  OwnerStore   = adapters/postgres.ProjectionCheckpointStore (fenced CAS)
//	  DeadLetters  = adapters/postgres.SagaProjectionDeadLetterStore (ambient-tx)
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
//
// # Blind spots (upstream/downstream strength split)
//
// Per ai-robust.md "Funnel 类约束必须分别说明上游和下游强度":
//
// Downstream (AST scan, what is actually enforced): the archtest scans only for
// direct calls to distlock.NewInProcessDriver in wiring-layer packages
// (cmd/*, cellmodules/*, examples/*). This is the genuinely new single-pod
// primitive introduced by sagaprojectiondeps and is the correct enforcement target.
//
// Upstream (empirical absence, not scanned): journal.NewMemJournal and
// projection.NewMemOwnerCheckpointStore are also single-pod primitives that this
// package's demo branch constructs, but they are NOT covered by the AST scan.
// The reason: both are test-ubiquitous across the entire repo — banning their
// direct construction globally is infeasible and would break hundreds of unit
// tests. The resolver is their sole sanctioned WIRING site in production roots;
// their misuse in a composition root is the pre-existing general "don't bypass
// the resolver" concern, not a new risk introduced here. The downstream AST scan
// (NewInProcessDriver) covers the genuinely novel, production-only primitive; the
// upstream absence of a scan for MemJournal/MemOwnerCheckpointStore is a known
// accepted blind spot documented here rather than silently tolerated.
package sagaprojectiondeps

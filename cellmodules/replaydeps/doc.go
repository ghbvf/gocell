// Package replaydeps resolves a composition root's topology-gated, Redis-backed
// distributed-replay primitives — the outbox consumer idempotency claimer and
// the /internal/v1/* service-token nonce store — plus the Redis client whose
// lifecycle the composition root must manage.
//
// It is the symmetric sibling of cellmodules/eventtransport (which resolves the
// outbox Publisher/Subscriber): both turn a bootstrap.Topology into the concrete
// infra a composition root wires, and both fail closed rather than silently
// degrade when a real multi-pod topology lacks its backend.
//
// Two composition roots consume it — cmd/corebundle (the production binary) and
// examples/ssobff (the demo, which has its own composition root and historically
// missed this gating: #825 + #2017). Promoting the previously cmd/corebundle-
// local glue here lets both share one code + test source (no parallel rule body,
// no drift).
//
// # Fail-closed invariant
//
// In real multi-pod topology (topo.RequiresDistributedReplay()) a missing Redis
// configuration is a startup error from Resolve, never a silent degrade to an
// in-memory claimer / nonce store — an in-process primitive cannot coordinate
// at-most-once / replay defense across replicas. This is the same rule
// cmd/corebundle's composition.SharedDeps.ConsumerClaimer.Kind() check enforces;
// it lives here so composition roots that bypass SharedDeps (examples/ssobff)
// are equally protected. demo / single-pod topology returns in-memory primitives
// (behavior unchanged for the demo path).
//
// # INVARIANT: REPLAYDEPS-INMEM-FUNNEL-01
//
// The in-memory distributed-replay constructors idempotency.NewInMemClaimer and
// auth.NewInMemoryNonceStore are reachable in the hardened composition roots
// (cmd/corebundle, examples/ssobff) ONLY through replaydeps.Resolve's
// demo/single-pod branch. A composition root that calls either constructor
// directly re-opens the #2017-class gap (a durable-storage deployment that
// silently runs an in-memory replay primitive in multi-pod). golangci depguard
// cannot express this — both packages are legitimately imported for their types
// (idempotency.Claimer, kauth.NonceStore) — so the funnel is enforced by the
// archtest REPLAYDEPS-INMEM-FUNNEL-01 (tools/archtest), a path-scoped AST scan
// over the two hardened roots with a synthetic RED fixture and dot-import
// blind-spot guard.
//
// AI-robust grade: Medium. Upstream (the construction-bypass ban) is a CI-time
// AST scan; downstream (the fail-close decision) is a runtime guard in Resolve.
// A Hard form (sealing the in-mem constructors behind this resolver) is high
// cost — those constructors have legitimate callers across other examples and
// runtime packages that are out of this PR's scope — so it is deliberately not
// pursued (no low-cost Hard path to register per .claude/rules/gocell/ai-robust.md).
//
// The sibling bus funnel (in-memory eventbus reachable only via
// eventtransport.Resolve) is a golangci depguard (COREBUNDLE-EVENTBUS-FUNNEL-01),
// because runtime/eventbus has no other symbol the roots need — a whole-package
// import ban suffices there. The three funnels (bus / claimer / nonce) together
// make every in-memory single-pod primitive in a hardened composition root
// reachable only through a sealed resolver.
//
// Authoritative semantics: this doc + tools/archtest REPLAYDEPS-INMEM-FUNNEL-01;
// threat model amended into ADR 202606131500-1940 (the #1940 transport funnel).
package replaydeps

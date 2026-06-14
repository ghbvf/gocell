// Package percellpg is the topology-gated single source for the per-cell
// postgres pool that a colocated composition root provisions. It is the
// postgres-pool sibling of cellmodules/eventtransport.Resolve (event transport),
// cellmodules/replaydeps.Resolve (consumer claimer + service-token nonce), and
// cellmodules/sagaprojectiondeps.Resolve (saga-journal projection dependencies):
// one Resolve maps a bootstrap.Topology to the correct PG backend, so a
// composition root never hard-codes a pool constructor.
//
// # Backend selection
//
//	memory topology (topo.StorageBackend() != postgres):
//	  No pool is opened. Resolve returns empty Deps (nil Provider, no Resources).
//	  Cell modules fall through to their in-memory storage path.
//
//	postgres topology:
//	  A single deduped pool is opened, verified, and wrapped into a sealed
//	  capability.PGProvider. The pool is returned as a lifecycle.ManagedResource
//	  so the composition root registers it first (LIFO: closed last, after every
//	  PG consumer — relay, cell workers, tx).
//
// # Dedup-by-DSN invariant
//
// Colocated assemblies share one physical database: all postgres cells must be
// configured with the SAME DSN (GOCELL_<CELLID>_DATABASE_URL). Resolve deduplicates
// by strings.TrimSpace(DSN) and opens exactly ONE pool, preserving today's
// shared-pool behavior while introducing per-cell DSN injection.
//
// # Fail-closed invariants
//
//   - A postgres-requiring cell whose DSN is empty (or whitespace-only) is a
//     startup error. The message names the cell ID and the expected env var
//     (GOCELL_<CELLID>_DATABASE_URL). Never a silent fallback — sharing another
//     cell's pool without operator intent would bypass per-cell credential isolation.
//
//   - Per-cell distinct DSNs (>1 distinct after dedup) are a startup error pointing
//     to the split-topology backlog (US4 #1963, per-cell outbox relay fan-out).
//     Colocated assemblies must set all per-cell DATABASE_URLs to the same value.
//
// # CAPABILITY-PROVIDER-FUNNEL-01 (pool construction site)
//
// Primary pool construction lives here in cellmodules/percellpg, not in
// cmd/corebundle/cap_wiring.go. Cellmodules is the Composition Root layer
// (trusted; may import all layers) and is the same class as the auditcore admin
// pool and sagaprojectiondeps' NewTxManager. The cmd-only CAPABILITY-PROVIDER-FUNNEL-01
// scan (archtest) bans direct adapter constructor calls in cmd/ files outside the
// sanctioned cap_wiring.go site; percellpg is intentionally outside that scan's
// cmd/-only scope. The upstream Hard guard is the sealed capability.PGProvider
// (unexported marker isPGProvider + sole NewPGProvider constructor in
// runtime/capability), which makes the provider unforgeable outside package capability.
// The downstream archtest scan + cmd-only ban serve as defense-in-depth for any
// residual cmd/-level direct construction.
//
// # Blind spots
//
// Per ai-robust.md "Funnel 类约束必须分别说明上游和下游强度":
//
// Downstream (archtest, cmd/-only scan): the CAPABILITY-PROVIDER-FUNNEL-01 scan
// covers cmd/... and flags direct adapterpg.NewPool / NewTxManager / NewOutboxWriter /
// NewJournalingOutboxWriter calls outside cap_wiring.go. percellpg is in cellmodules/,
// which is intentionally outside the cmd/-only scan — same class as sagaprojectiondeps.
//
// Upstream (sealed type, Hard): capability.PGProvider is unforgeable outside
// package capability. A cellmodules package that calls adapterpg.NewPool directly
// (outside percellpg) bypasses the dedup gate but not the sealed type: the composed
// bootstrap still requires a capability.PGProvider, so a second raw pool would have
// no injection path. The per-cell dedup invariant is therefore the primary new
// behavioral gate; pool-construction multiplicity is a known accepted blind spot
// (same posture as sagaprojectiondeps § "upstream empirical absence").
package percellpg

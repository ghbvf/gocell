// Package percellpg is the topology-gated single source for the per-cell postgres
// DSN decision a colocated composition root provisions its pool from. It is the
// per-cell-DSN sibling of cellmodules/eventtransport.Resolve, replaydeps.Resolve,
// and sagaprojectiondeps.Resolve — but it is a PURE decision function: it performs
// NO I/O and constructs NO adapter primitives.
//
// # Why pure (construction stays in cmd/corebundle/cap_wiring.go)
//
// The banned shared-infra constructors (adapterpg.NewPool / NewTxManager /
// NewJournalingOutboxWriter) must stay in the single sanctioned cmd/ provisioning
// site so the cmd/-scoped machine guards keep covering them:
//   - CAPABILITY-PROVIDER-FUNNEL-01 (bans those constructors outside cap_wiring.go), and
//   - PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-DERIVED-01 (requires NewJournalingOutboxWriter
//     to take the direct generatedProjectionSourceTopics() accessor, not a threaded variable).
//
// Moving construction into cellmodules/percellpg would silently take it out of both
// guards' cmd/-only scan — so this package only DECIDES the agreed DSN config;
// cap_wiring.go opens the pool, verifies schema, builds the journaling writer with
// the direct accessor, and wraps the capability.PGProvider.
//
// # Backend selection
//
//	memory topology (topo.StorageBackend() != postgres):
//	  Resolve returns (zero, ok=false, nil) — no pool. Cell modules take their
//	  in-memory storage path.
//
//	postgres topology:
//	  Resolve returns (agreedConfig, ok=true, nil) — the single deduped DSN config
//	  cap_wiring.go opens the assembly's one shared pool from.
//
// # Dedup-by-DSN invariant
//
// Colocated assemblies share one physical database: all postgres cells must be
// configured with the SAME DSN (GOCELL_<CELLID>_DATABASE_URL). Resolve deduplicates
// by strings.TrimSpace(DSN) and yields exactly one agreed config, preserving today's
// shared-pool behavior while making per-cell DSN injection explicit.
//
// # Pool knobs in colocated mode
//
// The agreed config is the alphabetically-first cell's Config (cellIDs[0] after
// sort.Strings), so its pool knobs (MaxConns / IdleTimeout / MaxLifetime) configure
// the shared pool. With the current platform cells that is always accesscore
// (a < au < c). Operators MUST set the pool knobs identically across all postgres
// cells' DATABASE_* vars — only the first cell's knobs are applied in colocated mode.
// Per-cell pool-knob isolation is split-topology territory (#2152).
//
// # Fail-closed invariants (the three gates)
//
//   - Empty cell set in postgres topology — a misconfiguration, fail-closed (also
//     guards the cellIDs[0] index from panicking on an empty map).
//   - A postgres-requiring cell whose DSN is empty (or whitespace-only) — fail-closed;
//     the diagnostic carries the cell ID and expected env var. Never a silent fallback.
//   - Per-cell distinct DSNs (>1 distinct after dedup) — fail-closed, pointing to the
//     split-topology backlog (US4 #1963 / #2152, per-cell outbox relay fan-out).
//
// Being pure, all three gates plus the success/memory branches are exhaustively
// unit-tested (no live database needed). The pool-open + schema-verify I/O that
// cap_wiring.go runs from the agreed config is covered by the real-PG integration
// test cmd/corebundle/corebundle_pg_env_integration_test.go.
package percellpg

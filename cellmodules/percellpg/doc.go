// Package percellpg is the topology-gated single source for the per-cell postgres
// DSN decision a composition root provisions its pools from. It is the per-cell-DSN
// sibling of cellmodules/eventtransport.Resolve, replaydeps.Resolve, and
// sagaprojectiondeps.Resolve — but it is a PURE decision function: it performs NO
// I/O and constructs NO adapter primitives.
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
// guards' cmd/-only scan — so this package only DECIDES the per-cell DSN grouping;
// cap_wiring.go opens the pools, verifies schema for each, builds the journaling
// writers with the direct accessor, and wraps the capability.PGProvider instances.
//
// # Backend selection
//
//	memory topology (topo.StorageBackend() != postgres):
//	  Resolve returns (zero, ok=false, nil) — no pools. Cell modules take their
//	  in-memory storage path.
//
//	postgres topology:
//	  Resolve returns (Resolution{Instances, CellToInstance}, ok=true, nil).
//	  cap_wiring.go iterates Resolution.Instances to open one pool per instance
//	  and builds the per-cell PGProvider map (capability.PGSet) from CellToInstance.
//
// # Dedup-by-DSN invariant and Resolution shape (#2341)
//
// Each postgres cell provides GOCELL_<CELLID>_DATABASE_URL. Resolve deduplicates
// by strings.TrimSpace(DSN) and groups cells by their distinct DSN:
//
//   - Colocated (all cells share the same DSN, 1 distinct after dedup):
//     Resolution.Instances contains one entry keyed DefaultInstanceKey(). This is the
//     behavior-preserving colocated path.
//   - Split (>1 distinct DSNs after dedup):
//     Resolution.Instances contains one entry per distinct DSN, each keyed
//     NewInfraInstanceKey(rep) where rep is the alphabetically-first cell ID in that
//     DSN group. CellToInstance maps every cell to its instance key.
//     cap_wiring.go opens one pool + one relay per instance (#2341).
//
// # Pool knobs
//
// All cells within a DSN group must declare identical pool knobs
// (MaxConns / IdleTimeout / MaxLifetime / ConnectTimeout). Sharing a DSN means
// sharing a pool — any mismatch is fail-closed at startup with an errcode error
// that carries the conflicting cell IDs and the first differing knob name in
// its WithInternal context (server-side only, never on wire). This replaces the
// former Soft convention ("operators MUST set identically; first cell wins"),
// which silently discarded non-rep cell knobs. In split mode each group uses its
// own rep cell's knobs; per-cell pool-knob isolation across groups is naturally
// achieved by distinct DSNs.
//
// # Fail-closed invariants (the two surviving gates)
//
//   - Empty cell set in postgres topology — a misconfiguration, fail-closed (also
//     guards the cellIDs[0] index from panicking on an empty map).
//   - A postgres-requiring cell whose DSN is empty (or whitespace-only) — fail-closed;
//     the diagnostic carries the cell ID and expected env var. Never a silent fallback.
//
// The former ">1 distinct DSN → fail-closed" gate has been lifted by #2341: N
// distinct DSNs now fan out to N keyed instances rather than rejecting at startup.
//
// Being pure, both gates plus the success/memory branches are exhaustively
// unit-tested (no live database needed). The pool-open + schema-verify I/O that
// cap_wiring.go runs from each instance config is covered by the real-PG integration
// test cmd/corebundle/corebundle_pg_env_integration_test.go.
package percellpg

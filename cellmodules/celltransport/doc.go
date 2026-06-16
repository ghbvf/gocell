// Package celltransport is the topology-gated single source for the
// cross-cell sync (http) CellTransport a composition root wires into a cell
// that consumes a remote or co-located sibling cell via contract.
//
// It is the CellTransport sibling of:
//   - cellmodules/eventtransport  — outbox Publisher/Subscriber
//   - cellmodules/replaydeps      — consumer claimer + service-token nonce
//   - cellmodules/sagaprojectiondeps — saga-journal projection deps
//
// # Transport selection
//
// co-located (topo.IsColocated(cellID)):
//
//	returns the shared *transport.InProcessTransport (already wired by the
//	composition root via composition.Builder.Build).
//
// remote (topo.RemoteEndpoint(cellID) ok):
//
//	builds a *transport.RemoteHTTPTransport targeting the declared endpoint
//	using a transport.StaticResolver over the cellID→endpoint mapping
//	extracted from topo at wiring time.
//
// # Fail-closed invariants
//
//   - An un-classified cellID (neither colocated nor remote in an explicit
//     topology) causes Resolve to return an error. gocell validate TOPO-11
//     statically prevents this at build time; the runtime check is
//     defense-in-depth.
//   - A co-located cellID with a nil inProc transport is a startup error —
//     the composition root must always supply a non-nil InProcessTransport
//     (composition.Builder.Build mints it).
//
// # CELLTRANSPORT-SELECT-FUNNEL-01 (Medium)
//
// INVARIANT: CELLTRANSPORT-SELECT-FUNNEL-01 — transport.NewRemoteHTTP is
// constructed ONLY inside this package's remote branch. A wiring-layer package
// (cmd/*, cellmodules/*, examples/*) that calls transport.NewRemoteHTTP
// directly bypasses the topology gate: it hard-codes remote for a cell that
// might be co-located in another assembly, and it cannot enforce the fail-
// closed invariant for an un-classified cell. The bypass is banned by an AST
// callsite scan in tools/archtest (call-granularity, scoped to the wiring
// layer, allowlist = this package). See .claude/rules/gocell/eventbus.md
// §"事件传输选型" for the topology-gated sibling pattern.
//
// AI-robust grade: Medium (AST callsite scan). Hard path (sealing
// NewRemoteHTTP behind a type funnel) is deferred as low-priority since
// adapters/ legitimately needs http.Client, making a depguard import-ban
// infeasible for the token alone; the AST scan is the reachable ceiling
// (tracked as a future enhancement).
package celltransport

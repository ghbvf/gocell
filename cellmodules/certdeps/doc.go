// Package certdeps is the topology-gated single source for the cert-signing
// dependencies a composition root wires into a device-identity cell: the issuing
// certsigning.Signer and the matching certsigning.RevocationStore.
//
// It is the cert sibling of cellmodules/eventtransport.Resolve (event transport),
// cellmodules/replaydeps.Resolve (consumer claimer + nonce), and
// cellmodules/sagaprojectiondeps.Resolve (saga-projection deps): one Resolve maps
// a bootstrap.Topology to the correct backend, so a composition root never
// hand-picks a CA implementation per deployment.
//
// # Backend selection
//
//	demo / memory topology:
//	  Signer + RevocationStore = adapters/softca, an ephemeral two-tier dev CA
//	  (softca.NewDevCA) over an in-memory issuance ledger (softca.NewMemLedger).
//	  Both halves share one ledger by construction (softca.NewSoftCA).
//
//	postgres topology:
//	  FAIL-CLOSED. No durable signing CA or issuance-ledger backend exists yet, so
//	  Resolve returns a startup error rather than serve the dev soft-CA. This is the
//	  expected partial-GA state, not a misconfiguration: until the durable backend
//	  lands (Epic #2299), a deployment that needs cert issuance must run the demo /
//	  memory topology (GOCELL_CELL_ADAPTER_MODE unset or "memory").
//
// The resolver takes neither a Config nor a context: the demo branch performs no
// I/O and owns no caller-provided handle, and the postgres branch is fail-closed.
// When a durable CA backend lands, a Config carrying its handles is added then —
// not pre-built for a backend that does not exist.
//
// It deliberately resolves only the CA side. certsigning.Authorizer (the
// enrollment authorization policy) has no framework default and is a
// consumer-cell decision, so the consuming cell provides it — mirroring how
// sagaprojectiondeps leaves the projection logic to its consumer.
//
// # Fail-closed invariant
//
//   - postgres storage is a startup error — never a silent degrade to the dev
//     soft-CA, whose trust anchor rotates on every restart and whose in-memory
//     ledger loses issuance and revocation state across restart (the exact
//     durability the postgres topology promises). This mirrors
//     sagaprojectiondeps.Resolve's postgres-needs-pool fail-closed gate. A durable
//     CA backend is registered follow-up work (Epic #2299); until it lands the
//     postgres branch stays fail-closed.
//
// # CERTDEPS-INMEM-FUNNEL-01 (Medium)
//
// INVARIANT: CERTDEPS-INMEM-FUNNEL-01 — the softca construction primitives
// (NewDevCA / NewFileCA / NewSoftCA / NewSigner / NewRevocationStore /
// NewMemLedger) are constructed in the wiring layer (cmd/*, cellmodules/*,
// examples/*) ONLY inside this package's demo branch. A composition root that
// constructs any of them directly bypasses the topology gate and re-opens the
// footgun of a postgres deployment silently served by the rotating-anchor dev
// soft-CA. The bypass is banned by an AST callsite scan in tools/archtest (the
// cert sibling of REPLAYDEPS-INMEM-FUNNEL-01 and
// SAGA-PROJECTION-DEPS-INMEM-FUNNEL-01; a depguard import-ban cannot express it
// because adapters/softca is legitimately imported here and by leaf tests for the
// certsigning types). See .claude/rules/gocell/eventbus.md §"复用层选型" for the
// sibling funnel family.
//
// # AI-robust grade (upstream/downstream strength split)
//
// Per ai-robust.md "Funnel 类约束必须分别说明上游和下游强度":
//
// Medium. Upstream (the construction-bypass ban) is a CI-time AST scan over the
// wiring roots. Downstream (the fail-close decision) is a runtime guard in Resolve
// (postgres → fail-fast), because bootstrap.Topology is a runtime value the type
// system cannot express — a runtime guard is the correct carrier per ai-robust.md
// "runtime guard 只用于 type system 不可表达的边界, 错误必须 fail-fast".
//
// No low-cost Hard path: sealing softca's constructors so the bypass is
// unexpressible would mean unexporting them and routing them only through
// certdeps, but adapters/softca is a standalone, independently-reusable adapter
// with its own test surface and Go has no friend-package mechanism — high cost and
// a layering violation. Same conclusion as REPLAYDEPS-INMEM-FUNNEL-01;
// deliberately not pursued, no Hard-ization issue registered.
//
// # Blind spots
//
//   - corecells/ and external cell modules (the epic's externalcells/mdm has its
//     own module) are NOT under the scanned roots — vacuous-green for an external
//     consumer, mirroring REPLAYDEPS-INMEM-FUNNEL-01. External-module wiring
//     governance is the #1090 / M9 track.
//   - dot-import of adapters/softca would evade the SelectorExpr scan; closed by
//     the reverse self-test TestCERTDEPS_INMEM_FUNNEL_01_NoDotImportBlindSpot.
package certdeps

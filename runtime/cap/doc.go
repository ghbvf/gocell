// Package cap defines GoCell capability providers: assembly-level shared
// infrastructure resources (postgres pool, redis client, ...) provisioned once
// by the composition root and injected into every consuming cell module.
//
// # Why assembly-level (not per-cell)
//
// A capability is a fx.Supply-shaped shared value, not a per-cell constructor.
// The composition root provisions one postgres pool / one redis client and
// hands the same handle to every module that needs it. Per-cell construction
// would open one pool per cell, breaking the single-pool + LIFO-shutdown
// invariant and contradicting the one-outbox-table / one-relay model. See ADR
// docs/architecture/202605251500-adr-capability-provider-interface.md.
//
// # Layering: runtime/cap never imports adapters/
//
// runtime/ may depend only on kernel/ + pkg/ (CLAUDE.md). So the provider
// interfaces are expressed over kernel types (persistence.TxRunner,
// outbox.Writer) plus a deliberate `any` typed-erasure seam (DB() / Client())
// for the raw adapter handle. The `any` is type-asserted exactly once, in the
// consumer site inside cmd/* (which may import adapters/).
//
// # Sealed construction (AI-robust Hard)
//
// PGProvider / RedisProvider carry an unexported marker method, so they can
// only be implemented inside this package. The concrete impls (pgProvider /
// redisProvider) are private; NewPGProvider / NewRedisProvider are the sole
// construction paths. A cell module cannot fabricate a bypass provider — it
// must consume the injected one. Combined with the downstream archtest
// CAPABILITY-PROVIDER-FUNNEL-01 (bans direct adapterpg.NewTxManager /
// adapterredis.NewCache in cmd/<id>/*_module.go outside the assembly wiring
// site), this is a closed funnel: upstream sealed + downstream caller-allowlist.
//
// # Blind spots (BS) for CAPABILITY-PROVIDER-FUNNEL-01
//
//   - BS-1 Name shadowing: a non-adapter package exporting a function literally
//     named NewTxManager does NOT match — the archtest resolves the callee's
//     owning package path via go/types, not the source Ident.
//   - BS-2 Function-value indirection (`var fn = adapterpg.NewTxManager; fn()`)
//     resolves the second callee to a *types.Var, ok=false. Accepted: the repo
//     has no such pattern (same accepted BS as CAS-PROTOCOL-COMPOSITION-ROOT-01).
//   - BS-3 Reflection construction: out of scope per ai-robust.md §3.
package cap

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
// # Sealed construction (AI-robust: upstream Hard)
//
// PGProvider / RedisProvider carry an unexported marker method, so they can
// only be implemented inside this package. The concrete impls (pgProvider /
// redisProvider) are private; NewPGProvider / NewRedisProvider are the sole
// construction paths. A cell module cannot fabricate a bypass provider — it
// must consume the injected one. This is the upstream-Hard half of the funnel:
// Go's type system makes a forged provider unrepresentable outside this package.
//
// # Funnel closure (downstream Medium)
//
// The downstream half is archtest CAPABILITY-PROVIDER-FUNNEL-01
// (tools/archtest/capability_provider_funnel_test.go): a type-aware caller
// allowlist that bans the shared-infrastructure constructors
// (adapters/postgres.NewPool / NewTxManager / NewOutboxWriter,
// adapters/redis.NewClient) anywhere except the single provisioning site
// cmd/corebundle/cap_wiring.go. Per-cell / per-role derivations that take an
// already-acquired handle (NewSessionStore / NewLedgerStore / NewOutboxStore /
// NewCache / NewRedisDriver / …) are intentionally NOT banned.
//
// Rating: upstream Hard (sealed) + downstream Medium (archtest caller-allowlist,
// not compile-time) — an allowed transition form per ai-robust.md §"Funnel
// 双向锁评级". The downstream→Hard upgrade is tracked at gh issue #988. The
// blind-spot inventory lives in that archtest's package godoc, not duplicated
// here.
package cap

# ADR: kernel/cell package decompose (G-10)

Status: Accepted (2026-05-24)
Issue: #615
PR: #900

## Context

`kernel/cell/` historically aggregated four orthogonal concerns into a single
package:

1. **Cell registration surface** — the `Registry` interface a cell uses inside
   `Init(ctx, reg)` to declare routes, subscriptions, health probes, lifecycle
   hooks, and config-reload callbacks; the `RegistryRecorder` accumulator that
   implements it; `RegistrySnapshot` etc.
2. **Auth plan declarations** — the sealed `AuthPlan` interface, its five typed
   implementations (`AuthNone`, `AuthJWT`, `AuthJWTFromAssembly`, `AuthMTLS`,
   `AuthServiceToken`), plus the narrow dependency interfaces
   (`IntentTokenVerifier`, `NonceStore`, `HMACKeyring`, `AuthProvider`,
   `Claims`, `TokenIntent`, ...) that `runtime/auth` concrete types satisfy
   structurally.
3. **Outbox emitter mode resolution** — `DurabilityMode` enum, the `Nooper`
   marker, `CheckNotNoop`, `ResolveEmitter` / `ResolveCellEmitter`, the
   `EmitterConfig` / `EmitterOutcome` data plumbing, plus `DemoTxRunner` /
   `DemoCellTxManager`.
4. **A one-line health alias** (`var ErrDegraded = outbox.ErrDegraded`).

The ISP split of the `Cell` interface (`CellIdentity` / `CellLifecycle` /
`CellStatus` / `CellInventory`) landed earlier under
`202605101800-adr-cell-interface-isp-split.md`. This ADR decomposes the
remaining four concerns into focused packages.

## Decision

### 1. New package `kernel/auth/` owns the auth model

Move `auth_plan.go` + `auth_types.go` + tests + the `celltest/auth_plan.go`
helpers to `kernel/auth/` and `kernel/auth/authtest/`. The new package's only
kernel-internal dependency is `kernel/cell` for the `cell.Cell` type referenced
by `AssemblyRef.Cell(id)` — a one-way edge with no cycle.

`runtime/auth` keeps its existing type aliases (`Claims = kauth.Claims`,
`TokenIntent = kauth.TokenIntent`, `IntentTokenVerifier = kauth.IntentTokenVerifier`)
but their RHS now resolves through `kauth` (the package alias used by callers
that also import `runtime/auth` under its bare `auth` name). The aliases let
production code keep writing `auth.Claims`, with the canonical declaration
living in `kernel/auth`.

### 2. `kernel/outbox/` absorbs the emitter mode resolver

Move `mode_resolver.go` + `durability.go` + `demo_tx_runner.go` (and their
tests) to `kernel/outbox/`. The `Registrar.DurabilityMode()` method now returns
`outbox.DurabilityMode` (the only remaining `cell → outbox` type-reference
edge); `outbox` does not import `cell`. Cycle resolution depends on this move
plus deletion of the `cell.ErrDegraded` alias (next item).

### 3. Delete `kernel/cell/health.go` and `kernel/cell/repo_readiness.go`

`health.go` was a single-line `var ErrDegraded = outbox.ErrDegraded` alias.
Callers now reach `outbox.ErrDegraded` directly. The `kernel/healthz/` package
introduced by PR #886 (issue #704) is unaffected — it is a separate interface
package for probe aggregation.

`repo_readiness.go` was already emptied by PR #886 (its
`RegisterRepoReadiness` function replaced by cellgen-generated typed helpers
`RegisterRepoReady` in `cells/*/healthz_gen.go`). The placeholder file is
deleted; its companion `repo_readiness_test.go` (which contained no live
tests) is deleted too. `kernel/cell/health_status.go` (the `HealthStatus`
struct) stays in `kernel/cell/`.

### 4. Rename `cell.Registry` → `cell.Registrar`

The `Registry` interface declared all-verb methods (`Subscribe`, `Healthz`,
`Lifecycle`, `RouteGroup`, `OnConfigReload`, ...) — the structural signature
of an agent that accepts registrations, not a noun-style storage/lookup
service. Renaming aligns with Kratos's `registry/registry.go`, where
`Registrar` (with `Register` / `Deregister`) is the verb interface and
`Discovery` is the lookup-side noun. The concrete data structures
(`RegistryRecorder`, `RegistrySnapshot`) keep their noun-style names. The
codegen templates (`cellgen/cell.tmpl`, `cellgen/healthz_gen.tmpl`,
`cellgen/scaffold-cell.tmpl`, `contractgen/subscription.tmpl`) emit the new
name; their generated outputs (`cells/*/cell_gen.go`,
`cells/*/healthz_gen.go`, `generated/contracts/event/*/subscription_gen.go`)
were regenerated as part of this change.

## Open-source benchmark

| Decision | Framework | Source |
|----------|-----------|--------|
| `Registrar` (verb) vs `Registry` (noun) | go-kratos | `registry/registry.go` declares `Registrar` (`Register` / `Deregister`) and `Discovery` (`GetService`) as separate interfaces |
| auth lives in its own sub-package, not in the DI/registration core | Kratos / K8s apiserver / Uber fx | Kratos `middleware/auth/`; K8s `pkg/authentication/`; fx omits auth from DI entirely |
| Mode (durable vs in-memory) decision lives in the publisher/emitter layer, not the cell-registration layer | Watermill | `pubsub/gochannel/Config.Persistent` selects in-memory vs durable backing inside the pub/sub implementation |

## Why `AssemblyRef.Cell(id) cell.Cell` and not `any`

`AssemblyRef.Cell(id)`'s sole production caller
(`runtime/bootstrap/auth_plan_apply.go::resolveAuthProviderVerifier`) does
`asm.Cell(id).(kauth.AuthProvider)` — an immediate type assertion — so any
return type that supports `.(T)` would compile, and `any` would eliminate the
`kernel/auth → kernel/cell` edge entirely. We rejected `any` for two reasons:

1. **Type safety at the API boundary**: a future caller that forgets the type
   assertion would silently treat the returned `any` as the empty interface,
   compile, and crash at the first method call. Returning `cell.Cell` keeps
   `gocell vet` / IDE call-graph / godoc useful — the caller sees what the
   returned value's *minimum* contract is, even though business code immediately
   narrows further.
2. **Documentation through types**: `Cell` documents the lifecycle/identity
   capability expected of the looked-up object; `any` documents nothing. The
   AI-robust principle "AI co-authors should not have to read prose to know
   the contract" pushes toward the typed return.

The cost is a one-way `kernel/auth → kernel/cell` edge (the only edge auth has
into the rest of kernel/). The `KERNEL-INTERNAL-DAG-01` archtest pins it
explicitly in `allowedKernelEdges`, and the compile-time tripwire
`var _ func(kauth.AssemblyRef, string) cell.Cell = kauth.AssemblyRef.Cell`
(`runtime/bootstrap/auth_plan_apply_test.go`) catches any drift.

## Consequences

**Positive**
- `kernel/cell/` shrinks to its single responsibility (the Cell/Slice
  registration model). All four orthogonal concerns now have their own
  package with focused dependencies.
- The kernel-internal DAG (`KERNEL-INTERNAL-DAG-01`) gets cleaner edges:
  `auth → cell` is a single targeted edge; `outbox → persistence/cellvocab`
  reflects the actual data flow; the `cell → clock/observability/persistence`
  legacy edges drop entirely.
- `Registrar` is name-stable: any future "kernel/registry" sub-package (e.g.
  for runtime cell-instance registries) can use the `Registry` noun without
  semantic conflict.

**Negative / migration cost**
- One-time fan-out: ~115 references to `cell.Registry` across 62 files; ~25
  exported auth symbols and ~13 outbox symbols updated by import-path
  replacement. PR #886's `cells/*/healthz_gen.go` files (and the
  `healthz_gen.tmpl` template) need re-generation.
- Comments and ADRs that mention `cell.Registry` / `cell.AuthPlan` /
  `cell.DurabilityMode` are left as archived history (per the AI-robust rule
  "amendments do not require rewriting archived prose").

## Implementation Matrix (per `.claude/rules/gocell/contract-fanout.md`)

Contract: `kernel/cell.Registry` (interface) → `kernel/cell.Registrar`;
auth and outbox-mode symbols moved across package boundaries.

Change: Move `auth_plan.go`, `auth_types.go`, `mode_resolver.go`,
`durability.go`, `demo_tx_runner.go` out of `kernel/cell/`; delete
`health.go` + `repo_readiness.go*`; rename the registrar interface; update
codegen templates + regenerate; update all callers + archtest path literals
+ name assertions.

Implementations: kernel/cell `RegistryRecorder` is the single implementation
of `Registrar`. Auth and outbox sub-packages are fresh; no existing
external implementations.

Conformance test: `kernel/cell/registry_test.go::TestRegistry_*` (recorder
behavior), `kernel/auth/auth_plan_test.go` (plan constructors / closed
enumeration), `kernel/outbox/mode_resolver_test.go` (emitter resolution),
`kernel/outbox/durability_test.go` (CheckNotNoop), and the archtests listed
below.

Repro: `go test ./kernel/auth/... ./kernel/outbox/... ./tools/archtest/... ./tools/codegen/...`.

Dependent contracts (governance scan): none — this is a kernel-internal
refactor; no contract.yaml or wire payload changes.

Invariant inventory: not applicable (no DROP COLUMN or schema_guard
additions).

## Archtests updated

- `KERNEL-INTERNAL-DAG-01` (`kernel_internal_dag_test.go`): `kernel/auth`
  added as a new owner with `→ cell` edge; `assembly → outbox` and
  `outbox → cellvocab/persistence` added; stale `cell → clock /
  observability / persistence` edges removed.
- `CELL-INIT-CONTRACTUSAGE-01` (`cell_init_test.go`):
  `TestKernelCell_RegistryDefinedHere` renamed to
  `TestKernelCell_RegistrarDefinedHere`; type-name lookup uses `"Registrar"`.
- `REGISTRY-SUBSCRIBE-CELLID-MANDATORY-01` (renamed from
  `...-POSITIONAL-01` when the #1087 builder prong was added)
  (`subscription_invariants_test.go`): interface lookup name updated to
  `Registrar`.
- `ASSEMBLYREF-METHOD-SET-01` (`assembly_invariants_test.go`): source path
  updated to `kernel/auth/auth_plan.go`; `Cell` method return type updated
  to `cell.Cell` (the qualified form after the move).
- `KERNEL-MUSTCTOR-PRODUCTION-DECL-01`
  (`kernel_mustctor_production_decl_test.go`): `kernel/auth/authtest` added
  to `testFixturePkgPrefixes`; carve-out logic test updated.
- `CODEGEN-INIT-INTERNAL-01` (`codegen_invariants_test.go`): expected param
  type updated from `cell.Registry` to `cell.Registrar`.
- `AUTH-PLAN-01` allowlist (`auth_plan_test.go`) + commentary in
  `errcode_invariants_test.go`, `jwt_claims_no_authz_epoch_test.go`,
  `wrapper_location_test.go`, `kernel_mustctor_production_decl_test.go`,
  `storage_backend_test.go`, `sealed_marker_noop_transparency_test.go`:
  path literals updated to the new file locations.

## References

- Issue: #615 [G-10] KERNEL-CELL-PACKAGE-DECOMPOSE
- Related ADRs:
  - `202605101800-adr-cell-interface-isp-split.md` — Cell interface ISP
    split (subitem 4 of G-10, prior PR)
  - `202605231400-002-required-dep-nil-guard-codegen-funnel.md` — service
    required-deps funnel; sessionvalidate's `auth.IntentTokenVerifier`
    required field now re-roots through `kernel/auth`
  - PR #886 (issue #704) — `kernel/healthz/` interface package + cellgen
    typed funnel for repo/emitter probes
- Framework refs:
  - go-kratos/kratos `registry/registry.go@main` — `Registrar` vs
    `Discovery` split
  - Watermill `pubsub/gochannel/pubsub.go@master` — mode decision in the
    publisher impl, not the router
  - kubernetes/apiserver `pkg/authentication/authenticator/interfaces.go@master`
    — sealed Token / Request / Password authenticator interfaces
  - uber-go/fx `lifecycle.go@master` — Lifecycle.Append registration model

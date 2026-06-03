# ADR: outbox.Subscription.CellID is mandatory and HARD-positional

- Status: Accepted
- Date: 2026-05-11
- Tracks: K#07 PR-V1-EVENTROUTER-SUBSCRIPTION-FIELDS (`docs/plans/archive/202605011500-029-master-roadmap.md` 关键路径 #07)
- Builds on: K#04 codegen-cell-gen (PR#360), K#05 marker single-source (PR#363/PR#365), K#06 contract DTO codegen
- Supersedes (description, not file): the historical "Subscription.CellID is optional with fallback to ConsumerGroup" semantics introduced before K#04 when cells had no automatic way to thread their identity into reg.Subscribe.

## Context

`outbox.Subscription` is the first-class identity object that traverses the
entire consumer pipeline: registry → bootstrap drain → router → middleware
chain → subscriber (rabbitmq / in-memory) → idempotency claim. It carries two
conceptually independent axes:

1. **Broker partition / idempotency** — `ConsumerGroup` (required). Drives
   queue / binding names, idempotency key namespaces (`"{ConsumerGroup}:{entry.ID}"`),
   and the competing-consumer / fan-out distinction.
2. **Observability owner** — `CellID` (the topic of this ADR). Drives metric
   labels, slog fields, trace span attributes — i.e. which cell owns the
   subscription, distinct from how the broker partitions traffic.

Before K#07 the two axes were declared independently on the struct but
linked back together by a chain of fallbacks:

- `Subscription.CellID` was documented as **optional** with a runtime fallback
  to ConsumerGroup inside `ObservabilityID()`;
- `kernel/cell.SubscriptionRequest.OwnerCellID` was filled by
  `bootstrap.drainCellSubscriptions` from the snapshot key, **not** by the cell
  during Init — godoc explicitly said "cells only know their ConsumerGroup";
- `runtime/eventrouter.AddContractHandler` had an additional
  `if ownerCellID == "" { ownerCellID = consumerGroup }` fallback for
  test-direct callers.

The trio of fallbacks was a historical artifact: pre-K#04 cells had no
automatic way to declare their identity to a contract-generated
`reg.Subscribe` call, so bootstrap had to retrofit the owner cell ID by
walking the snapshot map keyed by cell ID. K#04 / K#05 / K#06 collapsed
cell metadata into a single source of truth (`metadata.CellMeta` → cellgen
template), making the retrofit obsolete: the cell's ID is statically
available to the cellgen template at codegen time as `$.CellID`.

## Problem

The three fallback paths produced four AI-robust hazards:

1. **Soft semantics.** "CellID is optional with fallback" reads as a runtime
   convenience; any AI session can re-introduce a similar fallback later
   without tripping any guard, silently masking codegen defects.
2. **Silent rewrites.** `drainCellSubscriptions` overwriting `OwnerCellID`
   meant the snapshot value was always authoritative regardless of what the
   cell declared; if a cell registered a subscription owned by a different
   cell (miswire), bootstrap rewrote the wrong owner instead of failing.
3. **Two truths.** Metric labels and slog fields ended up sourced from
   ConsumerGroup when codegen failed to inject CellID — a label distinct from
   the cell identity, indistinguishable from the correct case by an operator
   reading dashboards.
4. **No compile-time gate.** Adding `WithSubscriptionCellID(string)` as an
   option (the obvious "nice API") would have left omission as a runtime
   problem, not a compile failure — Soft per `ai-robust.md` AI-robust matrix.

## Decision

### D1 — `outbox.Subscription.CellID` is required

`Subscription.Validate()` rejects empty `CellID` with the same fail-fast shape
as the other required fields (Topic, ConsumerGroup, ContractID/Kind/Transport).
`Subscription.ObservabilityID()` returns `s.CellID` unconditionally — no
fallback to ConsumerGroup. The empty value is intentionally surfaced when
Validate is bypassed (e.g. an ill-formed test fixture), because substituting
ConsumerGroup would mask the codegen defect that produced the empty CellID
in the first place.

### D2 — `Registry.Subscribe` takes cellID as a positional, mandatory parameter

Signature changes from
```go
Subscribe(spec, handler, consumerGroup string, opts ...SubscriptionOption) error
```
to
```go
Subscribe(spec, handler, consumerGroup, cellID string, opts ...SubscriptionOption) error
```

**Why positional, not option.** A `WithSubscriptionCellID(string)` option
would let a caller omit cellID and produce a `SubscriptionRequest` with
`CellID == ""`, deferring the failure to runtime. Positional makes omission
a compile failure at the call site — the AI-robust HARD pattern from
`ai-robust.md` ("typed function choice for walk depth" precedent). Codegen
templates inject the value from `metadata.CellMeta.ID` at compile time, so
business callers never have to reason about it.

### D2b — Fluent `Registry.Subscription` builder for hand-written cells (#1087, amendment 2026-06-03)

The positional `Subscribe` is ergonomic for codegen (which injects all four
arguments from cell metadata) but error-prone for an **external** cell that does
not run the monorepo codegen and must hand-write the two trailing `string`
arguments (easy to transpose `consumerGroup` ↔ `cellID`). The fluent builder is
the hand-written counterpart that keeps the cellID red line **compile-enforced**:

```go
reg.Subscription(spec).
    CellID(c.ID()).
    ConsumerGroup("payment-charge"). // optional; defaults to CellID
    Handler(c.svc.HandleCharge).
    Register()
```

`Subscription(spec)` returns a `*subscriptionDraft` whose ONLY method is
`CellID(id) *subscriptionBuilder`; the terminal `Register() error` lives only on
`*subscriptionBuilder`. There is therefore **no path to `Register` that skips
`CellID`** — omitting it is a compile error, the same HARD guarantee the
positional 4th parameter gives codegen. Both step types are themselves
**unexported** (sealed construction, same shape as `kernel/outbox.Entry` /
`runtime/observability/metrics.CellLabel`): an external package can chain methods
on the value `Subscription` returns but can neither name nor zero-value-construct
either type, so it cannot fabricate a `*subscriptionBuilder` and reach `Register`
without going through `CellID` (#1087 F1: unexporting the *fields* alone was
insufficient — an exported builder type is still zero-value constructible, so the
*types* are unexported to make the upstream Hard real). `Register` defaults
`consumerGroup` to `cellID`
when unset (the common `consumerGroup == cellID` case, cellgen-consistent) and
delegates to the **same** `RegistryRecorder.Subscribe` validation funnel — the
builder adds no validation of its own, so the positional and fluent forms are two
producers of one validated path, **not** a backward-compat dual path. The
codegen positional form is retained unchanged.

### D3 — `SubscriptionRequest.OwnerCellID` renamed to `CellID`

Single name across `outbox.Subscription.CellID` and
`kernel/cell.SubscriptionRequest.CellID` — historical `OwnerCellID` reminded
readers that bootstrap filled it post-hoc, but bootstrap no longer does.
The new name reflects the truth: the cell declared it itself.

### D4 — `drainCellSubscriptions` is a drift detector, not a rewriter

```go
if sub.CellID != id {
    return fmt.Errorf("bootstrap: cell %s subscription drift: declared CellID=%q but snapshot owner=%q ...", id, sub.CellID, id)
}
```

If a cell registers a subscription whose declared CellID does not match the
snapshot key (the assembly-level cell ID), bootstrap fails. This catches
cell-miswire bugs the old `sub.OwnerCellID = id` silent rewrite hid.

### D5 — `AddContractHandler` rejects empty ownerCellID; no fallback to consumerGroup

`runtime/eventrouter.AddContractHandler` fails fast when called with an empty
`ownerCellID`. The historical "fallback to consumerGroup for test-direct
callers" branch is deleted — all live callers (bootstrap drain + future
direct callers) now pass a real cellID.

### D6 — codegen funnel is the single source of truth

Cell identity flows: `cell.yaml id` → `metadata.CellMeta.ID` → cellgen
template (`$.CellID`) → `NewSubscription(handler, consumerGroup, cellID, sliceID)`
→ `Mount` calls `reg.Subscribe(spec, handler, consumerGroup, cellID, opts...)`.
There is no manual hand-off, no option, no retrofit; the cellID value
appears as a string literal in the generated `cell_gen.go`.

### D7 — Medium-档 archtest safety net

Invariants in `tools/archtest/subscription_invariants_test.go`:

| Invariant | Subject | Failure mode prevented |
|---|---|---|
| `SUBSCRIPTION-FIELDS-FROZEN-01` | `outbox.Subscription` field set | Adding/renaming fields without reviewing this ADR + codegen templates |
| `SUBSCRIPTION-OBSERVABILITY-NO-FALLBACK-01` | `ObservabilityID()` body shape | Re-introducing `if CellID == "" { return ConsumerGroup }` |
| `REGISTRY-SUBSCRIBE-CELLID-MANDATORY-01` (prong 1: positional signature) | `Registry.Subscribe` signature + `Registrar.Subscription` return type | Demoting cellID from positional to a SubscriptionOption (Soft) |
| `REGISTRY-SUBSCRIBE-CELLID-MANDATORY-01` (prong 2: builder shape + field-set, #1087) | `subscriptionDraft` / `subscriptionBuilder` method-set + return types + frozen field set | Collapsing the two-type builder into one type with an optional CellID, OR embedding a type that promotes `Register` onto the draft (both demote the compile-time red line to runtime fail-fast) |

They are Medium-档 (AST scan over kernel/cell source), complementary to the HARD
compile-time gate — if an AI session circumvents the Hard gate (e.g. by rewriting
the templates or collapsing the builder) the archtest catches the demotion. Prong
2's load-bearing assertion: `subscriptionDraft` exposes exactly `{CellID}` (no
`Register`) and `CellID` returns `*subscriptionBuilder`; the companion field-set
freeze additionally bans embedded fields (which could promote `Register` onto the
draft and reopen the missing-CellID path that the method-set scan alone cannot
see). The only door to the terminal `Register` is through `CellID` — freezing
this shape keeps "missing CellID is unreachable" a frozen invariant. (Note: the invariant was renamed from
`REGISTRY-SUBSCRIBE-CELLID-POSITIONAL-01` when prong 2 was added — the positional
signature is now one prong of the broader mandatory-CellID rule.)

## Consequences

### Positive

- Single source of truth for subscription owner is cell metadata, end to end.
- Metric labels / slog fields trace deterministically to a `cell.yaml id`,
  not to a ConsumerGroup string that happens to share the value.
- Cell-miswire bugs (cell A registering subscription owned by cell B) fail at
  bootstrap startup with a clear drift message, instead of producing
  mislabeled telemetry.
- Adding `WithSubscriptionCellID` as a future "convenience option" is now
  a deliberate decision blocked by an archtest, not an accidental Soft drift.

- Breaking change for every Subscription literal in tests + every direct
  `reg.Subscribe` caller. K#07 absorbed the full migration (≈ 100 literal
  fixes across kernel/outbox/outboxtest, runtime/{eventbus,eventrouter,
  observability,bootstrap}, adapters/rabbitmq, cells/*, cmd/corebundle,
  tests/integration). No deprecation alias.
- Tests that intentionally fed empty CellID (e.g. fallback-behavior tests)
  are deleted; new negative test asserts ObservabilityID returns "" on
  empty CellID rather than substituting.
- Positional `Subscribe` is ergonomically awkward for hand-written external
  cells (transposing the two trailing `string` args). **Resolved by D2b**: the
  fluent `Registry.Subscription` builder gives hand-writers the same
  compile-enforced cellID red line without the positional foot-gun.

### Neutral

- `Subscription.ConsumerGroup` semantics unchanged (broker partition key +
  idempotency namespace).
- `WithSubscriptionSliceID(string)` SubscriptionOption stays — SliceID is
  a genuinely optional per-slice observability owner and shares the
  builder-noop nil semantics already in use.

## Alternatives Considered

### A1 — Keep CellID optional, add `WithSubscriptionCellID` option (rejected)

A "nice" builder API where cells call
`reg.Subscribe(spec, handler, "cg", WithSubscriptionCellID(c.ID()))`. Rejected
because omission is silent — produces `CellID == ""` and defers failure to
runtime (or to a stricter Validate which the code didn't have at the time
of K#04). AI-robust: Soft. Per `ai-robust.md` Soft is severely discouraged.

> Not contradicted by D2b: the D2b fluent builder is **not** this rejected
> option form. The rejected A1 is a single-call `Subscribe(... WithCellID(...))`
> where CellID is an *optional* SubscriptionOption (omission compiles). D2b is a
> two-type **type-state** chain where `Register` is unreachable without `CellID`
> (omission does NOT compile) — Hard, not Soft.

### A2 — Newtype `type CellID string` with mandatory constructor (rejected)

A typed wrapper would make compile-time enforcement absolute and orthogonal
to position. Rejected because the value flow already uses `string` via
`cell.ID()` returning string + `metadata.CellMeta.ID` as string; introducing
a newtype would force a cascade of conversions through dozens of call sites
for marginal gain over the positional-parameter HARD gate plus the
typeseval archtest.

### A3 — Delete CellID entirely; collapse to ConsumerGroup (rejected)

Re-frame the metric/log owner as ConsumerGroup. Rejected because real-world
deployments use ConsumerGroup with role suffixes
(`accesscore-rbac-session-sync`) that are not valid cell IDs; conflating
them would force a brittle string parsing convention for dashboards.

## Implementation references

- Plan & W1 RED commit: this PR (K#07 worktree branch).
- W1 RED tests: `tools/archtest/subscription_invariants_test.go` + the
  `cellID empty` Validate case + `ObservabilityID_NoFallbackOnEmpty`.
- W2 GREEN commit: ref the PR title + body.
- W3 cleanup: this ADR + `.claude/rules/gocell/eventbus.md` `Cell 订阅注册` section.

## Industry references

- ThreeDotsLabs/watermill `message/router.go` — subscriber name + consumer
  group injected at the construction site of the subscriber, not lazily
  back-filled by a router lifecycle stage.
- kubernetes-sigs/controller-runtime `pkg/manager/manager.go` —
  add-during-builder pattern; identity is declared at the same point
  where the dependency is wired.
- zeromicro/go-zero goctl — codegen templates embed cell-equivalent
  service identity in the generated entrypoint, not in a runtime
  registration step.
- go-kratos/kratos `transport/http/server.go` — route spec accumulation
  during construction with explicit owner labels.

# GoCell Consistency Levels (L0-L4)

## Overview

Every Cell and Slice must declare a consistency level. This drives architectural decisions: transaction boundaries, outbox usage, replay requirements, and test strategy.

## L0 — LocalOnly

**Scope:** Single slice internal processing. No cross-cell propagation.

| Aspect | Rule |
|--------|------|
| Transaction | None or in-memory only |
| Outbox | Not applicable |
| Events | None published |
| Test | Unit test sufficient |

**Examples:** Input validation, pure computation, local formatting.

## L1 — LocalTx

**Scope:** Single cell, local database transaction. Strong consistency within the cell.

| Aspect | Rule |
|--------|------|
| Transaction | Required — single DB transaction |
| Outbox | Not required (no cross-cell effect) |
| Events | None published outside cell |
| Test | Transaction rollback/commit test |

**Examples:** Session creation, audit log write, config entry update.

## L2 — OutboxFact

**Scope:** Local transaction + outbox publication. The cell writes business state AND an outbox entry in the same transaction, then a relay publishes the event.

| Aspect | Rule |
|--------|------|
| Transaction | Required — business write + outbox write in same tx |
| Outbox | Required |
| Events | Published as authoritative facts |
| Consumer | Must be idempotent, must have consumed marker |
| Test | Outbox write atomicity + consumer idempotency test |

**Examples:** `session.created`, `config.changed`, `user.locked`.

**Critical rule:** Never `eventbus.Publish()` directly after DB commit. Always go through outbox.

## L3 — WorkflowEventual

**Scope:** Cross-cell orchestration, projections, queries, notifications. Eventually consistent.

| Aspect | Rule |
|--------|------|
| Transaction | Consumer-side only |
| Outbox | Consumer may use outbox for further propagation |
| Events | Consumed from L2 producers |
| Projection | Must be rebuildable (discard + replay) |
| Test | Replay test + projection rebuild test |

**Examples:** Fleet query view, audit timeline, compliance tracking.

**Critical rule:** Projections must never become authoritative truth.

## L4 — DeviceLatent

**Scope:** Depends on device coming online. Long delay, weak closure guarantee.

| Aspect | Rule |
|--------|------|
| Transaction | Application-level state machine |
| Outbox | May be used for initial dispatch |
| Closure | Depends on device callback (hours to days) |
| Test | Timeout test + late-arrival test + retry test |

**Examples:** SyncML command ACK, certificate renewal, device inventory refresh.

**Critical rule:** L4 must not be treated as ordinary async. Requires explicit timeout handling, retry budget, and late-arrival merge strategy.

## Verification Matrix

| Level | Unit | Contract | Smoke | Journey | Replay |
|-------|------|----------|-------|---------|--------|
| L0 | Required | — | — | — | — |
| L1 | Required | — | Required | — | — |
| L2 | Required | Required | Required | Required | — |
| L3 | Required | Required | Required | Required | Required |
| L4 | Required | Required | Required | Required | Required |

## Decision Framework

When implementing a new capability, ask:

1. Does it write to DB? → At least L1
2. Do other cells need to know? → L2 (outbox)
3. Is the consumer a projection or query? → L3
4. Does closure depend on external device? → L4
5. Is it pure computation? → L0

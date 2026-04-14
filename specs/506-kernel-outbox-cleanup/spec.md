# Spec: Kernel Outbox Cleanup

**Branch**: `refactor/506-kernel-outbox-cleanup`
**Backlog Source**: `docs/backlog.md` Batch 6A `kernel outbox 清理`
**Date**: 2026-04-13

## Problem

The Batch 6A outbox cleanup row mixes three small-but-related API ownership issues:

1. `P4-TD-01`: multiple tests and the `sso-bff` example each define their own local `noopWriter` even though they all mean the same thing: satisfy `outbox.Writer` without persisting anything in demo/test mode.
2. `P3-DEFER-04`: `Receipt` models idempotency lease lifecycle, but its interface currently lives in `kernel/outbox`, which blurs the ownership boundary between publishing and consumer-side idempotency.
3. `DISCARD-PUB-01`: the old `order-cell` once carried a local `discardPublisher`, but the shared kernel package never absorbed that pattern; today the example falls back to `nil` publisher checks instead of an explicit discard sink.

## Scope

- `kernel/outbox/`: add shared no-op/discard helpers and update docs/tests.
- `kernel/idempotency/`: make `Receipt` an idempotency-owned contract.
- `runtime/eventbus/`, `adapters/redis/`, `adapters/rabbitmq/`: update receipt typing where needed.
- `cells/order-cell/`: use the shared discard publisher without regressing the current stricter demo-mode semantics.
- `cmd/core-bundle/`, `cells/*_test.go`, `examples/sso-bff/`: replace duplicated local noop writers.

## Out Of Scope

- Relay polling behavior, cleanup SQL, or outbox retention policy.
- Any L4 command-state changes from the separate `L4 API 收敛` backlog row.
- Reworking eventbus disposition semantics.
- Production wiring changes for RabbitMQ/Postgres adapters.

## Acceptance Criteria

1. `kernel/idempotency` becomes the canonical home of `Receipt`, and `Claimer` no longer imports `kernel/outbox` just to name the receipt type.
2. A shared `kernel/outbox.NoopOutboxWriter` exists and is used by current test/dev call sites that only need a placeholder writer.
3. A shared `kernel/outbox.DiscardPublisher` exists, and `order-cell` uses the shared helper instead of a package-local pattern or raw `nil` fallback.
4. Demo-mode behavior remains explicit: when order events are discarded, the code logs a skip/discard path rather than pretending a publish succeeded.
5. Targeted tests for `kernel/outbox`, `kernel/idempotency`, `cells/order-cell`, `cmd/core-bundle`, and the affected adapter/runtime packages pass.

## Non-Goals

- Preserve every internal symbol location exactly as-is. This cleanup is allowed to tighten package ownership inside `kernel/`.
- Introduce new runtime fallback behavior in production paths. Shared no-op helpers remain for test/demo-only wiring unless a caller opts in explicitly.
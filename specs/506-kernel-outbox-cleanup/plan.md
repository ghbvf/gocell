# Implementation Plan: Kernel Outbox Cleanup

**Branch**: `refactor/506-kernel-outbox-cleanup`
**Date**: 2026-04-13
**Spec**: `specs/506-kernel-outbox-cleanup/spec.md`

## Summary

Close the Batch 6A `kernel outbox 清理` row with a TDD-first cleanup that does three things:

- move `Receipt` ownership to `kernel/idempotency`,
- provide shared `NoopOutboxWriter` / `DiscardPublisher` helpers in `kernel/outbox`,
- replace duplicated local placeholders and wire `order-cell` to the shared discard path without reviving misleading publish-success behavior.

## Root Cause

### 1. No-op writer duplication

The framework already has a stable `outbox.Writer` abstraction, but no shared placeholder implementation. Tests and examples each redefine the same one-line writer, which adds boilerplate and makes future behavior changes inconsistent.

### 2. Receipt is conceptually misplaced

`Receipt` is produced by `Claimer` and consumed when idempotency commit/release happens after broker Ack/Nack. That lifecycle belongs to the idempotency model, not the outbox publisher model.

### 3. Discard behavior lacks a shared abstraction

`order-cell` historically had a local discard publisher. After runtime-mode hardening, the example switched to `nil` publisher checks to avoid silently claiming success. The shared abstraction never materialized, so the pattern is now both duplicated historically and absent centrally.

## Planned Changes

### 1. Add shared helpers in `kernel/outbox`

- `NoopOutboxWriter` implementing `Writer` and `BatchWriter`
- `DiscardPublisher` implementing `Publisher`
- helper(s) needed to let callers detect the discard path without depending on package-local concrete types
- unit tests for both helpers

### 2. Move canonical `Receipt` ownership to `kernel/idempotency`

- define `Receipt` in `src/kernel/idempotency/idempotency.go`
- update `Claimer` to return `idempotency.Receipt`
- update core code paths that consume receipts
- if churn becomes disproportionate, keep a temporary alias in `kernel/outbox` while making `kernel/idempotency` the source of truth

### 3. Replace duplicated local noop writers

- update current test/example call sites to use `outbox.NoopOutboxWriter{}`
- remove redundant local structs where the shared helper is enough

### 4. Rewire `order-cell` demo fallback

- use `outbox.DiscardPublisher{}` instead of a raw `nil` fallback in demo mode
- keep the current explicit semantics: discarded direct publish logs a skip/discard path, not success
- add tests covering discard-mode behavior

## Expected Files

- `src/kernel/outbox/outbox.go`
- `src/kernel/outbox/outbox_test.go`
- `src/kernel/idempotency/idempotency.go`
- `src/kernel/idempotency/idempotency_test.go`
- `src/kernel/outbox/outboxtest/mock_receipt.go`
- `src/runtime/eventbus/eventbus.go`
- `src/runtime/eventbus/eventbus_test.go`
- `src/adapters/redis/idempotency.go`
- `src/adapters/redis/idempotency_test.go`
- `src/adapters/rabbitmq/consumer_base.go`
- `src/adapters/rabbitmq/rabbitmq_test.go`
- `src/cells/order-cell/cell.go`
- `src/cells/order-cell/cell_test.go`
- `src/cells/order-cell/slices/order-create/service.go`
- `src/cells/order-cell/slices/order-create/service_test.go`
- `src/cmd/core-bundle/auth_integration_test.go`
- `src/cells/access-core/cell_test.go`
- `src/cells/audit-core/cell_test.go`
- `src/cells/config-core/cell_test.go`
- `src/examples/sso-bff/main.go`
- `docs/backlog.md`

## TDD Order

1. Add kernel tests for `NoopOutboxWriter`, `DiscardPublisher`, and idempotency-owned `Receipt` typing.
2. Add `order-cell` tests for shared discard fallback / discard-mode logging semantics.
3. Add compile-time or behavior tests that replace local noop writers in the touched test/example files.
4. Implement the shared helpers and receipt move.
5. Update call sites.
6. Run targeted package tests, then `go build ./...`.

## Validation Plan

1. `cd src && go test ./kernel/outbox ./kernel/idempotency ./cells/order-cell/... ./cmd/core-bundle`
2. `cd src && go test ./runtime/eventbus ./adapters/redis ./adapters/rabbitmq`
3. `cd src && go test ./cells/access-core ./cells/audit-core ./cells/config-core ./examples/sso-bff`
4. `cd src && go build ./...`

## Review Focus

- no dependency cycle introduced between `kernel/outbox` and `kernel/idempotency`
- no misleading success logs on discarded publish paths
- no new production noop fallback beyond existing explicit demo/test wiring
- no unrelated behavior change to relay/eventbus semantics
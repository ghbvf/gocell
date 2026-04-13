# Tasks: Kernel Outbox Cleanup

**Input**: `specs/506-kernel-outbox-cleanup/spec.md`, `specs/506-kernel-outbox-cleanup/research.md`, `specs/506-kernel-outbox-cleanup/plan.md`
**TDD**: required

## Task List

- [x] T001 Add failing kernel tests for `NoopOutboxWriter` and `DiscardPublisher` in `kernel/outbox`.
- [x] T002 Add failing typing tests for idempotency-owned `Receipt` in `kernel/idempotency` and touched helper packages.
- [x] T003 Add failing `order-create` / `order-cell` tests covering shared discard fallback and explicit skip semantics.
- [x] T004 Implement `NoopOutboxWriter` and `DiscardPublisher` in `kernel/outbox`.
- [x] T005 Move canonical `Receipt` ownership to `kernel/idempotency` and update core receipt consumers.
- [x] T006 Replace duplicated local noop writers in touched tests/examples with `outbox.NoopOutboxWriter{}`.
- [x] T007 Update `order-cell` to use the shared discard publisher without reporting false publish success.
- [x] T008 Run targeted package tests for kernel, order-cell, runtime/eventbus, adapters/redis, and adapters/rabbitmq.
- [x] T009 Run touched cell/example tests and `go build ./...` from `src/`.
- [x] T010 Update `docs/backlog.md` to reflect closure of the outbox cleanup row.
- [x] T011 Create the PR with the plan/research context in the description.
- [x] T012 Launch six-seat review and collect findings.
- [x] T013 Check PR comments + CI, then use the fix workflow on confirmed C1/C2 findings.

## Execution Order

1. T001-T003
2. T004-T007
3. T008-T010
4. T011-T013

## Notes

- `T005` may use a temporary compatibility alias if that keeps the cleanup focused without violating ownership.
- `T007` must preserve the stricter runtime-mode behavior introduced by `fix(order-cell): harden runtime modes`.
- `T013` is limited to confirmed in-scope C1/C2 findings; larger architectural follow-ups go to backlog or tech debt.
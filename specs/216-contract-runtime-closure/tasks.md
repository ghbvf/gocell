# Tasks: Contract Runtime Closure

## Working Mode

- Follow TDD: write the failing test first, verify it fails for the right reason, then implement the smallest change that makes it pass.
- Use the implementation worktree at `/Users/shengming/Documents/code/gocell/worktrees/216-contract-runtime-closure`.
- Keep changes inside the defined scope for this batch.

## Task List

### Wave 0: Design Gate

- [x] T01 Document the HTTP transport metadata decision for migrated contracts.
- [x] T02 Document the canonical event identity decision (`outbox.Entry.ID -> wire id -> contract event_id`).
- [x] T03 Freeze scope and record explicit non-goals for this batch.

### Wave 1: Metadata Foundation

- [x] T04 Write failing governance and parser tests for `endpoints.http` metadata.
- [x] T05 Implement `endpoints.http` fields in `contract.schema.json`.
- [x] T06 Implement metadata type support in `kernel/metadata/types.go`.
- [x] T07 Update parser tests and metadata round-trip tests.
- [x] T08 Add governance rules and tests for `noContent` and response schema compatibility.
- [x] T09 Update scaffold awareness only if required to keep generated HTTP contracts structurally valid. Not required in this batch.

### Wave 2A: Helper Upgrade

- [x] T10 Write failing helper tests for HTTP status validation and no-content behavior.
- [x] T11 Extend `pkg/contracttest` to load `endpoints.http` metadata.
- [x] T12 Add recorder-based HTTP transport assertions.
- [x] T13 Preserve existing schema-only helper behavior.

### Wave 2B: Order-Cell Runtime Repair

- [x] T14 Write a failing test proving order-cell default init can panic on missing publisher.
- [x] T15 Add safe default publisher behavior for order-cell.
- [x] T16 Add `WithOutboxWriter` and `WithTxManager` to order-cell.
- [x] T17 Write failing tests for emitted entry identity in order-create service.
- [x] T18 Implement outbox-capable publish path in order-create service.
- [x] T19 Upgrade cell-level default-path tests to use a real create request.
- [x] T20 Evaluate whether device-cell should receive the same safe default publisher fix in this batch. Deferred to a follow-up because this batch only closes order-cell and contract/runtime gaps.

### Wave 3A: Access-Core Provider-Driven Migration

- [x] T21 Write the failing delete provider-driven contract test.
- [x] T22 Update delete request schema with explicit no-body/path-ID semantics.
- [x] T23 Add `endpoints.http` to auth user delete contract.
- [x] T24 Implement the minimal helper/contract changes needed to make delete pass.
- [x] T25 Select one representative body-returning route and write its failing provider-driven contract test.
- [x] T26 Migrate that route to the new contract testing pattern.

### Wave 3B: Order-Cell Provider-Driven Migration

- [x] T27 Write failing provider-driven tests for order create/get/list routes.
- [x] T28 Add `endpoints.http` to the selected order contracts.
- [x] T29 Migrate order HTTP contract tests to real handler-backed assertions.
- [x] T30 Write a failing event contract test that requires a real emitted entry identity.
- [x] T31 Migrate order-created event contract validation to emitted-entry-based verification.

### Wave 4: Documentation and Journey Alignment

- [x] T32 Rewrite `J-order-create` to distinguish demo and durable behavior.
- [x] T33 Update `todo-order` README response examples to match actual handlers.
- [x] T34 Add a demo-vs-durable feature matrix to `todo-order` README.
- [x] T35 Optionally add `cells/order-cell/README.md` if the new runtime path needs a dedicated operator/developer explanation. Not required in this batch.

### Wave 5: Verification and PR Preparation

- [x] T36 Run focused kernel/helper tests.
- [x] T37 Run focused access-core slice tests.
- [x] T38 Run focused order-cell tests.
- [x] T39 Run optional device-cell tests if that cell was touched. Not required because device-cell was not changed.
- [x] T40 Run full `go test ./...`.
- [x] T41 Run full `go build ./...`.
- [x] T42 Create the PR from `fix/216-contract-runtime-closure` to `develop`.
- [x] T43 Launch six-role review on the PR.
- [x] T44 Triage and fix review findings if needed.

## PR Status

- PR: #106
- URL: https://github.com/ghbvf/gocell/pull/106

## TDD Notes

For each migrated behavior, the first test to write should be one of the following:

1. Metadata/gating failure tests before schema/type updates
2. Helper failure tests before helper implementation changes
3. Provider-driven handler tests before contract test migration
4. Emitted-entry tests before order-create runtime changes
5. README/journey expectation checks after code behavior is stable

## Minimal Definition of Done

1. Access-core delete is explicitly modeled and provider-verified.
2. Order-cell no longer has a nil-publisher runtime hazard.
3. Order-created event validation uses a real emitted identity.
4. Demo vs durable semantics are honest in docs and journey.
5. PR is created and reviewed by six independent benches.
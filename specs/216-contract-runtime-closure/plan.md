# Plan: Contract Runtime Closure

## Goal

Close the current contract/runtime mismatch by delivering one coherent batch that:

1. Makes HTTP transport semantics explicit for selected contracts
2. Restores provider-driven verification for representative HTTP and event contracts
3. Fixes the order-cell nil publisher hazard and upgrades order-create toward an outbox-capable L2 path
4. Aligns journey and example docs with actual demo versus durable behavior

## Scope

### Included

1. `DELETE` no-content semantics closure for auth user delete
2. `endpoints.http` metadata rollout for the contracts migrated in this batch
3. Provider-driven HTTP contract tests for selected access-core and order-cell routes
4. Order-cell runtime repair for outbox-capable publish path and safe default publisher handling
5. Event identity alignment on emitted order-created entries
6. `J-order-create` and `todo-order` documentation alignment

### Excluded

1. `config.read` split and PascalCase DTO debt
2. Auth response schema hardening beyond what this batch directly touches
3. Broad migration of all HTTP contracts in the repository
4. Broker-header redesign for event identity
5. Helper package relocation unless implementation becomes blocked by it

## Worktree

- Root repo: `/Users/shengming/Documents/code/gocell`
- Implementation worktree: `/Users/shengming/Documents/code/gocell/worktrees/216-contract-runtime-closure`
- Branch: `fix/216-contract-runtime-closure`
- Base: `origin/develop`

## Delivery Strategy

Use TDD and phased implementation. For every substantive behavior change, write or upgrade the failing test first, confirm the failure mode, then implement the smallest code change to make the test pass.

## Phases

### Phase 0.5: Design Gate

Settle the implementation decisions before code edits:

1. Confirm `endpoints.http` field shape:
   - `method`
   - `path`
   - `successStatus`
   - `noContent`
2. Confirm backward-compatibility rules:
   - old contracts without `endpoints.http` still parse
   - once `endpoints.http` exists on a contract, all four fields are required for that contract
3. Confirm event identity rule:
   - `outbox.Entry.ID` is the canonical event identity source
4. Confirm test layering rule:
   - schema validation remains smoke
   - handler/emitted-entry validation becomes provider-driven verification

### Phase 1: Metadata Foundation

Update kernel metadata foundations so migrated HTTP contracts can declare transport semantics.

Targets:

1. `kernel/metadata/schemas/contract.schema.json`
2. `kernel/metadata/types.go`
3. `kernel/metadata/types_test.go`
4. `kernel/metadata/parser_test.go`
5. `kernel/governance/validate.go`
6. `kernel/governance/validate_test.go`

Required outcomes:

1. Transport metadata parses and round-trips correctly.
2. Governance rejects invalid `noContent` and response-schema combinations.
3. Existing non-migrated contracts still parse.

### Phase 2: Contract Test Helper Upgrade

Extend contract testing support without removing existing schema-only helpers.

Targets:

1. `pkg/contracttest/contracttest.go`
2. `pkg/contracttest/contracttest_test.go`

Required outcomes:

1. Recorder-based assertions can validate status and no-content behavior.
2. Existing request/response/payload/headers schema helpers still work.
3. Helper tests cover failing transport cases first.

### Phase 3A: Access-Core HTTP Migration

Start with the cleanest closure path: auth user delete, then one body-returning route.

Targets:

1. `contracts/http/auth/user/delete/v1/contract.yaml`
2. `contracts/http/auth/user/delete/v1/request.schema.json`
3. `cells/access-core/slices/identitymanage/contract_test.go`
4. `cells/access-core/slices/identitymanage/handler_test.go` as setup/reference reuse
5. `contracts/http/auth/user/create/v1/contract.yaml` if create is selected as the representative body-returning migration

Required outcomes:

1. Delete test is provider-driven and asserts `204`, empty body, and no envelope.
2. Request schema explicitly says body is empty and ID comes from path.
3. One route with a response body proves the new helper works on normal success responses.

### Phase 3B: Order-Cell Runtime Repair

Repair the runtime path before migrating order contract tests.

Targets:

1. `cells/order-cell/cell.go`
2. `cells/order-cell/slices/order-create/service.go`
3. `cells/order-cell/slices/order-create/service_test.go`
4. `cells/order-cell/cell_test.go`
5. `kernel/outbox/outbox.go` or a new helper file in that package for shared noop publisher support

Required outcomes:

1. No default init path can leave a nil publisher behind.
2. Order-create can emit a real `outbox.Entry` when configured with an outbox writer.
3. Fallback behavior is explicit and test-covered.

### Phase 3C: Order-Cell Contract Migration

Once runtime repair is in place, migrate selected order contracts.

Targets:

1. `contracts/http/order/create/v1/contract.yaml`
2. `contracts/http/order/get/v1/contract.yaml`
3. `contracts/http/order/list/v1/contract.yaml`
4. `cells/order-cell/slices/order-create/contract_test.go`
5. `cells/order-cell/slices/order-query/contract_test.go`
6. `contracts/event/order-created/v1/*`

Required outcomes:

1. Representative order HTTP routes are provider-driven.
2. Order-created event contract validation uses a real emitted entry identity.
3. Payload-only placeholder comments are removed.

### Phase 4: Documentation and Journey Alignment

Targets:

1. `journeys/J-order-create.yaml`
2. `examples/todo-order/README.md`
3. Optional: `cells/order-cell/README.md`

Required outcomes:

1. Demo versus durable behavior is explicit.
2. Response examples match the actual handlers.
3. Journey language does not overclaim durable event delivery.

### Phase 5: Verification and PR Preparation

Run focused verification first, then full regression.

Order:

1. `go test ./kernel/metadata ./kernel/governance ./pkg/contracttest`
2. `go test ./cells/access-core/slices/identitymanage ./cells/access-core/slices/sessionlogin`
3. `go test ./cells/order-cell/...`
4. Optional if touched: `go test ./cells/device-cell/...`
5. `go test ./...`
6. `go build ./...`

If the implementation passes the gates, create a PR from `fix/216-contract-runtime-closure` and then run six-role review.

## Acceptance Gates

1. Gate A: Metadata foundation is green and backward compatible.
2. Gate B: Helper can validate both no-content and body-returning HTTP responses.
3. Gate C: Access-core delete semantics are fully closed.
4. Gate D: Order-cell no longer risks nil-publisher panic and can emit a real entry identity.
5. Gate E: Order contract tests validate real runtime behavior.
6. Gate F: Journey and README no longer overclaim durable behavior.

## Review Plan After Implementation

After implementation and verification:

1. Create the PR
2. Launch six independent review benches:
   - architecture
   - security
   - testing/regression
   - ops/deployment
   - maintainability/DX
   - product/developer-visible experience
3. Aggregate findings by root cause, not by file inventory
# Review Findings — Contract Runtime Closure

## Review Baseline

- Branch: `fix/216-contract-runtime-closure`
- PR: `#106`
- Final commit: `2fa93b2dcc53743ce7b87219ca34105167c0fdf8`

## Round 1

### Blocking Findings

1. `P1` order-cell missing publisher path was converted into silent discard semantics.
2. `P1` durable order-create path allowed `outboxWriter` without `txRunner`.

### Non-blocking Findings

1. `P2` order HTTP contract tests still stopped at handler level instead of a full transport harness.
2. `P2` no-content helper accepted whitespace-only bodies.

## Round 1 Fixes

1. Removed the silent no-op publisher success path and made the no-publisher path explicit.
2. Bound `outboxWriter` and `txRunner` as one durable wiring unit.
3. Promoted order HTTP contract tests to real `ServeHTTP` transport harnesses.
4. Tightened no-content validation to require zero raw response bytes.

## Round 2

### Remaining Blocking Finding

1. `P1` durable mode still allowed the repository to default to in-memory storage.

## Round 2 Fix

1. Added fail-fast validation so durable order-cell wiring requires an explicit repository and no longer falls back to the in-memory repository.

## Final Outcome

- Final focused re-review result: `no blocking issues`
- Residual observations were recorded in `tech-debt.md`

## Round 3 — Cross-cutting PR review fixes

### Findings (from comprehensive PR #106 review)

| # | Severity | Issue | Fix |
|---|----------|-------|-----|
| 1 | P1 | `evt-` prefix contradicts headers.schema.json "UUID" description | Updated all 10 event `headers.schema.json` descriptions to "Prefixed event identifier (evt-{uuid})" |
| 2 | P1 | access-core contract test routes `/api/v1/auth/` don't match production `/api/v1/access/` | Updated contract YAML paths and contract_test.go routes |
| 3 | P2 | `outbox.Entry.Validate()` doesn't check empty ID | Added `ID != ""` check as first validation |
| 4 | P2 | FMT-13 missing reverse constraint: noContent=false without response schema | Added SeverityWarning for the gap |
| 5 | P2 | contractWriter/contractTxRunner duplicate mocks in order-create tests | Consolidated to use recordingWriter/stubTxRunner from service_test.go |
| 6 | P3 | Hardcoded `secret123` in identitymanage/contract_test.go | Extracted to testPassword constant |
| 7 | P3 | Specs contain absolute local paths | Replaced with relative/generic paths |
| 8 | — | Whitespace/alignment fixes across kernel types and governance | gofmt alignment cleanup |
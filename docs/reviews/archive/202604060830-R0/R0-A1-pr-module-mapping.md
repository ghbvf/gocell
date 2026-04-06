# R0-A1: PR->Module Mapping Table

> Agent: R0-A1 (reviewer)
> Date: 2026-04-06
> Baseline commit: `ce03ba1` (develop HEAD)
> Source: Existing review docs + review plans + branch names from FETCH_HEAD + file structure analysis
> Methodology: PR file lists extracted from review-plan-1-per-pr.md, phase3-pr7-12-six-role-review.md, pr3-phase2-runtime-cells-review.md, full-codebase-review archive, and branch-to-PR correlation via .git/FETCH_HEAD

## Notes

- **#18-#27 are GitHub Issues, not PRs.** GitHub uses shared numbering; there are no PRs with these numbers.
- **#5 (rebase)** and **#13 (replaced by #14)** are marked SKIP per review plan.
- Total PRs: 23 (numbered #1-#17, #28-#33, with #18-#27 gap being issues).
- Module mapping is based on the directory-to-module-ID table from the task specification.
- Branch names confirmed via `.git/FETCH_HEAD`: each PR maps to a feature/fix branch.

## Branch-to-PR Correlation

| PR | Branch |
|----|--------|
| #1 | `200-review-fixes` |
| #2 | `fix/workflow-branch-model` (or similar) |
| #3 | `feat/001-phase2-runtime-cells` |
| #4 | `fix/phase2-tech-debt` |
| #5 | (rebase, SKIP) |
| #6 | `worktree-workflow-fix` (or similar) |
| #7 | `feat/phase3/w0-bootstrap-refactor` |
| #8 | `feat/phase3/w0-uuid-seed-regression` |
| #9 | `feat/phase3/w1-devops` |
| #10 | `feat/phase3/w1-postgres-base` |
| #11 | `feat/phase3/w1-redis` |
| #12 | `feat/phase3/w1-rabbitmq` |
| #13 | `feat/phase3/w2-outbox-chain` (SKIP, replaced by #14) |
| #14 | `feat/phase3/w2-all-adapters` |
| #15 | `feat/phase3/w3-cells` |
| #16 | `feat/phase3/w3-security` |
| #17 | `feat/phase3/w3-kernel` |
| #28 | `feat/phase3/w4-tests` |
| #29 | `feat/phase3/w4-docs` |
| #30 | `feat/phase3/w4-all` |
| #31 | `feat/002-phase3-adapters` (integration branch) |
| #32 | `feat/003-phase4-examples-docs` |
| #33 | `fix/004-tier0-preflight` |

---

## PR->Module Mapping Table

| PR | Title | State | Phase | Affected Modules |
|----|-------|-------|-------|-----------------|
| #1 | Baseline review fixes (B1-B6 + S1-S2) | MERGED | Phase 0-1 Fix | kernel/governance, kernel/metadata, kernel/assembly, kernel/cell, kernel/outbox |
| #2 | Workflow branch model fix | MERGED | Infra | devops |
| #3 | Phase 2: Runtime + Built-in Cells | MERGED | Phase 2 | kernel/cell, kernel/assembly, kernel/governance, kernel/metadata, kernel/outbox, kernel/idempotency, runtime/auth, runtime/eventbus, runtime/config, runtime/http, runtime/observability, cells/access-core, cells/audit-core, cells/config-core, pkg, delivery/cmd, yaml-metadata, docs |
| #4 | Phase 2 tech-debt cleanup | MERGED | Phase 2 Fix | kernel/governance, kernel/metadata, kernel/cell, runtime/auth, cells/access-core, cells/audit-core, cells/config-core, pkg |
| #5 | Rebase | SKIP | -- | -- |
| #6 | Workflow fix | MERGED | Infra | devops |
| #7 | Bootstrap interface injection + outbox doc | MERGED | Phase 3 W0 | runtime/observability, kernel/outbox |
| #8 | crypto/rand UUID + seed script | MERGED | Phase 3 W0 | pkg, cells/access-core, cells/audit-core, cells/config-core, adapters/websocket, devops |
| #9 | Docker Compose + Makefile + healthcheck | MERGED | Phase 3 W1 | devops |
| #10 | PostgreSQL adapter (Pool/TxManager/Migrator/OutboxWriter/OutboxRelay) | MERGED | Phase 3 W1 | adapters/postgres |
| #11 | Redis adapter (Client/DistLock/Idempotency/Cache) | MERGED | Phase 3 W1 | adapters/redis |
| #12 | RabbitMQ adapter (Publisher/Subscriber/ConsumerBase) | MERGED | Phase 3 W1 | adapters/rabbitmq |
| #13 | Wave 2 outbox chain (replaced by #14) | SKIP | Phase 3 W2 | -- |
| #14 | Wave 2: Outbox relay, OIDC, S3, WebSocket | MERGED | Phase 3 W2 | adapters/postgres, adapters/oidc, adapters/s3, adapters/websocket, kernel/outbox, kernel/idempotency |
| #15 | Cells: outbox.Writer rewire + product fixes | MERGED | Phase 3 W3 | cells/access-core, cells/audit-core, cells/config-core, yaml-metadata |
| #16 | Security hardening: RS256, trustedProxies | MERGED | Phase 3 W3 | runtime/auth, runtime/http |
| #17 | Kernel: lifecycle LIFO + BaseCell mutex | MERGED | Phase 3 W3 | kernel/cell, kernel/assembly, kernel/governance, kernel/outbox, pkg, runtime/observability, runtime/config, runtime/eventbus |
| #28 | W4: integration test stubs + coverage | CLOSED | Phase 3 W4 | adapters/postgres, adapters/redis, adapters/rabbitmq, adapters/oidc, adapters/s3, adapters/websocket, delivery/cmd, delivery/tests |
| #29 | W4: doc.go + guides | CLOSED | Phase 3 W4 | adapters/postgres, adapters/redis, adapters/rabbitmq, adapters/oidc, adapters/s3, adapters/websocket, runtime/auth, runtime/eventbus, runtime/config, runtime/http, runtime/observability, kernel/cell, kernel/assembly, kernel/governance, kernel/metadata, kernel/outbox, kernel/idempotency, docs |
| #30 | W4: merged tests + docs + kg-verify | CLOSED | Phase 3 W4 | (superset of #28 + #29), devops |
| #31 | Phase 3 integration entry (22 commits) | MERGED | Phase 3 Integration | adapters/postgres, adapters/redis, adapters/rabbitmq, adapters/oidc, adapters/s3, adapters/websocket, runtime/auth, runtime/eventbus, runtime/config, runtime/http, runtime/observability, kernel/cell, kernel/assembly, kernel/governance, kernel/metadata, kernel/outbox, kernel/idempotency, cells/access-core, cells/audit-core, cells/config-core, pkg, delivery/cmd, delivery/tests, devops, yaml-metadata, docs |
| #32 | Phase 4: Examples + Docs | MERGED | Phase 4 | cells/device-cell, cells/order-cell, delivery/examples, delivery/tests, delivery/cmd, yaml-metadata, docs, devops |
| #33 | Tier 0 review findings: P0+P1+P2 batch fix | MERGED | Preflight Fix | delivery/cmd, delivery/examples, devops, cells/access-core, cells/audit-core, cells/config-core, runtime/http, pkg |

---

## Module->PR Reverse Index

| Module ID | Affected by PRs |
|-----------|----------------|
| **pkg** | #3, #4, #8, #17, #33 |
| **kernel/cell** | #1, #3, #4, #17, #29, #31 |
| **kernel/outbox** | #1, #3, #7, #14, #17, #29, #31 |
| **kernel/metadata** | #1, #3, #4, #29, #31 |
| **kernel/governance** | #1, #3, #4, #17, #29, #31 |
| **kernel/assembly** | #1, #3, #17, #29, #31 |
| **kernel/idempotency** | #3, #14, #29, #31 |
| **runtime/auth** | #3, #4, #16, #29, #31 |
| **runtime/eventbus** | #3, #17, #29, #31 |
| **runtime/config** | #3, #17, #29, #31 |
| **runtime/http** | #3, #16, #29, #31, #33 |
| **runtime/observability** | #3, #7, #17, #29, #31 |
| **adapters/postgres** | #10, #14, #28, #29, #31 |
| **adapters/redis** | #11, #28, #29, #31 |
| **adapters/rabbitmq** | #12, #28, #29, #31 |
| **adapters/oidc** | #14, #28, #29, #31 |
| **adapters/s3** | #14, #28, #29, #31 |
| **adapters/websocket** | #8, #14, #28, #29, #31 |
| **cells/access-core** | #3, #4, #8, #15, #31, #33 |
| **cells/audit-core** | #3, #4, #8, #15, #31, #33 |
| **cells/config-core** | #3, #4, #8, #15, #31, #33 |
| **cells/device-cell** | #32 |
| **cells/order-cell** | #32 |
| **delivery/examples** | #32, #33 |
| **delivery/cmd** | #3, #28, #31, #32, #33 |
| **delivery/tests** | #28, #31, #32 |
| **yaml-metadata** | #3, #15, #31, #32 |
| **docs** | #3, #29, #31, #32 |
| **devops** | #2, #6, #8, #9, #30, #31, #32, #33 |

---

## PR Dependency / Chronology

```
Phase 0 (pre-PR): Initial skeleton on main
  |
  v
Phase 0-1 Fix:
  PR#1 (baseline fixes) -----> develop
  PR#2 (workflow) ------------> develop
  |
  v
Phase 2:
  PR#3 (runtime + cells) -----> main (reverted), then develop via rebase
  PR#4 (tech-debt from #3) ---> develop
  PR#5 (rebase, SKIP)
  PR#6 (workflow fix) --------> develop
  |
  v
Phase 3 (all -> feat/002-phase3-adapters integration branch):
  Wave 0: PR#7, PR#8
  Wave 1: PR#9, PR#10, PR#11, PR#12
  Wave 2: PR#13 (SKIP), PR#14
  Wave 3: PR#15, PR#16, PR#17
  Wave 4: PR#28 (CLOSED), PR#29 (CLOSED), PR#30 (CLOSED)
    |
    v
  PR#31 (Phase 3 integration) -> develop (22 commits)
  |
  v
Phase 4:
  PR#32 (examples + docs) ----> develop
  |
  v
Preflight:
  PR#33 (Tier 0 fixes) -------> develop
```

---

## Statistics

| Metric | Value |
|--------|-------|
| Total PR numbers | 33 (#1-#33) |
| Actual PRs | 23 |
| GitHub Issues (#18-#27) | 10 |
| Skipped PRs | 2 (#5 rebase, #13 replaced) |
| Closed PRs (content in #31) | 3 (#28, #29, #30) |
| Merged PRs | 18 |
| Most-touched module | cells/access-core (6 PRs) |
| Most-touching PR | #31 (all modules, integration) |
| Widest non-integration PR | #3 (Phase 2, ~295 files) |
| Phase 3 adapter PRs | #10, #11, #12, #14 (4 adapters) |
| Phase 4 new cells | device-cell, order-cell (#32 only) |

---

## Key Observations for Downstream Review Agents

1. **PR#3 is the foundational PR** -- it introduced nearly all runtime, kernel, and cell code. Most modules' original implementation traces back to this single PR.

2. **PR#31 is a superset** -- as the Phase 3 integration entry, it contains all changes from PRs #7-#17 and #28-#30. Do not double-count findings.

3. **PRs #28-#30 are CLOSED but their content lives in #31** -- review these via the current codebase state, not PR diffs.

4. **device-cell and order-cell only appear in PR#32** -- they are Phase 4 additions with no prior history.

5. **kernel/governance is the most frequently fixed module** -- touched by PRs #1, #3, #4, #17, #29, #31, indicating ongoing refinement of validation rules.

6. **adapters/ modules have clean PR boundaries** -- each adapter was introduced in exactly one PR (#10 postgres, #11 redis, #12 rabbitmq, #14 oidc+s3+websocket), making PR-level review straightforward.

7. **The "gap" between PR#17 and PR#28** corresponds to Issues #18-#27 (follow-up findings from PR#7-#12 review). These are tracked separately and should be checked against their resolution status.

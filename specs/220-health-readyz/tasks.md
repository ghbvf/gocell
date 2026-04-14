# Tasks — 220 Health/Readyz

## Working Mode

- Follow TDD: write the failing test first, verify the failure, then implement the smallest code change that makes it pass.
- Use the implementation worktree at `worktrees/220-health-readyz`.
- Keep changes inside the scope defined in `plan.md`.

## Task List

### Wave 0 — Branch-Local Planning

- [x] T01 Create branch-local `research.md`, `plan.md`, and `tasks.md`.

### Wave 1 — Health Handler Semantics

- [x] T02 Write failing tests for liveness-only `/healthz`.
- [x] T03 Write failing tests for aggregate-only `/readyz` default output.
- [x] T04 Write failing tests for `/readyz?verbose` detailed output.
- [x] T05 Implement the health handler/reporting changes.
- [x] T06 Update router-level tests for the new readiness surface while preserving infra endpoint bypass semantics.

### Wave 2 — Watcher And Event Router Health

- [x] T07 Write failing watcher tests for not-started, started, and closed readiness states.
- [x] T08 Implement watcher health surface in `runtime/config`.
- [x] T09 Write failing event-router tests for before-running, running, and terminal-failure readiness states.
- [x] T10 Implement event-router health surface in `runtime/eventrouter`.

### Wave 3 — Bootstrap Wiring

- [x] T11 Write failing bootstrap tests for config watcher init fail-fast.
- [x] T12 Write failing bootstrap tests for verbose readyz including `config-watcher`.
- [x] T13 Write failing bootstrap tests for verbose readyz including `eventrouter` when subscriptions exist.
- [x] T14 Implement bootstrap wiring for watcher and event-router readiness.
- [x] T15 Regress existing bootstrap health-checker tests to ensure external adapter checkers still work.

### Wave 4 — Docs And Verification

- [x] T16 Update runbook and any touched example docs for `?verbose` semantics.
- [x] T17 Run focused runtime package tests.
- [x] T18 Run `go build ./...`.
- [x] T19 Run `go test ./... -count=1`.

### Wave 5 — PR And Review Loop

- [x] T20 Commit, push, and create the PR against `develop`.
- [x] T21 Launch six-seat review on the PR.
- [x] T22 Read PR comments and CI status.
- [x] T23 Use the fix flow to repair in-scope `C1` and `C2` findings, then re-run validation.

## Dependencies

| Task | Depends On |
|------|------------|
| T02-T06 | T01 |
| T07-T10 | T05 |
| T11-T15 | T08, T10 |
| T16-T19 | T14 |
| T20 | T16-T19 |
| T21 | T20 |
| T22 | T21 |
| T23 | T22 |

## Notes

1. `T02`, `T03`, `T04`, `T07`, `T09`, `T11`, `T12`, and `T13` are mandatory red-first TDD gates.
2. This branch intentionally does not add a second admin listener. Default-safe output plus verbose opt-in is the chosen scope cut.
3. Existing `WithHealthChecker` remains the adapter integration mechanism for PostgreSQL, Redis, RabbitMQ, and future external dependencies.

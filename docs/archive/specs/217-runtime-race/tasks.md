# Tasks: Runtime Race Closure

**Input**: `specs/217-runtime-race/spec.md`, `specs/217-runtime-race/research.md`, `specs/217-runtime-race/plan.md`  
**TDD**: required

## Task List

- [x] T001 Add bootstrap reload-gate tests that define the required shutdown semantics before implementation.
- [x] T002 Add an eventbus concurrent `Publish`/`Close` regression test that proves the lock invariant under `-race`.
- [x] T003 Revalidate the existing worker cancellation behavior and keep it unchanged unless a new failing case is proven.
- [x] T004 Implement the internal bootstrap reload gate using channel-plus-select drain signaling.
- [x] T005 Replace the bootstrap `reloadWG` shutdown coordination with the new gate.
- [x] T006 Add an explicit eventbus lock-order comment so the concurrency guarantee is maintained by future edits.
- [x] T007 Run targeted package tests and race tests for `runtime/eventbus`, `runtime/worker`, and `runtime/bootstrap` from 项目根目录.
- [x] T008 Run `go build ./...` from 项目根目录.
- [x] T009 Update `docs/backlog.md` to reflect the closure state of the runtime race row.
- [ ] T010 Create the PR and launch six-role review after verification passes.

## Execution Order

1. T001-T003
2. T004-T006
3. T007-T008
4. T009-T010

## Notes

- `T003` is a validation task, not automatically a code change.
- `T004` and `T005` are the only required behavior changes.
- `T006` is intentionally small: the concurrency contract already exists in code and must be made obvious.

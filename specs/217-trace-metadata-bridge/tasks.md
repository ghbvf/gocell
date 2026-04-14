# Tasks — 217 Trace Metadata Bridge

## Task List

- [x] T01 Create branch-local research, plan, acceptance, dependency, and PR plan docs.
- [x] T02 Write failing tests for shared bridge helper:
  `context -> metadata`, `metadata -> context`, nil metadata handling, preserve existing keys, no `span_id` propagation.
- [x] T03 Implement the shared bridge helper and consume-side middleware in `kernel/outbox/`.
- [x] T04 Write failing tests for PostgreSQL outbox writer auto-injection in both `Write` and `WriteBatch`.
- [x] T05 Implement publish-side metadata injection in `adapters/postgres/outbox_writer.go`.
- [x] T06 Write failing tests for bootstrap subscriber wrapping and restored handler context.
- [x] T07 Implement bootstrap wrapping with `outbox.SubscriberWithMiddleware` before `eventrouter.New(sub)`.
- [x] T08 Extend RabbitMQ / integration tests to prove end-to-end survival of `trace_id`, `request_id`, and `correlation_id`.
- [x] T09 Add or adjust logging tests if consumer logs still do not surface restored context strongly enough for `CID-01` acceptance.
- [x] T10 Implement the minimum logging changes required by T09.
- [x] T11 Run focused package tests and fix failures.
- [x] T12 Run `go build ./...` and `go test ./... -count=1`.
- [x] T13 Update public docs and backlog status notes.
- [x] T14 Commit, push, and create PR against `develop`.
- [x] T15 Launch six-seat review and aggregate findings.
- [x] T16 Read PR comments and CI state.
- [x] T17 Use the fix flow to repair in-scope `C1` and `C2` findings, then re-run validation.

## Dependencies

| Task | Depends On |
|------|------------|
| T02 | T01 |
| T03 | T02 |
| T04 | T03 |
| T05 | T04 |
| T06 | T03 |
| T07 | T06 |
| T08 | T05, T07 |
| T09 | T08 |
| T10 | T09 |
| T11 | T05, T07, T08, T10 |
| T12 | T11 |
| T13 | T12 |
| T14 | T13 |
| T15 | T14 |
| T16 | T15 |
| T17 | T16 |

## Notes

- `T02`, `T04`, `T06`, and `T09` are mandatory TDD gates: tests must fail before implementation.
- `T09` and `T10` are conditional. If `T08` already proves `CID-01` acceptance through existing log fields and context visibility, keep logging changes minimal.
- No task may introduce `TRACE-PROP-01` scope.

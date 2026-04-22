# Task Dependency Analysis — 217 Trace Metadata Bridge

## Critical Path

`T02 -> T03 -> T04 -> T05 -> T06 -> T07 -> T08 -> T11 -> T12 -> T14 -> T15 -> T16 -> T17`

## Dependency Notes

- The shared helper (`T03`) is the base dependency for both publish-side and consume-side behavior.
- Publish-side tests and implementation (`T04`, `T05`) must land before the full-chain integration test can assert automatic metadata injection.
- Consume-side wrapping (`T06`, `T07`) must land before we can claim `CID-01` is complete.
- Logging work is intentionally conditional so the branch does not expand unless the tests show a real acceptance gap.

## Suggested Execution Batches

### Batch A — Planning and Shared Helper

- T01
- T02
- T03

### Batch B — Publish Side

- T04
- T05

### Batch C — Consume Side

- T06
- T07
- T08

### Batch D — Validation and Closeout

- T09
- T10
- T11
- T12
- T13
- T14
- T15
- T16
- T17

## Main Risks

| Risk | Probability | Impact |
|------|-------------|--------|
| Hidden behavior in existing RabbitMQ tests makes the consumer change fail in multiple places | Medium | Medium |
| Logging acceptance is underspecified and forces late-cycle churn | Medium | Low |
| README/doc updates drift from implemented scope | Low | Low |

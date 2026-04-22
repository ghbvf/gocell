# PR Plan — 217 Trace Metadata Bridge

| PR | Scope | Tasks | Depends | Verify | Branch |
|----|-------|-------|---------|--------|--------|
| PR-1 | CID-01 + META-BRIDGE-01 bridge hardening | T01-T17 | none | `go test ./kernel/outbox ./adapters/postgres ./runtime/bootstrap ./adapters/rabbitmq ./tests/integration -count=1 && go build ./... && go test ./... -count=1` | `fix/217-trace-metadata-bridge` |

## Rationale

- The work is tightly coupled around one small abstraction seam and one acceptance story.
- Splitting into multiple PRs would add review overhead without reducing merge risk.
- Six-seat review will run on the single integrated PR after implementation.

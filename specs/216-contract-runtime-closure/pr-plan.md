# PR Plan: Contract Runtime Closure

## Branch and Worktree

- Implementation branch: `fix/216-contract-runtime-closure`
- Implementation worktree: `worktrees/216-contract-runtime-closure`

## PR Strategy

Use a single PR for this batch unless implementation shows the metadata foundation must be isolated. The task sequence is wave-based, but the default delivery target is one PR because the feature value only closes when metadata, provider-driven verification, runtime fix, and docs all align.

## Planned PR

| PR | Scope | Tasks | Depends | Verify | Branch |
|----|-------|-------|---------|--------|--------|
| PR-1 | Contract runtime closure | T01-T44 | none | `go test ./... && go build ./...` | `fix/216-contract-runtime-closure` |

## PR Body Checklist

- [ ] Explain why `endpoints.http` was added and how backward compatibility is preserved.
- [ ] Explain the TDD sequence used for the migrated behaviors.
- [ ] Explain why `outbox.Entry.ID` remains the canonical event identity.
- [ ] Call out that broker-header redesign is intentionally out of scope.
- [ ] Summarize demo vs durable doc/journey changes.

## Post-Implementation Review Flow

After the PR is created:

1. Launch six independent review benches.
2. Require each bench to produce either findings or an explicit “no findings” result.
3. Aggregate by root cause.
4. Fix blocking findings in the same worktree.
5. Re-run focused verification before final merge readiness.
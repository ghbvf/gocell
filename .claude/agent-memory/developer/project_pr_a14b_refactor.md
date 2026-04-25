---
name: PR-A14b B2-B6 refactor completion state
description: All B2-B6 tasks completed for branch 526-pr-a14b-route-group; build passing, lint clean, tests passing
type: project
---

B2-B6 refactor on branch `526-pr-a14b-route-group` completed in sessions spanning multiple context windows.

**B2 (health fallback + HealthListener enforcement)**: Done. `validateHTTPListenerConfigs` rejects metrics handler without HealthListener. `phase5CollectRouteGroups` has partial health fallback (remaps HealthListener routes to PrimaryListener when no HealthListener declared). All cmd/corebundle tests inject HealthListener.

**B3 (WithAuthDiscovery → PolicyJWTFromAssembly)**: Done. `PolicyJWTFromAssembly(asm)` returns `bootstrap.Option` (not `cell.Policy`), sets `b.authDiscovery=true`. `WithAuthDiscovery` kept (deprecated, not called from production code). All non-test call sites migrated.

**B4 (per-server shutGrace)**: Done. `boundServer.shutGrace` field added; propagated from `listenerConfig.shutGrace`; `shutdownAllServers` uses per-server `context.WithTimeout(Background, bs.shutGrace)` when `shutGrace > 0`. Phase0 validates `shutGrace >= 0`. `WithListenerShutdownGrace` was already in `listener.go`.

**B5 (FinalizeAuth warn + runtime-api.md)**: Done. `warnNoAuthVerifier` helper in `router.go` emits two `slog.Warn` when auth declarations compiled but no JWT verifier: one general, one specific to `Public:true`. `runtime-api.md` updated to use `PolicyJWTFromAssembly(asm)` and document `Public:true` warning semantics.

**B6 (4 new tests)**: Done.
- `TestBootstrap_Phase5_FinalizeFailure_OnInternalListener` — bootstrap_test.go
- `TestBootstrap_Phase5_FinalizeFailure_OnHealthListener` — bootstrap_test.go
- `TestTripleListener_MidBindFailure_RollsBackEarlierBindings` — dual_listener_test.go (sandbox-skip)
- `TestPolicyVerboseToken_QueryParamBoundary` — policy_test.go

Also added `TestWithListenerShutdownGrace_NegativeRejectsAtPhase0` for B4.

**Status**: All builds pass. `golangci-lint ./runtime/bootstrap/... ./runtime/http/router/...` = 0 issues.
Pending: C (verification with full integration test run) and D (commit+push) — requires `dangerouslyDisableSandbox: true`.

**Why**: B2-B6 are part of the PR-A14b merge+refactor for the 526-pr-a14b-route-group worktree.
**How to apply**: If resuming this task, B2-B6 are complete. Next step is C: run the CI integration test commands locally and then commit+push.

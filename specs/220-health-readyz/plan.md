# Plan — 220 Health/Readyz

## Goal

Complete the Batch 6A `Health/Readyz 体系` backlog item by separating liveness from readiness, making default `/readyz` output safe, and wiring runtime readiness for config watching and event routing into bootstrap.

## In Scope

1. `/healthz` becomes liveness-only.
2. `/readyz` becomes aggregate-only by default.
3. `/readyz?verbose` returns detailed readiness information for cells and dependencies.
4. `runtime/config.Watcher` exposes readiness state and no longer silently disappears from startup.
5. `runtime/eventrouter.Router` exposes readiness state suitable for bootstrap wiring.
6. Bootstrap wires watcher and event router readiness into the root readiness endpoint.
7. Focused docs updates for the new `?verbose` behavior.

## Out Of Scope

1. New admin listener or a multi-port infrastructure server split.
2. Metrics endpoint isolation and broader infra exposure policy.
3. New adapter constructors or composition roots for PostgreSQL and Redis beyond existing `WithHealthChecker` wiring.
4. Config reload debounce, observedGeneration, or watcher metrics work tracked in adjacent backlog items.
5. API versioning changes. These are infra endpoints, not versioned business APIs.

## Worktree

- Root repo: `/Users/shengming/Documents/code/gocell`
- Implementation worktree: `worktrees/220-health-readyz`
- Branch: `fix/220-health-readyz`
- Base: `origin/develop`

## Design Decisions

### 1. Endpoint Semantics

1. `GET /healthz` returns only liveness status.
2. `GET /readyz` returns only aggregate readiness status.
3. `GET /readyz?verbose=1|true` returns:
   - `status`
   - `cells`
   - `dependencies`

### 2. Readiness Composition

Readiness is computed from three sources:

1. Cell health from `Assembly.Health()`.
2. Named dependency checkers registered via bootstrap.
3. Runtime subsystem checkers created by bootstrap for:
   - config watcher
   - event router

### 3. Watcher Failure Policy

1. If `WithConfig(...)` is used and `config.NewWatcher(...)` fails, startup returns an error.
2. Once created, the watcher contributes a readiness checker.
3. The watcher checker is healthy only after the watcher loop has started and until it has been closed.

### 4. Event Router Health Policy

1. If there are no registered event handlers, no event-router readiness dependency is exposed.
2. If handlers exist, bootstrap registers an `eventrouter` readiness checker.
3. The checker is unhealthy before the router reaches running state and after the router records a terminal runtime failure.

## TDD Order

1. Write failing `runtime/http/health` tests for endpoint semantics and verbose output.
2. Implement the health handler changes.
3. Write failing `runtime/config/watcher` tests for watcher readiness state.
4. Implement watcher health surface.
5. Write failing `runtime/eventrouter` tests for router readiness state.
6. Implement event router health surface.
7. Write failing `runtime/bootstrap` tests for watcher fail-fast and readiness wiring.
8. Implement bootstrap wiring.
9. Update docs only after code behavior is stable.

## Phases

### Phase 1 — Health Handler Semantics

Targets:

1. `src/runtime/http/health/health.go`
2. `src/runtime/http/health/health_test.go`
3. `src/runtime/http/router/router_test.go`

Required outcomes:

1. `/healthz` no longer mirrors readiness.
2. `/readyz` defaults to aggregate status only.
3. `?verbose` exposes existing detail maps without changing default output.

### Phase 2 — Runtime Subsystem Health Surfaces

Targets:

1. `src/runtime/config/watcher.go`
2. `src/runtime/config/watcher_test.go`
3. `src/runtime/eventrouter/router.go`
4. `src/runtime/eventrouter/router_test.go`

Required outcomes:

1. Watcher exposes a reusable readiness check.
2. Event router exposes a reusable readiness check.
3. Both packages have focused tests that fail first.

### Phase 3 — Bootstrap Wiring

Targets:

1. `src/runtime/bootstrap/bootstrap.go`
2. `src/runtime/bootstrap/bootstrap_test.go`

Required outcomes:

1. Config watcher init failure aborts startup.
2. Config watcher readiness is visible under verbose readyz output.
3. Event router readiness is visible when subscriptions exist.
4. Existing external health checker behavior still works.

### Phase 4 — Docs And Verification

Targets:

1. `src/templates/runbook.md`
2. Example READMEs if needed for explicit verbose guidance

Required outcomes:

1. Docs match actual `readyz` behavior.
2. Verification includes focused tests, full build, and full test run.

## Verification Commands

Focused loop:

```bash
go test ./runtime/http/health ./runtime/config ./runtime/eventrouter ./runtime/bootstrap -count=1
```

After implementation:

```bash
go test ./runtime/http/health ./runtime/config ./runtime/eventrouter ./runtime/bootstrap -count=1
go build ./...
go test ./... -count=1
```

## Review Plan After Implementation

1. Create the PR from `fix/220-health-readyz` to `develop`.
2. Launch six independent review benches:
   - architecture
   - security
   - testing/regression
   - ops/deployment
   - maintainability/DX
   - product/developer-visible experience
3. Aggregate findings by root cause.
4. Use the fix flow for in-scope `C1` and `C2` review findings.

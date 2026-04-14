# Research — 220 Health/Readyz

## Upstream References

### Kubernetes

Checked:

1. `https://kubernetes.io/docs/reference/using-api/health-checks/`
2. `staging/src/k8s.io/apiserver/pkg/server/healthz/healthz.go`
3. `test/integration/apiserver/health_handlers_test.go`

Observed patterns:

1. Liveness and readiness are separate concerns: `/livez` and `/readyz`.
2. Default probe output is aggregate status; details are opt-in through `?verbose`.
3. Detailed check names are intended for operator diagnostics, not the default public shape.

Implications for GoCell:

1. `/healthz` should be liveness-only, not a second readiness endpoint.
2. `/readyz` should default to aggregate status.
3. Per-cell and per-dependency detail should move behind `?verbose`.

### Kratos

Checked:

1. `transport/grpc/server.go`
2. `transport/http/server.go`
3. `google.golang.org/grpc/health` integration via Kratos server setup

Observed patterns:

1. Health is registered as infrastructure capability at server/bootstrap level.
2. Same-listener deployment is acceptable when endpoint semantics stay minimal.
3. Health state is tied to service lifecycle transitions, not only business handlers.

Implications for GoCell:

1. Bootstrap should own readiness composition for runtime subsystems.
2. We do not need a second listener in this batch if `/readyz` no longer leaks topology by default.
3. Runtime subsystems such as event routing should participate in readiness.

### go-micro

Checked:

1. `config/default.go`
2. `config/loader/memory/memory.go`
3. `config/source/file/watcher.go`
4. `health/health.go`

Observed patterns:

1. Config watching is a runtime subsystem with explicit health semantics.
2. Watcher failure is operationally meaningful and should surface through readiness.
3. Hot-reload loops may degrade and retry, but operator-visible health remains critical.

Implications for GoCell:

1. Config watcher creation must no longer silently degrade startup.
2. A running watcher must expose readiness state.
3. Readiness should reflect config reload infrastructure, not only cells and external adapters.

## Current GoCell Gaps

1. `runtime/http/health/health.go` uses `Assembly.Health()` in both `/healthz` and `/readyz`, so liveness and readiness are not actually separated.
2. `/readyz` always returns `cells` and `dependencies`, which leaks runtime topology by default.
3. `runtime/bootstrap/bootstrap.go` warns and continues when `config.NewWatcher` fails, leaving reload unavailable while readiness still reports healthy.
4. `runtime/eventrouter.Router` only exposes a startup signal and does not provide a reusable runtime readiness surface.
5. `src/templates/runbook.md` already documents `/readyz?verbose`, but the implementation does not support it yet.

## Decisions For This Branch

1. `/healthz` becomes pure liveness: once the process is serving, it returns aggregate live status only and does not enumerate cells or dependencies.
2. `/readyz` becomes aggregate-only by default: `{"status":"healthy|unhealthy"}`.
3. `/readyz?verbose=1` and `/readyz?verbose=true` expose detailed `cells` and `dependencies` maps.
4. Bootstrap fails fast when a config watcher is requested but cannot be created.
5. Config watcher and event router are both surfaced as readiness dependencies.
6. Existing `WithHealthChecker` remains the bootstrap integration point for external adapters such as PostgreSQL, Redis, and RabbitMQ.

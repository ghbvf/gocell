# Migration — 220 Health/Readyz

## Breaking Behavior Changes

This branch intentionally changes the observable semantics of the runtime probe endpoints.

### `/healthz`

Old expectation:

```json
{"status":"healthy","checks":{"cell-1":"healthy"}}
```

New expectation:

```json
{"status":"healthy"}
```

Meaning:

1. `/healthz` is now process liveness only — it always returns `200` if the process can serve HTTP.
2. It no longer reports per-cell readiness details.

**Note on K8s `livenessProbe`**: Previously `/healthz` returned `503` when cells had not yet started, which could cause Kubernetes to restart the pod during slow startup. The new behavior (always `200`) is correct per K8s convention — liveness should not fail during startup (use `startupProbe` or `readinessProbe` for that). If your `livenessProbe` relied on the old `503`-during-init behavior, no action is needed — the new semantics prevent restart loops.

### `/readyz`

Old expectation:

```json
{
  "status": "healthy",
  "cells": {"cell-1": "healthy"},
  "dependencies": {"rabbitmq": "healthy"}
}
```

New default expectation:

```json
{"status":"healthy"}
```

Detailed breakdown now requires:

```bash
curl -s http://{host}:{port}/readyz?verbose | jq .
```

Verbose response:

```json
{
  "status": "healthy",
  "cells": {"cell-1": "healthy"},
  "dependencies": {"config-watcher": "healthy", "eventrouter": "healthy"}
}
```

## Upgrade Checklist

1. If any probe script or dashboard reads `checks` from `/healthz`, move that logic to `/readyz?verbose`.
2. If any script parses `cells` or `dependencies` from plain `/readyz`, append `?verbose`.
3. Keep Kubernetes or load balancer readiness probes on plain `/readyz`; use `?verbose` only for operator diagnostics.
4. Treat `/healthz` as liveness only and `/readyz` as readiness only.

## Rollout Guidance

1. Deploy to a canary instance first.
2. Verify `curl /healthz` still returns `200`.
3. Verify `curl /readyz` returns aggregate status only.
4. Verify `curl /readyz?verbose` shows the expected `cells` and `dependencies` keys.

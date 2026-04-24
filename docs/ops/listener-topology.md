# GoCell Three-Listener Topology (PR-A14b)

GoCell runs three independent HTTP listeners. Each listener has a dedicated
`chi.Mux` root and a per-listener auth policy — no route leaks between ports.

## Topology Diagram

```
                        ┌───────────────────────────────────────────┐
Internet / Edge         │              primary  :8080               │
     ──────────────────▶│  /api/v1/*   (JWT AuthMiddleware)         │
                        │  404 on /internal/v1/* (hard-blocked)     │
                        └───────────────────────────────────────────┘

                        ┌───────────────────────────────────────────┐
Internal Network        │             internal  127.0.0.1:9090      │
 (VPC / pod-local) ────▶│  /internal/v1/*   (ServiceToken / mTLS)   │
                        │  404 on all other paths                   │
                        └───────────────────────────────────────────┘

                        ┌───────────────────────────────────────────┐
k8s Kubelet / Prometheus│              health  127.0.0.1:9091       │
     ──────────────────▶│  /healthz  /readyz  /metrics              │
                        │  No auth (network-level isolation only)   │
                        └───────────────────────────────────────────┘
```

## Default Addresses

| Listener | Default Bind Address | Env Override |
|----------|---------------------|--------------|
| primary  | `:8080`             | `GOCELL_HTTP_PRIMARY_ADDR` |
| internal | `127.0.0.1:9090`    | `GOCELL_HTTP_INTERNAL_ADDR` |
| health   | `127.0.0.1:9091`    | `GOCELL_HTTP_HEALTH_ADDR` |

For full variable reference see `docs/ops/env-vars.md`.

## k8s Liveness / Readiness Probe Migration

**Breaking change from PR-A14a**: `/healthz`, `/readyz`, and `/metrics` are no
longer served on the primary port (`:8080`). They are now exclusive to the
health listener (`127.0.0.1:9091`).

Update your `Pod` / `Deployment` spec:

```yaml
# Before (pre-PR-A14b)
livenessProbe:
  httpGet:
    path: /healthz
    port: 8080        # primary — no longer serves health

# After (PR-A14b)
livenessProbe:
  httpGet:
    path: /healthz
    port: 9091        # health listener

readinessProbe:
  httpGet:
    path: /readyz
    port: 9091        # health listener
```

The health listener defaults to `127.0.0.1:9091`. Kubernetes probes originate
from the kubelet on the same node, so the loopback address is reachable without
exposing the port externally. If your cluster uses a dedicated health-check
network segment, override with `GOCELL_HTTP_HEALTH_ADDR=<node-ip>:9091`.

## Prometheus Scrape Config Migration

Update your scrape job to point at the health listener port:

```yaml
# Before
- job_name: gocell
  static_configs:
    - targets: ['<pod-ip>:8080']   # was primary

# After
- job_name: gocell
  static_configs:
    - targets: ['<pod-ip>:9091']   # health listener
```

The `/metrics` endpoint on the health listener uses the same `GOCELL_METRICS_TOKEN`
bearer-token guard as before (when `GOCELL_ADAPTER_MODE=real`).

## Cell Route Declaration

Cells declare routes via `RouteGroups()` and specify which listener each group
belongs to using `cell.ListenerRef` constants:

```go
func (c *MyCell) RouteGroups() []cell.RouteGroup {
    return []cell.RouteGroup{
        {
            Listener: cell.PrimaryListener,
            Prefix:   "/api/v1/my-domain",
            Register: func(mux cell.RouteMux) {
                auth.Declare(mux, auth.RouteDecl{
                    Method:  http.MethodGet,
                    Path:    "/resource",
                    Handler: http.HandlerFunc(c.handleGet),
                    Policy:  auth.Authenticated(),
                })
            },
        },
        {
            Listener: cell.InternalListener,
            Prefix:   "/internal/v1/my-domain",
            Register: func(mux cell.RouteMux) {
                auth.Declare(mux, auth.RouteDecl{
                    Method:    http.MethodPost,
                    Path:      "/admin/action",
                    Handler:   http.HandlerFunc(c.handleAdminAction),
                    Delegated: true,
                })
            },
        },
    }
}
```

The `health` listener is reserved for framework-owned endpoints (`/healthz`,
`/readyz`, `/metrics`). Cells must not declare routes on `cell.HealthListener`.

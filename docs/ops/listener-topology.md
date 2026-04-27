# GoCell Three-Listener Topology (PR-A14b / PR262)

GoCell runs three independent HTTP listeners. Each listener has a dedicated
`chi.Mux` root and a typed `[]cell.ListenerAuth` chain — no route leaks between
ports, no string-based auth dispatch.

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

## Health-listener fallback (test/dev convenience)

When no `cell.HealthListener` is declared via `WithListener`,
`phase5CollectRouteGroups` automatically remaps the framework-owned health
groups (`/healthz`, `/readyz`, `/metrics`) onto `cell.PrimaryListener` so
single-listener bootstraps still expose health endpoints. This is intended
for tests, examples, and one-port dev runs only.

**Production deployments must declare an explicit `HealthListener`** — the
fallback path collapses port-level isolation between business traffic and
infra probes, and exposes `/metrics` on the public port. Kubernetes liveness
/ readiness probes and Prometheus scrape targets must point at the dedicated
health port (`127.0.0.1:9091` by default).

The fallback is a structural convenience, not a deployment mode. There is no
flag to opt out; declaring `WithListener(cell.HealthListener, ...)` simply
disables the remap.

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

## Helm Migration (PR-A14b)

Update `values.yaml` to expose and probe the new health port:

```yaml
# values.yaml — container port declarations
containerPorts:
  - name: http
    containerPort: 8080       # primary
    protocol: TCP
  - name: internal
    containerPort: 9090       # internal (loopback in production, only exposed on cluster network)
    protocol: TCP
  - name: health
    containerPort: 9091       # health — probes target this port
    protocol: TCP

# Liveness probe
livenessProbe:
  httpGet:
    path: /healthz
    port: health              # references containerPorts[name=health]
  initialDelaySeconds: 5
  periodSeconds: 10

# Readiness probe
readinessProbe:
  httpGet:
    path: /readyz
    port: health
  initialDelaySeconds: 5
  periodSeconds: 5
```

### Prometheus ServiceMonitor

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: gocell
  labels:
    app: gocell
spec:
  selector:
    matchLabels:
      app: gocell
  endpoints:
    - port: health            # health port — /metrics lives here
      path: /metrics
      interval: 30s
      scheme: http
```

### Prometheus PodMonitor

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: gocell
spec:
  selector:
    matchLabels:
      app: gocell
  podMetricsEndpoints:
    - port: health            # match containerPorts[name=health]
      path: /metrics
      interval: 30s
```

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
                auth.Mount(mux, auth.Route{
                    Contract: specGetResource, // wrapper.ContractSpec — Method+Path+Kind=http
                    Handler:  http.HandlerFunc(c.handleGet),
                    Policy:   auth.Authenticated(), // route-level auth.Policy — distinct from listener-level cell.ListenerAuth
                })
            },
        },
        {
            Listener: cell.InternalListener,
            Prefix:   "/internal/v1/my-domain",
            Register: func(mux cell.RouteMux) {
                auth.Mount(mux, auth.Route{
                    Contract: specAdminAction,
                    Handler:  http.HandlerFunc(c.handleAdminAction),
                    Policy:   auth.AnyRole(auth.RoleInternalAdmin),
                })
            },
        },
    }
}
```

The `health` listener is reserved for framework-owned endpoints (`/healthz`,
`/readyz`, `/metrics`). Cells must not declare routes on `cell.HealthListener`.

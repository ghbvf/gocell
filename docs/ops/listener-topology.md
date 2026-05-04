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
Kubelet / Prometheus    │              health  :9091                │
 (PodIP / Service) ────▶│  /healthz  /readyz  /metrics              │
                        │  No auth (network-level isolation only)   │
                        └───────────────────────────────────────────┘
```

## Default Addresses

| Listener | Default Bind Address | Env Override |
|----------|---------------------|--------------|
| primary  | `:8080`             | `GOCELL_HTTP_PRIMARY_ADDR` |
| internal | `127.0.0.1:9090`    | `GOCELL_HTTP_INTERNAL_ADDR` |
| health   | `127.0.0.1:9091` local/dev default; use `:9091` for PodIP/Service probes | `GOCELL_HTTP_HEALTH_ADDR` |

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
health port. In production, bind the health listener to a Pod-reachable address
such as `:9091` unless the probe/scrape runs in the same network namespace.

The fallback is a structural convenience, not a deployment mode. There is no
flag to opt out; declaring `WithListener(cell.HealthListener, ...)` simply
disables the remap.

## k8s Liveness / Readiness Probe Migration

**Breaking change from PR-A14a**: `/healthz`, `/readyz`, and `/metrics` are no
longer served on the primary port (`:8080`). They are now exclusive to the
health listener.

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

The default `127.0.0.1:9091` bind is local-only: it works for local development,
same-Pod sidecars, or exec-probe style checks that run inside the container's
network namespace. Kubernetes `httpGet` probes target the Pod IP, and Prometheus
Pod/Service scrapes also use network addresses, so those deployments must set
`GOCELL_HTTP_HEALTH_ADDR=:9091` or another Pod-reachable address. In
`GOCELL_ADAPTER_MODE=real`, corebundle rejects loopback health binds unless
`GOCELL_HTTP_HEALTH_LOCAL_ONLY=1` explicitly waives this for same-netns access.

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
            Register: func(mux cell.RouteMux) error {
                return auth.Mount(mux, auth.Route{
                    Contract: specGetResource, // wrapper.ContractSpec — Method+Path+Kind=http
                    Handler:  http.HandlerFunc(c.handleGet),
                    Policy:   auth.Authenticated(), // route-level auth.Policy — distinct from listener-level cell.ListenerAuth
                })
            },
        },
        {
            Listener: cell.InternalListener,
            Prefix:   "/internal/v1/my-domain",
            Register: func(mux cell.RouteMux) error {
                spec := wrapper.ContractSpec{
                    ID: "http.my-domain.admin-action.v1", Kind: "http", Transport: "http",
                    Method: "POST", Path: "/internal/v1/my-domain/action",
                    Clients: []string{"my-cell"}, // mirrors contract.yaml endpoints.clients
                }
                return auth.Mount(mux, auth.Route{
                    Contract: spec,
                    Handler:  http.HandlerFunc(c.handleAdminAction),
                    // No explicit Policy — auth.Mount auto-applies RequireCallerCell
                    // when Contract.Clients is non-empty.
                })
            },
        },
    }
}
```

### Internal Endpoint Caller-Cell Allowlist (A5)

Internal listener routes use service token caller-cell identity for access control
rather than role-based policies:

- Service tokens carry a 4-part format `ts:nonce:callerCell:mac`; the `callerCell`
  segment identifies the originating cell.
- `ContractSpec.Clients` declares the allowlist of permitted callerCell values,
  mirroring `contract.yaml endpoints.clients`. FMT-18 validates the two are in sync.
- When `Clients` is non-empty, `auth.Mount` automatically injects `RequireCallerCell`;
  callers not in the allowlist receive 403 — no explicit `Policy` field is needed.
- In tests, inject a service principal with `auth.TestServiceContext(callerCell)`.

The `health` listener is reserved for framework-owned endpoints (`/healthz`,
`/readyz`, `/metrics`). Cells must not declare routes on `cell.HealthListener`.

# GoCell Three-Listener Topology and Deployment (PR-A14b / PR262)

GoCell runs three independent HTTP listeners. Each listener has a dedicated
stdlib `*http.ServeMux` root and a typed `[]cell.ListenerAuth` chain — no
route leaks between ports, no string-based auth dispatch.

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
                    Clients: []string{"mycell"}, // mirrors contract.yaml endpoints.clients
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
- `ContractSpec.Clients` declares the allowlist of permitted callerCell values
  and is copied from `contract.yaml endpoints.clients` by
  `tools/codegen/contractgen`. Governance rule **FMT-31** enforces non-empty
  `endpoints.clients` on `/internal/v1/*` paths at the YAML source layer;
  `kernel/contractspec.ContractSpec.validateHTTP` provides bidirectional
  runtime defense.
- When `Clients` is non-empty, `auth.Mount` automatically injects `RequireCallerCell`;
  callers not in the allowlist receive 403 — no explicit `Policy` field is needed.
- In tests, inject a service principal with `auth.TestServiceContext(callerCell)`.

The `health` listener is reserved for framework-owned endpoints (`/healthz`,
`/readyz`, `/metrics`). Cells must not declare routes on `cell.HealthListener`.

## Deployment Boundaries

The framework enforces one Hard invariant for the `/internal/v1/*` listener. Everything else operators do at deployment time — port mapping, bind address, NetworkPolicy — is defense-in-depth and lives in [Deployment Recommendations](#deployment-recommendations).

| # | Invariant | Violation | Enforcement |
|---|-----------|-----------|-------------|
| 1 | The internal listener MUST mount `cell.AuthServiceToken` (HMAC 4-part token + nonce); the caller-cell allowlist is enforced by `ContractSpec.Clients`. The primary listener mounts JWT. | Any in-cluster client can reach `/internal/v1/*` without authentication. | archtest `SEC-FAIL-CLOSED-06` (rejects `internal + AuthNone`) + governance `FMT-31` (`contract.yaml endpoints.clients` non-empty for `/internal/v1/*`) + `auth.Mount` auto-injects `RequireCallerCell` when `Contract.Clients` is non-empty. |

This is the **only fail-closed boundary** the framework enforces for `/internal/v1/*` traffic. LB mapping, the internal-listener bind address, and NetworkPolicy are deployment-side defense-in-depth (see [Deployment Recommendations](#deployment-recommendations)); none of them are code-side enforcement mechanisms — Kubernetes resources do not live in this repository (LB), and corebundle does not reject non-loopback `GOCELL_HTTP_INTERNAL_ADDR` values (internal bind). The AI-collab charter (`.claude/rules/gocell/ai-collab.md` §"AI-rebust 三档分级") prohibits filing such Soft conventions in the invariant table; they appear as recommendations instead.

## Deployment Recommendations

These are operator-side defense-in-depth practices. They are **not enforced by code** — violations are caught (if at all) by the Hard invariant in [Deployment Boundaries](#deployment-boundaries).

| Recommendation | Consequence of violation | Code-side interception |
|----------------|--------------------------|------------------------|
| Map only the primary listener (`:8080`) through LB / Ingress. Never expose the internal or health port through `Service type=LoadBalancer` or an Ingress rule. | `/internal/v1/*` is reachable from the public internet, leaving ServiceToken as the last line of defense. | **None** — Kubernetes manifests live outside this repository; archtest cannot enforce. |
| Bind the internal listener to `127.0.0.1:9090` (default) or a VPC-only network. If `GOCELL_HTTP_INTERNAL_ADDR` is set to a non-loopback address (e.g. `:9090`, `0.0.0.0:9090`), wrap the workload in a NetworkPolicy restricting ingress to authorized caller pods. | Other pods / namespaces in the same cluster can reach `/internal/v1/*` directly, again relying on ServiceToken. | **None** — corebundle does not currently reject non-loopback internal addresses; see `cmd/corebundle/access_module.go:373` for the acknowledged misconfiguration window. The upgrade path is tracked under backlog `BOOTSTRAP-INTERNAL-LOCAL-ONLY-FAIL-FAST-01` (symmetric to `HEALTH_LOCAL_ONLY`). |
| Bind the health listener to `:9091` for Pod-reachable probes (the default `127.0.0.1:9091` only works for same-netns access). Opt in to loopback in non-test deployments by setting `GOCELL_HTTP_HEALTH_LOCAL_ONLY=1`. | Kubernetes `httpGet` liveness / readiness probes cannot reach the health endpoint. | **Already enforced** — corebundle refuses to start in `real` adapter mode when the health bind is loopback unless `GOCELL_HTTP_HEALTH_LOCAL_ONLY=1` is set. This is the template used by the `INTERNAL_LOCAL_ONLY` backlog item. |

## Docker Compose Deployment

The three published ports map to the three listeners. Only the primary port is exposed on the host network; the internal and health ports stay bound to the loopback interface so they are reachable from same-pod helpers (sidecars, exec probes) but not from other containers on the same host.

```yaml
services:
  gocell:
    image: gocell:latest
    ports:
      - "8080:8080"               # primary — exposed on the host
      - "127.0.0.1:9090:9090"     # internal — host loopback only
      - "127.0.0.1:9091:9091"     # health   — host loopback only
    environment:
      GOCELL_ADAPTER_MODE: real
      GOCELL_HTTP_PRIMARY_ADDR: ":8080"
      GOCELL_HTTP_INTERNAL_ADDR: "127.0.0.1:9090"
      GOCELL_HTTP_HEALTH_ADDR: "127.0.0.1:9091"
      GOCELL_HTTP_HEALTH_LOCAL_ONLY: "1"
      GOCELL_SERVICE_SECRET: "${GOCELL_SERVICE_SECRET}"
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:9091/healthz"]
      interval: 10s
      timeout: 2s
      retries: 3
```

When fronting the stack with a reverse proxy (nginx, Traefik, …), publish only port `8080`; the internal and health ports must never appear in proxy `upstream` blocks.

## Kubernetes NetworkPolicy

When `GOCELL_HTTP_INTERNAL_ADDR` is bound to a Pod-reachable address (any non-loopback bind), wrap the workload with a NetworkPolicy that whitelists only the caller pods declared in `ContractSpec.Clients`. The example below restricts the internal port (`9090`) to ingress from pods labeled with the calling cell ID and drops all other in-cluster traffic; the primary and health ports are unaffected.

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: gocell-internal-listener
spec:
  podSelector:
    matchLabels:
      app: gocell
  policyTypes: [Ingress]
  ingress:
    - from:
        - podSelector:
            matchLabels:
              gocell.io/caller-cell: accesscore   # match ContractSpec.Clients entries
        - podSelector:
            matchLabels:
              gocell.io/caller-cell: configcore
      ports:
        - protocol: TCP
          port: 9090
```

This is a minimal example; production deployments typically combine the per-port allowlist with a default-deny baseline NetworkPolicy that closes everything else. Kubernetes' own documentation covers default-deny patterns — this file does not duplicate that material.

## Migration from Single-Listener

Workloads built against pre-PR-A14a binaries served `/api/v1/*`, `/internal/v1/*`, `/healthz`, `/readyz`, and `/metrics` from a single port (typically `:8080`). Migration to the three-listener topology is a coordinated env / Service / probe / scrape change:

1. **Env**: set `GOCELL_HTTP_PRIMARY_ADDR`, `GOCELL_HTTP_INTERNAL_ADDR`, and `GOCELL_HTTP_HEALTH_ADDR`. Keep the primary on the same port (`:8080`) so external clients are unaffected; pick internal / health ports the workload host has free.
2. **Service / Ingress**: drop the single backend port; declare three named container ports as shown in [Helm Migration (PR-A14b)](#helm-migration-pr-a14b) and route Ingress / `Service type=LoadBalancer` traffic only to the `http` (primary) port. Internal traffic stays on a cluster-internal Service or no Service at all.
3. **Probes**: redirect `livenessProbe` / `readinessProbe` `httpGet.port` from `8080` to the health listener (`9091` typically). See [k8s Liveness / Readiness Probe Migration](#k8s-liveness--readiness-probe-migration).
4. **Scrape**: move the Prometheus scrape job target from the primary port to the health port (see [Prometheus Scrape Config Migration](#prometheus-scrape-config-migration)).
5. **Roll-out**: deploy the new binary into a canary pod first. The canary serves `/api/v1/*` on the same external endpoint, so Ingress / Service stays valid throughout the swap; the only externally observable change is that `/healthz` on `:8080` starts returning 404 — which is why the probe swap in step 3 must land before traffic shifts.

If existing Pods cannot expose new container ports during the roll-out (for example, a `hostNetwork` deployment fully shared with another component), keep the legacy fallback path on by **not** declaring a `HealthListener` — the framework remaps `/healthz`, `/readyz`, `/metrics` onto the primary listener (see [Health-listener fallback (test/dev convenience)](#health-listener-fallback-testdev-convenience)). This is intended as a transient escape hatch, not a steady state.

## Troubleshooting

Three failure modes have been observed in real deployments. Each entry links to the section that explains the underlying mechanism.

### `httpGet` probe times out against the health endpoint

**Symptom**: Kubernetes events report `Liveness probe failed: Get http://<pod-ip>:9091/healthz: dial tcp ... connection refused`, even though `/healthz` works from inside the Pod (`kubectl exec -- wget -qO- http://127.0.0.1:9091/healthz`).

**Cause**: The default health bind is `127.0.0.1:9091`, which is unreachable from the kubelet via Pod IP. Same-netns probes (exec probes, sidecars) work; `httpGet` does not.

**Fix**: Either set `GOCELL_HTTP_HEALTH_ADDR=:9091` to bind on all interfaces, or set `GOCELL_HTTP_HEALTH_LOCAL_ONLY=1` to acknowledge the loopback-only configuration (corebundle will refuse to start in `real` mode otherwise). See [Health-listener fallback (test/dev convenience)](#health-listener-fallback-testdev-convenience).

### `/internal/v1/*` requests succeed from unrelated pods

**Symptom**: A pod outside the declared `ContractSpec.Clients` allowlist can open TCP to `/internal/v1/*` endpoints; only ServiceToken validation rejects the request.

**Cause**: `GOCELL_HTTP_INTERNAL_ADDR` is set to a non-loopback address (e.g. `:9090`) and no NetworkPolicy is in place, exposing the internal port across the cluster. The framework does not reject this configuration (`cmd/corebundle/access_module.go:373`) because ServiceToken + caller-cell allowlist is the fail-closed boundary.

**Fix**: Either restore the loopback bind (`GOCELL_HTTP_INTERNAL_ADDR=127.0.0.1:9090`) or attach a NetworkPolicy as shown in [Kubernetes NetworkPolicy](#kubernetes-networkpolicy). The upgrade path that turns this into a fail-fast condition is tracked under backlog `BOOTSTRAP-INTERNAL-LOCAL-ONLY-FAIL-FAST-01`.

### Service-token authenticated callers receive 401 or 403 from `/internal/v1/*`

**Symptom**: A cell whose service token is correctly minted (HMAC + nonce verified) still receives 401 (`token rejected`) or 403 (`caller cell not allowed`) at the internal listener.

**Cause**: 401 indicates a nonce-replay or HMAC mismatch — confirm the shared secret, nonce-store availability, and clock skew. 403 indicates the caller cell is not in the target endpoint's `ContractSpec.Clients` allowlist, populated from `contract.yaml endpoints.clients` by `tools/codegen/contractgen`.

**Fix**: For 401, inspect `auth.NonceStore` readiness and verify the service-token signing secret matches. For 403, add the calling cell ID to `endpoints.clients` in the relevant `contract.yaml` and re-run codegen. Governance `FMT-31` rejects empty `endpoints.clients` on `/internal/v1/*` paths at the YAML source layer; archtest `SEC-FAIL-CLOSED-06` rejects `internal + AuthNone` listener configurations.

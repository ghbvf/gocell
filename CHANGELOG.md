# Changelog

All notable changes to GoCell are documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

### Changed (Breaking) — PR262 AUTH-POLICY-PLAN-01

- **`cell.Policy` struct and all `bootstrap.Policy*` factory functions are deleted.**
  `WithListener` third parameter changes from `cell.Policy` to `[]cell.ListenerAuth`.
  Replacement map:

  | Deleted | Replacement |
  |---------|-------------|
  | `bootstrap.PolicyJWT(v)` | `[]cell.ListenerAuth{cell.NewAuthJWT(v)}` |
  | `bootstrap.PolicyJWTFromAssembly(asm)` | `[]cell.ListenerAuth{cell.NewAuthJWTFromAssembly(asm)}` |
  | `bootstrap.PolicyServiceToken(s, r)` | `[]cell.ListenerAuth{cell.NewAuthServiceToken(s, r)}` |
  | `bootstrap.PolicyMTLS()` | `[]cell.ListenerAuth{cell.AuthMTLS{}}` |
  | `bootstrap.PolicyNone()` / `cell.Policy{}` | `nil` |
  | `bootstrap.PolicyVerboseToken(h, t)` | `cell.NewAuthVerboseToken(h, t)` (GroupAuth, not ListenerAuth) |
  | `bootstrap.PolicyStack(a, b)` | pass multiple elements in the `[]cell.ListenerAuth` slice |

- **`WithReadyzPolicy` / `WithLivezPolicy` / `WithMetricsPolicy`** renamed to
  `WithReadyzAuth` / `WithLivezAuth` / `WithMetricsAuth`; accept `cell.GroupAuth`.

- **`cell.RouteGroup.Policy cell.Policy`** field renamed to `Auth cell.GroupAuth`.

- Hard break: no migration shim, no deprecated aliases.
  `tools/archtest/auth_plan_test.go` (AUTH-PLAN-01..04) guards against regression.

- **BREAKING (logging)**: startup log field renamed from `"policy"` to `"auth"` on
  `bootstrap: HTTP listener bound`. Update your dashboard / alert rules that
  filter on `policy=jwt|mtls|...` to filter on `auth=jwt|mtls|...` instead.
  Multi-plan chains render as "+"-joined kinds (e.g. `"mtls+service-token"`).
  `AuthJWTFromAssembly.Describe()` now returns `"jwt"` (same as `AuthJWT`) so
  both paths appear under `auth=jwt` in observability.

### Changed (Breaking) — PR-A35 READYZ-POLISH

- **`GOCELL_READYZ_VERBOSE_TOKEN` is now required in every adapter mode.**
  Before PR-A35 an unset token silently left `/readyz?verbose` open; the
  startup path now refuses to boot with
  `ERR_CONTROLPLANE_VERBOSE_TOKEN_MISSING`. Upgrade paths:
  - **Production**: set `GOCELL_READYZ_VERBOSE_TOKEN` to a high-entropy
    secret. This is also the only supported path — `VERBOSE_DISABLED=1` is
    refused in `GOCELL_ADAPTER_MODE=real`.
  - **Dev / local / CI smoke**: set `GOCELL_READYZ_VERBOSE_DISABLED=1`
    to waive the endpoint, or set `GOCELL_READYZ_VERBOSE_TOKEN` to any
    non-empty value. `cmd/corebundle` reads its configuration via
    `os.Getenv` only (no `.env` autoloader), so the supported dev
    bring-up is one of:

    ```sh
    # Inline (quick smoke):
    GOCELL_READYZ_VERBOSE_DISABLED=1 \
      GOCELL_JWT_ISSUER=gocell-dev GOCELL_JWT_AUDIENCE=gocell-dev \
      go run ./cmd/corebundle

    # Via .env (recommended for repeated runs):
    cp .env.example .env
    set -a; source .env; set +a
    go run ./cmd/corebundle
    ```

    `.env.example` ships non-empty placeholders for every required env
    var so the second form works without further editing. The repo-root
    `docker-compose.yml` is infra-only (postgres/redis/rabbitmq/minio)
    and runs no GoCell app service, so no app-side env wiring lives
    there. Note: the placeholder `GOCELL_READYZ_VERBOSE_TOKEN` value is
    rejected verbatim in `GOCELL_ADAPTER_MODE=real` with
    `ERR_CONTROLPLANE_VERBOSE_TOKEN_SAMPLE` — production must mint its
    own high-entropy secret.
- `/readyz?verbose` with a missing or mismatched `X-Readyz-Token` now
  returns `401 ERR_READYZ_VERBOSE_DENIED` (previously: silent 200
  downgrade). Kubernetes `readinessProbe` **must** use the bare
  `/readyz` — any existing probe pointed at `/readyz?verbose` will mark
  healthy pods as NotReady.
- `/healthz` and `/readyz` responses now use the project-standard envelope
  (`{"data": {...}}` / `{"error": {"code","message","details"}}`). New
  error codes `ERR_READYZ_UNHEALTHY` (503) and `ERR_READYZ_SHUTTING_DOWN`
  (503). Consumers that parsed the body directly for `status` must walk
  through `data` (success) or `error.details` (failure).

### Added
- **PR-A14b three-listener topology with declarative RouteGroups** — `runtime/bootstrap` runs N `http.Server` instances driven by `cell.ListenerRef` (`PrimaryListener` `:8080` for `/api/v1/*`, `InternalListener` `127.0.0.1:9090` for `/internal/v1/*`, `HealthListener` `127.0.0.1:9091` for `/healthz` `/readyz` `/metrics`). Each listener owns an independent `*router.Router`; the primary listener physically 404s `/internal/v1/*`. Cells declare routes via `RouteGroupContributor.RouteGroups()` and Bootstrap mounts them at phase5. `ref: go-kratos/kratos app.go L95-122 errgroup goroutine-pair; ory/kratos cmd/daemon/serve.go named public/admin constructors; kubernetes/apiserver pkg/server/secure_serving.go pre-bind listener`.
- `bootstrap.WithListener(ref cell.ListenerRef, addr string, authChain []cell.ListenerAuth, opts ...ListenerOption)` — single declarative listener option carrying ref, address, and the typed auth chain. `WithListenerNet(ln)` injects a pre-bound `net.Listener` for tests.
- `kernel/cell.LifecycleContributor` interface + `LifecycleHook` struct. Bootstrap auto-discovers lifecycle hooks at phase3b (mirror of `HealthContributor` discovery at phase5), eliminating composition-root boilerplate. `ref: github.com/uber-go/fx internal/lifecycle/lifecycle.go`.
- `kernel/cell.ResolveEmitter` / `EmitterConfig` / `EmitterOutcome` consolidating the durable/demo outbox emitter selection logic that was duplicated across accesscore / configcore / auditcore (~120 LOC removed).
- `cells/accesscore/initialadmin.Lifecycle` promoting first-run admin bootstrap from internal plumbing to a first-class `cell.LifecycleContributor` Hook. `OnStart` launches the cleaner in a background goroutine (Cleaner.Start blocks on ctx.Done until TTL expires); `OnStop` cancels it.
- `VAULT_NAMESPACE` env var support — applied via `client.SetNamespace` before any Vault I/O so Login + datakey + decrypt + key reads + rotate carry the `X-Vault-Namespace` header. Required for HCP Vault and Vault Enterprise multi-tenant deployments. (PR-A18 / A15)
- `adapters/rabbitmq.Connection` implements `lifecycle.ManagedResource` — wire via `bootstrap.WithManagedResource(conn)` to register the `rabbitmq_ready` /readyz probe automatically. (PR-A18 / RMQ-STATUS-01)

### Changed
- `cells/accesscore/cell.go` split into `cell.go` (shell, ~175 LOC) + `cell_init.go` (Init / mode-resolve / LifecycleHooks) + `cell_routes.go` (HTTP + event registration). Down from 625 LOC single file.
- `cells/accesscore/internal/initialadmin/` promoted out of `internal/` to `cells/accesscore/initialadmin/` (public subpackage, consistent with `cells/accesscore/slices/`). Consumers import it directly.
- `cells/configcore/cell.go` + `cells/auditcore/cell.go` adopt `cell.ResolveEmitter`; their private `resolveOutboxDeps` / `resolveDemoEmitter` / `isNoopDep` helpers removed.
- `adapters/vault` envelope encrypt path now uses `transit/datakey/plaintext` for single-RTT server-side (HSM-backed) DEK generation; the prior client-side `crypto/rand` DEK + `transit/encrypt` wrap path is removed. **Vault role policy must grant `update` on `transit/datakey/plaintext/<keyname>`** (and may drop `transit/encrypt/<keyname>`) — see `docs/ops/env-vars.md` for the full policy snippet. Decrypt path is unchanged; old EDKs continue to decrypt. (PR-A18 / A16)
- `adapters/vault.TransitKeyProvider` is now lock-free: the prior `sync.RWMutex` is removed and replaced by an `atomic.Int64` version cache. `Current()` is served from cache (zero Vault round-trip on hot path); `Rotate()` runs without holding any lock and self-heals from a failed post-rotate read. (PR-A18 / A18)
- RabbitMQ /readyz probe name changed from `"rabbitmq"` to `"rabbitmq_ready"` for parity with sibling adapter probe names (`"vault_transit_ready"`, etc.). Operator dashboards and alert rules consuming `/readyz?verbose` dependencies must be updated. (PR-A18 / RMQ-STATUS-01)

### Removed (breaking)
- **PR-A14a/A14b listener / guard API** — the historical single-listener and prefix-guard surface is gone. Current canonical surface is `bootstrap.WithListener(ref, addr, authChain, opts...)` per ref (see "Added" above and the Migration section). Removed names:
  - `bootstrap.WithHTTPAddr` (single global addr) — pass per-ref addresses through `WithListener`.
  - `bootstrap.WithInternalEndpointGuard(prefix, guard)` — internal mux is its own listener; attach guards via the listener's `authChain` (e.g. `[]cell.ListenerAuth{cell.NewAuthServiceToken(store, ring)}`).
  - `router.WithInternalPathPrefixGuard` + `auth.WithDelegatedMatcher` — physical mux split makes in-band JWT bypass unnecessary.
  - `router.Router.Handler()` — each `*router.Router` is now per-listener; obtain it from `bootstrap` wiring rather than via a global handler.
  - `auth.Route.Delegated` runtime JWT-bypass behaviour. The `Delegated bool` field remains; `FinalizeAuth` now asserts `Delegated=true ⇔ path begins with /internal/v1/` and the route is mounted on `InternalListener`. The old `auth.RouteDecl` shim has been removed; use `auth.Mount(mux, auth.Route{Contract: ..., ...})`.
- `runtime/bootstrap.BrokerHealthChecker` interface + `WithBrokerHealth` Option + `isNilBrokerHealthChecker` helper. Compose RabbitMQ readiness via `bootstrap.WithManagedResource(conn)` instead. (PR-A18 / RMQ-STATUS-01)
- `accesscore.WithBootstrapWorkerSink` — Bootstrap phase3b auto-discovery replaces sink plumbing. Remove both the `WithBootstrapWorkerSink(...)` call and the paired `bootstrap.WithWorkers(worker.Lazy())` wiring.
- `accesscore.InitialAdminOption` type alias + all `WithBootstrap{Username,CredentialPath,TTL,PasswordHasher}` options — moved to `cells/accesscore/initialadmin.With{Username,CredentialPath,TTL,PasswordHasher}`.
- `accesscore.PasswordHasher` / `accesscore.BcryptHasher` type aliases — use `initialadmin.PasswordHasher` / `initialadmin.BcryptHasher` directly.
- `cmd/corebundle.adminBootstrapWorkerOpts` helper — no longer needed with auto-discovery.
- `AccessCore.runInitialAdminBootstrap` private method — logic migrated to `initialadmin.Lifecycle.start` (runs during `bootstrap.Lifecycle.Start`, not `Cell.Init`).
- Residual `runInTx` local wrappers in `cells/accesscore/slices/identitymanage` + `rbacassign` — obsoleted by `persistence.RunnerOrNoop` boundary injection.

### Migration

```go
// Before
lazy := worker.Lazy()
accessOpts := append(baseOpts,
    accesscore.WithInitialAdminBootstrap(
        accesscore.WithBootstrapCredentialPath(path),
        accesscore.WithBootstrapPasswordHasher(hasher),
    ),
    accesscore.WithBootstrapWorkerSink(func(w worker.Worker) { _ = lazy.Set(w) }),
)
accessCore := accesscore.NewAccessCore(accessOpts...)
app := bootstrap.New(bootstrap.WithAssembly(asm), bootstrap.WithWorkers(lazy))

// After
import "github.com/ghbvf/gocell/cells/accesscore/initialadmin"

accessOpts := append(baseOpts, accesscore.WithInitialAdminBootstrap(
    initialadmin.WithCredentialPath(path),
    initialadmin.WithPasswordHasher(hasher),
))
accessCore := accesscore.NewAccessCore(accessOpts...)
app := bootstrap.New(bootstrap.WithAssembly(asm)) // phase3b auto-discovers
```

**PR-A14b/PR262 listener migration:**

```go
// Before (pre-PR-A14a — single global addr + prefix guard)
app := bootstrap.New(
    bootstrap.WithAssembly(asm),
    bootstrap.WithHTTPAddr(":8080"),
    bootstrap.WithListener(ln),
    bootstrap.WithInternalEndpointGuard("/internal/v1/", serviceTokenMW),
)

// After — one WithListener per ref; auth chain is typed (PR262):
//   primary    serves /api/v1/* with JWT verifier discovered from the assembly
//   internal   serves /internal/v1/* with mTLS + service-token chain
//   health     serves /healthz /readyz /metrics on loopback (no auth)
app := bootstrap.New(
    bootstrap.WithAssembly(asm),
    bootstrap.WithListener(cell.PrimaryListener, ":8080",
        []cell.ListenerAuth{cell.NewAuthJWTFromAssembly(asm)}),
    bootstrap.WithListener(cell.InternalListener, "127.0.0.1:9090",
        []cell.ListenerAuth{
            cell.AuthMTLS{},
            cell.NewAuthServiceToken(nonceStore, ring),
        },
        bootstrap.WithListenerTLS(tlsCfg)),
    bootstrap.WithListener(cell.HealthListener, "127.0.0.1:9091", nil),
)
```

For tests, inject a pre-bound listener via `bootstrap.WithListenerNet(ln)` as a `WithListener` option.

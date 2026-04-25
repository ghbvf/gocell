# Changelog

All notable changes to GoCell are documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

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
- **PR-A14a dual HTTP listener** — `runtime/bootstrap` now runs two `http.Server` instances: `primary` (default `:8080`, serves `/api/v1/*` + `/healthz` + `/readyz` + `/metrics`) and `internal` (default `127.0.0.1:9090`, serves `/internal/v1/*` only). `runtime/http/router.Router` physically splits routes across `publicMux` + `internalMux` via prefix dispatch; the primary listener explicitly 404s `/internal/v1/*` so the internal prefix never leaks through public AuthMiddleware. `ref: go-kratos/kratos app.go L95-122 errgroup goroutine-pair; ory/kratos cmd/daemon/serve.go named public/admin constructors; kubernetes/apiserver pkg/server/secure_serving.go pre-bind listener`.
- `bootstrap.WithHTTPPrimaryAddr` / `WithHTTPInternalAddr` — new option pair replacing `WithHTTPAddr`.
- `bootstrap.WithPrimaryListener` / `WithInternalListener` — new test-injection options replacing `WithListener`.
- `bootstrap.WithInternalMiddleware(mw ...)` — variadic middleware option for the internal mux chain (service-token / mTLS). Replaces `WithInternalEndpointGuard(prefix, guard)`.
- `router.Router.PublicHandler()` / `InternalHandler()` — explicit per-listener `http.Handler` accessors for Bootstrap wiring.
- Env vars `GOCELL_HTTP_PRIMARY_ADDR` + `GOCELL_HTTP_INTERNAL_ADDR` (consumed by `cmd/corebundle`).
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
- **PR-A14a listener / guard API**:
  - `bootstrap.WithHTTPAddr` → use `WithHTTPPrimaryAddr` + `WithHTTPInternalAddr`.
  - `bootstrap.WithListener` → use `WithPrimaryListener` + `WithInternalListener`.
  - `bootstrap.WithInternalEndpointGuard(prefix, guard)` → `WithInternalMiddleware(mw)` (no prefix parameter; routes dispatch by pattern).
  - `router.WithInternalPathPrefixGuard` + `auth.WithDelegatedMatcher` — deleted; physical mux split makes in-band JWT bypass unnecessary.
  - `router.Router.Handler()` — deleted; use `PublicHandler()` or `InternalHandler()`.
  - `auth.Route.Delegated` field semantic change: still a `bool` with the same field name, but the runtime JWT-bypass behaviour is gone. `FinalizeAuth` now asserts `Delegated=true ⇔ path begins with /internal/v1/` at startup; callers must set the flag consistently with the path prefix or startup fails. The old `auth.RouteDecl` shim has been removed; use `auth.Mount(mux, auth.Route{Contract: ..., ...})`.
  - PR-A32 F3-CLOSURE SELECTOR-GUARD-REMOVE-01 absorbed: `cmd/corebundle/bundle.go` no longer wires a `WithInternalEndpointGuard` transitional guard.
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

**PR-A14a dual-listener migration:**

```go
// Before
app := bootstrap.New(
    bootstrap.WithAssembly(asm),
    bootstrap.WithHTTPAddr(":8080"),
    bootstrap.WithListener(ln),
    bootstrap.WithInternalEndpointGuard("/internal/v1/", serviceTokenMW),
)

// After — primary listener serves /api/v1/* + infra; internal listener
// serves /internal/v1/* only. Service-token / mTLS attaches to internalMux
// directly (no prefix parameter; routes dispatch by pattern).
app := bootstrap.New(
    bootstrap.WithAssembly(asm),
    bootstrap.WithHTTPPrimaryAddr(":8080"),
    bootstrap.WithHTTPInternalAddr("127.0.0.1:9090"), // loopback by default; bind VPC in prod
    bootstrap.WithPrimaryListener(primaryLn),
    bootstrap.WithInternalListener(internalLn),
    bootstrap.WithInternalMiddleware(serviceTokenMW),
)
```

Operators: set `GOCELL_HTTP_PRIMARY_ADDR` + `GOCELL_HTTP_INTERNAL_ADDR` env vars. The pre-PR-A14a `GOCELL_HTTP_ADDR` var is no longer consumed; `cmd/corebundle` emits `slog.Warn` at startup if it is set while the new vars are not.

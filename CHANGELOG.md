# Changelog

All notable changes to GoCell are documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

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

### Changed
- `cells/accesscore/cell.go` split into `cell.go` (shell, ~175 LOC) + `cell_init.go` (Init / mode-resolve / LifecycleHooks) + `cell_routes.go` (HTTP + event registration). Down from 625 LOC single file.
- `cells/accesscore/internal/initialadmin/` promoted out of `internal/` to `cells/accesscore/initialadmin/` (public subpackage, consistent with `cells/accesscore/slices/`). Consumers import it directly.
- `cells/configcore/cell.go` + `cells/auditcore/cell.go` adopt `cell.ResolveEmitter`; their private `resolveOutboxDeps` / `resolveDemoEmitter` / `isNoopDep` helpers removed.

### Removed (breaking)
- **PR-A14a listener / guard API**:
  - `bootstrap.WithHTTPAddr` → use `WithHTTPPrimaryAddr` + `WithHTTPInternalAddr`.
  - `bootstrap.WithListener` → use `WithPrimaryListener` + `WithInternalListener`.
  - `bootstrap.WithInternalEndpointGuard(prefix, guard)` → `WithInternalMiddleware(mw)` (no prefix parameter; routes dispatch by pattern).
  - `router.WithInternalPathPrefixGuard` + `auth.WithDelegatedMatcher` — deleted; physical mux split makes in-band JWT bypass unnecessary.
  - `router.Router.Handler()` — deleted; use `PublicHandler()` or `InternalHandler()`.
  - `auth.RouteDecl.Delegated` field semantic change (NOT a rename): still a `bool` with the same field name, but the runtime JWT-bypass behaviour is gone. `FinalizeAuth` now asserts `Delegated=true ⇔ path begins with /internal/v1/` at startup; callers must set the flag consistently with the path prefix or startup fails.
  - PR-A32 F3-CLOSURE SELECTOR-GUARD-REMOVE-01 absorbed: `cmd/corebundle/bundle.go` no longer wires a `WithInternalEndpointGuard` transitional guard.
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

# Changelog

All notable changes to GoCell are documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

### Added
- `kernel/cell.LifecycleContributor` interface + `LifecycleHook` struct. Bootstrap auto-discovers lifecycle hooks at phase3b (mirror of `HealthContributor` discovery at phase5), eliminating composition-root boilerplate. `ref: github.com/uber-go/fx internal/lifecycle/lifecycle.go`.
- `kernel/cell.ResolveEmitter` / `EmitterConfig` / `EmitterOutcome` consolidating the durable/demo outbox emitter selection logic that was duplicated across accesscore / configcore / auditcore (~120 LOC removed).
- `cells/accesscore/initialadmin.Lifecycle` promoting first-run admin bootstrap from internal plumbing to a first-class `cell.LifecycleContributor` Hook. `OnStart` launches the cleaner in a background goroutine (Cleaner.Start blocks on ctx.Done until TTL expires); `OnStop` cancels it.

### Changed
- `cells/accesscore/cell.go` split into `cell.go` (shell, ~175 LOC) + `cell_init.go` (Init / mode-resolve / LifecycleHooks) + `cell_routes.go` (HTTP + event registration). Down from 625 LOC single file.
- `cells/accesscore/internal/initialadmin/` promoted out of `internal/` to `cells/accesscore/initialadmin/` (public subpackage, consistent with `cells/accesscore/slices/`). Consumers import it directly.
- `cells/configcore/cell.go` + `cells/auditcore/cell.go` adopt `cell.ResolveEmitter`; their private `resolveOutboxDeps` / `resolveDemoEmitter` / `isNoopDep` helpers removed.

### Removed (breaking)
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

# Changelog

All notable changes to GoCell are documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

### Breaking Changes

- **readyz wire**: `/readyz?verbose` dependency names for 3 outbox relay probes
  renamed from hyphen to underscore form (typed `ProbeName` funnel — see PR #1187):
  - `outbox-relay-poll` → `outbox_relay_poll`
  - `outbox-relay-reclaim` → `outbox_relay_reclaim`
  - `outbox-relay-cleanup` → `outbox_relay_cleanup`

  Operators with hard-coded references in Prometheus rules / Grafana dashboards /
  alerting rules / startup-validation scripts must update. Recommended order:
  update monitoring config first, then roll out new binary. See ADR
  `docs/architecture/202605271100-adr-probename-sealed-funnel.md` §F1 amendment
  §3 + `docs/ops/readyz.md` "Recent breaking changes".

- **ADV-05 (dead event) reclassified `error` → `warning`** (PR for #687, M3-RULE-ENGINE):
  an active event contract with no subscribers no longer fails `gocell validate` (exit 1);
  it is now an advisory warning (exit 0), fixing the ADR-noted SeverityError
  misclassification (ADV is the advisory series). CI pipelines that relied on ADV-05
  blocking a merge must add their own gate that *fails* (non-zero) when ADV-05 is
  present, e.g.
  `! gocell validate --format=json | jq -e '.issues[] | select(.code=="ADV-05")' >/dev/null`.
  The leading `!` is required: `jq -e 'select(...)'` exits 0 on a match, so the
  bare pipeline would *pass* exactly when a dead event exists (inverted gate); the
  `!` flips it so the step blocks the merge when — and only when — ADV-05 fires.
  Verify the `.issues[]` filter against the actual `--format=json` output first
  (`gocell validate --format=json | jq .`): if the JSON schema ever changes, an
  unmatched filter makes `jq -e` exit non-zero on *every* run, silently disabling
  the gate.
  Governance rules are now a single data-driven `allRules` registry (ADR `202605041430`
  §M3, amended: Go typed-struct carrier, not YAML). `next-action` disposition and
  per-finding `metric` fields were speculative repository-convergence scaffolding
  with no consumer; they were removed from M3 and from the current M0-M4 roadmap
  after M5-HARVEST was canceled (see ADR §M3 amendment).

- **`gocell_vault_cached_key_version` removes `mount_path` and `key_name` ConstLabels** (PR for #879):
  single-process single-key deployment has label cardinality 1; the labels carried no
  information. Dashboards / alerts referencing `mount_path` or `key_name` matchers on
  this metric must drop them.

- **`gocell_vault_token_auth_healthy` semantics fix** (PR for #879):
  the gauge now defaults to 0 at construction and transitions to 1 only after the
  renewal worker starts successfully. Previously it was unconditionally set to 1
  during initTokenRenewal, which produced a false-green signal for non-renewable
  token deployments (no worker, gauge=1). Alerts that assumed "gauge stays at 1
  unless degraded" must be reviewed: replace simple `== 0` threshold rules with
  either an absent-metric rule (for static-token deployments where the gauge is
  permanently 0) or a stabilisation window such as
  `gocell_vault_token_auth_healthy == 0 unless on() (time() - process_start_time_seconds < 60)`
  to suppress the brief startup window between `NewTransitMetrics` and worker start.

- **`gocell_vault_auth_login_total` now records initial Login** (PR for #879):
  `TransitKeyProvider.authenticate` (called once at construction) now increments
  `auth_login_total{method,result,reason}` on success and failure. Previously only
  the renewal worker's re-auth loop recorded outcomes, so startup auth success was
  invisible. Dashboards counting auth attempts will see one additional sample per
  process startup.

- **Removed `TransitKeyProvider.Metrics()` / `RenewalMetrics()` / `CacheVersionMetrics()` accessor methods** (PR for #879):
  metric ownership moved to `*vault.TransitMetrics` passed into `NewTransitKeyProvider`
  / `NewTransitKeyProviderFromEnv` as a required positional parameter. Callers that
  previously fetched collectors via the provider must now hold and inspect the
  `*TransitMetrics` they constructed via `vault.NewTransitMetrics(reg)`. The
  composition-root `cmd/corebundle` wires this through `SharedDeps.VaultTransitMetrics`
  (eagerly constructed in `buildSharedMetricsDeps`); the corresponding helper
  functions `replaceRegisteredCollectors` / `registerKeyProviderMetrics` /
  `keyProviderMetricCollectors` and the interfaces `renewalMetricsProvider` /
  `keyProviderMetricsProvider` were removed.

- **`kernel/cell` decompose — auth + outbox extraction + `Registry`→`Registrar` rename** (`615-g10-kernel-cell-decompose`, PR #900, close #615): the listener-auth and outbox-demo concerns left `kernel/cell` for dedicated packages; the cell registration interface was renamed for intent clarity.
  - Listener auth plans moved `kernel/cell` → `kernel/auth`: `cell.ListenerAuth` → `auth.ListenerAuth`; `cell.AuthNone` / `AuthJWT` / `AuthJWTFromAssembly` / `AuthMTLS` / `AuthServiceToken` → `auth.*`; constructors `cell.NewAuthJWT` / `NewAuthJWTFromAssembly` / `NewAuthServiceToken` → `auth.NewAuth*`; supporting types `IntentTokenVerifier` / `AssemblyRef` / `HMACKeyring` / `NonceStore` likewise moved to `auth`.
  - Test-fixture `Must*` helpers moved `kernel/cell/celltest` → `kernel/auth/authtest`: `celltest.MustAuthJWT` / `MustAuthJWTFromAssembly` / `MustAuthServiceToken` → `authtest.Must*`. Production composition roots use the error-first `auth.NewAuth*` directly.
  - Cell-internal demo tx fallback moved `kernel/cell` → `kernel/outbox`: `cell.DemoCellTxManager()` → `outbox.DemoCellTxManager()`; `cell.DemoTxRunner{}` → `outbox.DemoTxRunner{}`.
  - `cell.Registry` interface → `cell.Registrar`: `Cell.Init(ctx, reg cell.Registry)` → `Init(ctx, reg cell.Registrar)`. Listener-ref constants (`cell.PrimaryListener` / `InternalListener` / `HealthListener`) are unchanged.
  - All call sites migrated atomically; no compatibility shim.

- **Cell `With*` Option sealed marker types** (`refactor/549-cell-iface-isp-split`, PR #441): `cells/<x>/cell.go` (platform + examples) public Options no longer accept raw infra types. Composition roots MUST wrap raw infra into sealed markers before injection.
  - Signature changes:
    - `accesscore/auditcore/configcore.WithTxManager(persistence.TxRunner)` → `WithTxManager(persistence.CellTxManager)`
    - `accesscore/auditcore/configcore.WithOutboxDeps(outbox.Publisher, outbox.Writer)` → `WithOutboxDeps(outbox.CellPublisher, outbox.CellWriter)`
    - `examples/todoorder/cells/ordercell.WithTxManager(persistence.TxRunner)` → `WithTxManager(persistence.CellTxManager)`
    - `examples/todoorder/cells/ordercell.WithOutboxWriter(outbox.Writer)` → `WithOutboxWriter(outbox.CellWriter)`
    - `examples/iotdevice/cells/devicecell.WithDirectPublisher(outbox.Publisher)` → `WithDirectPublisher(outbox.CellPublisher)`
  - Migration: from composition root (`cmd/*` / `examples/<demo>/main.go` / `examples/<demo>/app.go` / `*_test.go`), wrap before calling cell `With*`:
    - `persistence.WrapForCell(txRunner)`
    - `outbox.WrapPublisherForCell(publisher)`
    - `outbox.WrapWriterForCell(writer)`
  - Cell-internal demo fallback: use `outbox.DemoCellTxManager()` (returns sealed `persistence.CellTxManager`); do NOT use `outbox.DemoTxRunner{}` directly inside cells (will not compile against the new field types). (Moved from `kernel/cell` to `kernel/outbox` in PR #900.)
  - All 6 composition-root sites + 11 test files migrated atomically; no compatibility shim.
  - **Defense in depth (Hard sealed fields + Medium archtest API surface)** per ai-robust.md §"违反不可表达":
    - **Hard (type system)**: raw infra fields and raw→`CellXxx` assignments are unexpressible at compile time — sealed marker requires the unimplementable `sealedXxx()` method only the wrapper packages provide.
    - **Medium (archtest, necessary double defense)**: type system alone cannot exhaust signature forms; `func WithBad(p interface{ outbox.Publisher })` (inline interface embed) and `import . "kernel/persistence"; WrapForCell(p)` (dot-import) compile around bare `*types.Named` / `*ast.SelectorExpr` matchers. `CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01` walks `*types.Interface` embedded types; `CELL-RAW-INFRA-WRAPPER-LOCATION-01` resolves both `*ast.SelectorExpr` and `*ast.Ident` call forms.
  - See ADR `docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md` (amends `202605101800` §D6).

- **Sweeper construction API** (`refactor/549-cell-iface-isp-split`, PR #441): `kernel/command.Sweeper` fields are now unexported; `&kcommand.Sweeper{Scanner: ..., Queue: ..., Clk: ..., ...}` literal construction no longer compiles. Use `kcommand.NewSweeper(scanner, queue, clk, opts...) (*Sweeper, error)` factory with `kcommand.WithSweeperFilter / WithSweeperInterval / WithSweeperOnError`. Required deps fail-fast on nil at construction time (mirrors `OUTBOX-SERVICE-01` pattern). PR 441 F2 follow-up: typed-nil deps are rejected at NewSweeper via `validation.IsNilInterface` (**Hard via NewSweeper path**); Sweeper struct gains an unexported `built` sentinel, and `Start` head-checks `if !s.built` to fail-closed against the residual `&command.Sweeper{}` zero-value literal attack surface (**Medium runtime fail-closed** — the zero value remains expressible at the type level; Hard upgrade path is opaque-interface return from NewSweeper, tracked as backlog).

- **HTTP Service interface signature** (`refactor/533-typed-response-envelope`, PR #403): All 45 codegen-emitted HTTP contracts now use typed response envelope. `Service.Method(ctx, *Request) (*Response, error)` → `Service.Method(ctx, *Request) (XxxResponseObject, error)`. Business 4xx/5xx must be returned as typed structs (e.g. `Create404ErrorResponse{Body: *errcode.New(...)}`). The `error` return is reserved for undeclared framework 5xx (panic recover, infrastructure faults).
  - All 24 cell + example slice adapters migrated atomically (no compatibility shim).
  - See ADR `docs/architecture/202605061500-adr-typed-response-envelope.md`.
  - Roadmap: `docs/plans/202605011500-029-master-roadmap.md` 06.FU.

### Changed

- `gocell scaffold assembly` runnable stub now blocks on signal (SIGINT/SIGTERM)
  instead of returning a "not implemented" error immediately. `go run ./cmd/{id}`
  will wait for signal rather than exiting with code 1. (#867, sub-item
  ASSEMBLY-RUN-RUNTIME-SMOKE)

### Added

- `pkg/httputil.WriteErrorWithStatus(ctx, w, status, ecErr)` — pin wire status to typed envelope identity, share 4xx/5xx redaction policy with `WriteError`.
- `pkg/httputil.AppendCorrelationAttrs(ctx, attrs) []any` — exported correlation key set for generated handlers (request_id / trace_id / span_id).
- kernel/governance CH-06 — typed-response-set bijection between contract.yaml `responses[]` and generated `XxxResponseObject` struct set.
- `kernel/cell` adds `CellIdentity` / `CellLifecycle` / `CellStatus` / `CellInventory` sub-interfaces; `Cell` is now their composite (io.ReadWriter pattern). Callers may declare narrower sub-interface dependencies (metrics middleware / health handler etc.). See ADR 202605101800 §D1/D2.

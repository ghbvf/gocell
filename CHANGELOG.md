# Changelog

All notable changes to GoCell are documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

### Breaking Changes

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
  - Cell-internal demo fallback: use `cell.DemoCellTxManager()` (returns sealed `persistence.CellTxManager`); do NOT use `cell.DemoTxRunner{}` directly inside cells (will not compile against the new field types).
  - All 6 composition-root sites + 11 test files migrated atomically; no compatibility shim.
  - **Defense in depth (Hard sealed fields + Medium archtest API surface)** per ai-collab.md §"违反不可表达":
    - **Hard (type system)**: raw infra fields and raw→`CellXxx` assignments are unexpressible at compile time — sealed marker requires the unimplementable `sealedXxx()` method only the wrapper packages provide.
    - **Medium (archtest, necessary double defense)**: type system alone cannot exhaust signature forms; `func WithBad(p interface{ outbox.Publisher })` (inline interface embed) and `import . "kernel/persistence"; WrapForCell(p)` (dot-import) compile around bare `*types.Named` / `*ast.SelectorExpr` matchers. `CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01` walks `*types.Interface` embedded types; `CELL-RAW-INFRA-WRAPPER-LOCATION-01` resolves both `*ast.SelectorExpr` and `*ast.Ident` call forms.
  - See ADR `docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md` (amends `202605101800` §D6).

- **Sweeper construction API** (`refactor/549-cell-iface-isp-split`, PR #441): `kernel/command.Sweeper` fields are now unexported; `&kcommand.Sweeper{Scanner: ..., Queue: ..., Clk: ..., ...}` literal construction no longer compiles. Use `kcommand.NewSweeper(scanner, queue, clk, opts...) (*Sweeper, error)` factory with `kcommand.WithSweeperFilter / WithSweeperInterval / WithSweeperOnError`. Required deps fail-fast on nil at construction time (mirrors `OUTBOX-SERVICE-01` pattern). PR 441 F2 follow-up: typed-nil deps are rejected at NewSweeper via `validation.IsNilInterface` (**Hard via NewSweeper path**); Sweeper struct gains an unexported `built` sentinel, and `Start` head-checks `if !s.built` to fail-closed against the residual `&command.Sweeper{}` zero-value literal attack surface (**Medium runtime fail-closed** — the zero value remains expressible at the type level; Hard upgrade path is opaque-interface return from NewSweeper, tracked as backlog).

- **HTTP Service interface signature** (`refactor/533-typed-response-envelope`, PR #403): All 45 codegen-emitted HTTP contracts now use typed response envelope. `Service.Method(ctx, *Request) (*Response, error)` → `Service.Method(ctx, *Request) (XxxResponseObject, error)`. Business 4xx/5xx must be returned as typed structs (e.g. `Create404ErrorResponse{Body: *errcode.New(...)}`). The `error` return is reserved for undeclared framework 5xx (panic recover, infrastructure faults).
  - All 24 cell + example slice adapters migrated atomically (no compatibility shim).
  - See ADR `docs/architecture/202605061500-adr-typed-response-envelope.md`.
  - Roadmap: `docs/plans/202605011500-029-master-roadmap.md` 06.FU.

### Added

- `pkg/httputil.WriteErrorWithStatus(ctx, w, status, ecErr)` — pin wire status to typed envelope identity, share 4xx/5xx redaction policy with `WriteError`.
- `pkg/httputil.AppendCorrelationAttrs(ctx, attrs) []any` — exported correlation key set for generated handlers (request_id / trace_id / span_id).
- kernel/governance CH-06 — typed-response-set bijection between contract.yaml `responses[]` and generated `XxxResponseObject` struct set.

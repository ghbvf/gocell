# Changelog

All notable changes to GoCell are documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

### Breaking Changes

- **HTTP Service interface signature** (`refactor/533-typed-response-envelope`, PR #403): All 45 codegen-emitted HTTP contracts now use typed response envelope. `Service.Method(ctx, *Request) (*Response, error)` → `Service.Method(ctx, *Request) (XxxResponseObject, error)`. Business 4xx/5xx must be returned as typed structs (e.g. `Create404ErrorResponse{Body: *errcode.New(...)}`). The `error` return is reserved for undeclared framework 5xx (panic recover, infrastructure faults).
  - All 24 cell + example slice adapters migrated atomically (no compatibility shim).
  - See ADR `docs/architecture/202605061500-adr-typed-response-envelope.md`.
  - Roadmap: `docs/plans/202605011500-029-master-roadmap.md` 06.FU.

### Added

- `pkg/httputil.WriteErrorWithStatus(ctx, w, status, ecErr)` — pin wire status to typed envelope identity, share 4xx/5xx redaction policy with `WriteError`.
- `pkg/httputil.AppendCorrelationAttrs(ctx, attrs) []any` — exported correlation key set for generated handlers (request_id / trace_id / span_id).
- kernel/governance CH-06 — typed-response-set bijection between contract.yaml `responses[]` and generated `XxxResponseObject` struct set.

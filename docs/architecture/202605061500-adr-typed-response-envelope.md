# ADR: contractgen typed response envelope

- **Status**: accepted (2026-05-06)
- **PR**: PR-V1-CONTRACT-TYPED-RESPONSE-ENVELOPE (roadmap 06.FU)
- **Author**: ghbvf
- **Supersedes / relates**: `docs/architecture/202605051730-adr-errcode-message-pii-safety.md` (errcode redaction policy reused by `httputil.WriteErrorWithStatus`); `docs/architecture/202605031600-adr-v1-schema-evolution.md` (response schemas remain non-strict — typed structs stay forward-compatible).

## Context

`tools/codegen/contractgen` previously emitted `Service.Method(ctx, *Request) (*Response, error)` for every HTTP contract and let the generated handler call `httputil.WriteError(err)` for the error path. Status codes were derived at runtime from `errcode.Kind`. Three problems compounded:

1. **Three-way drift**. The handler implementation, the runtime `errcode.Kind → Status` map, and `contract.yaml http.responses[]` each held their own copy of the response status set. CH-04 governance had to reverse-infer the handler-emitted set from AST scanning + Kind name lookup (`kernel/governance/rules_http_response_alignment.go`), which is fragile and only catches one of the three pairs.
2. **Two limit error envelopes**. Builder's `detectPagination` required exactly `(cursor, limit)` query params and rejected any GET that also had path params or filter params. Endpoints like `auth/role/list/{userID}?cursor=&limit=&...` fell into the per-param branch in `handler.tmpl`, where `limit` was parsed inline by `strconv.ParseInt` and produced `ERR_VALIDATION_FAILED` instead of the canonical `ERR_PAGE_SIZE_EXCEEDED` from `pkg/httputil.ParsePageParams` — diverging from every other paginated endpoint.
3. **Stale doc.go**. `tools/codegen/contractgen/doc.go` listed only three artifacts (types/iface/handler) and missed `spec_gen.go` / `subscription_gen.go`; new maintainers had no map of the generator surface.

## Decision

Adopt a typed response envelope on the `oapi-codegen pkg/codegen/templates/strict` model, restructured as follows:

### D1. Typed response envelope (every HTTP contract)

`tools/codegen/contractgen/templates/types.tmpl` emits, alongside the existing Request / Response DTOs:

- `{HandlerMethod}ResponseObject` — a per-endpoint interface with a single unexported method `visit{HandlerMethod}Response(ctx, w)`. The unexported receiver closes the implementation set to types declared in the same generated package.
- One concrete struct per declared status:
  - `{HandlerMethod}{Status}JSONResponse` — body-bearing success (`type X Response`).
  - `{HandlerMethod}{Status}NoContentResponse` — `struct{}` for 204.
  - `{HandlerMethod}{Status}ErrorResponse` — declared 4xx/5xx with `Body errcode.Error`. The Visit method calls `pkg/httputil.WriteErrorWithStatus(ctx, w, status, &r.Body)`, sharing the 4xx/5xx redaction policy with the framework fallback path (single source for the wire envelope shape, ADR `202605051730-adr-errcode-message-pii-safety.md`).

`iface.tmpl` flips the Service signature to `Method(ctx, *Request) ({HandlerMethod}ResponseObject, error)`. The `error` return is reserved for *un-declared* framework 5xx (panic recover, infrastructure faults); the generated handler routes it through `httputil.WriteError(err)` which reverts to the `errcode.Kind → Status` mapping. Business 4xx/5xx must be returned as a typed struct.

`handler.tmpl` collapses the post-service path to `_ = resp.visit{HandlerMethod}Response(r.Context(), w)`. The Visit method writes status + body; the handler-level discard is safe because the typed structs already log encode/write failures via `slog.ErrorContext` and there is no recovery branch once headers/body have been (partially) flushed.

#### Rejected alternatives

- **Half-typed (success-only)**: keep `(*Response, error)` for business 4xx, only generate typed structs for the success status. Preserves existing service code but leaves the runtime `errcode.Kind → Status` reverse inference in place. Rejected — does not actually close the three-way drift the change is meant to fix.
- **Single default error wrapper** (`{HandlerMethod}DefaultErrorResponse{Status int; Body errcode.Error}`): one struct per endpoint instead of one per status. Rejected — `Status int` is dynamic, losing the compile-time guarantee that statically-declared endpoints can only return statically-declared statuses.

### D2. PaginationShape IR (replaces `IsPagination bool`)

`spec.go` introduces `PaginationShape{HasCursor, HasLimit bool; ExtraQueryParams []ParamSpec}`; `HTTPEndpointSpec.Pagination *PaginationShape` replaces the boolean field. `IsPagination()` becomes a method (`Pagination != nil`) so existing handler.tmpl branches keep working.

`builder.detectPagination` no longer rejects mixed pagination+filter endpoints; any GET that declares cursor (string) + limit (integer) is paginated, and the remaining query params land in `Pagination.ExtraQueryParams`. The previous call-site preconditions (`len(QueryParams) == 2` and `len(PathParams) == 0`) are dropped — they were leftovers from the strict 2-element invariant and would have left e.g. `auth/role/list/{userID}?cursor=&limit=` emitting the divergent inline-limit envelope.

`handler.tmpl` extracts the per-query-param parse block into a `{{define "queryParamParse"}}` helper used by both the `Pagination.ExtraQueryParams` branch and the non-pagination branch. cursor/limit always go through `pkg/httputil.ParsePageParams`; everything else parses per-param. Single source for the limit error envelope across the entire HTTP surface.

### D3. CH-06 governance (kernel/governance)

A new rule `CH-06` (`kernel/governance/rules_http_typed_envelope.go`) enforces the contract.yaml ↔ generated bijection:

- AST-scans `generated/contracts/<segments>/types_gen.go` for type declarations matching `^[A-Z]\w*?(\d{3})(JSONResponse|NoContentResponse|ErrorResponse)$`.
- Compares the extracted status set against `SuccessStatus ∪ http.responses[]` keys.
- Emits `SeverityError` for each declared-but-not-implemented status (codegen drift) and each implemented-but-not-declared status (orphan struct).

CH-06 sits alongside the existing CH-04 (handler-emitted status ⊂ declared, owned by `rules_http_response_alignment.go`). The two are complementary:

- CH-04 covers **pre-service** error sources where the helper name carries the status (`DecodeJSONStrict` → 400/413, `ParsePageParams` → 400, `ParseUUIDPathParam` → 400) and path-param validation `errcode.New(KindInvalid, …)` calls in handler.tmpl.
- CH-06 covers the **post-service** typed response set where the status is encoded in the struct identity rather than in `errcode.Kind`.

Together they replace the fragile "single AST scan reverse-inferring everything" pattern the original plan flagged.

### D4. doc.go full artifact matrix

`tools/codegen/contractgen/doc.go` is rewritten as the one-page maintainer map: the artifact-by-kind matrix, the typed-envelope naming convention, the cell attribution constraint (existing), and pointers to CH-04 / CH-06 / archtest. The previous 30-line stub is replaced wholesale.

### D5. archtest `HANDLER-NO-INLINE-LIMIT-PARSE-01`

`tools/archtest/handler_inline_limit_parse_test.go` walks `generated/contracts/http/**/handler_gen.go` and flags any function whose body contains both `strconv.ParseInt` and a `"limit"` string literal. The two-condition match keeps the rule from firing on legitimate generic `int64` query parsing for unrelated params. Guards the generator template against regressing to per-param limit parsing — the symptom of D2's old behavior.

## Consequences

### Wins

- Single source of truth for HTTP response status. Drift is statically caught at codegen / governance time, not at runtime by paying clients.
- `http_response_alignment` (CH-04) reverse inference logic is no longer load-bearing for post-service paths — Service no longer emits `errcode.New(K, …)` from generated handler bodies, only typed structs. CH-04 still owns pre-service helper-emitted codes, where the helper-name table remains the authoritative inference.
- Pagination endpoints (auth/role/list, audit/list, device/command/dequeue, …) now share the same `ERR_PAGE_SIZE_EXCEEDED` envelope as `order/list`. PR#376 F-COR-001 and roadmap F4 absorb closed.
- doc.go renames itself useful: artifact map + typed envelope semantics + governance cross-references.

### Costs

- 24 cell + example slice adapters had to migrate to the typed signature in a single PR (no compatibility shim — see `feedback_no_backcompat_elegant`). Mechanical; absorbed by 4 parallel sub-agents in `Batch 3`.
- `pkg/httputil` gains one new public function (`WriteErrorWithStatus`) which is now part of the framework's stable surface for typed-envelope generated code.
- 46 generated http handlers + ~14 golden testdata files regenerated; a single Batch 2 commit churns 5k LOC of generated content.

### Carried forward (not in scope)

- **Converter codegen** (06.FU2 `PR-V1-CONTRACT-RESPONSE-CONVERTER-CODEGEN`). The 6 `to{X}ResponseData` helpers in `examples/iotdevice/cells/devicecell/slices/devicecommand/service.go` were preserved as-is. Typed envelope only changes the outer Service signature; converter dedup is a separate codegen pass.
- **Pre-service typed wrap**. Validation errors emitted by `handler.tmpl` decoder/path-param branches still call `httputil.WriteError(errcode.New(KindInvalid, …))` rather than constructing `Xxx400ErrorResponse`. They produce the same wire envelope and CH-04 covers their static guarantees; promoting them to typed envelope would add ~50 sites of mechanical wrap with no end-user benefit.

## References

- Plan: `~/.claude-ming/plans/docs-plans-202605011500-029-master-road-shimmying-sloth.md`
- Roadmap entry: `docs/plans/202605011500-029-master-roadmap.md` line 108 (06.FU)
- PR#376 F-COR-001 source: `docs/reviews/202605051730-PR376/06-correctness.md`
- ref: oapi-codegen `pkg/codegen/templates/strict/strict-interface.tmpl@main`
- ref: oapi-codegen `pkg/codegen/templates/strict/strict-responses.tmpl@main`
- ref: oapi-codegen `examples/petstore-expanded/strict/api/petstore-server.gen.go@v2.1.0`

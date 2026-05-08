# ADR: contractgen typed response envelope

- **Status**: accepted (2026-05-06)
- **PR**: PR-V1-CONTRACT-TYPED-RESPONSE-ENVELOPE (roadmap 06.FU)
- **Author**: ghbvf
- **Supersedes / relates**: `docs/architecture/202605051730-adr-errcode-message-pii-safety.md` (errcode redaction policy reused by `httputil.WriteErrorWithStatus`); `docs/architecture/202605031600-adr-v1-schema-evolution.md` (response schemas remain non-strict — typed structs stay forward-compatible).

## Context

`tools/codegen/contractgen` previously emitted `Service.Method(ctx, *Request) (*Response, error)` for every HTTP contract and let the generated handler call `httputil.WriteError(err)` for the error path. Status codes were derived at runtime from `errcode.Kind`. Three problems compounded:

1. **Three-way drift**. The handler implementation, the runtime `errcode.Kind → Status` map, and `contract.yaml http.responses[]` each held their own copy of the response status set. CH-04 governance had to reverse-infer the handler-emitted set from AST scanning + Kind name lookup (`kernel/governance/rules_http.go`, CH-04 section), which is fragile and only catches one of the three pairs.
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

`handler.tmpl` collapses the post-service path to `_ = resp.visit{HandlerMethod}Response(r.Context(), w)`. The Visit method buffers JSON encoding to `bytes.Buffer` first, then commits status + body atomically; encode failures surface to the handler for 5xx fallback via `httputil.WriteError(ctx, w, errcode.New(KindInternal, ...))`, ensuring wire status code and body are always consistent.

#### Rejected alternatives

- **Half-typed (success-only)**: keep `(*Response, error)` for business 4xx, only generate typed structs for the success status. Preserves existing service code but leaves the runtime `errcode.Kind → Status` reverse inference in place. Rejected — does not actually close the three-way drift the change is meant to fix.
- **Single default error wrapper** (`{HandlerMethod}DefaultErrorResponse{Status int; Body errcode.Error}`): one struct per endpoint instead of one per status. Rejected — `Status int` is dynamic, losing the compile-time guarantee that statically-declared endpoints can only return statically-declared statuses.

### D2. PaginationShape IR (replaces `IsPagination bool`)

`spec.go` introduces `PaginationShape{HasCursor, HasLimit bool; ExtraQueryParams []ParamSpec}`; `HTTPEndpointSpec.Pagination *PaginationShape` replaces the boolean field. `IsPagination()` becomes a method (`Pagination != nil`) so existing handler.tmpl branches keep working.

`builder.detectPagination` no longer rejects mixed pagination+filter endpoints; any GET that declares cursor (string) + limit (integer) is paginated, and the remaining query params land in `Pagination.ExtraQueryParams`. The previous call-site preconditions (`len(QueryParams) == 2` and `len(PathParams) == 0`) are dropped — they were leftovers from the strict 2-element invariant and would have left e.g. `auth/role/list/{userID}?cursor=&limit=` emitting the divergent inline-limit envelope.

`handler.tmpl` extracts the per-query-param parse block into a `{{define "queryParamParse"}}` helper used by both the `Pagination.ExtraQueryParams` branch and the non-pagination branch. cursor/limit always go through `pkg/httputil.ParsePageParams`; everything else parses per-param. Single source for the limit error envelope across the entire HTTP surface.

### D3. CH-06 governance (kernel/governance)

A new rule `CH-06` (`kernel/governance/rules_http.go`, CH-06 section) enforces the contract.yaml ↔ generated bijection:

- AST-scans `generated/contracts/<segments>/types_gen.go` for type declarations matching `^[A-Z]\w*?(\d{3})(JSONResponse|NoContentResponse|ErrorResponse)$`.
- Compares the extracted status set against `SuccessStatus ∪ http.responses[]` keys.
- Emits `SeverityError` for each declared-but-not-implemented status (codegen drift) and each implemented-but-not-declared status (orphan struct).

CH-06 sits alongside the existing CH-04 (handler-emitted status ⊂ declared, both now in `kernel/governance/rules_http.go` after PR-FUNNEL-03 consolidation). The two are complementary:

- CH-04 covers **pre-service** error sources where the helper name carries the status (`DecodeJSONStrict` → 400/413, `ParsePageParams` → 400, `ParseUUIDPathParam` → 400) and path-param validation `errcode.New(KindInvalid, …)` calls in handler.tmpl.
- CH-06 covers the **post-service** typed response set where the status is encoded in the struct identity rather than in `errcode.Kind`.

Together they replace the fragile "single AST scan reverse-inferring everything" pattern the original plan flagged.

### D4. doc.go full artifact matrix

`tools/codegen/contractgen/doc.go` is rewritten as the one-page maintainer map: the artifact-by-kind matrix, the typed-envelope naming convention, the cell attribution constraint (existing), and pointers to CH-04 / CH-06 / archtest. The previous 30-line stub is replaced wholesale.

### D5. funnel-first 守门：pagination shape relax + golden 字节锁定

PR #403 当时引入了 `tools/archtest/handler_invariants_test.go::TestHandlerNoInlineLimitParse`（archtest `HANDLER-NO-INLINE-LIMIT-PARSE-01`），扫描 `generated/contracts/http/**/handler_gen.go` 中同时含 `strconv.ParseInt` 与 `"limit"` 字面量的函数体，作为 generator 退化到 per-param inline limit 解析的兜底守卫。

PR-FUNNEL-02（参见 `docs/plans/202605070431-pr403-funnel-fix-roadmap.md` §7）把同主题 5 条 archtest 拆分为"模板侧 funnel-pin（4 条）+ 调用侧 archtest 残留（1 条）"两类：

**模板侧 funnel-pin（删除 archtest，由 handler.tmpl + golden 字节锁定取代）**

- HANDLER-NO-INLINE-LIMIT-PARSE-01 — 由 `http_order_list_v1` golden 字节锁定 `httputil.ParsePageParams`
- HANDLER-NO-SCHEMA-FOR-NOBODY-01 — 由 builder `endpointSpec.HasBody` gate + `if .RequestSchemaJSON` template gate 联合保证，`http_order_get_v1` / `http_order_list_v1` / `synth_http_full` golden 字节锁定 GET handler 无 `requestSchemaJSON` literal
- HANDLER-PATH-QUERY-LENGTH-VALIDATION-01 — 由 `synth_http_full` / `synth_http_keyword_conflict` golden 字节锁定 `len(v) < N` / `len(v) > N` 块
- HANDLER-VALIDATOR-FAIL-FAST-01 — 由 9 个 handler golden 全数字节锁定 `panic(errcode.Assertion("schema compile failed: %v", err))` literal（Auth / Public / Bootstrap / PasswordResetExempt 全分支覆盖）

**调用侧 archtest 残留（funnel 触不到，保留 archtest 平铺兜底）**

- HANDLER-POLICY-REQUIRED-01 — PR#411 review F1 fix 将此条升级为 **funnel + 平铺兜底两级防御**：funnel 端通过 `auth.clientsOnly` / `auth.serviceOwned` 新标志生成单参 `NewHandler(svc Service)`（无 policy 参数，caller 无法传 nil），Default 分支构造期加 `if policy == nil { panic(errcode.Assertion(...)) }` 把所有 nil-policy 产生源消灭；archtest scanner 退化为简化平铺兜底，删除 `handlerPolicyPublicExemptPkgs` 字符串豁免列表（alias 伪装绕过路径），捕获"字面 nil 传给 2-arg NewHandler"的代码气味（此类代码在 runtime 也会被构造期 panic 炸出，scanner 只是更早发现）。保留为 `tools/archtest/handler_policy_required_test.go`（一个文件、一个 Test func、一个 negative fixture，无豁免列表）。

**Funnel 性质**

模板侧 4 条由 golden 字节级 diff 守 —— 任何模板分支字面量变化（panic 文本、policy 参数声明、`len()` 检查、ParsePageParams 调用）都触发 golden 字节差异，且无 false positive。调用侧 1 条由 AST 扫描守 —— scanner 自身被 negative fixture 验证（`tools/archtest/testdata/handler_nil_policy/cell.go`）。

主流对照（K8s / CockroachDB / Linux / Rust / Go 工具链）都接受 funnel 不到的残留平铺管理 —— 模板可表达的约束走 funnel + freeze，模板看不到的约束保留 archtest，分类清晰。

### D6. 5xx wire/log 隔离强化

PR #403 review 暴露 `pkg/httputil.WriteErrorWithStatus` 的 5xx 路径残留两个隐性炸弹：

1. **Kind 透传**：`out = errcode.New(ecErr.Kind, ...)` 把传入 ecErr 的 Kind 原样写进 5xx wire body。若 ecErr.Kind 为 4xx（如 KindNotFound），`MarshalJSON` 的 `IsClient()` 返回 true，Details 不会 strip，5xx wire body 可能携带 runtime 字段（违反 errcode message PII safety 原则，ADR `202605051730`）。
2. **Details log 未 redact**：`log5xx` 把 ecErr.Details 的每个 slog.Attr 直接追加到 logAttrs 写入 slog，控制字段（dsn / token 等）泄漏到 stdout/stderr。

修复：

- 5xx 路径强制 normalize Kind 为与 status 匹配的 5xx Kind 常量（`KindUnavailable` / `KindDeadlineExceeded` / `KindNotImplemented` / `KindInternal`），由 archtest `HTTPUTIL-5XX-KIND-NORMALIZE-01` 静态守卫。
- log5xx 通过 `pkg/redaction.RedactSlogAttr(slog.Attr) slog.Attr` 处理 Details 后再追加，由 archtest `HTTPUTIL-5XX-LOG-REDACT-01` 静态守卫。
- `RedactSlogAttr` 是 `slog.Attr` 的 redaction 适配层（KindString → RedactString，KindGroup → 递归），与 `pkg/redaction.RedactError` 形成完整覆盖。

ref: ADR `docs/architecture/202605051730-adr-errcode-message-pii-safety.md` §"errcode 三层 redaction 分工"（5xx 必须 strip Details + Internal 永不出现）。

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
- Framework 5xx paths where service returns a non-nil error fall outside the CH-06 assertion surface — they are covered by CH-04's `httpHelperWritesStatuses` table (`kernel/governance/rules_http.go:113-131`), which maps `WriteError` and pre-service helpers to their emitted status set. CH-04 and CH-06 are thus complementary: CH-04 owns pre-service and framework-fallback error sources, CH-06 owns the post-service typed-struct bijection.

### Runtime 行为

- `XxxResponseObject` 的 unexported `visitXxxResponse(ctx, w)` 方法把实现集封闭在 generated package 内部（与 `oapi-codegen` strict 模式同源）。Service 方法的返回类型是接口而非具体类型，在 Go 编译期即可确认：返回错误 concrete type 是编译错误，运行时不存在 dispatch 失败路径。
- Service 返回 `(nil, nil)` 时，generated handler 走 `httputil.WriteError(errcode.New(KindInternal, …))` 兜底，向客户端返回 500 + `ERR_INTERNAL`，并通过 `slog.ErrorContext` 记录 "service returned nil response without error"。该路径属于 CH-04 覆盖的 `WriteError` 兜底面，不属于 CH-06 的 typed-struct 断言面。
- Success 与 Error 路径均采用 **buffer-then-commit** 模式（先序列化到 `bytes.Buffer`，再 `WriteHeader` + `buf.WriteTo`），与 `oapi-codegen pkg/codegen/templates/strict/strict-responses.tmpl@main` / `Kratos transport/http/codec.go DefaultResponseEncoder` / `go-zero rest/httpx/responses.go doWriteJson` 三家上游共识一致。`visitXxxResponse` 返回 non-nil error 时 header 尚未提交，handler 调用 `httputil.WriteError(ctx, w, errcode.New(KindInternal, ...))` 兜底 5xx，wire 状态码与 body 严格一致；底层 encode 失败的关联 trace 字段由 visit 内部 `slog.ErrorContext` 记录。

### Carried forward (not in scope)

- **Converter codegen** (06.FU2 `PR-V1-CONTRACT-RESPONSE-CONVERTER-CODEGEN`). The 6 `to{X}ResponseData` helpers in `examples/iotdevice/cells/devicecell/slices/devicecommand/service.go` were preserved as-is. Typed envelope only changes the outer Service signature; converter dedup is a separate codegen pass.
- **Pre-service typed wrap**. Validation errors emitted by `handler.tmpl` decoder/path-param branches still call `httputil.WriteError(errcode.New(KindInvalid, …))` rather than constructing `Xxx400ErrorResponse`. They produce the same wire envelope and CH-04 covers their static guarantees; promoting them to typed envelope would add ~50 sites of mechanical wrap with no end-user benefit.
- **handler.tmpl 模块化** (06.FU2). PR #403 已达 199 文件，handler.tmpl 按关注点拆分（decode / pagination / visit / error-fallback）作为独立 follow-up，与 typed envelope 主题不直接相关。

### D7. 双向闭合契约（PR #403 第三轮 review §R1 收口）

D1-D6 编码了 typed envelope 的"链中段"——`contract.yaml` 声明 → `typed XxxErrorResponse` struct 生成。链头（contract.yaml 必须声明 ≥1 4xx/5xx）和链尾（adapter return status 集合 ⊆ contract 声明）都需要静态守卫，否则中段 codegen 在两端开口。

**链头：C18-error-required**

`tools/codegen/contractgen/builder.go::collectAndValidateStatuses` 末尾新增 hasError 累计检查：HTTP endpoint 必须显式声明至少一个 4xx/5xx response code，不允许 success-only contract（如 `successStatus: 201, Responses: nil`）静默通过。理由：typed `XxxErrorResponse` struct 的源是 `contract.yaml.responses[]`；无声明则 typed envelope 在错误路径上无 wire form，CH-06 bijection 也无法验证。

**链尾：ADAPTER-RETURNS-DECLARED-TYPES-01（Ceiling 守语义）**

`tools/archtest/adapter_returns_declared_types_test.go` 新增 archtest，AST 扫描 `cells/*/slices/*/{handler,service}.go` 与 `examples/*/cells/*/slices/*/{handler,service}.go`，提取实现 `XxxResponseObject` 接口的方法的 `return XxxNNNJSONResponse{...}` / `XxxNNNNoContentResponse{...}` / `XxxNNNErrorResponse{...}` 字面量返回，断言 status NNN ∈ contract.yaml `SuccessStatus ∪ Responses[]`；超集报错。

**Ceiling 语义边界**：return 集合 ⊆ declared 集合。**零 typed return（adapter 全部走 `return nil, err` framework fallback）合法**。理由：当前 cells/examples 下所有 adapter 全部 framework-fallback 是 Pre-CH-06 时代的历史包袱；一次性升级到 Floor 守需改 25+ 个 adapter，超出 Cx3 范围。

**演进锚点（依赖段 2 invariant Registry 工具产品化）**

| 阶段 | 守语义升级 | adapter 改造 | 入口 | 触发条件 |
|---|---|---|---|---|
| 段 1（本 ADR） | ADAPTER-RETURNS-DECLARED-TYPES-01 = Ceiling 守 | 零改动；零 typed return 合法 | 已落地 | — |
| 段 2.5（独立 PR） | + ADAPTER-RETURNS-SUCCESS-FLOOR-01（successStatus 必须至少有一处 typed return） | ~25 adapter `return nil, err` → `return XxxNNNJSONResponse{...}, nil` | docs/backlog.md `B-FLOOR-FOLLOWUP` | 段 2 invariant Registry PR ship 后启动；预估 16h dev + 4h review |
| 段 4（独立 PR） | + ADAPTER-RETURNS-FULL-FLOOR-01（每个声明 status 都至少返一次） | 桩典型错误路径返 typed `XxxNNNErrorResponse` | 同上 | 段 2.5 ship 后再启动（依赖 Success-Floor 已稳定）；预估 24h dev + 6h review |

**触发判定**：`B-FLOOR-FOLLOWUP.ready = true` 当且仅当（a）段 2 `INVARIANT-REGISTRY-COMPLETENESS-01` archtest 已绿、（b）`gocell check invariants` CLI 可用、（c）typed envelope 6 条 invariant 全部入注。三者任一未达成即维持 Ceiling 守，避免分批 floor 升级导致 archtest 与 adapter 状态不一致。

**Bijection 图**

```
contract.yaml.responses[]
       │
       │ C18 (链头) — 必含 ≥1 4xx/5xx
       ▼
generated/contracts/.../types_gen.go (XxxNNNErrorResponse / XxxNNNJSONResponse)
       │
       │ CH-06 (链中) — bijection: 声明 ⇔ 生成
       ▼
adapter return XxxNNN...{...} 字面量
       │
       │ ADAPTER-RETURNS-DECLARED-TYPES-01 (链尾, Ceiling)
       ▼ — return ⊆ declared；零 typed return 合法
http wire response
```

### D8. 5xx wire code 单源（PR #403 第三轮 review §R2 收口）

PR #403 rebase 触发 `TestWriteErrorWithStatus_5xxKindNormalize/501` 失败，根因是 D6 引入的 `PublicCodeForStatus(501) → ErrNotImplemented` 与 `Kind.PublicCode()` 中 `KindNotImplemented` 投影到 `ErrInternal` 的真实行为冲突。修复方向是声明 5xx wire code 单源：

- **权威**：`pkg/errcode/kind.go::Kind.PublicCode()`——每个 5xx Kind 投影到 `{ErrInternal, ErrServiceUnavailable, ErrServerTimeout}` 之一
- **镜像**：`pkg/errcode/status.go::PublicCodeForStatus`——是 `Kind.PublicCode()` 的 status→Code 镜像；只有 503/504 有显式 case（其 Kind 有专用 wire code），其他 5xx（500/501/502/507/...）全部 fall through 到 default ErrInternal
- **收敛集**：5xx wire code 永远只能是 `{ErrInternal, ErrServiceUnavailable, ErrServerTimeout}` 三者之一

引入新 dedicated 5xx wire code 必须**同时**扩展 `Kind.PublicCode()` 和 `PublicCodeForStatus`，由 archtest `WIRE-CODE-5XX-SINGLE-SOURCE-01`（`tools/archtest/wire_code_5xx_single_source_test.go`）静态守卫：AST 扫两处 switch case，断言 5xx 段集合一致。

`PublicCodeForStatus` 的 godoc 顶部记载此约定，作为下次修改者的入口指引。

D6 §"5xx wire/log 隔离强化"中"Kind 透传 normalize"的部分被 D8 收紧——5xx 路径不仅 normalize Kind，还必须把任何非收敛集 wire code 折叠到 ErrInternal。

## References

- Plan: `~/.claude-ming/plans/docs-plans-202605011500-029-master-road-shimmying-sloth.md`
- Roadmap entry: `docs/plans/202605011500-029-master-roadmap.md` line 108 (06.FU)
- PR#376 F-COR-001 source: `docs/reviews/202605051730-PR376/06-correctness.md`
- PR #403 third-wave review: `docs/reviews/202605070153-pr403-third-wave-review.md`
- ref: oapi-codegen `pkg/codegen/templates/strict/strict-interface.tmpl@main`
- ref: oapi-codegen `pkg/codegen/templates/strict/strict-responses.tmpl@main`
- ref: oapi-codegen `examples/petstore-expanded/strict/api/petstore-server.gen.go@v2.1.0`
- ref: Kratos `transport/http/codec.go` DefaultResponseEncoder（buffer-then-commit）
- ref: go-zero `rest/httpx/responses.go` doWriteJson（buffer-then-commit）
- ref: oapi-codegen `pkg/codegen/operations.go::generateOperationDefinition` strict-server require ≥1 4xx (D7 链头)
- ref: goa `goagen/codegen/types/types.go` strict response set; connect-go `cmd/protoc-gen-connect-go` typed error returns (D7 链尾，goa/connect-go 通过编译期类型，GoCell 选 archtest 因 contract.yaml 不在 protobuf 编译流)
- ref: grpc-go `internal/status/status.go::Code()` switch single source (D8)
- ref: go-zero `tools/goctl/api/spec/spec.go` registry-driven contract surface (D6 暴露面注册表)
- PR #403 review (multi-role 6 dimensions)

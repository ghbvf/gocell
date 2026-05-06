# PR #403 第三轮 Review 报告

**PR**: refactor(contractgen): typed response envelope + paginationShape IR + CH-06
**分支**: refactor/533-typed-response-envelope (rebased on origin/develop @4f3e1fd2)
**规模**: 7 commit / 214 文件 / +9228 -669
**审查范围**: Batch 6 增量 + 跨 6 batch 一致性 + L2/L3 概念模型审计
**审查日期**: 2026-05-07

---

## 0. 方向评估

**Direction: 正确，不可逆。**

PR 实质交付：

1. AST 反推 → 显式声明：CH-06 静态比对 contract.yaml ↔ typed struct，bijection 编译期强制
2. adapter 签名表达 status 意图：`(XxxResponseObject, error)` 替代 `(*Response, error)`
3. 限分页错误信封统一：全 HTTP 面 `ParsePageParams` → `ERR_PAGE_SIZE_EXCEEDED`
4. 5xx wire body PII strip 跨整面统一
5. PaginationShape + ResponseSpec IR 替代 bool 旗标

向上对标 oapi-codegen / goa / connect-go 同范式。**架构方向独立成立，与下文挑出的问题无关。**

---

## 1. 验证：14 个症状

| # | 文件:行 | 行为 | 来源 |
|---|---|---|---|
| S1 | `pkg/httputil/response.go::writeInternalErrorSentinel` | helper 与 `writeErrorBody` 内联 fail 路径重复 (DRY) | 第 3 轮 |
| S2 | `kernel/governance/rules_http_response_alignment.go::knownNonWriters` | `AppendCorrelationAttrs` 未注册 | 第 3 轮 |
| S3 | `pkg/idutil/id.go::MaxHTTPIDLen` godoc | 仍举 X-Request-Id 例子，已被 regex 取代 | 第 3 轮 |
| S4 | `pkg/redaction/redaction.go::RedactSlogAttr` | `KindLogValuer`/`KindAny` 透传无测试锁定 | 第 3 轮 |
| S5 | `pkg/httputil/response.go::encodeErrorEnvelopeTo` | marshal/decode/encode 失败路径未直测 | 第 3 轮 |
| S6 | `pkg/httputil/response_test.go:591-599` | 死代码 `type _ = struct{}` + 过期注释 | 第 3 轮 |
| S7 | `tools/codegen/contractgen/builder_test.go` | 缺 success-only 反例 case | 第 3 轮 |
| S8 | `tools/archtest/visit_buffer_then_commit_test.go::checkVisitBufferThenCommit` | 排除 NoContent/Error 受体未文档化 | 第 3 轮 |
| S9 | `journeys/J-typed-envelope-roundtrip.yaml:29` + `tests/contracttest/typed_envelope_roundtrip_test.go:124` | `mode: auto` 但 runtime subtest 仅 `t.Log`（伪绿） | 第 3 轮 |
| S10 | `tools/codegen/contractgen/generator_test.go` (新建) | C5/C18 codegen-CLI 端到端测试缺位 | 第 3 轮 |
| S11 | `docs/guides/codegen-new-endpoint.md:47-53` | `responses:` 错放 YAML 顶层；真实 schema 要求 `endpoints.http.responses`，被 `kernel/metadata/parser.go:442` `KnownFields(true)` 直接拒收 | 用户补 |
| S12 | `tools/codegen/contractgen/builder.go:350` | C18 仅在 `SuccessStatus==0 && len(Responses)==0` 同时为空时报错；success-only contract（如 `successStatus:201, Responses=nil`）静默放过 | 用户补 |
| S13 | `tools/codegen/contractgen/testdata/synth/synth_http_minimal/.../contract.yaml` + `synth_http_minimal_types_gen_go.golden:42` | success-only fixture 是 S12 活证：只生成 `Ping201JSONResponse`，零 error struct | 派生 |
| S14 | 45 个真实 `contracts/http/.../contract.yaml` | S12 收紧后须扫并补 4xx/5xx 声明 | 派生 |

**已落地但不计入 finding**：rebase 后 `TestWriteErrorWithStatus_5xxKindNormalize/501` 失败 → `pkg/errcode/status.go::PublicCodeForStatus` 删 501 case + `WriteErrorWithStatus` 删 501 分支 + 两处测试 expect `ErrInternal`，对齐"5xx wire code 收敛到 {ErrInternal, ErrServiceUnavailable, ErrServerTimeout}"既有契约。

---

## 2. 根因聚合（5 + 1）

### R0（meta，跨 PR）— GoCell 派生物治理无产品化通道

**这是 R1-R5 的共同上游。**

contract.yaml DSL 的覆盖范围有意识地停在"HTTP 协议层形状"。但 GoCell 想要的不变量越过这条边界（adapter 必须返 typed、5xx wire body PII strip、redaction passthrough 规则、buffer-then-commit 模式、5xx wire code 收敛、httputil 暴露面）—— 这些是 Go 运行时行为/代码结构约束，**不在 YAML 表达范围**。

后果：每条新 invariant 落在 Go 代码 + N 个外挂载体（archtest / governance / godoc / ADR / journey YAML / cell-patterns.md / handler.tmpl / IR）。**加一条规则要在 8-12 处同时改**，没有统一入口。typed envelope 一次塞 5-6 条 invariant × 4 件套 = 20+ 处可漏点 → 三轮 review 必然。

R1-R5 都是这条上游缺陷的下游表现。

### R1 [L3] typed envelope 完整性契约只编码了链中段
**症状收口**: S7 / S10 / S11 / S12 / S13 / S14

ADR D1 链：`contract.yaml.responses[]` → `typed XxxErrorResponse` → `adapter 必须返回` → `handler 分发`。codegen 只机械化中段（声明→struct）。链头（C18 太松）和链尾（adapter 返回的 status 集合 ⊆ contract.yaml 声明，无静态守）都开口。文档错位（S11）是同根的另一面：连写文档的人都没把握 schema 在哪。

### R2 [L2] 5xx wire code 单源没明示
**症状收口**: rebase 暴露的 501 失败 / 防未来漂移

`Kind.PublicCode()` 是 5xx wire 唯一权威；`PublicCodeForStatus` 是它的 status→code 镜像。这条规则只在测试里写死，没在代码注释或 ADR 写。Batch 6 加 `PublicCodeForStatus(501) → ErrNotImplemented` 时引入第三套映射 → rebase 一碰就崩。

### R3 [L2] redaction 边界没用测试锚定
**症状收口**: S4

`RedactSlogAttr` 透传 `KindLogValuer`/`KindAny` 是有意 fail-open（caller 责任，由 `WithDetails` panic 第一道防线兜底）。但 godoc 含糊（"all other kinds pass through"），无测试锁定边界。下个修改者看不出是漏写还是有意。

### R4 [L2] httputil 暴露面无集中注册
**症状收口**: S1 / S2

`pkg/httputil` 现有 7 个对外 helper（`WriteError`, `WriteErrorWithStatus`, `WritePublic`, `WriteNilResponseInternal`, `WriteEncodeFaultInternal`, `AppendCorrelationAttrs`, `writeInternalErrorSentinel`）。每加一个要去 `httpHelperWritesStatuses` / `knownNonWriters` 两表手动登记，漏一个出 false positive 或 false negative。`writeInternalErrorSentinel` 同时 godoc 与 prod 路径脱钩。

### R5 [L1] 测试-archtest-journey 三角形职责未明示
**症状收口**: S3 / S5 / S6 / S8 / S9

同一不变量（buffer-then-commit）有三处证人：archtest 静态守、runtime smoke、journey YAML PM 验收。Batch 6 把 runtime smoke 写成空 `t.Log` 但 journey 仍标 `mode: auto` → 伪绿。`visit_buffer_then_commit` 排除 NoContent/Error 不写理由。`idutil.MaxHTTPIDLen` godoc 不跟随 regex 切换。`response_test.go` 死代码残留。统称"侧文件未跟随"过程问题。

---

## 3. 复杂度分级

| Root | L 级 | Cx | 说明 |
|---|---|---|---|
| R0 (meta) | L3+流程 | Cx4 | GoCell 工具链层级，独立 roadmap |
| R1 typed envelope 双向闭合 | L3 | Cx3 | 链头收紧 + 新链尾 archtest + 45 真实 contract 扫补 + 测试 + ADR + doc |
| R2 5xx wire code 单源 | L2 | Cx2 | godoc + archtest 守对偶约束 |
| R3 redaction 边界 | L2 | Cx1 | 测试锁定 + godoc 增 "Known limitations" |
| R4 httputil 暴露面 | L2 | Cx2 | doc.go 注册表升级 + archtest 完整性守卫 + sentinel 内联化 |
| R5 测试三角形 + 跟随 | L1 | Cx2 | journey mode + archtest 注释 + idutil godoc + dead code + encodeErrorEnvelopeTo 直测 |

---

## 4. 修复方案（4 段）

### 段 1（本 PR 内，必做，Cx3 主体）— 闭环

**1.1 R1 链头闭合**

- `tools/codegen/contractgen/builder.go::collectAndValidateStatuses`：HTTP endpoint 必须声明 ≥1 个 4xx/5xx
- 45 真实 contract.yaml 扫描 + 补 `endpoints.http.responses`
- `synth_http_minimal/contract.yaml` 补 `responses: {400, 500}` 保留为最小有效样本
- 文档示例从真实 `audit/list/v1/contract.yaml` 拷贝
- builder_test 新增 success-only 反例 case；新建 generator_test 覆盖 CLI 错误传播

**1.2 R1 链尾闭合（关键新防线）**

- 新 archtest `ADAPTER-RETURNS-DECLARED-TYPES-01`：扫 `cells/*/slices/*/handler.go` + `examples/*/cells/*/slices/*/service.go` 中实现 `XxxResponseObject` 的 adapter 方法，AST 抽 `return Xxx{Status}{Suffix}{...}` 的 status 集合，比对 `endpoints.http.responses[] ∪ successStatus`，超集报错

**1.3 R2-R5 四件套配齐**

- R2: archtest 守 `PublicCodeForStatus` 5xx case ⊆ `Kind.PublicCode()` 5xx case
- R3: `pkg/redaction/redaction_test.go` 加 LogValuer/AnyValue 透传 case + godoc 增 "Known limitations" 段
- R4: `pkg/httputil/doc.go` 升级 "Stable Surface" 为完整注册表分类；archtest 守 `pkg/httputil` exported func 全在 CH-04 表内；`writeErrorBody` fail 路径替为 `writeInternalErrorSentinel(w)` 调用
- R5: journey YAML `buffer-then-commit` 改 `mode: manual` + 注释指向 archtest；删 runtime subtest 空 `t.Log`；archtest 文件 doc-comment 写明 NoContent/Error 排除理由；`idutil/id.go` godoc 删 X-Request-Id 例子；`response_test.go:591-599` 死代码删除；新增 `TestEncodeErrorEnvelopeTo_FailingWriter` 覆盖 io.Writer 失败路径

**1.4 ADR D6/D7 升级**

`docs/architecture/202605061500-adr-typed-response-envelope.md`：

- §"双向闭合契约" — 链头 C18 + 链尾 ADAPTER-RETURNS-DECLARED-TYPES-01
- §"5xx wire code 单源" — Kind.PublicCode() 权威，PublicCodeForStatus 镜像

**段 1 完成 = 本 PR ship gate 全绿。**

### 段 2（独立 PR，必做，Cx3）— 派生物治理产品化

ADR `docs/architecture/{ts}-adr-invariant-four-piece-kit.md`：

| 件 | 必须 | 位置 |
|---|---|---|
| 静态守卫 | ✅ | `tools/archtest/{ID}_test.go` 或 `kernel/governance/rules_*.go` |
| 文档契约 | ✅ | godoc 顶部 `// {ID}: ...` + ADR 段 |
| 回归测试 | ✅ | unit/contract/integration ≥1 处 |
| 注册表登记 | ✅ | `kernel/governance/invariants.go::Registry` |

工具：

- `kernel/governance/invariants.go` 中心注册表（每条 ID/Description/StaticGuard/GoDoc/ADR/Tests）
- archtest `INVARIANT-REGISTRY-COMPLETENESS-01` 守每条都解析成功
- `gocell check invariants` 命令
- typed envelope 6 条 invariant 作为首批入注

**目标**：加新 invariant 成本从"扫 8-12 处"降到"Registry +1 行 + 写四件"。

### 段 3（独立 PR，必做，Cx1）— PR 切片纪律

`.claude/rules/gocell/pr-slicing.md`：

- 一个 PR ≤ 2 个新 invariant
- L3 概念变化（codegen DSL / governance 模型）与 L1 大规模迁移（>10 文件机械改）必须切片
- L3 切片先 land、L1 跟随
- PR description 强制声明引入/修复哪几个 invariant

CLAUDE.md 增链。

### 段 4（roadmap，不立即）— 历史 invariant 审计

`docs/backlog.md` 新增 `GOCELL-INVARIANT-AUDIT-V1`：

- 反推现有 30+ archtest / 15+ governance rule 对应的 invariant
- 评估四件套齐套度
- 缺件补
- 全部入 `Registry`
- 依赖段 2 工具 → 不立即做

---

## 5. 时间盘

| 段 | 估时 | 顺序 | 依赖 |
|---|---|---|---|
| 段 1 | 1.5 天 | 立刻 | 无 |
| 段 2 | 1 天 | 段 1 ship 后 | 段 1 |
| 段 3 | 0.5 天 | 段 2 同周期 | 无 |
| 段 4 | 3-5 天 | 段 2/3 land 后 | 段 2 注册表 |

---

## 6. 结论

**REQUEST_CHANGES — 段 1 必须本 PR 闭环；段 2/3 独立 PR 紧随。**

- 方向正确不可逆，不应回退
- 段 1 是 typed envelope 自身闭环（双向 + 四件套）
- 段 2/3 是 GoCell 工具链账单，本 PR 暴露但不修也是债务，必须开独立 PR 立刻偿
- 段 4 受段 2 工具支撑，延后做以避免本批堆积

**本 PR ship 标准**：

- 14 个症状全闭，链头链尾 archtest 都绿
- `go run ./cmd/gocell check contract-health` PASS
- `go run ./cmd/gocell generate contract --all` 后 `git diff` 空（idempotent）
- golangci-lint 0 issues、`go test ./...` 全绿、`go build -tags=integration ./...` 0 errors
- ADR D6/D7 升级 land

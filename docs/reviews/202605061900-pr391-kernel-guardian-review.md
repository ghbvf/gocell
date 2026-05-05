# PR#391 Kernel Guardian Review — K#08 errcode 残余收口

> Reviewer: Kernel Guardian
> Date: 2026-05-06 19:00
> Branch: `refactor/523-k08-errcode-residual` HEAD `4b3f969d`
> Base: `origin/develop` (`ae8211b4`)
> Scope: 219 files changed (+3658 / -973), 3 commits（含 K#08 主提交 `4b3f969d`）
> ADR: `docs/architecture/202605051730-adr-errcode-message-pii-safety.md`

## 摘要

PR#391 完成 K#08 三块改造：`errcode.Assertion` ctor、`Details []slog.Attr`、`Message`
const-literal 静态约束。kernel/ 分层完整（无新增上行依赖）；契约 schema 与 testdata
已在 4 处同步（canonical + 2 examples + tests/contracttest/testdata 共 4 份）；archtest 三
件套（DETAILS-SLOG-ATTR-01、MESSAGE-CONST-LITERAL-01、ASSERTION-CTOR-01）落地。

但发现 **3 项必须修复** 的不合规项：

1. **MESSAGE-CONST-LITERAL-01 在 `adapters/postgres/schema_guard.go:150` 失败** — PR
   自带的新静态守卫无法在自己的 production 树上通过，迁移收口不完整。
2. **ERRCODE-KIND-LITERAL-01 新增 `pkg/ctxcancel/` + `pkg/httputil/` 全包 allowlist 缺
   ADR/backlog 登记** — 违反"引入新约束必须同 PR 闭环"反馈条目；allowlist 范围本可
   收窄到 file 级。
3. **doc 漂移** — 至少 4 处注释/godoc 仍描述旧 `details: {object}` wire 形式，与新
   `details: [{key,value}]` 不符。

7 维度评分：B、E 红，A、C、D、F、G 绿。

---

## Findings

### F1 [必须修复, Cx2] MESSAGE-CONST-LITERAL-01 在 production 代码上失败

**证据**：

```
$ go test ./tools/archtest/ -run TestErrcodeMessageConstLiteral -count=1
--- FAIL: TestErrcodeMessageConstLiteral (0.87s)
    errcode_message_const_test.go:114: adapters/postgres/schema_guard.go:150:
        errcode.New(...) message must be a const literal (got *ast.CallExpr)
        — move runtime data to WithDetails(slog.Attr) or WithInternal
```

`adapters/postgres/schema_guard.go:150-151`：

```go
return errcode.New(errcode.KindInternal, ErrAdapterPGQuery,
    fmt.Sprintf("schema_guard: %d invalid index(es): %s", len(indexes), strings.Join(names, ", ")))
```

dynamic count + index names 直接拼进 message。

**影响**：PR 引入的 archtest 在自己的 main 分支上 fail；CI 必然红。Kernel Guardian
原则之一是"引入新约束必须同 PR 闭环（静态守卫 + 文档契约 + 回归测试）"。此处守卫
落地了，但本 PR 的迁移没收口，违反三件套自闭环。

**建议修复**（in-scope）：

```go
return errcode.New(errcode.KindInternal, ErrAdapterPGQuery,
    "schema_guard: invalid indexes detected",
    errcode.WithInternal(fmt.Sprintf("schema_guard: %d invalid index(es): %s",
        len(indexes), strings.Join(names, ", "))),
    errcode.WithDetails(
        slog.Int("invalidCount", len(indexes)),
        // 索引名属于 schema 层标识符（公共 DDL），可作 details 公开；
        // 若视作内部，统一并入 WithInternal。
    ),
)
```

参考 ADR §"runtime 数据通道"表（message=const、details=4xx 公开、internal=服务端 only）。

**复杂度**：Cx2（替换单点 + 选择 details vs internal 归类）。

---

### F2 [必须修复, Cx3] ERRCODE-KIND-LITERAL-01 carve-out 范围与登记

**证据** — `tools/archtest/errcode_constructor_test.go:33-49`：

```go
if strings.HasPrefix(rel, "pkg/errcode/") { continue }
if strings.HasPrefix(rel, "pkg/ctxcancel/") { continue }   // 新增
if strings.HasPrefix(rel, "pkg/httputil/") { continue }    // 新增
```

实际 production struct-literal 命中点（`grep -rEn '&errcode\.Error\{' --include='*.go'`，
排除 `_test.go` / `tools/archtest/testdata`）只有 3 处：

| 文件 | 行 | 用途 |
|------|----|------|
| `pkg/httputil/response.go` | 53 | `WritePublic` 接受 caller 提供的 `message string` 参数 |
| `pkg/httputil/response.go` | 165 | 5xx public-code rewrite，message 来自 `public5xxMessage(status)` 三常量 switch |
| `pkg/ctxcancel/ctxcancel.go` | 141 | `WrapOrInfra` 接受 caller 提供的 `fallbackMsg string` 参数 |

观察：

- 实际命中只有 3 个 file/site；但 allowlist 是 **package-prefix** 粒度（整个
  `pkg/httputil/`、整个 `pkg/ctxcancel/`），过宽。
- `pkg/httputil/` 内 `path_param.go`、`request.go`、`decode.go` 共 9 个
  `errcode.New/Wrap` 调用全部使用 const literal message，本可继续受 ERRCODE-KIND-LITERAL-01
  保护（虽然该规则只检查 struct literal 形式，但 file-级 allowlist 比 package-级更稳）。
- `pkg/ctxcancel/` 内除 `WrapOrInfra` 外的 `Wrap` 调用同样使用 const literal。

**ADR / 背书空洞**：

- `docs/architecture/202605051730-adr-errcode-message-pii-safety.md` 未提及新增的
  `pkg/ctxcancel/` + `pkg/httputil/` 在 ERRCODE-KIND-LITERAL-01 上的 carve-out。ADR
  §Negative 仅预告 MESSAGE-CONST-LITERAL-01 的"误报豁免列表维护成本"。
- `docs/backlog.md` / `docs/backlog1.md` / `docs/backlog2.md` 无对应 follow-up 条目
  ("收窄 ERRCODE-KIND-LITERAL-01 carve-out 到 file-level / 在 ADR 中正式记录该 carve-out
  + WrapOrInfra 设计动机" 这类登记)。

引用反馈条目：用户的 `feedback_constraint_self_close.md` 与本任务明确要求"任何
carve-out 必须有 backlog 登记"。

**建议修复**（择一，本 PR 内）：

- **A**（推荐）：把 allowlist 改成 file-level（`pkg/httputil/response.go` +
  `pkg/ctxcancel/ctxcancel.go`），缩小命中面；调整测试 string 比较为 `==` 而非
  `HasPrefix`。
- **B**：维持 package-level allowlist，但在 ADR §Decision 1（Assertion ctor）后追加
  §Decision 5 `pkg/ctxcancel.WrapOrInfra` / `pkg/httputil.WritePublic` 的 dynamic-message
  bridge helper 设计动机；同步在 `docs/backlog.md` 登记 file-level 收窄 follow-up（Cx2，
  ~1h）。

**复杂度**：Cx3（评估 file vs package 粒度 + ADR/backlog 同步登记）。

---

### F3 [必须修复, Cx2] doc 漂移：godoc / 测试注释仍写旧 `details:{}` 形式

`Error.MarshalJSON` 已切换为 `details: array<{key,value}>`，但以下注释仍按旧 object
形式描述 wire 格式，与代码事实不一致：

| 文件 | 行 | 旧描述 | 应改为 |
|------|----|--------|--------|
| `pkg/httputil/decode.go` | 26-30, 40 | `details: {"reason": ...}` | `details: [{"key":"reason","value":...}]` |
| `runtime/bootstrap/bootstrap_test.go` | 120 | `{"error": {"details": {...}}}` | `{"error": {..., "details":[...]}}` |
| `runtime/bootstrap/bootstrap_test.go` | 126 | `"details":{}` | `"details":[]` |
| `runtime/http/health/health.go` | 440 | `"details": {"status":"unhealthy",...}` | `"details": [{"key":"status","value":"unhealthy"}, ...]` |
| `cmd/corebundle/vault_readiness_wiring_test.go` | 193 | `"details": {...}` | `"details": [...]` |

注释不影响 build，但读者根据注释构造 e2e 测试断言或客户端 SDK 时会被误导。属于
"激进三原则 / 不留向后兼容尾巴" 反馈条目（`feedback_no_soft_fallback.md`）应该收口的部分。

**复杂度**：Cx2（5 处注释机械替换）。

---

### F4 [建议加固, Cx2] schema 缺乏 `details[].key` 唯一性约束

`contracts/shared/errors/error-response-v1.schema.json`：

```json
"details": {
  "type": "array",
  "items": {
    "required": ["key", "value"],
    "additionalProperties": false,
    "properties": {"key": {"type": "string"}, "value": {}}
  }
}
```

未声明 `uniqueItems: true` 或更精确的 `unevaluatedItems` / 自定义校验。`slog.Attr` 允许
重复 key（`Error.FindAttr` 返回 first match），重复键合法但语义不清晰。客户端 SDK
按 key 查询时只会拿到第一项，第二项静默丢失。

**建议**（可登记 backlog 不强制本 PR）：

- 选项 A：schema 加 `uniqueItems: true`（基于完整 item 比较，非 key 唯一）
- 选项 B：schema 加自定义 `not.contains.duplicates` 模式 — 复杂，价值有限
- 选项 C：在 `Error.MarshalJSON` 出口去重（last-write-wins 或 first-write-wins 显式
  policy），杜绝 wire 上出现重复 key

`tests/contracttest/shared_error_schema_test.go` 也建议补一个负样本：旧
`{"details":{}}` 必须被 schema 拒绝（确认 cutover 不向后兼容）。

**复杂度**：Cx2。

---

### F5 [信息项, Cx1] kernel/ 分层与 redaction 链路完整

确认无回退：

- `kernel/` 顶层 `import` 扫描（`grep -rln 'gocell/(runtime|adapters|cells)' kernel/ --include='*.go' | grep -v _test.go`）
  返回空，无新增上行依赖。
- `pkg/` 同样扫描返回空。
- `pkg/errcode` 仍只 import 标准库（`encoding/json`、`fmt`、`log/slog`）。新增的
  `Assertion` ctor 不引入新依赖。
- panic→500 redaction 链路（`runtime/http/middleware.Recovery` →
  `recordPanicOnActiveSpan` → `panicAsError` → `redaction.RedactError` →
  `span.RecordError`）保持原样。`errcode.Assertion` 经 `panic(...)` 抛出后，
  `panicAsError` 类型 switch 第二分支命中 `case error:`（因为 `*errcode.Error`
  实现 `Error()`），格式化文本经 redaction 后写入 span。HTTP body 始终是 const
  `"internal server error"`，不携带 Assertion 的 runtime args（双层防线：5xx wire
  body 走 `errcode.New(KindInternal, ErrInternal, "internal server error")` 重建 +
  `MarshalJSON` 5xx 自动 strip details）。
- `kernel/wrapper.WrapConsumer` redaction 同上路径，`recordErr`
  （consumer.go:146）显式 `redaction.RedactError(err)`。

`errcode.Assertion` 在 kernel/runtime/wrapper 三处 panic 路径均经过现有 redaction
治理，无新增 PII 泄漏面。

---

### F6 [信息项, Cx1] 元数据合规快照

| 维度 | 状态 | 证据 |
|------|------|------|
| cell.yaml / slice.yaml 改动 | 无 | `git diff --stat origin/develop..HEAD -- 'cells/**/cell.yaml' 'cells/**/slice.yaml'` 空 |
| contract.yaml 改动 | 无 | 同上 |
| assemblies/ 改动 | 仅 `corebundle/generated/boundary.yaml` sourceFingerprint | 由工具重新生成；assembly.yaml 本身未改 |
| journeys/ 改动 | 无 | — |
| 共享 schema 同步 | 4 份齐全 | `contracts/shared/errors/error-response-v1.schema.json` + `tests/contracttest/testdata/contracts/shared/errors/error-response-v1.schema.json` + `examples/iotdevice/contracts/shared/errors/error-response-v1.schema.json` + `examples/todoorder/contracts/shared/errors/error-response-v1.schema.json` 全部从 `details: object` 改为 `array<{key,value}>` 且 `additionalProperties: false`。`diff` 三份均一致 |
| contracttest array shape 断言 | 通过 | `go test ./tests/contracttest/ -run 'TestValidateErrorResponse|TestSharedErrorSchema' -count=1` PASS |

---

### F7 [信息项, Cx1] archtest 治理一致性矩阵

| 规则 | 范围 | 与既有规则交叉 |
|------|------|---------------|
| `ASSERTION-CTOR-01`（已存在，PR#368 / 早期） | 拦截非豁免 file 中 bare panic | 与 `MESSAGE-CONST-LITERAL-01` 互补：前者管 panic 形式，后者管 New/Wrap 第三参 |
| `MESSAGE-CONST-LITERAL-01`（本 PR 新增） | `errcode.New/Wrap` 第三参必须 const literal | allowlist 仅 `pkg/errcode/` + `tools/archtest/testdata/`；fixture + production 双扫描；用 `golang.org/x/tools/go/packages` + `go/types` 精确 resolve |
| `DETAILS-SLOG-ATTR-01`（本 PR 新增） | `WithDetails(...)` 不允许 map[string]any | 仅纯 AST，allowlist `pkg/errcode/` + testdata；fixture 验证 |
| `ERRCODE-KIND-LITERAL-01`（已存在，PR 内扩 allowlist） | 禁止 `&errcode.Error{...}` 直接构造 | allowlist 新增 `pkg/ctxcancel/` + `pkg/httputil/`，见 F2 |

**交叉空洞检查**（无）：

- `errcode.Assertion(...)` 路径：内部用 `fmt.Sprintf` 构造 message，**不**走 `New/Wrap`
  第三参检查（Assertion 自己显式调用 `New(KindInternal, ErrInternal, fmt.Sprintf(...))`，
  在 `pkg/errcode/` 内，已被 allowlist 豁免）— 设计正确。
- `WithInternal(fmt.Sprintf(...))`：不在 MESSAGE-CONST 检查范围（仅检查 `New/Wrap`
  index 2），符合 ADR 表"internal=服务端 only"。
- `ASSERTION-CTOR-01` 与 `MESSAGE-CONST-LITERAL-01` 协同：Assertion 是非 const
  message 的唯一合法逃生阀（在 `pkg/errcode/` 内豁免），bare panic 由 ASSERTION-CTOR
  拦截，runtime 数据 wire 泄漏由 MESSAGE-CONST 拦截 + 5xx strip 兜底。

---

### F8 [信息项] codegen template 集成

`tools/codegen/contractgen/templates/handler.tmpl:65,80` 把
`panic(fmt.Sprintf("..., err"))` 替换为 `panic(errcode.Assertion("...", err))`。
20 个生成产物（`generated/contracts/http/.../handler_gen.go`）静态扫描全部已迁移。

`generated/` 目录被 `tools/internal/fileroles.IsProductionCode` 显式跳过（line 71），
因此不参与 MESSAGE-CONST-LITERAL-01 扫描。但 ERRCODE-KIND-LITERAL-01 用的
`collectGoFiles` 不跳 `generated/`；扫描通过（`go test … TestErrcodeLiteralConstructionBanned`
PASS），证实生成产物未触发 struct-literal 形式。

---

## 7 维度评审

| 维度 | 评分 | 证据 / 结论 |
|------|------|------------|
| A. 工作流完整性 | 绿 | S0-S8 完整：ADR 落地（202605051730-adr-errcode-message-pii-safety.md）+ 三块改造分别列于 ADR Decision 1/2/4 + archtest 三件套 + golden 同步 + 4 份 schema 同步 |
| B. 工具合规 | 红 | F1：MESSAGE-CONST-LITERAL-01 在 production tree 上 fail；PR 主旨是工具守卫但未自闭环 |
| C. 角色完整性 | 绿 | Architect / Test / Lint 角色覆盖（ADR 写明，contracttest 覆盖 schema array shape，archtest fixtures 双向覆盖 compliant/violates） |
| D. 内核集成健康度 | 绿 | F5：kernel/ 无新增 import，redaction 链路完整，5xx strip 在 `MarshalJSON` 单源治理 |
| E. 标准文件齐全度 | 红 | F2 + F3：ADR 缺 ERRCODE-KIND-LITERAL-01 carve-out 记录、backlog 缺 file-level 收窄登记；godoc 漂移 5+ 处 |
| F. 反馈闭环 | 绿 | 主线（K#08）按 029 roadmap §Wave1-G 推进；ADR/计划同步 |
| G. Tech Debt 趋势 | 绿（边缘） | 移除 `attrsToMap` (~30 行) + 19 contracts gen handler panic 路径统一 + outbox/registry 类零散 panic 收口；本 PR 引入的新债（F2 carve-out 范围 + F3 doc 漂移）≤ 解决量 |

---

## 必须修复（≤ 3）

1. **F1**（Cx2）：`adapters/postgres/schema_guard.go:150` 的
   `errcode.New + fmt.Sprintf` 必须迁移到 `WithInternal`/`WithDetails` 通道，让
   `MESSAGE-CONST-LITERAL-01` 在自己的 main tree 通过。
2. **F2**（Cx3）：要么把 ERRCODE-KIND-LITERAL-01 的
   `pkg/ctxcancel/` + `pkg/httputil/` allowlist 收窄到 file-level（`response.go` +
   `ctxcancel.go`），要么在 ADR + backlog 登记 package-level carve-out 决策（一次
   性闭环，不甩 review）。
3. **F3**（Cx2）：5 处 godoc/comment 漂移修正（`pkg/httputil/decode.go` 26-30/40、
   `runtime/bootstrap/bootstrap_test.go` 120/126、`runtime/http/health/health.go`
   440、`cmd/corebundle/vault_readiness_wiring_test.go` 193），统一描述
   `details: array<{key,value}>`。

---

## 建议（非阻塞）

- **F4**：`details[].key` 唯一性 schema 约束 / `MarshalJSON` 出口去重；contracttest
  补"旧 `{}` object 必须被拒"负样本。可登记 backlog（Cx2）。
- 出口路径（`pkg/httputil/response.go`）2 处 `&errcode.Error{...}` struct literal 的
  保留 + `pkg/ctxcancel/ctxcancel.go` 1 处，长期最好通过新增 `errcode.NewWithMessage(kind,
  code, msg)`（明确允许动态 msg 但仍走 ctor）替代 struct literal，统一构造路径，让
  ERRCODE-KIND-LITERAL-01 不必 carve-out 任何 production 包。属于 design refactor，
  ADR 应留口子并 backlog 登记。

---

## 引用

- ADR: `docs/architecture/202605051730-adr-errcode-message-pii-safety.md`
- Plan: `docs/plans/202605011500-029-master-roadmap.md` Track K#08 W1-G
- Plan: `docs/plans/202605051600-030-review-0504-implementation.md` Wave 2
- Constitution: `.claude/rules/gocell/error-handling.md` §"Message PII 静态字面量约束"
  / §"Assertion vs panic" / §"Details 类型安全"
- Constitution: `.claude/rules/gocell/observability.md` §"errcode 三层 redaction 分工"
- Feedback: `feedback_constraint_self_close.md`（引入新约束必须同 PR 闭环）

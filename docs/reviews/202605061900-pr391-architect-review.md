# PR#391 Architect Review — K#08 errcode 残余三块改造

> Branch: `refactor/523-k08-errcode-residual` HEAD `4b3f969d`
> Base: `origin/develop`
> Date: 2026-05-06
> Reviewer role: Architect (架构师)
> Scope: GoCell 分层完整性 / 接口稳定性 / 单源治理 / 一致性级别 / 可扩展性 / 依赖方向

## TL;DR

整体架构合理，三层 redaction（Message const / Details `[]slog.Attr` / Internal）边界清晰，单源治理通过 `Error.MarshalJSON()` 唯一出口达成；wire schema 4 副本完全一致；分层依赖未引入任何反向边。`errcode.Assertion` ctor 的 panic 词汇收口 与现有 `PANIC-REGISTERED-01` archtest 配合良好。

主要架构关注点集中在两处文档/规则一致性偏差（不阻塞）：(1) 新 ADR 提到的 "C 类 6 处豁免" 与实际 archtest 强制 4 项白名单语义不一致，名词需对齐；(2) `pkg/ctxcancel` / `pkg/httputil` 加入 `ERRCODE-KIND-LITERAL-01` allowlist 是合理的「桥层动态消息」豁免，但豁免理由应从 archtest 注释提升到 ADR 主文，便于未来评估边界扩张。

---

## Findings

### 1. [单源治理 / 接口稳定性] Cx2 — `Error.MarshalJSON` 是 details strip 的唯一出口，wire 一致性已闭环

`pkg/errcode/errcode.go:681-705` 的 `MarshalJSON` 是 `details: array<{key,value}>` 与 5xx strip 的唯一序列化点：所有出口路径（`pkg/httputil/response.go:177-199` 的 `writeErrorBody` 用 `json.Marshal(ecErr)`，`writeErrcodeError` 已先把 5xx 替换为 sanitized sentinel `&errcode.Error{Kind, Code: publicCode, Message: public5xxMessage(...)}`，再传给 writeErrorBody）都收敛到 `MarshalJSON`。`Recovery` middleware (`runtime/http/middleware/recovery.go:60`) 也走 `httputil.WriteError → writeErrcodeError`。

代码搜索确认无任何生产路径直读 `.Details[...]` 并自行序列化（命中只在 errcode_test 与 query/cursor_test 内部断言）。`MarshalJSON` 中的 5xx 判定使用 `e.Kind.IsClient()`，与 `writeErrcodeError` 的 `status >= 500` 判定**双重保险**——即便上游忘了替换 sentinel，details 仍会被 strip。

**评分：合理**。单源治理完整、双层 fail-closed。

---

### 2. [一致性 / 文档对齐] Cx2 — ADR 列出 "C 类 6 处豁免"，archtest 实际仅锁 4 项；语义口径需统一（不阻塞）

`docs/architecture/202605051730-adr-errcode-message-pii-safety.md:53-55` 与 `.claude/rules/gocell/error-handling.md` 的 "Assertion vs panic" 段都声明 C 类共 6 处豁免：`lifecycle / circuit_breaker / tx_manager / websocket handler / metrics / kernel cell bootstrap fatal`。

但 `tools/archtest/panic_registered_test.go:23-32` 的 `architecturalPanicWhitelist` 实际仅 4 项（lifecycle/recoverAndFinish, circuit_breaker/repanicAfterBreakerFailure, tx_manager/repanicAfterTopLevelTxRollback, tx_manager/repanicAfterSavepointRollback），其余两项（websocket / metrics）是通过 `Must*` 前缀**自动豁免**而非 ADR-pinned；"kernel cell bootstrap fatal" 实际已迁到 `errcode.Assertion`（如 `kernel/cell/registry.go:411,425,448,456,500`）不再是 bare panic。

所以 ADR 的 "6 处" 把两个治理通道（"硬编 ADR 白名单 4" + "Must* 前缀自动豁免 2") 写成了一类，形成口径偏差。建议在新 ADR `202605051730` 显式说明：
- "硬编白名单 4 处"（指向 `202604270030-architectural-panic-whitelist.md`）
- "Must* 自动豁免" 类目（websocket/metrics 等 Must* 函数的 panic 内嵌）
- "kernel cell bootstrap" 已转 `errcode.Assertion`，不在 panic 豁免之列

**评分：低风险（文档命名偏差），但与新 archtest `MESSAGE-CONST-LITERAL-01` 的 PII 守卫相邻，长期维护需要清晰口径**。建议本 PR 内调整 ADR 文字（约 5 行），无代码改动。

---

### 3. [分层一致性 / 桥层豁免] Cx2 — `ERRCODE-KIND-LITERAL-01` 加 `pkg/ctxcancel/` `pkg/httputil/` allowlist 合理，但豁免理由需提升到 ADR 主文（不阻塞）

`tools/archtest/errcode_constructor_test.go:40-48` 在 `pkg/ctxcancel/` `pkg/httputil/` 加白名单允许 struct literal `&errcode.Error{Kind: ..., Message: <dynamic>}`。理由由 archtest 内联注释承载：

- `pkg/ctxcancel.WrapOrInfra`（`pkg/ctxcancel/ctxcancel.go:133-142`）的 `fallbackMsg` 必须接受 caller 传入的字符串字面量，桥接到 errcode struct literal；调用方都是 const literal，桥层只是值透传。
- `pkg/httputil.WritePublic` / `writeErrcodeError`（`pkg/httputil/response.go:37-54, 146-169`）构造 5xx sanitized sentinel 与 framework-controlled message。

判定：**这不是分层职责模糊，而是合理的「plumbing 层桥接」**。理由：
1. ctxcancel 是 IO-边界 helper，按 ADR `errcode-message-pii-safety` 的设计意图，桥层接受调用方动态字符串、由调用方负责 const literal 是单源治理的合理切分（vs 把 Sprintf 展开下推到每个调用点，会**反而**让 PII 风险扩散）。
2. httputil 是 wire 序列化层，5xx sanitization 必须用 framework-controlled fixed messages（"internal server error" / "gateway timeout" / "service unavailable"），struct literal 是为了避免 New 必须接受 dynamic kind+code+message 的笨拙签名。

**架构建议**：本 PR 在新 ADR `202605051730` 中追加一节 "Bridge layer exemptions"，记入两处 pkg 豁免与判定依据，避免未来 reviewer 把它当遗漏。无代码改动。

---

### 4. [接口稳定性 / wire schema] Cx1 — `details: object → array` 4 副本完全一致，破坏性变更口径正确

contracts/shared/errors/error-response-v1.schema.json 的 4 个 in-tree 副本（`contracts/`, `examples/iotdevice/contracts/`, `examples/todoorder/contracts/`, `tests/contracttest/testdata/contracts/`）我已逐字节比对，全部为同一份 schema，`details` 字段统一为：

```json
{"type":"array","items":{"type":"object","required":["key","value"],"additionalProperties":false,...}}
```

GoCell 宪法明确 "Review 和重构时不考虑向后兼容——当前只有 gocell 自身，没有外部调用方"，破坏性变更口径正确；error-response-v1 schema 仍保留 `additionalProperties: false`（与 `api-versioning.md` 第1条 "shared error envelope 例外保持 strict" 一致）。

**评分：合理**。

---

### 5. [分层依赖] Cx1 — pkg → kernel/runtime/adapters 反向依赖检查通过

复核改动涉及的 4 个 pkg 子包：
- `pkg/errcode` 仅 import 标准库（`encoding/json`, `fmt`, `log/slog`）— 干净。
- `pkg/ctxcancel` import `pkg/errcode`（同层）+ 标准库 — 干净。
- `pkg/httputil/response.go` import `pkg/ctxcancel`, `pkg/ctxkeys`, `pkg/errcode`（同层）+ 标准库 — 干净。
- `pkg/redaction` 不在本 PR 改动范围；与本 PR 设计上 message-PII 隔离正交（message 不走 redaction，靠静态守卫；redaction 仍处理 span error / outbox last_error）。

无任何 `pkg/` → `kernel/` / `runtime/` / `adapters/` / `cells/` 反向依赖被引入。`go-standards.md` 第1表静态约束保持。

**评分：合理**。

---

### 6. [archtest 治理 / 可扩展性] Cx2 — `MESSAGE-CONST-LITERAL-01` 用 `go/types` 精确解析常量，避开 false negative，但 fixture-mode AST fallback 接受所有 Ident/Selector — 需注意未来新增 fixture 不能依赖此宽松路径

`tools/archtest/errcode_message_const_test.go:221-245` `isAcceptableMessageExpr` 在 `info != nil`（生产模式）下严格要求 `*types.Const`；`info == nil`（fixture 模式）下放宽为接受所有 Ident/SelectorExpr，注释解释 "fixture 不能 type-check，且关心的 violation 是 CallExpr/BinaryExpr 形态"。

**架构建议**：fixture-mode fallback 是务实选择，但本 PR 应在 archtest godoc（已有部分内容）中显式声明 "**fixture 必须保留至少一个 CallExpr 形态的 violation**，不能仅靠 Ident-bound runtime variable 来表达 violation"。否则未来加 fixture 时容易误以为 fixture-mode 等同生产模式严格性，造成漏检。

`scanErrcodeMessageAST` 第三参数位置 hard-coded（New 与 Wrap 都是 index=2）— 如果未来新增 ctor（如 `NewWithCause`，签名不同），必须同步更新此处。建议在 `errcode.go` 的 New/Wrap godoc 加一行 "新增同名 ctor 必须同步更新 archtest MESSAGE-CONST-LITERAL-01 的 index"。

**评分：低风险，可在本 PR 内补 5 行注释闭环**。

---

## 综合评分

| 维度 | 评分 | 备注 |
|------|------|------|
| 分层完整性 | 优秀 | pkg/ 仅依赖标准库 + 同层 pkg；无反向边 |
| 接口稳定性 | 优秀 | 破坏性变更口径明确；wire schema 4 副本一致 |
| 单源治理 | 优秀 | `MarshalJSON` 唯一出口；5xx strip 双层 fail-closed |
| Cell 边界 | n/a | 本 PR 不涉及 Cell 划分 |
| 一致性级别 | n/a | 本 PR 不涉及 CUD 操作的 L0-L4 |
| 性能与可扩展性 | 良好 | `[]slog.Attr` 替代 `map` 减少分配；MarshalJSON 重新分配但只在错误路径 |
| 依赖方向 | 优秀 | 无反向边 |

## 建议合入决策

**接受合入**。三处建议（Finding 2 / 3 / 6）均为文档/注释级别小调整，建议在本 PR 内闭环（共约 15 行 ADR + godoc 修改），不涉及代码逻辑或新 archtest，不阻塞合入。

## 引用

- `pkg/errcode/errcode.go:625-705` — WithDetails / Error / MarshalJSON
- `pkg/errcode/errcode.go:746-795` — Assertion / New / Wrap godoc
- `pkg/httputil/response.go:146-199` — writeErrcodeError / writeErrorBody
- `runtime/http/middleware/recovery.go:60` — Recovery → WriteError
- `tools/archtest/details_slog_attr_test.go` — DETAILS-SLOG-ATTR-01
- `tools/archtest/errcode_message_const_test.go` — MESSAGE-CONST-LITERAL-01
- `tools/archtest/errcode_constructor_test.go:40-48` — pkg/ctxcancel + pkg/httputil allowlist
- `tools/archtest/panic_registered_test.go:23-32` — 4-entry whitelist
- `docs/architecture/202605051730-adr-errcode-message-pii-safety.md` — 本 PR ADR
- `docs/architecture/202604270030-architectural-panic-whitelist.md` — panic 治理 ADR
- `contracts/shared/errors/error-response-v1.schema.json` + 3 副本 — wire schema

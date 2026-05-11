# PR #450 文档工程师审查报告

**审查日期**: 2026-05-11
**审查者**: 文档工程师（兼新人导师）
**PR Branch**: `refactor/554-pg-s7-audit-ledger` → base `develop`
**审查基础**: `git diff origin/develop...HEAD`

---

## 总体判断

文档质量整体良好，是 GoCell PR 中 godoc 覆盖最完整的一次。`runtime/audit/ledger/` 包的 doc.go + storetest + archtest 构成了可自助上手的文档三角。主要缺陷集中在两处：**doc.go 与 protocol.go 的幂等指纹描述与实际实现不符**（P1），以及 **observability.md audit 小节的 key 列表与 `pkg/redaction` 单源不同步**（P2）。

ADR 结构完整，引用框架准确，Migration 编号已正确反映 rebase 后的最终序号（021）。backlog 条目符合"PR 范围切割必须显式 backlog"原则。

---

## Findings

### DOC-01 — P1 / Cx1

**文件**: `runtime/audit/ledger/doc.go:45-47`  
**文件**: `runtime/audit/ledger/protocol.go:59-63`

**问题**: 两处对 `IdempotencyContentFingerprint` 的描述均称指纹字段为 `eventID + eventType + actorID + UnixNano(timestamp) + payload`（多字段），与实际实现不符。

实际实现（`mem_store.go:contentFingerprint`、`audit_ledger_store.go:selectFingerprintSQL`、ADR D3）均明确：**指纹 = SHA-256(EventID only)**。多字段描述是 F-CR-2 修订前的旧形态，已被 ADR D3 废弃，但 godoc 未同步。

此外 protocol.go 称算法为 "HMAC-SHA256 fingerprint"，实际实现用的是 plain SHA-256（`sha256.New()`，不使用 HMAC key），术语也不准确。

**建议**:
- `doc.go` 第 45-47 行改为：`IdempotencyContentFingerprint uses SHA-256(EventID) as the idempotency key. EventID (outbox entry UUID) is stable across at-least-once redeliveries; Timestamp and Payload are excluded (F-CR-2).`
- `protocol.go` 第 59-63 行同步修正，删除多字段列表，删除 "HMAC-SHA256 fingerprint" 表述，改为 "SHA-256 of EventID"

---

### DOC-02 — P2 / Cx1

**文件**: `.claude/rules/gocell/observability.md:87`（Audit Payload Redaction 小节）

**问题**: 小节列出的敏感 key 列表为：
```
password / passwd / pwd / secret / token / api_key / authorization / private_key / signing_key
```

而 `pkg/redaction` 单源 `sensitiveKeyPattern` 实际包含更多 token aliases：
```
access[_-]?token | refresh[_-]?token | id[_-]?token | bearer | dsn | connection[_ ]?string
```

observability.md 称"与 `pkg/redaction.RedactError` 同源 key 列表"，但列出的列表已偏离单源。

**附加问题**: 同一小节描述 fail-closed 行为为"整段替换为 `<REDACTED>` 字符串"，实际返回的是 `jsonMaskString = []byte('"<REDACTED>"')` —— 是合法 JSON string token，而不是裸字符串。这一区别对 `json.RawMessage` 嵌入场景有影响。

**建议**: 
- 移除 key 列表的内联枚举，改为 `ref: pkg/redaction/redaction.go sensitiveKeyPattern（单源，详见该文件）`
- fail-closed 描述改为：`不可解析为 JSON 的 payload 整段替换为 JSON string token \`"<REDACTED>"\`（合法 JSON，可安全嵌入 json.RawMessage）`

---

### DOC-03 — P2 / Cx2

**文件**: `docs/architecture/202605101800-adr-audit-ledger-protocol.md:1`（命名）

**问题**: CLAUDE.md 规定文档命名格式为 `yyyyMMddHHmm-编号-实际功能或问题.md`，示例为 `202603281443-022-compliance-api-review.md`（其中编号为数字）。

本 ADR 文件名为 `202605101800-adr-audit-ledger-protocol.md`，"编号"位置用的是 `adr` 字面量而非数字编号。对照 `docs/architecture/` 目录，所有已有 ADR 均遵循相同的 `adr-xxx` 命名模式（如 `202605051600-adr-pg-outbox-fencing.md`）。

**评估**: 这是全仓库一致的 ADR 命名惯例，不是本 PR 引入的偏差。`docs/reviews/` 和 `docs/plans/` 的文件使用数字编号（如 `022-...`、`034-...`），ADR 文件使用功能描述命名是实践惯例。**不构成真正 defect，标注供人工确认是否需要在 CLAUDE.md 澄清"ADR 文件不要求数字编号"。**

---

### DOC-04 — P2 / Cx1

**文件**: `docs/architecture/202605101800-adr-audit-ledger-protocol.md` §1.2

**问题**: ADR §1.2（typed-Go-heavy 范式中的位置）描述：

> - PG-backed Store（S8 PR 实施）
> - auditcore cell 接入（S9 PR 实施）

但实际上 PG Store 和 auditcore cell 接入均在本 PR（S7）完成，`docs/plans/202605082145-034-pg-corecell-b-route-plan.md` 也已更新 S7 为 `✅ shipped`。

ADR §1.2 的"S8/S9"是最初写作时的计划，未随实施提前而同步修订，与当前状态不符。

**建议**: 将 §1.2 相关行更新为"（已在本 PR 实施）"或删除 Sprint 编号标注，避免历史阶段标号误导后续读者。

---

### DOC-05 — P2 / Cx1

**文件**: `adapters/postgres/audit_ledger_store.go:90-108`（`LedgerStore` godoc）

**问题**: `LedgerStore` 类型 godoc 引用了 migration 021（两处），但未引用 migration 020（建表）。完整理解 `LedgerStore` 的 DDL 来源需要两个 migration，只引用 021 会让读者不知道表结构来自哪里。

**建议**: 在 godoc 的 `Consistency level` 行之后加一行：
```
// DB schema: migration 020 (audit_entries table) + migration 021 (event_id UNIQUE INDEX).
```

---

### DOC-06 — P3 / Cx1

**文件**: `runtime/audit/ledger/doc.go:40-41`

**问题**: doc.go 的 Restart Recovery 小节引用：
```
ref: google/trillian log/sequencer.go — IntegrateBatch verifies tree integrity
```
正确。但整个 doc.go 没有关于 **L1 LocalTx 一致性级别**的说明。新人阅读 doc.go 后不知道 `ledger.Store` 在 L1 和 L2 场景下的使用差异（L1：仅 `store.Append`；L2：`store.Append + outbox.Emit` 同一 `RunInTx`）。

`LedgerStore` 的 godoc 有此说明，但 `Store` 接口的 godoc 和 `doc.go` 均未提及。

**建议**: 在 doc.go 末尾新增一节 `# Consistency Level`，说明 L1/L2 使用模式，指向 `LedgerStore.Append` 的 godoc 和 ADR。

---

### DOC-07 — P3 / Cx1

**文件**: `.claude/rules/gocell/observability.md:85-91`（Audit Payload Redaction 小节）

**问题**: 该小节第二条 bullet 称"不可解析为 JSON object 的 payload（数组 / 标量 / 不合法 JSON）整段替换"，但根据 `pkg/redaction/redaction.go:280-285` 的更新（F-CR-3），`RedactPayload` 现在**接受并递归遍历顶层 JSON 数组**，不再将其整段替换。只有完全不合法的 JSON 才会 fail-closed 替换。

**建议**: 修正为"不合法 JSON 整段替换为 `"<REDACTED>"`（JSON string token）；顶层数组和 scalar 类型正常遍历或透传"。

---

### DOC-08 — P3 / Cx2

**文件**: `cmd/corebundle/audit_module.go:31-32`（godoc 环境变量说明）

**问题**: `Provide` 方法的 godoc 说明了三个环境变量：`GOCELL_AUDITCORE_HMAC_KEY`、`GOCELL_AUDITCORE_CURSOR_KEY`、`GOCELL_AUDITCORE_CURSOR_PREVIOUS_KEY`，但未说明：
1. `GOCELL_AUDITCORE_HMAC_KEY` 必须 ≥ 32 字节（ADR D1 / `WithChainHMAC` 的 RFC 2104 §3 要求）
2. 生产模式下使用 demo key 会被 `rejectDemoKey` 拒绝

新人在 real 模式部署时容易因密钥长度不足而遇到不明错误。

**建议**: 在 godoc 中加一行：`GOCELL_AUDITCORE_HMAC_KEY must be ≥ 32 bytes (RFC 2104 §3); real mode rejects the dev default.`

---

## godoc 覆盖率统计

### `runtime/audit/ledger/` 包（5 文件）

| 文件 | 导出符号数 | 有 godoc | 缺 godoc |
|------|-----------|---------|---------|
| `doc.go` | — | package-level 完整 | — |
| `protocol.go` | 12（RestartRecoveryMode、RestartRecoveryStrictTailVerify、IdempotencyMode、IdempotencyContentFingerprint、NamespaceID、Protocol、Option、NewProtocol、MustNewProtocol、WithChainHMAC、WithNamespace、WithRestartRecovery、WithIdempotency + HMACKey/Namespace/RestartRecovery/Idempotency/ComputeHash 5 方法 = 18 总）| 18/18 | 0 |
| `store.go` | TailSnapshot、AuditFilters、QueryListParams、Store | 4/4 | 0 |
| `entry.go` | Entry | 1/1 | 0 |
| `mem_store.go` | MemStore、NewMemStore + 6 方法 + 2 MustTamper | 全部 | 0 |
| **合计** | **~28 导出符号** | **28/28** | **0** |

`runtime/audit/ledger/` 导出符号 godoc 覆盖率：**100%**

### `adapters/postgres/audit_ledger_store.go`

| 符号 | godoc | 备注 |
|------|-------|------|
| `LedgerStore` | 有 | 完整，含 advisory lock + FOR UPDATE + L1 + F-CR-2 |
| `NewLedgerStore` | 有 | 列出 4 个 nil 校验路径 |
| `Append` | 有 | 7 步算法说明 |
| `Tail` | 有 | |
| `Probes` | 有 | |
| `GetBySeq` | 有 | |
| `Query` | 有 | |
| `Verify` | 有 | 含 sub-range 说明 |
| **合计** | **8/8** | **100%** |

### `pkg/redaction/redaction.go`

| 符号 | godoc | 备注 |
|------|-------|------|
| `Mask` | 有 |  |
| `RedactString` | 有 | |
| `RedactError` | 有 | fail-closed 策略说明 |
| `RedactPanic` | 有 | |
| `RedactSlogAttr` | 有 | |
| `RedactPayload` | 有 | fail-closed + F-CR-3 递归遍历 + json.RawMessage 注意 |
| `RedactAny` | 有 | |
| `TruncateString` | 有 | |
| **合计** | **8/8** | **100%** |

### `cells/auditcore/internal/appender/`

| 文件/符号 | godoc | 备注 |
|----------|-------|------|
| `doc.go` | 有 | 解释 why internal/appender + 4 slices 关系 + AI-rebust 4 防线 |
| `Service`（service.go） | 有 | |
| `NewService` | 有 | 含 OUTBOX-SERVICE-01 标注 |
| `HandleEvent` | 有 | Consumer 声明完整 |
| `Option`、`WithEmitter`、`WithTxManager` | 有 | |
| `ActorMode`、`ActorAcceptUserFallback`、`ActorRequireExplicit` | 有 | |
| `Spec`、`MustNewSpec`、`Name`、`Mode` | 有 | |
| `extractActor` | 有（unexported，有完整说明）| |
| **合计** | **全部有** | **100%** |

---

## 文档分类亮点（值得保留的样板）

### 1. `runtime/audit/ledger/mem_store.go:contentFingerprint()`

```go
// contentFingerprint computes a SHA-256 hex digest over the entry's stable
// identity: EventID (UUID). At-least-once redelivery produces the same EventID
// regardless of when the re-delivery occurs...
//
// Fields deliberately excluded:
//   - EventType, ActorID: stable per-event metadata but redundant when EventID
//     is globally unique...
//   - Timestamp (clk.Now()): changes on every redelivery — including it would
//     produce a different fingerprint for each retry, defeating idempotency.
//   - Payload: may differ due to schema evolution; EventID is the stable key.
//
// ref: Watermill router.go — message.UUID as dedup key
// ref: NServiceBus MessageDeduplicationBehavior — message ID as idempotency key
```

"Fields deliberately excluded" 结构是记录设计决策负空间的最佳实践——比只说"我们做了什么"更有价值。

### 2. `adapters/postgres/audit_ledger_store.go:Append()`

```go
// F2: advisory lock (step 2) must precede fingerprint check (step 3) so that
// concurrent Appends with identical content cannot both pass the fingerprint
// check and both insert.
```

内联标注 TOCTOU 顺序约束，与锁顺序规则绑定，避免后续维护者无意重排顺序。

### 3. `cells/auditcore/internal/appender/doc.go`

AI-rebust 四防线清单（Hard/Hard/Hard/Medium）直接写在 package doc 里，让 reviewer 在看代码前就知道防线层级分布。符合 `.claude/rules/gocell/ai-collab.md` 要求。

### 4. `runtime/audit/ledger/storetest/suite.go:runIdempotencyDifferentTimestampSameEventID()`

```go
// At-least-once outbox redelivery produces the same EventID but a new clk.Now()
// timestamp on each attempt. The old multi-field fingerprint... would produce a
// different fingerprint each time and allow the same event to be appended
// multiple times. The EventID-only fingerprint detects the duplicate regardless
// of the timestamp difference.
```

测试函数注释直接说明了"为什么有这个测试"（回归保护），是测试即文档的示范。

### 5. `tools/archtest/auditcore_appender_single_source_test.go` 文件头 godoc

明确说明：(1) Slice 允许携带什么；(2) 禁止什么；(3) AI-rebust 评级（Medium）；(4) Hard 防线在哪里。这种"archtest 为何存在 + 保护范围"结构是 archtest 文档的推荐格式。

---

## 新人 Onboarding 审查

### 链路完整性评估

| 节点 | 状态 | 说明 |
|------|------|------|
| README → auditcore | 通 | README:385 有一行简介："Tamper-proof audit trail with HMAC-SHA256 hash chain (4 Slices)" |
| README → runtime/audit/ledger | 不通 | README 未提及 `runtime/audit/ledger` package 的存在，新人不知道底层框架 |
| doc.go → ADR | 通 | doc.go:60 直接引用 ADR 路径 |
| doc.go → storetest | 间接通 | storetest/suite.go 有完整 package doc，但 doc.go 没有直接链接到 storetest |
| ADR → migration | 通 | D3 节引用 `021_audit_entries_event_id_unique.sql` |
| composition root → 使用示例 | 通 | `cmd/corebundle/audit_module.go` + `examples/ssobff/app.go:buildSSOBFFAuditCore` 两处示例 |
| 术语表 → ledger 词汇 | 不通 | `docs/architecture/glossary.md` 无 `ledger.Store`、`NamespaceID`、`TailSnapshot`、`ContentFingerprint` 等词条 |

### 新人路径演练

一个新接入者要写 audit ledger 消费者，当前可以完成：

1. `runtime/audit/ledger/doc.go` 读懂整体范式（约 5 分钟）
2. `runtime/audit/ledger/store.go` 理解 Store 接口（完整 godoc）
3. `runtime/audit/ledger/storetest/suite.go` 看 conformance spec，理解幂等/验证/并发语义
4. `cmd/corebundle/audit_module.go` 看 composition root 如何构造 Protocol + Store
5. `examples/ssobff/app.go:buildSSOBFFAuditCore` 看完整 example wiring

无法自助完成（需要问人）：

- 不清楚 `GOCELL_AUDITCORE_HMAC_KEY` 必须 ≥ 32 字节（仅在 `WithChainHMAC` 和 ADR 有说明，不在环境变量文档中）
- 不清楚 `IdempotencyContentFingerprint` 实际只对 EventID 指纹（doc.go 和 protocol.go 说的是多字段，实现是 EventID-only）—— **这是 DOC-01 的直接伤害**
- 术语表中没有 `TailSnapshot`/`NamespaceID` 的业务语义说明

---

*审查文件路径: `docs/reviews/PR-450/doc-engineer.md`*

# ADR-1042: Outbox Wire Envelope — Principal 族 + OccurredAt 补齐 + audit ledger HMAC chain canonical rewrite

**Status**: Accepted
**Date**: 2026-05-28 (audit-side rewrite landed 2026-05-29)
**Issue**: #1042 (D1/W0 — 005 framework capability roadmap), #1228 (PR #1218 withdrawal — DROP+CREATE canonical rewrite)
**Related**: PR #1035 (errcode sealed details), ADR `202605221000-adr-outbox-observability-envelope.md`, ADR `202605101400-adr-credential-session-protocol.md` §A8 (DROP COLUMN invariant inventory template)

## PR delivery split

This ADR is implemented across two physical PRs because the audit ledger and
outbox sides are independently reviewable and the audit-side rewrite has a
hard deadline (PR #1218 retraction):

- **PR-A1** (this PR, issue #1228): audit ledger DROP+CREATE rebuild +
  11-field canonical-JSON HMAC + archtest `AUDIT-HASH-INPUT-FROZEN-01`.
  Lands all Decision points that touch `runtime/audit/ledger/*`,
  `adapters/postgres/audit_ledger*`, and `043_audit_entries_v2.sql`.
- **PR-A2** (follow-up, issue #1229): outbox-side `Entry` / `PrincipalMetadata`
  sealed construction (kernel/outbox/* with unexported fields + `NewEntry`
  constructor + 241 callsite literal rewrites), migration 042 outbox_entries
  rebuild, `kernel/wrapper` Principal span attrs, `pkg/ctxkeys` typed key
  pairs, `cells/auditcore` appender wiring to consume `outbox.Entry.Principal`,
  and auditquery output policy tightening (C3 decision in #1229). #1229
  upgrades the funnel from Medium upstream (archtest caller allowlist) to
  Hard upstream (type-system unexported gate), so `PRINCIPAL-SEALED-FIELD-FROZEN-01`
  archtest from the original PR #1218 plan may be retired in favor of the
  compile-time gate. Lands all Decision points that touch `kernel/outbox/*`,
  `kernel/wrapper/*`, `pkg/ctxkeys/*`, and outbox-side `cells/auditcore`
  derivation.
- **#1219** (auditquery API exposure): separate follow-up to extend the
  auditquery contract.yaml + handler to surface the 5 new audit_entries
  columns to API consumers. Sequenced after PR-A2 / #1229 because #1229 §4
  may rewrite the output policy (e.g. remove `SessionID` from the wire DTO
  to align with `pkg/redaction` sensitive-key set).

Per-Decision PR assignment is recorded inline below; the §"Implementation
matrix" table summarises the cross-PR layout.

## Context

005 W0 杠杆点：让 `outbox` wire envelope 在 Principal / Correlation /
Time-Causality 三族字段集上具备 Hard funnel —— schema 固定、类型保证、跨包
不可表达伪造、producer 端 fail-fast、consumer 端自动还原。W0 解锁 W2 HTTP
Idempotency、W3 Command Bus、`/correlate` 等下游缺口。

**探索发现 issue 描述 stale，实际状态**：

| 族 | 当前 | W0 闭环 | PR 落地 |
|----|------|--------|--------|
| Correlation | ✅ 已 Hard sealed — `ObservabilityMetadata` typed + `wireMessage` unexported + 双 archtest（`SAFEID-WIREMESSAGE-USAGE-01` / `SAFEID-UPSTREAM-FUNNEL-HARD-01`） | 无改动 | — |
| Principal | ❌ 缺失 | 加 4 字段 typed struct `PrincipalMetadata`（镜像 ObservabilityMetadata） | PR-A2 |
| Time-Causality | 🟡 outbox `Entry.CreatedAt`（store INSERT 时间） | 加 `outbox.Entry.OccurredAt time.Time` + audit `ledger.Entry.OccurredAt time.Time`（producer-domain 事件时间） | PR-A2（outbox）/ PR-A1（audit） |
| Audit chain | 🟡 6-field pipe HMAC，无 Principal 覆盖 | 11-field canonical JSON HMAC + audit_entries 表 DROP+CREATE 重建 + `AUDIT-HASH-INPUT-FROZEN-01` 双向 Hard 锁 | PR-A1 |

## Decision

### 1. 设计载体 — 镜像 sealed construction，拒 codegen funnel （PR-A2）

issue body 提"codegen funnel + golden"，复核后否决：

- 既有 `ObservabilityMetadata`（Correlation 族范本）是 hand-written sealed
  struct，已通过 reflect+AST + Go 包级可见性达到 Hard 双向锁。
- `PrincipalMetadata` 完全镜像同一 pattern。codegen 会额外引入 generator
  维护负担而不产生 Hard 增益。
- AI-robust §Hard 范本目录"sealed construction"载体明确接受此范式。

字段类型一律 `idutil.SafeID`（UnmarshalJSON 拒不安全字符，CWE-117 防御）。

### 2. Principal 字段集 — 4 字段 OAuth/OIDC 标准 （PR-A2）

`PrincipalMetadata{ActorID, SubjectID, TenantID, SessionID}`：

- `ActorID` = impersonator（实际触发动作的主体，OAuth `act.sub`）
- `SubjectID` = subject-of-record（被代理的用户，OAuth `sub`）
- `TenantID` = 租户边界（多租户部署）
- `SessionID` = 会话标识（server-side session 绑定）

普通流程 ActorID = SubjectID；impersonation 场景二者分离（OIDC token `act`
claim）。

### 3. Time-Causality 范围 — OccurredAt 直接字段，无 wrapper struct （audit: PR-A1 / outbox: PR-A2）

`outbox.Entry.OccurredAt time.Time` 直接放 outbox Entry，
`runtime/audit/ledger.Entry.OccurredAt time.Time` 直接放 audit ledger Entry。
不引 TimeMetadata wrapper：

- 单字段 wrapper struct 是 over-engineering（违反"不预设未来需求"）。
- `OccurredAt` 与 `CreatedAt`（outbox）/ `Timestamp`（audit）语义分层：
  - `OccurredAt` = producer 域业务事件时间（producer-clock）
  - `outbox.Entry.CreatedAt` = outbox row INSERT 时间（store-clock）
  - `audit ledger.Entry.Timestamp` = ledger 持久化 / HMAC 时间

audit-side（PR-A1）：`ledger.Entry.OccurredAt` 字段加进 11-field HMAC 输入；
表列 `occurred_at TIMESTAMPTZ NOT NULL`（无 DEFAULT），caller 必须供值（零
值或真实值）。

outbox-side（PR-A2）：`outbox.Entry.OccurredAt` **mandatory**（zero-value 被
`Entry.Validate()` 拒绝）；producers 必须 `entry.OccurredAt = clk.Now().UTC()`
或通过 `outbox.Emit` helper 自动填充。

Lamport / causation_id defer 到 W3 Command Bus —— causation 链需 command/event
双向关系上下文，单字段补齐无意义。

### 4. audit ledger HMAC msg 格式 canonical rewrite — 一刀切无哨兵 （PR-A1） ✅

`runtime/audit/ledger.Protocol.ComputeHash` HMAC msg 一次性 rewrite：

```
OLD (pipe-separated, develop 020_audit_ledger):
    prevHash|eventID|eventType|actorID|UnixNano|payload                          (6 字段)

NEW (canonical JSON, 043_audit_entries_v2):
    json.Marshal(auditHashInput{
        namespace,                                                                 ← cross-namespace domain separation
        prev_hash, event_id, event_type, actor_id,
        subject_id, tenant_id, session_id, correlation_id,
        occurred_at_unix_nano, timestamp_unix_nano, payload
    })                                                                            (12 字段)
```

`namespace` 作为第 1 字段进入 HMAC：对标 google/trillian TreeID 参与
`SignedEntryTimestamp` 的 domain separation 设计 —— 相同 `(prevHash, ..., payload)`
在 namespace A 与 namespace B 下产生不同 HMAC，cross-namespace replay 被签名层
拒绝。

`NewProtocol(namespace, key, opts...)` 改为 **位置参数**：删除
`WithChainHMAC` / `WithNamespace` Option（**type-system Hard 上游**），调用方
无法漏传或交换 namespace 与 key。`AUDIT-HASH-INPUT-FROZEN-01.A1` reflect-lock
固定 12 字段集 + 顺序；`A2` 用 `archtest.RunTyped` + `ResolvePackageRef` 在
go/types 层解析 `crypto/hmac.New` 调用点（**alias-proof**，`import h
"crypto/hmac"` 不可绕过）；`B` 反向自检覆盖 bare function / wrong receiver /
import alias 三个 vector。

`auditHashInput` 是 `runtime/audit/ledger` 包内 **unexported** typed struct
（包外不可构造、不可作 unmarshal target、不可 alias re-shape）；11 字段，字段
顺序与 JSON tag 固定。`json.Marshal` 按 struct source-declaration order 输出
确定性字节序列。Payload 为 `[]byte`，由 `encoding/json` 编码为 base64 JSON
string（无需手工 hex-encode）。

旧 pipe-separator 格式的 field-boundary collision 风险由此彻底消除（PR #1218
F3+F6）：JSON quote/escape 处理使任何字段值都无法移动字段边界。

**无版本字节、无 legacy 路径、无 ALTER ADD 哨兵**：CLAUDE.md
「Review 和重构时不考虑向后兼容——当前只有 gocell 自身」原则下，旧 audit
chain 数据由 migration 043 一并 DROP TABLE 丢弃；hash chain 从新 seq=1
重建。所有 hash 期望 fixture 在同 PR (PR-A1) 内 regen。

PR #1218 原方案 `041_extend_audit_principal_correlation.sql` 用 `ALTER TABLE
ADD COLUMN ... NOT NULL DEFAULT ''` / `DEFAULT '1970-01-01 00:00:00+00'`
哨兵进 W0 transition；该方案在 review 中触发 14 类问题（详见 PR #1218 review
notes），根因是 Soft "adoption incremental" 路径违反向后兼容原则，本 ADR
amendment 2026-05-29 全面替代为 DROP+CREATE 一刀切（**migration 043 文件名
随之改为 `043_audit_entries_v2.sql`**），并在 §"威胁矩阵" 中按
`contract-fanout.md` DROP COLUMN 模板逐项做 invariant inventory + 替代证明。

`cells/auditcore/internal/appender/service.go` 通过 `s.protocol.ComputeHash`
委托，PR-A1 不修改该文件 —— Go struct literal 自动用零值填充 5 个新字段
(`SubjectID=""`, `TenantID=""`, `SessionID=""`, `CorrelationID=""`,
`OccurredAt=time.Time{}`)，PG store INSERT VALUES 以零值满足 5 列的 NOT NULL
约束。PR-A2 引入 outbox.Entry.Principal/OccurredAt 后，appender 改读真实源
即可，audit schema 不再变。

### 5. ReservedMetadataKeys 扩展 5 key （PR-A2）

阻断业务通过 `outbox.Entry.Metadata` 伪造 Principal / OccurredAt：

- `actor_id` / `subject_id` / `tenant_id` / `session_id`（Principal）
- `occurred_at`（Time-Causality）

12 key total（7 existing observability + 5 new）。`outbox.Entry.Validate()`
fail-fast 拒绝。

### 6. archtest Hard funnel 双向锁 （audit: PR-A1 / outbox: PR-A2）

| ID | 形态 | 评级 | PR |
|----|------|------|----|
| `AUDIT-HASH-INPUT-FROZEN-01` （新增） | A1 上游：AST/reflect 锁 `auditHashInput` 字段集 + 顺序 + JSON tag + Go 类型；包外不可构造（Go 包私可见性）。A2 下游：AST 锁 `hmac.New(sha256.New, _)` callsite ⊆ `{Protocol.ComputeHash.Body}`，audit ledger 包内其他位置不得直接构造 HMAC。A3 字段顺序冻结。B 反向自检（合成违反 fixture）。 | Hard 双向 | **PR-A1** |
| `SAFEID-WIREMESSAGE-USAGE-01`（既有，carve-out 扩展） | deny-by-default：wireMessage 所有 exported 字段必须 `idutil.SafeID`，PrincipalMetadata 加入 walked types | Hard 下游 | PR-A2 |
| `SAFEID-UPSTREAM-FUNNEL-HARD-01`（既有） | wireMessage unexported + 无任意名 re-export | Hard 上游（自动覆盖） | — |
| `PRINCIPAL-SEALED-FIELD-FROZEN-01`（新增） | reflect 锁 4 字段名 + JSON tag + AST 锁方法集 (`IsZero` / `Validate` / `RestoreToContext` + `ContextPrincipal` + `Entry.InjectPrincipalFromContext`) + 盲区反向自检 | Hard 下游 | PR-A2 |

**不引入** INJECTION-FUNNEL callsite uniqueness archtest：业务构造
`outbox.Entry{Principal: ..., OccurredAt: ...}` 字面量是合法形态；wire 安全四路
（wireMessage unexported + SafeID + ReservedMetadataKeys + Entry.Validate）已
Hard 双向锁，额外 callsite uniqueness 无 Hard 增益。

### 7. consumer span Principal attrs （PR-A2）

`kernel/wrapper.WrapConsumer/WrapSubscriber` 通过 `entryEnvelopeAttrs(entry)`
helper 写入 delivery span：

- `gocell.principal.{actor,subject,tenant,session}_id`（string，走 adapters/otel
  safeStringAttr free-form RedactString + Truncate 路径）
- `gocell.event.occurred_at_unix_nano`（int64 typed scalar，绕过 string redact）

零值字段不写 —— 保持 span 在 producer 未注入 Principal 时安静。

## 威胁矩阵

### 三层 redaction 分工（outbox + audit 横切）

| 层 | 内容 | wire | server-side slog | trace span |
|----|------|------|-----------------|-----------|
| Observability 4 字段 | trace_id / traceparent / request_id / correlation_id | ✅ envelope 字段 | ✅ ctx 字段还原 | ✅ free-form redact |
| Principal 4 字段 | actor_id / subject_id / tenant_id / session_id | ✅ envelope 字段（PR-A2） | ✅ ctx 字段还原（PR-A2） | ✅ free-form redact（PR-A2） |
| OccurredAt | producer-clock | ✅ envelope `occurredAt`（PR-A2） | ✅ via `entry.OccurredAt`（PR-A2） | ✅ typed int64 (`_unix_nano`) （PR-A2） |
| Audit ledger row | 包含上述 9 字段 + actor_id / event_id / event_type / timestamp / payload / prev_hash / hash 共 15 列 | — | — | — |
| Payload | 业务事件数据 | ✅ envelope `payload` (json.RawMessage) | ❌ 不在 slog | ❌ 不在 span |
| ReservedMetadataKeys 伪造 | actor_id 等通过 Metadata 写入 | ❌ `validateMetadata` 拒（PR-A2 Entry.Validate fail-fast） | — | — |

### audit_entries DROP+CREATE invariant inventory（PR-A1）

按 `.claude/rules/gocell/contract-fanout.md` DROP COLUMN 模板 +
`202605101400-adr-credential-session-protocol.md` §A8 范式，PR-A1 把 audit_entries
表 + HMAC chain 一刀切重建，三类原 invariant 与替代证明逐项列出：

#### A. 旧 6-field hash chain（develop `020_audit_ledger.sql`）

- **原 invariant**：
  1. **字段边界**：HMAC msg `prevHash|eventID|eventType|actorID|UnixNano|payload`，字段边界由 `|` 分隔（6 字段）；chain 验证只读取这 6 字段。
  2. **运维约定**：业务侧 payload 不含 `|` 字节（无强制 enforcement，依赖业务侧守约）。
  3. **类型路由**：`fmt.Sprintf("%s", []byte)` 隐式走 `string` 路径（payload 以原始字节直接落 HMAC msg），string vs []byte 的类型路由由 `fmt.Sprintf` 动词运行时分支决定。
- **替代证明**：
  - 11-field canonical JSON (`auditHashInput` unexported typed struct,
    `json.Marshal` source-order) 包含全 5 新字段（subject_id / tenant_id /
    session_id / correlation_id / occurred_at_unix_nano）+ 旧 6 字段。
  - JSON quote/escape 消除字段边界 collision 风险（PR #1218 F3+F6）：任何字段
    值都无法移动字段边界，即便 payload 含 `|` 也无法伪造另一条 entry 的边界；
    旧 invariant #2 的运维守约被静态消除。
  - Payload `[]byte` 由 `encoding/json` base64 自编码，**类型路由从 runtime
    `fmt.Sprintf` 分支降为编译期静态绑定**：`auditHashInput.Payload` 字段类型
    硬编 `[]byte`，encoding/json 对 `[]byte` 的处理在标准库内唯一确定，
    旧 invariant #3 的隐式分支被 Go 类型系统消除。
  - 所有 hash 期望 fixture（`mem_store_test.go::TestMemStore_Append_HashEquivalence`，
    `TestProtocol_ComputeHash_ByteForByte`）在 PR-A1 内 regen 为 11-field 形态。
  - **funnel 守卫**：`AUDIT-HASH-INPUT-FROZEN-01` 双向 Hard 锁（A1 字段集 + 顺序 + JSON
    tag + Go 类型 reflect/AST 锁；A2 `hmac.New` callsite ⊆
    `{Protocol.ComputeHash.Body}` 且 receiver 必须是 `*Protocol`）保证 caller
    无法旁路 ComputeHash 或漂移 msg 格式 / 类型路由。

#### B. audit_entries 表数据（develop 已有 row）

- **原 invariant**：
  1. **chain SoR**：已有 row 在 020 schema 下生成 hash valid；`RestartRecoveryStrictTailVerify` 在 restart 期 SELECT 最大 seq_no 再 verify tail HMAC，确保新 Append 接在合法 chain 末尾。
  2. **DB-level dedup**：`021_audit_entries_event_id_unique.sql` 引入的 `uq_audit_namespace_event_id` UNIQUE INDEX `(namespace, event_id)` 是 application-layer `selectFingerprintSQL` 的二线 dedup guard（`mem_store.go:308-309` 注释明示），防止两个并发 Append 在 fingerprint check 之间窗口插入同 EventID 的 row。
  3. **hash 格式 CHECK**：`ck_audit_hash_format` CHECK 约束在 DB 层强制 prev_hash / hash 是 64-char 小写 hex（seq_no-coupled，genesis row 例外），作为 wire-format 的最后一道防线。
- **替代证明**：
  - **chain SoR**（替代原 invariant #1）：migration 043_audit_entries_v2 在
    +goose Up 阶段 DROP TABLE audit_entries（配合 **专属** GUC
    `gocell.allow_audit_rebuild` fail-closed guard，与 destructive-down GUC
    解耦以避免语义混用；发现表已存在且含 row 时拒绝迁移；以 `pg_class`
    探测表存在以避免 `information_schema` 权限盲区），然后 CREATE TABLE
    重建。DROP 之后表不存在 → `TailVerify` SELECT 取不到任何旧 row（结果集为
    空）→ `RestartRecoveryStrictTailVerify` 自动从 `prevHash=""`, `seqNo=1`
    重建 chain 起点（与首次部署语义等价），无 W0 detection / sentinel 检测
    路径。旧 row 的 hash 无法被新 ComputeHash 验证（12-field 与 6-field
    字节不同），但由于行已被 DROP，不存在「旧 hash 与新 hash 共存」二义性。
    Down 块直接 `RAISE EXCEPTION`（无独立反向路径），运维必须走
    `goose down-to 020 && goose up` 显式重建——杜绝
    「版本 41 但表不存在」半状态。**Hardness ceiling**：goose SQL 只能读 GUC
    字符串；typed `MigratorPermit` 是更 Hard 的形态但需要把 forward rebuild
    搬到 Go 侧（独立 backlog 跟踪）。
  - **DB-level dedup**（替代原 invariant #2）：043 line 101 `CREATE UNIQUE INDEX
    uq_audit_namespace_event_id ON audit_entries (namespace, event_id)` 复刻
    021 的 UNIQUE 约束。schema_guard `expectedIndexes` 同步注册该索引名 +
    `Unique: true` 标记，运行时 schema_guard 启动校验保证索引未漂移。
  - **hash 格式 CHECK**（替代原 invariant #3）：043 CREATE TABLE 内联
    `CONSTRAINT ck_audit_hash_format CHECK (...)` 子句，约束 regex 字面与 020
    一致；schema_guard `expectedChecks` 注册 `ck_audit_hash_format` 名持续守
    护其存在性。
  - **事实陈述**：gocell 无外部部署 → dev/test/CI 环境重跑 migration 即可。
    这是事实层面的运维真值，不在 §Consequences 重复作免责论述（CLAUDE.md
    「Review 和重构时不考虑向后兼容」明确该原则的应用前提，非个案豁免）。
  - 关联 archtest：schema_guard `expectedColumns` 15 行（10 旧 + 5 新 NotNull=true，
    无 expectedDefault），`expectedVersion=43`；`expectedIndexes` 含 `uq_audit_namespace_event_id Unique=true`；
    `expectedChecks` 含 `ck_audit_hash_format`；`MIGRATION-PAIR-DEPLOY-01`
    无新加 pair-deploy directive（043 自包含含 020 + 021 全部约束）。

#### C. ALTER ADD 5 列 + 哨兵 DEFAULT（PR #1218 旧方案 `041_extend_audit_principal_correlation.sql`）

- **原 invariant**：W0 transition 阶段，已有 row 由
  `NOT NULL DEFAULT ''` / `NOT NULL DEFAULT '1970-01-01 00:00:00+00'` 哨兵
  backfill；新 row 由 producer (W1+) 注入真值；hash chain 含 11 字段且哨兵 row
  hash 仍能被 ComputeHash 验证；W0 → W1 期 dual-state 允许 producer 增量接入。
- **替代证明**：
  - DROP+CREATE 不存在「旧 row 需要 backfill」语义 —— 旧 row 已随 DROP TABLE
    丢弃，新 row 5 字段 NOT NULL 无 DEFAULT，caller (PG audit store INSERT
    VALUES) 必须显式供值（零值或真实值，由 Go struct literal 字段填充驱动）。
  - W0 transition / adoption incremental 语义彻底删除 —— 不存在哨兵 row 判定
    逻辑，不存在「这条 row 是哨兵还是真值」的运行时分支；运行时 4 字段
    `string` 与 1 字段 `time.Time` 是否零值不再带语义意图。
  - 关联 archtest：`AUDIT-HASH-INPUT-FROZEN-01.A1` reflect-lock 排除任何
    `hash_version` 字段或 sentinel detection 逻辑悄悄回灌进 `auditHashInput`。

## Consequences

**Positive**:

- W2/W3 下游缺口（HTTP Idempotency、Command Bus、/correlate）可直接消费 Principal
  而无需自建 schema（PR-A2 落地）。
- producer 通过 ctx → InjectPrincipalFromContext 形成 sealed write path，业务
  无法伪造（PR-A2 落地）。
- consumer 自动 ctx 还原，handler 透明感知 Principal（PR-A2 落地）。
- audit ledger hash chain 含 Principal 5 字段，tamper-detection 覆盖面从 6 字段
  扩到 11 字段（PR-A1 落地）。
- archtest funnel 双向锁 (`AUDIT-HASH-INPUT-FROZEN-01` Hard 双向，`PRINCIPAL-SEALED-FIELD-FROZEN-01`
  Hard 下游 + `SAFEID-UPSTREAM-FUNNEL-HARD-01` Hard 上游)，wire schema 不可漂移。
- DROP+CREATE 一刀切清除 PR #1218 W0 哨兵 / 增量接入语义，无双状态运行时分支
  需要维护。

**Negative**:

- audit ledger HMAC msg rewrite 不向后兼容 —— audit chain 从 seq=1 重建，旧 row
  数据丢失。该后果对 gocell（无外部部署）是设计接受值，不是免责论述。
- 12 reserved key 集合（PR-A2 落地） —— 业务需熟悉 Metadata 黑名单，trade-off 换
  typed envelope 反伪造。
- Principal optional 字段需 producer 显式调用 InjectPrincipalFromContext —— audit
  侧零值满足 schema 但不携带业务语义；PR-A2 引入接入路径，PR-A1 仅完成 schema
  + chain 准备。
- `cells/auditcore/internal/appender/service.go` 在 PR-A1 不动 —— 5 个新字段当前
  以 Go 零值落 DB，PR-A2 引入 outbox.Entry.Principal/OccurredAt 后改读真实源。
  这不是双路径或半成品语义：appender 始终是「读源构造 Entry」单路径，PR-A1 与
  PR-A2 切换的是「源是 zero literal 还是 outbox.Entry.Principal」一个字段
  填充表达式。

**Future** (defer):

- Lamport / causation_id —— W3 Command Bus 引入。
- ReceivedAt（broker / consumer 接收时间） —— 消费侧 telemetry，不进 producer
  envelope。

## Implementation matrix

```
Contract: kernel/outbox.Entry / kernel/outbox.wireMessage / runtime/audit/ledger.Entry / Protocol.ComputeHash
Change: 三族字段集补齐（Principal 4 字段 + OccurredAt）+ audit HMAC msg canonical rewrite + audit_entries 表 DROP+CREATE
Implementations:
  [x] (PR-A1) runtime/audit/ledger.Entry (+5 字段)
  [x] (PR-A1) runtime/audit/ledger.Protocol.ComputeHash (canonical JSON, 11-field, auditHashInput unexported struct)
  [x] (PR-A1) runtime/audit/ledger.mem_store (字段透传 via *e 复制 + protocol.ComputeHash 单源)
  [x] (PR-A1) adapters/postgres/audit_ledger.store (15 列 INSERT/SELECT/scan)
  [x] (PR-A1) adapters/postgres/migrations/043_audit_entries_v2.sql (DROP+CREATE, NOT NULL 无 DEFAULT)
  [x] (PR-A1) adapters/postgres/schema_guard.go (15 列 + version 43 + inventory)
  [x] (PR-A1) runtime/audit/ledger/storetest.RunPrincipalFieldsRoundTrip (5 字段 RoundTrip + HMAC parity)
  [x] (PR-A1) runtime/audit/ledger.TestProtocol_AllElevenFieldsAffectHash (11-field tamper sensitivity)
  [x] (PR-A1) tools/archtest/audit_hash_input_frozen_test.go (AUDIT-HASH-INPUT-FROZEN-01 A1/A2/A3/B)
  [ ] (PR-A2) kernel/outbox.PrincipalMetadata (typed + Validate + Context/Restore/Inject)
  [ ] (PR-A2) kernel/outbox.wireMessage (Principal + OccurredAt wire 字段)
  [ ] (PR-A2) kernel/outbox.Entry.OccurredAt 字段 + Validate mandatory
  [ ] (PR-A2) kernel/outbox.SubscriberWithMiddleware (Principal RestoreToContext)
  [ ] (PR-A2) kernel/outbox/outboxtest.RunPrincipalRoundTripConformance
  [ ] (PR-A2) cells/auditcore/internal/appender (5 字段派生 + HMAC 对齐)
  [ ] (PR-A2) kernel/wrapper.{WrapConsumer,WrapSubscriber} (Principal/OccurredAt span attrs)
  [ ] (PR-A2) pkg/ctxkeys (4 new typed key pairs)
  [ ] (PR-A2) tools/archtest/principal_sealed_field_frozen_test.go (PRINCIPAL-SEALED-FIELD-FROZEN-01)
  [ ] (PR-A2) tools/archtest/safeid_funnel_test.go (carve-out 扩 PrincipalMetadata)
  [ ] (PR-A2) adapters/postgres/migrations/042_outbox_principal_occurred_at.sql
Conformance test:
  - runtime/audit/ledger/storetest.RunPrincipalFieldsRoundTrip (PR-A1)
  - runtime/audit/ledger.TestProtocol_AllElevenFieldsAffectHash (PR-A1)
  - kernel/outbox/outboxtest.RunPrincipalRoundTripConformance (PR-A2)
  - cells/auditcore/internal/appender 全套测试（hash 期望 regen 含 11-字段 HMAC）(PR-A1 zero / PR-A2 真值)
Repro:
  PR-A1 verify:
    go test ./runtime/audit/ledger/... ./adapters/postgres/... ./cells/auditcore/...
    go test ./tools/archtest/ -run 'AuditHashInputFrozen'
    bash hack/verify-archtest-invariants.sh
  PR-A2 verify (future):
    go test ./kernel/outbox/... ./pkg/ctxkeys/... ./kernel/wrapper/...
    go test ./tools/archtest/ -run 'PRINCIPAL|SAFEID-WIREMESSAGE|SAFEID-UPSTREAM'
Dependent contracts (governance scan): none — 三族字段集是 framework 横切，不进 contract.yaml payload.schema.json
Invariant inventory (DROP COLUMN 043_audit_entries_v2.sql):
  - 6-field hash chain → 11-field canonical JSON via auditHashInput unexported struct + AUDIT-HASH-INPUT-FROZEN-01 双向 Hard 锁
  - existing audit_entries rows → DROP TABLE + chain restart from seq=1; TailVerify 自然适配空表
  - W0 sentinel adoption (PR #1218 retracted) → 5 列 NOT NULL 无 DEFAULT 一刀切，appender 零值满足约束
```

## References

- 实施：PR-A1（issue #1228 — 撤回 PR #1218 重做：audit_entries v2 + HMAC canonical rewrite）
- 撤回：PR #1218（14 类 review finding 触发 ADR amendment）
- 后续：PR-A2（issue #1229 — outbox Entry sealed construction + 241 字面量重写 + migration 042 + appender 接入 + auditquery 出口收紧）
- 关联：issue #1219（auditquery API 暴露 5 新列；sequenced after #1229 因为 §4 可能重写 wire DTO）
- 重构 backlog（PR #1239 round-3 派生）：
  - #1245 SignedDomainContext 跨 audit/outbox/jwt 三层签名上下文统一化
  - #1246 tools/archtest/internal/callresolver 通用 callsite 解析框架
  - #1248 MigratorPermit typed channel — forward-rebuild 移到 Go 侧
  - #1249 storetest property-based fuzzing — Entry 字段 / payload binary corpus
  - #1241 RestartRecoveryStrictTailVerify sealed type 未触发实际 Verify
  - won't-do: HMAC key GC finalizer（Go 语言约束，已知 docs-only 议题）
- Foundation: `kernel/outbox/observability.go` (Correlation 族范本)
- Sealed construction template: `pkg/errcode/details.go` (PR #1035) + `errcode_invariants_test.go::TestDetailsSealedFieldFrozen01` 参考形态
- Hard 范本: `.claude/rules/gocell/ai-robust.md` §Hard 范本目录 "sealed construction" + "typed function choice"
- archtest sibling locks: `tools/archtest/safeid_funnel_test.go` (`SAFEID-WIREMESSAGE-USAGE-01` / `SAFEID-UPSTREAM-FUNNEL-HARD-01`)
- archtest new (audit-side, PR-A1): `tools/archtest/audit_hash_input_frozen_test.go` (`AUDIT-HASH-INPUT-FROZEN-01`)
- archtest new (outbox-side, PR-A2): `tools/archtest/principal_sealed_field_frozen_test.go` (`PRINCIPAL-SEALED-FIELD-FROZEN-01`)
- DROP COLUMN 模板：`.claude/rules/gocell/contract-fanout.md` §"5 个必查载体" + §"Implementation matrix 模板" + ADR `202605101400-adr-credential-session-protocol.md` §A8
- 005 roadmap §W0: `docs/plans/framework-capability-gaps/202605162100-005-framework-capability-roadmap-plan.md:52`
- 004 §强枢纽节点: `docs/plans/framework-capability-gaps/202605131500-004-capability-gap-analysis.md:276`
- OAuth/OIDC: OpenID Connect Core 1.0 §5.1 ("sub"), RFC 8693 §4.1 ("act")
- CloudEvents v1.0 §3 Required Attributes (Time = OccurredAt 模式)
- HMAC canonical 参考：google/trillian `storage/leafdata.go`，RFC 8785 (JCS) struct-order determinism

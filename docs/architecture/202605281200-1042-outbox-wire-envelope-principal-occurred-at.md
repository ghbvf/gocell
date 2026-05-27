# ADR-1042: Outbox Wire Envelope — Principal 族 + OccurredAt 补齐

**Status**: Accepted
**Date**: 2026-05-28
**Issue**: #1042 (D1/W0 — 005 framework capability roadmap)
**Related**: PR #1035 (errcode sealed details), ADR `202605221000-adr-outbox-observability-envelope.md`

## Context

005 W0 杠杆点：让 `outbox` wire envelope 在 Principal / Correlation / Time-Causality 三族字段集上具备 Hard funnel——schema 固定、类型保证、跨包不可表达伪造、producer 端 fail-fast、consumer 端自动还原。W0 解锁 W2 HTTP Idempotency、W3 Command Bus、`/correlate` 等下游缺口。

**探索发现 issue 描述 stale，实际状态**：

| 族 | 当前 | W0 闭环 |
|----|------|--------|
| Correlation | ✅ 已 Hard sealed — `ObservabilityMetadata` typed + `wireMessage` unexported + 双 archtest（`SAFEID-WIREMESSAGE-USAGE-01` / `SAFEID-UPSTREAM-FUNNEL-HARD-01`） | 无改动 |
| Principal | ❌ 缺失 | 加 4 字段 typed struct `PrincipalMetadata`（镜像 ObservabilityMetadata） |
| Time-Causality | 🟡 `Entry.CreatedAt`（store INSERT 时间） | 加 `Entry.OccurredAt time.Time` 直接字段（producer-domain 事件时间，与 CreatedAt 分层） |

## Decision

### 1. 设计载体 — 镜像 sealed construction，拒 codegen funnel

issue body 提"codegen funnel + golden"，复核后否决：

- 既有 `ObservabilityMetadata`（Correlation 族范本）是 hand-written sealed struct，已通过 reflect+AST + Go 包级可见性达到 Hard 双向锁。
- `PrincipalMetadata` 完全镜像同一 pattern。codegen 会额外引入 generator 维护负担而不产生 Hard 增益。
- AI-robust §Hard 范本目录"sealed construction"载体明确接受此范式。

字段类型一律 `idutil.SafeID`（UnmarshalJSON 拒不安全字符，CWE-117 防御）。

### 2. Principal 字段集 — 4 字段 OAuth/OIDC 标准

`PrincipalMetadata{ActorID, SubjectID, TenantID, SessionID}`：

- `ActorID` = impersonator（实际触发动作的主体，OAuth `act.sub`）
- `SubjectID` = subject-of-record（被代理的用户，OAuth `sub`）
- `TenantID` = 租户边界（多租户部署）
- `SessionID` = 会话标识（server-side session 绑定）

普通流程 ActorID = SubjectID；impersonation 场景二者分离（OIDC token `act` claim）。

### 3. Time-Causality 范围 — OccurredAt 直接字段，无 wrapper struct

`Entry.OccurredAt time.Time` 直接放 Entry（与 ObservabilityMetadata 分组并列），不引 TimeMetadata wrapper：

- 单字段 wrapper struct 是 over-engineering（违反"不预设未来需求"）。
- `Entry.OccurredAt` 与 `Entry.CreatedAt` 语义分层：
  - `OccurredAt` = producer 域业务事件时间（producer-clock）
  - `CreatedAt` = outbox row INSERT 时间（store-clock，由 PG writer / DirectEmitter 自动填）

OccurredAt **optional**（zero-value 允许，匹配 ObservabilityMetadata 模型）。

Lamport / causation_id defer 到 W3 Command Bus —— causation 链需 command/event 双向关系上下文，单字段补齐无意义。

### 4. audit ledger HMAC msg 格式 rewrite — 无向后兼容

`runtime/audit/ledger.Protocol.ComputeHash` HMAC msg 格式：

```
OLD: prevHash|eventID|eventType|actorID|UnixNano|payload
NEW: prevHash|eventID|eventType|actorID|subjectID|tenantID|sessionID|correlationID|occurredAtUnixNano|timestampUnixNano|payload
```

PR #1042 工作时 gocell 无外部部署 → 用户明确放弃向后兼容（无 v1/v2 协议分叉，无 nullable shim）。所有 hash 期望 fixture 同 PR regen。

`cells/auditcore/internal/appender/service.go` 同步对齐新 11-字段 HMAC 格式（不再有 cells/auditcore/internal/domain/hashchain.go 因为已收编到 appender 路径）。

### 5. ReservedMetadataKeys 扩展 5 key

阻断业务通过 `Entry.Metadata` 伪造 Principal / OccurredAt：

- `actor_id` / `subject_id` / `tenant_id` / `session_id`（Principal）
- `occurred_at`（Time-Causality）

12 key total（7 existing observability + 5 new）。`Entry.Validate()` fail-fast 拒绝。

### 6. archtest Hard funnel 双向锁

| ID | 形态 | 评级 |
|----|------|------|
| `SAFEID-WIREMESSAGE-USAGE-01`（既有，carve-out 扩展） | deny-by-default：wireMessage 所有 exported 字段必须 `idutil.SafeID`，否则注册 carve-out；PrincipalMetadata 加入 walked types | Hard 下游 |
| `SAFEID-UPSTREAM-FUNNEL-HARD-01`（既有，自动覆盖） | wireMessage unexported + 无任意名 re-export | Hard 上游 |
| `PRINCIPAL-SEALED-FIELD-FROZEN-01`（新增） | reflect 锁 4 字段名 + JSON tag + AST 锁方法集 (`IsZero` / `Validate` / `RestoreToContext` + `ContextPrincipal` + `Entry.InjectPrincipalFromContext`) + 盲区反向自检 | Hard 下游 |

**不引入** INJECTION-FUNNEL callsite uniqueness archtest：既有 ObservabilityMetadata 无对应 archtest 已 Hard（业务构造 `Entry{Principal: ..., OccurredAt: ...}` 字面量是合法形态）；wire 安全四路（wireMessage unexported + SafeID + ReservedMetadataKeys + Entry.Validate）已 Hard 双向锁，额外 callsite uniqueness 无 Hard 增益。

### 7. consumer span Principal attrs

`kernel/wrapper.WrapConsumer/WrapSubscriber` 通过 `entryEnvelopeAttrs(entry)` helper 写入 delivery span：

- `gocell.principal.{actor,subject,tenant,session}_id`（string，走 adapters/otel safeStringAttr free-form RedactString + Truncate 路径）
- `gocell.event.occurred_at_unix_nano`（int64 typed scalar，绕过 string redact）

零值字段不写 — 保持 span 在 producer 未注入 Principal 时安静。

## Threat Model — 三层 redaction 分工

| 层 | 内容 | wire | server-side slog | trace span |
|----|------|------|-----------------|-----------|
| Observability 4 字段 | trace_id / traceparent / request_id / correlation_id | ✅ envelope 字段 | ✅ ctx 字段还原 | ✅ free-form redact |
| Principal 4 字段 | actor_id / subject_id / tenant_id / session_id | ✅ envelope 字段 | ✅ ctx 字段还原 | ✅ free-form redact |
| OccurredAt | producer-clock | ✅ envelope `occurredAt` | ✅ via `entry.OccurredAt` | ✅ typed int64 (`_unix_nano`) |
| Payload | 业务事件数据 | ✅ envelope `payload` (json.RawMessage) | ❌ 不在 slog | ❌ 不在 span |
| ReservedMetadataKeys 伪造 | actor_id 等通过 Metadata 写入 | ❌ `validateMetadata` 拒 (Entry.Validate fail-fast) | — | — |

## Implementation matrix

```
Contract: kernel/outbox.Entry / kernel/outbox.wireMessage / runtime/audit/ledger.Entry / Protocol.ComputeHash
Change: 三族字段集补齐（Principal 4 字段 + OccurredAt）+ HMAC msg rewrite
Implementations:
  [x] kernel/outbox.PrincipalMetadata (typed + Validate + Context/Restore/Inject)
  [x] kernel/outbox.wireMessage (Principal + OccurredAt wire 字段)
  [x] kernel/outbox.SubscriberWithMiddleware (Principal RestoreToContext)
  [x] kernel/outbox/outboxtest.RunPrincipalRoundTripConformance
  [x] runtime/audit/ledger.Entry (+5 字段) + Protocol.ComputeHash (HMAC rewrite)
  [x] runtime/audit/ledger.mem_store (+4 filter, hash fixture regen)
  [x] adapters/postgres/audit_ledger.store (INSERT/SELECT 列对齐)
  [x] adapters/postgres/migrations/041_extend_audit_principal_correlation.sql
  [x] cells/auditcore/internal/appender (5 字段派生 + HMAC 对齐)
  [x] kernel/wrapper.{WrapConsumer,WrapSubscriber} (Principal/OccurredAt span attrs)
  [x] pkg/ctxkeys (4 new typed key pairs)
Conformance test:
  - kernel/outbox/outboxtest.RunPrincipalRoundTripConformance
  - cells/auditcore/internal/appender 全套测试（hash 期望 regen 含 11-字段 HMAC）
Repro:
  go test ./kernel/outbox/... ./runtime/audit/... ./pkg/ctxkeys/... ./cells/auditcore/...
  go test ./tools/archtest/ -run 'PRINCIPAL|SAFEID-WIREMESSAGE|SAFEID-UPSTREAM'
Dependent contracts (governance scan): none — 三族字段集是 framework 横切，不进 contract.yaml payload.schema.json
```

## Consequences

**Positive**:
- W2/W3 下游缺口（HTTP Idempotency、Command Bus、/correlate）可直接消费 Principal 而无需自建 schema
- producer 通过 ctx → InjectPrincipalFromContext 形成 sealed write path，业务无法伪造
- consumer 自动 ctx 还原，handler 透明感知 Principal
- audit ledger hash chain 含 Principal 5 字段，tamper-detection 覆盖面扩
- archtest funnel 双向锁两栏 Hard，wire schema 不可漂移

**Negative**:
- audit ledger HMAC msg rewrite 不向后兼容 — 已部署系统升级需 hash 重算（gocell 无外部部署，可接受）
- 12 reserved key 集合 — 增加业务需熟悉的 Metadata 黑名单（trade-off 换 typed envelope 反伪造）
- Principal optional 字段需 producer 显式调用 InjectPrincipalFromContext — 不强制接入（adoption incremental），现存 241 处 `Entry{}` 字面量不需同 PR 改造

**Future** (defer):
- Lamport / causation_id — W3 Command Bus 引入
- ReceivedAt（broker / consumer 接收时间）— 消费侧 telemetry，不进 producer envelope
- OccurredAt 强制必填 — 等业务全面 adoption 后通过单独 PR 收紧 Entry.Validate（届时所有 emit 路径已有 producer clock）

## References

- Implementation: PR #1042 (this PR)
- Foundation: `kernel/outbox/observability.go` (Correlation 族范本)
- Sealed construction template: `pkg/errcode/details.go` (PR #1035)
- Hard 范本: `.claude/rules/gocell/ai-robust.md` §Hard 范本目录 "sealed construction"
- archtest sibling locks: `tools/archtest/safeid_funnel_test.go` (`SAFEID-WIREMESSAGE-USAGE-01` / `SAFEID-UPSTREAM-FUNNEL-HARD-01`)
- archtest new: `tools/archtest/principal_sealed_field_frozen_test.go` (`PRINCIPAL-SEALED-FIELD-FROZEN-01`)
- 005 roadmap §W0: `docs/plans/framework-capability-gaps/202605162100-005-framework-capability-roadmap-plan.md:52`
- 004 §强枢纽节点: `docs/plans/framework-capability-gaps/202605131500-004-capability-gap-analysis.md:276`
- OAuth/OIDC: OpenID Connect Core 1.0 §5.1 ("sub"), RFC 8693 §4.1 ("act")
- CloudEvents v1.0 §3 Required Attributes (Time = OccurredAt 模式)

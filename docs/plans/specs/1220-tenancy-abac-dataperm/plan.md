# Implementation Plan: Multi-tenancy + ABAC + Row/Column-Level Data Permission Control

**Branch**: `1220-tenancy-abac-dataperm` | **Date**: 2026-05-31 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/docs/plans/specs/1220-tenancy-abac-dataperm/spec.md`

## Summary

四层分治（**D1–D4，与 GoCell 一致性等级 L0–L4 无关**）、内嵌实现、fail-closed：D1 租户边界（middleware → `ctxkeys.TenantID`）、D2 ABAC policy-as-code 决策（accesscore 内嵌引擎，consistencyLevel L0，输出 Decision + obligations）、D3 行级 enforcement（repo 强制 `tenant.TenantID` typed param + policy 派生 `RowScope{self,device,tenant,all}` typed param + PG RLS 兜底）、D4 列级 masking（`pkg/projection.ResourceProjection` sealed type ← `FieldMask` obligation）。subject 支持 user/device（`principalKind`），喂下游 MDM/零信任。租户隔离**漏传**做成 type-system Hard（空值经 Validate Medium），列 masking 做成 sealed Hard，ABAC fail-closed 做 Medium archtest。

## Technical Context

**Language/Version**: Go（仓库当前版本）
**Primary Dependencies**: 标准库 + `pgx/v5` + `gopkg.in/yaml.v3`；复用 `pkg/errcode` / `pkg/redaction` / `pkg/query` / `kernel/outbox` / `runtime/auth` / `kernel/clock`；不新增外部 authz 依赖（无 OPA/Cedar/Casbin/SpiceDB）。
**Storage**: PostgreSQL（shared-schema + `tenant_id` 列 + `FORCE ROW LEVEL SECURITY`）；policy store 新表；mem store 用于测试。
**Testing**: table-driven unit（kernel/ ≥90%，其余 ≥80%）+ conformance suite（repo/policy store 跨实现）+ integration（`-tags=integration` 跑真实 PG RLS）+ archtest 不变式 + 反向自检 fixture。
**Target Platform**: Linux server（GoCell 平台底座）。
**Project Type**: Cell-native Go 框架底座（kernel/runtime/cells/adapters/pkg 分层）。
**Performance Goals**: 内嵌 ABAC 决策 p95 < 1ms（无网络往返）；RLS 谓词不引入 join 爆炸。
**Constraints**: fail-closed by default、无 caller opt-out（对齐现有 redaction 范式）、不向后兼容（无 shim）、每条约束同 PR 闭环。
**Scale/Scope**: 平台底座多租户；租户数中等规模（shared-schema 适配）；subject 主体类型 user/device/service。

## Constitution Check

*GATE: 必须在 Phase 0 前通过，Phase 1 设计后复查。*

| 宪法/规则约束 | 本特性符合性 |
|--------------|------------|
| 分层依赖（kernel 不依赖 runtime/adapters/cells） | `tenant.TenantID` newtype 放 `pkg/tenant`（共享工具，无层依赖）；ABAC 引擎放 `cells/accesscore`（持有 policy repo 接口，PG 实现在 adapters）；`RowScope` 放 `pkg/tenant`、`ResourceProjection` 放 `pkg/projection`（均无层依赖）。kernel 不引入 authz 业务。✅ |
| Cell 间只经 contract 通信 | ABAC 决策经 `accesscore.authorizationdecide` 暴露（L0 同 assembly 可直 import；跨 cell 经 contract）。decision 不跨 async 边界（FR-019）。✅ |
| 一致性等级 | authorizationdecide L0（纯决策）；policy 写入 L2（OutboxFact，policy.updated 事件）；accesscore 维持 L3。✅ |
| fail-closed 安全哲学（redaction 范式） | L1/L2/L3/L4 全 fail-closed；列 masking 无 opt-out，复用 `pkg/redaction`。✅ |
| AI-robust：新约束按 Hard/Medium 评级，Soft 禁立项 | 租户/RowScope typed param **漏传** → 编译器 Hard（空值经 Validate Medium）；`TENANT/ROWSCOPE-REPO-PARAM-FUNNEL` typed scan → Medium；ResourceProjection sealed（unexported 字段 + 唯一构造器）→ Hard；ABAC fail-closed / resource-attr 租户一致性 / role-name 字面量 ban / PrincipalKind 穷举 / 裸 SET ban → Medium archtest。无 Soft 立项。✅ |
| 引入新约束同 PR 闭环（三件套：静态守卫 + 文档契约 + 回归测试） | 每个 PR 自带其 enforcement archtest + 反向自检 + godoc/ADR。✅ |
| 不向后兼容 | repo 签名、`auth.Authorizer.Authorize` 签名（→返回 `Decision`）直接破坏式演化，无 deprecation 别名；interface 签名变更出 contract-fanout matrix（PR-7 T7.4）。✅ |
| errcode / slog / DB snake_case / 认知复杂度 ≤15 / 覆盖率 | 遵循。decision 错误走 errcode（KindUnavailable for fail-closed）。✅ |
| 契约扇出闭环（DROP COLUMN / forbiddenColumns / 新 status） | 新增 `tenant_id` 列 + RLS policy + policy 表为 additive migration；无 DROP COLUMN。auditquery 暴露 tenantId 走 #1219 additive omitempty。✅ |

**结论**：无 Constitution 违例，无需 Complexity Tracking 豁免。

## Project Structure

### Documentation (this feature)

```text
docs/plans/specs/1220-tenancy-abac-dataperm/
├── plan.md              # 本文件
├── spec.md              # 已产出
├── tasks.md             # PR ↔ 任务映射（见下）
└── research.md          # 探索结论（三 explorer 报告摘要，已内联 spec 设计基线）
```

### Source Code (repository root) — 受影响目录

```text
pkg/tenant/                          # 新增：TenantID sealed newtype + RowScope typed obligation
pkg/ctxkeys/                         # 扩展：TenantID 已有 key，补 principalKind
pkg/projection/                      # 新增：ResourceProjection sealed type + FieldMask obligation（无层依赖）
runtime/auth/
├── middleware.go                    # injectPrincipalCtxKeys 解析 tenant/device claim（消除 no-source）
├── principal.go                     # PrincipalKind 增 PrincipalDevice + 穷举 fanout
└── auth.go                          # Authorizer.Authorize 破坏式重做：返回 Decision + obligations
cells/accesscore/
├── slices/authorizationdecide/      # ABAC 引擎：policy 模型 + 评估 + obligations（consistencyLevel L0）
├── slices/policymanage/ (新)        # policy CRUD（L2 OutboxFact，policy.updated 事件）
├── internal/ports/                  # repo 接口加 tenant param + policy repo 接口
├── internal/adapters/postgres/      # policy PG store + roles/users repo 加 tenant param
└── internal/mem/                    # mem store 加 tenant/scope 过滤
adapters/postgres/
├── tx_manager.go                    # RunInTx 注入 SET LOCAL app.tenant_id（fail-closed）+ pgxpool BeforeAcquire guard
└── migrations/                      # tenant_id 列(CONCURRENTLY) + FORCE RLS policy + policy 表
cells/auditcore/                     # audit_entries 加 tenant_id + repo tenant param + auditquery 接 RowScope/Projection/暴露 tenantId（#1219/#1296）
cells/configcore/                    # config_entries/feature_flags 加 tenant_id + repo 接 tenant param
tools/archtest/                      # 新增不变式：tenant/RowScope funnel、projection sealed、ABAC fail-closed、PrincipalKind 穷举、裸 SET ban
.claude/rules/gocell/tenancy.md      # PR-1 起增量补充的 tenant/authz 规则导航
docs/architecture/                   # ADR ×3（租户隔离 / ABAC 引擎 / 列 masking）
```

**Structure Decision**：复用现有分层与 accesscore cell，不新建 cell。`tenant.TenantID` / `RowScope` 放 `pkg/tenant`、`ResourceProjection` / `FieldMask` 放 `pkg/projection`（均无层依赖，所有层可引用）。ABAC 引擎扩展现有 `authorizationdecide` slice（consistencyLevel L0）+ 新增 `policymanage` slice（policy 写侧，consistencyLevel L2）。**tenant typed param 迁移覆盖三个平台 cell**（accesscore/auditcore/configcore，非仅 accesscore）。RLS 在 adapters/postgres，TxRunner 注入。

## 四层 ↔ PR ↔ 既有 issue 映射

> 层用 **D1–D4**（Data-perm 架构分层），与 GoCell 一致性等级 L0–L4 无关。

| 层 / 故事 | PR 序列 | 关联既有 issue |
|----------|---------|---------------|
| D1 租户边界（US1） | PR-1 tenant newtype + claim source；PR-2 三 cell repo tenant param 迁移；PR-3 PG RLS + TxRunner 注入 | **#1296**（主线，降为子）、#1219（tenantId 暴露） |
| D3 行级 user/device（US2） | PR-4 RowScope typed obligation + repo param；PR-5 身份→scope 收窄 + RowScope=all 审计 | #1296（enforcement 段）、#654（device RBAC） |
| D2 ABAC 引擎（US3） | PR-6 policy 域模型 + mem store；PR-7 评估引擎 + Authorizer 接口重做；PR-8 PG policy store + conformance + readyz；PR-9 policymanage slice（L2 写侧） | #635（per-tenant policy）、#1051/#1052（device/zerotrust 下游消费） |
| 接线 + 迁移（US4） | PR-10 authorizationdecide 接线 composition root + permission-based 迁移 | **#914**（关闭）、#1055（authz↔contract）、#645（authz funnel） |
| D4 列级 masking（US5） | PR-11 ResourceProjection sealed framework（`pkg/projection`）；PR-12 应用到 auditquery + 读端点 | #1219（auditquery 字段暴露） |
| 治理收口（US6） | PR-13 跨层 ADR + 扇出汇总（各 PR 已自带 archtest，本 PR 只收口） | #645（funnel Hard 化） |

> 详细 PR 拆分（行数估算 + 依赖图 + 文件归属）见 [tasks.md](./tasks.md)。

## Complexity Tracking

> 无 Constitution 违例，本节空。

ABAC 走 policy-as-code 而非轻量 RBAC 扩展，是**用户显式决策**（为 MDM #1051 / 零信任 #1052 预留 device 态势 + 持续评估能力）。规避 OPA partial-eval→SQL 编译层（explorer 评估投入产出比低），policy 只发有界 obligation，repo 用 typed 参数消费——在拿到 policy-as-code 前瞻性的同时控制复杂度。

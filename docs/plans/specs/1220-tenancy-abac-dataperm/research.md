# Research: Multi-tenancy + ABAC + Row/Column-Level Data Permission Control

探索阶段（ship 阶段 1，3 个并行 explorer）结论存档。

## 1. 开源对标（结论）

三者**不是一个机制**，业界主流分四层；GoCell 规避独立 PDP 进程，全部内嵌。

| 对标 | 核心机制 | GoCell 采纳 | 规避 |
|------|---------|------------|------|
| OPA/Rego | partial-eval 残差 AST → SQL 谓词 | 「已知量(tenant)消解 / 未知量(列)下推」思路 | 独立进程 + Rego DSL + AST walker 维护成本 |
| AWS Cedar | schema 化 entity + forbid-wins + default-deny | **forbid wins / default deny 语义**、entity `in` 层次 | Policy-Store-per-tenant 管理负担、不解决 data filtering |
| Casbin | PERM 模型 model/policy 分离 + domain | **model/policy 分离**、domain=tenant 第一类参数 | 反射读 struct（无编译期类型检查，违 typed funnel） |
| Oso/Polar | policy 编译为 ORM Filter | `AuthorizedQuery` 返回可扩展查询对象的思路 | OSS 已废弃 + SaaS 依赖 |
| Zanzibar/SpiceDB/OpenFGA | ReBAC 关系元组 + ListObjects | search-then-check 模式 | 全套关系元组存储是过度架构 |
| PG RLS | DB 内核行过滤 fail-closed | **SET LOCAL + NULLIF + FORCE RLS** 作兜底 | PgBouncer session mode 不兼容（pgxpool 已规避） |
| Snowflake | row access policy + column masking | 列 masking「应用层 projection 重写」最适用 OLTP | DDM（PG 无原生） |
| 多租户隔离 | db/schema/shared-schema 三档 | **shared-schema + tenant_id + RLS**（与 pgxpool/TxRunner 契合） | db/schema-per-tenant 运维倍增 |

**综合判断**：分四层（边界 / 决策 / 行 / 列），各层 enforcement 落点业界共识：行级 DB RLS + 应用层双层、列级应用层 projection（OLTP）。OPA 式 policy→SQL 统一机制投入产出比对当前规模不划算。

## 2. GoCell 现状（关键缺口）

1. **TenantID 零生产来源**：ctxkey/wire/DB 列都有位置，`injectPrincipalCtxKeys` 明确「no source on develop」，所有 repo 查询无 tenant 谓词。
2. **authorizationdecide 未连线死代码**：实现 `auth.Authorizer` 但 composition root 从未注入路由，零流量；无 contractUsage。
3. **纯 RBAC 无 ABAC**：`Authorize(ctx, subject, resource, action) bool`，无 attributes/condition/env；Permission 是 `(resource,action)` 精确匹配。
4. **无行列 enforcement 基建**：auditquery 的 actor-scoping（非 admin 强制 `actorId=self`）是 handler 层 ad-hoc，非框架；无 query builder 谓词注入点。
5. **#914 permission-based authz 未启动**：业务端点仍 role-name 字符串直比。
6. **现有可复用基建**：outbox `PrincipalMetadata`（含 TenantID 占位）sealed construction、`CTXKEYS-PRINCIPAL-WRITE-CALLER-01` caller-allowlist、`pkg/query` cursor 分页、authzmutate funnel、`pkg/redaction`、`idutil.SafeID` newtype 范式、`clock.Clock` 注入。

## 3. 边界 / 安全 / AI-robust 评级（关键结论）

- **租户隔离 ≠ ABAC 一条规则**：信任级别不同。租户谓词做成 repo 接口**强制 typed 位置参**（路线 A）= 唯一可达 type-system Hard 的形态；ctx-read + archtest 只能 Medium（路线 A 已选）。
- **P0 风险**：① 忘注入租户谓词→全表泄漏（typed param Hard 化消除）；② resource 属性查询无租户隔离→跨租户 ABAC 污染（archtest Medium 守 cross-callsite 同 tenant）；③ async consumer bootstrap ctx 覆盖 entry TenantID（`CTXKEYS-PRINCIPAL-WRITE-CALLER-01` 扩展）。
- **列级 masking**：`ResourceProjection` sealed type（对齐 errcode `PublicDetail` sealed marker）→ handler 返回 full view 编译失败 = Hard。
- **跨 async 边界只传 PrincipalMetadata identity，不传 authz 结论**，消费侧重决策（type system 上 PrincipalMetadata 不含 bool flag = Hard）。
- **AI-robust 评级汇总**：tenant/RowScope typed param funnel → **Hard**；ResourceProjection sealed → **Hard**；ABAC fail-closed / resource-attr 租户一致性 / role-name 字面量 ban / async tenant 来源 → **Medium archtest**。无 Soft 立项。

## 4. 用户决策（AskUserQuestion 确认）

- Epic 结构：新建总 EPIC，#1296 降为子 issue 并入。
- ABAC 深度：完整 policy-as-code 引擎（为 MDM #1051 / 零信任 #1052 预留 device 态势 + 持续评估）。
- 行级防御：路线 A typed 参数 + DB RLS 双层兜底。
- 粒度细化：行列级精确到 **用户级 / 设备级**（subject 引入 `principalKind ∈ {user,device,service}`，device 一等主体）。

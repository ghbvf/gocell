# ADR: 观测三栈反查链路（trace_id → audit），端点收敛后的修正设计

- 状态：Accepted
- 日期：2026-06-02（定稿）/ 范围修正 2026-06-03
- Issue：#1048（[D7] 观测三栈反查链路 — 缺口 11）
- 关联：004 §缺口 11、005 W1；延后项 #1447（OTel metric exemplar 自动注入）；Hard 化路径 #1501
- Supersedes：PR #1457（endpoint-centric 设计，未合入，因与 develop 深度分叉 + 重复债作废）

## 1. Context

oncall 拿到一个 `trace_id` 无法直接定位对应的审计记录——cell label 在 HTTP metrics / span / audit ledger 三处已对齐，但 `trace_id → audit` 的反查链路缺失，排障靠手工拼接。

原始设计（PR #1457）走 endpoint-centric 路线：独立 `/internal/v1/audit/correlate` 反查端点 + `?cell=` owner-lookup mode + 自造 `hasMore`/`returned` 分页 + 经 `ModuleExports.AuditQueryStore` 借 auditcore 的 store 取数。2026-06-03 评审认定该路线**大半重复既有面**，整体作废。

## 2. Decision

只补**真核心**，复用既有 Hard 面，零新 Soft reach-in：

1. **`kernel/observability/correlation.Correlation`** — 从 W0 outbox observability envelope（`kernel/outbox.ObservabilityMetadata`）派生的 sealed read-model。`{traceID, requestID, correlationID}` 全字段 unexported，唯一构造路径 `FromObservability(meta)` —— 包外结构字面量无法设私有字段，**fabrication 在 type system 层不可表达**（上游 Hard 单源派生）。`TraceParent` 刻意不携带（read-model 表达三个 cross-cutting opaque id，非完整 W3C 传播上下文）。
2. **audit ledger `trace_id`** — `ledger.Entry` 新增 `TraceID` 字段 + `audit_entries.trace_id` 列（migration 047）+ `(namespace, trace_id)` 索引（migration 048，`CREATE INDEX CONCURRENTLY` 独立非事务迁移）。注入唯一路径 = `cells/auditcore/internal/appender`，经 `correlation.FromObservability(entry.Observability())` 同时派生 `trace_id` 与 `correlation_id`（read-model 的真实消费者）。
3. **auditquery `?traceId=` filter** — `AuditFilters.TraceID` + mem/PG store WHERE 复合 + `GET /api/v1/audit/entries?traceId=` query param，**复用** auditquery 既有 store / admin policy / 游标分页 / 出口 redaction。

### 2.1 端点收敛——为何不另立 `/correlate`

| 原 `/correlate` 模式 | 重复了谁 | 归宿 |
|---|---|---|
| `?traceId=` 反查 audit | **auditquery**（原生 store + admin policy + 游标分页） | 给 auditquery 加 `traceId` filter |
| 自造 `hasMore`/`returned` 分页 | auditquery 游标分页 | 删，用标准 `nextCursor`/`hasMore` |
| `?cell=` → owner + selector | **devtools catalog**（Backstage `spec.owner`） | 溶进 devtools；owner 已原生暴露 |
| 独立 framework 端点 + 借 cell store | develop #1423 已删 `ModuleExports`（framework 不借 cell store） | 整体删除 |

OSS 一致做法：ownership 放在 service-catalog 实体上（Backstage `spec.owner` + observability annotations；k8s labels/ownerReferences；Prometheus/Alertmanager owner 作 label 并按 label 路由），**无人**为「谁拥有 / 反查」另立端点。

### 2.2 trace_id 刻意不链入 HMAC

`trace_id` 是 observability metadata，**不是 audited fact**。`Protocol.ComputeHash` 的 12-field 输入冻结不变（`tools/archtest/audit_hash_input_frozen_test.go` 守）。理由：把 trace 上下文的存在/缺失（background job / test / 非 instrumented flow 都无 trace）耦合进 tamper-evidence 在运维上脆弱。`TestMemStore_TraceID_NotInHashChain` 证明两条仅 `trace_id` 不同的 entry 产生相同 hash。迁移为 ADD COLUMN 加法演化（`NOT NULL DEFAULT '' → DROP DEFAULT`，对齐 `correlation_id` 语义），**非 DROP，无 invariant 移除**。

### 2.3 auditquery filter 不开后门

`auditQueryPolicy` 本体不改：tenant-bearing caller fail-closed 403；非 admin caller 经 `actorID = subject` 默认强制 `actor_id = self`。`?traceId=` 只是再 AND 一个 store WHERE，非 admin 的 trace 查询永远 AND `actor_id=self`（只能看自己那条 trace 的行），admin 全局。结构上无越权面。

## 3. 威胁矩阵

| 威胁 | 防御 | 评级 |
|------|------|------|
| 业务代码伪造 `Correlation` read-model | 全字段 unexported + 唯一构造器 `FromObservability`；包外字面量编译不可表达 | **上游 Hard**（sealed read-model 单源派生） |
| 业务代码把伪造的 trace_id 写进 audit 记录 | (a) 业务 cell 持不到 audit Store → 调不到 `Append`；(b) trace_id 值的可信来源 = sealed `outbox.Entry`（`OUTBOX-ENTRY-SEALED-CONSTRUCTION-01`）经 appender 提取；(c) `AUDIT-TRACE-ID-WRITE-CALLER-01` 锁 `ledger.Entry.{TraceID,CorrelationID}` 写侧 = appender + storetest | 上游 Hard（继承 sealed outbox.Entry）/ **下游 Medium**（archtest caller-allowlist，见 §4） |
| 篡改 hash 链伪造 tamper-evidence | `Store.Append` 无视 caller 传入的 `Hash`/`PrevHash`，一律 `Protocol.ComputeHash` 重算 + `Verify()` 读时校验；trace_id 不在 hash 输入内（加 trace_id 不改链） | runtime 强制（既有，不变） |
| 非 admin 经 `?traceId=` 越权他人审计行 | `auditQueryPolicy` 强制非 admin `actor_id=self`，与 traceId filter AND | 既有 policy（不变） |
| trace_id 出口泄漏 PII | trace_id 是 opaque 非敏感 id，不在 `pkg/redaction` sensitive-key 集；`RedactPayload` 仍只脱敏 payload nested object | 既有出口 redaction（不变） |

> **逐行对照（amendment 必查，本 ADR 为修正范围首版，非 amendment）**：相对 PR #1457 endpoint-centric 设计，删除端点消除了「framework 借 cell store（撞 #1423）」「internal listener caller-cell allowlist 无法声明 framework route（启动即崩）」两个 ❌ 格子；新增的反查面全部落在 auditquery（store cell-native，Hard）+ devtools catalog（codegen golden，Hard）既有 Hard 面之上，无新增攻击面。

## 4. AI-robust 评级

| 载体 | 上游 | 下游 |
|------|------|------|
| `correlation.Correlation` 派生 | **Hard**（sealed 单源：unexported 字段 + 唯一 `FromObservability`） | — |
| `AUDIT-TRACE-ID-WRITE-CALLER-01`（守 `ledger.Entry.{TraceID,CorrelationID}` 写侧） | **Hard（继承）** sealed `outbox.Entry` + `correlation.Correlation` | **Medium**：archtest type-aware caller-allowlist（go/types 字段-写扫描，import-alias/dot-import 无效） |
| trace 反查 / cell-owner | — | **不新增端点**，复用 auditquery（store cell-native Hard）+ devtools catalog（codegen golden Hard），**零新 Soft** |

`AUDIT-TRACE-ID-WRITE-CALLER-01` 下游为何只能 Medium：`ledger.Entry` 字段必须导出（PG adapter `rows.Scan(&e.Field)` reflect/scan 重建需要），Go 类型系统无法表达「只有 appender 能写该导出字段」，archtest 是唯一下游 backstop。这是与 #851（SPAN-SETATTR-HOLDER-SEAL）/ #893（HEALTHZ-HOLDER-SEAL）同款永久 Medium 天花板。

**已评估并否决「现在就全 seal `ledger.Entry`」**：seal 关不掉 live 漏洞（业务 cell 调不到 `Append`；hash 链 runtime 重算 + `Verify`；残留向量正是该 archtest 抓的），代价是改 `Store.Append` 契约（mutate→return sealed）+ reshape 核心 audit 类型，对一个 P2 特性 over-reach。Hard 化路径（把 `ledger.Entry` 全字段私有化，对齐 `outbox.Entry` sealed construction）作 won't-do-now 跟踪于 **#1501**，并在 `tools/archtest/audit_trace_id_write_caller_test.go` godoc 点名。

## 5. Consequences

- oncall 反查：`GET /api/v1/audit/entries?traceId=<tid>`（admin token 全局；非 admin 仅自己的行）。
- metric exemplar（trace_id 自动附在 Prometheus exemplar 上）仍缺，需独立 OTel SDK 写侧机制，延后 #1447；反查链路不依赖它。
- `correlation.Correlation` read-model 当前消费者 = audit appender；未来 #1447 / 其它工具可复用同一 sealed 派生。

## 6. Alternatives considered

- **独立 `/correlate` 端点（PR #1457）**：作废，见 §2.1。
- **现在就 seal `ledger.Entry`**：否决，见 §4（#1501 跟踪 Hard 化路径）。
- **trace_id 链入 HMAC**：否决，见 §2.2（observability metadata 非 audited fact，trace 缺失场景使 tamper-evidence 脆弱）。

ref: kubernetes apiserver healthz（wire/klog 双 buffer 分离范式）；Backstage `spec.owner`（ownership 放 catalog 实体，不另立端点）；hashicorp/vault audit log_raw（出口脱敏）。

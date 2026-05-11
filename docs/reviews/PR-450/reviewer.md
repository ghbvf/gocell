# PR #450 六维度代码审查报告

**分支**：`refactor/554-pg-s7-audit-ledger`，base `develop`
**审查时间**：2026-05-11

---

## 总体判断

**需修复**（部分 P0/P1 需 PR 内解决，部分 P1 可 follow-up）

整体质量较高：分层清晰、fail-fast 设计完备、安全路径（HMAC key 防御性拷贝 + `clear()`、RedactPayload 出口 redaction、PG 参数化查询）均无明显漏洞。主要问题集中在：
1. 文档与实现不一致（doc.go fingerprint 描述已过时）
2. 审计时间戳使用消费时钟而非原始事件时间（影响审计语义）
3. MemStore 与 LedgerStore 的 `Query` 排序不一致（storetest 未覆盖排序验证）
4. `strictTailVerifyOnStartup` 全量扫描导致大数据量时启动超时风险
5. `auditQueryFetchCap = 5000` 内存加载上限导致潜在 OOM，且 5000 行超过了 `query.MaxPageSize = 500` 的设定意图

---

## Findings 清单（P0→P2，同级内 Cx1→Cx4）

### P0

#### F-01 `[P0] [Cx2] [安全/DX]` `runtime/audit/ledger/doc.go:46`

**问题**：`IdempotencyContentFingerprint` 的 godoc 描述使用了过时的多字段 fingerprint 规格（`eventID + eventType + actorID + UnixNano(timestamp) + payload`），但 F-CR-2 修复后实际实现（`mem_store.go:contentFingerprint`、PG `selectFingerprintSQL`）仅使用 `EventID` 一个字段。文档与代码不一致会让消费方在排查重复 append 时误判，也会让 security reviewer 无法正确评估幂等语义。

**证据**：
- `runtime/audit/ledger/doc.go:46`：`IdempotencyContentFingerprint uses a SHA-256 digest of the entry fields (eventID + eventType + actorID + UnixNano(timestamp) + payload)`
- `runtime/audit/ledger/mem_store.go:285-288`：`contentFingerprint` 仅写入 `e.EventID`
- `adapters/postgres/audit_ledger_store.go:83-87`：`selectFingerprintSQL` 仅过滤 `event_id`

**建议**：将 `doc.go:46` 改为：`IdempotencyContentFingerprint uses the entry's EventID as the sole idempotency key (F-CR-2: Timestamp is excluded because at-least-once redelivery produces a new timestamp per attempt).`

---

### P1

#### F-02 `[P1] [Cx2] [安全/产品]` `cells/auditcore/internal/appender/service.go:133`

**问题**：审计条目的 `Timestamp` 使用消费时本地时钟 `s.clk.Now()`，而非原始事件的产生时间（`entry.CreatedAt`）。在 at-least-once 重投递场景下，同一事件每次消费都产生不同的 Timestamp，导致：
1. 审计日志的时间轴与实际业务事件发生时间不匹配（审计语义失真）
2. 幂等性依赖 EventID-only 指纹（F-CR-2 正确），但时间失真已进入 hash chain（`Timestamp.UnixNano()` 参与哈希计算），重投递路径被幂等挡住，但首次延迟投递时存入的时间是"消费时间"而非"事件时间"

**证据**：`service.go:133` `Timestamp: s.clk.Now()`；`outbox.Entry` 有 `CreatedAt time.Time` 字段（`kernel/outbox/outbox.go:95`）

**建议**：将 `Timestamp: s.clk.Now()` 改为 `Timestamp: entry.CreatedAt`，确保审计条目携带原始事件产生时间。若 `entry.CreatedAt` 在极端场景可能为零值，增加 fallback `if entry.CreatedAt.IsZero() { entry.CreatedAt = s.clk.Now() }` 并记录 Warn 日志。同步更新 storetest 相关测试用例。

**影响文件**：`cells/auditcore/internal/appender/service.go`、`cells/auditcore/internal/appender/service_test.go`（需更新测试）

---

#### F-03 `[P1] [Cx2] [测试/运维]` `cells/auditcore/slices/auditquery/service.go:29`、`runtime/audit/ledger/store.go:44-52`

**问题**：`auditQueryFetchCap = 5000` 作为内存缓冲全量加载上限，单次 HTTP 请求最多将 5000 条 audit entry 加载进内存后做 in-memory cursor/sort，这与 `query.MaxPageSize = 500` 的 API 层上限（用于 HTTP 出口分页）存在 10 倍差距——前者是内存保护、后者是 HTTP 语义，但两者未形成文档化的意图对齐。更重要的是，随着 audit_entries 表增长，5000 行全量加载存在 OOM 风险（每行 payload 可能数百字节，5000 行 = 数兆字节）。`QueryListParams` 的注释也已写明"PG store will replace this with keyset cursor semantics"——当前 PG 实现并未实现 keyset，仍走内存路径。

**证据**：
- `service.go:29`：`const auditQueryFetchCap = 5000`
- `service.go:83`：`all, err := s.store.Query(ctx, filters, ledger.QueryListParams{Limit: auditQueryFetchCap})`
- `store.go:43-52`：`QueryListParams` 注释声明 PG store 应替换为 keyset cursor，但当前 PG 实现未实现

**建议**：
1. 在代码注释中明确记录 `auditQueryFetchCap` 的 OOM 风险及触发条件（当前服务已有 Warn 日志，但缺少运维告警建议）
2. 在 `store.go` 的 `QueryListParams` 注释中补充说明当前全量加载的 P99 内存预算假设
3. backlog 中已有 `S8-AUDIT-QUERY-KEYSET-PUSH-DOWN-01`，需确认其优先级（P1 级别依赖 audit 表规模）

---

#### F-04 `[P1] [Cx3] [运维/测试]` `cells/auditcore/cell.go:301`

**问题**：`strictTailVerifyOnStartup` 在每次进程启动时对整个链执行 `Verify(ctx, 1, tail.SeqNo)`（全量扫描）。当 audit_entries 表积累数十万条目后，启动时全量 HMAC 重计算会导致：
1. 进程启动超时（k8s readiness probe 默认 10-30s，全量验证可能超时）
2. 每次部署/pod 重启都触发昂贵的全量读

**证据**：`cell.go:301` `valid, firstInvalid, err := c.ledgerStore.Verify(ctx, 1, tail.SeqNo)`

**建议**：这是 Cx3 架构级问题，需要决策：
- 方案 A：仅验证最近 N 条（e.g. 1000）作为"尾部 integrity sampling"，降低启动成本，需明确文档化 trade-off
- 方案 B：维护一个 checkpoint 表记录最后验证通过的 `lastVerifiedSeq`，启动时从 checkpoint 续验
- 方案 C：降级为异步后台验证（启动时不阻塞，3 分钟后结果报 readyz），适合 SLA 要求不阻塞启动的场景

当前实现缺少时间预算保护。需在 `strictTailVerifyOnStartup` 上至少增加 context timeout 保护（如 `ctx, cancel := context.WithTimeout(ctx, 30*time.Second)`），防止 k8s 启动超时无法 recover。

---

#### F-05 `[P1] [Cx2] [测试]` `runtime/audit/ledger/storetest/suite.go`、`adapters/postgres/audit_ledger_store.go:386`

**问题**：MemStore 的 `Query` 按 SeqNo 升序（FIFO）返回结果，而 PG `LedgerStore.Query` 使用 `ORDER BY timestamp DESC, id ASC`（时间戳降序）。storetest conformance suite 的 `runQueryByFilters` 只验证结果数量（`len(results) == 3`），没有验证排序顺序，导致两个实现的排序不一致未被测试发现。

**证据**：
- `mem_store.go:159-176`：`Query` 按 `m.entries` 顺序（升序 SeqNo）迭代
- `audit_ledger_store.go:386`：`b.Append("ORDER BY timestamp DESC, id ASC")`
- `storetest/suite.go:528-530`：`assert len(results) == 3` 无排序验证

**建议**：在 storetest suite 中增加排序验证用例，或在 `Query` 返回前统一排序（选择其中一种语义作为 contract）。若 auditquery service 依赖降序（`auditSort` 已声明 `timestamp DESC`），则 MemStore 也应返回降序结果。建议以 `timestamp DESC` 为 contract 标准，统一 MemStore 行为，并在 storetest 增加排序断言。

---

#### F-06 `[P1] [Cx1] [运维]` `adapters/postgres/migrations/020_audit_ledger.sql:29`

**问题**：migration 020 使用了 `SET LOCAL lock_timeout = '5s'`，但此指令只在事务内有效，而 goose up 在事务内执行，因此 `SET LOCAL` 是正确的。不过 migration down 路径缺少 `lock_timeout` 保护，且注释中警告 down migration 会"PERMANENTLY DELETE all audit data"，但没有任何防止意外执行的机制（如需要 `CONFIRM_AUDIT_DELETE=yes` 环境变量）。此外，`SET LOCAL lock_timeout = '5s'` 只限制了 advisory lock 和 `ALTER TABLE`，`CREATE TABLE` 本身也会申请 `AccessExclusiveLock`——对于新表这是安全的，但注释应明确说明为什么 down 路径不需要同等保护。

**证据**：`020_audit_ledger.sql:29` `SET LOCAL lock_timeout = '5s'`；down 路径第 53-60 行无 timeout 保护

**建议**：在 down 路径前补充 `SET LOCAL lock_timeout = '5s'`；在 down 路径注释增加生产操作步骤（需先 `pg_dump`）。这是低优先级运维改进。

---

#### F-07 `[P1] [Cx1] [DX]` `cells/auditcore/slices/auditquery/service.go:62-66`

**问题**：`Query` 方法中，`query.QueryContext` 将 `filters.From.Format(time.RFC3339Nano)` 和 `filters.To.Format(time.RFC3339Nano)` 作为字符串记录到查询上下文，当 `filters.From` 为零值（`time.Time{}`）时会产生 `0001-01-01T00:00:00Z` 这样的无意义字符串进入日志，增加日志噪音，且不符合结构化日志的 zero-value = 省略约定。

**证据**：`service.go:65-66`：
```go
"from", filters.From.Format(time.RFC3339Nano),
"to", filters.To.Format(time.RFC3339Nano),
```

**建议**：对零值 time 使用空字符串或 omit：
```go
"from", cond(filters.From.IsZero(), "", filters.From.Format(time.RFC3339Nano)),
```
或在记录前检查是否为零值。

---

#### F-08 `[P1] [Cx1] [安全]` `cells/auditcore/internal/appender/service.go:155-158`

**问题**：成功路径日志记录了 `slog.String("actor_id", e.ActorID)`。`ActorID` 通常是用户 UUID，不是 PII，但 `actor_id` 字段在 audit 上下文中确实携带用户身份信息。根据 `observability.md` 的规范，结构化字段需谨慎——`actor_id` 本身属于可追溯的身份标识，在某些合规要求（GDPR 等）下不应无条件记录到访问日志。

**证据**：`service.go:157` `slog.String("actor_id", e.ActorID)`

**建议**：由产品/合规决策是否需要在此 Info 日志中包含 `actor_id`。若保留，确认日志后端已加 IAM 访问控制。这是一个合规合理性确认项。

---

### P2

#### F-09 `[P2] [Cx1] [DX]` `adapters/postgres/audit_ledger_store.go:302-308`

**问题**：`tailWithCountSQL` 中 `$1` 参数在同一 SQL 语句的两处（子查询和主查询 `WHERE` 子句）分别引用，调用时只传一个参数 `ns`。虽然 PostgreSQL 允许同一参数在查询中多处引用，但 pgx 中此行为（单参数多次占位引用）需确认 pgx/v5 明确支持，否则存在 runtime 错误风险。从 pgx 文档来看，参数编号（`$1`）在整个查询中是全局的，引用同一占位符是合法的，但缺少注释说明这一边界条件。

**证据**：`audit_ledger_store.go:302-308`，`$1` 出现两次，调用 `s.pool.QueryRow(ctx, tailWithCountSQL, ns)` 只传了 1 个参数

**建议**：在 `tailWithCountSQL` 注释中补充说明 "`$1` is referenced twice; pgx passes a single argument bound to both occurrences"，避免后续维护者误以为是 bug。

---

#### F-10 `[P2] [Cx1] [DX]` `runtime/audit/ledger/doc.go:46`（与 F-01 相同位置，附加问题）

**问题**：`doc.go:27` 的哈希链算法描述与 `protocol.go:164` 的 `ComputeHash` 实现一致（包含 Timestamp），但 `doc.go:46` 同时描述了 fingerprint（只有 EventID），两段描述在同一 doc 中极易混淆。

**建议**：在哈希链算法说明和幂等 fingerprint 算法说明之间增加一行注释区分两者职责，避免读者将两者混淆。

---

#### F-11 `[P2] [Cx1] [DX]` `cmd/corebundle/audit_module.go:79`

**问题**：`clear(hmacKey)` 在 `MustNewProtocol` 调用后再次清零 `hmacKey` 局部变量，注释说明这是 belt-and-suspenders。但 `MustNewProtocol` → `WithChainHMAC` 内部已经调用了 `clear(key)` 清零调用方 slice（`protocol.go:218`）。同时清零两次会让阅读者困惑，也需要确认两次 `clear` 针对的是同一底层数组（`hmacKey` 在 `WithChainHMAC` 调用后已被清零，此时再 `clear` 只操作同一 slice header 指向的已清零内存，安全但冗余）。

**证据**：`audit_module.go:79` `clear(hmacKey)`；`protocol.go:218` `clear(key)`

**建议**：保留一次 `clear` 即可，并在注释中说明 `WithChainHMAC` 已完成清零，此处 `clear` 是防御性二次清零（或删除冗余的 `clear`），选其一。当前两处均存在反而容易引起读者误以为 `WithChainHMAC` 内部没有清零。

---

#### F-12 `[P2] [Cx1] [DX]` `cells/auditcore/slices/auditverify/service.go:36-43`

**问题**：`auditverify.WithTxManager` 接受 `persistence.TxRunner`（原始接口）而非 `persistence.CellTxManager`（sealed marker）。在 `cell.go:389` 调用 `auditverify.WithTxManager(c.txRunner)` 时，`c.txRunner` 类型为 `persistence.CellTxManager`（它嵌入了 `TxRunner`，所以可赋值）。但这意味着如果未来有人直接从外部构造 `auditverify.Service`（绕过 cell.go），可以注入原始 `TxRunner` 而不需要走 sealed 路径。这不是立即风险（`auditverify` 是 cell-internal，外部不可见），但与项目 sealed marker 范式不一致。

**证据**：`auditverify/service.go:37` `func WithTxManager(tx persistence.TxRunner) Option`

**建议**：`auditverify` 是 cells/ 内部 slice，不是公开的 cell-root API，当前 `CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01` archtest 不覆盖此路径（scope 限于 `cells/<x>/*.go`），所以不是 archtest 违规。但为一致性，可以将 `auditverify.WithTxManager` 改为接受 `persistence.CellTxManager`，并确认 appender.WithTxManager 同样处理（P2 follow-up）。

---

#### F-13 `[P2] [Cx1] [测试]` `adapters/postgres/audit_ledger_store_test.go:137`

**问题**：`TestAuditLedgerStore_RestartRecovery_AcrossPool` 中 migration table 名为硬编码字符串 `"schema_migrations_restart_a"`，不使用 `migrationsTableName(t, ...)` 工具函数进行 63 字符截断处理。虽然 `"schema_migrations_restart_a"` 本身长度是 28 字节（安全范围内），但与其他测试的约定不一致，且如果将来重命名测试文件，可能因为名称冲突导致问题。

**证据**：`audit_ledger_store_test.go:137` `"schema_migrations_restart_a"` 及第 241 行 `"schema_migrations_subrange_tampered"`（30 字节，安全），第 292 行 `"schema_migrations_advlock"`，第 409 行 `"schema_migrations_nsiso"`

**建议**：统一改为使用 `migrationsTableName(t, prefix)` 工具函数，保持代码一致性。

---

#### F-14 `[P2] [Cx1] [DX]` `cells/auditcore/slices/auditquery/handler.go:25`

**问题**：`auditQueryPolicy` 函数注释中的 backlog 标记 `(S43, tracked by PERMISSION-BASED-AUTHZ-01)` 缺少对应的 backlog 条目（未在代码库中发现 PERMISSION-BASED-AUTHZ-01 的注册位置）。根据 CLAUDE.md 规则"PR 范围切割必须显式 backlog"，暂缓项需要有明确的 backlog 条目。

**证据**：`handler.go:25`：`// Deferred (S43, tracked by PERMISSION-BASED-AUTHZ-01)`

**建议**：在 `journeys/status-board.yaml` 或项目 backlog 中增加 `PERMISSION-BASED-AUTHZ-01` 条目，记录延迟迁移到 permission-based authz 的上下文和触发条件。

---

#### F-15 `[P2] [Cx2] [测试]` `cells/auditcore/slices/auditverify/service_test.go`

**问题**：`auditverify/service_test.go` 缺少以下关键覆盖：
1. `VerifyChain` 返回 `valid=false` 时的行为（只测试了 valid=true 和 invalid range error 两种情况）
2. `outbox.Emit` 失败时 `VerifyChain` 的错误返回语义（L2 保证）
3. `EntriesChecked` 在 `valid=false` 时的计算正确性

**证据**：`service_test.go` 只有 3 个测试：`TestNewService_TxRunnerRequired`、`TestService_VerifyChain_Empty`、`TestService_VerifyChain_ValidEntries`、`TestService_VerifyChain_InvalidRange_Error`

**建议**：增加 `TestService_VerifyChain_InvalidChain` 用例（注入篡改后的 MemStore），以及 `TestService_VerifyChain_EmitFails` 用例（注入 failing emitter），确保 L2 atomicity 语义可测试。

---

## 复杂度汇总

```
Cx1: 9 (F-01, F-06, F-07, F-08, F-09, F-10, F-11, F-13, F-14)
Cx2: 4 (F-02, F-03, F-05, F-12, F-15)
Cx3: 1 (F-04)
Cx4: 0
```

---

## 修复分流建议

### Cx1/Cx2 候选 → 可派发 developer agent 处理

| Finding | 文件 | 修复要点 |
|---------|------|---------|
| F-01 (Cx1, P0) | `runtime/audit/ledger/doc.go:46` | 修正 IdempotencyContentFingerprint 描述为 EventID-only |
| F-02 (Cx2, P1) | `cells/auditcore/internal/appender/service.go:133` + `service_test.go` | `Timestamp: entry.CreatedAt`（含零值 fallback） |
| F-05 (Cx2, P1) | `storetest/suite.go` + `mem_store.go:Query` | 统一 Query 排序语义，storetest 增加排序断言 |
| F-07 (Cx1, P1) | `cells/auditcore/slices/auditquery/service.go:62-66` | 零值 time 不格式化输出 |
| F-09 (Cx1, P2) | `adapters/postgres/audit_ledger_store.go:302` | 增加 `$1` 双引用注释 |
| F-11 (Cx1, P2) | `cmd/corebundle/audit_module.go:79` | 去除冗余 `clear` 或加注释说明 |
| F-13 (Cx1, P2) | `adapters/postgres/audit_ledger_store_test.go:137` | 统一使用 `migrationsTableName` |
| F-14 (Cx1, P2) | `cells/auditcore/slices/auditquery/handler.go:25` | 注册 backlog 条目 |
| F-15 (Cx2, P2) | `cells/auditcore/slices/auditverify/service_test.go` | 增加 invalid chain + emit fail 测试 |

### Cx3 → 需人工决策（标注 backlog）

| Finding | 问题 | 建议行动 |
|---------|------|---------|
| F-04 (Cx3, P1) | `strictTailVerifyOnStartup` 全量扫描，无时间预算保护 | 架构讨论：tail sampling vs checkpoint vs 异步验证；短期至少增加 `context.WithTimeout` 保护（30s），防止 k8s readiness 超时无法 recover |

### 附加确认项（F-03 Cx2）

F-03 `auditQueryFetchCap = 5000` 内存加载问题：已有 backlog 条目 `S8-AUDIT-QUERY-KEYSET-PUSH-DOWN-01`，需确认其在路线图中的优先级和预期时间。若 audit 表在本 PR 生产上线后 6 个月内不超过 1 万条，当前实现风险可接受；若可能更快增长，需将 S8 任务提前。

---

## 关键安全路径验证（通过）

以下关键安全路径经代码确认无问题，不产生 Finding：

1. **HMAC key 材料保护**：`WithChainHMAC` 防御性拷贝后立即 `clear(key)`（`protocol.go:218`），`HMACKey()` 每次返回副本。
2. **PII redaction 出口**：`toListResponseDataItem` 调用 `redaction.RedactPayload(e.Payload)`（`handler.go:137`），出站路径已覆盖。
3. **内部存储保留原始数据**：`audit_entries.payload` 列存储未经 redaction 的原始 JSONB，合规审计可用。
4. **SQL 注入防护**：所有 SQL 使用参数化查询（`$1`, `$2` 等），`pgquery.NewBuilder` 也使用参数绑定；`lockNamespaceSQL` 对 namespace 使用 `$1` 参数，无拼接。
5. **幂等性设计**：应用层 `checkFingerprint` + DB 层 `uq_audit_namespace_event_id` UNIQUE INDEX 双重保护，并在 advisory lock 内执行。
6. **OUTBOX-TOPIC-FAILOPEN-01**：`auditcore` 使用 `DirectPublishFailClosed`，审计事件不会 opt-in fail-open（archtest `OUTBOX-TOPIC-FAILOPEN-01` 覆盖）。
7. **分层合规**：`cells/auditcore` 不直接 import `adapters/`，通过 `ledger.Store` 接口解耦，`AUDIT-LEDGER-PROTOCOL-COMPOSITION-ROOT-01` 等 archtest 保证 Protocol 只在 composition root 构造。

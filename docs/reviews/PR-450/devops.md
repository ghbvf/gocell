# PR #450 DevOps 审查报告

**审查时间**: 2026-05-11
**审查角色**: DevOps
**Branch**: `refactor/554-pg-s7-audit-ledger`
**Base**: `develop`

---

## 总体判断

**状态: 可以合并（附 1 项 P1 待关注）**

PR 的基础设施质量整体良好：Migration 设计有充分的注释说明决策依据，UP/DOWN 对称，UNIQUE INDEX 双防线符合 F-CR-2 幂等要求。CI 集成测试已通过自动发现机制（`-tags=integration` diff 检测）覆盖新增的 `adapters/postgres/audit_ledger_store_test.go`，无需手动更新 workflow。HMAC key 注入路径经 `buildHMACKey` + `rejectDemoKey` 双重防护，real 模式拒绝已知 demo key。

唯一 P1 问题是 `schema_guard.go` 中新增注释将 `audit_entries` 的迁移序号标注为 `(017)` 而实际文件名为 `020_audit_ledger.sql`——文档与现实不符，虽不影响运行时行为，但会给后续维护者带来混淆。

---

## Findings

### D-01 [P1 / Cx1] schema_guard.go 注释迁移序号错误

**文件**: `adapters/postgres/schema_guard.go:27`

**问题**: 新增的表清单注释写 `audit_entries (017)`，但实际迁移文件是 `020_audit_ledger.sql`。序号 017 对应 `017_users.sql`（accesscore 用户表）。

**建议**: 将注释改为 `audit_entries (020)`。序号是文档中唯一可供运维/开发人员核对对应关系的线索，错误标注会导致迁移故障排查走弯路。

---

### D-02 [P2 / Cx2] hash 字段类型 TEXT 无长度约束

**文件**: `adapters/postgres/migrations/020_audit_ledger.sql:40-41`

**问题**: `prev_hash TEXT NOT NULL` 和 `hash TEXT NOT NULL` 使用无限长 TEXT 类型。HMAC-SHA256 hex 输出固定为 64 字节，使用无界 TEXT 允许存入任意长度字符串。

**建议**: 变更为 `CHAR(64)` 或保留 TEXT 但添加 `CHECK (length(hash) = 64 AND length(prev_hash) = 64)` 约束，在 DB 层做二次防御。当前应用层 `Protocol.ComputeHash` 输出固定 64 字节，约束不影响正常路径，仅防止直接 SQL 篡改引入越界值。

**注**: 由于 `020_audit_ledger.sql` 已包含"此后新增索引必须使用 CONCURRENTLY"的注释，在该表已上线后无法通过新 migration 改列类型（需要 ALTER TABLE，高风险）。若团队评估可接受当前行为，可降至 P3 并登记 backlog。

---

### D-03 [P2 / Cx2] strictTailVerifyOnStartup 全链扫描无超时上限

**文件**: `cells/auditcore/cell.go:291-311`

**问题**: `strictTailVerifyOnStartup` 调用 `Verify(ctx, 1, tail.SeqNo)`，当 audit_entries 积累数十万行后，全链 SELECT + HMAC 重计算会在启动时阻塞较长时间。函数直接使用传入的 `ctx`（来自 bootstrap phase5），该 ctx 无独立超时预算。

**场景影响**: 生产环境运行数月后若需要滚动重启，每个 Pod 在接受流量前需等待全链验证完成。审计链越长，启动越慢，可能触发 K8s readiness probe 超时导致 Pod 被杀。

**建议**: 选项 A（激进）: 限制 `strictTailVerifyOnStartup` 仅验证最近 N 条（如 1000 条）。从完整性角度，尾部完整性是最关键的；历史链的完整验证应由独立的定期后台 job 承担。选项 B（保守）: 在该函数入口注入 `context.WithTimeout(ctx, 30*time.Second)` 上限，超时则记 Warn 日志并跳过（降级为 "best-effort verify"），而不是阻塞启动。登记 backlog 追踪增量验证方案。

---

### D-04 [P2 / Cx1] 021 migration 未在 Down 中设置 lock_timeout

**文件**: `adapters/postgres/migrations/021_audit_entries_event_id_unique.sql`

**问题**: `020_audit_ledger.sql` 的 Up 区块使用 `SET LOCAL lock_timeout = '5s'`，避免 DDL 锁长时间阻塞。`021_audit_entries_event_id_unique.sql` 的 Up 和 Down 均未设置 `lock_timeout`。

**背景**: `DROP INDEX IF EXISTS uq_audit_namespace_event_id` 在高并发环境下需要 ShareLock，若此时有长事务持有 audit_entries 读锁，DROP 会等待。

**建议**: 在 `021_audit_entries_event_id_unique.sql` 的 Up/Down 区块顶部各加一行 `SET LOCAL lock_timeout = '5s';`，与 020 保持一致。

---

### D-05 [P2 / Cx2] audit_ledger_ready 探针未在 docs/ops/readyz.md 注册

**文件**: `adapters/postgres/audit_ledger_store.go:337-348` / `cells/auditcore/cell.go:275-279`

**问题**: `LedgerStore.Probes()` 暴露了 `audit_ledger_ready` 探针，auditcore cell 在 `registerHealthProbes` 中将其注册到 readyz 聚合器。但 `docs/ops/readyz.md` 中既没有列出该探针，也没有说明其 PG-only 激活语义（MemStore 不实现 HealthProber，内存模式下该探针不存在）。

**运维风险**: 运维人员配置告警 `/readyz` 时不知道 `audit_ledger_ready` 探针的存在及其触发条件。

**建议**: 在 `docs/ops/readyz.md` 的探针列表部分添加 `audit_ledger_ready` 条目，说明：该探针仅在 postgres 存储后端下激活，探针逻辑为调用 `Tail(ctx)` 验证 PG 连通性。

---

### D-06 [P2 / Cx1] auditQueryFetchCap 5000 行无超时防护

**文件**: `cells/auditcore/slices/auditquery/service.go:19-33`

**问题**: `auditQueryFetchCap = 5000` 是内存保护上限，但 `store.Query` 调用本身无查询超时。当 audit_entries 大表全表扫描（无 eventType/actorId 过滤时），实际 SQL 可能需要扫描大量行并阻塞较长时间。虽然 `idx_audit_namespace_ts_id` 覆盖了排序方向，但无过滤条件时 LIMIT 5000 仍需扫描相对多的行。

**建议**: 此为已知债务，backlog 中 `S8-AUDIT-QUERY-KEYSET-PUSH-DOWN-01` 是正确的追踪条目。建议补充说明：该接口应配合 API 层 timeout middleware（所有 HTTP handler 已有请求 ctx），在 S8 落地前靠调用方 ctx deadline 兜底。无需修改代码，但建议在 `auditQueryFetchCap` 注释中显式说明"调用方 ctx 超时是唯一的查询超时保护"。

---

### D-07 [P3 / Cx1] integration tag 对 sealed-marker archtest 不可见

**文件**: `cmd/corebundle/audit_test_helper_test.go:1`

**问题**: `//go:build integration` 标签使该 helper 文件只在集成测试上下文编译。archtest 规则 `CELL-RAW-INFRA-WRAPPER-LOCATION-01` 扫描所有 `*_test.go` 文件时是否包含带 build tag 的文件，取决于 archtest 的扫描策略。

**观察**: 文件内容仅包含 helper 函数（`buildTestAuditProtocol`、`buildTestAuditStore`、`auditcoreLedgerOpts`），不调用 `WrapPublisherForCell` / `WrapWriterForCell`，不触发 wrapper location archtest。风险为 P3 低优先级。

**建议**: 现状可接受。已登记到 backlog `AUDIT-LEDGER-CROSS-POOL-TEST-IMPROVE-01` 中（F26 注释说明了共享 pool 的限制）。

---

### D-08 [P3 / Cx1] outbox_failopen_rate probe 名含连字符

**文件**: `kernel/outbox/emitter.go`（out-of-scope，已存在）

**问题**: PR backlog 已记录 `KERNEL-OUTBOX-PROBE-NAME-RENAME-01`，probe 名 `outbox-failopen-rate.auditcore` 违反 observability.md snake_case 约束。

**状态**: 已登记 backlog，P3，非本 PR 引入。此处仅确认 backlog 条目完整，无需 PR 内修复。

---

### D-09 [P3 / Cx1] LedgerStore 无 trace span

**文件**: `adapters/postgres/audit_ledger_store.go`

**问题**: LedgerStore 的 `Append`、`Verify`、`Query` 等方法没有 OTel trace span，与 `adapters/postgres/outbox_writer.go` 中已有的 span 实现不一致。Append 的 advisory lock 等待时间和 hash 计算时间在 trace 中完全不可见。

**建议**: 此为 P3 技术债，不阻塞 PR 合并。建议在 S8 后续 PR 中按 outbox_writer.go 同样模式补充 span。登记 backlog 条目追踪。

---

## 部署 Checklist

按顺序执行：

1. **迁移准备**
   - 确认 `GOCELL_AUDITCORE_HMAC_KEY` 已在生产环境 secrets manager 中配置（`≥ 32 bytes`，非 wellKnownDemoKeys 中的任何值）
   - 确认 `GOCELL_AUDITCORE_CURSOR_KEY` 已配置
   - 验证：`openssl rand -hex 32` 生成新密钥；不得使用 `dev-hmac-key-replace-in-prod!!!!`

2. **Migration 020 + 021 执行**
   - 在 staging 先跑：`goose -dir adapters/postgres/migrations postgres "$DSN" up`
   - 验证 migration version 到达 21：`SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`
   - 验证表结构：`\d audit_entries` 应有 11 列，含 `uq_audit_namespace_seq` 约束
   - 验证索引：`\di audit_entries*` 应有 3 个索引（PRIMARY KEY + idx_namespace_ts_id + idx_namespace_event_type）+ UNIQUE INDEX `uq_audit_namespace_event_id`

3. **部署 corebundle**
   - 以 `GOCELL_CELL_ADAPTER_MODE=postgres` 和 `GOCELL_STORAGE_BACKEND=postgres` 启动
   - 观察启动日志：期望看到 `auditcore: tail verify passed (empty store)`（首次部署）或 `auditcore: tail verify passed seq_no=N`（已有数据时）
   - 若看到 `auditcore: chain integrity broken on startup`，立即停止部署，触发回滚 runbook

4. **验证 readyz 探针**
   - `curl -s http://localhost:9091/readyz | jq '.dependencies.audit_ledger_ready'` 应返回 `"healthy"`
   - MemStore 模式（dev）该字段不存在，属正常行为

5. **验证 HMAC key 拒绝（real 模式）**
   - `GOCELL_CELL_ADAPTER_MODE=real` 下若设置了 wellKnownDemoKeys 中的值，启动应 fail-fast
   - 确认错误信息包含 `"is set to a well-known demo key"`

6. **端到端 smoke 验证**
   - 触发一个 session.created 事件，验证 `GET /api/v1/audit/entries` 返回带 payload 的审计记录
   - 验证 payload 中的敏感字段（如 `password`）已被 `<REDACTED>` 替换（B2-C-09 redaction）
   - 验证重复 EventID 的 Append 返回 `ErrAuditLedgerAlreadyExists` 而不是写入第二条记录

---

## 回滚 Runbook

### 场景 A：Migration 021 上线后需要回滚

021 仅是 UNIQUE INDEX，Down 不删表数据：

```sql
-- 查看当前 migration 版本
SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1;

-- 回滚 021（删除 UNIQUE INDEX，保留 audit_entries 表和数据）
-- 注意：这会移除 DB 层的 duplicate-EventID 防线，但应用层 fingerprint check 仍有效
goose -dir adapters/postgres/migrations postgres "$DSN" down-to 20
```

回滚后应用层 `selectFingerprintSQL` 仍提供单实例防重复，仅并发竞争时可能产生重复记录。

### 场景 B：Migration 020 上线后需要回滚（数据丢失，高风险）

**警告：Down 020 会永久删除所有 audit_entries 数据。生产环境禁止直接执行，必须先备份。**

```bash
# Step 1: 备份（生产必做，否则不可回滚）
pg_dump -h $PG_HOST -U $PG_USER -d $PG_DB \
  -t audit_entries \
  --no-owner --no-acl \
  -f audit_entries_backup_$(date +%Y%m%d%H%M%S).sql

# Step 2: 确认备份完整
wc -l audit_entries_backup_*.sql  # 应 > 0

# Step 3: 回滚（同时 down 021 和 020）
goose -dir adapters/postgres/migrations postgres "$DSN" down-to 19
```

### 场景 C：启动时 strictTailVerifyOnStartup 报告链断裂

症状：日志包含 `auditcore: chain integrity broken on startup`，`first_invalid_seq` 字段指出最先损坏的序号。

处置步骤：

1. 立即阻止新 Pod 启动（修改 Deployment replicas=0 或 HPA minReplicas=0）
2. 保留现有运行中的 Pod（已跳过验证，依然服务中），不滚动更新
3. 执行取证查询，确认是否真实篡改：
   ```sql
   SELECT seq_no, event_id, hash, prev_hash
   FROM audit_entries
   WHERE namespace = 'auditcore'
   ORDER BY seq_no
   LIMIT 20 OFFSET <first_invalid_seq - 1>;
   ```
4. 通知合规团队（audit chain 断裂属于安全事件）
5. 禁用 strictTailVerify 的临时 workaround：删除 `WithRestartRecovery(RestartRecoveryStrictTailVerify{})` 选项后重新部署（此举允许链断裂后继续写入，仅作临时应急，必须同步启动事后调查）

### 场景 D：HMAC key 泄漏或丢失

HMAC key 泄漏意味着攻击者可以伪造 hash，使篡改过的 audit_entries 通过 Verify 检查。

1. **泄漏时**: 生成新 key，滚动更新到新 key。历史链用旧 key 签名，新链用新 key 签名——链在 key 轮换点处"断开"（Verify 跨越轮换点会失败），这是设计权衡。如需无缝轮换，需要在 `protocol.go` 中实现 key rotation API（当前未支持，见 ADR §D1）。
2. **丢失时**: 历史链无法验证，但 audit_entries 数据本身完整。可通过业务端日志/outbox event 重建事件顺序，但 HMAC 完整性证明失效。需通知合规团队评估合规影响。
3. 任何 key 变更后，在 wellKnownDemoKeys 中追加旧 key，防止意外重用。

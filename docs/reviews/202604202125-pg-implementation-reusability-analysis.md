# PG 实现复用性分析报告

> 基准：develop 分支（R1a/R1b/R1c/R1d 合入后）
> 范围：config-core PG 实现能力盘点 + 其他 Cell 接入评估
> 日期：2026-04-20

---

## 一、总体结论

GoCell 的 PG 能力分为两层：**框架层通用基础设施**（可直接复用，零改动）和 **Cell 层业务适配**（每个 Cell 自己写，但有明确样板）。

```
adapters/postgres/          ← 框架层，所有 Cell 共享，不改动
  pool.go                   连接池创建与健康探针
  pool_resource.go          lifecycle.ManagedResource 实现
  tx_manager.go             事务管理 + savepoint 嵌套
  outbox_store.go           outbox_entries 表 CRUD（SQL 方言中立）
  outbox_writer.go          原子 outbox 写（单条 + 批量）
  migrator.go               goose 包装，支持嵌入式 FS
  schema_guard.go           启动时版本预检，fail-fast

cells/config-core/internal/adapters/postgres/   ← Cell 层，config-core 专属，作为样板
  session.go                将 pgxpool.Pool + pgx.Tx 统一为本地 DBTX 接口
  config_repo.go            ConfigRepository 实现（含字段加密）
  flag_repo.go              FlagRepository 实现
  plaintext_migration.go    存量明文数据补加密工具
```

---

## 二、通用能力（直接复用，零改动）

### 2.1 连接池 + 生命周期

| 组件 | 文件 | 能力 |
|------|------|------|
| `adapters/postgres.Pool` | `pool.go` | pgxpool 包装，DSN/maxConns/idle timeout 从环境变量读取，Health() 5s 超时探针 |
| `adapters/postgres.PGResource` | `pool_resource.go` | 实现 `kernel/lifecycle.ManagedResource`，暴露 `db_pool` 健康检查，托管 relay worker 生命周期 |
| `adapters/postgres.Statter` | `pool_statter.go` | OTel metrics 适配，把 pgxpool.Stat 映射为中立的 poolstats.Snapshot |

**其他 Cell 如何使用**：在 cell module 的 `Provide()` 里构建 `adapters/postgres.NewPool(ctx, cfg)`，包装为 `PGResource`，通过 `bootstrap.WithManagedResource(pgRes)` 注入生命周期管理。

---

### 2.2 事务管理

| 组件 | 文件 | 能力 |
|------|------|------|
| `adapters/postgres.TxManager` | `tx_manager.go` | `RunInTx(ctx, fn)` 自动提交/回滚；检测 ctx 已携带 tx 时降级为 savepoint，支持嵌套事务；panic 自动回滚 |
| `kernel/persistence.TxCtxKey` | `kernel/persistence/txctx.go` | 上下文键，TxManager 写入，Cell 本地 repo 读取；kernel-owned 避免 Cell 和 adapter 互相 import |
| `kernel/persistence.TxRunner` | `kernel/persistence/tx.go` | 接口定义；`NoopTxRunner` 提供 demo 模式降级 |

**其他 Cell 如何使用**：Cell 的 service 层通过 `ports.TxRunner`（实际注入 `adapters/postgres.TxManager`）调用 `RunInTx`，在闭包内同时写业务数据和 outbox，两个操作在同一事务里。

---

### 2.3 Outbox（事务型事件发布）

| 组件 | 文件 | 能力 |
|------|------|------|
| `adapters/postgres.PGOutboxStore` | `outbox_store.go` | 实现 `runtime/outbox.Store`；`ClaimPending` FOR UPDATE SKIP LOCKED；指数退避重试；`ReclaimStale` 恢复超时 claiming 行 |
| `adapters/postgres.OutboxWriter` | `outbox_writer.go` | `Write/WriteBatch`，从 ctx 取 pgx.Tx；批量写按 7000 分块防 65535 参数限制 |
| `runtime/outbox.Relay` | `runtime/outbox/relay.go` | 从 Store 轮询 → 发布到 EventBus → MarkPublished/MarkRetry/MarkDead |

**共享表**：所有 Cell 写同一张 `outbox_entries` 表，按 `aggregate_type`/`event_type` 区分。不需要每个 Cell 建自己的 outbox 表。

---

### 2.4 Migration Runner

| 组件 | 文件 | 能力 |
|------|------|------|
| `adapters/postgres.Migrator` | `migrator.go` | goose v3 包装；支持嵌入式 FS；预检无效索引（`CREATE INDEX CONCURRENTLY` 中断场景）；Up/Down 幂等 |
| `adapters/postgres.VerifyExpectedVersion` | `schema_guard.go` | 启动时对比 DB 版本与二进制内嵌 FS 最大版本，版本不匹配 fail-fast（`ErrAdapterPGSchemaMismatch`） |

**Migration 序列共享**：`001-003、005`（outbox 表）是全局共享 migration，所有 Cell 共用。Cell 专属表（如 config_entries、audit_entries）追加各自的序号。

---

## 三、config-core 样板层（需参照实现，不能直接复用）

### 3.1 本地 DBTX 接口模式

```go
// cells/{cell}/internal/adapters/postgres/session.go
// 关键设计：Cell 的 repo 不 import adapters/postgres，
// 而是定义本地 DBTX 接口，由 session.go 把 pgx.Tx 和 pgxpool.Pool 适配进来。

type DBTX interface {
    Exec(ctx context.Context, sql string, args ...any) (int64, error)
    Query(ctx context.Context, sql string, args ...any) (Rows, error)
    QueryRow(ctx context.Context, sql string, args ...any) Row
}

type Session struct{ pool *pgxpool.Pool }

func (s *Session) resolve(ctx context.Context) DBTX {
    if tx, ok := persistence.TxFromContext(ctx); ok {
        return &dbtxAdapter{tx}  // 事务路径
    }
    return &poolAdapter{s.pool}  // 只读路径
}

func (s *Session) resolveWrite(ctx context.Context) (DBTX, error) {
    tx, ok := persistence.TxFromContext(ctx)
    if !ok {
        return nil, errcode.New(errcode.ErrAdapterPGNoTx, "write op requires tx")
    }
    return &dbtxAdapter{tx}, nil
}
```

**为什么这样设计**：Cell 的 repo 只依赖本地 DBTX 接口，不 import `adapters/postgres`，符合分层规则（cells 不依赖 adapters）。测试时可以直接注入 fake DBTX。

---

### 3.2 L2 原子写模式

```go
// service 层：domain write + outbox write 在同一事务
func (s *Service) CreateConfig(ctx context.Context, req CreateRequest) error {
    return s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
        entry, err := s.repo.Insert(txCtx, req)  // 写 config_entries
        if err != nil {
            return err
        }
        return s.outboxWriter.Write(txCtx, outbox.Entry{  // 写 outbox_entries（同一 tx）
            ID:        outbox.NewEntryID(),
            EventType: "event.config.changed.v1",
            Payload:   marshal(entry),
        })
    })
}
```

---

### 3.3 字段加密模式（仅需加密字段的 Cell 参考）

```go
// config-core 专属 AAD 构造
func AADForConfig(cellID, configKey string) []byte {
    return fmt.Appendf(nil, "cell:%s/key:%s", cellID, configKey)
}

// repo 内部 encryptValue / decryptValue
func (r *ConfigRepository) encryptValue(ctx, key, plaintext) (cipher, nonce, edk []byte, keyID string, err error) {
    aad := configcrypto.AADForConfig(cellID, key)
    return r.transformer.Encrypt(ctx, []byte(plaintext), aad)
}
```

**其他 Cell 如不需要字段加密**：跳过 `ValueTransformer` 注入，repo 直接存明文。

---

## 四、各 Cell 接入 PG 的工作量评估

### 4.1 现状快照

| Cell | PG 适配层 | Migration | 一致性级别 | 状态 |
|------|----------|-----------|----------|------|
| config-core | ✅ 完整（config_repo + flag_repo + 加密） | 001-010 | L2 | 已完成 |
| audit-core | ✅ AuditRepository（哈希链，无加密） | 无专属 migration（待补） | L1 | repo 有，migration 缺 |
| access-core | ❌ 无（纯内存） | 007（refresh_tokens 已有） | L2 | 待实现 |
| device-cell | ❌ 无（纯内存） | 无 | L4 | 待实现 |
| order-cell | ❌ 无（纯内存） | 无 | L2 | 待实现 |

---

### 4.2 access-core 接入 PG（最迫切）

**能直接复用**：Pool、TxManager、OutboxStore/Writer、Migrator、TxCtxKey、错误码

**需要新写**（预计 1.5-2 天）：

```
cells/access-core/internal/adapters/postgres/
├── session.go              照抄 config-core，换 import 路径
├── user_repo.go            实现 ports.UserRepository（users 表 CRUD）
├── session_repo.go         实现 ports.SessionRepository（refresh_tokens 表，migration 007 已有）
├── role_repo.go            实现 ports.RoleRepository（需新增 migration）
└── *_test.go               fakeDBTX 单元测试 + testcontainers 集成测试
```

**Migration 追加**：
```
011_create_users_table.sql          用户主表（已有内存模型可参照）
012_create_rbac_tables.sql          roles + user_roles 关联表
013_create_rbac_index.sql           CREATE INDEX CONCURRENTLY
```

**不需要加密**：密码已用 bcrypt 单向 hash，不走 `ValueTransformer`；session token 是 JWT 自描述，不加密存 PG。

---

### 4.3 audit-core 接入 PG（中等优先级）

**能直接复用**：同上

**需要新写**（预计 0.5-1 天）：
- `audit_entries` 表 migration（含 prev_hash、hash 列）
- `AuditRepository` 已有代码（`audit_repo.go`），但缺与 `TxManager` 的事务集成
- 哈希链完整性：`Append` 操作需在事务里保证 prev_hash 一致（防并发写破坏链）

**特殊挑战**：哈希链的 prev_hash 要求串行写入（不能并发 Append），需要 `SELECT FOR UPDATE` 或单写 goroutine 设计。

---

### 4.4 device-cell / order-cell（暂缓）

目前纯内存实现，无 PG 压力。待业务量增长或需要持久化时再接入，样板已有参考，工作量约 1-2 天 per cell。

---

## 五、已知缺陷与可改进点

### 5.1 4 列加密 schema 负担（中优先级）

每个加密字段需要 4 列：`value_cipher / value_nonce / value_edk / value_key_id`。
- **影响**：其他 Cell 若接入字段加密，schema 膨胀，migration 繁琐
- **改进方向**：参考 Tink 的 5 字节 ciphertext 前缀方案，将 4 列压缩为 1 列 `value_encrypted`（需 migration 改造，无向后兼容要求下可行）
- **当前决策**：保留 4 列，透明性强，便于 DBA 调试；等 S14a（AWS/GCP KMS）上线时一并评估切单列

### 5.2 缺少 Cell PG 接入标准 migration 模板（低优先级）

目前只有 config-core 的 migration 作为样板。新 Cell 需要自己判断列类型、索引策略、并发索引写法。
- **改进**：在 `docs/contributing/adapter-checklist.md`（R1e 产出）里补充 migration 编写规范，含 `CREATE INDEX CONCURRENTLY` 独立文件要求、up/down 对、默认值/NULL 规则。

### 5.3 audit-core 哈希链并发写缺乏框架支持（高优先级，接入 PG 时必须解决）

`HashChain.Append` 依赖前一条记录的 hash 作为 prevHash，并发写会破坏链。当前内存实现无问题，PG 实现时需要串行化写入。
- **方案**：`INSERT INTO audit_entries ... SELECT hash FROM audit_entries WHERE ... FOR UPDATE`（悲观锁）或单写 channel
- **当前状态**：还未实现，是 audit-core 接入 PG 的主要风险点

### 5.4 schema_guard 只检查版本号，不检查列结构（低优先级）

`VerifyExpectedVersion` 只对比 goose 版本号，不验证实际表结构是否与代码期望一致。理论上可能出现版本号匹配但 migration 被手动改过的情况。
- **改进**：可选的 schema checksum（参考 Flyway 的 checksum 验证），但对当前规模过重，暂不需要。

---

## 六、参考资源

- `docs/patterns/pg-cell-template.md`（728 行，PG 接入完整样板，含测试策略）
- `cells/config-core/internal/adapters/postgres/`（完整参考实现）
- `adapters/postgres/migrations/`（共享 migration 序列 001-010）

---

**结论**：框架层基础设施（连接池、事务、outbox、migration）是完整的、可直接复用的通用能力。Cell 层的工作量集中在 `session.go` + repo 实现 + migration 文件，有明确样板，per Cell 约 1-2 天。audit-core 的哈希链并发写是接入 PG 时唯一需要特别设计的非标准问题。

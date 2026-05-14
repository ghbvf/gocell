# PostgreSQL 迁移规范

本文件固化 GoCell PG 适配层的迁移编写约定，所有新 migration 必须遵守。

## 规则 1：CONCURRENTLY 的 migration 必须加 `-- +goose no transaction`

`CREATE INDEX CONCURRENTLY` 和 `DROP INDEX CONCURRENTLY` 不允许在事务内执行。
任何包含 CONCURRENTLY 语句的 migration 文件，**必须**在文件顶部添加：

```sql
-- +goose no transaction
```

已有示例：`004_create_config_entries_and_versions.sql`、`005_recreate_outbox_pending_concurrent.sql`。

## 规则 2：`no transaction` migration 的原子性边界

标记了 `-- +goose no transaction` 的文件，其中所有语句均在**无显式事务上下文**中逐条执行。
PostgreSQL 在此模式下：
- `CREATE INDEX CONCURRENTLY` / `DROP INDEX CONCURRENTLY`：**只能**在 no-transaction 模式执行
- `CREATE TABLE` / `ALTER TABLE` / DML：可混合在同一文件中（每条语句仍是原子的）
- `BEGIN` / `COMMIT` / `ROLLBACK`：**禁止**出现（goose 不会再包一层事务，显式事务语句会破坏 migration 生命周期）

混用 CREATE TABLE + CREATE INDEX CONCURRENTLY 是 **允许的模式**（见 `004_create_config_entries_and_versions.sql`：建表 + 建索引在同一文件），PostgreSQL 语义层面合法。

**但必须接受的取舍——无文件级原子性**：
- 若文件中第 N 条语句失败，前 N-1 条已生效，后续语句不再执行。goose 版本号**不推进**，但数据库处于"半迁移"状态。
- 作者必须保证每条语句都用 `IF NOT EXISTS` / `IF EXISTS` 幂等措辞，以便直接重跑 migration。
- 如果某步失败可能留下 INVALID 索引（见规则 5），重跑前需按规则 5 清理。

何时应拆分为两个 migration：
- 后一步依赖前一步的**事务性提交可见性**（罕见，大多数 DDL 可见性在 no-transaction 模式下也无问题）
- 需要显式 `BEGIN/COMMIT` 的批量 DML（则该 migration 应完全不含 CONCURRENTLY，走事务型文件）

## 规则 3：down 路径对称使用 `DROP INDEX CONCURRENTLY`

如果 up 路径使用 `CREATE INDEX CONCURRENTLY`，对应的 down 路径必须使用：

```sql
DROP INDEX CONCURRENTLY IF EXISTS <index_name>;
```

以保证回滚同样不阻塞写入，且支持 `IF EXISTS` 的幂等性。

## 规则 4：事务型 migration 首行建议 `SET LOCAL lock_timeout = '5s'`

不含 CONCURRENTLY 的事务型 migration（001/002/003 模式）应在 `-- +goose Up` 之后第一行写：

```sql
SET LOCAL lock_timeout = '5s';
```

这将访问排他锁（ACCESS EXCLUSIVE）的等待时间限制在 5 秒内，避免长时间阻塞生产写入。

## 规则 5：INVALID 索引 pre-check 与启动期防线

`CREATE INDEX CONCURRENTLY` 失败时，PostgreSQL 可能留下 `indisvalid = false` 的 INVALID 索引。
`CREATE INDEX CONCURRENTLY IF NOT EXISTS` 遇到 INVALID 残留会**静默跳过**，不重建。

GoCell 通过两道防线确保 INVALID 索引不会被静默跳过：

**第一道防线（migration 执行边界）**：`Migrator.Up` 在执行任何 migration 前调用
`DetectInvalidIndexes` pre-check：

- 发现 INVALID 索引 → 立即返回 error，**不推进版本**，要求人工清理后重试。
- 这遵循 pressly/goose migration workflow、Atlas lint gate 和 golang-migrate 的设计原则：
  在版本推进边界处验证前置条件，而不是在应用中途或启动期自愈。

**第二道防线（启动期 detect-and-warn）**：`cmd/corebundle/main.go` 的 postgres 分支
在 pool 创建后调用 `DetectInvalidIndexes`，以 `slog.Warn` 告警，**不中断启动**。
该防线覆盖 migration 被旁路工具绕过、并发 DDL 在 migration 之外留下残留等场景。

人工清理步骤：

```sql
DROP INDEX CONCURRENTLY <index_name>;
-- 然后重新跑 migration 或手动 CREATE INDEX CONCURRENTLY
```

**禁止**在 migration 文件中使用 `CREATE INDEX CONCURRENTLY IF NOT EXISTS` 而不通过
`Migrator.Up` 执行（否则绕过 pre-check 防线）。

## 规则 6：destructive forward migration 必须停 traffic + 写明 runbook

如果一个 migration 在 Up 路径上 DROP 一个被运行中旧二进制 INSERT/UPDATE 的列、表，或修改 NOT NULL/类型/外键
约束，使旧二进制写入立即失败，则该 migration 是 **destructive forward**。GoCell 不维持旧二进制
向后兼容（项目宪法："不向后兼容时不留软回退"），所以这类 migration 没有滚动部署窗口：

- 必须在文件顶部注释块写明 Up 部署 runbook：drain traffic → goose up → deploy 新二进制 → 恢复 traffic。
- 必须同时写明 Down 的 GUC（`gocell.allow_destructive_down=true`）与回退顺序。
- 必须加 `SET LOCAL lock_timeout = '5s'`（规则 4）。
- 如果未来运行时拓扑需要无停机滚动 DDL，按"两阶段 migration"拆：(a) 先放宽约束 / 让二进制停止写该列；
  (b) 等新二进制全量部署后再 DROP 列。当前 GoCell 仅 ship 自身，无外部 schema 消费方，单 PR + 计划停机更简单。

已有示例：`025_drop_sessions_authz_epoch_at_issue.sql`（S4b Batch 1C）。

## 参考

- [pressly/goose 官方文档](https://github.com/pressly/goose#transactions)：`-- +goose no transaction` 用法
- [pressly/goose Provider.Up](https://pkg.go.dev/github.com/pressly/goose/v3#Provider.Up)：migration workflow 边界
- [Atlas lint](https://atlasgo.io/versioned/lint)：在版本推进前做 lint/pre-check 的设计原则
- [golang-migrate Source](https://github.com/golang-migrate/migrate)：Source.Read 先验证再执行
- [PostgreSQL CREATE INDEX CONCURRENTLY](https://www.postgresql.org/docs/current/sql-createindex.html#SQL-CREATEINDEX-CONCURRENTLY)：限制与 INVALID 索引处理

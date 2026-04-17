# PostgreSQL 迁移规范

本文件固化 GoCell PG 适配层的迁移编写约定，所有新 migration 必须遵守。

## 规则 1：CONCURRENTLY 的 migration 必须加 `-- +goose no transaction`

`CREATE INDEX CONCURRENTLY` 和 `DROP INDEX CONCURRENTLY` 不允许在事务内执行。
任何包含 CONCURRENTLY 语句的 migration 文件，**必须**在文件顶部添加：

```sql
-- +goose no transaction
```

已有示例：`004_create_config_entries_and_versions.sql`、`005_recreate_outbox_pending_concurrent.sql`。

## 规则 2：`no transaction` migration 不能混用事务内和事务外语句

标记了 `-- +goose no transaction` 的文件，其中的语句会在无事务上下文执行。
**禁止**在同一文件中混合：
- `BEGIN` / `COMMIT` / `ROLLBACK` 等显式事务控制语句
- 既有 CONCURRENTLY 操作又有事务型 DDL（如 `CREATE TABLE`、`ALTER TABLE`）

若需两类操作，拆分为两个独立 migration 文件。

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

## 规则 5：INVALID 索引告警与处理

`CREATE INDEX CONCURRENTLY` 失败时，PostgreSQL 可能留下 `indisvalid = false` 的 INVALID 索引。
`CREATE INDEX CONCURRENTLY IF NOT EXISTS` 遇到 INVALID 残留会**静默跳过**，不重建。

- 启动期 `DetectInvalidIndexes` 会检测并以 `slog.Warn` 告警，**不自动清理**，需人工确认后执行：
  ```sql
  DROP INDEX CONCURRENTLY <index_name>;
  -- 然后重新跑 migration 或手动 CREATE INDEX CONCURRENTLY
  ```
- **禁止**在 migration 文件中使用 `CREATE INDEX CONCURRENTLY IF NOT EXISTS` 而不配套 INVALID 索引 pre-check。

## 参考

- [pressly/goose 官方文档](https://github.com/pressly/goose#transactions)：`-- +goose no transaction` 用法
- [PostgreSQL CREATE INDEX CONCURRENTLY](https://www.postgresql.org/docs/current/sql-createindex.html#SQL-CREATEINDEX-CONCURRENTLY)：限制与 INVALID 索引处理

# ADR-1122: MigratorPermit Typed Channel — Forward-Rebuild 门控从 GUC 迁移到 Go 类型系统

**Status**: Accepted
**Date**: 2026-05-31
**Issue**: #1248 (MigratorPermit typed channel)
**Related**: PR #1239 (#1228 round-3 派生 backlog), ADR-1042 (`docs/architecture/202605281200-1042-outbox-wire-envelope-principal-occurred-at.md`), gh #1335

## Context

### PR #1239 的 GUC 方案

PR #1239（ADR-1042 round-3）为 migration 012/043/044 的 forward-rebuild 操作引入了四个专属 PostgreSQL GUC 守卫：

- `gocell.allow_destructive_down` — destructive rollback 通用 GUC（已存在于更早的 PR）
- `gocell.allow_audit_rebuild` — 043 forward DROP+CREATE 专属
- `gocell.allow_outbox_rebuild` — 044 forward TRUNCATE 专属
- `gocell.allow_destructive_refresh_tokens_rebuild` — 012 forward TRUNCATE 专属

这是 SQL 层能做到的最强形态。每个 GUC 的 fail-closed 逻辑在 `-- +goose Up` 段中以 `DO $$ ... IF current_setting(...) <> 'true' THEN RAISE EXCEPTION ... END IF; $$` 块实现。

ADR-1042 §Decision B §"Hardness ceiling" 明确记录了该方案的局限：

> **Hardness ceiling**：goose SQL 只能读 GUC 字符串；typed `MigratorPermit` 是更 Hard 的形态但需要把 forward-rebuild 搬到 Go 侧（独立 backlog 跟踪 — #1248）。

GUC 方案的本质是**字符串约定**（Soft），原因如下：

1. **伪造可行性**：任何具有 migration 执行权限的 session 都可以在同一 session 内先 `SET gocell.allow_outbox_rebuild = 'true'` 再 `SELECT goose_migrate()`——GUC 无访问控制语义，仅是"操作员声明已知悉后果"的荣誉系统（honor system）。
2. **真值散落**：守卫逻辑分布在 ~30 个 SQL 文件中，与 Go 侧 `Migrator` API 存在语义断层，没有单一 Go-level enforcement 点可以机器守卫。
3. **GUC 拼写绕过**：自定义 GUC 名是字符串；AI 代码生成时打错 GUC 名或写错大小写，goose SQL 的 `current_setting` 读不到该 GUC 会返回空串而非报错，`<> 'true'` 仍为 true，导致静默放行（fail-open 分支）。

archtest `MIGRATION-DESTRUCTIVE-UP-REBUILD-GUC-01`（退役前）以正则匹配 SQL 文件内容来保证 GUC 守卫存在——这是 archtest §Medium（SQL 文本正则）而非 Hard。

### 对标：pressly/goose + Atlas

pressly/goose v3 的 `Provider.Up` 是纯 Go API，所有 migration 控制通过 Go option 传入，不依赖数据库侧 GUC 做业务决策。Atlas migrate engine 的 `LinCheck`/`VersionedMigration` lint gate 也在 Go 层前置检查，同样不在 SQL 体内做运行时 RAISE。GoCell 的 `Migrator` 封装 goose `Provider` 的调用点，本身是 Go 层——将 fail-closed 门移入 Go 层是架构上的自然位置。

## Decision

### 1. ForwardRebuildPermit sealed interface

新增 `ForwardRebuildPermit` interface（`adapters/postgres/migrator.go`）：

```go
type ForwardRebuildPermit interface {
    forwardRebuildPermit()   // unexported marker — 包外无法实现
    MigrationNumber() int64
    Reason() string
}
```

`AllowForwardRebuild(num int64, reason string) (ForwardRebuildPermit, error)` 是唯一构造器，强制 `num > 0` 且 `reason` 非空。包外代码无法实现 `forwardRebuildPermit()` marker，因此无法伪造一个 `ForwardRebuildPermit` 值——类型系统在编译期封闭授权。

`DestructiveDownPermit`（PR #1239 已引入）保留不变，`Down(ctx, permit)` 签名不改。

### 2. Migrator.ForwardRebuild + forwardRun 共享逻辑

```
Migrator.Up(ctx)                      → forwardRun(ctx, nil)
Migrator.ForwardRebuild(ctx, permits) → forwardRun(ctx, permits)
```

`forwardRun` 是 `m.provider.Up` 的唯一调用点（`MIGRATOR-PROVIDER-UP-CALLSITE-01` archtest 锁定），执行顺序：

1. **checkNoInvalidIndexes** — 检测 INVALID 索引，有则 fail-fast（延续 PR #1239 前置检查）
2. **gatePendingRebuilds（phase0）** — 解析所有 pending migration 的 `-- +gocell forward-rebuild target=<table>` 注解，逐个探针目标表
3. **m.provider.Up** — 真正执行 goose Up

`Down` 保持独立，仍直接调 `m.provider.Down`——destructive-down permit 逻辑不变。

### 3. phase0 gate 详细设计（data-aware，非粗暴拒绝）

phase0 不能无差别拒绝所有带 forward-rebuild 注解的 migration，原因在于 `Up()` 的四个调用方：

| 调用方 | 场景 |
|-------|------|
| `tools/pg-migrate/main.go` | CLI 工具，CI/CD fresh provision |
| `tests/testutil/pgshare` | 集成测试 fresh DB |
| `examples/iotdevice` | 示例项目 fresh provision |
| `examples/ssobff` | 示例项目 fresh provision |

这四个调用方都是 **fresh provision**（全新空库）。如果无差别拒绝所有 forward-rebuild migration，每次 fresh deploy 都会因为目标表是空的（甚至不存在）而失败——这是错误的。

phase0 的正确逻辑是 **data-aware**：

```
collectPendingForwardRebuilds()
  ↓ 枚举 version > currentDB 的 pending migration
  ↓ 读取各 SQL 的 +gocell forward-rebuild target=<table> 注解
  ↓ 返回 version→table map

requirePermitIfDangerous(version, target, permitByNum)
  ↓ tableHasRows(table)
      ├─ to_regclass($1) IS NOT NULL（两步探针第 1 步：表是否存在）
      └─ EXISTS(SELECT 1 FROM <ident>)（两步探针第 2 步：是否有行）
  ↓ 缺表 / 表空 → 放行（fresh deploy 自动通过）
  ↓ 非空 → 必须有 permitByNum[version] → 否则 fail-closed 报错
```

还有一个误配校验：调用方提供了一个 `permit` 但该 migration 实际不是 pending 的 forward-rebuild → 误配，`fail-fast` 报错，不静默忽略。

### 4. SQL 注解取代 GUC 守卫

删除全部 `DO $$ ... RAISE EXCEPTION ... END IF; $$` GUC 守卫块（涉及 012/043/044 及相关 GUC 设置）。Go phase0 承担门控职责后，SQL 体只需保留机器可读的注解：

```sql
-- +gocell forward-rebuild target=outbox_entries
```

注解格式 `-- +gocell forward-rebuild target=<identifier>` 由正则 `(?m)^\s*--\s*\+gocell\s+forward-rebuild\s+target=([a-zA-Z_][a-zA-Z0-9_]*)\s*$` 解析，identifier 部分经 `validateIdentifier` 防 SQL 注入（用于 `EXISTS(SELECT 1 FROM <ident>)` 查询，FROM 位置不能参数化）。

### 5. Archtest 退役与新增

| 变化 | Invariant ID | 说明 |
|------|-------------|------|
| 退役 | `MIGRATION-DESTRUCTIVE-DOWN-GUC-GUARD-01` | GUC 守卫已删，SQL 文本正则不再适用 |
| 退役 | `MIGRATION-DESTRUCTIVE-UP-REBUILD-GUC-01` | 同上，forward-rebuild GUC 整体退出 |
| 新增 | `MIGRATION-FORWARD-REBUILD-ANNOTATION-01` | 每个 Up 段含 TRUNCATE/DROP TABLE 的 migration 必须有 `-- +gocell forward-rebuild target=<table>` 注解；Medium（SQL 文本正则），runtime gate 是 Hard 后盾 |
| 新增 | `MIGRATION-NO-GUC-RESIDUE-01` | SQL 文件不得出现退役 GUC 名称残留（`gocell.allow_audit_rebuild` / `allow_outbox_rebuild` / `allow_destructive_refresh_tokens_rebuild` / `allow_destructive_down`）；Medium |
| 新增 | `MIGRATOR-PROVIDER-UP-CALLSITE-01` | `m.provider.Up` / `m.provider.Down` 在 `adapters/postgres/migrator.go` 中只允许出现在 `forwardRun` / `Down` 方法体内；funnel 下游 Medium（包内 archtest 锁）+ 上游包外 Hard（`provider` 字段 unexported，包外不可引用） |

`MIGRATOR-PROVIDER-UP-CALLSITE-01` 活在独立文件 `tools/archtest/migrator_permit_funnel_test.go`（单规则文件）。`MIGRATION-FORWARD-REBUILD-ANNOTATION-01` 与 `MIGRATION-NO-GUC-RESIDUE-01` 合并在 `tools/archtest/pg_schema_guard_invariants_test.go`（同主题 ≥ 3 规则共享文件）。

### 6. pg-migrate CLI 新增 -rebuild flag

```
tools/pg-migrate -rebuild "44:<reason>,12:<reason>"
```

格式 `<migrationNumber>:<reason>` 逗号分隔，解析为 `[]ForwardRebuildPermit` 后调 `migrator.ForwardRebuild(ctx, permits...)`。`-rebuild ""` 或不传时走 `migrator.Up(ctx)`（无 permit，fresh provision 路径）。

### 7. Migration 文件修改的 append-only 例外

`go-standards.md` 规则"Migration 文件只增不改"。本 PR 对 012/043/044（以及相关迁移）等**已提交**的 SQL 文件进行了编辑，删除了 GUC 守卫块。这是**具名例外**：

- **例外条件**（用户已批准）：pre-v1.0、gocell 无外部部署、无生产 DB。
- **操作安全性**：本次修改**不改变结果 schema shape**（DDL 语句不变，表结构相同），仅去掉 `DO $$ RAISE $$` runtime 门语义。等价于：在同一个 DB 上跑改前版本与改后版本，最终 schema 完全一致。
- **理由记录**：go-standards.md 的不可变原则保护「schema shape 不被意外改变」；删 GUC 守卫不触碰 schema shape，例外可辩护。

## data-aware gate：为何不能粗暴拒绝（详论）

这一设计决策值得单独展开，因为"看到 forward-rebuild 注解就拒绝"是看似 fail-closed 实为 fail-open 的反模式：

一个全新的 PostgreSQL 数据库，在跑 `goose up` 之前没有任何表。migration 044 的 forward-rebuild target 是 `outbox_entries`，而在全新库上该表尚未创建（它由更早的 migration 001 创建）。phase0 在执行时，migration 044 的 Up 尚未执行，目标表是否存在取决于 1-043 是否已 applied：

- 如果 1-043 已 applied（部分升级场景），`outbox_entries` 存在且有行 → 需要 permit。
- 如果库是全新的（fresh provision），1-043 还没跑，`outbox_entries` 不存在 → 探针 `to_regclass($1) IS NOT NULL` 返回 false → 安全放行。

两步探针设计（`to_regclass` + `EXISTS`）保证：

1. 缺表不报错（`to_regclass` 返回 NULL 而非 `ERROR: relation does not exist`）
2. 有表无行（空表）也视为安全——重建空表不丢数据

## 044 行为粗化说明

旧 SQL GUC 守卫对 `outbox_entries` 的判据是 `status <> 'published'`（只有未投递行才需审批）。新 Go 探针 `tableHasRows` 的判据是 `EXISTS(SELECT 1 FROM outbox_entries)`（任意行都需要 permit）。

这是**有意为之的粗化**（更保守、更 fail-closed）：

- 旧判据要求操作员精准理解 `status` 语义，新判据只看「表是否有行」，语义更简单。
- `published` 且未清理的行现在也需要 permit。这类行本质上是「等待 retention cleanup」的已投递历史行，理论上可以被 TRUNCATE 丢弃（没有在途投递风险），但 Go gate 统一处理为"有行就要审批"，避免操作员误判。
- 代价：在持有历史 outbox 行的库上运行 044 时，需要额外提供 permit，即便行都已 published。运维文档已同步更新（见 `docs/ops/migration-044-outbox-rebuild.md`）。

## AI-robust 评级

### 授权轴（permit 能否被伪造）

| | 原方案（GUC） | 新方案（typed permit） |
|--|-------------|----------------------|
| 包外伪造 | 任何 session 可 `SET gocell.allow_outbox_rebuild = 'true'` | `forwardRebuildPermit()` marker unexported，包外**编译不可实现** |
| AI 代码生成绕过 | 打错 GUC 名 → `current_setting` 返回空串 → 静默放行 | 不实现 marker interface → 编译报错 |
| 评级 | **Soft** | **Hard 上游**（type-system，Go compiler gate） |

### 执行轴（能否绕过 phase0 直接跑 rebuild）

| | 原方案（SQL RAISE） | 新方案（Go phase0） |
|--|-------------------|-------------------|
| 包外绕过 | 直连 psql 可以 SET GUC 再执行 SQL | `provider` 字段 unexported，包外不可引用 → **包外 Hard** |
| 包内绕过 | 仅 SQL 层强制，Go 层无约束 | `MIGRATOR-PROVIDER-UP-CALLSITE-01` archtest 锁 `forwardRun`/`Down` 为唯一 caller → **包内 Medium**（archtest CI 捕获，Go type system 无法表达） |
| 评级 | SQL RAISE = DB 引擎无条件强制（**Hard**，但为 SQL-layer Hard） | 包外 **Hard**（unexported field）+ 包内 **Medium**（archtest，永久天花板）；gh #1335 跟踪（won't-do，同 #851/#893/#1131 形态） |

### 诚实的档位迁移声明

新方案**不是**「SQL Soft → Go Hard」的全面升级，以下是精确的变化：

- **授权（permit 伪造）**：Soft → Hard（显著改善），GUC 字符串约定被 unexported marker interface 替代
- **执行（绕过 gate 跑 Up）**：SQL RAISE（DB 引擎 Hard）→ Go phase0（包外 Hard / 包内 Medium）

执行轴存在一个具体的向量降级：原来直连 psql 跑 `goose up` 时，SQL 体内的 RAISE 会在 DB 引擎层强制；现在 GUC 守卫已删除，直连 psql 可以无阻碍地执行 goose。

但该向量在 GoCell 架构下实为 moot（无效）：
1. GoCell 的 migration 嵌入 Go 二进制（`adapters/postgres/migrations` embed.FS），没有独立部署的 goose CLI
2. 没有 shipped goose CLI；CI/CD 唯一执行路径是 `tools/pg-migrate`（Go binary）
3. 直连 psql 执行 raw SQL 是运维红线操作，已由部署流程约束

因此该向量的防御后退**在当前架构下可接受**，但必须诚实记录。

### archtest 评级汇总

| Invariant | 评级 |
|-----------|------|
| `MIGRATION-FORWARD-REBUILD-ANNOTATION-01` | Medium（SQL 文本正则；runtime Go gate 是 Hard 后盾） |
| `MIGRATION-NO-GUC-RESIDUE-01` | Medium（SQL 文本正则） |
| `MIGRATOR-PROVIDER-UP-CALLSITE-01` | 包外 Hard（unexported `provider` 字段）+ 包内 Medium（archtest AST 锁，永久天花板 gh #1335） |

## 威胁矩阵（逐行重评）

以下表格是本 ADR 引入的变化对安全威胁面的完整评估，符合 ai-robust §"ADR amendment 落地必查"要求。

| 攻击向量 | 原防御（GUC 方案） | 新防御（typed permit 方案） | Δ | 补偿 |
|---------|-----------------|--------------------------|---|------|
| 伪造授权——无审批触发 forward-rebuild | GUC `SET` 字符串约定（Soft，任何具权限 session 可绕过） | `ForwardRebuildPermit` marker 不可伪造（type-system Hard） | **✅ 升** | — |
| 包外直接调用 `m.provider.Up` 绕过 phase0 | SQL RAISE（DB 引擎在 SQL 执行时强制） | `provider` 字段 unexported，包外引用编译报错（Hard） | **✅ 升**（包外） | — |
| 包内同包新增函数绕过 phase0 | SQL RAISE（不依赖调用方路径） | `MIGRATOR-PROVIDER-UP-CALLSITE-01` archtest（Medium） | **⚠️ 包内从 DB Hard → archtest Medium** | provider 字段 unexported + archtest CI + gh #1335 (won't-do，同 #851/#893/#1131 形态) |
| 直连 psql / goose CLI 绕过 Go gate | SQL RAISE EXCEPTION（DB 引擎强制） | GUC 守卫已删，裸 SQL 无 gate | **⚠️ 降** | GoCell migration 嵌入 Go 二进制，无 shipped goose CLI，唯一执行路径 = `tools/pg-migrate` Go binary；该向量在当前架构下 moot |
| GUC 名拼写错误导致静默放行（fail-open） | 存在（`current_setting` 读不到自定义 GUC 返回空串） | 不存在（Go 类型系统不接受错误名称） | **✅ 升** | — |
| fresh provision 误拒（空库无法 Up） | 不存在（GUC 仅在表有非 published 行时 RAISE） | data-aware phase0 探针：缺表 / 空表 → 放行 | **✅ 等价** | `tableHasRows` 两步探针 + 集成测试覆盖 |

## Consequences

**Positive**：

- forward-rebuild 授权从 SQL 字符串约定（Soft）升级到 Go type-system（Hard），伪造不可表达
- GUC 守卫散落 ~30 SQL 文件的真值分布问题彻底消除
- `MIGRATOR-PROVIDER-UP-CALLSITE-01` archtest 在机器层锁定 `provider.Up` 的唯一调用点
- `tools/pg-migrate -rebuild "<num>:<reason>"` 提供类型安全的运维入口

**Negative / Known limitations**：

- 直连 psql 运行时无 DB 层 gate（见威胁矩阵 ⚠️ 降格行），当前架构下可接受但已记录
- 包内 Medium 天花板（`MIGRATOR-PROVIDER-UP-CALLSITE-01` 包内上游）无 Go 类型系统闭环路径；gh #1335 记录（won't-do，与 #851/#893/#1131 同形态）
- 044 判据从"未投递行"粗化到"任意行"——有历史 published 行的库也需要 permit；运维步骤已更新

## References

- 实施：issue #1248（本 PR）
- 前驱 GUC 方案：PR #1239（#1228 round-3），ADR-1042 §"Hardness ceiling" 段
- ADR-1042：`docs/architecture/202605281200-1042-outbox-wire-envelope-principal-occurred-at.md`（§Amendment 2026-05-31 同步更新）
- 044 运维 runbook：`docs/ops/migration-044-outbox-rebuild.md`（同 PR 更新）
- archtest 实现：
  - `tools/archtest/migrator_permit_funnel_test.go` — `MIGRATOR-PROVIDER-UP-CALLSITE-01`
  - `tools/archtest/pg_schema_guard_invariants_test.go` — `MIGRATION-FORWARD-REBUILD-ANNOTATION-01` / `MIGRATION-NO-GUC-RESIDUE-01`
- gh #1335 — `MIGRATOR-PROVIDER-UP-CALLSITE-01` 包内上游 Hard 化（won't-do，同 #851/#893/#1131）
- 参考框架：pressly/goose v3 `Provider.Up` API；Atlas `LinCheck` migrate lint gate
- AI-robust 治理章程：`.claude/rules/gocell/ai-robust.md` §Hard 范本目录 "typed marker funnel for unbounded ops" + "sealed construction"

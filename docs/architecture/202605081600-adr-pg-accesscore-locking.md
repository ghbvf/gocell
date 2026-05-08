# ADR：PG AccessCore 并发控制策略矩阵

- **编号**：202605081600
- **状态**：已接受（Accepted）
- **作者**：PR #417 W6
- **日期**：2026-05-08

---

## Context（背景）

PR #417 为 `accesscore` 引入了三处针对具体写热路径的并发控制机制，涵盖：

1. **users 表**：多 pod 场景下并发 `UpdateProfile / Lock / Unlock / RotatePassword` 可能造成丢写（last-writer-wins 覆盖）。
2. **sessions 创建路径**：`Login` 读取用户行后加锁，防止并发登录在同一事务窗口拿到不一致的用户快照。
3. **role_assignments revoke 路径**：`RemoveFromUserIfNotLast` 必须保证至少一个 admin 保留。系统允许多个 admin，但高并发撤权下需要串行化"计数后删除"窗口。

在这三条路径上，可选方案包括：

- **SERIALIZABLE 隔离级别全局升级**：理论上覆盖所有并发冲突，但代价极高（整个连接级别的强序列化、冲突时大量 retry、长事务阻塞）。
- **悲观锁（SELECT FOR UPDATE）**：行级锁，适合"读后写"场景；锁随事务释放，不跨请求持有。
- **乐观并发（Version CAS）**：适合"写频率不极高但需要冲突检测"的场景；不持锁，冲突时调用方重试或返回 409。
- **pg_advisory_xact_lock**：应用定义的逻辑锁，适合跨多行、针对特定业务对象（如"一个 role 下的撤权操作"）做串行化。

本 ADR 记录每条路径的选择依据，形成可查阅的决策矩阵，避免未来维护者在相似场景重走相同分析路径。

---

## Decision（决策）

### 策略 1：Version CAS（users 表）

**对应 migration**：`022_users_add_version.sql`

在 `users` 表新增 `version BIGINT NOT NULL DEFAULT 1`，每次 UPDATE 采用：

```sql
UPDATE users
SET    field = $2, version = version + 1
WHERE  id = $1 AND version = $3
```

`RowsAffected = 0` 时返回 `ErrAuthConcurrentUpdate`（HTTP 409），调用方决定是否重试。

**选择理由**：

- `users` 的各字段由不同操作"拥有"（admin 写 `locked_at`，用户自己写 `profile_fields`，密码重置写 `password_hash`），冲突概率低但需检测。
- CAS 不持锁，读-写窗口短，在低冲突场景下吞吐优于悲观锁。
- 与 K8s apimachinery `resourceVersion` 字段语义对齐：调用方拿到当前版本 → mutate → 提交 patch，版本不匹配即冲突。
- 每个 PATCH 方法只写自己"拥有"的字段，admin-lock 与用户 self-update 撞同一字段在结构上已近乎不可能，CAS 作为额外兜底层。

**ref**: kubernetes/apimachinery `pkg/api/meta/v1/types.go` `ResourceVersion` 字段注释；ory/kratos `persistence/sql/persister.go` 的 `version` 字段 UPDATE 模式。

---

### 策略 2：SELECT FOR UPDATE 行锁（sessions 创建路径）

**对应代码**：`cells/accesscore/slices/sessionlogin/service.go` `Login` 方法内 `RunInTx + GetByIDForUpdate`

在 `Login` 事务内，密码校验后以 `SELECT ... FOR UPDATE` 重新拿住 `users` 行，再复验 username、状态、密码 hash，读取角色并写入 `sessions`：

```sql
SELECT id, status, locked_at, ...
FROM   users
WHERE  id = $1
FOR UPDATE
```

**选择理由**：

- 读后写窗口需要锁定外部参考（用户行状态）：如果两个并发 `Login` 同时读到"用户正常"、再同时写 session，可能一个在读后、写前 `users` 行被 admin lock，导致新 session 创建在一个已 locked 账户下。
- 锁随事务提交/回滚即释放，不跨请求持有，影响范围最小。
- `sessionlogin` 是高频路径，但通常同一用户并发登录请求极少（token 多设备 refresh 才是高频）；行锁代价可接受。
- 不选 CAS：`sessions` 写入是纯 INSERT，无"冲突版本"语义；FOR UPDATE 用于锁定被读的 `users` 行，而非 `sessions` 行。

**ref**: ory/kratos `persistence/sql/persister_session.go` `CreateSession`：同样在事务内先 `GetSession` 锁定用户，再写 session；PostgreSQL 文档 §13.3.2 Explicit Locking。

---

### 策略 3：pg_advisory_xact_lock（admin revoke / first-run setup 路径）

**对应代码**：`cells/accesscore/internal/adapters/postgres/role_repo.go` `RemoveFromUserIfNotLast` / `LockAdminProvision`

在 `RemoveFromUserIfNotLast` 的事务开头调用：

```sql
SELECT pg_advisory_xact_lock(hashtextextended('role:' || $1, 0))
```

`$1` 为 `role_id`（admin 路径使用 `'admin'`）。同 role 下的并发 revoke 请求在此串行排队；事务提交后，下一个请求才会进入临界区并重新检查 "至少还有一个 admin"。First-run admin provisioning 使用独立的 `accesscore:adminprovision` advisory key，把跨 pod 的首次 setup 请求串行化。

**选择理由**：

- **safety-critical aggregate**：admin 语义是"至少保留一个"，不是"只能有一个"。两个并发 revoke 都读到"还有 2 个 admin"时，如果不串行化，它们可能各自删除一个 assignment，最终留下 0 个 admin。
- **FOR UPDATE 不适用**：无法单行锁住"整个 role 下所有 assignments 的聚合状态"；需要锁住的是逻辑对象（role），而非某一行。
- **advisory lock 范围精准**：`hashtextextended` 将 `'role:admin'` 哈希为 int64 key，同 role 的并发操作串行化，不同 role 的操作并行执行；锁随事务结束自动释放，无泄漏风险。
- **不选 SERIALIZABLE**：会将连接升级为全局冲突检测，所有读写均参与序列化环，代价不成比例（整个 accesscore 事务都被卷入），且需要完整 retry helper。

**ref**: ory/keto `internal/persistence/sql/advisory_lock.go`：以 `pg_advisory_xact_lock` 串行化 relation-write 路径；PostgreSQL 文档 §9.27.10 Advisory Lock Functions。

---

## 为什么不用 SERIALIZABLE 全局升级

SERIALIZABLE 隔离级别在理论上覆盖所有写冲突，但存在以下代价：

1. **retry helper 必须完整**：serialization failure（`40001`）需要调用方检测并重试整个事务。目前 GoCell 的 `TxRunner` 无内置重试，贸然升级会将 retry 责任暴露给所有业务层。
2. **吞吐退化**：PG SERIALIZABLE 在读-写冲突检测上比 READ COMMITTED 有明显 CPU 和锁追踪开销，影响所有路径，而我们只需在三条热路径上精准控制。
3. **粒度不匹配**：每条路径需要的是不同范围的串行化（单行版本 / 读者-写者联锁 / 逻辑对象级锁），SERIALIZABLE 在"太宽"的范围序列化，副作用不透明。

结论：选择"按路径选最小范围的控制机制"，而非"全局升级隔离级别"。

---

## Consequences（影响）

### 正面

- 每条热路径的并发控制范围最小化，不影响无关路径的吞吐。
- Version CAS 给调用方提供可观测的冲突信号（409 + `ErrAuthConcurrentUpdate`）。
- Advisory lock 精准串行化 role-level 操作，PG 自动释放，无泄漏风险。
- 三种机制各有充分的业界对照（K8s / ory/keto / ory/kratos），后续维护者可溯源。

### 约束

- **BUILTIN-ROLE-ID-NAME-EQ-01 archtest**（`tools/archtest/builtin_role_invariants_test.go`）锁定了 `runtime/auth.RoleAdmin = "admin"` 这一隐式约定。如果未来引入 id≠name 的 builtin role，必须同时更新 `019_roles.sql` / PG role repo 中 admin holder 计数与 advisory-lock key，并放宽对应断言。
- **Version 字段由 migration 引入**：已有数据行 `version` 默认为 1；首次 PATCH 后递增至 2，客户端若缓存了旧 `version` 需重新 GET。
- **Advisory lock key 冲突概率**：`hashtextextended` 产生 int64 空间，极低概率碰撞；如果未来 role 种类超过数十种，建议加前缀区分 namespace（当前 `'role:'` 前缀已足够）。
- **FOR UPDATE 持锁时长**：`Login` 事务内 FOR UPDATE 锁住 `users` 行直到事务结束。如果事务内有网络 I/O（未来扩展），持锁时长可能延长，需监控 `pg_stat_activity` 中的锁等待。

---

## 关联

- `adapters/postgres/migrations/021_sessions_fk.sql`：sessions FK + CASCADE
- `adapters/postgres/migrations/022_users_add_version.sql`：users version CAS 字段
- `adapters/postgres/migrations/019_roles.sql`：roles + role_assignments + role_id lookup index
- `tools/archtest/builtin_role_invariants_test.go`：BUILTIN-ROLE-ID-NAME-EQ-01
- PR #417 review P2c（admin 保底不变量绑定分析）、P2f（文档同步）

---

## 参考

- kubernetes/apimachinery `pkg/api/meta/v1/types.go` — `ResourceVersion` 乐观并发设计
- ory/keto `internal/persistence/sql/advisory_lock.go` — advisory lock 串行化 relation 写路径
- ory/kratos `persistence/sql/persister_session.go` — FOR UPDATE 锁住用户行再写 session
- PostgreSQL 文档 §13.3 Explicit Locking；§9.27.10 Advisory Lock Functions

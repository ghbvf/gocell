# PG / accesscore / auditcore / configcore 待办问题清单

**生成日期**: 2026-05-08
**来源**: 整理自 `docs/backlog.md` + `docs/plans/202605071200-033-pg-implementation-plan.md` + `docs/reviews/202605082044-pr417-pg-corecell-framework-analysis.md`
**用途**: 快速查阅，不重复 backlog 详情

---

## C. accesscore

| ID | 优先级 | 一句话 |
|---|---|---|
| B2-C-02 SETUP-ADMIN-PUBLIC-ROUTE-PERMANENT | 🔴 P0 | setup 端点常驻 Public，需移到 `/internal/v1/setup/` |
| ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01 | 🔴 P1 | sessionlogin 无失败累计 + 阈值 + auto-lock |
| CELLS-IDENTITYMANAGE-LEVEL-MISLABEL-01 | 🔴 Cx1 | 标 L0 实为 L1 |
| B5-FU-PG-RUNTIME-WIRING-AND-ARCHTEST-TYPE-AWARE-01 | 🟠 P1 | corebundle 仍 `WithInMemoryDefaults` + archtest 类型化 |
| PR338-FU-LOGIN-DURABLE-TX-ATOMICITY-TEST | 🟠 | 卡 PG session repo |
| PR392-FU-AUDIT-CHAIN-WIRING | 🟠 P2 | onAuthFail 用 slog 未接 audit chain |
| P3-TD-10 TOCTOU 竞态修复 | 🟠 P2 | 卡 PG session repo + Redis distlock |
| B2-PROVISIONER-MUTEX-REVIEW | 🟠 P2 | PG adapter 落地后审视 mutex 是否仍需 |
| B2-T-02 RBACASSIGN event contract waiver expiry | 🟠 P1 | waiver 到期前补真实 contract |
| B2-T-07-FU-1 RBACASSIGN caller wiring | 🟠 | production wiring 启动后接入 |
| B2-C-06 SessionLogout consumer action 无验证 | 🟡 P1 | 加 action enum 校验 |
| PR280-FU1 CHANGEPASSWORD-CONCURRENT-SEMANTICS-01 | 🟡 P2 | 旧密码校验在 RunInTx 之外，并发改密无 CAS |
| PR267-FU-AUTHTEST-INTERNAL | 🟡 | testHelpers 内部化 |
| PR250-F3 Event wire byte pinning | 🟡 | 缺 byte 级回归 |
| X5 P3-TD-11 accesscore domain 拆分 | 🟡 P3 | User/Session/Role 拆分（卡 X1） |
| X13 REFRESH-PARTITION-01 | 🟠 P3 | `expires_at` range 分区，触发条件未达 |

## D. auditcore

| ID | 优先级 | 一句话 |
|---|---|---|
| B2-C-01 Audit hashchain 重启未恢复尾节点 | 🔴 P0 | NewHashChain 启动从空链开始；多实例/重启后尾哈希不连续 |
| AUDITAPPEND-L2-FAILURE-PROOF-01 | 🟡 P1 | testcontainer fail outbox writer 验证事务真回滚 |
| B2-C-05 Auditappend actor 缺失降级不安全 | 🟡 P1 | actor 缺失静默降级，需 fail-closed |
| B2-C-09 Auditquery raw payload 直接回传 | 🟡 P1 | handler 含敏感字段，需 redact |
| B2-C-10 Auditappend 全局 mutex 串行化 13 topic | 🟡 P1 | 容量/吞吐压力出现时 |
| B2-C-14 Hash-chain 跨重启连续性测试缺 | 🟡 P2 | testcontainer 重启回归 |
| C-DC9 auditarchive 死代码靶子打通 | 🟡 P2 | S3 adapter 已就绪但中间业务层漏接 |
| PR266-AUDITAPPEND-STRICT | 🟡 P2 | strict 模式 toggle 待第一个 strict-audit 客户 |
| CELLS-SLICE-MULTI-VERB-DECOMPOSE-01（auditappend） | 🟡 P1 | auditappend 14 contractUsages 拆分 |

## E. configcore

| ID | 优先级 | 一句话 |
|---|---|---|
| B2-T-01 Config rollback 乐观锁缺 | 🟡 P1 | 加版本号（与 P3-TD-12 同根源） |
| P3-TD-12 configpublish.Rollback 版本校验 | 🟠 P2 | 卡 post-v1.0 + 持久化版本管理 |
| CONFIGCORE-CACHE-LIFECYCLE-OWNER-01 | 🟠 Cx2 | 内存增长信号 |
| C-02 CONFIGSUBSCRIBE-CACHE-LIFECYCLE | 🟡 P1 | 进程内无界 + 未挂 Lifecycle |
| B2-C-11 Configsubscribe tombstone 无 TTL | 🟡 P2 | 永久保留导致内存膨胀 |
| CONFIGCORE-RECEIVE-PLACEHOLDER-CLEANUP-01 | 🟡 P1 | 删 `accesscore/configreceive` 占位 |
| PR-CFG-A-DEFER-2 ConfigCore L2 divergence | 🟡 Cx1 | L2 与 L1 表项 schema 偏差 |
| C-05 CELLS-CELLROUTES-PLACEHOLDER-DELETE | 🟡 P2 | 直接删 `configcore/cell_routes.go` |
| PR320-FU-CONFIGCORE-CI-NOOP | 🟡 P3 | noop publisher CI 路径未覆盖 |
| PR-CFG-G1-FU6 | 🟡 Cx2 | 余项 |
| PR238-FU4 CONFIGREPO-LEGACY-NOTFOUND-TEST-DEDUP-01 | 🟡 P3 | mutation-test 误导 |
| PR238-FU8 CONFIGREPO-UPDATE-ROLLBACK-OP-LABEL-TEST-01 | 🟡 P3 | InternalMessage op 断言缺失 |
| CELLS-SLICE-MULTI-VERB-DECOMPOSE-01（configread） | 🟡 P1 | configread 双 listener 拆分 |

## F. 横切（≥2 cell 或 PG 通用）

| ID | 优先级 | 一句话 |
|---|---|---|
| A-01 OIDC-FAILFAST-MR-COMPLETENESS（含 A-07/A-08） | 🔴 P0 | 4 adapter 实现 Checkers + postgres.Pool 升 ManagedResource |
| ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01 | 🟠 P1 | postgres/redis/s3 错误分 transient/permanent |
| ADAPTER-CONNECT-BUDGET-01 | 🟡 P1 | adapter 级 ConnectTimeout（PG 部分由 PR#401 已覆盖） |
| REPO-HEALTHCHECKER-01 | 🟡 P1 | configcore + auditcore repo 接 HealthCheckers |
| B2-R-02 Readyz 缺少 repo probe | 🟡 P1 | configcore + auditcore 仅接 outbox |
| ADAPTER-MANAGED-RESOURCE-COMPLETENESS-01 | 🟡 Cx2 | 部分 adapter 缺 ready probe |
| B2-X-03 PG invalid index warn continue | 🟡 P2 | 改 fail-fast |
| B2-A-13 PG pool tx rollback 日志泄漏 | 🟡 P2 | 走 `pkg/redaction` |
| PR-V1-PG-STARTUP-HARDEN-FU-RACE-COVERAGE | 🟡 P2 | test-race.yml 加 adapters/postgres |
| B2-C-13 L2 跨层 e2e 回归不足 | 🟡 P2 | setup → audit → config 跨 cell e2e |
| C-04 CELLS-INIT-TEMPLATE-CONVERGE（含 C-07） | 🟡 P2 | 3 cell Init 切分各异 + emitter health probe helper |
| C-09 CELL-SPLIT-LAYOUT-NORMALIZE | 🟡 P2 | accesscore + configcore 三文件范式不一致 |
| M1-OBSERVED HEALTHZ-INTERFACE-PACKAGE-01 | 🟡 P2 | 38 处 Health 实现分散 |

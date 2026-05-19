# PG / accesscore / auditcore / configcore 未完成清单

**原始生成日期**: 2026-05-08
**最近精简**: 2026-05-18（4 轮亲眼核查后归档 39 条闭环 → 仅保留未完成 12 条；PR-CFG-G1-FU6 经 PR #463 FMT-21 PATH-ID-MAPPING coverage RECYCLE 闭环，从清单移除）
**完整闭环快照**: `docs/plans/archive/202605181530-pg-corecell-issues-closure-snapshot.md`
**来源**: 整理自 `docs/backlog.md` + `docs/plans/archive/202605071200-033-pg-implementation-plan.md` + `docs/reviews/202605082044-pr417-pg-corecell-framework-analysis.md`

---

## C. accesscore（含 auditcore 共用）

| ID | 优先级 | 一句话 |
|---|---|---|
| ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01 | 🔴 P1 | sessionlogin 无失败累计 + 阈值 + auto-lock；`domain.StatusLocked` 框架存在但 schema (`failed_login_count` / `locked_at`) + 业务逻辑全缺 |
| PR392-FU-AUDIT-CHAIN-WIRING | 🟠 P2 待合同 | event.session.auth-failed.v1 schema 未发布；`cells/auditcore/slices/auditappendsession/service.go:9-10` stub 占位待 contract 落地后补充订阅 |
| B2-PROVISIONER-MUTEX-REVIEW | 🟠 P2 | setup 已加 cross-process `setupLock` advisory；provisioner.go:71-77 内部 sync.Mutex 必要性待 review |
| PR250-F3 Event wire byte pinning | 🟡 | 缺 byte 级 golden 回归 |
| X5 P3-TD-11 accesscore domain 拆分 | 🟡 P3 | User/Session/Role 拆分（卡 X1） |
| X13 REFRESH-PARTITION-01 | 🟠 P3 | `expires_at` range 分区，触发条件未达（生产流量阈值） |

## E. configcore

| ID | 优先级 | 一句话 |
|---|---|---|
| ~~B2-T-01 Config rollback 乐观锁缺~~ | ✅ closed | 实施侧 PR S6（service `expectedVersion` + PG SQL `WHERE version=$N` + handler 409 + mem 并发单元测试）；PG SQL Medium runtime regression guard PR-V11-CONFIG-ROLLBACK-OPTLOCK（029 D5, PR #583）；Hard 升级 backlog cap-14 `CONFIG-ROLLBACK-CAS-HARD-UPGRADE-01` |
| ~~P3-TD-12 configpublish.Rollback 版本校验~~ | ✅ closed | 同 B2-T-01（同根源） |
| CONFIGCORE-CACHE-LIFECYCLE-OWNER-01 | 🟠 Cx2 | 内存增长信号 |
| C-02 CONFIGSUBSCRIBE-CACHE-LIFECYCLE | 🟡 P1 | 进程内无界 + 未挂 Lifecycle |
| B2-C-11 Configsubscribe tombstone 无 TTL | 🟡 P2 | 永久保留导致内存膨胀 |
| CONFIGCORE-RECEIVE-PLACEHOLDER-CLEANUP-01 | 🟠 P2 | **ID 名误导**：实际是「业务 reload 接入骨架已就位，等业务触发」。configreceive/service.go 已完整 ship event decode + ConfigGetter HTTP refetch + 错误分类 + DLQ + metrics（~10h 骨架），cell wiring 与 contract subscribers 真实就位；2026-05-10 激进自审撤回「直接删除」主方案；触发条件: 业务侧 JWT TTL hot-reload / key rotation 需求 |
| PR-CFG-A-DEFER-2 ConfigCore L2 divergence | 🟡 Cx1 | L2 与 L1 表项 schema 偏差 |
| PR320-FU-CONFIGCORE-CI-NOOP | 🟡 P3 | noop publisher CI 路径未覆盖 |
| PR238-FU8 CONFIGREPO-UPDATE-ROLLBACK-OP-LABEL-TEST-01 | 🟠 P3 | 部分修复：PR#553 抽 opUpdate/opUpdateForRollback const + Update_NotFound 双向 NotContains（Medium）；Hard 升级 typed enum 跟 `CONFIGREPO-OP-LABEL-TYPED-ENUM-HARD-01` |

## F. 横切（≥2 cell 或 PG 通用）

| ID | 优先级 | 一句话 |
|---|---|---|
| C-04 CELLS-INIT-TEMPLATE-CONVERGE（含 C-07） | 🟡 P2 | 3 cell Init 切分各异：auditcore 已有 `registerHealthProbes` helper，accesscore + configcore 未提取 |
| C-09 CELL-SPLIT-LAYOUT-NORMALIZE | 🟡 P2 | accesscore 比 configcore/auditcore 多 `mem/` `configgetter/` `refresh_gc.go` `refresh_policy.go` 等顶层文件，三 cell 命名层次不一致 |

---

## 建议处理顺序

**P0/P1 红旗**
1. **ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01**（P1）— 完整设计 + schema migration（3-5 日）+ journey 测试（2 日）；无 spec/ADR 起点
2. **PR392-FU-AUDIT-CHAIN-WIRING**（P2 待合同）— 依赖 `event.session.auth-failed.v1` contract 设计先行（accesscore 失败登录事件 schema）；contract 落地后审计消费接入 ~2 日

**P2 优化**
3. **B2-PROVISIONER-MUTEX-REVIEW** — review-only task；PG advisory lock 落地后 sync.Mutex 多半冗余，删除验证 1 日
4. **C-04 / C-09**（layout 收敛）— 一次性 refactor，提取 `registerHealthProbes` helper + 命名归一 2-3 日
5. **PR238-FU8** Hard upgrade — `opUpdate` 字面量 → typed enum 1 日

**P3 长尾 / deferred**
- PR250-F3 byte pinning、PR-CFG-A-DEFER-2、PR320 noop CI — 按 P3 优先级；触发条件达成时处理
- X5 domain 拆分（卡 X1）、X13 refresh partition（触发流量阈值未达）— 真正 deferred

## 不要做的项

- **CONFIGCORE-RECEIVE-PLACEHOLDER-CLEANUP-01** ID 名带 "PLACEHOLDER" "CLEANUP" 都**严重误导**：service.go 已 ship 完整业务 reload 接入骨架（event decode + ConfigGetter HTTP refetch + 401/403/404/transient 四叉路错误分类 + DLQ + metrics），cell wiring 与 contract subscribers 真实就位。删除会撞三处真值源：(1) ADV-05 治理（active event 必须有 subscriber）；(2) `event.config.entry-upserted.v1` contract.yaml `subscribers: [accesscore, auditcore, configcore]`；(3) 业务侧重做 ~10h。按 "占位 log → 业务 reload" 演化，不按 "清理" 演化。backlog.md:193 2026-05-10 激进自审已撤回「直接删除」主方案。service.go:30 注释中 "placeholder per ADV-05" 是 stale 表述（理由已升级为"业务骨架等触发"），下次触碰时同 PR 修正。

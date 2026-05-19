# PG / accesscore / auditcore / configcore 未完成清单

> **🗄️ 已归档（2026-05-19）**：剩余 9 条未完成项全部在 active backlog（`backlog.md` cap-01 + `cap-13` + `cap-14`）有 canonical 落点，任务台账已被 active backlog 单源覆盖。本文件作历史脉络保存，不再更新；后续追踪以 active backlog 为准。

**原始生成日期**: 2026-05-08
**最近精简**: 2026-05-19（看代码逐条核查：删 6 条已闭环 — ACCOUNT-LOCKOUT (#585) / CACHE-LIFECYCLE-OWNER (won't-do) / C-02 (#518) / B2-C-11 (#518) / PR320-FU-NOOP (`service_test.go:39` 已覆盖) / PR238-FU8 主项 (config_repo_test.go 双向锁就位)；剩 9 条未完成）
**上一次精简**: 2026-05-18（4 轮亲眼核查后归档 39 条闭环 → 保留 12 条；PR-CFG-G1-FU6 经 PR #463 FMT-21 PATH-ID-MAPPING coverage RECYCLE 闭环）
**完整闭环快照**: `docs/plans/archive/202605181530-pg-corecell-issues-closure-snapshot.md`
**来源**: 整理自 `docs/backlog.md` + `docs/plans/archive/202605071200-033-pg-implementation-plan.md` + `docs/reviews/202605082044-pr417-pg-corecell-framework-analysis.md`

---

## C. accesscore（含 auditcore 共用）

| ID | 优先级 | 一句话 |
|---|---|---|
| PR392-FU-AUDIT-CHAIN-WIRING | 🟠 P2 待合同 | event.session.auth-failed.v1 schema 未发布；`cells/auditcore/slices/auditappendsession/service.go:9-10` stub 占位待 contract 落地后补充订阅 |
| B2-PROVISIONER-MUTEX-REVIEW | 🟠 P2 | setup 已加 cross-process `setupLock` advisory；provisioner.go:71-77 内部 sync.Mutex 必要性待 review |
| PR250-F3 Event wire byte pinning | 🟡 | 缺 byte 级 golden 回归 |
| X5 P3-TD-11 accesscore domain 拆分 | 🟡 P3 | User/Session/Role 拆分（卡 X1） |
| X13 REFRESH-PARTITION-01 | 🟠 P3 | `expires_at` range 分区，触发条件未达（生产流量阈值） |

## E. configcore

| ID | 优先级 | 一句话 |
|---|---|---|
| CONFIGCORE-RECEIVE-PLACEHOLDER-CLEANUP-01 | 🟠 P2 | **ID 名误导**：实际是「业务 reload 接入骨架已就位，等业务触发」。configreceive/service.go 已完整 ship event decode + ConfigGetter HTTP refetch + 错误分类 + DLQ + metrics（~10h 骨架），cell wiring 与 contract subscribers 真实就位；2026-05-10 激进自审撤回「直接删除」主方案；触发条件: 业务侧 JWT TTL hot-reload / key rotation 需求 |
| PR-CFG-A-DEFER-2 ConfigCore L2 divergence | 🟡 Cx1 | L2 与 L1 表项 schema 偏差（2026-05-19 复核：grep `cells/configcore/` 无 L2 divergence 匹配证据，描述疑过期，需进一步核实）|

## F. 横切（≥2 cell 或 PG 通用）

| ID | 优先级 | 一句话 |
|---|---|---|
| C-04 CELLS-INIT-TEMPLATE-CONVERGE（含 C-07） | 🟡 P2 | 3 cell Init 切分各异：auditcore 已有 `registerHealthProbes` helper，accesscore + configcore 未提取 |
| C-09 CELL-SPLIT-LAYOUT-NORMALIZE | 🟡 P2 | **2026-05-19 重新校准**：原 `cell_routes.go` 命名 + `RegisterSubscriptions` 错位载体经 codegen 迁移已消失，原"依赖 K-07"也已脱钩。残留真问题：(a) 三 cell 切分形态不一致 — accesscore(`cell_init.go + cell_providers.go + refresh_*.go`) / auditcore(单 `cell.go`) / configcore(`cell_init.go + cell_lifecycle.go`)；(b) `ensureCursorCodec` pure helper 仍在 `cells/configcore/cell_init.go:166-169` 而非独立 helpers 文件；(c) accesscore 无 `cell_lifecycle.go` / `cell_helpers.go`，pure helper 与 lifecycle hook 混在 cell_init.go |

---

## 建议处理顺序

**P2 等触发**
1. **PR392-FU-AUDIT-CHAIN-WIRING** — 依赖 `event.session.auth-failed.v1` contract 设计先行（accesscore 失败登录事件 schema）；contract 落地后审计消费接入 ~2 日
2. **CONFIGCORE-RECEIVE-PLACEHOLDER-CLEANUP-01** — 等业务侧 JWT TTL hot-reload / key rotation 真实需求；不要走「清理」路线

**P2 可立做**
3. **B2-PROVISIONER-MUTEX-REVIEW** — review-only task；PG advisory lock 落地后 sync.Mutex 多半冗余，删除验证 1 日
4. **C-04** — accesscore + configcore 抽 `registerHealthProbes` helper（auditcore 已抽，照搬即可）~1 日
5. **C-09** — 三件套：(a) accesscore 新增 `cell_lifecycle.go` + `cell_helpers.go`，把 cell_init.go 里 lifecycle hook 和 pure helper 分文件；(b) configcore `ensureCursorCodec` 迁到 `cell_helpers.go`；(c) auditcore 单 `cell.go` 是否要切分待评估（slice 数少，分歧可接受）。2-3 日；与 K-07 已脱钩，无前置依赖

**P3 长尾 / deferred**
- PR250-F3 byte pinning、PR-CFG-A-DEFER-2 — 按 P3 优先级；触发条件达成或复核出实际证据后处理
- X5 domain 拆分（卡 X1）、X13 refresh partition（触发流量阈值未达）— 真正 deferred

## 不要做的项

- **CONFIGCORE-RECEIVE-PLACEHOLDER-CLEANUP-01** ID 名带 "PLACEHOLDER" "CLEANUP" 都**严重误导**：service.go 已 ship 完整业务 reload 接入骨架（event decode + ConfigGetter HTTP refetch + 401/403/404/transient 四叉路错误分类 + DLQ + metrics），cell wiring 与 contract subscribers 真实就位。删除会撞三处真值源：(1) ADV-05 治理（active event 必须有 subscriber）；(2) `event.config.entry-upserted.v1` contract.yaml `subscribers: [accesscore, auditcore, configcore]`；(3) 业务侧重做 ~10h。按 "占位 log → 业务 reload" 演化，不按 "清理" 演化。backlog.md:193 2026-05-10 激进自审已撤回「直接删除」主方案。service.go:30 注释中 "placeholder per ADV-05" 是 stale 表述（理由已升级为"业务骨架等触发"），下次触碰时同 PR 修正。

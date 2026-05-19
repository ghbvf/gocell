# 043 archtest 审计 × 框架能力缺口 — 并行实施计划（冲突域 × 依赖 × ROI）

**生成日期**：2026-05-19
**真值源**（只引指针，不重述结论）：[`042 archtest 审计`](../reviews/202605181109-042-archtest-six-agent-audit.md) · [`004 缺口分析`](framework-capability-gaps/202605131500-004-capability-gap-analysis.md) · [`005 roadmap`](framework-capability-gaps/202605162100-005-framework-capability-roadmap-plan.md)

本文把上述待办按文件冲突域划 lane、ROI 排序，给出可并行调度。每 lane 立项各起 PR 级实施计划 + ADR。

---

## 1. 冲突域（并行划分键）

| 冲突域 | 核心文件/包 |
|---|---|
| D-ARCHTEST | `tools/archtest/*`（含 façade） |
| D-OBHDR | `kernel/outbox` Entry.Headers + `Emit` + HandleResult |
| D-CELLHEALTH | `kernel/cell` health 注册面 + `cell.yaml` deps |
| D-AUTHREVOKE | `session.Store`/`refresh.Store`/`UserRepository` + Invalidator |
| D-ERRCODE | `pkg/errcode` |
| D-CODEGEN | `tools/codegen` + `contract.yaml` schema |
| D-TXLIFE | `adapters/postgres` tx + `kernel/lifecycle` |
| D-OBSV | observability 反查 + audit ledger + redaction sink |
| D-CLOCK | `adapters/clock` + 全局 `time.Now` ban |
| D-HTTPMW | `runtime/http/middleware` chain |

**两处同冲突域碰撞**：

- **D-OBHDR**：W0 信封 与 042§4#4（HandleResult unexport）同改 `kernel/outbox` → 串行，W0 先。
- **D-CELLHEALTH**：005[7]（health dep 声明）与 042§4#1（ProbeName sealed）同改 health 注册面 → 合并单 Wave，一次重写。

---

## 2. 工作项（ROI 排序）

| ROI | 工作项 | 来源 | 冲突域 | 依赖 |
|:-:|---|---|---|---|
| #0 | 4 组 silent-carryover Soft 登记 backlog 升级 / godoc 显式 accept | 042§3a | 无代码域 | 无 |
| #1 | W0 wire envelope | 005 | D-OBHDR | 无 |
| #1 | credential-invalidate sealed fenceToken（顺带闭 §3a-credential 真修） | 042§4#2 | D-AUTHREVOKE | 无 |
| #2 | after-commit hook | 005[5] | D-TXLIFE | 无 |
| #2 | ProbeName sealed + health dependency 声明（合并） | 042§4#1 + 005[7] | D-CELLHEALTH | 无 |
| #2 | errcode sealed PublicDetail/InternalDetail | 042§4#3 | D-ERRCODE | 无 |
| #3 | in-proc contract | 005[3] | D-CODEGEN | 无 |
| #3 | HandleResult 字段全 unexport | 042§4#4 | D-OBHDR | W0 |
| #4 | HTTP 幂等 | 005[1] | D-HTTPMW | [5] |
| #4 | sink 侧脱敏（SafeSpan + slog middleware） | 042§4#5 | D-OBSV | 无 |
| #5 | sub-rule 塌缩 + theme 归并 + 命令式 boundary 转 depguard | 042§2a/b/c | D-ARCHTEST | rule 族避让 |
| #5 | façade 收缩 | 042§4#6 | D-ARCHTEST | §2 整合 |
| #6 | schema reg / command bus / 反查 / outbound / authz↔contract | 005[6/4/11/27/22] | 各 | 依赖链 |
| #6 | contractspec authority token / buf 式 contract breaking gate | 042§4#9/§3c | D-CODEGEN | [3] |
| #7 | typed clock funnel / identitymanage lastAdminGuard 必填 | 042§4#7/#8 | D-CLOCK / cells | 无 |
| #8 | secret rotation / migration↔deploy / 第二三批余项 | 005[20/25/…] | 各 | — |

ROI 依据：042§3a 是 ai-collab 章程硬性（silent carryover 违章），零代码 → #0；042§4#2/#1/#3 "一次类型收口消灭多条 Medium/Soft" → 最高 ROI；004§难度分布"改 wire envelope = 最高 ROI 区" → W0 #1。

**休眠**：005 W6 Saga / W10 projection — 补偿原语 / 超时语义 / 声明 vs 编程式边界设计张力未收敛，scope 未知，先独立 ADR，禁凭猜。

---

## 3. 并行调度

**t0（域两两不相交，零依赖）— 7 lane 全并行**：
①§3a 合规登记（无代码域）②W0 ③fenceToken ④[5] after-commit ⑤ProbeName+[7] ⑥errcode sealed ⑦[3] in-proc

**t1（t0 ship 后解锁）**：
- W0 ship → [11] 反查（D-OBSV）/ [27] outbound（D-OBHDR）/ [6] schema reg（需 header 冻结 + ⑦释放 D-CODEGEN）/ §4#4 HandleResult（同 D-OBHDR lane 紧随）
- [5] ship → [1] HTTP 幂等（D-HTTPMW）
- fenceToken ship → 关闭 §3a-credential P0 条目
- 容量空档 → §2 整合（**避开正被收口的 rule 族**：W0 改 outbox 时不并 OUTBOX 族；ProbeName 改 health 时不并 CELL-REPO-READYZ）

**t2**：
- [1] ship → [4] command bus（D-CODEGEN）
- §2 整合 ship → §4#6 façade 收缩（D-ARCHTEST）
- 空档 → §4#5 sink 脱敏 / §4#7 clock / §4#8 identitymanage / [22] / [25][20]

---

## 4. 纪律

1. 单 lane = 单 Wave = 单实施计划 + 单 ADR。
2. 冲突域互斥：同域同时仅一个 in-flight PR；"释放后解锁"必须等前序 merge。
3. §2 整合 rule-族避让：不碰正被其它 lane 收口的 rule 族（OUTBOX/HEALTH/CELL-REPO-READYZ/credential/DETAILS），否则 golden/ID 双改冲突。
4. funnel 类（042§4#1/#2/#3/#4/#9 + 005 D-CODEGEN/D-OBHDR）立项须给上游 Hard / 下游 Hard 两栏评级，仅一侧 Hard 不立项。
5. 不留软回退：sealed token / W0 envelope / schema check 禁 `${VAR:-default}`、禁 double-write、禁前置 status 探针。

---

## 5. 下一步

t0 七 lane 各起 Wave 实施计划 + ADR。任一 lane ship 后按 §3 t1/t2 解锁后继。新能力项（005 W0–W10 来源）优先级低于 #0 合规与 #1/#2 高 ROI 收口，reviewer 容量空档穿插。

> 本计划仅落编排，未做代码改动。

# 043 archtest 审计 × 框架能力缺口 — 双流并行实施计划（依赖 × 冲突域 × ROI）

**生成日期**：2026-05-19
**性质**：执行编排层。把**两条独立真值流**合并为单一并行计划，按文件冲突域划 lane、ROI 跨流统一排序。每 lane 立项时各自起 PR 级实施计划 + ADR。
**真值源**（禁止重述结论，只引指针）：
- **流 B**：[`042 archtest 六-agent 审计`](../reviews/202605181109-042-archtest-six-agent-audit.md) — archtest enforcement 全量审计；§3a P0 合规 / §4 ROI 收口 / §2 整合 / §3 Soft 升级范式
- **流 A**：[`004 缺口分析`](framework-capability-gaps/202605131500-004-capability-gap-analysis.md) + [`005 roadmap`](framework-capability-gaps/202605162100-005-framework-capability-roadmap-plan.md) — 35 框架能力缺口 / W0–W10 / 依赖图

> 本文只回答：两条流的待办按冲突域去碰撞后，哪些可同时跑、按什么 ROI 序启动。不重排 W 编号、不重述缺口收益、不重述 042 评级。

---

## 0. 双流性质差异（决定 DG 门作用域）

| | 流 B（042） | 流 A（005） |
|---|---|---|
| 内容 | archtest enforcement 自身的合规/收口/整合 | 新增框架能力抽象（W0–W10） |
| 是否受 DG-0 约束 | **否**。§3a P0 是 ai-collab **章程义务**（与做广/做深无关）；§4 funnel 是高 ROI 做深，ai-collab 鼓励 | **是**。新能力是"做广"，受激活裁决 |

**DG-0′（替换 005 原版伪二选一，修上一版 043 的不一致）**：
DG-0 只管**流 A 的激活与优先级位次**，不是"做广 OR 做深"二选一，更不令流 B 休眠。

- 流 B §3a P0：**无条件立即**（现行章程违章，零代码）。
- 流 B §4/§2：高 ROI 做深，**不需 DG-0**，按 ROI 排。
- 流 A W0–W10：待 architect/product 答 **DG-0′ = 同意激活 / 暂缓 / 调位次**（不是休眠全停）。

---

## 1. 冲突域定义（并行划分键，跨流统一）

| 冲突域 | 核心文件/包 | 涉及条目 |
|---|---|---|
| **D-ARCHTEST** | `tools/archtest/*`（含 façade） | 042 §2a/§2b/§2c/§4#6 |
| **D-OBHDR** | `kernel/outbox` Entry.Headers + `Emit` + HandleResult | 005 W0[10/11/23]、042 §4#4 **← 跨流碰撞** |
| **D-CELLHEALTH** | `kernel/cell` health 注册面 + `cell.yaml` deps | 005 W1[7]、042 §4#1 **← 跨流碰撞** |
| **D-AUTHREVOKE** | `session.Store`/`refresh.Store`/`UserRepository` + Invalidator | 042 §3a-credential + §4#2（同问题两成熟度） |
| **D-ERRCODE** | `pkg/errcode` | 042 §4#3 |
| **D-CODEGEN** | `tools/codegen` + `contract.yaml` schema | 005 [3/4/6/22]、042 §4#9、§3c |
| **D-TXLIFE** | `adapters/postgres` tx + `kernel/lifecycle` | 005 [5/26/32]、042 §4#8(局部) |
| **D-OBSV** | observability 反查 + audit ledger + redaction sink | 005 [11]、042 §4#5 |
| **D-CLOCK** | `adapters/clock` + 全局 `time.Now` ban | 042 §4#7（与 005 [23] 信封协同） |

**两处必须协同/串行的跨流碰撞**（上一版 043 完全没看到）：

- **D-OBHDR**：005 W0（Entry.Headers 信封）与 042 §4#4（HandleResult 字段全 unexport）同改 `kernel/outbox`。**串行**：W0 先（ROI #1，解锁近半数缺口），§4#4 紧随同 lane（机械退役 2 archtest）。
- **D-CELLHEALTH**：005 [7]（health dependency 声明，改 cell.yaml + runtime health 计算）与 042 §4#1（`reg.Health`→`ProbeName` sealed，改注册 API 签名）同改 health 注册面。**合并为单一 Wave**：一次性重写 health 注册面 = ProbeName 收口 + dependency 声明，不能各开 PR。

---

## 2. 跨流 ROI 阶梯（高 ROI 优先，受依赖/合规约束）

```
#0  042 §3a P0 合规           ── 零代码、无冲突域、章程违章 → 立即，并行所有
#1  042 §4#2 fenceToken       ── 安全最高 ROI；封吊销模型唯一结构开口（顺带闭 §3a-credential）
#1  005 W0 wire envelope      ── 流 A 最高杠杆，解锁近半数缺口
#2  042 §4#1 + 005 [7] 合并   ── health 面一次收口（ProbeName Hard + dep 声明）
#2  042 §4#3 errcode sealed   ── 两 Medium → 一 Hard 类型，独立无碰撞
#2  005 [5] after-commit      ── 链根，解锁 [1]→[4]
#3  042 §4#4 HandleResult     ── 紧随 W0 同 D-OBHDR lane（机械）
#3  005 [3] in-proc contract  ── D-CODEGEN，独立线
#4  005 [1] HTTP 幂等         ── 依赖 [5]；最高频痛点
#4  042 §4#5 sink 脱敏        ── 删 4 条 call-site redaction Soft
#5  042 §2a/§2b/§2c 整合      ── 机械、低风险、D-ARCHTEST 隔离
#5  042 §4#6 façade 收缩      ── 随 §2 后（同域）
#6  005 [6] schema reg / [4] command bus / [11] 反查 / [22] / [27]
#7  042 §4#7 clock / §4#8 identitymanage / §4#9 contractspec / §3c buf-gate
#8  005 [25]/[20] · 第二三批余项 ── backlog 候选池
休眠 005 W6 Saga / W10        ── scope 未知，设计张力 ADR 未收敛，禁凭猜
```

依据：042 §3a 是章程硬性（ai-collab Review checklist，silent carryover 违章）→ 绝对 #0；042 §4#2/#1/#3 是"一次类型收口消灭多条 Medium/Soft"→ 流 B 最高 ROI；004 §难度分布"改 wire envelope = 最高 ROI 区"→ W0 流 A #1。

---

## 3. 并行调度（冲突域不相交 → 同时跑）

### t0 — 7 lane 全并行（域两两不相交，依赖满足）

| Lane | 内容 | 冲突域 | ROI |
|:-:|---|---|---|
| **P0** | 042 §3a：4 组 silent-carryover Soft 登记 backlog 升级条目 / godoc 显式 accept | docs+godoc（无代码域） | #0 合规 |
| **A** | 005 W0 envelope → 042 §4#4 HandleResult unexport（同 lane 串行） | D-OBHDR | #1 → #3 |
| **B** | 005 [5] after-commit | D-TXLIFE | #2 |
| **C** | 042 §4#1 ProbeName + 005 [7] health dep（合并单 Wave 重写 health 面） | D-CELLHEALTH | #2 |
| **D** | 042 §4#2 credential fenceToken（顺带闭 P0 §3a-credential 真修） | D-AUTHREVOKE | #1 安全 |
| **E** | 042 §4#3 errcode sealed PublicDetail/InternalDetail | D-ERRCODE | #2 |
| **F** | 005 [3] in-proc contract → 042 §4#9 contractspec authority token（同 lane 串行） | D-CODEGEN | #3 → #7 |

7 域两两不相交 → 真并行。P0 无代码域，可叠加任意 reviewer 容量空档先清（章程违章不等）。

### t1 — t0 lane ship 后解锁

| 触发 | 解锁 | 冲突域 | 备注 |
|---|---|---|---|
| A ship（W0+#4） | 005 [11] 反查 / [27] outbound / [6] schema reg | D-OBSV / D-OBHDR / D-CODEGEN | [6] 需 header 冻结 + F 释放 |
| B ship（[5]） | 005 [1] HTTP 幂等 | D-HTTPMW | 最高频痛点 |
| D ship（fenceToken） | 关闭 §3a-credential P0 backlog 条目 | — | 同问题真修完成 |
| F 释放 D-CODEGEN | 042 §3c buf 式 contract breaking gate | D-CODEGEN | 补 §3c 覆盖缺口 |
| 任意空档 | 042 §2a/§2b/§2c 整合（**避开正被其它 lane 改的 rule 族**：A 在改 outbox 时不并 OUTBOX 族；C 在改 health 时不并 CELL-REPO-READYZ） | D-ARCHTEST | 机械、隔离 |

### t2 — 链条续推

| 触发 | 内容 | 冲突域 |
|---|---|---|
| [1] ship | 005 [4] command bus | D-CODEGEN |
| §2 整合 ship | 042 §4#6 façade 收缩 | D-ARCHTEST |
| 独立空档 | 042 §4#5 sink 脱敏（与 [11] 协调 D-OBSV）/ §4#7 clock（与 W0 [23] 协同 D-CLOCK）/ §4#8 identitymanage | D-OBSV / D-CLOCK / cells |
| W0/W1 ship | 005 [22] authz↔contract / auth→audit / [26][32] / [25][20] | D-CODEGEN / D-OBSV / D-TXLIFE |

### 休眠（不入调度）

- 005 **W6 [2] Saga** / **W10 projection**：补偿原语 / 超时语义 / 声明 vs 编程式边界设计张力未收敛 → 先独立 ADR，**禁凭猜先做**（005 §纪律 3，PR #445 反模式）。

---

## 4. 推进纪律

1. **单 lane = 单 Wave = 单实施计划 + 单 ADR**；本文件不替代 Wave 级计划。
2. **冲突域互斥硬约束**：同一冲突域同时只允许一个 in-flight PR；t1/t2"释放后解锁"必须等前序 merge，不抢先开分支。
3. **§2 整合的 rule-族避让**：D-ARCHTEST 整合不得碰正被其它 lane 收口的 rule 族（A→OUTBOX、C→HEALTH/CELL-REPO-READYZ、D→credential、E→DETAILS/MESSAGE），否则 golden/ID 双改冲突。
4. **DG-1 评级前置**：042 §4 funnel 类（#1/#2/#3/#4/#9）+ 005 D-CODEGEN/D-OBHDR funnel 立项须给上游 Hard / 下游 Hard 两栏评级，仅一侧 Hard 不立项。
5. **不留软回退**：W0 envelope / W4 transport bind / W5 schema check / 042 sealed token 禁 `${VAR:-default}`、禁 double-write、禁前置 status 探针。
6. **scope 未知保持休眠**（W6/W10）。

---

## 5. 下一步

- [ ] **流 B 立即可启（不待 DG-0）**：Lane P0（§3a 章程违章，零代码）+ Lane D（§4#2 fenceToken，安全最高 ROI）+ Lane C/E（§4#1+[7] / §4#3）各起实施计划。
- [ ] **流 A 待 DG-0′ 裁决**（architect/product）：同意激活 → Lane A（W0）/B（[5]）/F（[3]）启动；暂缓 → 流 A 转 backlog，流 B 不受影响照常推进；调位次 → 重排 §2 阶梯流 A 行。
- [ ] 任一 t0 lane ship → 按 §3 t1/t2 表解锁后继。

> 本计划仅落编排，未做任何代码改动。两条真值流均以指针引用，未重述其结论。

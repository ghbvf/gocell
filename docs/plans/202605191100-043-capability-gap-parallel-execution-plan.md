# 043 框架能力缺口并行实施计划（依赖序 × 冲突域 × ROI）

**生成日期**：2026-05-19
**性质**：执行编排层。把 [`005 roadmap`](framework-capability-gaps/202605162100-005-framework-capability-roadmap-plan.md) 的 W0–W10 拆成**按文件冲突域划分的可并行 lane**，ROI 高者先行。每 lane 单 Wave 仍各自起 PR 级实施计划 + ADR。
**真值源**（禁止在本文重述结论，只引指针）：
- [`004 缺口分析`](framework-capability-gaps/202605131500-004-capability-gap-analysis.md) — 35 缺口 / 依赖图 / 难度分布 / 落地优先级
- [`005 roadmap`](framework-capability-gaps/202605162100-005-framework-capability-roadmap-plan.md) — W0–W10 编排 / 立项门 DG-0/1/2 / 推进纪律

> 本文不重排 W 编号、不重述缺口收益。只回答一个问题：**哪些 Wave 因冲突域不相交可同时跑，按什么 ROI 序启动。**

---

## 0. 编排前置（继承 005，不复述）

- **DG-0 未裁决前本计划休眠**：005 §0 根门。DG-0=做广 → 本计划生效；DG-0=做深 → 整体转 backlog。
- **DG-1 AI-rebust / DG-2 DDD 边界**：每 lane 立 Wave 实施计划时按 005 §0 自证，本文不替代。
- **scope 未知项不入并行池**：W6 Saga / W10 Projection 在设计张力 ADR 收敛前是 A 类 blocker，**不排 lane、不凭猜**（005 §推进纪律 3）。

---

## 1. 冲突域定义（并行的划分键）

冲突域 = 两个 PR 若同时改动会产生**合并冲突或语义耦合**的文件/包域。lane 间冲突域不相交 = 可真并行。

| 冲突域 | 核心文件/包 | 性质 |
|---|---|---|
| **D-OBHDR** | `kernel/outbox` Entry.Headers + `Emit` 签名 | **热共享根**——W0 在此，近半数缺口下游依赖 |
| **D-CODEGEN** | `tools/codegen/*` + `contract.yaml` schema + 模板 | funnel 单源，内部需串行 |
| **D-CELLMETA** | `cell.yaml`/`assembly.yaml` schema + `kernel/cell` + `kernel/governance` validate | 声明面，内部需串行 |
| **D-HTTPMW** | `runtime/http/middleware` chain + 装载位序 | 中间件位序敏感 |
| **D-TXLIFE** | `adapters/postgres` tx_manager + `kernel/lifecycle`/drain | 事务 + 生命周期 |
| **D-OBSV** | observability 反查 API + `runtime/audit/ledger` 注入 | W0 信封下游 |
| **D-CRYPTO** | `kernel/crypto` Rotatable | 独立窄域 |

---

## 2. 缺口 → 冲突域 + 依赖（源自 004 依赖图）

| Wave | 缺口 | 冲突域 | 上游依赖 | scope |
|:-:|---|---|---|---|
| W0 | 10/11-inject/23 wire envelope | D-OBHDR | **无** | ✅ |
| W1 | 5 after-commit | D-TXLIFE | **无**（004 依赖图链根） | ✅ |
| W1 | 7 health dependency | D-CELLMETA | **无**（独立线） | ✅ |
| W1 | 11 观测反查 API | D-OBSV | W0（信封） | ✅ |
| W2 | 1 HTTP 幂等 | D-HTTPMW | 缺口 5 | ✅ |
| W3 | 4 command bus | D-CODEGEN | 缺口 1 | ✅ |
| W4 | 3 in-proc contract | D-CODEGEN | **无**（独立 codegen 线） | ✅ |
| W5 | 6 schema registry | D-CODEGEN + D-OBHDR | W0（header 冻结） | ✅ |
| W5 | 15 version skew | D-CELLMETA | W4 | ✅ |
| W7 | 22 authz↔contract | D-CODEGEN | W0/W1 | ✅ |
| W7 | auth→audit middleware | D-OBSV | W0/W1 | ✅ |
| W8 | 27 outbound HTTP | D-OBHDR | W0 | ✅ |
| W8 | 26 drain / 32 restart-safe | D-TXLIFE | W0 | ✅ |
| W9 | 25 migration↔deploy | D-TXLIFE | — | ✅ |
| W9 | 20 secret rotation | D-CRYPTO | — | ✅ |
| W6 | 2 L3 Saga | (kernel/workflow NEW) | W1–W4 | ❌ **休眠** |
| W10 | projection/replay | — | W5+W6 | ❌ **休眠** |

> 第二批/第三批余项（8/9/12/13/14/16/17/18/19/21/24/28/31/34/35）按 005 §第二批/第三批余项归 **backlog 候选池**，不入固定 lane，由对应 Wave 顺带或业务信号触发单独立项。

---

## 3. 并行调度（冲突域不相交 → 同时跑）

### t0 — 4 lane 全并行（零依赖 + 冲突域两两不相交）

| Lane | Wave | 冲突域 | ROI 依据（004 指针） |
|:-:|---|---|---|
| **A** | **W0** wire envelope | D-OBHDR | §强枢纽节点：解锁近半数缺口，**最高杠杆，ROI #1** |
| **B** | **[5]** after-commit | D-TXLIFE | 依赖链根，解锁 [1]→[4]；004 §W1 立即受益 |
| **C** | **[7]** health dependency | D-CELLMETA | 短周期、立即受益；独立线无阻塞 |
| **D** | **[3]** in-proc contract | D-CODEGEN | 解决跨 cell 调用合规缺口；独立 codegen 线，可不等 W0 |

四者文件域 `kernel/outbox` ⊥ `adapters/postgres` ⊥ `cell.yaml` ⊥ `tools/codegen`，**真并行，4 reviewer 同时**。

### t1 — A 与 B 各自 ship 后解锁

| 触发 | 解锁 lane | Wave | 冲突域 | 备注 |
|---|---|---|---|---|
| A=W0 ✅ | E | **[11]** 观测反查 | D-OBSV | 需信封；D-OBHDR 已空出 |
| A=W0 ✅ | F | **[27]** outbound | D-OBHDR | W0 后 D-OBHDR 可重入 |
| A=W0 ✅ + D=[3] ✅ | G | **[6]** schema registry | D-CODEGEN+D-OBHDR | 需 header 冻结 + D-CODEGEN 空出 |
| B=[5] ✅ | H | **[1]** HTTP 幂等 | D-HTTPMW | 004 §W2 最高频痛点 |

t1 可并行：E(D-OBSV) ∥ F(D-OBHDR) ∥ H(D-HTTPMW) ∥ C/D 续跑。G 须等 D 释放 D-CODEGEN。

### t2 — 链条续推

| 触发 | Wave | 冲突域 |
|---|---|---|
| H=[1] ✅ | **[4]** command bus | D-CODEGEN（须 D/G 释放） |
| W0/W1 ✅ | **[22]** authz↔contract / auth→audit | D-CODEGEN / D-OBSV |
| W0 ✅ | **[26]/[32]** drain/restart-safe | D-TXLIFE（B 释放后） |
| 独立 | **[25]/[20]** migration / rotation | D-TXLIFE / D-CRYPTO |

### 休眠（不入调度）

- **W6 [2] Saga**：补偿原语/超时语义/声明 vs 编程式边界设计张力未收敛 → 先独立 ADR，**禁止凭猜先做**。
- **W10 projection/replay**：依赖 W6，W6 收敛后重评。

---

## 4. ROI 启动序（高 ROI 优先，受依赖约束）

```
ROI #1  W0 envelope            ── t0 立即（解锁近半数，动 1 处接管多缺口）
ROI #2  [5] after-commit       ── t0 立即（链根，短周期）
ROI #3  [7] health dep         ── t0 立即（短、即受益、独立）
ROI #3  [3] in-proc contract   ── t0 立即（合规缺口、独立 codegen 线）
ROI #4  [1] HTTP 幂等          ── B ship 后（最高频痛点）
ROI #5  [11] 观测反查          ── W0 ship 后（排障基础）
ROI #5  [6] schema registry    ── W0+[3] 后（约定→机制 Hard 升级）
ROI #6  [4] command bus        ── [1] 后（打通 CQRS 写侧）
ROI #7  [22]/[27]/[26]/[32]    ── W0/W1 后
ROI #8  [25]/[20]              ── 独立，容量空档
后置    第二/三批余项          ── backlog 候选池，业务信号触发
休眠    W6 [2] / W10           ── 设计张力 ADR 收敛前不动
```

004 §难度分布对齐：**改 wire envelope（~8 缺口）= 最高 ROI 区**故 W0 绝对第一；**新建 framework 抽象（~10）= 最大投入区**（[1][2][4]）排在依赖满足后；**DX 工具链（~3）= 最易拖延**进 backlog 候选池。

---

## 5. 推进纪律（继承 005 §2，本文只补并行特有项）

1. **单 lane = 单 Wave = 单实施计划 + 单 ADR**：本文件不替代 Wave 级计划。
2. **冲突域互斥是硬约束**：同一冲突域同时只允许一个 in-flight PR；t1/t2 的"释放后解锁"必须等前序 PR merge，不抢先开分支。
3. **scope 未知保持休眠**：W6/W10 不入池（005 §纪律 3，PR #445 反模式）。
4. **DG-1 评级前置**：D-CODEGEN / D-OBHDR funnel 类 Wave 立项须给上游 Hard / 下游 Hard 两栏评级，仅一侧 Hard 不立项。
5. **不留软回退**：W0 envelope / W4 transport bind / W5 schema check 禁 `${VAR:-default}` 回退、禁 double-write 新旧序列、禁前置 status 探针。

---

## 6. 下一步（待 DG-0 裁决，不自动推进）

- [ ] **DG-0 裁决**（architect/product）：做广→本计划生效；做深→转 backlog。
- [ ] DG-0=做广 → t0 四 lane 各起 Wave 实施计划 + ADR：W0 envelope / [5] after-commit / [7] health-dep / [3] in-proc contract。
- [ ] t0 lane 中任一 ship → 按 §3 t1/t2 表解锁后继 lane。

> 本计划仅落编排，未做任何代码改动。
